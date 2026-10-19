package heal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Bus praat met de agents: databaseverzoeken en de deploystappen van een
// herstel.
type Bus interface {
	DB(ctx context.Context, nodeID uuid.UUID, req protocol.DBRequest) (protocol.DBResponse, error)
	deploy.Commander
}

// parallel is hoeveel nodes Inspect tegelijk vraagt.
const parallel = 8

// Service stelt vast wat er mis is en houdt per node het laatste antwoord
// van heal inspect bij.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	bus  Bus
	log  *slog.Logger
	ev   *events.Writer
	// runner voert heal.run uit; nil tot EnableRun.
	runner *jobs.Runner
	// Overview geeft de databases van de pagina Databases.
	Overview func(ctx context.Context) ([]databases.Group, error)
	// Interval is hoe vaak Run de nodes bekijkt; nul of minder zet het uit.
	Interval time.Duration
	// Timeout is hoe lang een node mag doen over zijn antwoord.
	Timeout time.Duration
	Now     func() time.Time
	// Poll is hoe vaak een herstel kijkt of een node er weer is; Retry de
	// wachttijd als de agent even weg is. StartTimeout is hoe lang een
	// gestarte server mag doen over zijn eerste antwoord, JoinTimeout hoe
	// lang een Galera-node over aansluiten, met een volledige kopie.
	Poll         time.Duration
	Retry        time.Duration
	StartTimeout time.Duration
	JoinTimeout  time.Duration
	// Changed wordt aangeroepen na een herstel, zoals de evaluator.
	Changed func()
	// AutoAfter is hoe lang een probleem moet bestaan voor Zelfherstel het
	// vanzelf herstelt: een herstart door systemd is dan al klaar.
	AutoAfter time.Duration

	mu    sync.Mutex
	cache map[uuid.UUID]inspected
	// seen is per database sinds wanneer er een plan is dat vanzelf mag.
	seen map[string]time.Time
	kick chan struct{}
}

type inspected struct {
	at      time.Time
	engines []protocol.HealEngine
	err     string
}

func NewService(pool *pgxpool.Pool, bus Bus, overview func(context.Context) ([]databases.Group, error), log *slog.Logger) *Service {
	return &Service{
		pool: pool, q: store.New(pool), bus: bus, log: log, Overview: overview, Interval: time.Minute, Timeout: protocol.DBTimeout, Now: time.Now,
		Poll: 2 * time.Second, Retry: 5 * time.Second, StartTimeout: 5 * time.Minute, JoinTimeout: 12 * time.Hour, AutoAfter: 2 * time.Minute,
		cache: map[uuid.UUID]inspected{}, seen: map[string]time.Time{}, kick: make(chan struct{}, 1),
	}
}

// Kick laat Run meteen opnieuw kijken.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run bekijkt de nodes elke Interval, tot ctx afloopt.
func (s *Service) Run(ctx context.Context) {
	if s.Interval <= 0 {
		return
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.InspectAll(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("zelfherstel: nodes bekijken mislukt", "err", err)
		} else if err := s.AutoRun(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("zelfherstel: vanzelf herstellen mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// InspectAll vraagt elke node met een databaseserver heal inspect.
func (s *Service) InspectAll(ctx context.Context) error {
	groups, err := s.Overview(ctx)
	if err != nil {
		return err
	}
	nodes, err := s.q.ListHealNodes(ctx)
	if err != nil {
		return err
	}
	want := map[uuid.UUID]bool{}
	for _, g := range groups {
		for _, srv := range g.Servers {
			want[srv.NodeID] = true
		}
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, n := range nodes {
		if !want[n.ID] {
			continue
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			res := s.inspect(ctx, n)
			s.mu.Lock()
			s.cache[n.ID] = res
			s.mu.Unlock()
		})
	}
	wg.Wait()
	s.mu.Lock()
	for id := range s.cache {
		if !want[id] {
			delete(s.cache, id)
		}
	}
	s.mu.Unlock()
	return ctx.Err()
}

// InspectNode vraagt één node opnieuw, zoals tijdens een herstel.
func (s *Service) InspectNode(ctx context.Context, id uuid.UUID) ([]protocol.HealEngine, error) {
	nodes, err := s.q.ListHealNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.ID == id {
			res := s.inspect(ctx, n)
			s.mu.Lock()
			s.cache[n.ID] = res
			s.mu.Unlock()
			if res.err != "" {
				return nil, errors.New(res.err)
			}
			return res.engines, nil
		}
	}
	return nil, errors.New("deze node heeft geen agent")
}

func (s *Service) inspect(ctx context.Context, n store.ListHealNodesRow) inspected {
	now := s.Now()
	res := inspected{at: now}
	switch {
	case n.AgentProtocol < protocol.HealSince:
		res.err = fmt.Sprintf("de agent op %s heeft protocol %d; Zelfherstel vraagt protocol %d: werk hem bij met het installatiescript en --upgrade",
			n.Hostname, n.AgentProtocol, protocol.HealSince)
		return res
	case n.HeartbeatAt == nil || now.Sub(*n.HeartbeatAt) > status.HeartbeatLate:
		res.err = "de agent op " + n.Hostname + " is niet verbonden"
		return res
	}
	rctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	r, err := s.bus.DB(rctx, n.ID, protocol.DBRequest{
		ID: uuid.NewString(), Op: protocol.DBHeal, Deadline: now.Add(s.Timeout), Heal: &protocol.DBHealRequest{Action: protocol.HealInspect},
	})
	switch {
	case errors.Is(err, agentbus.ErrAgentOffline):
		res.err = "de agent op " + n.Hostname + " is niet verbonden"
	case errors.Is(err, agentbus.ErrNoAnswer):
		res.err = "de agent op " + n.Hostname + " antwoordde niet op tijd"
	case err != nil:
		res.err = err.Error()
	case r.Error != "":
		res.err = r.Error
	case r.Heal == nil:
		res.err = "de agent gaf geen antwoord op heal inspect"
	default:
		res.engines = r.Heal.Engines
	}
	return res
}

// Reports maakt het rapport van elke database, met wat de agents het laatst
// zeiden.
func (s *Service) Reports(ctx context.Context) ([]Report, error) {
	out, _, err := s.reports(ctx)
	return out, err
}

// reports geeft de rapporten met de invoer waaruit ze komen, in dezelfde
// volgorde, en zegt per cluster of het plan nu vanzelf mag.
func (s *Service) reports(ctx context.Context) ([]Report, []Input, error) {
	groups, err := s.Overview(ctx)
	if err != nil {
		return nil, nil, err
	}
	in, err := s.inputs(ctx, groups)
	if err != nil {
		return nil, nil, err
	}
	last, err := s.lastRuns(ctx)
	if err != nil {
		return nil, nil, err
	}
	autos, err := s.q.ListHealAuto(ctx)
	if err != nil {
		return nil, nil, err
	}
	settings := map[uuid.UUID]store.HealAuto{}
	for _, a := range autos {
		settings[a.ClusterID] = a
	}
	now := s.Now()
	out := make([]Report, 0, len(in))
	for _, x := range in {
		r := Diagnose(x)
		r.Last = last[r.Key]
		// Een onderdeel van ClusterForge heeft zijn eigen Vanzelf opnieuw
		// starten.
		if r.ClusterID != nil && x.Managed == nil {
			a := settings[*r.ClusterID]
			st, err := s.autoState(ctx, r, a, now)
			if err != nil {
				return nil, nil, err
			}
			r.Auto = &st
		}
		out = append(out, r)
	}
	return out, in, nil
}

// lastRuns is de laatste taak heal.run per database.
func (s *Service) lastRuns(ctx context.Context) (map[string]*LastRun, error) {
	rows, err := s.q.ListHealJobs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*LastRun, len(rows))
	for _, r := range rows {
		out[r.Key] = &LastRun{JobID: r.ID, Status: string(r.Status), Error: r.Error, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt}
	}
	return out, nil
}

// Report maakt het rapport van één database, met de sleutel van de groep.
func (s *Service) Report(ctx context.Context, key string) (Report, Input, error) {
	groups, err := s.Overview(ctx)
	if err != nil {
		return Report{}, Input{}, err
	}
	for _, g := range groups {
		if g.Key() != key {
			continue
		}
		in, err := s.inputs(ctx, []databases.Group{g})
		if err != nil {
			return Report{}, Input{}, err
		}
		return Diagnose(in[0]), in[0], nil
	}
	return Report{}, Input{}, ErrNotFound
}

// ErrNotFound: er is geen database met die sleutel.
var ErrNotFound = errors.New("deze database bestaat niet (meer)")

func (s *Service) inputs(ctx context.Context, groups []databases.Group) ([]Input, error) {
	nodes, err := s.q.ListHealNodes(ctx)
	if err != nil {
		return nil, err
	}
	vips, err := s.q.ListVIPAddresses(ctx)
	if err != nil {
		return nil, err
	}
	comps, err := s.q.ListWatchedComponents(ctx)
	if err != nil {
		return nil, err
	}
	managed := map[string]*Managed{}
	for _, c := range comps {
		managed[c.Engine+"/"+c.ClusterID.String()] = &Managed{ComponentID: c.ID, Title: databases.EngineName(c.Engine)}
	}
	byID := map[uuid.UUID]store.ListHealNodesRow{}
	known := map[string]string{}
	for _, n := range nodes {
		byID[n.ID] = n
		for _, a := range n.Addresses {
			known[a] = n.Hostname
		}
	}
	s.mu.Lock()
	cache := make(map[uuid.UUID]inspected, len(s.cache))
	for k, v := range s.cache {
		cache[k] = v
	}
	s.mu.Unlock()
	now := s.Now()
	out := make([]Input, 0, len(groups))
	for _, g := range groups {
		in := Input{Group: g, Nodes: map[uuid.UUID]Node{}, VIPs: vips, Known: known}
		if g.ClusterID != nil {
			in.Managed = managed[g.Engine+"/"+g.ClusterID.String()]
		}
		for _, srv := range g.Servers {
			n := byID[srv.NodeID]
			node := Node{ID: srv.NodeID, Hostname: srv.Hostname, Protocol: int(n.AgentProtocol), Addresses: n.Addresses}
			node.Online = n.HeartbeatAt != nil && now.Sub(*n.HeartbeatAt) <= status.HeartbeatLate
			if n.PrimaryIp != nil {
				node.PrimaryIP = n.PrimaryIp.String()
			}
			c, ok := cache[srv.NodeID]
			switch {
			case !ok:
				node.InspectError = "nog niet bekeken; Zelfherstel kijkt elke minuut"
			case c.err != "":
				node.InspectError = c.err
			default:
				for i := range c.engines {
					if c.engines[i].Engine == g.Engine {
						e := c.engines[i]
						node.Inspect = &e
					}
				}
				if node.Inspect == nil {
					node.InspectError = "de agent ziet geen " + strings.ToLower(databases.EngineName(g.Engine)) + " meer op deze node"
				}
			}
			in.Nodes[srv.NodeID] = node
		}
		out = append(out, in)
	}
	return out, nil
}

// InspectedAt is wanneer de agent van een node het laatst antwoordde op
// heal inspect; nul als nog nooit.
func (s *Service) InspectedAt(id uuid.UUID) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cache[id].at
}
