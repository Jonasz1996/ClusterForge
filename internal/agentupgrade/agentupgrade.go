// Package agentupgrade werkt de agents op de nodes bij naar de agent die de
// server aanbiedt: als taak agents.upgrade, één node tegelijk per cluster,
// en vanzelf na een serverupdate. Vanaf agentprotocol 23 doet de agent het
// zelf (protocol.CmdAgentUpgrade); een oudere werk je één keer met de hand
// bij.
package agentupgrade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
const Kind = "agents.upgrade"

// Archs zijn de architecturen waarvoor de server een agent aanbiedt.
var Archs = []string{"amd64", "arm64"}

// Toestand van een agent.
const (
	// StateCurrent: dezelfde binary als die de server aanbiedt.
	StateCurrent = "current"
	// StateOutdated: een oudere of andere agent, die zichzelf kan bijwerken.
	StateOutdated = "outdated"
	// StateFailed: de update naar deze agent werd op de node teruggezet.
	// Vanzelf probeert ClusterForge hem niet nog eens; met de knop wel.
	StateFailed = "failed"
	// StateManual: verouderd, maar ouder dan protocol 23; met de hand
	// bijwerken.
	StateManual = "manual"
	// StateNewer: nieuwer dan de server; ClusterForge zet hem nooit terug.
	StateNewer = "newer"
	// StateUnknown: de server biedt geen agent voor deze node aan, of de
	// agent meldde nog niet welke binary hij is.
	StateUnknown = "unknown"
)

// Commander stuurt een commando naar de agent van een node.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
}

// Offer is de agent die deze server aanbiedt.
type Offer struct {
	Version  string
	Protocol int
	// SHA256 per architectuur; leeg als de server die niet meebouwde.
	SHA256 map[string]string
}

// LoadOffer leest de sha256-bestanden naast de binaries in dir.
func LoadOffer(dir, version string) Offer {
	o := Offer{Version: version, Protocol: protocol.Version, SHA256: map[string]string{}}
	for _, arch := range Archs {
		b, err := os.ReadFile(filepath.Join(dir, "cf-agent-linux-"+arch+".sha256"))
		if err != nil {
			continue
		}
		if f := strings.Fields(string(b)); len(f) > 0 && len(f[0]) == 64 {
			o.SHA256[arch] = strings.ToLower(f[0])
		}
	}
	return o
}

// Agent is één agent met zijn toestand.
type Agent struct {
	NodeID      uuid.UUID
	Hostname    string
	ClusterID   *uuid.UUID
	ClusterName string
	ClusterSlug string
	ClusterEnv  string
	Version     string
	Protocol    int
	Arch        string
	SHA256      string
	Online      bool
	State       string
	// Why legt de toestand uit, in gewone woorden.
	Why string
	// Auto: deze agent wordt vanzelf bijgewerkt.
	Auto bool
}

// Prod is true voor een agent in een prodcluster.
func (a Agent) Prod() bool { return a.ClusterEnv == string(store.EnvironmentProd) }

// Upgradable is true als de knop deze agent nu bijwerkt.
func (a Agent) Upgradable() bool {
	return a.Online && (a.State == StateOutdated || a.State == StateFailed)
}

// Classify zegt hoe een agent ervoor staat tegenover de agent van de server.
func Classify(r store.ListUpgradeAgentsRow, o Offer, now time.Time) Agent {
	a := Agent{
		NodeID: r.ID, Hostname: r.Hostname, ClusterID: r.ClusterID, ClusterName: r.ClusterName, ClusterSlug: r.ClusterSlug,
		ClusterEnv: r.ClusterEnvironment, Version: r.Version, Protocol: int(r.ProtocolVersion), Arch: r.Arch, SHA256: r.BinarySha256,
		Online: r.HeartbeatAt != nil && now.Sub(*r.HeartbeatAt) <= status.HeartbeatLate,
	}
	want := o.SHA256[r.Arch]
	switch {
	case a.Protocol > o.Protocol:
		a.State, a.Why = StateNewer, fmt.Sprintf("deze agent heeft protocol %d en de server %d; ClusterForge zet een agent nooit terug", a.Protocol, o.Protocol)
	case a.Protocol < protocol.SelfUpgradeSince:
		a.State, a.Why = StateManual, fmt.Sprintf("protocol %d kan zichzelf nog niet bijwerken; werk deze agent één keer bij met het installatiescript en --upgrade", a.Protocol)
	case len(o.SHA256) == 0:
		a.State, a.Why = StateUnknown, "deze server biedt geen agent aan (geen binaries in CF_AGENT_DIR)"
	case a.SHA256 == "" || a.Arch == "":
		a.State, a.Why = StateUnknown, "de agent meldde nog niet welke binary hij is"
	case want == "":
		a.State, a.Why = StateUnknown, "deze server biedt geen agent voor "+a.Arch+" aan"
	case a.SHA256 == want:
		a.State = StateCurrent
	case r.UpgradeRolledBack == want:
		a.State, a.Why = StateFailed, "de vorige update naar deze agent werd op de node teruggezet: de nieuwe agent praatte niet op tijd met de server. Kijk met journalctl -u cf-agent waarom; Bijwerken probeert het opnieuw"
	default:
		a.State = StateOutdated
		if a.Protocol < o.Protocol {
			a.Why = fmt.Sprintf("protocol %d, de server biedt %d aan", a.Protocol, o.Protocol)
		} else {
			a.Why = "een andere build dan de agent van de server"
		}
	}
	return a
}

// Service houdt bij welke agents verouderd zijn en werkt ze bij.
type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	ev     *events.Writer
	log    *slog.Logger
	bus    Commander
	runner *jobs.Runner
	// Dir is de map met de binaries (CF_AGENT_DIR), Version de versie van
	// de server.
	Dir     string
	Version string
	// Poll is hoe vaak een update kijkt of de node terug is; Back hoe lang
	// een node daarover mag doen, na het commando.
	Poll time.Duration
	Back time.Duration
	// Interval is hoe vaak Run kijkt of er agents vanzelf bij te werken
	// zijn; nul of minder zet het uit. StartDelay is hoe lang Run na de
	// start wacht, zodat de agents na een serverupdate eerst terug zijn.
	Interval   time.Duration
	StartDelay time.Duration
	Now        func() time.Time
	// Changed wordt aangeroepen als een agent bijgewerkt is.
	Changed func()

	kick chan struct{}
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Commander, dir, version string) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, bus: bus, runner: runner, Dir: dir, Version: version,
		Poll: 2 * time.Second, Back: protocol.UpgradeRollbackAfter + 2*time.Minute,
		Interval: time.Minute, StartDelay: 2 * time.Minute, Now: time.Now, kick: make(chan struct{}, 1),
	}
	if runner != nil {
		runner.Register(Kind, s.run)
	}
	return s
}

// Offer is de agent die de server nu aanbiedt.
func (s *Service) Offer() Offer { return LoadOffer(s.Dir, s.Version) }

// Overview is de toestand van alle agents.
type Overview struct {
	Offer  Offer
	Auto   bool
	Off    []uuid.UUID
	Agents []Agent
	// Running zijn de taken agents.upgrade die wachten of lopen, per
	// cluster; uuid.Nil voor nodes zonder cluster.
	Running map[uuid.UUID]uuid.UUID
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	ov := Overview{Offer: s.Offer(), Running: map[uuid.UUID]uuid.UUID{}}
	settings, err := s.q.GetAgentUpgradeSettings(ctx)
	if err != nil {
		return ov, err
	}
	ov.Auto = settings
	if ov.Off, err = s.q.ListAgentUpgradeOff(ctx); err != nil {
		return ov, err
	}
	rows, err := s.q.ListUpgradeAgents(ctx)
	if err != nil {
		return ov, err
	}
	now := s.Now()
	for _, r := range rows {
		a := Classify(r, ov.Offer, now)
		a.Auto = ov.Auto && a.State == StateOutdated && !a.Prod() && (a.ClusterID == nil || !slices.Contains(ov.Off, *a.ClusterID))
		ov.Agents = append(ov.Agents, a)
	}
	running, err := s.q.ListActiveAgentUpgradeJobs(ctx)
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

// Request zegt welke agents bijgewerkt worden.
type Request struct {
	// NodeIDs: deze nodes. ClusterID: elke verouderde agent van dit
	// cluster. All: elke verouderde agent buiten prod.
	NodeIDs   []uuid.UUID
	ClusterID *uuid.UUID
	All       bool
}

// ConflictError: het verzoek kan nu niet.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Select geeft de agents die een verzoek bijwerkt. Een gevraagde node die
// niet bij te werken is, geeft een ConflictError met de reden.
func (s *Service) Select(ctx context.Context, req Request) ([]Agent, error) {
	ov, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	var out []Agent
	for _, a := range ov.Agents {
		switch {
		case len(req.NodeIDs) > 0:
			if !slices.Contains(req.NodeIDs, a.NodeID) {
				continue
			}
			if !a.Upgradable() {
				return nil, &ConflictError{why(a)}
			}
		case req.ClusterID != nil:
			if a.ClusterID == nil || *a.ClusterID != *req.ClusterID || !a.Upgradable() {
				continue
			}
		case req.All:
			if a.Prod() || !a.Upgradable() {
				continue
			}
		}
		out = append(out, a)
	}
	if len(req.NodeIDs) > 0 && len(out) < len(req.NodeIDs) {
		return nil, &ConflictError{"niet elke gevraagde node heeft een agent"}
	}
	if len(out) == 0 {
		return nil, &ConflictError{"er is geen agent om bij te werken"}
	}
	return out, nil
}

func why(a Agent) string {
	switch {
	case a.State == StateCurrent:
		return "de agent op " + a.Hostname + " is al bijgewerkt"
	case a.State != StateOutdated && a.State != StateFailed:
		return a.Hostname + ": " + a.Why
	default:
		return "de agent op " + a.Hostname + " is niet verbonden"
	}
}

type params struct {
	NodeIDs     []uuid.UUID `json:"node_ids"`
	ClusterName string      `json:"cluster_name,omitempty"`
	Auto        bool        `json:"auto,omitempty"`
}

// Start zet per cluster één taak in de wachtrij. Een cluster waar al een
// taak loopt, slaat het over met de reden in skipped; is dat het enige
// cluster, dan is het een ConflictError.
func (s *Service) Start(ctx context.Context, actor events.Actor, agents []Agent, auto bool) (started []store.Job, skipped []string, err error) {
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
		p := params{ClusterName: g.name, Auto: auto}
		for _, a := range g.nodes {
			p.NodeIDs = append(p.NodeIDs, a.NodeID)
		}
		title := "Agents bijwerken in " + g.name
		if g.cluster == nil {
			title = "Agents bijwerken zonder cluster"
		}
		spec := jobs.Spec{Kind: Kind, Params: p, ClusterID: g.cluster, Actor: actor}
		if len(g.nodes) == 1 {
			title, spec.NodeID = "Agent op "+g.nodes[0].Hostname+" bijwerken", &g.nodes[0].NodeID
		}
		if auto {
			title += " (vanzelf)"
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

// enqueueLoose zet een taak voor nodes zonder cluster in de wachtrij, als
// er niet al een wacht of loopt.
func (s *Service) enqueueLoose(ctx context.Context, spec jobs.Spec) (store.Job, error) {
	var j store.Job
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('agents.upgrade'))"); err != nil {
			return err
		}
		running, err := q.ListActiveAgentUpgradeJobs(ctx)
		if err != nil {
			return err
		}
		for _, r := range running {
			if r.ClusterID == nil {
				return &ConflictError{"er loopt al een taak die agents zonder cluster bijwerkt"}
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

// stepState bewaart wanneer het commando wegging; een hervatte taak stuurt
// het niet opnieuw maar wacht verder.
type stepState struct {
	SentAt time.Time `json:"sent_at"`
	To     string    `json:"to"`
	From   string    `json:"from_version"`
	FromP  int       `json:"from_protocol"`
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
	rows, err := s.q.ListUpgradeAgents(ctx)
	if err != nil {
		return err
	}
	hosts := map[uuid.UUID]store.ListUpgradeAgentsRow{}
	for _, r := range rows {
		hosts[r.ID] = r
	}
	for _, id := range p.NodeIDs {
		r, ok := hosts[id]
		name := r.Hostname
		if !ok {
			name = id.String()
		}
		err := j.Step(ctx, name+": cf-agent bijwerken", func(ctx context.Context, st *jobs.Step) error {
			if !ok {
				st.Logf("deze node heeft geen agent meer; overgeslagen")
				return nil
			}
			return s.upgradeNode(ctx, j, st, r, actor, p.Auto)
		})
		if err != nil {
			// De andere nodes van dit cluster blijven zoals ze zijn.
			return fmt.Errorf("%s: %w; de andere agents van deze taak zijn niet bijgewerkt", name, err)
		}
	}
	return nil
}

func (s *Service) upgradeNode(ctx context.Context, j *jobs.Job, st *jobs.Step, r store.ListUpgradeAgentsRow, actor events.Actor, auto bool) error {
	var state stepState
	if !st.State(&state) || state.SentAt.IsZero() {
		cur, err := s.q.ListUpgradeAgents(ctx)
		if err != nil {
			return err
		}
		offer := s.Offer()
		var a *Agent
		for _, row := range cur {
			if row.ID == r.ID {
				x := Classify(row, offer, s.Now())
				a = &x
			}
		}
		switch {
		case a == nil:
			st.Logf("deze node heeft geen agent meer; overgeslagen")
			return nil
		case a.State == StateCurrent:
			st.Logf("al bijgewerkt (versie %s, protocol %d)", a.Version, a.Protocol)
			return nil
		case a.State != StateOutdated && a.State != StateFailed:
			st.Logf("overgeslagen: %s", a.Why)
			return nil
		case !a.Online:
			st.Logf("overgeslagen: de agent is niet verbonden")
			return nil
		}
		state = stepState{SentAt: s.Now(), To: offer.SHA256[a.Arch], From: a.Version, FromP: a.Protocol}
		st.Logf("van versie %s (protocol %d) naar %s (protocol %d)", a.Version, a.Protocol, offer.Version, offer.Protocol)
		cmd := protocol.Command{
			ID: "upgrade-" + j.ID.String() + "-" + r.ID.String(), Action: protocol.CmdAgentUpgrade,
			Deadline: state.SentAt.Add(protocol.UpgradeTimeout), Reason: j.Title,
			Upgrade: &protocol.AgentUpgrade{Version: offer.Version, Protocol: offer.Protocol, SHA256: offer.SHA256},
		}
		cctx, cancel := context.WithTimeout(ctx, protocol.UpgradeTimeout+30*time.Second)
		res, err := s.bus.Command(cctx, r.ID, cmd)
		cancel()
		for _, line := range res.Output {
			st.Logf("%s", line)
		}
		switch {
		case ctx.Err() != nil:
			return context.Cause(ctx)
		case err != nil:
			return s.failed(ctx, j, r, actor, auto, err.Error())
		case !res.OK:
			return s.failed(ctx, j, r, actor, auto, "de agent meldt: "+res.Error)
		}
		if err := st.SetState(ctx, state); err != nil {
			return err
		}
	}
	st.Logf("wachten tot de nieuwe agent met de server praat")
	_ = st.Flush(ctx)
	wctx, cancel := context.WithTimeout(ctx, time.Until(state.SentAt.Add(s.Back)))
	defer cancel()
	var result string
	err := pollEvery(wctx, s.Poll, func(ctx context.Context) (bool, error) {
		cur, err := s.q.GetUpgradeAgent(ctx, r.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errors.New("de agent van deze node werd ingetrokken")
		}
		if err != nil || cur.HeartbeatAt == nil || !cur.HeartbeatAt.After(state.SentAt) {
			return false, err
		}
		switch state.To {
		case cur.BinarySha256:
			st.Logf("terug na %s: versie %s, protocol %d", seconds(s.Now().Sub(state.SentAt)), cur.Version, cur.ProtocolVersion)
			result = cur.Version
			return true, nil
		case cur.UpgradeRolledBack:
			return false, errors.New("de nieuwe agent praatte niet op tijd met de server en de node zette de oude terug; kijk op de node met journalctl -u cf-agent waarom")
		}
		return false, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if wctx.Err() != nil {
			err = fmt.Errorf("de agent kwam niet terug binnen %s; kijk op de node met systemctl status cf-agent en journalctl -u cf-agent", seconds(s.Back))
		}
		return s.failed(ctx, j, r, actor, auto, err.Error())
	}
	if s.Changed != nil {
		s.Changed()
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: r.ID.String(), ClusterID: r.ClusterID, Action: "node.agent_upgraded",
		Payload: map[string]any{
			"hostname": r.Hostname, "from": state.From, "from_protocol": state.FromP, "to": result, "to_protocol": protocol.Version,
			"job_id": j.ID, "auto": auto,
		},
	})
}

// failed schrijft een logboekregel en geeft de fout terug.
func (s *Service) failed(ctx context.Context, j *jobs.Job, r store.ListUpgradeAgentsRow, actor events.Actor, auto bool, msg string) error {
	if err := s.ev.Write(context.WithoutCancel(ctx), nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: r.ID.String(), ClusterID: r.ClusterID, Action: "node.agent_upgrade_failed",
		Payload: map[string]any{"hostname": r.Hostname, "error": msg, "job_id": j.ID, "auto": auto},
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

// seconds schrijft een duur als "42 s" of "3 min 5 s".
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
