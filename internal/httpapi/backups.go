package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) GetBackups(w http.ResponseWriter, r *http.Request) {
	ov, err := s.backups.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (s *Server) GetNodeBackups(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	nb, err := s.backups.ForNode(r.Context(), id)
	if s.backupError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, nb)
}

func (s *Server) GetBackupPolicy(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, err := s.backups.Policy(r.Context(), id)
	if s.backupError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) UpdateBackupPolicy(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.BackupPolicyInput
	if !decode(w, r, &req) {
		return
	}
	actor := events.User(p.User.ID)
	if req.MaxAgeHours != nil && s.backupError(w, r, s.backups.UpdatePolicy(r.Context(), actor, &p.User.ID, id, *req.MaxAgeHours)) {
		return
	}
	if req.VerifyEnabled != nil && s.backupError(w, r, s.backups.SetVerifySchedule(r.Context(), actor, &p.User.ID, id, *req.VerifyEnabled)) {
		return
	}
	s.GetBackupPolicy(w, r, id)
}

func (s *Server) SetBackupWatch(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.BackupWatchInput
	if !decode(w, r, &req) {
		return
	}
	entries := make([]backups.WatchEntry, 0, len(req.Items))
	for _, it := range req.Items {
		entries = append(entries, backups.WatchEntry{VMID: it.Vmid, Label: it.Label})
	}
	if s.backupError(w, r, s.backups.SetWatch(r.Context(), events.User(p.User.ID), id, entries)) {
		return
	}
	ov, err := s.backups.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, c := range ov.Connections {
		if c.ID == id {
			writeJSON(w, http.StatusOK, c)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
}

func (s *Server) backupError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ve backups.ValidationError
	var ce backups.ConflictError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, ce.Code, ce.Msg)
	case errors.Is(err, backups.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	default:
		s.internalError(w, r, err)
	}
	return true
}

func (s *Server) VerifyNodeBackup(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.BackupVerifyInput
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	volid := ""
	if req.Volid != nil {
		volid = *req.Volid
	}
	run, err := s.backups.StartVerify(r.Context(), events.User(p.User.ID), id, volid)
	if s.backupError(w, r, err) {
		return
	}
	s.writeTestRun(w, r, run.ID, http.StatusAccepted)
}

func (s *Server) CleanupBackupSandbox(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	// Stoppen en verwijderen duurt bij Proxmox even; de aanvraag mag de
	// gewone schrijflimiet van de server overschrijden.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(4 * time.Minute))
	_, err := s.backups.Cleanup(ctx, events.User(p.User.ID), id)
	var ce backups.ConflictError
	if err != nil && !errors.As(err, &ce) && !errors.Is(err, backups.ErrNotFound) {
		writeError(w, http.StatusBadGateway, "proxmox", "opruimen mislukt: "+err.Error())
		return
	}
	if s.backupError(w, r, err) {
		return
	}
	sb, err := s.backups.Sandbox(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISandbox(sb))
}

func (s *Server) ListTestRuns(w http.ResponseWriter, r *http.Request, params gen.ListTestRunsParams) {
	lim := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 200 {
			writeError(w, http.StatusBadRequest, "validation", "limit moet tussen 1 en 200 liggen")
			return
		}
		lim = *params.Limit
	}
	arg := store.ListTestRunsFilteredParams{ClusterID: params.ClusterId, NodeID: params.NodeId, Lim: int32(lim)}
	if params.Kind != nil {
		k := string(*params.Kind)
		arg.Kind = &k
	}
	if params.Result != nil {
		res := string(*params.Result)
		arg.Result = &res
	}
	rows, err := s.q.ListTestRunsFiltered(r.Context(), arg)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.TestRunList{Items: make([]gen.TestRun, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, toAPIRun(row.TestRun, row.JobStatus, row.RequestedByName, deref(row.ClusterName)))
	}
	writeJSON(w, http.StatusOK, out)
}

// backupReport is het deel van het rapport dat alleen een back-upcontrole
// heeft. sb is nil in een lijst, of als er geen sandbox kwam.
func backupReport(run store.TestRun, sb *backups.Sandbox) gen.BackupVerifyReport {
	var def backups.Definition
	_ = json.Unmarshal(run.Definition, &def)
	var m backups.Measurements
	_ = json.Unmarshal(run.Measurements, &m)
	out := gen.BackupVerifyReport{
		Definition: gen.BackupVerifyDefinition{
			Volid: def.Volid, BackupTime: def.BackupTime, Size: def.Size, Format: def.Format, BackupStorage: def.BackupStorage,
			ConnectionId: def.ConnectionID, ConnectionName: def.ConnectionName, SourceVmid: def.SourceVMID,
			GuestName: def.GuestName, Chosen: def.Chosen,
		},
		Measurements: gen.BackupVerifyMeasurements{
			RestoreSeconds: nullableOf(m.RestoreSeconds), BootSeconds: nullableOf(m.BootSeconds),
			CheckSeconds: nullableOf(m.CheckSeconds), CleanupSeconds: nullableOf(m.CleanupSeconds),
			TotalSeconds: nullableOf(m.TotalSeconds), Host: m.Host, Storage: m.Storage, SandboxVmid: m.SandboxVMID,
			Hostname: m.Hostname, Os: m.OS, Filesystems: m.Filesystems, DestroyedAt: nullableOf(m.DestroyedAt),
			AgentVersion: m.AgentVersion, ServicesExpected: m.ServicesExpected, ServicesActive: m.ServicesActive,
			Databases: nonNil(m.Databases),
		},
		Sandbox: nullable.NewNullNullable[gen.BackupSandbox](),
	}
	if sb != nil {
		out.Sandbox = nullable.NewNullableWithValue(toAPISandbox(*sb))
	}
	return out
}

func toAPISandbox(sb backups.Sandbox) gen.BackupSandbox {
	out := gen.BackupSandbox{
		Id: sb.ID, ConnectionId: sb.ConnectionID, ConnectionName: sb.ConnectionName, Vmid: sb.VMID, SourceVmid: sb.SourceVMID,
		Source: nullable.NewNullNullable[gen.BackupRef](), RunId: nullableOf(sb.RunID), Running: sb.Running, Volid: sb.Volid,
		State: gen.BackupSandboxState(sb.State), Host: sb.Host, Storage: sb.Storage, Error: sb.Error, CreatedAt: sb.CreatedAt,
		DestroyedAt: nullableOf(sb.DestroyedAt),
	}
	if sb.Source != nil {
		out.Source = nullable.NewNullableWithValue(gen.BackupRef{Id: sb.Source.ID, Name: sb.Source.Name})
	}
	return out
}
