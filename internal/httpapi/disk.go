package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/diskwatch"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// Schijfbewaking (fase 41): drempels, de stand per node, meten en opruimen.

func (s *Server) diskError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *diskwatch.FieldError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.Is(err, diskwatch.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "deze node bestaat niet of heeft geen agent")
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) GetDiskWatchSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.disk.Settings(r.Context())
	if err != nil {
		s.diskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.DiskWatchSettings{WarnPct: st.WarnPct, CritPct: st.CritPct, IgnoreMounts: st.IgnoreMounts})
}

func (s *Server) SetDiskWatchSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.DiskWatchSettings
	if !decode(w, r, &in) {
		return
	}
	st, err := s.disk.SetSettings(r.Context(), events.User(p.User.ID), diskwatch.Settings{WarnPct: in.WarnPct, CritPct: in.CritPct, IgnoreMounts: in.IgnoreMounts})
	if err != nil {
		s.diskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.DiskWatchSettings{WarnPct: st.WarnPct, CritPct: st.CritPct, IgnoreMounts: st.IgnoreMounts})
}

func (s *Server) GetNodeDisk(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	ctx := r.Context()
	n, err := s.disk.Node(ctx, nodeId)
	if err != nil {
		s.diskError(w, r, err)
		return
	}
	st, err := s.disk.Settings(ctx)
	if err != nil {
		s.diskError(w, r, err)
		return
	}
	out := gen.NodeDisk{NodeId: n.NodeID, Hostname: n.Hostname, UsedRatio: float32(n.Ratio), Mount: n.Mount, Level: gen.NodeDiskLevel(n.Level),
		WarnPct: st.WarnPct, CritPct: st.CritPct, Online: n.Online, RunningJobId: n.RunningJob}
	if n.Scan != nil {
		var sc gen.DiskScan
		if b, err := json.Marshal(n.Scan); err == nil && json.Unmarshal(b, &sc) == nil {
			out.Scan = &sc
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ScanNodeDisk(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := s.disk.StartScan(r.Context(), events.User(p.User.ID), nodeId)
	if err != nil {
		s.diskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id})
}

func (s *Server) CleanupNodeDisk(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CleanupNodeDiskJSONBody
	if !decode(w, r, &in) {
		return
	}
	id, err := s.disk.StartCleanup(r.Context(), events.User(p.User.ID), nodeId, in.Items)
	if err != nil {
		s.diskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id})
}
