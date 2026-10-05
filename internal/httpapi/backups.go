package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
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
	if s.backupError(w, r, s.backups.UpdatePolicy(r.Context(), events.User(p.User.ID), &p.User.ID, id, req.MaxAgeHours)) {
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
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.Is(err, backups.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	default:
		s.internalError(w, r, err)
	}
	return true
}
