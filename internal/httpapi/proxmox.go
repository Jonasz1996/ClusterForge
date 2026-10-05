package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) ListProxmox(w http.ResponseWriter, r *http.Request) {
	conns, err := s.q.ListProxmoxConnections(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	counts, err := s.proxmoxCounts(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.ProxmoxConnection, 0, len(conns))
	for _, c := range conns {
		items = append(items, toAPIProxmox(c, counts[c.ID]))
	}
	writeJSON(w, http.StatusOK, struct {
		Items   []gen.ProxmoxConnection `json:"items"`
		Enabled bool                    `json:"enabled"`
	}{items, s.pve.Enabled()})
}

func (s *Server) proxmoxCounts(r *http.Request) (map[uuid.UUID]gen.ProxmoxCounts, error) {
	rows, err := s.q.CountProxmoxResources(r.Context())
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]gen.ProxmoxCounts{}
	for _, row := range rows {
		c := out[row.ConnectionID]
		switch row.Type {
		case "node":
			c.Hosts = int(row.Count)
		case "qemu":
			c.Vms = int(row.Count)
		case "lxc":
			c.Containers = int(row.Count)
		}
		out[row.ConnectionID] = c
	}
	return out, nil
}

func toAPIProxmox(c store.ProxmoxConnection, counts gen.ProxmoxCounts) gen.ProxmoxConnection {
	fp := ""
	if c.TlsFingerprint != "" {
		fp = proxmox.FormatFingerprint(c.TlsFingerprint)
	}
	return gen.ProxmoxConnection{
		Id: c.ID, Name: c.Name, ApiUrl: c.ApiUrl, TokenId: c.TokenID, TlsFingerprint: fp, PveVersion: c.PveVersion,
		LastSyncAt: nullableOf(c.LastSyncAt), LastError: c.LastError, Counts: counts,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func (s *Server) writeProxmox(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	c, err := s.q.GetProxmoxConnection(r.Context(), id)
	if s.proxmoxError(w, r, err) {
		return
	}
	counts, err := s.proxmoxCounts(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, toAPIProxmox(c, counts[id]))
}

func (s *Server) GetProxmox(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.writeProxmox(w, r, http.StatusOK, id)
}

func (s *Server) CreateProxmox(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.ProxmoxInput
	if !decode(w, r, &req) {
		return
	}
	c, err := s.pve.Create(r.Context(), events.User(p.User.ID), proxmox.Fields{
		Name: req.Name, URL: req.ApiUrl, TokenID: req.TokenId, TokenSecret: deref(req.TokenSecret),
		Fingerprint: deref(req.TlsFingerprint),
	})
	if s.proxmoxError(w, r, err) {
		return
	}
	s.writeProxmox(w, r, http.StatusCreated, c.ID)
}

func (s *Server) UpdateProxmox(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.ProxmoxPatch
	if !decode(w, r, &req) {
		return
	}
	_, err := s.pve.Update(r.Context(), events.User(p.User.ID), id, func(f *proxmox.Fields) {
		set(&f.Name, req.Name)
		set(&f.URL, req.ApiUrl)
		set(&f.TokenID, req.TokenId)
		set(&f.TokenSecret, req.TokenSecret)
		set(&f.Fingerprint, req.TlsFingerprint)
	})
	if s.proxmoxError(w, r, err) {
		return
	}
	// Met nieuwe gegevens meteen opnieuw syncen.
	_ = s.pve.Sync(r.Context(), id)
	s.writeProxmox(w, r, http.StatusOK, id)
}

func (s *Server) DeleteProxmox(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.proxmoxError(w, r, s.pve.Delete(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ProbeProxmox(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var req gen.ProbeProxmoxJSONBody
	if !decode(w, r, &req) {
		return
	}
	base, err := proxmox.NormalizeURL(req.ApiUrl)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	res, err := proxmox.Probe(r.Context(), base)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreachable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, gen.ProxmoxProbe{
		ApiUrl: base, Fingerprint: proxmox.FormatFingerprint(res.Fingerprint), Subject: res.Subject,
		Issuer: res.Issuer, NotAfter: res.NotAfter, Trusted: res.Trusted,
	})
}

func (s *Server) SyncProxmox(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	// Een fout van Proxmox staat daarna in last_error, een fout bij het
	// lezen van de back-ups in de back-upstand van de koppeling.
	if err := s.pve.Sync(r.Context(), id); errors.Is(err, proxmox.ErrNotFound) {
		s.proxmoxError(w, r, err)
		return
	}
	_ = s.backups.Inventory(r.Context(), id)
	s.writeProxmox(w, r, http.StatusOK, id)
}

func (s *Server) GetProxmoxResources(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, err := s.q.GetProxmoxConnection(r.Context(), id); s.proxmoxError(w, r, err) {
		return
	}
	rows, err := s.q.ListProxmoxResources(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.ProxmoxResources{Hosts: []gen.ProxmoxHost{}, Guests: []gen.ProxmoxGuest{}, Storages: []gen.ProxmoxStorage{}}
	for _, row := range rows {
		var res proxmox.Resource
		if err := json.Unmarshal(row.ProxmoxResource.Data, &res); err != nil {
			continue
		}
		switch res.Type {
		case "node":
			out.Hosts = append(out.Hosts, gen.ProxmoxHost{
				Name: res.Node, Status: res.Status, Cpu: res.CPU, Maxcpu: res.MaxCPU, Mem: res.Mem, Maxmem: res.MaxMem,
				Disk: res.Disk, Maxdisk: res.MaxDisk, Uptime: res.Uptime,
			})
		case "qemu", "lxc":
			tags := append([]string{}, strings.FieldsFunc(res.Tags, func(r rune) bool { return r == ';' || r == ',' || r == ' ' })...)
			out.Guests = append(out.Guests, gen.ProxmoxGuest{
				Vmid: res.VMID, Type: gen.GuestType(res.Type), Name: res.Name, Host: res.Node, Status: res.Status,
				Template: res.Template == 1, Cpu: res.CPU, Maxcpu: res.MaxCPU, Mem: res.Mem, Maxmem: res.MaxMem,
				Disk: res.Disk, Maxdisk: res.MaxDisk, Uptime: res.Uptime, Tags: tags, Lock: res.Lock,
				NodeId: nullableOf(row.NodeID), NodeHostname: nullableOf(row.NodeHostname),
			})
		case "storage":
			out.Storages = append(out.Storages, gen.ProxmoxStorage{
				Name: res.Storage, Host: res.Node, Shared: res.Shared == 1, Type: res.Plugin, Content: res.Content,
				Disk: res.Disk, Maxdisk: res.MaxDisk,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) VmAction(w http.ResponseWriter, r *http.Request, id uuid.UUID, vmid int) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.VmActionInput
	if !decode(w, r, &req) {
		return
	}
	j, err := s.pve.RequestAction(r.Context(), events.User(p.User.ID), id, vmid, proxmox.ActionRequest{
		Action: string(req.Action), SnapshotName: deref(req.SnapshotName), Description: deref(req.Description),
		VMState: deref(req.Vmstate), Target: deref(req.Target),
	})
	if s.proxmoxError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) ListVmSnapshots(w http.ResponseWriter, r *http.Request, id uuid.UUID, vmid int) {
	snaps, err := s.pve.Snapshots(r.Context(), id, vmid)
	if s.proxmoxError(w, r, err) {
		return
	}
	items := make([]gen.ProxmoxSnapshot, 0, len(snaps))
	for _, sn := range snaps {
		var t *time.Time
		if sn.Time > 0 {
			v := time.Unix(sn.Time, 0)
			t = &v
		}
		items = append(items, gen.ProxmoxSnapshot{
			Name: sn.Name, Description: strings.TrimSpace(sn.Description), Parent: sn.Parent, Time: nullableOf(t),
			Vmstate: sn.VMState == 1,
		})
	}
	writeJSON(w, http.StatusOK, list[gen.ProxmoxSnapshot]{items})
}

// proxmoxError schrijft een passende foutrespons en geeft true terug als
// err niet nil was.
func (s *Server) proxmoxError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ve proxmox.ValidationError
	var ce proxmox.ConflictError
	var ue *proxmox.UpstreamError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	case errors.Is(err, proxmox.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	case errors.Is(err, proxmox.ErrNoMasterKey):
		writeError(w, http.StatusServiceUnavailable, "no_master_key", err.Error())
	case errors.As(err, &ue):
		writeError(w, http.StatusBadGateway, "proxmox_error", ue.Error())
	default:
		s.internalError(w, r, err)
	}
	return true
}

// --- taken ---

func toAPIJob(j store.Job, requestedBy *string) gen.Job {
	return gen.Job{
		Id: j.ID, Kind: j.Kind, Title: j.Title, Status: gen.JobStatus(j.Status), Error: j.Error,
		ClusterId: nullableOf(j.ClusterID), NodeId: nullableOf(j.NodeID), ProxmoxId: nullableOf(j.ProxmoxID),
		RequestedBy: nullableOf(requestedBy), Attempts: int(j.Attempts), CancelRequested: j.CancelRequested,
		Retryable: retryableKinds[j.Kind] && (j.Status == store.JobStatusFailed || j.Status == store.JobStatusCanceled),
		CreatedAt: j.CreatedAt, StartedAt: nullableOf(j.StartedAt), FinishedAt: nullableOf(j.FinishedAt),
	}
}

func (s *Server) ListJobs(w http.ResponseWriter, r *http.Request, params gen.ListJobsParams) {
	limit := 50
	if params.Limit != nil {
		limit = max(1, min(*params.Limit, 200))
	}
	rows, err := s.q.ListJobs(r.Context(), store.ListJobsParams{
		NodeID: params.NodeId, ProxmoxID: params.ProxmoxId, ClusterID: params.ClusterId, MaxRows: int32(limit),
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.Job, 0, len(rows))
	for _, row := range rows {
		items = append(items, toAPIJob(row.Job, row.RequestedByName))
	}
	writeJSON(w, http.StatusOK, list[gen.Job]{items})
}

func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	row, err := s.q.GetJob(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "taak niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	steps, err := s.q.ListJobSteps(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	j := toAPIJob(row.Job, row.RequestedByName)
	d := gen.JobDetail{
		Id: j.Id, Kind: j.Kind, Title: j.Title, Status: j.Status, Error: j.Error, ClusterId: j.ClusterId,
		NodeId: j.NodeId, ProxmoxId: j.ProxmoxId, RequestedBy: j.RequestedBy, Attempts: j.Attempts,
		CancelRequested: j.CancelRequested, Retryable: j.Retryable, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt,
		FinishedAt: j.FinishedAt, Steps: make([]gen.JobStep, 0, len(steps)),
	}
	for _, st := range steps {
		d.Steps = append(d.Steps, gen.JobStep{
			Seq: int(st.Seq), Name: st.Name, Status: gen.JobStatus(st.Status), StartedAt: st.StartedAt,
			FinishedAt: nullableOf(st.FinishedAt), Log: nonNil(st.Log), Error: st.Error,
		})
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) CancelJob(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	j, err := s.jobs.Cancel(r.Context(), id, events.User(p.User.ID))
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "taak niet gevonden")
	case errors.Is(err, jobs.ErrFinished):
		writeError(w, http.StatusConflict, "conflict", "de taak is al klaar")
	case err != nil:
		s.internalError(w, r, err)
	default:
		row, err := s.q.GetJob(r.Context(), j.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, toAPIJob(row.Job, row.RequestedByName))
	}
}
