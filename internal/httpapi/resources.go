package httpapi

import (
	"errors"
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/pvehost"
	"github.com/Jonasz1996/clusterforge/internal/resources"
)

// Testlijst 3, N27: voorstellen voor resources met één klik.

func (s *Server) resourcesError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *resources.FieldError
	var pe pvehost.ValidationError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.As(err, &pe):
		writeError(w, http.StatusBadRequest, "invalid", pe.Error())
	case errors.Is(err, resources.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) resourcesList(w http.ResponseWriter, r *http.Request) {
	ps, err := s.resources.Proposals(r.Context(), r.URL.Query().Get("fresh") == "1")
	if err != nil {
		s.resourcesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ps})
}

func (s *Server) resourcesApply(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &in) {
		return
	}
	j, err := s.resources.Apply(r.Context(), events.User(p.User.ID), in.ID)
	if err != nil {
		s.resourcesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": j.ID})
}

func (s *Server) resourcesSettingsGet(w http.ResponseWriter, r *http.Request) {
	st, err := s.resources.Settings(r.Context())
	if err != nil {
		s.resourcesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) resourcesSettingsPut(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in resources.Settings
	if !decode(w, r, &in) {
		return
	}
	st, err := s.resources.SetSettings(r.Context(), events.User(p.User.ID), in)
	if err != nil {
		s.resourcesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
