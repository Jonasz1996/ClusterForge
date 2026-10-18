package httpapi

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/heal"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
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

func toAPIHealReport(rep heal.Report) gen.HealReport {
	out := gen.HealReport{
		Key: rep.Key, ClusterId: nullableOf(rep.ClusterID), ClusterName: rep.ClusterName, ClusterSlug: rep.ClusterSlug,
		ClusterEnvironment: nullable.NewNullNullable[gen.Environment](), Engine: gen.HealReportEngine(rep.Engine), Kind: rep.Kind,
		Health: rep.Health, Problems: make([]gen.HealProblem, 0, len(rep.Problems)), Plan: nullable.NewNullNullable[gen.HealPlan](),
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
