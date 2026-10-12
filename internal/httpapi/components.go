package httpapi

import (
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// ListComponentTemplates geeft de databases die een cluster erbij kan
// krijgen, met hun instellingen.
func (s *Server) ListComponentTemplates(w http.ResponseWriter, r *http.Request) {
	items := []gen.ComponentTemplate{}
	for _, engine := range slices.Sorted(maps.Keys(components.Templates)) {
		t, ok := components.Registry().Latest(components.Templates[engine])
		if !ok {
			continue
		}
		out := gen.ComponentTemplate{
			Engine: gen.ComponentTemplateEngine(engine), Name: t.Name, Version: t.Version, Title: t.Title,
			Description: strings.TrimSpace(t.Description), Params: []gen.TemplateParam{},
		}
		for _, p := range t.Params {
			out.Params = append(out.Params, toAPIParam(p))
		}
		items = append(items, out)
	}
	writeJSON(w, http.StatusOK, list[gen.ComponentTemplate]{items})
}

// ListClusterComponents geeft de onderdelen van een cluster, zoals zijn
// database. Ook voor viewers.
func (s *Server) ListClusterComponents(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	ctx := r.Context()
	if _, err := s.q.GetCluster(ctx, clusterID); err != nil {
		s.componentError(w, r, err)
		return
	}
	list, err := s.components.List(ctx, clusterID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.ClusterComponent, 0, len(list))
	for _, c := range list {
		items = append(items, toAPIComponent(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// PlanClusterComponent toont wat een database toevoegen zou doen.
func (s *Server) PlanClusterComponent(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.ComponentInput
	if !decode(w, r, &in) {
		return
	}
	p, err := s.components.Plan(r.Context(), clusterID, componentRequest(in.Engine, in.Nodes, in.Arbiter.Get, in.Settings))
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIComponentPlan(p))
}

// AddClusterComponent rolt de database uit volgens het plan met deze hash.
func (s *Server) AddClusterComponent(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentApplyInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "een database toevoegen") {
		return
	}
	j, err := s.components.Add(ctx, events.User(p.User.ID), clusterID, componentRequest(in.Engine, in.Nodes, in.Arbiter.Get, in.Settings), in.Hash)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// ReapplyClusterComponent past de database opnieuw toe.
func (s *Server) ReapplyClusterComponent(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentReapplyInput
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "een database opnieuw toepassen") {
		return
	}
	j, err := s.components.Reapply(ctx, events.User(p.User.ID), clusterID, componentID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// RemoveClusterComponent haalt een database die nooit gelukt is uit
// ClusterForge.
func (s *Server) RemoveClusterComponent(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.components.Remove(r.Context(), events.User(p.User.ID), clusterID, componentID); err != nil {
		s.componentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func componentRequest(engine string, nodes []uuid.UUID, arbiter func() (uuid.UUID, error), settings *map[string]any) components.Request {
	req := components.Request{Engine: engine, Nodes: nodes, Settings: map[string]any{}}
	if a, err := arbiter(); err == nil {
		req.Arbiter = &a
	}
	if settings != nil {
		req.Settings = *settings
	}
	return req
}

func (s *Server) componentError(w http.ResponseWriter, r *http.Request, err error) {
	var ie *components.InvalidError
	var ce *components.ConflictError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
	case errors.Is(err, components.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, components.ErrStale):
		writeError(w, http.StatusConflict, "plan_stale", err.Error())
	case errors.As(err, &ie):
		writeError(w, http.StatusBadRequest, "validation", ie.Msg)
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	default:
		s.internalError(w, r, err)
	}
}

func toAPIComponent(c components.Component) gen.ClusterComponent {
	out := gen.ClusterComponent{
		Id: c.ID, ClusterId: c.ClusterID, Engine: c.Engine, Template: c.Template, Version: c.Version, Title: c.Title,
		Revision: c.Revision, AppliedRevision: nullableOf(c.AppliedRevision), Params: c.Params, Nodes: make([]gen.ComponentNode, 0, len(c.Nodes)),
		State: c.State, JobId: nullableOf(c.JobID), JobError: c.JobError, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	for _, n := range c.Nodes {
		out.Nodes = append(out.Nodes, gen.ComponentNode{NodeId: n.NodeID, Hostname: n.Hostname, Role: n.Role, Index: n.Index})
	}
	return out
}

func toAPIComponentPlan(p components.Plan) gen.ComponentPlan {
	out := gen.ComponentPlan{
		ClusterId: p.ClusterID, ClusterName: p.ClusterName, Slug: p.Slug, Prod: p.Prod, Engine: p.Engine, Template: p.Template,
		Version: p.Version, Title: p.Title, Params: p.Params, Blocked: p.Blocked, Hash: p.Hash,
		Nodes: make([]gen.ComponentPlanNode, 0, len(p.Nodes)), Findings: make([]gen.ComponentFinding, 0, len(p.Findings)),
	}
	for _, n := range p.Nodes {
		out.Nodes = append(out.Nodes, gen.ComponentPlanNode{
			NodeId: n.NodeID, Hostname: n.Hostname, Role: n.Role, Index: n.Index, Address: n.Address, Os: n.OS, PveType: n.PveType,
			PveNode: n.PveNode, Vmid: n.VMID, MemoryBytes: n.MemoryBytes, MemoryUsedPct: nullableOf(float32Of(n.MemoryUsedPct)),
			NeedBytes: n.NeedBytes, FreeAfterPct: nullableOf(float32Of(n.FreeAfterPct)), DiskFreeBytes: nullableOf(n.DiskFreeBytes),
			DiskMount: n.DiskMount, Existing: n.Existing, Steps: nonNil(n.Steps),
		})
	}
	for _, f := range p.Findings {
		out.Findings = append(out.Findings, gen.ComponentFinding{
			Level: f.Level, Code: f.Code, NodeId: nullableOf(f.NodeID), Hostname: f.Hostname, Message: f.Message, Command: f.Command,
		})
	}
	return out
}

func float32Of(p *float64) *float32 {
	if p == nil {
		return nil
	}
	v := float32(*p)
	return &v
}
