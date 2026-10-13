package httpapi

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// PlanClusterComponentSettings toont wat nieuwe instellingen van een
// database doen.
func (s *Server) PlanClusterComponentSettings(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.ComponentSettingsPlanInput
	if !decode(w, r, &in) {
		return
	}
	p, err := s.components.SettingsPlan(r.Context(), clusterID, componentID, deref(in.Settings))
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISettingsPlan(p))
}

// ChangeClusterComponentSettings wijzigt de instellingen van een database,
// als het plan nog is wat de gebruiker zag.
func (s *Server) ChangeClusterComponentSettings(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentSettingsInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "de instellingen van een database wijzigen") {
		return
	}
	j, err := s.components.ChangeSettings(ctx, events.User(p.User.ID), clusterID, componentID, components.SettingsInput{Settings: deref(in.Settings), Hash: in.Hash})
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// GetClusterComponentUpgrade toont wat bijwerken van een database doet.
func (s *Server) GetClusterComponentUpgrade(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	p, err := s.components.UpdatePlan(r.Context(), clusterID, componentID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIUpgradePlan(p))
}

// UpgradeClusterComponent werkt een database node voor node bij, als het
// plan nog is wat de gebruiker zag.
func (s *Server) UpgradeClusterComponent(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentUpgradeInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "een database bijwerken") {
		return
	}
	j, err := s.components.Update(ctx, events.User(p.User.ID), clusterID, componentID, in.Hash)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func toAPISettingsPlan(p components.SettingsPlan) gen.ComponentSettingsPlan {
	out := gen.ComponentSettingsPlan{
		ClusterId: p.ClusterID, ClusterName: p.ClusterName, Slug: p.Slug, Prod: p.Prod, Engine: gen.ComponentSettingsPlanEngine(p.Engine), Title: p.Title,
		Revision: p.Revision, Params: make([]gen.ComponentSettingParam, 0, len(p.Params)), Changes: make([]gen.ComponentSettingChange, 0, len(p.Changes)),
		Nodes: make([]gen.ComponentSettingsNode, 0, len(p.Nodes)), Effects: nonNil(p.Effects), Findings: toAPIFindings(p.Findings), Blocked: p.Blocked, Hash: p.Hash,
	}
	for _, x := range p.Params {
		out.Params = append(out.Params, gen.ComponentSettingParam{Name: x.Name, Label: x.Label, Help: x.Help, Type: x.Type, Value: x.Value, Min: x.Min, Max: x.Max})
	}
	for _, x := range p.Changes {
		out.Changes = append(out.Changes, gen.ComponentSettingChange{Name: x.Name, Label: x.Label, From: x.From, To: x.To})
	}
	for _, n := range p.Nodes {
		out.Nodes = append(out.Nodes, gen.ComponentSettingsNode{
			NodeId: n.NodeID, Hostname: n.Hostname, State: n.State, Healthy: n.Healthy, Writer: n.Writer, MemoryBytes: n.MemoryBytes,
			MemoryUsedPct: nullableOf(n.MemoryUsedPct), FreeAfterPct: nullableOf(n.FreeAfterPct),
		})
	}
	return out
}

func toAPIUpgradePlan(p components.UpdatePlan) gen.ComponentUpgradePlan {
	out := gen.ComponentUpgradePlan{
		ClusterId: p.ClusterID, ClusterName: p.ClusterName, Slug: p.Slug, Prod: p.Prod, Engine: gen.ComponentUpgradePlanEngine(p.Engine), Title: p.Title,
		Nodes: make([]gen.ComponentUpgradeNode, 0, len(p.Nodes)), Effects: nonNil(p.Effects), Findings: toAPIFindings(p.Findings), Blocked: p.Blocked, Hash: p.Hash,
	}
	for _, n := range p.Nodes {
		pkgs := nullable.NewNullNullable[[]string]()
		if n.Packages != nil {
			pkgs = nullable.NewNullableWithValue(n.Packages)
		}
		out.Nodes = append(out.Nodes, gen.ComponentUpgradeNode{
			NodeId: n.NodeID, Hostname: n.Hostname, Role: gen.ComponentUpgradeNodeRole(n.Role), State: n.State, Healthy: n.Healthy, Writer: n.Writer,
			Version: n.Version, Packages: pkgs, Skip: n.Skip,
		})
	}
	return out
}
