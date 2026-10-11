package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/disaster"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) ListDisasterScenarios(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	opts, err := s.disaster.Options(ctx, id)
	if s.failoverError(w, r, err) {
		return
	}
	rows, err := s.q.ListDisasterScenarios(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	last, conns, err := s.disasterContext(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.DisasterScenarioList{Items: make([]gen.DisasterScenario, 0, len(rows)), Options: toAPIDisasterOptions(opts)}
	for _, row := range rows {
		out.Items = append(out.Items, toAPIScenario(disaster.Decode(row), last[row.ID], conns))
	}
	writeJSON(w, http.StatusOK, out)
}

// disasterContext leest de laatste run per scenario van een cluster en de
// namen van de Proxmox-verbindingen.
func (s *Server) disasterContext(ctx context.Context, clusterID uuid.UUID) (map[uuid.UUID]*gen.TestRun, map[uuid.UUID]string, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.q.LatestDisasterRuns(ctx, clusterID)
	if err != nil {
		return nil, nil, err
	}
	last := map[uuid.UUID]*gen.TestRun{}
	for _, row := range rows {
		if row.TestRun.ScenarioID != nil {
			run := toAPIRun(row.TestRun, row.JobStatus, nil, c.Name)
			last[*row.TestRun.ScenarioID] = &run
		}
	}
	pc, err := s.q.ListProxmoxConnections(ctx)
	if err != nil {
		return nil, nil, err
	}
	conns := make(map[uuid.UUID]string, len(pc))
	for _, p := range pc {
		conns[p.ID] = p.Name
	}
	return last, conns, nil
}

func (s *Server) CreateDisasterScenario(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.DisasterScenarioInput
	if !decode(w, r, &req) {
		return
	}
	sc, err := s.disaster.Create(r.Context(), events.User(p.User.ID), id, fromAPIDisasterInput(req))
	if s.failoverError(w, r, err) {
		return
	}
	s.writeDisasterScenario(w, r, sc.ID, http.StatusCreated)
}

func (s *Server) UpdateDisasterScenario(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.DisasterScenarioInput
	if !decode(w, r, &req) {
		return
	}
	_, err := s.disaster.Update(r.Context(), events.User(p.User.ID), id, fromAPIDisasterInput(req))
	if s.failoverError(w, r, err) {
		return
	}
	s.writeDisasterScenario(w, r, id, http.StatusOK)
}

func (s *Server) DeleteDisasterScenario(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.failoverError(w, r, s.disaster.Delete(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeDisasterScenario(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int) {
	ctx := r.Context()
	row, err := s.q.GetDisasterScenario(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "rampoefening niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	last, conns, err := s.disasterContext(ctx, row.ClusterID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, toAPIScenario(disaster.Decode(row), last[id], conns))
}

func (s *Server) PreviewDisasterScenario(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, err := s.disaster.Preview(r.Context(), id)
	if s.failoverError(w, r, err) {
		return
	}
	out := gen.DisasterPreview{
		Ok: p.OK, Checks: make([]gen.TestRunCheck, 0, len(p.Checks)), Description: p.Description,
		Targets: toAPITargets(p.Targets), Clusters: make([]gen.DisasterClusterPlan, 0, len(p.Clusters)),
		Skipped: orEmpty(p.Skipped), Notes: orEmpty(p.Notes), Confirm: orEmpty(p.Confirm),
	}
	for _, c := range p.Checks {
		out.Checks = append(out.Checks, gen.TestRunCheck{Name: c.Name, Ok: c.OK, Detail: c.Detail})
	}
	for _, c := range p.Clusters {
		out.Clusters = append(out.Clusters, gen.DisasterClusterPlan{
			Id: c.ID, Name: c.Name, Slug: c.Slug, Environment: c.Environment, Role: gen.DisasterClusterPlanRole(c.Role),
			Before: c.Before, Predicted: toAPIPrediction(c.Predicted), Vips: toAPIDisasterVIPs(c.VIPs),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) RunDisasterScenario(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.DisasterRunInput
	if !decode(w, r, &req) {
		return
	}
	run, err := s.disaster.Start(r.Context(), events.User(p.User.ID), id, req.Confirm)
	if s.failoverError(w, r, err) {
		return
	}
	s.writeTestRun(w, r, run.ID, http.StatusAccepted)
}

func fromAPIDisasterInput(in gen.DisasterScenarioInput) disaster.Input {
	out := disaster.Input{
		Name: in.Name, Kind: string(in.Kind), ConnectionID: in.ConnectionId, FaultSeconds: in.FaultSeconds, RecoverySeconds: in.RecoverySeconds,
	}
	if in.Host != nil {
		out.Host = *in.Host
	}
	if in.Unit != nil {
		out.Unit = *in.Unit
	}
	return out
}

func toAPIDisasterOptions(o disaster.Options) gen.DisasterOptions {
	out := gen.DisasterOptions{
		Allowed: o.Allowed, Reason: o.Reason, Slug: o.Slug, Kinds: make([]gen.DisasterKindOption, 0, len(o.Kinds)),
		Hosts: make([]gen.DisasterHostOption, 0, len(o.Hosts)), Units: orEmpty(o.Units), Vips: orEmpty(o.VIPs),
	}
	for _, k := range o.Kinds {
		out.Kinds = append(out.Kinds, gen.DisasterKindOption{
			Key: gen.DisasterKind(k.Key), Label: k.Label, Available: k.Available, Reason: k.Reason,
			FaultSeconds: k.FaultSeconds, MinFaultSeconds: disaster.MinFault(k.Key), RecoverySeconds: k.RecoverySeconds,
		})
	}
	for _, h := range o.Hosts {
		out.Hosts = append(out.Hosts, gen.DisasterHostOption{
			ConnectionId: h.ConnectionID, Connection: h.Connection, Host: h.Host, Nodes: orEmpty(h.Nodes), Others: orEmpty(h.Others),
		})
	}
	return out
}

func toAPIScenario(sc disaster.Scenario, last *gen.TestRun, conns map[uuid.UUID]string) gen.DisasterScenario {
	out := gen.DisasterScenario{
		Id: sc.ID, ClusterId: sc.ClusterID, Name: sc.Name, Kind: gen.DisasterKind(sc.Kind), Description: disaster.Describe(sc.Kind, sc.P),
		Host: nullable.NewNullNullable[gen.DisasterHost](), Unit: sc.P.Unit, FaultSeconds: sc.E.FaultSeconds,
		RecoverySeconds: sc.E.RecoverySeconds, LastRun: nullableOf(last), CreatedAt: sc.CreatedAt, UpdatedAt: sc.UpdatedAt,
	}
	if sc.Kind == disaster.HostDown && sc.P.ConnectionID != nil {
		out.Host = nullable.NewNullableWithValue(gen.DisasterHost{
			ConnectionId: *sc.P.ConnectionID, Connection: conns[*sc.P.ConnectionID], Host: sc.P.Host,
		})
	}
	return out
}

func toAPIPrediction(p disaster.Prediction) gen.DisasterPrediction {
	return gen.DisasterPrediction{Status: gen.DisasterPredictionStatus(p.Status), Reason: p.Reason}
}

func toAPIDisasterVIPs(vs []disaster.VIPPlan) []gen.DisasterVIP {
	out := make([]gen.DisasterVIP, 0, len(vs))
	for _, v := range vs {
		out = append(out, gen.DisasterVIP{Address: v.Address, Probe: toAPIProbe(v.Probe)})
	}
	return out
}

func toAPITargets(ts []disaster.Target) []gen.DisasterTarget {
	out := make([]gen.DisasterTarget, 0, len(ts))
	for _, t := range ts {
		dt := gen.DisasterTarget{
			NodeId: t.NodeID, Hostname: t.Hostname, ClusterId: t.ClusterID, Cluster: t.Cluster, Unit: t.Unit,
			Vmid: nullable.NewNullNullable[int](),
		}
		if t.VM != nil {
			dt.Vmid = nullable.NewNullableWithValue(t.VM.VMID)
		}
		out = append(out, dt)
	}
	return out
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// disasterReport zet de voorspelling en, na de run, de meting in out.
// Tijdens de run staat de voorspelling er met measured onbekend.
func disasterReport(run store.TestRun, out *gen.TestRun) {
	var def disaster.Definition
	_ = json.Unmarshal(run.Definition, &def)
	var m disaster.Measurements
	_ = json.Unmarshal(run.Measurements, &m)
	rep := gen.DisasterReport{
		ScenarioId: def.ScenarioID, Name: def.Name, Kind: gen.DisasterKind(def.Kind), Description: def.Description, Unit: def.Unit,
		FaultMs: m.FaultMS, ExpectRecoveryMs: m.ExpectRecoveryMS, ShortOutageMs: m.ShortOutageMS,
		ClearMs: nullableOf(m.ClearMS), RecoveryMs: nullableOf(m.RecoveryMS), TotalMs: nullableOf(m.TotalMS), EndMs: m.EndMS,
		Targets: toAPITargets(def.Targets), TargetResults: make([]gen.DisasterTargetResult, 0, len(m.Targets)),
		Clusters: []gen.DisasterClusterResult{}, Vips: make([]gen.DisasterVIPResult, 0, len(m.VIPs)),
		Skipped: orEmpty(def.Skipped), Notes: orEmpty(def.Notes),
	}
	if rep.FaultMs == 0 {
		rep.FaultMs = int64(def.Expect.FaultSeconds) * 1000
	}
	if rep.ExpectRecoveryMs == 0 {
		rep.ExpectRecoveryMs = int64(def.Expect.RecoverySeconds) * 1000
	}
	if m.Notes != nil {
		rep.Notes = m.Notes
	}
	for _, t := range m.Targets {
		rep.TargetResults = append(rep.TargetResults, gen.DisasterTargetResult{NodeId: t.NodeID, Hostname: t.Hostname, Error: t.Error})
	}
	rows := m.Clusters
	if rows == nil {
		for _, c := range def.Clusters {
			rows = append(rows, disaster.ClusterResult{ClusterPlan: c, Measured: disaster.Prediction{Status: "unknown"}, Match: disaster.MatchUnknown})
		}
	}
	for _, c := range rows {
		rep.Clusters = append(rep.Clusters, gen.DisasterClusterResult{
			Id: c.ID, Name: c.Name, Slug: c.Slug, Environment: c.Environment, Role: gen.DisasterClusterResultRole(c.Role),
			Before: c.Before, Predicted: toAPIPrediction(c.Predicted), Measured: toAPIPrediction(c.Measured), LongestMs: c.LongestMS,
			RecoveredMs: nullableOf(c.RecoveredMS), Match: gen.DisasterClusterResultMatch(c.Match), Slow: c.Slow,
		})
	}
	for _, v := range m.VIPs {
		segs := make([]gen.TestRunSegment, 0, len(v.Segments))
		for _, sg := range v.Segments {
			segs = append(segs, gen.TestRunSegment{FromMs: sg.FromMS, ToMs: sg.ToMS, Ok: sg.OK})
		}
		rep.Vips = append(rep.Vips, gen.DisasterVIPResult{ClusterId: v.ClusterID, Address: v.Address, Probe: toAPIProbe(v.Probe), Segments: segs})
	}
	out.Disaster = nullable.NewNullableWithValue(rep)
}
