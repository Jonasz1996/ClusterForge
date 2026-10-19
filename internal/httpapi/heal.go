package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/heal"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) ListHeal(w http.ResponseWriter, r *http.Request) {
	reports, err := s.heal.Reports(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.HealReport, 0, len(reports))
	for _, rep := range reports {
		items = append(items, toAPIHealReport(rep))
	}
	writeJSON(w, http.StatusOK, list[gen.HealReport]{items})
}

// RunHeal voert het herstelplan van een database uit, als het nog het plan
// is dat de beheerder zag.
func (s *Server) RunHeal(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.HealRunInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	rep, _, err := s.heal.Report(ctx, in.Key)
	if err != nil {
		s.healError(w, r, err)
		return
	}
	if !confirmProd(w, p, rep.ClusterEnv == string(store.EnvironmentProd), rep.ClusterSlug, in.Confirm, "een database herstellen") {
		return
	}
	j, err := s.heal.RunPlan(ctx, events.User(p.User.ID), in.Key, in.Hash)
	if err != nil {
		s.healError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// SetHealAuto zet Zelfherstel vanzelf aan of uit op een cluster.
func (s *Server) SetHealAuto(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.SetHealAutoJSONRequestBody
	if !decode(w, r, &in) {
		return
	}
	a, err := s.heal.SetAuto(r.Context(), events.User(p.User.ID), clusterID, in.Enabled)
	if err != nil {
		s.healError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.HealAutoSetting{Enabled: a.Enabled, PausedAt: nullableOf(a.PausedAt), PausedReason: a.PausedReason})
}

func (s *Server) healError(w http.ResponseWriter, r *http.Request, err error) {
	var ce *heal.ConflictError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
	case errors.Is(err, heal.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, heal.ErrStale):
		writeError(w, http.StatusConflict, "plan_stale", err.Error())
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	default:
		s.internalError(w, r, err)
	}
}

func toAPIHealReport(rep heal.Report) gen.HealReport {
	out := gen.HealReport{
		Key: rep.Key, ClusterId: nullableOf(rep.ClusterID), ClusterName: rep.ClusterName, ClusterSlug: rep.ClusterSlug,
		ClusterEnvironment: nullable.NewNullNullable[gen.Environment](), Engine: gen.HealReportEngine(rep.Engine), Kind: rep.Kind,
		Health: rep.Health, Problems: make([]gen.HealProblem, 0, len(rep.Problems)), Plan: nullable.NewNullNullable[gen.HealPlan](),
		LastRun: nullable.NewNullNullable[gen.HealRun](),
	}
	out.Auto = nullable.NewNullNullable[gen.HealAuto]()
	if a := rep.Auto; a != nil {
		out.Auto = nullable.NewNullableWithValue(gen.HealAuto{
			Enabled: a.Enabled, PausedAt: nullableOf(a.PausedAt), PausedReason: a.PausedReason, Allowed: a.Allowed, Due: a.Due, Note: a.Note,
		})
	}
	if l := rep.Last; l != nil {
		out.LastRun = nullable.NewNullableWithValue(gen.HealRun{
			JobId: l.JobID, Status: gen.JobStatus(l.Status), Error: l.Error, CreatedAt: l.CreatedAt, FinishedAt: nullableOf(l.FinishedAt),
		})
	}
	if rep.ClusterEnv != "" {
		out.ClusterEnvironment = nullable.NewNullableWithValue(gen.Environment(rep.ClusterEnv))
	}
	for _, p := range rep.Problems {
		nodes := p.Nodes
		if nodes == nil {
			nodes = []string{}
		}
		out.Problems = append(out.Problems, gen.HealProblem{Code: p.Code, Level: gen.HealProblemLevel(p.Level), Title: p.Title, Why: p.Why, Nodes: nodes})
	}
	if p := rep.Plan; p != nil {
		plan := gen.HealPlan{
			Action: p.Action, Steps: p.Steps, Nodes: make([]gen.HealPlanNode, 0, len(p.Nodes)), Auto: p.Auto, AutoWhy: p.AutoWhy,
			Blocked: p.Blocked, ManagedComponentId: nullable.NewNullNullable[uuid.UUID](), Hash: p.Hash,
		}
		if p.Managed != nil {
			plan.ManagedComponentId = nullable.NewNullableWithValue(p.Managed.ComponentID)
		}
		for _, n := range p.Nodes {
			pids := n.KillPIDs
			if pids == nil {
				pids = []int{}
			}
			plan.Nodes = append(plan.Nodes, gen.HealPlanNode{
				NodeId: n.NodeID, Hostname: n.Hostname, Unit: n.Unit, Start: n.Start, Kill: n.Kill, KillPids: pids, Primary: n.Primary,
				SetAddress: n.SetAddress, OldClusterAddress: n.OldClusterAddress, ClusterAddress: n.ClusterAddress,
				OldNodeAddress: n.OldNodeAddress, NodeAddress: n.NodeAddress, File: n.File,
			})
		}
		out.Plan = nullable.NewNullableWithValue(plan)
	}
	return out
}
