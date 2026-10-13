package components

// De bewaking van de databases. Om de twintig seconden vraagt ClusterForge
// elke datanode van een toegepast Galera-onderdeel waar hij staat. Zit er
// een in een Primary component, dan draait de database; een node die er
// langer dan vijf minuten niet bij is, komt in het logboek. Zit er geen
// enkele in een Primary component, dan is de database weg, en begint
// ClusterForge hem opnieuw zodra dat veilig kan:
//
//   - alle datanodes antwoorden, want een node die ontbreekt kan de
//     nieuwste data hebben;
//   - op geen enkele node draait MariaDB nog, want die zou nog kunnen
//     aansluiten (na een netwerkstoring vinden de nodes elkaar zelf terug);
//   - geen enkele node is nog aan het starten, want na een stroomuitval van
//     alle nodes vindt Galera zelf de laatste Primary component terug;
//     blijft een node langer dan HungAfter hangen, dan wacht ClusterForge
//     niet meer;
//   - MariaDB startte ergens niet (failed): staat hij overal gewoon uit,
//     dan zette iemand hem uit, en blijft hij uit;
//   - de database is al RecoverGrace weg, en ClusterForge begon niet al in
//     de laatste AutoBackoff.
//
// Hetzelfde voor een enkele node die niet startte terwijl het cluster
// draait: na RecoverGrace start ClusterForge MariaDB daar opnieuw. Wie het
// niet vanzelf wil, zet het per database uit.
//
// PostgreSQL met Patroni bewaakt ClusterForge net zo, met de snelle status
// van elke datanode: zonder primary is de database weg. Opnieuw beginnen
// doet Patroni zelf.
//
// Bij elke ronde legt de bewaking vast hoe het met de database gaat
// (health.go), voor de status van het cluster en dus de incidenten, en
// zet ze een herstart, een failover of een state transfer in het logboek.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// nodeOut is een datanode die niet meedoet terwijl het cluster draait.
type nodeOut struct {
	Hostname string    `json:"hostname"`
	Since    time.Time `json:"since"`
	Reason   string    `json:"reason"`
	// Reported: het event cluster.database_node_out is geschreven.
	Reported bool `json:"reported"`
}

// Run bewaakt de databases elke WatchInterval, en begint de nachtelijke
// dumps die aan de beurt zijn, tot ctx afloopt.
func (s *Service) Run(ctx context.Context) {
	if s.WatchInterval <= 0 {
		return
	}
	t := time.NewTicker(s.WatchInterval)
	defer t.Stop()
	for {
		if err := s.CheckAll(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("databases bewaken mislukt", "err", err)
		}
		if err := s.RunBackups(ctx, s.Now()); err != nil && ctx.Err() == nil {
			s.log.Warn("nachtelijke dumps plannen mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// CheckAll kijkt een keer naar elke toegepaste database.
func (s *Service) CheckAll(ctx context.Context) error {
	rows, err := s.q.ListWatchedComponents(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rows {
		if err := s.check(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.ClusterName, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) check(ctx context.Context, w store.ListWatchedComponentsRow) error {
	// Loopt er een taak in het cluster, dan zijn de nodes misschien met
	// opzet even weg.
	if _, err := s.q.GetClusterSlotJob(ctx, w.ClusterID); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	spec, err := parseSpec(w.Spec)
	if err != nil {
		return err
	}
	slug := spec.Cluster.Slug
	var all []obs
	var nodes []dbNode
	if w.Engine == protocol.DBPostgreSQL {
		nodes = s.observePatroni(ctx, spec)
	} else {
		all = s.observe(ctx, dataNodes(spec, nil), false)
		nodes = galeraNodes(all, slug)
	}
	now := s.Now()
	ev := func(q *store.Queries, action string, payload map[string]any) error {
		payload["name"], payload["component"] = w.ClusterName, title(spec)
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "cluster", SubjectID: w.ClusterID.String(), ClusterID: &w.ClusterID, Action: action, Payload: payload,
		})
	}
	if err := s.watched(ctx, w, spec, nodes, now, ev); err != nil {
		return err
	}
	if !up(w.Engine, nodes) {
		if w.Engine == protocol.DBPostgreSQL {
			// Zonder de naam ervoor, zoals bij Galera.
			_, reason := dbHealth(healthInput{engine: w.Engine, spec: spec, nodes: nodes})
			return s.markDown(ctx, w, strings.TrimPrefix(reason, title(spec)+": "), now, ev)
		}
		return s.down(ctx, w, all, slug, now, ev)
	}

	out := map[string]nodeOut{}
	_ = json.Unmarshal(w.NodesOut, &out)
	next := map[string]nodeOut{}
	var join []string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if w.DownSince != nil {
			if err := ev(q, "cluster.database_up", map[string]any{"since": *w.DownSince, "seconds": int64(now.Sub(*w.DownSince).Seconds())}); err != nil {
				return err
			}
		}
		for i, d := range nodes {
			id := d.node.NodeID.String()
			prev, known := out[id]
			if d.synced {
				if known && prev.Reported {
					if err := ev(q, "cluster.database_node_back", map[string]any{"hostname": d.node.Hostname, "since": prev.Since}); err != nil {
						return err
					}
				}
				continue
			}
			if !known {
				prev = nodeOut{Hostname: d.node.Hostname, Since: now}
			}
			prev.Reason = d.state
			// Een node die in de Primary component zit maar niet Synced is,
			// zoals een donor, doet wel mee.
			if !prev.Reported && !d.member && now.Sub(prev.Since) >= s.NodeOutAfter {
				if err := ev(q, "cluster.database_node_out", map[string]any{"hostname": d.node.Hostname, "since": prev.Since, "reason": prev.Reason}); err != nil {
					return err
				}
				prev.Reported = true
			}
			next[id] = prev
			// MariaDB startte niet op deze node, terwijl het cluster draait.
			if all != nil && all[i].answered() && all[i].pos.Active == "failed" && now.Sub(prev.Since) >= s.RecoverGrace {
				join = append(join, d.node.Hostname)
			}
		}
		b, _ := json.Marshal(next)
		if was, _ := json.Marshal(out); w.DownSince == nil && string(was) == string(b) {
			return nil
		}
		return q.SetClusterComponentHealth(ctx, store.SetClusterComponentHealthParams{ID: w.ID, NodesOut: b})
	})
	if err != nil {
		return err
	}
	if len(join) > 0 && w.AutoRecover && s.backoffOver(w, now) {
		reason := fmt.Sprintf("MariaDB startte niet op %s terwijl het cluster draait", joinHosts(join))
		return s.autoRecover(ctx, w, reason)
	}
	return nil
}

// observePatroni vraagt alle datanodes van Patroni tegelijk de snelle
// status.
func (s *Service) observePatroni(ctx context.Context, spec Spec) []dbNode {
	nodes := dataNodes(spec, nil)
	out := make([]dbNode, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			srv, err := s.quickServer(ctx, n.NodeID, protocol.DBPostgreSQL)
			out[i] = patroniNode(n, srv, err, spec.Cluster.Slug)
		})
	}
	wg.Wait()
	return out
}

// watched legt vast hoe het met de database gaat en wat de bewaking zag, en
// zet een herstart, een failover of een state transfer in het logboek.
func (s *Service) watched(ctx context.Context, w store.ListWatchedComponentsRow, spec Spec, nodes []dbNode, now time.Time,
	ev func(*store.Queries, string, map[string]any) error) error {
	var prev observed
	_ = json.Unmarshal(w.Observed, &prev)
	cur := observeNodes(w.Engine, nodes, now)
	in, err := s.healthInput(ctx, w.Engine, spec, nodes, now)
	if err != nil {
		return err
	}
	if prev.Primary == cur.Primary && now.Sub(prev.At) <= observedGap {
		in.stablePrimary = cur.Primary
	}
	health, reason := dbHealth(in)
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		for _, e := range changes(w.Engine, prev, cur) {
			if err := ev(q, e.action, e.payload); err != nil {
				return err
			}
		}
		return q.SetClusterComponentWatch(ctx, store.SetClusterComponentWatchParams{
			ID: w.ID, Health: string(health), HealthReason: reason, HealthAt: &now, Observed: b,
		})
	})
	// Een andere toestand: de status van het cluster meteen opnieuw.
	if err == nil && (string(health) != w.Health || reason != w.HealthReason) && s.Changed != nil {
		s.Changed()
	}
	return err
}

// markDown legt vast dat de database weg is, met een event de eerste keer.
func (s *Service) markDown(ctx context.Context, w store.ListWatchedComponentsRow, reason string, now time.Time,
	ev func(*store.Queries, string, map[string]any) error) error {
	if w.DownSince != nil && w.DownReason == reason {
		return nil
	}
	since := now
	if w.DownSince != nil {
		since = *w.DownSince
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if w.DownSince == nil {
			spec, err := parseSpec(w.Spec)
			if err != nil {
				return err
			}
			if err := ev(q, "cluster.database_down", map[string]any{"reason": reason, "nodes": spec.hosts(RoleData)}); err != nil {
				return err
			}
		}
		return q.SetClusterComponentHealth(ctx, store.SetClusterComponentHealthParams{ID: w.ID, DownSince: &since, DownReason: reason, NodesOut: []byte("{}")})
	})
}

// down legt vast dat Galera weg is, en begint het opnieuw als dat veilig
// kan.
func (s *Service) down(ctx context.Context, w store.ListWatchedComponentsRow, all []obs, slug string, now time.Time,
	ev func(*store.Queries, string, map[string]any) error) error {
	var parts []string
	for _, o := range all {
		parts = append(parts, o.node.Hostname+": "+o.describe(slug))
	}
	reason := "geen enkele node zit in een Primary component; " + strings.Join(parts, "; ")
	since := now
	if w.DownSince != nil {
		since = *w.DownSince
	}
	if err := s.markDown(ctx, w, reason, now, ev); err != nil {
		return err
	}
	if !w.AutoRecover || !s.backoffOver(w, now) {
		return nil
	}
	failed, starting := 0, 0
	for _, o := range all {
		switch {
		case !o.answered(), o.pos.Running:
			return nil
		case o.starting():
			starting++
		case o.pos.Active == "failed":
			failed++
		}
	}
	gone := now.Sub(since)
	switch {
	case failed == 0 && starting == 0:
		// Overal gewoon uit: met opzet.
		return nil
	case starting > 0 && gone < s.HungAfter, gone < s.RecoverGrace:
		return nil
	}
	return s.autoRecover(ctx, w, reason)
}

func (s *Service) backoffOver(w store.ListWatchedComponentsRow, now time.Time) bool {
	return w.AutoRecoveredAt == nil || now.Sub(*w.AutoRecoveredAt) >= s.AutoBackoff
}

func (s *Service) autoRecover(ctx context.Context, w store.ListWatchedComponentsRow, reason string) error {
	_, err := s.enqueueRecover(ctx, events.System(), w.ClusterID, w.ID, recoverParams{ComponentID: w.ID, Auto: true}, reason)
	var ce *ConflictError
	if errors.As(err, &ce) {
		// Er kwam net een taak in het cluster; de volgende ronde kijkt
		// opnieuw.
		return nil
	}
	if err == nil {
		s.log.Info("database vanzelf opnieuw starten", "cluster", w.ClusterName, "reason", reason)
	}
	return err
}
