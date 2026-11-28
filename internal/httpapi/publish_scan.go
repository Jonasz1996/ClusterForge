package httpapi

import (
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/publish"
)

// Scannen (fase 49). De antwoorden zijn de structs van internal/publish;
// de webinterface typt ze in web/src/lib/publish.ts. Toepassen gaat langs
// /publish/plan en /publish/actions met action "scan".

func (s *Server) GetPublishScan(w http.ResponseWriter, r *http.Request) {
	v, err := s.publish.ScanOverview(r.Context())
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) RunPublishScan(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	sc, err := s.publish.RunScan(r.Context(), events.User(p.User.ID), &p.User.ID)
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) SavePublishPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in publish.Policy
	if !decode(w, r, &in) {
		return
	}
	pol, err := s.publish.SavePolicy(r.Context(), events.User(p.User.ID), &p.User.ID, in)
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pol)
}

func (s *Server) DeletePublishPolicy(w http.ResponseWriter, r *http.Request, name string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.publish.DeletePolicy(r.Context(), events.User(p.User.ID), name); err != nil {
		s.publishError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetPublishOwners(w http.ResponseWriter, r *http.Request) {
	v, err := s.publish.Owners(r.Context())
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) SetPublishOwner(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in publish.OwnerInput
	if !decode(w, r, &in) {
		return
	}
	if err := s.publish.SetOwner(r.Context(), events.User(p.User.ID), &p.User.ID, in); err != nil {
		s.publishError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
