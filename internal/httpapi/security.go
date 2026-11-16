package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agents"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
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

func scanInput(in gen.SecurityScanInput) security.ScanInput {
	out := security.ScanInput{Name: in.Name, ScopeID: in.ScopeId, Profile: string(in.Profile), Schedule: string(in.Schedule),
		MaxRate: deref(in.MaxRate), Weekday: deref(in.Weekday), Hour: 2, DefaultPasswords: deref(in.DefaultPasswords)}
	if in.Hour != nil {
		out.Hour = *in.Hour
	}
	if v, err := in.ScannerId.Get(); err == nil {
		id := v
		out.ScannerID = &id
	}
	return out
}

func (s *Server) GetSecurityScans(w http.ResponseWriter, r *http.Request) {
	if !s.securityReady(w) {
		return
	}
	o, err := s.security.Scans(r.Context())
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) CreateSecurityScan(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in gen.SecurityScanInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	sc, err := s.security.SaveScan(r.Context(), events.User(p.User.ID), &uid, nil, scanInput(in))
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *Server) UpdateSecurityScan(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in gen.SecurityScanInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	sc, err := s.security.SaveScan(r.Context(), events.User(p.User.ID), &uid, &id, scanInput(in))
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) DeleteSecurityScan(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	if err := s.security.DeleteScan(r.Context(), events.User(p.User.ID), id); err != nil {
		s.securityError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RunSecurityScan(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	runID, err := s.security.StartScan(r.Context(), events.User(p.User.ID), id, "hand")
	switch {
	case errors.Is(err, security.ErrScanRunning):
		writeError(w, http.StatusConflict, "running", "Deze scan loopt al.")
		return
	case errors.Is(err, security.ErrNoScanner):
		writeError(w, http.StatusConflict, "no_scanner", err.Error())
		return
	case err != nil:
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}

func (s *Server) GetSecurityScanRun(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if !s.securityReady(w) {
		return
	}
	d, err := s.security.ScanRunDetail(r.Context(), id)
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// LinkSecurityScanner maakt de node cf-scanner met een token van één
// gebruik, en zet het aanmeldbestand in de map die de server met de
// scannercontainer deelt. De container meldt zich daarmee vanzelf aan.
func (s *Server) LinkSecurityScanner(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	ctx := r.Context()
	dir := s.cfg.ScannerDir
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		writeError(w, http.StatusConflict, "no_dir", "De map "+dir+" ontbreekt in de server. Werk deploy/docker-compose.yml bij (volume scanner) en start de server opnieuw.")
		return
	}
	var nodeID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM nodes WHERE hostname = $1`, security.ScannerHostname).Scan(&nodeID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		n, err := s.inv.CreateNode(ctx, events.User(p.User.ID), inventory.NodeFields{Hostname: security.ScannerHostname, Role: "scanner",
			Description: "Scannercontainer van ClusterForge (nmap), naast de server."})
		if s.inventoryError(w, r, err) {
			return
		}
		nodeID = n.ID
	case err != nil:
		s.internalError(w, r, err)
		return
	default:
		if _, err := s.q.GetActiveAgentByNode(ctx, nodeID); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"node_id": nodeID, "hostname": security.ScannerHostname, "linked": true})
			return
		}
	}
	token, _, err := s.agents.CreateToken(ctx, p.User.ID, agents.TokenInput{Description: "scannercontainer", NodeID: &nodeID, MaxUses: 1, TTL: 24 * time.Hour})
	if s.agentsError(w, r, err) {
		return
	}
	body, err := json.Marshal(agent.EnrollFile{Server: s.cfg.ScannerServerURL, Token: token, AllowHTTP: strings.HasPrefix(s.cfg.ScannerServerURL, "http://")})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	path := filepath.Join(dir, "enroll.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		writeError(w, http.StatusConflict, "no_dir", "Het aanmeldbestand schrijven in "+dir+" lukte niet: "+err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		writeError(w, http.StatusConflict, "no_dir", "Het aanmeldbestand schrijven lukte niet: "+err.Error())
		return
	}
	s.security.LinkedScanner(ctx, events.User(p.User.ID), nodeID)
	writeJSON(w, http.StatusOK, map[string]any{"node_id": nodeID, "hostname": security.ScannerHostname, "linked": false})
}
