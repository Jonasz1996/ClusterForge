package httpapi

import (
	"net/http"

	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/cronjobs"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

func (s *Server) ListCronjobs(w http.ResponseWriter, r *http.Request) {
	groups, err := s.cronjobs.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.CronGroup, 0, len(groups))
	for _, g := range groups {
		out := gen.CronGroup{
			Key: "none", ClusterId: nullableOf(g.ClusterID), ClusterName: g.ClusterName, ClusterSlug: g.ClusterSlug,
			ClusterEnvironment: nullable.NewNullNullable[gen.Environment](),
			Nodes:              make([]gen.CronNodeRead, 0, len(g.Nodes)), Jobs: make([]gen.CronJob, 0, len(g.Jobs)),
		}
		if g.ClusterID != nil {
			out.Key = g.ClusterID.String()
		}
		if g.ClusterEnv != "" {
			out.ClusterEnvironment = nullable.NewNullableWithValue(gen.Environment(g.ClusterEnv))
		}
		for _, n := range g.Nodes {
			out.Nodes = append(out.Nodes, gen.CronNodeRead{
				NodeId: n.NodeID, Hostname: n.Hostname, Daemon: n.Daemon, Logging: n.Logging, Journal: n.Journal, Truncated: n.Truncated,
				Error: n.Error, ErrorKind: n.ErrorKind, SyncedAt: n.SyncedAt, OkAt: nullableOf(n.OKAt),
			})
		}
		for _, j := range g.Jobs {
			out.Jobs = append(out.Jobs, toAPICronJob(j))
		}
		items = append(items, out)
	}
	writeJSON(w, http.StatusOK, list[gen.CronGroup]{items})
}

func toAPICronJob(j cronjobs.Job) gen.CronJob {
	out := gen.CronJob{
		Key: j.Key, Source: gen.CronJobSource(j.Source), File: j.File, Line: j.Line, User: j.User, Schedule: j.Schedule,
		Describe: j.Describe, Command: j.Command, Unit: j.Unit, Description: j.Description, Package: j.Package,
		Clusterforge: j.ClusterForge, NextRun: nullableOf(j.NextRun), Nodes: make([]gen.CronJobNode, 0, len(j.Nodes)),
	}
	for _, n := range j.Nodes {
		out.Nodes = append(out.Nodes, gen.CronJobNode{
			NodeId: n.NodeID, Hostname: n.Hostname, Enabled: n.Enabled, LastRun: nullableOf(n.LastRun), LastEnd: nullableOf(n.LastEnd),
			LastExit: nullableOf(n.LastExit), LastResult: gen.CronJobNodeLastResult(n.LastResult), Runs: n.Runs, Failures: n.Failures,
			NextRun: nullableOf(n.NextRun),
		})
	}
	return out
}

func (s *Server) RefreshCronjobs(w http.ResponseWriter, r *http.Request) {
	s.cronjobs.Kick()
	w.WriteHeader(http.StatusAccepted)
}
