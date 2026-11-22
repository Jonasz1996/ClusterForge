package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/rollout"
)

func (s *Server) RemediateClusterDrift(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.RemediationInput
	if !decode(w, r, &in) {
		return
	}
	choices := make([]rollout.Choice, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		c := rollout.Choice{NodeID: n.NodeId}
		for _, st := range n.Steps {
			c.Steps = append(c.Steps, rollout.StepChoice{Step: st.Step, Fingerprints: st.Fingerprints})
		}
		choices = append(choices, c)
	}
	plan, err := s.rollout.PlanRemediation(r.Context(), id, choices)
	if s.rolloutError(w, r, err) {
		return
	}
	if in.Preview != nil && *in.Preview {
		writeJSON(w, http.StatusOK, gen.RemediationResult{Plan: toAPIPlan(plan), Job: nullable.NewNullNullable[gen.Job]()})
		return
	}
	if plan.NeedsConfirmation() {
		if p.User.TotpEnabledAt == nil {
			writeError(w, http.StatusForbidden, "totp_required", "op prod kan alleen een beheerder met tweestapsverificatie herstellen; zet die aan bij Instellingen")
			return
		}
		if in.Confirm == nil || *in.Confirm != plan.Slug {
			writeError(w, http.StatusConflict, "needs_confirmation", "dit is een prodcluster; tik ter bevestiging de slug "+plan.Slug+" in")
			return
		}
	}
	j, err := s.rollout.StartRemediation(r.Context(), events.User(p.User.ID), plan)
	if s.rolloutError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, gen.RemediationResult{Plan: toAPIPlan(plan), Job: nullable.NewNullableWithValue(toAPIJob(j, &p.User.Username))})
}

// rolloutError schrijft de passende foutrespons en geeft true als err niet
// nil was.
func (s *Server) rolloutError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var fe *rollout.FieldError
	var ce *rollout.ConflictError
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: fe.Message, Field: optional(fe.Field)})
	case errors.Is(err, rollout.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, ce.Code, ce.Msg)
	default:
		s.internalError(w, r, err)
	}
	return true
}

func toAPIPlan(p rollout.Plan) gen.RemediationPlan {
	out := gen.RemediationPlan{
		ClusterId: p.ClusterID, Cluster: p.Cluster, Slug: p.Slug, Environment: gen.Environment(p.Environment),
		Template: p.Template, Version: p.Version, Revision: p.Revision, NeedsConfirmation: p.NeedsConfirmation(),
		Nodes: make([]gen.RemediationPlanNode, 0, len(p.Nodes)), Notes: p.Notes,
	}
	for _, n := range p.Nodes {
		steps := make([]gen.RemediationPlanStep, 0, len(n.Steps))
		for _, st := range n.Steps {
			steps = append(steps, gen.RemediationPlanStep{Step: st.Step, Title: st.Title, Action: st.Action})
		}
		out.Nodes = append(out.Nodes, gen.RemediationPlanNode{
			NodeId: n.NodeID, Hostname: n.Hostname, Vips: nonNil(n.VIPs), Steps: steps, Ignored: nonNil(n.Ignored),
		})
	}
	return out
}
