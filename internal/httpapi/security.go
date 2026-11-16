package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/security"
)

// Beveiliging (fase 27). De antwoorden zijn de structs van
// internal/security; de webinterface typt ze in web/src/lib/security.ts.

func (s *Server) securityError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *security.FieldError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.Is(err, security.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Niet gevonden.")
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) securityReady(w http.ResponseWriter) bool {
	if s.security == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "De beveiligingsmodule is niet beschikbaar.")
		return false
	}
	return true
}

func (s *Server) GetSecurity(w http.ResponseWriter, r *http.Request) {
	if !s.securityReady(w) {
		return
	}
	o, err := s.security.Overview(r.Context())
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) RefreshSecurity(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !s.securityReady(w) {
		return
	}
	var in gen.SecurityRefreshInput
	if !decode(w, r, &in) {
		return
	}
	feeds, nodes := in.Feeds != nil && *in.Feeds, in.Nodes != nil && *in.Nodes
	if !feeds && !nodes {
		feeds, nodes = true, true
	}
	s.security.Refresh(feeds, nodes, in.NodeId)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := s.security.Tick(ctx); err != nil {
			s.log.Warn("beveiligingsronde mislukt", "err", err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) ListSecurityFindings(w http.ResponseWriter, r *http.Request, params gen.ListSecurityFindingsParams) {
	if !s.securityReady(w) {
		return
	}
	f := security.FindingFilter{NodeID: params.NodeId}
	if params.Status != nil {
		f.Status = string(*params.Status)
	}
	if params.Kind != nil {
		f.Kind = *params.Kind
	}
	items, err := s.security.Findings(r.Context(), f)
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) AcceptSecurityFinding(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in gen.SecurityAcceptInput
	if !decode(w, r, &in) {
		return
	}
	var until *time.Time
	if in.Days != nil && *in.Days > 0 {
		t := time.Now().Add(time.Duration(*in.Days) * 24 * time.Hour)
		until = &t
	}
	uid := p.User.ID
	f, err := s.security.Accept(r.Context(), events.User(p.User.ID), &uid, id, in.Note, until)
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) ReopenSecurityFinding(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	f, err := s.security.Reopen(r.Context(), events.User(p.User.ID), id)
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) ListSecurityScopes(w http.ResponseWriter, r *http.Request) {
	if !s.securityReady(w) {
		return
	}
	items, err := s.security.Scopes(r.Context())
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "confirm_text": security.PublicConfirmText})
}

func scopeInput(in gen.SecurityScopeInput) security.ScopeInput {
	out := security.ScopeInput{Name: in.Name, CIDRs: in.Cidrs}
	if in.Description != nil {
		out.Description = *in.Description
	}
	if in.Exclude != nil {
		out.Exclude = *in.Exclude
	}
	if in.PublicConfirm != nil {
		out.PublicConfirm = *in.PublicConfirm
	}
	if in.PublicNote != nil {
		out.PublicNote = *in.PublicNote
	}
	return out
}

func (s *Server) CreateSecurityScope(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in gen.SecurityScopeInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	sc, err := s.security.SaveScope(r.Context(), events.User(p.User.ID), &uid, nil, scopeInput(in))
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *Server) UpdateSecurityScope(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in gen.SecurityScopeInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	sc, err := s.security.SaveScope(r.Context(), events.User(p.User.ID), &uid, &id, scopeInput(in))
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) DeleteSecurityScope(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	if err := s.security.DeleteScope(r.Context(), events.User(p.User.ID), id); err != nil {
		s.securityError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetSecurityHardening(w http.ResponseWriter, r *http.Request) {
	if !s.securityReady(w) {
		return
	}
	o, err := s.security.Hardening(r.Context())
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}
