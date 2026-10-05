package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ErrTemplateCluster: een cluster uit een template heeft de template als
// gewenste staat en krijgt geen baseline.
var ErrTemplateCluster = errors.New("dit cluster komt uit een template; de template is zijn gewenste staat")

// ErrNotFound: het cluster of de regel bestaat niet.
var ErrNotFound = errors.New("niet gevonden")

// CaptureInput is een baseline vastleggen, of met Preview alleen tonen wat
// er vastgelegd zou worden.
type CaptureInput struct {
	NodeIDs []uuid.UUID
	Items   BaselineItems
	Preview bool
}

// Captured is de uitkomst per node: de verwachtingen en wat niet
// vastgelegd werd.
type Captured struct {
	Revision int
	Nodes    []CapturedNode
}

type CapturedNode struct {
	NodeID   uuid.UUID
	Hostname string
	Expect   []Expect
	Notes    []string
}

// Capture legt vast wat er nu op de gekozen nodes staat. Nodes die al een
// baseline hebben en niet gekozen zijn, houden de hunne; zo legt Jonas één
// node opnieuw vast na een bewuste wijziging.
func (s *Service) Capture(ctx context.Context, actor events.Actor, clusterID uuid.UUID, in CaptureInput) (Captured, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Captured{}, ErrNotFound
	}
	if err != nil {
		return Captured{}, err
	}
	if c.TemplateName != nil {
		return Captured{}, ErrTemplateCluster
	}
	items, err := in.Items.Validate()
	if err != nil {
		return Captured{}, err
	}
	rows, err := s.q.ListCaptureNodes(ctx, &clusterID)
	if err != nil {
		return Captured{}, err
	}
	if len(in.NodeIDs) == 0 {
		return Captured{}, &FieldError{Field: "node_ids", Message: "kies minstens één node"}
	}
	now := s.Now()
	var chosen []store.ListCaptureNodesRow
	for _, id := range in.NodeIDs {
		i := slices.IndexFunc(rows, func(r store.ListCaptureNodesRow) bool { return r.ID == id })
		if i < 0 {
			return Captured{}, &FieldError{Field: "node_ids", Message: "een gekozen node hoort niet bij dit cluster"}
		}
		n := rows[i]
		busy, err := s.q.NodeJobBusy(ctx, store.NodeJobBusyParams{NodeID: &n.ID, ClusterID: n.ClusterID})
		if err != nil {
			return Captured{}, err
		}
		dn := store.ListDriftNodesRow{
			ID: n.ID, Hostname: n.Hostname, ClusterID: n.ClusterID, Lifecycle: n.Lifecycle,
			AgentProtocol: n.AgentProtocol, HasAgent: n.HasAgent, HeartbeatAt: n.HeartbeatAt,
		}
		if reason := Skip(dn, busy, now); reason != "" {
			return Captured{}, &FieldError{Field: "node_ids", Message: n.Hostname + ": " + reason}
		}
		if !slices.ContainsFunc(chosen, func(r store.ListCaptureNodesRow) bool { return r.ID == n.ID }) {
			chosen = append(chosen, n)
		}
	}

	// Alle nodes tegelijk bekijken, elk met de tijd van Nu controleren.
	out := Captured{Nodes: make([]CapturedNode, len(chosen))}
	var mu sync.Mutex
	var failed []string
	var g errgroup.Group
	for i, n := range chosen {
		g.Go(func() error {
			cn := CapturedNode{NodeID: n.ID, Hostname: n.Hostname}
			cmd := protocol.Command{
				ID: "baseline-" + uuid.NewString(), Action: protocol.CmdInspect, Deadline: time.Now().Add(s.NowTimeout), Inspect: items.CaptureRequest(),
			}
			cctx, cancel := context.WithTimeout(ctx, s.NowTimeout)
			res, err := s.bus.Command(cctx, n.ID, cmd)
			cancel()
			switch {
			case errors.Is(err, agentbus.ErrAgentOffline) || errors.Is(err, agentbus.ErrNoAnswer):
				err = errors.New("agent niet bereikbaar")
			case err == nil && !res.OK:
				err = errors.New("de agent: " + res.Error)
			case err == nil:
				cn.Expect, cn.Notes, err = items.Capture(s.key, res.Observations)
			}
			if err != nil {
				mu.Lock()
				failed = append(failed, n.Hostname+": "+err.Error())
				mu.Unlock()
				return nil
			}
			out.Nodes[i] = cn
			return nil
		})
	}
	_ = g.Wait()
	if len(failed) > 0 {
		slices.Sort(failed)
		return Captured{}, &FieldError{Field: "node_ids", Message: "niet vast te leggen: " + strings.Join(failed, "; ")}
	}
	if in.Preview {
		return out, nil
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockBaseline(ctx, clusterID.String()); err != nil {
			return err
		}
		c, err := q.GetCluster(ctx, clusterID)
		if err != nil {
			return err
		}
		if c.TemplateName != nil {
			return ErrTemplateCluster
		}
		b, err := ParseBaseline(c.Spec)
		if err != nil || b == nil {
			b = &Baseline{}
		}
		previous := int(c.SpecRevision)
		next := Baseline{Kind: "baseline", Items: items, Nodes: []BaseNode{}}
		captured := map[uuid.UUID]CapturedNode{}
		for _, cn := range out.Nodes {
			captured[cn.NodeID] = cn
		}
		// In de volgorde van de inventory; wie niet meer bij het cluster
		// hoort, valt weg.
		for _, r := range rows {
			if cn, ok := captured[r.ID]; ok {
				next.Nodes = append(next.Nodes, BaseNode{NodeID: r.ID, Hostname: r.Hostname, Role: r.Role, CapturedAt: now.UTC(), Expect: cn.Expect})
			} else if old, ok := b.Node(r.ID); ok {
				next.Nodes = append(next.Nodes, old)
			}
		}
		spec, err := json.Marshal(next)
		if err != nil {
			return err
		}
		rev, err := q.SetBaselineSpec(ctx, store.SetBaselineSpecParams{ID: clusterID, Spec: spec})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTemplateCluster
		}
		if err != nil {
			return err
		}
		var by *uuid.UUID
		if id, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
			by = &id
		}
		if err := q.InsertSpecRevision(ctx, store.InsertSpecRevisionParams{
			ClusterID: clusterID, Revision: rev, Spec: spec, Source: "ui", CreatedBy: by,
		}); err != nil {
			return err
		}
		var prev any
		if previous > 0 {
			prev = previous
		}
		if err := s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.spec_changed",
			Payload: map[string]any{"name": c.Name, "revision": rev, "previous_revision": prev, "source": "ui", "kind": "baseline"},
		}); err != nil {
			return err
		}
		hosts := make([]string, 0, len(out.Nodes))
		for _, cn := range out.Nodes {
			hosts = append(hosts, cn.Hostname)
		}
		out.Revision = int(rev)
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "drift.baseline_set",
			Payload: map[string]any{
				"name": c.Name, "revision": rev, "nodes": hosts,
				"packages": items.Packages, "services": items.Services, "files": items.Files,
			},
		})
	})
	if err != nil {
		return Captured{}, err
	}
	// Meteen controleren, zodat de kaart de nieuwe baseline laat zien.
	if err := s.CheckNow(ctx, clusterID, nil); err != nil {
		return out, fmt.Errorf("baseline vastgelegd, maar controleren mislukte: %w", err)
	}
	return out, nil
}

// IgnoreInput is een nieuwe negeerregel.
type IgnoreInput struct {
	NodeID    *uuid.UUID
	Key       string
	Reason    string
	ExpiresAt *time.Time
}

// AddIgnore maakt een negeerregel en rekent de drift van het cluster meteen
// opnieuw door.
func (s *Service) AddIgnore(ctx context.Context, actor events.Actor, clusterID uuid.UUID, in IgnoreInput) (store.DriftIgnore, error) {
	in.Key, in.Reason = strings.TrimSpace(in.Key), strings.TrimSpace(in.Reason)
	if err := ValidateIgnore(in.Key, in.Reason, in.ExpiresAt, s.Now()); err != nil {
		return store.DriftIgnore{}, err
	}
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.DriftIgnore{}, ErrNotFound
	}
	if err != nil {
		return store.DriftIgnore{}, err
	}
	var hostname string
	if in.NodeID != nil {
		ref, err := s.q.GetNodeRef(ctx, *in.NodeID)
		if err != nil || ref.ClusterID == nil || *ref.ClusterID != clusterID {
			return store.DriftIgnore{}, &FieldError{Field: "node_id", Message: "die node hoort niet bij dit cluster"}
		}
		hostname = ref.Hostname
	}
	var by *uuid.UUID
	if id, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
		by = &id
	}
	var out store.DriftIgnore
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		out, err = q.InsertDriftIgnore(ctx, store.InsertDriftIgnoreParams{
			ClusterID: clusterID, NodeID: in.NodeID, Key: in.Key, Reason: in.Reason, ExpiresAt: in.ExpiresAt, CreatedBy: by,
		})
		if err != nil {
			return err
		}
		payload := map[string]any{"name": c.Name, "key": in.Key, "reason": in.Reason, "node": nilIfEmpty(hostname), "expires_at": in.ExpiresAt}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "drift.ignore_added", Payload: payload,
		})
	})
	if err != nil {
		return store.DriftIgnore{}, err
	}
	return out, s.Reevaluate(ctx, clusterID)
}

// RemoveIgnore heft een regel op en rekent de drift opnieuw door.
func (s *Service) RemoveIgnore(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	ig, err := s.q.GetDriftIgnore(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	c, err := s.q.GetCluster(ctx, ig.ClusterID)
	if err != nil {
		return err
	}
	var hostname string
	if ig.NodeID != nil {
		if ref, err := s.q.GetNodeRef(ctx, *ig.NodeID); err == nil {
			hostname = ref.Hostname
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.DeleteDriftIgnore(ctx, id); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: ig.ClusterID.String(), ClusterID: &ig.ClusterID, Action: "drift.ignore_removed",
			Payload: map[string]any{"name": c.Name, "key": ig.Key, "reason": ig.Reason, "node": nilIfEmpty(hostname)},
		})
	})
	if err != nil {
		return err
	}
	return s.Reevaluate(ctx, ig.ClusterID)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
