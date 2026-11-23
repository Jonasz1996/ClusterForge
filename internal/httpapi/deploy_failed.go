package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
)

// Fase 48 (4.2): een mislukte uitrol opruimen, of opnieuw met andere
// instellingen. Buiten de OpenAPI, zoals /replacements; de webinterface
// typt ze in web/src/lib/deployFailed.ts.

func (s *Server) deployJobID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "ongeldig id")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) deployFailedError(w http.ResponseWriter, r *http.Request, err error) {
	var ve deploy.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "validation", "message": ve.Msg, "field": optional(ve.Field)})
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "taak niet gevonden")
	case errors.Is(err, deploy.ErrNotDeploy):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) getFailedDeploy(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := s.deployJobID(w, r)
	if !ok {
		return
	}
	f, err := s.deploy.FailedDeploy(r.Context(), id)
	if err != nil {
		s.deployFailedError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) cleanupFailedDeploy(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, ok := s.deployJobID(w, r)
	if !ok {
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
		Guests  bool   `json:"guests"`
	}
	if !decode(w, r, &in) {
		return
	}
	j, err := s.deploy.StartCleanup(r.Context(), events.User(p.User.ID), id, deploy.CleanupRequest{Confirm: in.Confirm, Guests: in.Guests})
	if err != nil {
		s.deployFailedError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}
