package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Shrink is de revisie die een node uit de spec haalt.
type Shrink struct {
	Revision int32
	// Applied: de revisie geldt meteen als toegepast, want op de andere
	// nodes verandert geen stap en de vorige revisie was toegepast.
	Applied bool
	// Changed zijn de nodes waarop een stap verandert; die krijgen de
	// nieuwe revisie pas bij opnieuw toepassen.
	Changed []string
}

// ShrinkTx haalt een node uit de spec van zijn cluster, in transactie q:
// een nieuwe revisie met bron ui, waarin het aantal nodes van zijn rol één
// lager is. Staat de node er al niet meer in, dan verandert er niets en is
// Revision 0.
func (s *Service) ShrinkTx(ctx context.Context, q *store.Queries, actor events.Actor, clusterID, nodeID uuid.UUID) (Shrink, error) {
	c, err := q.LockCluster(ctx, clusterID)
	if err != nil {
		return Shrink{}, err
	}
	inv, states, err := s.Nodes(ctx, clusterID)
	if err != nil {
		return Shrink{}, err
	}
	spec, err := ParseSpec(c.Spec, templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)}, inv)
	if err != nil {
		return Shrink{}, err
	}
	i := slices.IndexFunc(spec.Nodes, func(n SpecNode) bool { return n.NodeID == nodeID })
	if i < 0 {
		return Shrink{}, nil
	}
	gone := spec.Nodes[i]
	tpl, ok := s.Templates.Get(spec.Template.Name, spec.Template.Version)
	if !ok {
		return Shrink{}, fmt.Errorf("%s %s zit niet in deze server", spec.Template.Name, spec.Template.Version)
	}
	param := tpl.CountParam(gone.Role)
	if param == "" {
		return Shrink{}, fmt.Errorf("het aantal nodes in rol %s ligt vast in %s %s", gone.Role, tpl.Name, tpl.Version)
	}
	next := spec
	next.Nodes = slices.Delete(slices.Clone(spec.Nodes), i, i+1)
	next.Params = maps.Clone(spec.Params)
	left := 0
	for _, n := range next.Nodes {
		if n.Role == gone.Role {
			left++
		}
	}
	next.Params[param] = left

	// Verandert er op een andere node een stap, bijvoorbeeld omdat hij zijn
	// peers kent, dan is de nieuwe revisie daar nog niet toegepast.
	var out Shrink
	for _, n := range next.Nodes {
		before, err1 := RenderMasked(tpl, spec, states, n.NodeID)
		after, err2 := RenderMasked(tpl, next, states, n.NodeID)
		if err1 != nil || err2 != nil {
			out.Changed = append(out.Changed, n.Hostname)
			continue
		}
		b1, _ := json.Marshal(before)
		b2, _ := json.Marshal(after)
		if string(b1) != string(b2) {
			out.Changed = append(out.Changed, n.Hostname)
		}
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return Shrink{}, err
	}
	if out.Revision, err = q.SetClusterSpec(ctx, store.SetClusterSpecParams{
		ID: clusterID, Spec: raw, TemplateName: &next.Template.Name, TemplateVersion: &next.Template.Version,
	}); err != nil {
		return Shrink{}, err
	}
	var by *uuid.UUID
	if uid, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
		by = &uid
	}
	if err := q.InsertSpecRevision(ctx, store.InsertSpecRevisionParams{
		ClusterID: clusterID, Revision: out.Revision, Spec: raw, Source: "ui", CreatedBy: by,
	}); err != nil {
		return Shrink{}, err
	}
	if c.AppliedRevision == c.SpecRevision && len(out.Changed) == 0 {
		if err := q.SetAppliedRevision(ctx, store.SetAppliedRevisionParams{ID: clusterID, Revision: out.Revision}); err != nil {
			return Shrink{}, err
		}
		out.Applied = true
	}
	return out, s.ev.Write(ctx, q, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.spec_changed",
		Payload: map[string]any{
			"name": c.Name, "revision": out.Revision, "previous_revision": c.SpecRevision, "source": "ui",
			"template": next.Template.Name, "template_version": next.Template.Version, "removed_node": gone.Hostname,
		},
	})
}
