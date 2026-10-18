package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/cronjobs"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// cronJobError schrijft de fout van een beheerde job.
func (s *Server) cronJobError(w http.ResponseWriter, r *http.Request, err error) {
	var inv *cronjobs.InvalidError
	var agentErr *cronjobs.AgentError
	switch {
	case errors.As(err, &inv):
		field := inv.Field
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: inv.Msg, Field: &field})
	case errors.Is(err, cronjobs.ErrJobNotFound), errors.Is(err, cronjobs.ErrRunNotFound), errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	case errors.Is(err, cronjobs.ErrRunNotRunning):
		writeError(w, http.StatusConflict, "not_running", err.Error())
	case errors.Is(err, cronjobs.ErrAgentTooOld):
		writeError(w, http.StatusConflict, "agent_too_old", err.Error())
	case errors.As(err, &agentErr):
		writeError(w, http.StatusBadGateway, "agent", agentErr.Msg)
	default:
		s.internalError(w, r, err)
	}
}

func toAPIRunSummary(r cronjobs.RunSummary) gen.CronRunSummary {
	return gen.CronRunSummary{
		Id: r.ID, NodeId: r.NodeID, Hostname: r.Hostname, Reason: gen.CronRunSummaryReason(r.Reason), Result: gen.CronRunSummaryResult(r.Result),
		StartedAt: r.StartedAt, EndedAt: nullableOf(r.EndedAt), ExitCode: nullableOf(r.Exit), Sync: toAPISyncResults(r.Sync),
	}
}

func toAPISyncResults(in []protocol.SyncResult) []gen.CronSyncResult {
	out := make([]gen.CronSyncResult, 0, len(in))
	for _, r := range in {
		id, _ := uuid.Parse(r.NodeID)
		out = append(out, gen.CronSyncResult{
			NodeId: id, Hostname: r.Hostname, Result: gen.CronSyncResultResult(r.Result), Exit: r.Exit, Files: r.Files, Bytes: r.Bytes,
			Deleted: r.Deleted, Error: r.Error,
		})
	}
	return out
}

func toAPIManagedJob(j cronjobs.ManagedJob) gen.ManagedCronJob {
	out := gen.ManagedCronJob{
		Id: j.ID, ClusterId: j.ClusterID, ClusterName: j.ClusterName, ClusterSlug: j.ClusterSlug, ClusterEnvironment: gen.Environment(j.ClusterEnvironment),
		Name: j.Name, Schedule: j.Schedule, Describe: j.Describe, OnCalendar: j.OnCalendar, RunOn: gen.ManagedCronJobRunOn(j.RunOn),
		NodeId: nullableOf(j.NodeID), NodeHostname: deref(j.NodeHostname), User: j.UserName, Command: j.Command,
		TimeoutSeconds: int(j.TimeoutSeconds), Enabled: j.Enabled, Revision: int(j.Revision), NextRun: nullableOf(j.NextRun),
		Failures24h: j.Failures24h, Retries: int(j.Retries), Alert: j.Alert, MissedAt: nullableOf(j.MissedAt), Kind: gen.ManagedCronJobKind(j.Kind),
		Sync: nullable.NewNullNullable[gen.ManagedCronSync](), Nodes: make([]gen.ManagedCronJobNode, 0, len(j.Nodes)), CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
	if j.Kind == protocol.CronKindSync {
		out.Sync = nullable.NewNullableWithValue(gen.ManagedCronSync{
			Source: j.SyncSource, Target: j.SyncTarget, ToClusterId: nullableOf(j.SyncToCluster), ToClusterName: j.SyncToClusterName,
			Delete: j.SyncDelete, Exclude: j.SyncExclude, BwlimitMb: int(j.SyncBwlimitMb),
		})
	}
	for _, n := range j.Nodes {
		mn := gen.ManagedCronJobNode{NodeId: n.NodeID, Hostname: n.Hostname, Applied: n.Applied, HasVip: n.HasVIP, Error: n.Error}
		var last *gen.CronRunSummary
		if n.LastRun != nil {
			x := toAPIRunSummary(*n.LastRun)
			last = &x
		}
		mn.LastRun = nullableOf(last)
		if n.LastRun != nil && n.LastRun.Result == "running" {
			out.Running++
		}
		out.Nodes = append(out.Nodes, mn)
	}
	return out
}

func (s *Server) ListCronJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.cronjobs.Jobs(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.ManagedCronJob, 0, len(jobs))
	for _, j := range jobs {
		items = append(items, toAPIManagedJob(j))
	}
	writeJSON(w, http.StatusOK, list[gen.ManagedCronJob]{items})
}

func (s *Server) writeManagedJob(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	j, _, err := s.cronjobs.Job(r.Context(), id, 0)
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	writeJSON(w, status, toAPIManagedJob(j))
}

func jobInput(in gen.ManagedCronJobInput) cronjobs.JobInput {
	out := cronjobs.JobInput{
		Name: in.Name, Schedule: in.Schedule, RunOn: string(in.RunOn), NodeID: nullablePtr(in.NodeId), User: in.User,
		Command: in.Command, TimeoutSeconds: in.TimeoutSeconds, Enabled: in.Enabled, Alert: true,
	}
	if in.Kind != nil {
		out.Kind = string(*in.Kind)
	}
	if sy := in.Sync; sy != nil {
		out.Sync = &cronjobs.SyncInput{Source: sy.Source, Target: deref(sy.Target), ToCluster: nullablePtr(sy.ToClusterId), Delete: sy.Delete != nil && *sy.Delete}
		if sy.Exclude != nil {
			out.Sync.Exclude = *sy.Exclude
		}
		if sy.BwlimitMb != nil {
			out.Sync.BwLimitMB = *sy.BwlimitMb
		}
	}
	if in.Retries != nil {
		out.Retries = *in.Retries
	}
	// Zonder alert opent een mislukte run een incident, zoals bij elke job.
	if in.Alert != nil {
		out.Alert = *in.Alert
	}
	return out
}

func (s *Server) CreateCronJob(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ManagedCronJobCreate
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, in.ClusterId)
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "een cronjob maken") {
		return
	}
	uid := p.User.ID
	job, err := s.cronjobs.CreateJob(ctx, events.User(p.User.ID), &uid, c.ID, c.Name, jobInput(gen.ManagedCronJobInput{
		Name: in.Name, Schedule: in.Schedule, RunOn: gen.ManagedCronJobInputRunOn(in.RunOn), NodeId: in.NodeId, User: in.User,
		Command: in.Command, TimeoutSeconds: in.TimeoutSeconds, Enabled: in.Enabled, Retries: in.Retries, Alert: in.Alert,
		Kind: (*gen.ManagedCronJobInputKind)(in.Kind), Sync: in.Sync,
	}))
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	s.writeManagedJob(w, r, http.StatusCreated, job.ID)
}

func (s *Server) CheckCronSchedule(w http.ResponseWriter, r *http.Request, params gen.CheckCronScheduleParams) {
	info, err := s.cronjobs.CheckSchedule(params.Schedule)
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	next := info.Next
	if next == nil {
		next = []time.Time{}
	}
	writeJSON(w, http.StatusOK, gen.CronScheduleInfo{Describe: info.Describe, OnCalendar: info.OnCalendar, Next: next})
}

func (s *Server) GetCronJob(w http.ResponseWriter, r *http.Request, jobID uuid.UUID) {
	j, runs, err := s.cronjobs.Job(r.Context(), jobID, 100)
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	out := gen.ManagedCronJobDetail{Job: toAPIManagedJob(j), Runs: make([]gen.CronRunSummary, 0, len(runs))}
	for _, run := range runs {
		out.Runs = append(out.Runs, toAPIRunSummary(run))
	}
	writeJSON(w, http.StatusOK, out)
}

// jobCluster haalt het cluster van een job, voor de prod-bevestiging.
func (s *Server) jobCluster(w http.ResponseWriter, r *http.Request, jobID uuid.UUID) (store.GetCronJobRow, bool) {
	j, err := s.q.GetCronJob(r.Context(), jobID)
	if err != nil {
		s.cronJobError(w, r, err)
		return j, false
	}
	return j, true
}

func (s *Server) UpdateCronJob(w http.ResponseWriter, r *http.Request, jobID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ManagedCronJobInput
	if !decode(w, r, &in) {
		return
	}
	j, ok := s.jobCluster(w, r, jobID)
	if !ok {
		return
	}
	if !confirmProd(w, p, j.ClusterEnvironment == store.EnvironmentProd, j.ClusterSlug, in.Confirm, "een cronjob wijzigen") {
		return
	}
	if _, err := s.cronjobs.UpdateJob(r.Context(), events.User(p.User.ID), jobID, jobInput(in)); err != nil {
		s.cronJobError(w, r, err)
		return
	}
	s.writeManagedJob(w, r, http.StatusOK, jobID)
}

func (s *Server) DeleteCronJob(w http.ResponseWriter, r *http.Request, jobID uuid.UUID, params gen.DeleteCronJobParams) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	j, ok := s.jobCluster(w, r, jobID)
	if !ok {
		return
	}
	if !confirmProd(w, p, j.ClusterEnvironment == store.EnvironmentProd, j.ClusterSlug, params.Confirm, "een cronjob weghalen") {
		return
	}
	if err := s.cronjobs.DeleteJob(r.Context(), events.User(p.User.ID), jobID); err != nil {
		s.cronJobError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RunCronJob(w http.ResponseWriter, r *http.Request, jobID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.RunCronJobJSONBody
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	j, ok := s.jobCluster(w, r, jobID)
	if !ok {
		return
	}
	if !confirmProd(w, p, j.ClusterEnvironment == store.EnvironmentProd, j.ClusterSlug, in.Confirm, "een cronjob uitvoeren") {
		return
	}
	nodes, err := s.cronjobs.RunJob(r.Context(), events.User(p.User.ID), jobID)
	if err != nil && len(nodes) == 0 {
		s.cronJobError(w, r, err)
		return
	}
	if nodes == nil {
		nodes = []string{}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"nodes": nodes})
}

func (s *Server) GetCronRun(w http.ResponseWriter, r *http.Request, runID uuid.UUID) {
	p, _ := principalFrom(r.Context())
	run, err := s.cronjobs.RunRecord(r.Context(), runID)
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	out := gen.CronRun{
		Id: run.ID, NodeId: run.NodeID, Hostname: run.Hostname, JobId: run.JobID, JobName: run.JobName, Revision: int(run.Revision),
		Reason: gen.CronRunReason(run.Reason), Result: gen.CronRunResult(run.Result), StartedAt: run.StartedAt,
		EndedAt: nullableOf(run.EndedAt), ExitCode: nullableOf(intOf(run.ExitCode)), Output: run.Output, OutputTruncated: run.OutputTruncated,
	}
	var sync []protocol.SyncResult
	_ = json.Unmarshal(run.Sync, &sync)
	out.Sync = toAPISyncResults(sync)
	// Een kijker ziet de uitvoer van een job als root niet: die kan alles
	// bevatten wat root kan lezen.
	if p.User.Role != store.UserRoleAdmin && run.UserName == "root" {
		out.Output, out.OutputHidden = "", true
	} else if run.Result == "running" {
		// Loopt de run nog, dan komt de uitvoer tot nu toe van de node; lukt
		// dat niet, dan blijft de opgeslagen uitvoer staan.
		if text, truncated, err := s.cronjobs.LiveOutput(r.Context(), run); err == nil {
			out.Output, out.OutputTruncated, out.Live = text, truncated, true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) StopCronRun(w http.ResponseWriter, r *http.Request, runID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.StopCronRunJSONBody
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	run, err := s.cronjobs.RunRecord(r.Context(), runID)
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	j, ok := s.jobCluster(w, r, run.JobID)
	if !ok {
		return
	}
	if !confirmProd(w, p, j.ClusterEnvironment == store.EnvironmentProd, j.ClusterSlug, in.Confirm, "een cronjob stoppen") {
		return
	}
	if err := s.cronjobs.StopRun(r.Context(), events.User(p.User.ID), runID); err != nil {
		s.cronJobError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func intOf(v *int32) *int {
	if v == nil {
		return nil
	}
	x := int(*v)
	return &x
}

func (s *Server) GetCronSyncPlan(w http.ResponseWriter, r *http.Request, jobID uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	plan, err := s.cronjobs.SyncPlan(r.Context(), jobID)
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	type check struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	type pnode struct {
		NodeID   uuid.UUID `json:"node_id"`
		Hostname string    `json:"hostname"`
		Role     string    `json:"role"`
		Address  string    `json:"address"`
		OK       bool      `json:"ok"`
		Checks   []check   `json:"checks"`
	}
	out := struct {
		Nodes []pnode  `json:"nodes"`
		Notes []string `json:"notes"`
	}{Nodes: []pnode{}, Notes: plan.Notes}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	for _, n := range plan.Nodes {
		pn := pnode{NodeID: n.NodeID, Hostname: n.Hostname, Role: n.Role, Address: n.Address, OK: n.OK, Checks: []check{}}
		for _, c := range n.Checks {
			pn.Checks = append(pn.Checks, check{c.Name, c.OK, c.Text})
		}
		out.Nodes = append(out.Nodes, pn)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) DryRunCronSync(w http.ResponseWriter, r *http.Request, jobID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.DryRunCronSyncJSONBody
	if !decode(w, r, &in) {
		return
	}
	src, lines, err := s.cronjobs.DryRun(r.Context(), events.User(p.User.ID), jobID, in.Target)
	if errors.Is(err, cronjobs.ErrNotSync) {
		writeError(w, http.StatusBadRequest, "not_sync", err.Error())
		return
	}
	if err != nil {
		s.cronJobError(w, r, err)
		return
	}
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": src, "lines": lines})
}
