package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/events"
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
		v   Version
		res []Resource
	)
	api, err := s.api(conn)
	if err == nil {
		v, err = api.Version(ctx)
	}
	if err == nil {
		res, err = api.Resources(ctx)
	}
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return err
		}
		s.saveError(ctx, conn, err)
		return err
	}
	changed, err := s.save(ctx, conn, v, res)
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
// gekoppelde VM's die van toestand of host veranderden. changed is true als
// er zo'n verandering was.
func (s *Service) save(ctx context.Context, conn store.ProxmoxConnection, v Version, res []Resource) (changed bool, err error) {
	now := time.Now()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		linked, err := q.ListLinkedGuests(ctx, &conn.ID)
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
		return nil
	})
	return changed, err
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
