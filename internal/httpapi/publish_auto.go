package httpapi

import (
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/publish"
)

// Automatisch publiceren (fase 48). De antwoorden zijn de structs van
// internal/publish; de webinterface typt ze in web/src/lib/publish.ts.

func (s *Server) GetPublishAuto(w http.ResponseWriter, r *http.Request) {
	v, err := s.publish.AutoOverview(r.Context())
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) SavePublishAutoRule(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in publish.AutoRuleInput
	if !decode(w, r, &in) {
		return
	}
	rule, err := s.publish.SaveAutoRule(r.Context(), events.User(p.User.ID), &p.User.ID, in)
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) RetryPublishAuto(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		ServiceKey string `json:"service_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.publish.RetryAuto(r.Context(), events.User(p.User.ID), in.ServiceKey); err != nil {
		s.publishError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetPublishAutoBackup(w http.ResponseWriter, r *http.Request, params gen.GetPublishAutoBackupParams) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	b, err := s.publish.AutoBackup(r.Context(), params.ServiceKey)
	if err != nil {
		s.publishError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
