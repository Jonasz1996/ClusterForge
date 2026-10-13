package status

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
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
	// VIPGrace is hoe lang een VIP op meerdere of geen nodes mag staan voor
	// het telt; zie status.VIPGrace.
	VIPGrace time.Duration
	// After rekent verder na de commit van elke ronde, zoals de status van
	// de diensten. Een fout wordt alleen gelogd: de status van nodes en
	// clusters en de VIP-eigenaar staan dan al vast. Zet hem voor Run.
	After func(context.Context) error

	mu sync.Mutex
	// unsettled is per VIP sinds wanneer het niet op precies één node staat.
	unsettled map[uuid.UUID]time.Time
}

func NewEvaluator(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger) *Evaluator {
	return &Evaluator{
		pool: pool, ev: ev, log: log, kick: make(chan struct{}, 1),
		started: time.Now(), Warmup: HeartbeatLate, Now: time.Now, VIPGrace: VIPGrace,
		unsettled: map[uuid.UUID]time.Time{},
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

// Evaluate berekent alles één keer en slaat de wijzigingen op. Daarna, na
// de commit, draait After.
func (e *Evaluator) Evaluate(ctx context.Context) error {
	now := e.Now()
	if now.Sub(e.started) < e.Warmup {
		return nil
	}
	if err := e.evaluate(ctx, now); err != nil {
		return err
	}
	if e.After != nil {
		if err := e.After(ctx); err != nil && ctx.Err() == nil {
			e.log.Error("diensten en incidenten berekenen mislukt", "err", err)
		}
	}
	return nil
}

func (e *Evaluator) evaluate(ctx context.Context, now time.Time) error {
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
		kube, err := e.kube(ctx, q, now)
		if err != nil {
			return err
		}
		health, err := q.ListComponentHealth(ctx)
		if err != nil {
			return err
		}
		dbs := map[uuid.UUID][]DBHealth{}
		for _, h := range health {
			if h.Health != "" && h.HealthAt != nil {
				dbs[h.ClusterID] = append(dbs[h.ClusterID], DBHealth{Status: Status(h.Health), Reason: h.HealthReason, At: *h.HealthAt})
			}
		}
		perCluster := map[uuid.UUID][]ClusterNode{}
		hostnames := map[uuid.UUID]string{}
		for _, n := range nodes {
			hostnames[n.ID] = n.Hostname
			age := time.Duration(-1)
			if n.HeartbeatAt != nil {
				// De database zet heartbeat_at met haar eigen klok. Een
				// heartbeat die net na now binnenkwam, is vers, niet
				// ontbrekend.
				age = max(0, now.Sub(*n.HeartbeatAt))
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
			cn := ClusterNode{ID: n.ID, Hostname: n.Hostname, Counts: n.Lifecycle == store.NodeLifecycleActive, Result: res, HeartbeatAge: age}
			// Een node waarvan de VM uit staat, heeft geen adressen meer,
			// ook al is zijn laatste heartbeat nog jong.
			if n.HasAgent && age >= 0 && age <= HeartbeatDown && n.VmStatus != "stopped" {
				cn.Addresses = n.Addresses
			}
			perCluster[*n.ClusterID] = append(perCluster[*n.ClusterID], cn)
		}
		vipsPerCluster := map[uuid.UUID][]store.ListAllVIPsRow{}
		for _, v := range vips {
			vipsPerCluster[v.ClusterID] = append(vipsPerCluster[v.ClusterID], v)
		}
		seen := map[uuid.UUID]bool{}
		for _, c := range clusters {
			cv := make([]ClusterVIP, len(vipsPerCluster[c.ID]))
			for i, v := range vipsPerCluster[c.ID] {
				cv[i] = ClusterVIP{ID: v.ID, Address: v.Address.String()}
			}
			holders := Holders(perCluster[c.ID], cv, e.VIPGrace)
			for _, v := range vipsPerCluster[c.ID] {
				seen[v.ID] = true
				holders[v.ID] = Settle(holders[v.ID], v.OwnerNodeID, e.unsettledFor(v.ID, len(holders[v.ID]) == 1, now), e.VIPGrace)
			}
			res := ClusterWith(perCluster[c.ID], cv, holders)
			result := res.Result
			if in, ok := kube[c.ID]; ok && c.Type == "kubernetes" {
				result = Kube(result, *in, now)
			}
			if in := dbs[c.ID]; len(in) > 0 {
				result = Database(result, in, now)
			}
			if err := e.saveCluster(ctx, q, c, result); err != nil {
				return err
			}
			for _, v := range vipsPerCluster[c.ID] {
				if err := e.saveVIPOwner(ctx, q, v, res.Holders[v.ID], hostnames); err != nil {
					return err
				}
			}
		}
		e.forget(seen)
		return nil
	})
}

// kube leest per Kubernetes-cluster wat de sync laatst zag, en geeft elke
// app zijn status. Antwoordt de API niet, dan blijven de apps staan zoals
// ze waren: wat de sync laatst zag, is niet meer actueel.
func (e *Evaluator) kube(ctx context.Context, q *store.Queries, now time.Time) (map[uuid.UUID]*KubeInput, error) {
	rows, err := q.ListKubeStatusInputs(ctx)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	out := make(map[uuid.UUID]*KubeInput, len(rows))
	for _, r := range rows {
		out[r.ClusterID] = &KubeInput{Synced: r.Synced, APIOk: r.ApiOk, ErrorKind: r.ErrorKind, Error: r.Error, OKAt: r.OkAt}
	}
	nodes, err := q.ListAllKubeNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if in := out[n.ClusterID]; in != nil {
			in.Nodes = append(in.Nodes, KubeNode{Name: n.Name, Roles: n.Roles, Ready: n.Ready})
		}
	}
	apps, err := q.ListKubeApps(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		in := out[a.ClusterID]
		if in == nil || !in.APIOk {
			continue
		}
		app := App{
			Namespace: a.Namespace, Kind: a.Kind, Name: a.Name, Desired: int(a.Desired), Ready: int(a.Ready), Trouble: a.Trouble,
			ShortSince: a.ShortSince,
		}
		res := AppStatus(app, now)
		in.Apps = append(in.Apps, AppResult{app, res})
		if err := e.saveApp(ctx, q, a, res, now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// saveApp bewaart de status van een app. Wordt hij verminderd of down, dan
// telt hij vanaf het moment dat hij niet meer deed wat hij moest. De eerste
// status van een app geeft geen event, net als bij een dienst.
func (e *Evaluator) saveApp(ctx context.Context, q *store.Queries, a store.ListKubeAppsRow, res Result, now time.Time) error {
	if string(res.Status) == a.Status && res.Reason == a.StatusReason {
		return nil
	}
	since := &now
	switch {
	case string(res.Status) == a.Status:
		since = a.StatusSince
	case a.Status == string(Healthy) || a.Status == string(Unknown):
		if res.Status != Healthy && a.ShortSince != nil {
			since = a.ShortSince
		}
	}
	if err := q.SetKubeAppStatus(ctx, store.SetKubeAppStatusParams{
		ID: a.ID, Status: string(res.Status), StatusReason: res.Reason, StatusSince: since,
	}); err != nil {
		return err
	}
	if string(res.Status) == a.Status || a.Status == string(Unknown) {
		return nil
	}
	id := a.ClusterID
	return e.ev.Write(ctx, q, events.Event{
		Actor: systemActor, SubjectType: "cluster", SubjectID: id.String(), ClusterID: &id,
		Action: "kube.app_status_changed",
		Payload: map[string]any{
			"app_id": a.ID.String(), "kind": a.Kind, "namespace": a.Namespace, "name": a.Name,
			"from": a.Status, "to": res.Status, "reason": res.Reason,
		},
	})
}

// unsettledFor zegt hoe lang een VIP al niet op precies één node staat.
func (e *Evaluator) unsettledFor(id uuid.UUID, settled bool, now time.Time) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if settled {
		delete(e.unsettled, id)
		return 0
	}
	since, ok := e.unsettled[id]
	if !ok {
		e.unsettled[id] = now
		return 0
	}
	return now.Sub(since)
}

// forget ruimt VIP's op die niet meer bestaan.
func (e *Evaluator) forget(seen map[uuid.UUID]bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id := range e.unsettled {
		if !seen[id] {
			delete(e.unsettled, id)
		}
	}
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
	// node_ids zet de wissel ook in de tijdlijn van beide nodes.
	nodeIDs := []string{}
	for _, id := range []*uuid.UUID{v.OwnerNodeID, owner} {
		if id != nil {
			nodeIDs = append(nodeIDs, id.String())
		}
	}
	payload := map[string]any{
		"address": v.Address.String(), "from": v.OwnerNodeID, "to": owner, "owner_hostname": nil, "node_ids": nodeIDs,
	}
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
