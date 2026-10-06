package deps

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// statusLock is de advisory lock van de evaluator. De resolver neemt hem
// ook, zodat hij en de impactberekening van mijlpaal 12 nooit tegelijk
// dezelfde rijen bijwerken.
const statusLock = 4242001

// Run zoekt meteen en daarna elke Interval naar voorstellen.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.Resolve(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("diensten voorstellen mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// proposal is een unit die in een cluster draait.
type proposal struct {
	unit, kind string
	hosts      []string
}

// Resolve maakt van bekende units op de actieve nodes van een cluster
// voorgestelde diensten. Hoort een unit in dat cluster al bij een dienst, in
// welke staat ook, dan komt er geen voorstel en schuift alleen last_seen_at
// op. Elk voorstel is een eigen savepoint: één rij die niet lukt, houdt de
// rest niet tegen.
func (s *Service) Resolve(ctx context.Context) error {
	clusters, err := s.q.ListDepClusters(ctx)
	if err != nil {
		return err
	}
	nodes, err := s.q.ListNodeStatusInputs(ctx)
	if err != nil {
		return err
	}
	units := map[uuid.UUID]map[string][]string{} // cluster → unit → hostnames
	for _, n := range nodes {
		if n.ClusterID == nil || n.Lifecycle != store.NodeLifecycleActive {
			continue
		}
		var st map[string]string
		if err := json.Unmarshal(n.Services, &st); err != nil {
			continue
		}
		if units[*n.ClusterID] == nil {
			units[*n.ClusterID] = map[string][]string{}
		}
		for u := range st {
			units[*n.ClusterID][u] = append(units[*n.ClusterID][u], n.Hostname)
		}
	}
	now := s.Now()
	for _, c := range clusters {
		seen := units[c.ID]
		if len(seen) == 0 {
			continue
		}
		var props []proposal
		hasInstance := slices.ContainsFunc(keys(seen), postgresInstance.MatchString)
		for u, hosts := range seen {
			kind, ok := unitKind(u, c.Type)
			// postgresql.service is op Debian een overkoepelende unit die altijd
			// active staat; de instantie zegt meer.
			if !ok || !unitRe.MatchString(u) || (u == "postgresql" && hasInstance) {
				continue
			}
			slices.Sort(hosts)
			props = append(props, proposal{unit: u, kind: kind, hosts: hosts})
		}
		slices.SortFunc(props, func(a, b proposal) int { return strings.Compare(a.unit, b.unit) })
		if err := s.resolveCluster(ctx, c, props, now); err != nil {
			s.log.Error("voorstellen voor cluster mislukt", "cluster", c.Name, "err", err)
		}
	}
	return nil
}

func (s *Service) resolveCluster(ctx context.Context, c store.ListDepClustersRow, props []proposal, now time.Time) error {
	if len(props) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", statusLock); err != nil {
			return err
		}
		q := store.New(tx)
		all, err := q.ListServices(ctx)
		if err != nil {
			return err
		}
		taken := map[string]bool{}
		for _, svc := range all {
			if svc.ClusterID != nil && *svc.ClusterID == c.ID {
				taken["unit:"+svc.Unit] = true
				taken["name:"+strings.ToLower(svc.Name)] = true
			}
		}
		units := make([]string, 0, len(props))
		for _, p := range props {
			units = append(units, p.unit)
		}
		if err := q.TouchServices(ctx, store.TouchServicesParams{SeenAt: &now, ClusterID: &c.ID, Units: units}); err != nil {
			return err
		}
		for _, p := range props {
			if taken["unit:"+p.unit] || taken["name:"+strings.ToLower(p.unit)] {
				continue
			}
			if err := s.propose(ctx, tx, c, p, now); err != nil {
				s.log.Warn("dienst voorstellen mislukt", "cluster", c.Name, "unit", p.unit, "err", err)
			}
		}
		return nil
	})
}

// propose schrijft één voorstel met zijn event in een savepoint.
func (s *Service) propose(ctx context.Context, tx pgx.Tx, c store.ListDepClustersRow, p proposal, now time.Time) error {
	return pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		q := store.New(sp)
		svc, err := q.InsertService(ctx, store.InsertServiceParams{
			ClusterID: &c.ID, Name: p.unit, Kind: p.kind, Unit: p.unit, Source: SourceDiscovered, State: StateSuggested, LastSeenAt: &now,
		})
		if err != nil {
			return err
		}
		payload := servicePayload(c.Name, svc)
		payload["hosts"] = p.hosts
		return s.ev.Write(ctx, q, serviceEvent(events.System(), svc, "service.created", payload))
	})
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
