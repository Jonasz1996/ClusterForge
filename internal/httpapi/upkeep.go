package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/upkeep"
)

// Automatisch onderhoud door de agents (fase 46). Buiten de OpenAPI, zoals
// ClusterForge zelf.

func (s *Server) upkeepError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *upkeep.FieldError
	var ce *upkeep.ConflictError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "validation", fe.Msg)
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	case errors.Is(err, upkeep.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "dit beleid bestaat niet")
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) upkeepOverview(w http.ResponseWriter, r *http.Request) {
	if s.upkeep == nil {
		writeError(w, http.StatusNotFound, "not_found", "niet beschikbaar")
		return
	}
	scopes, err := s.upkeep.Overview(r.Context())
	if err != nil {
		s.upkeepError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": time.Local.String(), "defaults": upkeep.DefaultPolicy, "scopes": scopes,
	})
}

func (s *Server) upkeepHistory(w http.ResponseWriter, r *http.Request) {
	if s.upkeep == nil {
		writeError(w, http.StatusNotFound, "not_found", "niet beschikbaar")
		return
	}
	var node, cluster *uuid.UUID
	if v := r.URL.Query().Get("node"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation", "ongeldige node")
			return
		}
		node = &id
	}
	if v := r.URL.Query().Get("cluster"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation", "ongeldig cluster")
			return
		}
		cluster = &id
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.upkeep.History(r.Context(), node, cluster, limit)
	if err != nil {
		s.upkeepError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": rows})
}

func (s *Server) upkeepSetPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Mode   string        `json:"mode"`
		Policy upkeep.Policy `json:"policy"`
	}
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	if err := s.upkeep.SetPolicy(r.Context(), events.User(uid), &uid, chi.URLParam(r, "scope"), in.Mode, in.Policy); err != nil {
		s.upkeepError(w, r, err)
		return
	}
	s.upkeepOverview(w, r)
}

func (s *Server) upkeepStart(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Preview bool `json:"preview"`
	}
	if !decode(w, r, &in) {
		return
	}
	mode := upkeep.ModeRun
	if in.Preview {
		mode = upkeep.ModePreview
	}
	id, err := s.upkeep.Start(r.Context(), events.User(p.User.ID), chi.URLParam(r, "scope"), mode, false)
	if err != nil {
		s.upkeepError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id})
}
