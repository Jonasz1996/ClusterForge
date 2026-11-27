package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/mailrelay"
)

// Testlijst 3, N30: de Postfix-relay op de pagina van het cluster.

func (s *Server) mailrelayError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *mailrelay.FieldError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "validation", fe.Msg)
	case errors.Is(err, mailrelay.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) mailrelayCluster(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if s.mailrelay == nil {
		writeError(w, http.StatusNotFound, "not_found", "niet beschikbaar")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "clusterId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "ongeldig id")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) mailrelayStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mailrelayCluster(w, r)
	if !ok {
		return
	}
	st, err := s.mailrelay.Status(r.Context(), id, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		s.mailrelayError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": st})
}

func (s *Server) mailrelayTestMail(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, ok := s.mailrelayCluster(w, r)
	if !ok {
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if !decode(w, r, &in) {
		return
	}
	j, err := s.mailrelay.TestMail(r.Context(), events.User(p.User.ID), id, in.To)
	if err != nil {
		s.mailrelayError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": j.ID})
}
