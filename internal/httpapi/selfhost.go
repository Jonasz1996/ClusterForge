package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/selfhost"
)

// ClusterForge zelf (fase 45): bijwerken vanuit de webinterface en
// verhuizen naar een eigen cluster. Buiten de OpenAPI, zoals TrueNAS.

func (s *Server) selfInfo(w http.ResponseWriter, r *http.Request) {
	if s.selfHost == nil {
		writeError(w, http.StatusNotFound, "not_found", "niet beschikbaar")
		return
	}
	in, err := s.selfHost.Info(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) selfCheckImage(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in struct {
		Image string `json:"image"`
	}
	if !decode(w, r, &in) {
		return
	}
	img, err := s.selfHost.NormalizeImage(in.Image)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ok, err := s.selfHost.CheckImage(ctx, img)
	out := map[string]any{"image": img, "exists": ok}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// confirmSelf vraagt de code van de tweestapsverificatie (of het
// wachtwoord zonder): bijwerken en verhuizen raken alles.
func (s *Server) confirmSelf(w http.ResponseWriter, r *http.Request, password, code, what string) bool {
	p, _ := principalFrom(r.Context())
	if code == "" && password == "" {
		writeError(w, http.StatusConflict, "needs_confirmation", "bevestig met de code van je tweestapsverificatie (of je wachtwoord)")
		return false
	}
	return !s.authError(w, r, s.auth.Confirm(r.Context(), p, password, code, what))
}

func (s *Server) selfError(w http.ResponseWriter, r *http.Request, err error) {
	var ce *selfhost.ConflictError
	if errors.As(err, &ce) {
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
		return
	}
	s.internalError(w, r, err)
}

func (s *Server) selfUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Image    string     `json:"image"`
		Host     *uuid.UUID `json:"host_node_id"`
		TotpCode string     `json:"totp_code"`
		Password string     `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	img, err := s.selfHost.NormalizeImage(in.Image)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	if !s.confirmSelf(w, r, in.Password, in.TotpCode, "clusterforge_update") {
		return
	}
	j, err := s.selfHost.StartUpdate(r.Context(), events.User(p.User.ID), img, in.Host)
	if err != nil {
		s.selfError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) selfMove(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Cluster  uuid.UUID `json:"cluster_id"`
		Host     uuid.UUID `json:"host_node_id"`
		Force    bool      `json:"force"`
		Metrics  *bool     `json:"metrics"`
		TotpCode string    `json:"totp_code"`
		Password string    `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Cluster == uuid.Nil || in.Host == uuid.Nil {
		writeError(w, http.StatusBadRequest, "validation", "kies het cluster en de node waarop ClusterForge nu draait")
		return
	}
	if !s.confirmSelf(w, r, in.Password, in.TotpCode, "clusterforge_move") {
		return
	}
	e, err := s.agentConf.Endpoints(r.Context(), r.Host)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	metrics := in.Metrics == nil || *in.Metrics
	j, err := s.selfHost.StartMove(r.Context(), events.User(p.User.ID), in.Cluster, in.Host, e.ServerURL, in.Force, metrics)
	if err != nil {
		s.selfError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) selfExport(w http.ResponseWriter, r *http.Request) {
	if s.selfHost == nil {
		http.NotFound(w, r)
		return
	}
	s.selfHost.ServeExport(w, r, chi.URLParam(r, "token"), chi.URLParam(r, "what"))
}
