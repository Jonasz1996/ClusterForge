package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Run synchroniseert alle verbindingen tot ctx stopt.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		s.SyncAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SyncAll synchroniseert alle verbindingen, een paar tegelijk.
func (s *Service) SyncAll(ctx context.Context) {
	conns, err := s.q.ListProxmoxConnections(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("Proxmox-verbindingen ophalen", "err", err)
		}
		return
	}
	var g errgroup.Group
	g.SetLimit(4)
	for _, c := range conns {
		g.Go(func() error {
			_ = s.Sync(ctx, c.ID)
			return nil
		})
	}
	_ = g.Wait()
}

// Sync haalt /cluster/resources op en bewaart het resultaat. Een fout komt
// in last_error van de verbinding en wordt ook teruggegeven.
func (s *Service) Sync(ctx context.Context, id uuid.UUID) error {
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	conn, err := s.q.GetProxmoxConnection(ctx, id)
	if err != nil {
		return translate(err)
	}
	var (
		v     Version
		res   []Resource
		tasks []Task
	)
	api, err := s.api(conn)
	if err == nil {
		v, err = api.Version(ctx)
	}
	if err == nil {
		res, err = api.Resources(ctx)
	}
	if err == nil {
		// Zonder taken werkt de rest gewoon; het journaal mist dan alleen
		// wat er in Proxmox zelf gebeurde.
		var terr error
		if tasks, terr = api.Tasks(ctx); terr != nil && ctx.Err() == nil {
			s.log.Warn("Proxmox-taken lezen mislukt", "proxmox", conn.Name, "err", terr)
		}
	}
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return err
		}
		s.saveError(ctx, conn, err)
		return err
	}
	changed, err := s.save(ctx, conn, v, res, tasks)
	if err != nil {
		s.log.Error("Proxmox-sync bewaren", "proxmox", conn.Name, "err", err)
		return err
	}
	if changed && s.Changed != nil {
		s.Changed()
	}
	return nil
}

func (s *Service) saveError(ctx context.Context, conn store.ProxmoxConnection, syncErr error) {
	msg := syncErr.Error()
	ctx = context.WithoutCancel(ctx)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetProxmoxSyncResult(ctx, store.SetProxmoxSyncResultParams{ID: conn.ID, Error: msg}); err != nil {
			return err
		}
		if conn.LastError != "" {
			return nil
		}
		s.log.Warn("Proxmox-sync mislukt", "proxmox", conn.Name, "err", syncErr)
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "proxmox", SubjectID: conn.ID.String(), Action: "proxmox.sync_failed",
			Payload: map[string]any{"name": conn.Name, "error": msg},
		})
	})
	if err != nil {
		s.log.Error("Proxmox-syncfout bewaren", "proxmox", conn.Name, "err", err)
	}
}

// save vervangt de resources van een verbinding en schrijft events voor
// gekoppelde VM's die van toestand of host veranderden en voor hosts die
// offline of weer online gingen. De taken komen in proxmox_tasks, en een
// afgeronde taak over een gekoppelde VM in het journaal van die node.
// changed is true als er zo'n verandering was.
func (s *Service) save(ctx context.Context, conn store.ProxmoxConnection, v Version, res []Resource, tasks []Task) (changed bool, err error) {
	now := time.Now()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		linked, err := q.ListLinkedGuests(ctx, &conn.ID)
		if err != nil {
			return err
		}
		prevHosts, err := q.ListProxmoxHosts(ctx, conn.ID)
		if err != nil {
			return err
		}
		params := make([]store.UpsertProxmoxResourceParams, 0, len(res))
		seen := map[int]Resource{}
		for _, r := range res {
			switch r.Type {
			case "node", "qemu", "lxc", "storage":
			default:
				continue
			}
			data, err := json.Marshal(r)
			if err != nil {
				return err
			}
			p := store.UpsertProxmoxResourceParams{
				ConnectionID: conn.ID, PveID: r.ID, Type: r.Type, PveNode: r.Node, Name: r.Name, Status: r.Status,
				Template: r.Template == 1, Data: data, SyncedAt: now,
			}
			if r.Type == "qemu" || r.Type == "lxc" {
				p.Vmid = int32p(r.VMID)
				seen[r.VMID] = r
			}
			if r.Type == "storage" {
				p.Name = r.Storage
			}
			params = append(params, p)
		}
		var batchErr error
		q.UpsertProxmoxResource(ctx, params).Exec(func(_ int, err error) {
			if err != nil && batchErr == nil {
				batchErr = err
			}
		})
		if batchErr != nil {
			return batchErr
		}
		if err := q.DeleteStaleProxmoxResources(ctx, store.DeleteStaleProxmoxResourcesParams{ConnectionID: conn.ID, SyncedAt: now}); err != nil {
			return err
		}
		if err := q.SetProxmoxSyncResult(ctx, store.SetProxmoxSyncResultParams{ID: conn.ID, PveVersion: v.Version}); err != nil {
			return err
		}
		if conn.LastError != "" {
			s.log.Info("Proxmox-sync werkt weer", "proxmox", conn.Name)
			if err := s.ev.Write(ctx, q, events.Event{
				Actor: events.System(), SubjectType: "proxmox", SubjectID: conn.ID.String(), Action: "proxmox.sync_recovered",
				Payload: map[string]any{"name": conn.Name},
			}); err != nil {
				return err
			}
		}
		for _, l := range linked {
			e, ok := guestChange(l, seen)
			if !ok {
				continue
			}
			changed = true
			e.ClusterID = l.ClusterID
			if err := s.ev.Write(ctx, q, e); err != nil {
				return err
			}
		}
		for _, e := range hostChanges(conn, prevHosts, res) {
			changed = true
			if err := s.ev.Write(ctx, q, e); err != nil {
				return err
			}
		}
		return s.saveTasks(ctx, q, conn, linked, tasks, now)
	})
	return changed, err
}

// hostChanges vergelijkt de hosts met de vorige sync. Een host die er de
// vorige keer niet was, geeft niets.
func hostChanges(conn store.ProxmoxConnection, prev []store.ListProxmoxHostsRow, res []Resource) []events.Event {
	before := map[string]string{}
	for _, h := range prev {
		before[h.PveNode] = h.Status
	}
	var out []events.Event
	for _, r := range res {
		if r.Type != "node" {
			continue
		}
		old, ok := before[r.Node]
		if !ok || old == r.Status || (old != "online" && r.Status != "online") {
			continue
		}
		action := "proxmox.host_offline"
		if r.Status == "online" {
			action = "proxmox.host_online"
		}
		out = append(out, events.Event{
			Actor: events.System(), SubjectType: "proxmox", SubjectID: conn.ID.String(), Action: action,
			Payload: map[string]any{"name": conn.Name, "host": r.Node, "from": old, "to": r.Status},
		})
	}
	return out
}

// taskRetention is hoe lang Proxmox-taken bewaard worden.
const taskRetention = journal.TaskRetention

// saveTasks bewaart de taken en schrijft een afgeronde taak over een
// gekoppelde VM in het journaal van de node. Een taak die er al stond als
// afgerond, schrijft niets meer.
func (s *Service) saveTasks(ctx context.Context, q *store.Queries, conn store.ProxmoxConnection, linked []store.ListLinkedGuestsRow,
	tasks []Task, now time.Time) error {
	if len(tasks) == 0 {
		return nil
	}
	oldest := now.Add(-taskRetention)
	known, err := q.ListProxmoxTasksSince(ctx, store.ListProxmoxTasksSinceParams{ConnectionID: conn.ID, StartedAt: oldest})
	if err != nil {
		return err
	}
	ended := map[string]bool{}
	for _, t := range known {
		if t.EndedAt != nil {
			ended[t.Upid] = true
		}
	}
	nodes := map[int]store.ListLinkedGuestsRow{}
	for _, l := range linked {
		nodes[int(l.Vmid)] = l
	}
	var params []store.UpsertProxmoxTaskParams
	var entries []journal.Entry
	for _, t := range tasks {
		started := time.Unix(t.StartTime, 0)
		if t.UPID == "" || t.StartTime == 0 || started.Before(oldest) || len(t.UPID) > 500 {
			continue
		}
		p := store.UpsertProxmoxTaskParams{
			ConnectionID: conn.ID, Upid: t.UPID, PveNode: limit(t.Node, 100), Type: limit(t.Type, 50),
			Username: limit(t.User, 200), StartedAt: started,
		}
		if vmid := t.VMID(); vmid > 0 {
			p.Vmid = int32p(vmid)
		}
		if t.EndTime > 0 {
			end := time.Unix(t.EndTime, 0)
			p.EndedAt, p.Exitstatus = &end, limit(t.Status, 500)
		}
		params = append(params, p)
		if t.EndTime == 0 || ended[t.UPID] || !journal.VMTaskType(t.Type) {
			continue
		}
		l, ok := nodes[t.VMID()]
		if !ok {
			continue
		}
		e := journal.Entry{
			NodeID: l.NodeID, ClusterID: l.ClusterID, TS: started, Source: journal.SourceProxmox, Kind: journal.KindVM,
			Subject: strconv.Itoa(t.VMID()), Action: t.Type, By: t.User,
			Detail: map[string]string{"host": t.Node, "status": t.Status, "upid": t.UPID},
		}
		if t.User == conn.TokenID {
			e.Detail["clusterforge"] = "true"
		}
		entries = append(entries, journal.Sanitize(e))
	}
	var batchErr error
	q.UpsertProxmoxTask(ctx, params).Exec(func(_ int, err error) {
		if err != nil && batchErr == nil {
			batchErr = err
		}
	})
	if batchErr != nil {
		return batchErr
	}
	return journal.Record(ctx, q, entries...)
}

func limit(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// guestChange vergelijkt een gekoppelde VM met de vorige sync.
func guestChange(l store.ListLinkedGuestsRow, seen map[int]Resource) (events.Event, bool) {
	e := events.Event{Actor: events.System(), SubjectType: "node", SubjectID: l.NodeID.String()}
	r, found := seen[int(l.Vmid)]
	switch {
	case l.Status == nil:
		// Bij de vorige sync was de VM er niet; nieuw gekoppeld of terug.
		return e, false
	case !found:
		e.Action = "vm.missing"
		e.Payload = map[string]any{"hostname": l.Hostname, "vmid": l.Vmid}
	case r.Status != *l.Status:
		e.Action = "vm.status_changed"
		e.Payload = map[string]any{"hostname": l.Hostname, "vmid": l.Vmid, "from": *l.Status, "to": r.Status}
	case l.PveNode != nil && r.Node != *l.PveNode:
		e.Action = "vm.moved"
		e.Payload = map[string]any{"hostname": l.Hostname, "vmid": l.Vmid, "from": *l.PveNode, "to": r.Node}
	default:
		return e, false
	}
	return e, true
}
