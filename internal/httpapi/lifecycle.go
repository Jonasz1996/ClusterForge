package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
)

func (s *Server) NodeAction(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.NodeActionInput
	if !decode(w, r, &req) {
		return
	}
	j, err := s.life.RequestAction(r.Context(), events.User(p.User.ID), id, lifecycle.ActionRequest{
		Action: string(req.Action), Reason: deref(req.Reason), Drain: req.Drain, Force: deref(req.Force),
	})
	var ve lifecycle.ValidationError
	var ce lifecycle.ConflictError
	var fe lifecycle.NeedsForceError
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.As(err, &fe):
		writeError(w, http.StatusConflict, "needs_force", fe.Msg)
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	case errors.Is(err, lifecycle.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
	default:
		s.internalError(w, r, err)
	}
}
