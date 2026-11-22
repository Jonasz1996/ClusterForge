package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// De spec van een cluster uit een template wijzigen in de webinterface, met
// hetzelfde plan, dezelfde revisies en dezelfde uitrol als een wijziging
// uit Git.

func (s *Server) GetClusterSpecEdit(w http.ResponseWriter, r *http.Request, id uuid.UUID, params gen.GetClusterSpecEditParams) {
	f, err := s.git.EditForm(r.Context(), id, deref(params.Version))
	if err != nil {
		s.gitError(w, r, err)
		return
	}
	out := gen.SpecEditForm{Template: f.Template, Version: f.Version, Versions: f.Versions, Params: []gen.SpecEditParam{}, Blocked: f.Blocked}
	for _, p := range f.Params {
		v := nullable.NewNullNullable[any]()
		if p.Value != nil {
			v = nullable.NewNullableWithValue(p.Value)
		}
		out.Params = append(out.Params, gen.SpecEditParam{Param: toAPIParam(p.Param), Value: v, Locked: p.Locked})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) PlanClusterSpec(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.SpecEditInput
	if !decode(w, r, &in) {
		return
	}
	ep, err := s.git.PlanEdit(r.Context(), id, gitops.Edit{Version: deref(in.Version), Params: in.Params})
	if err != nil {
		s.gitError(w, r, err)
		return
	}
	// Het plan gaat via JSON naar het API-type; de velden heten gelijk.
	raw, err := json.Marshal(ep.Plan)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var plan gen.GitPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.SpecEditPlan{
		Plan: plan, Hash: ep.Hash, NeedsConfirmation: ep.Prod, FullApply: ep.All, Blocked: ep.Blocked, BlockedCode: ep.BlockedCode,
	})
}

func (s *Server) ApplyClusterSpec(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.SpecApplyInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, id)
	if err != nil {
		s.gitError(w, r, err)
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "de specificatie wijzigen") {
		return
	}
	a, err := s.git.ApplyEdit(ctx, events.User(p.User.ID), id, gitops.Edit{Version: deref(in.Version), Params: in.Params}, in.Hash, deref(in.ServerUrl))
	if err != nil {
		s.gitError(w, r, err)
		return
	}
	out := gen.SpecApplied{Revision: int(a.Revision), Job: nullable.NewNullNullable[gen.Job]()}
	status := http.StatusOK
	if a.Job != nil {
		out.Job = nullable.NewNullableWithValue(toAPIJob(*a.Job, &p.User.Username))
		status = http.StatusAccepted
	}
	writeJSON(w, status, out)
}
