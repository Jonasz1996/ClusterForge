package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/truenas"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TrueNAS met een agent (fase 37), buiten de OpenAPI. De antwoorden zijn
// de structs van internal/truenas; de webinterface typt ze in
// web/src/lib/truenas.ts.

func (s *Server) trueNASError(w http.ResponseWriter, err error) {
	var ve truenas.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid", ve.Msg)
	case errors.Is(err, truenas.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.log.Warn("TrueNAS", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func trueNASNode(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "node"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "ongeldig node-id")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) listTrueNAS(w http.ResponseWriter, r *http.Request) {
	list, err := s.truenas.Servers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "protocol": protocol.TrueNASSince})
}

func (s *Server) trueNASOverview(w http.ResponseWriter, r *http.Request) {
	id, ok := trueNASNode(w, r)
	if !ok {
		return
	}
	o, err := s.truenas.Overview(r.Context(), id)
	if err != nil {
		s.trueNASError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) trueNASGet(w http.ResponseWriter, r *http.Request) {
	id, ok := trueNASNode(w, r)
	if !ok {
		return
	}
	p, _ := principalFrom(r.Context())
	var args json.RawMessage
	if a := r.URL.Query().Get("args"); a != "" {
		args = json.RawMessage(a)
	}
	data, err := s.truenas.Get(r.Context(), id, r.URL.Query().Get("method"), args, p.User.Role == store.UserRoleAdmin)
	if err != nil {
		s.trueNASError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) trueNASCall(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, ok := trueNASNode(w, r)
	if !ok {
		return
	}
	var in truenas.Call
	if !decode(w, r, &in) {
		return
	}
	j, err := s.truenas.StartCall(r.Context(), events.User(p.User.ID), id, in)
	if err != nil {
		s.trueNASError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}
