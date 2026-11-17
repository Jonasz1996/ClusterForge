// Package agentconf bepaalt op welk adres agents de server bereiken en zet
// agents om naar een nieuw adres (agentprotocol 47): het installcommando en
// nieuwe aanmeldingen gebruiken standaard het interne adres uit
// CF_NATS_ADVERTISE, niet de naam achter een reverse proxy. Omzetten gaat als
// taak agents.reconfigure, per cluster één node tegelijk; de agent test het
// nieuwe adres eerst en zet het oude terug als hij er de server niet mee
// bereikt.
package agentconf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak.
const Kind = "agents.reconfigure"

// Waar een adres vandaan komt.
const (
	// SourceSetting: ingesteld bij Instellingen.
	SourceSetting = "setting"
	// SourceAdvertise: afgeleid van CF_NATS_ADVERTISE, het interne adres.
	SourceAdvertise = "advertise"
	// SourceRequest: het adres waarmee de browser of de agent de server
	// aanspreekt; achter een reverse proxy is dat de naam van de proxy.
	SourceRequest = "request"
)

// Toestand van een agent tegenover de adressen van de server.
const (
	StateCurrent = "current"
	StateDiffers = "differs"
	StateFailed  = "failed"
	// StateManual: ouder dan protocol 47; eerst bijwerken.
	StateManual = "manual"
	// StateUnknown: de agent meldde zijn adressen nog niet.
	StateUnknown = "unknown"
)

// Commander stuurt een commando naar de agent van een node.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
}

// Endpoints zijn de adressen waarop agents de server bereiken.
type Endpoints struct {
	// ServerURL is leeg als er niets ingesteld is en geen intern adres
	// bekend is; de webinterface gebruikt dan zijn eigen adres.
	ServerURL    string
	ServerSource string
	NatsURL      string
	NatsSource   string
	// De waarden bij Instellingen; leeg is de standaard.
	ServerSetting string
	NatsSetting   string
	// ServerDefault en NatsDefault zijn de adressen zonder instelling.
	ServerDefault string
	NatsDefault   string
}

// Service houdt de adressen bij en zet agents om.
type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	ev     *events.Writer
	log    *slog.Logger
	bus    Commander
	runner *jobs.Runner
	// Advertise is CF_NATS_ADVERTISE; HTTPPort de poort van de webserver
	// en NATSPort die van NATS. HTTPLoopback: de webserver is op de host
	// alleen op 127.0.0.1 gepubliceerd (CF_BIND), dus is het interne adres
	// met HTTPPort niet bereikbaar voor agents.
	Advertise    string
	HTTPPort     int
	NATSPort     int
	HTTPLoopback bool
	// Poll is hoe vaak een omzetting kijkt of de node terug is; Back hoe
	// lang een node daarover mag doen. RollbackAfter is na hoeveel tijd de
	// terugvaltimer op de node afgaat.
	Poll          time.Duration
	Back          time.Duration
	RollbackAfter time.Duration
	Now           func() time.Time
	// Changed wordt aangeroepen als een agent omgezet is.
	Changed func()
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Commander) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, bus: bus, runner: runner,
		HTTPPort: 8080, NATSPort: 4222,
		Poll: 2 * time.Second, Back: protocol.ReconfigureRollbackAfter + 2*time.Minute, RollbackAfter: protocol.ReconfigureRollbackAfter,
		Now: time.Now,
	}
	if runner != nil {
		runner.Register(Kind, s.run)
	}
	return s
}

// PortOf haalt de poort uit een listen-adres zoals ":8080"; def als dat niet
// lukt.
func PortOf(listen string, def int) int {
	if _, p, err := net.SplitHostPort(listen); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// advertiseHost is de host uit CF_NATS_ADVERTISE, zonder poort.
func (s *Service) advertiseHost() string {
	a := strings.TrimSpace(s.Advertise)
	if a == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(a); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(a, "[]")
}

// Endpoints geeft de adressen; requestHost is de Host van de request (voor
// NATS zonder instelling en zonder CF_NATS_ADVERTISE).
func (s *Service) Endpoints(ctx context.Context, requestHost string) (Endpoints, error) {
	set, err := s.q.GetAgentEndpointSettings(ctx)
	if err != nil {
		return Endpoints{}, err
	}
	e := Endpoints{ServerSetting: set.ServerUrl, NatsSetting: set.NatsUrl}
	if h := s.advertiseHost(); h != "" && !s.HTTPLoopback {
		e.ServerDefault = "http://" + net.JoinHostPort(h, strconv.Itoa(s.HTTPPort))
	}
	if a := strings.TrimSpace(s.Advertise); a != "" {
		e.NatsDefault, _ = protocol.NormalizeNatsURL(a, s.NATSPort)
	} else if requestHost != "" {
		host := requestHost
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		e.NatsDefault = "tls://" + net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(s.NATSPort))
	}
	switch {
	case e.ServerSetting != "":
		e.ServerURL, e.ServerSource = e.ServerSetting, SourceSetting
	case e.ServerDefault != "":
		e.ServerURL, e.ServerSource = e.ServerDefault, SourceAdvertise
	default:
		e.ServerSource = SourceRequest
	}
	switch {
	case e.NatsSetting != "":
		e.NatsURL, e.NatsSource = e.NatsSetting, SourceSetting
	case strings.TrimSpace(s.Advertise) != "":
		e.NatsURL, e.NatsSource = e.NatsDefault, SourceAdvertise
	default:
		e.NatsURL, e.NatsSource = e.NatsDefault, SourceRequest
	}
	return e, nil
}

// ValidationError: een ongeldig adres.
type ValidationError struct{ Field, Msg string }

func (e ValidationError) Error() string { return e.Field + ": " + e.Msg }

// SetEndpoints bewaart de adressen; leeg is de standaard.
func (s *Service) SetEndpoints(ctx context.Context, actor events.Actor, by *uuid.UUID, server, nats string) error {
	var err error
	if server = strings.TrimSpace(server); server != "" {
		if server, err = protocol.NormalizeServerURL(server); err != nil {
			return ValidationError{"server_url", err.Error()}
		}
	}
	if nats = strings.TrimSpace(nats); nats != "" {
		if nats, err = protocol.NormalizeNatsURL(nats, s.NATSPort); err != nil {
			return ValidationError{"nats_url", err.Error()}
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.GetAgentEndpointSettings(ctx)
		if err != nil {
			return err
		}
		if old.ServerUrl == server && old.NatsUrl == nats {
			return nil
		}
		if err := q.SetAgentEndpointSettings(ctx, store.SetAgentEndpointSettingsParams{ServerUrl: server, NatsUrl: nats, UpdatedBy: by}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "settings", SubjectID: "agent_endpoints", Action: "settings.agent_endpoints",
			Payload: map[string]any{"server_url": server, "nats_url": nats, "from_server_url": old.ServerUrl, "from_nats_url": old.NatsUrl},
		})
	})
}

// Agent is één agent met zijn adressen.
type Agent struct {
	NodeID      uuid.UUID
	Hostname    string
	ClusterID   *uuid.UUID
	ClusterName string
	ClusterSlug string
	ClusterEnv  string
	Protocol    int
	ServerURL   string
	NatsURL     string
	Online      bool
	State       string
	Why         string
}

// Prod is true voor een agent in een prodcluster.
func (a Agent) Prod() bool { return a.ClusterEnv == string(store.EnvironmentProd) }

// Applicable is true als Omzetten deze agent nu omzet.
func (a Agent) Applicable() bool {
	return a.Online && (a.State == StateDiffers || a.State == StateFailed)
}

// Classify vergelijkt de adressen van een agent met e.
func Classify(r store.ListReconfigureAgentsRow, e Endpoints, now time.Time) Agent {
	a := Agent{
		NodeID: r.ID, Hostname: r.Hostname, ClusterID: r.ClusterID, ClusterName: r.ClusterName, ClusterSlug: r.ClusterSlug,
		ClusterEnv: r.ClusterEnvironment, Protocol: int(r.ProtocolVersion), ServerURL: r.ServerUrl, NatsURL: r.NatsUrl,
		Online: r.HeartbeatAt != nil && now.Sub(*r.HeartbeatAt) <= status.HeartbeatLate,
	}
	switch {
	case a.Protocol < protocol.ReconfigureSince:
		a.State = StateManual
		a.Why = fmt.Sprintf("protocol %d kent omzetten nog niet; werk deze agent eerst bij (protocol %d)", a.Protocol, protocol.ReconfigureSince)
	case a.NatsURL == "":
		a.State, a.Why = StateUnknown, "de agent meldde zijn adressen nog niet"
	case matches(a, e):
		a.State = StateCurrent
	case e.NatsURL != "" && r.ReconfigureRolledBack == e.NatsURL:
		a.State = StateFailed
		a.Why = "de vorige omzetting naar " + e.NatsURL + " werd op de node teruggezet: de agent bereikte de server daar niet. Omzetten probeert het opnieuw"
	default:
		a.State = StateDiffers
		var d []string
		if e.NatsURL != "" && a.NatsURL != e.NatsURL {
			d = append(d, "NATS "+a.NatsURL)
		}
		if e.ServerURL != "" && a.ServerURL != e.ServerURL {
			d = append(d, "server "+or(a.ServerURL, "(leeg)"))
		}
		a.Why = "wijst nog naar " + strings.Join(d, " en ")
	}
	return a
}

func matches(a Agent, e Endpoints) bool {
	return (e.NatsURL == "" || a.NatsURL == e.NatsURL) && (e.ServerURL == "" || a.ServerURL == e.ServerURL)
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Overview is de toestand van alle agents.
type Overview struct {
	Endpoints Endpoints
	Agents    []Agent
	// Running zijn de taken agents.reconfigure die wachten of lopen, per
	// cluster; uuid.Nil voor nodes zonder cluster.
	Running map[uuid.UUID]uuid.UUID
}

func (s *Service) Overview(ctx context.Context, requestHost string) (Overview, error) {
	ov := Overview{Running: map[uuid.UUID]uuid.UUID{}}
	var err error
	if ov.Endpoints, err = s.Endpoints(ctx, requestHost); err != nil {
		return ov, err
	}
	rows, err := s.q.ListReconfigureAgents(ctx)
	if err != nil {
		return ov, err
	}
	now := s.Now()
	for _, r := range rows {
		ov.Agents = append(ov.Agents, Classify(r, ov.Endpoints, now))
	}
	running, err := s.q.ListActiveAgentReconfigureJobs(ctx)
	if err != nil {
		return ov, err
	}
	for _, j := range running {
		key := uuid.Nil
		if j.ClusterID != nil {
			key = *j.ClusterID
		}
		ov.Running[key] = j.ID
	}
	return ov, nil
}

// Request zegt welke agents omgezet worden: deze nodes, of met All elke
// afwijkende agent (op prod alleen met IncludeProd).
type Request struct {
	NodeIDs     []uuid.UUID
	All         bool
	IncludeProd bool
}

// ConflictError: het verzoek kan nu niet.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Select geeft de agents die een verzoek omzet, en de adressen waarnaar.
func (s *Service) Select(ctx context.Context, requestHost string, req Request) ([]Agent, Endpoints, error) {
	ov, err := s.Overview(ctx, requestHost)
	if err != nil {
		return nil, ov.Endpoints, err
	}
	if ov.Endpoints.NatsURL == "" {
		return nil, ov.Endpoints, &ConflictError{"er is geen NATS-adres voor agents; vul het in bij Instellingen of zet CF_NATS_ADVERTISE"}
	}
	var out []Agent
	for _, a := range ov.Agents {
		switch {
		case len(req.NodeIDs) > 0:
			if !slices.Contains(req.NodeIDs, a.NodeID) {
				continue
			}
			if !a.Applicable() {
				return nil, ov.Endpoints, &ConflictError{why(a)}
			}
		case req.All:
			if (a.Prod() && !req.IncludeProd) || !a.Applicable() {
				continue
			}
		}
		out = append(out, a)
	}
	if len(req.NodeIDs) > 0 && len(out) < len(req.NodeIDs) {
		return nil, ov.Endpoints, &ConflictError{"niet elke gevraagde node heeft een agent"}
	}
	if len(out) == 0 {
		return nil, ov.Endpoints, &ConflictError{"er is geen agent om om te zetten: ze wijzen allemaal al naar het juiste adres, of zijn niet verbonden"}
	}
	return out, ov.Endpoints, nil
}

func why(a Agent) string {
	switch a.State {
	case StateCurrent:
		return "de agent op " + a.Hostname + " wijst al naar het juiste adres"
	case StateManual, StateUnknown:
		return a.Hostname + ": " + a.Why
	default:
		return "de agent op " + a.Hostname + " is niet verbonden"
	}
}

type params struct {
	NodeIDs     []uuid.UUID `json:"node_ids"`
	ClusterName string      `json:"cluster_name,omitempty"`
	ServerURL   string      `json:"server_url,omitempty"`
	NatsURL     string      `json:"nats_url"`
}

// Start zet per cluster één taak in de wachtrij. Een cluster waar al een
// taak loopt, staat in skipped.
func (s *Service) Start(ctx context.Context, actor events.Actor, agents []Agent, e Endpoints) (started []store.Job, skipped []string, err error) {
	type group struct {
		cluster *uuid.UUID
		name    string
		nodes   []Agent
	}
	var groups []*group
	byKey := map[uuid.UUID]*group{}
	for _, a := range agents {
		key := uuid.Nil
		if a.ClusterID != nil {
			key = *a.ClusterID
		}
		g := byKey[key]
		if g == nil {
			g = &group{cluster: a.ClusterID, name: a.ClusterName}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.nodes = append(g.nodes, a)
	}
	for _, g := range groups {
		p := params{ClusterName: g.name, ServerURL: e.ServerURL, NatsURL: e.NatsURL}
		for _, a := range g.nodes {
			p.NodeIDs = append(p.NodeIDs, a.NodeID)
		}
		title := "Agents omzetten naar het nieuwe adres in " + g.name
		if g.cluster == nil {
			title = "Agents zonder cluster omzetten naar het nieuwe adres"
		}
		spec := jobs.Spec{Kind: Kind, Params: p, ClusterID: g.cluster, Actor: actor}
		if len(g.nodes) == 1 {
			title, spec.NodeID = "Agent op "+g.nodes[0].Hostname+" omzetten naar het nieuwe adres", &g.nodes[0].NodeID
		}
		spec.Title = title
		var j store.Job
		var err error
		if g.cluster != nil {
			j, err = s.runner.EnqueueForCluster(ctx, spec)
		} else {
			j, err = s.enqueueLoose(ctx, spec)
		}
		var busy jobs.BusyError
		var ce *ConflictError
		switch {
		case errors.As(err, &busy):
			skipped = append(skipped, busy.Error())
		case errors.As(err, &ce):
			skipped = append(skipped, ce.Msg)
		case err != nil:
			return started, skipped, err
		default:
			started = append(started, j)
		}
	}
	if len(started) == 0 && len(skipped) > 0 {
		return nil, skipped, &ConflictError{strings.Join(skipped, "; ")}
	}
	return started, skipped, nil
}

func (s *Service) enqueueLoose(ctx context.Context, spec jobs.Spec) (store.Job, error) {
	var j store.Job
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('agents.reconfigure'))"); err != nil {
			return err
		}
		running, err := q.ListActiveAgentReconfigureJobs(ctx)
		if err != nil {
			return err
		}
		for _, r := range running {
			if r.ClusterID == nil {
				return &ConflictError{"er loopt al een taak die agents zonder cluster omzet"}
			}
		}
		j, err = s.runner.EnqueueTx(ctx, q, spec)
		return err
	})
	if err == nil {
		s.runner.Kick()
	}
	return j, err
}

type stepState struct {
	SentAt     time.Time `json:"sent_at"`
	FromServer string    `json:"from_server"`
	FromNats   string    `json:"from_nats"`
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	rows, err := s.q.ListReconfigureAgents(ctx)
	if err != nil {
		return err
	}
	hosts := map[uuid.UUID]store.ListReconfigureAgentsRow{}
	for _, r := range rows {
		hosts[r.ID] = r
	}
	for _, id := range p.NodeIDs {
		r, ok := hosts[id]
		name := r.Hostname
		if !ok {
			name = id.String()
		}
		err := j.Step(ctx, name+": agent omzetten naar "+p.NatsURL, func(ctx context.Context, st *jobs.Step) error {
			if !ok {
				st.Logf("deze node heeft geen agent meer; overgeslagen")
				return nil
			}
			return s.reconfigureNode(ctx, j, st, r, actor, p)
		})
		if err != nil {
			// De andere nodes van dit cluster blijven op hun oude adres.
			return fmt.Errorf("%s: %w; de andere agents van deze taak zijn niet omgezet en werken op hun oude adres", name, err)
		}
	}
	return nil
}

func (s *Service) reconfigureNode(ctx context.Context, j *jobs.Job, st *jobs.Step, r store.ListReconfigureAgentsRow, actor events.Actor, p params) error {
	e := Endpoints{ServerURL: p.ServerURL, NatsURL: p.NatsURL}
	var state stepState
	if !st.State(&state) || state.SentAt.IsZero() {
		cur, err := s.q.ListReconfigureAgents(ctx)
		if err != nil {
			return err
		}
		var a *Agent
		for _, row := range cur {
			if row.ID == r.ID {
				x := Classify(row, e, s.Now())
				a = &x
			}
		}
		switch {
		case a == nil:
			st.Logf("deze node heeft geen agent meer; overgeslagen")
			return nil
		case a.State == StateCurrent:
			st.Logf("wijst al naar %s", p.NatsURL)
			return nil
		case a.State == StateManual || a.State == StateUnknown:
			st.Logf("overgeslagen: %s", a.Why)
			return nil
		case !a.Online:
			st.Logf("overgeslagen: de agent is niet verbonden")
			return nil
		}
		state = stepState{SentAt: s.Now(), FromServer: a.ServerURL, FromNats: a.NatsURL}
		st.Logf("van NATS %s en server %s naar NATS %s en server %s", a.NatsURL, or(a.ServerURL, "(leeg)"), p.NatsURL, or(p.ServerURL, "(ongewijzigd)"))
		cmd := protocol.Command{
			ID: "reconfigure-" + j.ID.String() + "-" + r.ID.String(), Action: protocol.CmdAgentReconfigure,
			Deadline: state.SentAt.Add(protocol.ReconfigureTimeout), Reason: j.Title,
			Reconfigure: &protocol.AgentReconfigure{ServerURL: p.ServerURL, NatsURL: p.NatsURL},
		}
		cctx, cancel := context.WithTimeout(ctx, protocol.ReconfigureTimeout+30*time.Second)
		res, err := s.bus.Command(cctx, r.ID, cmd)
		cancel()
		for _, line := range res.Output {
			st.Logf("%s", line)
		}
		switch {
		case ctx.Err() != nil:
			return context.Cause(ctx)
		case err != nil:
			return s.failed(ctx, j, r, actor, err.Error())
		case !res.OK:
			return s.failed(ctx, j, r, actor, "de agent meldt: "+res.Error)
		case res.Repeat:
			return s.done(ctx, j, r, actor, p, state)
		}
		if err := st.SetState(ctx, state); err != nil {
			return err
		}
	}
	st.Logf("wachten tot de agent op het nieuwe adres met de server praat")
	_ = st.Flush(ctx)
	wctx, cancel := context.WithTimeout(ctx, time.Until(state.SentAt.Add(s.Back)))
	defer cancel()
	err := pollEvery(wctx, s.Poll, func(ctx context.Context) (bool, error) {
		cur, err := s.q.GetReconfigureAgent(ctx, r.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errors.New("de agent van deze node werd ingetrokken")
		}
		if err != nil || cur.HeartbeatAt == nil || !cur.HeartbeatAt.After(state.SentAt) {
			return false, err
		}
		if cur.NatsUrl == p.NatsURL && (p.ServerURL == "" || cur.ServerUrl == p.ServerURL) {
			st.Logf("terug na %s op %s", seconds(s.Now().Sub(state.SentAt)), cur.NatsUrl)
			return true, nil
		}
		if cur.ReconfigureRolledBack == p.NatsURL && !cur.HeartbeatAt.Before(state.SentAt.Add(s.RollbackAfter)) {
			return false, errors.New("de agent bereikte de server niet op het nieuwe adres en de node zette het oude agent.json terug; hij werkt verder op " + cur.NatsUrl)
		}
		return false, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if wctx.Err() != nil {
			err = fmt.Errorf("de agent kwam niet terug binnen %s; de node zet het oude agent.json vanzelf terug. Kijk op de node met journalctl -u cf-agent", seconds(s.Back))
		}
		return s.failed(ctx, j, r, actor, err.Error())
	}
	return s.done(ctx, j, r, actor, p, state)
}

func (s *Service) done(ctx context.Context, j *jobs.Job, r store.ListReconfigureAgentsRow, actor events.Actor, p params, state stepState) error {
	if s.Changed != nil {
		s.Changed()
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: r.ID.String(), ClusterID: r.ClusterID, Action: "node.agent_reconfigured",
		Payload: map[string]any{
			"hostname": r.Hostname, "nats_url": p.NatsURL, "server_url": p.ServerURL,
			"from_nats_url": state.FromNats, "from_server_url": state.FromServer, "job_id": j.ID,
		},
	})
}

func (s *Service) failed(ctx context.Context, j *jobs.Job, r store.ListReconfigureAgentsRow, actor events.Actor, msg string) error {
	if err := s.ev.Write(context.WithoutCancel(ctx), nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: r.ID.String(), ClusterID: r.ClusterID, Action: "node.agent_reconfigure_failed",
		Payload: map[string]any{"hostname": r.Hostname, "error": msg, "job_id": j.ID},
	}); err != nil {
		s.log.Warn("logboek schrijven mislukt", "err", err)
	}
	if s.Changed != nil {
		s.Changed()
	}
	return errors.New(msg)
}

func pollEvery(ctx context.Context, every time.Duration, check func(context.Context) (bool, error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		ok, err := check(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-t.C:
		}
	}
}

func seconds(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 120 {
		return fmt.Sprintf("%d s", s)
	}
	if s%60 == 0 {
		return fmt.Sprintf("%d min", s/60)
	}
	return fmt.Sprintf("%d min %d s", s/60, s%60)
}
