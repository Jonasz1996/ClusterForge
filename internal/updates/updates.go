// Package updates werkt de software op de nodes bij via de agents (fase
// 10): pakketten met apt en Docker Compose-projecten, als taak updates.run,
// één node tegelijk met de VIP-houder als laatste, en na elke node wachten
// tot hij gezond terug is. Op een node van een databasecluster blijven de
// databasepakketten staan; die gaan via Bijwerken op de databasekaart.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak.
const Kind = "updates.run"

// DatabasePackages blijven staan op een node van een databasecluster.
var DatabasePackages = []string{
	"mariadb-*", "galera-*", "libmariadb*", "mysql-common",
	"postgresql*", "libpq*", "patroni", "python3-etcd",
}

// Commander stuurt een commando naar een agent.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
	noderun.Bus
}

// ConflictError: het verzoek kan nu niet.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// FieldError: een veld van het verzoek klopt niet.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// Service toont en start updates.
type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	ev     *events.Writer
	runner *jobs.Runner
	bus    Commander
	Run    *noderun.Runner
	Gate   *health.Gate
	// ReadyTimeout is hoe lang een node na de update mag doen over gezond
	// terugkomen; RebootTimeout na een herstart.
	ReadyTimeout  time.Duration
	RebootTimeout time.Duration
	Now           func() time.Time
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus Commander) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, runner: runner, bus: bus,
		Run: &noderun.Runner{Bus: bus}, Gate: health.NewGate(pool),
		ReadyTimeout: 3 * time.Minute, RebootTimeout: 10 * time.Minute, Now: time.Now,
	}
	if runner != nil {
		runner.Register(Kind, s.run)
	}
	return s
}

// Node is een node zoals de pagina Updates hem toont.
type Node struct {
	NodeID      uuid.UUID
	Hostname    string
	ClusterID   *uuid.UUID
	ClusterName string
	ClusterSlug string
	ClusterEnv  string
	Online      bool
	Protocol    int
	// Supported: de agent kent runs (protocol 25).
	Supported      bool
	Upgrades       *protocol.Upgrades
	RebootRequired bool
	AptUpdatedAt   time.Time
	Docker         *protocol.Docker
	HasDatabase    bool
	VIPHolder      bool
	FactsAt        *time.Time
}

// Prod is true voor een node in een prodcluster.
func (n Node) Prod() bool { return n.ClusterEnv == string(store.EnvironmentProd) }

// Overview geeft elke node met een agent.
func (s *Service) Overview(ctx context.Context) ([]Node, error) {
	rows, err := s.q.ListUpdateNodes(ctx)
	if err != nil {
		return nil, err
	}
	vips := map[uuid.UUID][]string{}
	out := make([]Node, 0, len(rows))
	now := s.Now()
	for _, r := range rows {
		n := Node{
			NodeID: r.ID, Hostname: r.Hostname, ClusterID: r.ClusterID, ClusterName: r.ClusterName, ClusterSlug: r.ClusterSlug,
			ClusterEnv: r.ClusterEnvironment, Protocol: int(r.ProtocolVersion), HasDatabase: r.HasDatabase, FactsAt: r.CollectedAt,
			Online: r.HeartbeatAt != nil && now.Sub(*r.HeartbeatAt) <= status.HeartbeatLate,
		}
		n.Supported = n.Protocol >= protocol.RunSince
		if len(r.Facts) > 0 {
			var f protocol.Facts
			if json.Unmarshal(r.Facts, &f) == nil {
				n.Upgrades, n.RebootRequired, n.AptUpdatedAt, n.Docker = f.Upgrades, f.RebootRequired, f.AptUpdatedAt, f.Docker
			}
		}
		if r.ClusterID != nil {
			cv, ok := vips[*r.ClusterID]
			if !ok {
				if cv, err = s.clusterVIPs(ctx, *r.ClusterID); err != nil {
					return nil, err
				}
				vips[*r.ClusterID] = cv
			}
			n.VIPHolder = slices.ContainsFunc(r.Addresses, func(a string) bool { return slices.Contains(cv, a) })
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Service) clusterVIPs(ctx context.Context, clusterID uuid.UUID) ([]string, error) {
	rows, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Vip.Address.String())
	}
	return out, nil
}

// Apt zegt hoe apt bijwerkt; nil is niet.
type Apt struct {
	Full       bool `json:"full,omitempty"`
	Autoremove bool `json:"autoremove,omitempty"`
}

// Docker zegt welke Compose-projecten; nil is niet.
type Docker struct {
	// Projects: leeg is alle projecten op elke node.
	Projects []string `json:"projects,omitempty"`
	Prune    bool     `json:"prune,omitempty"`
}

// Request is wat de beheerder vraagt.
type Request struct {
	// NodeIDs of ClusterID.
	NodeIDs   []uuid.UUID
	ClusterID *uuid.UUID
	Apt       *Apt
	Docker    *Docker
	// Reboot herstart een node als hij daar na de update om vraagt.
	Reboot bool
}

// Select geeft de nodes van een verzoek, in de volgorde van de taak per
// cluster: zonder VIP eerst, de VIP-houder als laatste.
func (s *Service) Select(ctx context.Context, req Request) ([]Node, error) {
	if req.Apt == nil && req.Docker == nil {
		return nil, &FieldError{"kies pakketten (apt), Docker of beide"}
	}
	if (len(req.NodeIDs) > 0) == (req.ClusterID != nil) {
		return nil, &FieldError{"kies nodes of een cluster"}
	}
	all, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	var out []Node
	for _, n := range all {
		switch {
		case req.ClusterID != nil && (n.ClusterID == nil || *n.ClusterID != *req.ClusterID):
			continue
		case len(req.NodeIDs) > 0 && !slices.Contains(req.NodeIDs, n.NodeID):
			continue
		}
		switch {
		case !n.Supported:
			return nil, &ConflictError{fmt.Sprintf("de agent op %s heeft protocol %d en kent updates nog niet (vanaf %d); werk de agents eerst bij", n.Hostname, n.Protocol, protocol.RunSince)}
		case !n.Online:
			return nil, &ConflictError{"de agent op " + n.Hostname + " is niet verbonden"}
		}
		out = append(out, n)
	}
	if len(req.NodeIDs) > 0 && len(out) < len(req.NodeIDs) {
		return nil, &ConflictError{"niet elke gevraagde node heeft een agent"}
	}
	if len(out) == 0 {
		return nil, &ConflictError{"dit cluster heeft geen nodes met een agent"}
	}
	return Order(out), nil
}

// Order zet de nodes per cluster op volgorde: zonder VIP eerst, de
// VIP-houders als laatste; de clusters blijven in hun volgorde.
func Order(nodes []Node) []Node {
	out := slices.Clone(nodes)
	key := func(n Node) uuid.UUID {
		if n.ClusterID == nil {
			return uuid.Nil
		}
		return *n.ClusterID
	}
	first := map[uuid.UUID]int{}
	for i, n := range out {
		if _, ok := first[key(n)]; !ok {
			first[key(n)] = i
		}
	}
	slices.SortStableFunc(out, func(a, b Node) int {
		if d := first[key(a)] - first[key(b)]; d != 0 {
			return d
		}
		switch {
		case a.VIPHolder == b.VIPHolder:
			return 0
		case a.VIPHolder:
			return 1
		}
		return -1
	})
	return out
}

// params zijn de parameters van de taak.
type params struct {
	Nodes  []jobNode `json:"nodes"`
	Apt    *Apt      `json:"apt,omitempty"`
	Docker *Docker   `json:"docker,omitempty"`
	Reboot bool      `json:"reboot,omitempty"`
}

type jobNode struct {
	ID          uuid.UUID `json:"id"`
	Hostname    string    `json:"hostname"`
	VIPHolder   bool      `json:"vip_holder,omitempty"`
	HasDatabase bool      `json:"has_database,omitempty"`
}

// Start zet per cluster één taak in de wachtrij. Een cluster waar al een
// taak loopt, slaat het over met de reden in skipped.
func (s *Service) Start(ctx context.Context, actor events.Actor, req Request, nodes []Node) (started []store.Job, skipped []string, err error) {
	type group struct {
		cluster *uuid.UUID
		name    string
		nodes   []Node
	}
	var groups []*group
	byKey := map[uuid.UUID]*group{}
	for _, n := range nodes {
		key := uuid.Nil
		if n.ClusterID != nil {
			key = *n.ClusterID
		}
		g := byKey[key]
		if g == nil {
			g = &group{cluster: n.ClusterID, name: n.ClusterName}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.nodes = append(g.nodes, n)
	}
	what := whatText(req.Apt, req.Docker)
	for _, g := range groups {
		p := params{Apt: req.Apt, Docker: req.Docker, Reboot: req.Reboot}
		for _, n := range g.nodes {
			p.Nodes = append(p.Nodes, jobNode{ID: n.NodeID, Hostname: n.Hostname, VIPHolder: n.VIPHolder, HasDatabase: n.HasDatabase})
		}
		spec := jobs.Spec{Kind: Kind, Params: p, ClusterID: g.cluster, Actor: actor}
		switch {
		case len(g.nodes) == 1:
			spec.Title, spec.NodeID = fmt.Sprintf("%s bijwerken op %s", what, g.nodes[0].Hostname), &g.nodes[0].NodeID
		case g.cluster != nil:
			spec.Title = fmt.Sprintf("%s bijwerken in %s", what, g.name)
		default:
			spec.Title = what + " bijwerken op nodes zonder cluster"
		}
		var j store.Job
		var err error
		if g.cluster != nil {
			j, err = s.runner.EnqueueForCluster(ctx, spec)
		} else {
			j, err = s.runner.Enqueue(ctx, spec)
		}
		var busy jobs.BusyError
		switch {
		case errors.As(err, &busy):
			skipped = append(skipped, busy.Error())
		case err != nil:
			return started, skipped, err
		default:
			started = append(started, j)
		}
	}
	if len(started) == 0 && len(skipped) > 0 {
		return nil, skipped, &ConflictError{strings.Join(skipped, "; ")}
	}
	s.runner.Kick()
	return started, skipped, nil
}

func whatText(a *Apt, d *Docker) string {
	switch {
	case a != nil && d != nil:
		return "Pakketten en Docker"
	case d != nil:
		return "Docker"
	}
	return "Pakketten"
}

// prep is de state van de eerste stap per node: de diensten die vóór de
// update draaiden.
type prep struct {
	Services []string  `json:"services"`
	At       time.Time `json:"at"`
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	for i, n := range p.Nodes {
		name := n.Hostname
		if n.VIPHolder {
			name += " (VIP-houder)"
		}
		prepStep := name + ": voorbereiden"
		if err := j.Step(ctx, prepStep, func(ctx context.Context, st *jobs.Step) error {
			return s.prepare(ctx, st, n)
		}); err != nil {
			return stop(ctx, err, n)
		}
		if p.Apt != nil {
			spec := protocol.RunSpec{
				ID: runID(j, i, "apt"), Kind: protocol.RunApt, Title: "Pakketten bijwerken op " + n.Hostname,
				TimeoutSeconds: int((time.Hour).Seconds()),
				Apt:            &protocol.AptRun{Full: p.Apt.Full, Autoremove: p.Apt.Autoremove},
			}
			if n.HasDatabase {
				spec.Apt.Hold = DatabasePackages
			}
			if err := j.Step(ctx, name+": pakketten (apt)", func(ctx context.Context, st *jobs.Step) error {
				if n.HasDatabase {
					st.Logf("dit cluster heeft een database: de databasepakketten blijven staan; werk die bij met Bijwerken op de databasekaart")
				}
				return s.runOne(ctx, j, st, n, spec)
			}); err != nil {
				return stop(ctx, err, n)
			}
		}
		if p.Docker != nil {
			spec := protocol.RunSpec{
				ID: runID(j, i, "docker"), Kind: protocol.RunDocker, Title: "Docker bijwerken op " + n.Hostname,
				TimeoutSeconds: int((time.Hour).Seconds()),
				Docker:         &protocol.DockerRun{Projects: p.Docker.Projects, Prune: p.Docker.Prune},
			}
			if err := j.Step(ctx, name+": Docker", func(ctx context.Context, st *jobs.Step) error {
				return s.runOne(ctx, j, st, n, spec)
			}); err != nil {
				return stop(ctx, err, n)
			}
		}
		if p.Reboot {
			aptStep := name + ": pakketten (apt)"
			if err := j.Step(ctx, name+": herstarten als het nodig is", func(ctx context.Context, st *jobs.Step) error {
				res, ok := noderun.Result(j, aptStep)
				if !ok || !res.RebootRequired {
					st.Logf("%s vraagt geen herstart", n.Hostname)
					return nil
				}
				return s.reboot(ctx, st, n)
			}); err != nil {
				return stop(ctx, err, n)
			}
		}
		if err := j.Step(ctx, name+": gezond terug", func(ctx context.Context, st *jobs.Step) error {
			var pr prep
			j.Done(prepStep, &pr)
			return s.settle(ctx, st, n, pr)
		}); err != nil {
			return stop(ctx, err, n)
		}
	}
	return nil
}

// stop zegt bij een fout dat de nodes erna niet aan de beurt kwamen.
func stop(ctx context.Context, err error, n jobNode) error {
	if ctx.Err() != nil {
		return err
	}
	return fmt.Errorf("%s: %w; de nodes erna zijn niet bijgewerkt", n.Hostname, err)
}

// runID is uniek per taak, poging, node en soort.
func runID(j *jobs.Job, i int, kind string) string {
	return fmt.Sprintf("%s-%d-%d-%s", j.ID, j.Attempts, i, kind)
}

// prepare onthoudt welke gevolgde diensten nu draaien en controleert dat
// de agent er is.
func (s *Service) prepare(ctx context.Context, st *jobs.Step, n jobNode) error {
	rt, err := s.q.GetNodeRuntime(ctx, n.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("de node bestaat niet meer")
	}
	if err != nil {
		return err
	}
	switch {
	case !rt.HasAgent:
		return errors.New("de node heeft geen agent meer")
	case rt.AgentProtocol < protocol.RunSince:
		return fmt.Errorf("de agent heeft protocol %d en kent updates nog niet (vanaf %d)", rt.AgentProtocol, protocol.RunSince)
	case rt.HeartbeatAt == nil || s.Now().Sub(*rt.HeartbeatAt) > status.HeartbeatLate:
		return errors.New("de agent is niet verbonden")
	}
	var active []string
	for name, state := range health.ServiceStates(rt.Services) {
		if state == "active" {
			active = append(active, name)
		}
	}
	slices.Sort(active)
	if len(active) > 0 {
		st.Logf("draait nu: %s; daar wacht ClusterForge na de update op", strings.Join(active, ", "))
	}
	return st.SetState(ctx, prep{Services: active, At: s.Now()})
}

// runOne voert een run uit; alles behalve ok is een fout.
func (s *Service) runOne(ctx context.Context, j *jobs.Job, st *jobs.Step, n jobNode, spec protocol.RunSpec) error {
	res, err := s.Run.Do(ctx, st, n.ID, spec)
	if err != nil {
		return err
	}
	if s.ev != nil {
		actor := events.System()
		if j.RequestedBy != nil {
			actor = events.User(*j.RequestedBy)
		}
		action := "node.software_updated"
		if res.State != protocol.RunOK {
			action = "node.software_update_failed"
		}
		_ = s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: j.ClusterID,
			Action: action, Payload: map[string]any{
				"hostname": n.Hostname, "kind": spec.Kind, "result": res.State, "held": res.Held,
				"reboot_required": res.RebootRequired, "job_id": j.ID.String(),
			}})
	}
	if len(res.Held) > 0 {
		st.Logf("bleven staan: %s", strings.Join(res.Held, ", "))
	}
	if res.RebootRequired {
		st.Logf("%s vraagt om een herstart", n.Hostname)
	}
	if res.State != protocol.RunOK {
		return errors.New(noderun.Describe(res))
	}
	return nil
}

// reboot herstart de node en wacht tot hij terug is.
func (s *Service) reboot(ctx context.Context, st *jobs.Step, n jobNode) error {
	sent := s.Now()
	st.Logf("%s herstart", n.Hostname)
	_ = st.Flush(ctx)
	res, err := s.bus.Command(ctx, n.ID, protocol.Command{
		ID: uuid.NewString(), Action: protocol.CmdReboot, Deadline: sent.Add(time.Minute), DelaySeconds: 3, Reason: "updates",
	})
	if err != nil {
		return fmt.Errorf("herstarten: %w", err)
	}
	if !res.OK {
		return fmt.Errorf("herstarten: %s", res.Error)
	}
	wctx, cancel := context.WithTimeout(ctx, s.RebootTimeout)
	defer cancel()
	err = health.Poll(wctx, 2*time.Second, func(ctx context.Context) (bool, error) {
		rt, err := s.q.GetNodeRuntime(ctx, n.ID)
		if err != nil {
			return false, err
		}
		up := time.Duration(rt.UptimeSeconds) * time.Second
		return rt.HeartbeatAt != nil && rt.HeartbeatAt.After(sent) && up < s.Now().Sub(sent), nil
	})
	if err != nil {
		return fmt.Errorf("%s kwam na de herstart niet binnen %s terug: %w", n.Hostname, s.RebootTimeout, err)
	}
	st.Logf("%s is terug", n.Hostname)
	return nil
}

// settle wacht tot de diensten van vóór de update weer draaien en de VIP's
// een vaste houder hebben, en vraagt nieuwe facts.
func (s *Service) settle(ctx context.Context, st *jobs.Step, n jobNode, pr prep) error {
	since := s.Now()
	progress := func(msg string) {
		st.Logf("%s", msg)
		_ = st.Flush(ctx)
	}
	st.Logf("wachten op twee gezonde heartbeats van %s", n.Hostname)
	_ = st.Flush(ctx)
	rctx, cancel := context.WithTimeout(ctx, s.ReadyTimeout)
	err := s.Gate.NodeReady(rctx, n.ID, since, pr.Services, progress)
	cancel()
	if err != nil {
		return fmt.Errorf("%s komt niet gezond terug: %w; de taak stopt hier", n.Hostname, err)
	}
	if rt, err := s.q.GetNodeRuntime(ctx, n.ID); err == nil && rt.ClusterID != nil {
		vips, err := s.clusterVIPs(ctx, *rt.ClusterID)
		if err != nil {
			return err
		}
		if len(vips) > 0 {
			sctx, cancel := context.WithTimeout(ctx, s.ReadyTimeout)
			_, err := s.Gate.Settled(sctx, *rt.ClusterID, vips, since, nil, progress)
			cancel()
			if err != nil {
				return fmt.Errorf("de VIP's hebben geen vaste houder: %w; de taak stopt hier", err)
			}
		}
	}
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = s.bus.Command(fctx, n.ID, protocol.Command{ID: uuid.NewString(), Action: protocol.CmdFactsCollect, Deadline: s.Now().Add(30 * time.Second)})
	cancel()
	st.Logf("%s is gezond", n.Hostname)
	return nil
}
