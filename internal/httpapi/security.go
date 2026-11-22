package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func (s *Server) GetSecurityReport(w http.ResponseWriter, r *http.Request, params gen.GetSecurityReportParams) {
	if !s.securityReady(w) {
		return
	}
	rep, err := s.security.Report(r.Context(), params.ScopeId)
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// Veilige oplossingen (fase 27 M6).

func (s *Server) fixError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, security.ErrFixUnsupported):
		writeError(w, http.StatusConflict, "unsupported", err.Error())
	case errors.Is(err, security.ErrFixNotWaiting):
		writeError(w, http.StatusConflict, "not_waiting", "Deze wijziging wacht niet meer op bevestiging.")
	default:
		s.securityError(w, r, err)
	}
}

func (s *Server) ListSecurityFixes(w http.ResponseWriter, r *http.Request) {
	if !s.securityReady(w) {
		return
	}
	list, err := s.security.FixRuns(r.Context())
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) ApplySecurityFixes(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in gen.SecurityFixInput
	if !decode(w, r, &in) {
		return
	}
	var rollback time.Duration
	if in.RollbackMinutes != nil {
		rollback = time.Duration(*in.RollbackMinutes) * time.Minute
	}
	uid := p.User.ID
	run, err := s.security.ApplyFixes(r.Context(), events.User(p.User.ID), &uid, in.NodeId, in.Rules, rollback)
	if err != nil {
		s.fixError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) ConfirmSecurityFix(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.finishFix(w, r, id, true)
}

func (s *Server) RollbackSecurityFix(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.finishFix(w, r, id, false)
}

func (s *Server) finishFix(w http.ResponseWriter, r *http.Request, id uuid.UUID, keep bool) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	run, err := s.security.FinishFix(r.Context(), events.User(p.User.ID), id, keep)
	if err != nil {
		s.fixError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// Beveiligingstools (fase 27 M7).

func (s *Server) GetSecurityTools(w http.ResponseWriter, r *http.Request) {
	if !s.securityReady(w) {
		return
	}
	o, err := s.security.Tools(r.Context())
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) SecurityToolAction(w http.ResponseWriter, r *http.Request, nodeID uuid.UUID, tool string, action string) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	err := s.security.ToolAction(r.Context(), events.User(p.User.ID), nodeID, tool, action)
	switch {
	case errors.Is(err, security.ErrToolBusy):
		writeError(w, http.StatusConflict, "busy", "Deze tool is al bezig op die node.")
		return
	case err != nil:
		s.securityError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) SetSecurityToolSchedule(w http.ResponseWriter, r *http.Request, nodeID uuid.UUID, tool string) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in gen.SecurityToolSchedule
	if !decode(w, r, &in) {
		return
	}
	weekday, hour := 0, 3
	if in.Weekday != nil {
		weekday = *in.Weekday
	}
	if in.Hour != nil {
		hour = *in.Hour
	}
	if err := s.security.SetToolSchedule(r.Context(), events.User(p.User.ID), nodeID, tool, string(in.Schedule), weekday, hour); err != nil {
		s.securityError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ImportSecurityScan leest een nmap-rapport van buiten in (fase 43). De
// bestanden zijn groter dan een gewone vraag; daarom een eigen grens.
func (s *Server) ImportSecurityScan(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in gen.SecurityImportInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, security.MaxImportBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "ongeldige vraag of te groot (hoogstens 64 MB): "+err.Error())
		return
	}
	files := make([]security.ImportFile, 0, len(in.Files))
	for _, f := range in.Files {
		files = append(files, security.ImportFile{Name: f.Name, XML: f.Xml})
	}
	name := ""
	if in.Name != nil {
		name = *in.Name
	}
	run, err := s.security.ImportNmap(r.Context(), events.User(p.User.ID), name, files)
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

// --- importsleutels (fase 43b) ---

func (s *Server) ListSecurityImportTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok || !s.securityReady(w) {
		return
	}
	list, err := s.security.ImportTokens(r.Context())
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "path": importBase + "/nmap"})
}

func (s *Server) CreateSecurityImportToken(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, secret, err := s.security.CreateImportToken(r.Context(), events.User(p.User.ID), &p.User.ID, in.Name)
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret, "path": importBase + "/nmap"})
}

func (s *Server) RevokeSecurityImportToken(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok || !s.securityReady(w) {
		return
	}
	if err := s.security.RevokeImportToken(r.Context(), events.User(p.User.ID), id); err != nil {
		s.securityError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// importBase is de API voor pentestsystemen: alleen rapporten inlezen, met
// een importsleutel in plaats van een sessie.
const importBase = "/api/import/v1"

// importNmap neemt een of meer XML-bestanden van nmap aan, als multipart
// (curl -F file=@02_tcp.xml -F file=@03_udp.xml) of als ruwe XML in de body.
func (s *Server) importNmap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.securityReady(w) {
		return
	}
	ip := clientIP(r)
	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if secret == "" {
		secret = r.Header.Get("X-API-Key")
	}
	t, ok, err := s.security.CheckImportToken(r.Context(), strings.TrimSpace(secret), ip)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		if !s.loginLimiter.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "te veel foute sleutels; probeer het straks opnieuw")
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthenticated", "onbekende of ingetrokken importsleutel (Authorization: Bearer cfi_…)")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, security.MaxImportBytes)
	var files []security.ImportFile
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "upload niet te lezen of te groot (hoogstens 64 MB): "+err.Error())
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		for _, list := range r.MultipartForm.File {
			for _, fh := range list {
				f, err := fh.Open()
				if err != nil {
					writeError(w, http.StatusBadRequest, "bad_request", err.Error())
					return
				}
				b, err := io.ReadAll(f)
				_ = f.Close()
				if err != nil {
					writeError(w, http.StatusBadRequest, "bad_request", err.Error())
					return
				}
				files = append(files, security.ImportFile{Name: filepath.Base(fh.Filename), XML: string(b)})
			}
		}
	} else {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "body niet te lezen of te groot (hoogstens 64 MB)")
			return
		}
		name := r.URL.Query().Get("file")
		if name == "" {
			name = "rapport.xml"
		}
		files = append(files, security.ImportFile{Name: filepath.Base(name), XML: string(b)})
	}
	if len(files) > 20 {
		writeError(w, http.StatusBadRequest, "bad_request", "hoogstens 20 bestanden per keer")
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		name = t.Name + " " + time.Now().In(time.Local).Format("02/01 15:04")
	}
	run, err := s.security.ImportNmap(r.Context(), events.Actor{Type: events.System().Type, ID: "importsleutel:" + t.Name}, name, files)
	if err != nil {
		s.securityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"run_id": run.ID, "hosts_up": run.HostsUp, "open_ports": run.OpenPorts, "findings": run.Findings,
		"url": "/kwetsbaarheden/scans?tab=uitvoeringen&run=" + run.ID.String()})
}
