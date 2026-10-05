package status

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Interval is hoe vaak de evaluator alles opnieuw berekent als er niets
// gebeurt; een heartbeat of facts laat hem ook meteen rekenen.
const Interval = 5 * time.Second

// Evaluator houdt de status van nodes en clusters en de eigenaar van elk VIP
// bij, en schrijft een event bij elke wijziging.
type Evaluator struct {
	pool    *pgxpool.Pool
	ev      *events.Writer
	log     *slog.Logger
	kick    chan struct{}
	started time.Time
	// Warmup is de tijd na het starten waarin niets wordt opgeslagen: agents
	// moeten na een herstart van de server eerst opnieuw verbinden, anders
	// lijkt elke node even down.
	Warmup time.Duration
	// Now is de klok; tests zetten hem vooruit.
	Now func() time.Time
}

func NewEvaluator(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger) *Evaluator {
	return &Evaluator{
		pool: pool, ev: ev, log: log, kick: make(chan struct{}, 1),
		started: time.Now(), Warmup: HeartbeatLate, Now: time.Now,
	}
}

// Kick vraagt om een nieuwe berekening zonder te wachten.
func (e *Evaluator) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// Run rekent tot ctx afloopt.
func (e *Evaluator) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if err := e.Evaluate(ctx); err != nil && ctx.Err() == nil {
			e.log.Error("status berekenen mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.kick:
			// Een reeks heartbeats tegelijk geeft één berekening.
			time.Sleep(200 * time.Millisecond)
		}
	}
}

var systemActor = events.System()

// Evaluate berekent alles één keer en slaat de wijzigingen op.
func (e *Evaluator) Evaluate(ctx context.Context) error {
	now := e.Now()
	if now.Sub(e.started) < e.Warmup {
		return nil
	}
	return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// Eén evaluator tegelijk, ook als er ooit twee servers draaien.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(4242001)"); err != nil {
			return err
		}
		nodes, err := q.ListNodeStatusInputs(ctx)
		if err != nil {
			return err
		}
		clusters, err := q.ListClusterStatuses(ctx)
		if err != nil {
			return err
		}
		vips, err := q.ListAllVIPs(ctx)
		if err != nil {
			return err
		}
		perCluster := map[uuid.UUID][]ClusterNode{}
		hostnames := map[uuid.UUID]string{}
		for _, n := range nodes {
			hostnames[n.ID] = n.Hostname
			age := time.Duration(-1)
			if n.HeartbeatAt != nil {
				age = now.Sub(*n.HeartbeatAt)
			}
			in := NodeInput{
				HasAgent: n.HasAgent, HeartbeatAge: age, DiskUsedRatio: n.DiskUsedRatio, DiskUsedMount: n.DiskUsedMount,
				VMStatus: n.VmStatus,
			}
			_ = json.Unmarshal(n.Services, &in.Services)
			_ = json.Unmarshal(n.EnabledServices, &in.EnabledServices)
			res := Node(in)
			if err := e.saveNode(ctx, q, n, res); err != nil {
				return err
			}
			if n.ClusterID == nil {
				continue
			}
			cn := ClusterNode{ID: n.ID, Hostname: n.Hostname, Counts: n.Lifecycle == store.NodeLifecycleActive, Result: res}
			if n.HasAgent && age >= 0 && age <= HeartbeatDown {
				cn.Addresses = n.Addresses
			}
			perCluster[*n.ClusterID] = append(perCluster[*n.ClusterID], cn)
		}
		vipsPerCluster := map[uuid.UUID][]store.ListAllVIPsRow{}
		for _, v := range vips {
			vipsPerCluster[v.ClusterID] = append(vipsPerCluster[v.ClusterID], v)
		}
		for _, c := range clusters {
			cv := make([]ClusterVIP, len(vipsPerCluster[c.ID]))
			for i, v := range vipsPerCluster[c.ID] {
				cv[i] = ClusterVIP{ID: v.ID, Address: v.Address.String()}
			}
			res := Cluster(perCluster[c.ID], cv)
			if err := e.saveCluster(ctx, q, c, res.Result); err != nil {
				return err
			}
			for _, v := range vipsPerCluster[c.ID] {
				if err := e.saveVIPOwner(ctx, q, v, res.Holders[v.ID], hostnames); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (e *Evaluator) saveNode(ctx context.Context, q *store.Queries, n store.ListNodeStatusInputsRow, res Result) error {
	if string(res.Status) == n.Status && res.Reason == n.StatusReason {
		return nil
	}
	if err := q.SetNodeStatus(ctx, store.SetNodeStatusParams{ID: n.ID, Status: string(res.Status), StatusReason: res.Reason}); err != nil {
		return err
	}
	if string(res.Status) == n.Status {
		return nil
	}
	return e.ev.Write(ctx, q, events.Event{
		Actor: systemActor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: n.ClusterID,
		Action:  "node.status_changed",
		Payload: map[string]any{"hostname": n.Hostname, "from": n.Status, "to": res.Status, "reason": res.Reason},
	})
}

func (e *Evaluator) saveCluster(ctx context.Context, q *store.Queries, c store.ListClusterStatusesRow, res Result) error {
	if string(res.Status) == c.Status && res.Reason == c.StatusReason {
		return nil
	}
	if err := q.SetClusterStatus(ctx, store.SetClusterStatusParams{ID: c.ID, Status: string(res.Status), StatusReason: res.Reason}); err != nil {
		return err
	}
	if string(res.Status) == c.Status {
		return nil
	}
	id := c.ID
	return e.ev.Write(ctx, q, events.Event{
		Actor: systemActor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &id,
		Action:  "cluster.status_changed",
		Payload: map[string]any{"name": c.Name, "from": c.Status, "to": res.Status, "reason": res.Reason},
	})
}

// saveVIPOwner maakt de node die het adres heeft eigenaar. Hebben meerdere
// nodes het (split-brain), dan blijft de huidige eigenaar staan als hij er
// een van is; zo springt het VIP niet bij elke berekening heen en weer.
func (e *Evaluator) saveVIPOwner(ctx context.Context, q *store.Queries, v store.ListAllVIPsRow, holders []uuid.UUID, hostnames map[uuid.UUID]string) error {
	var owner *uuid.UUID
	switch {
	case len(holders) == 0:
	case v.OwnerNodeID != nil && slices.Contains(holders, *v.OwnerNodeID):
		owner = v.OwnerNodeID
	default:
		owner = &holders[0]
	}
	if sameID(owner, v.OwnerNodeID) {
		return nil
	}
	if err := q.SetVIPOwner(ctx, store.SetVIPOwnerParams{ID: v.ID, OwnerNodeID: owner}); err != nil {
		return err
	}
	cid := v.ClusterID
	payload := map[string]any{"address": v.Address.String(), "from": v.OwnerNodeID, "to": owner, "owner_hostname": nil}
	if owner != nil {
		payload["owner_hostname"] = hostnames[*owner]
	}
	return e.ev.Write(ctx, q, events.Event{
		Actor: systemActor, SubjectType: "vip", SubjectID: v.ID.String(), ClusterID: &cid,
		Action: "vip.owner_changed", Payload: payload,
	})
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
