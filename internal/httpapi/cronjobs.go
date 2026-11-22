package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/cronjobs"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
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
		Differs: make([]gen.CronVariant, 0, len(j.Differs)), Warnings: make([]gen.CronWarning, 0, len(j.Warnings)),
	}
	for _, v := range j.Differs {
		out.Differs = append(out.Differs, gen.CronVariant{Hostname: v.Hostname, Schedule: v.Schedule, Command: v.Command})
	}
	for _, w := range j.Warnings {
		out.Warnings = append(out.Warnings, gen.CronWarning{Kind: gen.CronWarningKind(w.Kind), Level: gen.CronWarningLevel(w.Level), Text: w.Text})
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

func (s *Server) SetCronLogging(w http.ResponseWriter, r *http.Request, nodeID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.SetCronLoggingJSONRequestBody
	if !decode(w, r, &req) {
		return
	}
	out, err := s.cronjobs.SetLogging(r.Context(), events.User(p.User.ID), nodeID, req.Enabled)
	var agentErr *cronjobs.AgentError
	switch {
	case errors.Is(err, cronjobs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden of zonder agent")
	case errors.Is(err, cronjobs.ErrAgentTooOld):
		writeError(w, http.StatusConflict, "agent_too_old", err.Error())
	case errors.As(err, &agentErr):
		writeError(w, http.StatusBadGateway, "agent", agentErr.Msg)
	case err != nil:
		s.internalError(w, r, err)
	default:
		if out == nil {
			out = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"output": out})
	}
}

func (s *Server) ToggleNodeCron(w http.ResponseWriter, r *http.Request, nodeID uuid.UUID, key string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.ToggleNodeCronJSONRequestBody
	if !decode(w, r, &req) {
		return
	}
	ref, ok := s.nodeCluster(w, r, nodeID)
	if !ok {
		return
	}
	if ref != nil {
		c, err := s.q.GetCluster(r.Context(), *ref)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		what := "een cronjob uitzetten"
		if req.Enabled {
			what = "een cronjob weer aanzetten"
		}
		if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, req.Confirm, what) {
			return
		}
	}
	out, err := s.cronjobs.ToggleFound(r.Context(), events.User(p.User.ID), nodeID, key, req.Enabled)
	var agentErr *cronjobs.AgentError
	switch {
	case errors.Is(err, cronjobs.ErrNotFound), errors.Is(err, cronjobs.ErrCronNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, cronjobs.ErrNotToggleable):
		writeError(w, http.StatusBadRequest, "not_toggleable", err.Error())
	case errors.Is(err, cronjobs.ErrAgentTooOld):
		writeError(w, http.StatusConflict, "agent_too_old", err.Error())
	case errors.As(err, &agentErr):
		writeError(w, http.StatusBadGateway, "agent", agentErr.Msg)
	case err != nil:
		s.internalError(w, r, err)
	default:
		if out == nil {
			out = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"output": out})
	}
}
