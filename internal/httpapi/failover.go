package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) ListFailoverTests(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	opts, err := s.failover.Options(ctx, id)
	if s.failoverError(w, r, err) {
		return
	}
	c, err := s.q.GetCluster(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	tests, err := s.q.ListFailoverTests(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	last, err := s.latestRuns(r, id, c.Name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	unrestored, err := s.q.ListUnrestoredRuns(ctx, &id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.FailoverTestList{Items: make([]gen.FailoverTest, 0, len(tests)), Options: toAPIOptions(opts), Unrestored: []gen.TestRun{}}
	for _, t := range tests {
		out.Items = append(out.Items, toAPITest(t, last[t.FailoverTest.ID]))
	}
	for _, run := range unrestored {
		out.Unrestored = append(out.Unrestored, toAPIRun(run, store.NullJobStatus{}, nil, c.Name))
	}
	writeJSON(w, http.StatusOK, out)
}

// latestRuns geeft de laatste run per test van een cluster.
func (s *Server) latestRuns(r *http.Request, clusterID uuid.UUID, clusterName string) (map[uuid.UUID]*gen.TestRun, error) {
	rows, err := s.q.LatestTestRuns(r.Context(), &clusterID)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]*gen.TestRun{}
	for _, row := range rows {
		if row.TestRun.TestID != nil {
			run := toAPIRun(row.TestRun, row.JobStatus, nil, clusterName)
			out[*row.TestRun.TestID] = &run
		}
	}
	return out, nil
}

func (s *Server) CreateFailoverTest(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.FailoverTestInput
	if !decode(w, r, &req) {
		return
	}
	t, err := s.failover.Create(r.Context(), events.User(p.User.ID), id, fromAPITestInput(req))
	if s.failoverError(w, r, err) {
		return
	}
	s.writeFailoverTest(w, r, t.ID, http.StatusCreated)
}

func (s *Server) GetFailoverTest(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.writeFailoverTest(w, r, id, http.StatusOK)
}

func (s *Server) UpdateFailoverTest(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.FailoverTestInput
	if !decode(w, r, &req) {
		return
	}
	_, err := s.failover.Update(r.Context(), events.User(p.User.ID), id, fromAPITestInput(req))
	if s.failoverError(w, r, err) {
		return
	}
	s.writeFailoverTest(w, r, id, http.StatusOK)
}

func (s *Server) DeleteFailoverTest(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.failoverError(w, r, s.failover.Delete(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeFailoverTest(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int) {
	ctx := r.Context()
	t, err := s.q.GetFailoverTest(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "failovertest niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	clusterID := t.FailoverTest.ClusterID
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.q.ListFailoverTests(ctx, clusterID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	last, err := s.latestRuns(r, clusterID, c.Name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, row := range rows {
		if row.FailoverTest.ID == id {
			writeJSON(w, status, toAPITest(row, last[id]))
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "failovertest niet gevonden")
}

func (s *Server) ListFailoverTestRuns(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	t, err := s.q.GetFailoverTest(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "failovertest niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	c, err := s.q.GetCluster(ctx, t.FailoverTest.ClusterID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.q.ListTestRuns(ctx, store.ListTestRunsParams{TestID: &id, Lim: 50})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.TestRunList{Items: make([]gen.TestRun, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, toAPIRun(row.TestRun, row.JobStatus, row.RequestedByName, c.Name))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) StartFailoverTest(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.FailoverStartInput
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	t, err := s.q.GetFailoverTest(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "failovertest niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	c, err := s.q.GetCluster(ctx, t.FailoverTest.ClusterID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if c.Environment == store.EnvironmentProd {
		if p.User.TotpEnabledAt == nil {
			writeError(w, http.StatusForbidden, "totp_required", "op prod kan alleen een beheerder met tweestapsverificatie een failovertest starten; zet die aan bij Instellingen")
			return
		}
		if req.Confirm == nil || *req.Confirm != c.Slug {
			writeError(w, http.StatusConflict, "needs_confirmation", "dit is een prodcluster; tik ter bevestiging de slug "+c.Slug+" in")
			return
		}
	}
	run, err := s.failover.Start(ctx, events.User(p.User.ID), id)
	if s.failoverError(w, r, err) {
		return
	}
	s.writeTestRun(w, r, run.ID, http.StatusAccepted)
}

func (s *Server) GetTestRun(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.writeTestRun(w, r, id, http.StatusOK)
}

func (s *Server) writeTestRun(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int) {
	row, err := s.q.GetTestRun(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "run niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	name := ""
	if row.ClusterName != nil {
		name = *row.ClusterName
	}
	out := toAPIRun(row.TestRun, row.JobStatus, row.RequestedByName, name)
	if row.TestRun.Kind == backups.KindVerify {
		running := row.JobStatus.Valid && (row.JobStatus.JobStatus == store.JobStatusQueued || row.JobStatus.JobStatus == store.JobStatusRunning)
		sb, err := s.backups.RunSandbox(r.Context(), row.TestRun, running)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out.Backup = nullable.NewNullableWithValue(backupReport(row.TestRun, sb))
	}
	writeJSON(w, status, out)
}

func (s *Server) RestoreTestRun(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	j, err := s.failover.StartRestore(r.Context(), events.User(p.User.ID), id)
	if s.failoverError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// failoverError schrijft de passende foutrespons en geeft true als err niet
// nil was.
func (s *Server) failoverError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var fe *failover.FieldError
	var pe *failover.PrecheckError
	var ce *failover.ConflictError
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: fe.Message, Field: optional(fe.Field)})
	case errors.Is(err, failover.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	case errors.As(err, &pe):
		checks := make([]gen.TestRunCheck, 0, len(pe.Checks))
		for _, c := range pe.Checks {
			checks = append(checks, gen.TestRunCheck{Name: c.Name, Ok: c.OK, Detail: c.Detail})
		}
		writeJSON(w, http.StatusConflict, gen.Error{Code: "precheck_failed", Message: pe.Error(), Checks: &checks})
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, ce.Code, ce.Msg)
	default:
		s.internalError(w, r, err)
	}
	return true
}

func fromAPITestInput(in gen.FailoverTestInput) failover.Input {
	out := failover.Input{
		Name: in.Name, VIPID: in.VipId, Scenario: string(in.Scenario), MaxTakeoverSeconds: in.MaxTakeoverSeconds,
		ExpectFailback: in.ExpectFailback, Probe: fromAPIProbe(in.Probe), Scheduled: in.Scheduled != nil && *in.Scheduled,
	}
	if in.Service != nil {
		out.Service = *in.Service
	}
	return out
}

func fromAPIProbe(p gen.FailoverProbe) failover.Probe {
	var out failover.Probe
	if p.Http != nil {
		out.HTTP = &failover.HTTPProbe{Path: p.Http.Path, Expect: p.Http.Expect}
	}
	if p.Tcp != nil {
		out.TCP = &failover.TCPProbe{Port: p.Tcp.Port}
	}
	return out
}

func toAPIProbe(p failover.Probe) gen.FailoverProbe {
	var out gen.FailoverProbe
	if p.HTTP != nil {
		out.Http = &gen.FailoverProbeHTTP{Path: p.HTTP.Path, Expect: p.HTTP.Expect}
	}
	if p.TCP != nil {
		out.Tcp = &gen.FailoverProbeTCP{Port: p.TCP.Port}
	}
	return out
}

func toAPIOptions(o failover.Options) gen.FailoverOptions {
	out := gen.FailoverOptions{
		Scenarios: make([]gen.FailoverScenarioOption, 0, len(o.Scenarios)), Vips: make([]gen.FailoverVIPOption, 0, len(o.VIPs)),
		DefaultFailback: o.DefaultFailback, Prod: o.Prod, Slug: o.Slug, Window: o.Window, NextWindow: o.NextWindow,
		LastTestedAt: nullableOf(o.LastTested),
	}
	for _, sc := range o.Scenarios {
		out.Scenarios = append(out.Scenarios, gen.FailoverScenarioOption{
			Key: gen.FailoverScenario(sc.Key), Label: sc.Label, Available: sc.Available, Reason: sc.Reason,
			DefaultSeconds: sc.DefaultSeconds, Units: sc.Units,
		})
	}
	for _, v := range o.VIPs {
		out.Vips = append(out.Vips, gen.FailoverVIPOption{Id: v.ID, Address: v.Address, OwnerHostname: nullableOf(v.OwnerHostname), Probe: toAPIProbe(v.Probe)})
	}
	return out
}

func toAPITest(row store.ListFailoverTestsRow, last *gen.TestRun) gen.FailoverTest {
	t := row.FailoverTest
	var probe failover.Probe
	_ = json.Unmarshal(t.Probe, &probe)
	return gen.FailoverTest{
		Id: t.ID, ClusterId: t.ClusterID, VipId: t.VipID, VipAddress: row.VipAddress.String(), VipOwner: nullableOf(row.VipOwnerHostname),
		Name: t.Name, Scenario: gen.FailoverScenario(t.Scenario), Service: t.Service, Description: failover.Describe(t.Scenario, t.Service),
		MaxTakeoverSeconds: int(t.MaxTakeoverSeconds), ExpectFailback: t.ExpectFailback, Probe: toAPIProbe(probe),
		Scheduled: t.Scheduled, NextRunAt: nullableOf(t.NextRunAt), LastRun: nullableOf(last), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func toAPIRun(run store.TestRun, jobStatus store.NullJobStatus, requestedBy *string, clusterName string) gen.TestRun {
	out := gen.TestRun{
		Id: run.ID, Kind: gen.TestRunKind(run.Kind), Trigger: gen.TestRunTrigger(run.Trigger), ClusterId: nullableOf(run.ClusterID),
		ClusterName: clusterName, TestId: nullableOf(run.TestID), NodeId: nullableOf(run.NodeID), Hostname: run.Hostname,
		JobId: nullableOf(run.JobID), JobStatus: nullable.NewNullNullable[gen.JobStatus](),
		Result: nullable.NewNullNullable[gen.TestRunResult](), Restored: nullableOf(run.Restored), Summary: run.Summary,
		Checks: []gen.TestRunCheck{}, Timeline: []gen.TestRunEvent{}, RequestedBy: nullableOf(requestedBy),
		CreatedAt: run.CreatedAt, FinishedAt: nullableOf(run.FinishedAt),
		Measurements: nullable.NewNullNullable[gen.FailoverMeasurements](),
		Definition:   nullable.NewNullNullable[gen.FailoverDefinition](), Backup: nullable.NewNullNullable[gen.BackupVerifyReport](),
	}
	if jobStatus.Valid {
		out.JobStatus = nullable.NewNullableWithValue(gen.JobStatus(jobStatus.JobStatus))
	}
	if run.Result != nil {
		out.Result = nullable.NewNullableWithValue(gen.TestRunResult(*run.Result))
	}
	// Failovertest en back-upcontrole delen de vorm van checks en timeline.
	var checks []backups.Check
	_ = json.Unmarshal(run.Checks, &checks)
	for _, c := range checks {
		out.Checks = append(out.Checks, gen.TestRunCheck{Name: c.Name, Ok: c.OK, Detail: c.Detail, Warning: c.Warning})
	}
	var timeline []backups.TimelineEvent
	_ = json.Unmarshal(run.Timeline, &timeline)
	for _, e := range timeline {
		out.Timeline = append(out.Timeline, gen.TestRunEvent{TMs: e.TMS, Kind: gen.TestRunEventKind(e.Kind), Text: e.Text})
	}
	switch run.Kind {
	case failover.KindTest:
		failoverReport(run, &out)
	case backups.KindVerify:
		out.Backup = nullable.NewNullableWithValue(backupReport(run, nil))
	}
	return out
}

func failoverReport(run store.TestRun, out *gen.TestRun) {
	var m failover.Measurements
	_ = json.Unmarshal(run.Measurements, &m)
	meas := gen.FailoverMeasurements{
		DowntimeMs: nullableOf(m.DowntimeMS), ExpectMs: m.ExpectMS, WindowMs: m.WindowMS, TakeoverNode: m.TakeoverNode,
		TakeoverNodeId: nullableOf(m.TakeoverNodeID), TakeoverMs: nullableOf(m.TakeoverMS), FailbackMs: nullableOf(m.FailbackMS),
		ReturnedTo: m.ReturnedTo, EndMs: m.EndMS, Probe: make([]gen.TestRunSegment, 0, len(m.Probe)),
	}
	for _, sg := range m.Probe {
		meas.Probe = append(meas.Probe, gen.TestRunSegment{FromMs: sg.FromMS, ToMs: sg.ToMS, Ok: sg.OK})
	}
	var def failover.Definition
	_ = json.Unmarshal(run.Definition, &def)
	if meas.ExpectMs == 0 {
		meas.ExpectMs = int64(def.MaxTakeoverSeconds) * 1000
	}
	out.Measurements = nullable.NewNullableWithValue(meas)
	out.Definition = nullable.NewNullableWithValue(gen.FailoverDefinition{
		TestId: def.TestID, Name: def.Name, Scenario: gen.FailoverScenario(def.Scenario), Service: def.Service, Unit: def.Unit,
		Vip: def.VIP, MaxTakeoverSeconds: def.MaxTakeoverSeconds, ExpectFailback: def.ExpectFailback, Probe: toAPIProbe(def.Probe),
		Description: failover.Describe(def.Scenario, def.Service),
	})
}
