package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/opnsense"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// OPNsense-firewalls met een agent (fase 34). De antwoorden zijn de structs
// van internal/opnsense; de webinterface typt ze in web/src/lib/opnsense.ts.

func (s *Server) opnError(w http.ResponseWriter, err error) {
	var ve opnsense.ValidationError
	var busy jobs.BusyError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid", ve.Msg)
	case errors.Is(err, opnsense.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.As(err, &busy):
		writeError(w, http.StatusConflict, "busy", "op dit cluster loopt al een taak: "+busy.Title)
	default:
		s.log.Warn("OPNsense", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) ListOpnsenseClusters(w http.ResponseWriter, r *http.Request) {
	list, err := s.opnsense.Clusters(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if list == nil {
		list = []opnsense.Cluster{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "protocol": protocol.OPNsenseSince})
}

func (s *Server) GetOpnsenseApi(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID, params gen.GetOpnsenseApiParams) {
	p, _ := principalFrom(r.Context())
	admin := p.User.Role == store.UserRoleAdmin
	var (
		data json.RawMessage
		err  error
	)
	if opnsense.IsSearch(params.Path) {
		var fields map[string]string
		if params.Params != nil && *params.Params != "" {
			if err := json.Unmarshal([]byte(*params.Params), &fields); err != nil {
				writeError(w, http.StatusBadRequest, "invalid", "params is geen JSON-object met tekst: "+err.Error())
				return
			}
		}
		data, err = s.opnsense.Search(r.Context(), nodeId, params.Path, fields, admin)
	} else {
		data, err = s.opnsense.Get(r.Context(), nodeId, params.Path, admin)
	}
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) GetOpnsenseConfigctl(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID, params gen.GetOpnsenseConfigctlParams) {
	data, err := s.opnsense.Configctl(r.Context(), nodeId, params.Action)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) ListOpnsenseRevisions(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	data, err := s.opnsense.Revisions(r.Context(), nodeId)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": data})
}

// GetOpnsenseDiff vergelijkt revisies; alleen voor beheerders, want de
// config bevat ook zonder geheimen de hele opbouw van het netwerk.
func (s *Server) GetOpnsenseDiff(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID, params gen.GetOpnsenseDiffParams) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	d, err := s.opnsense.RevisionDiff(r.Context(), nodeId, params.From, params.To, params.Other)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type opnChangeInput struct {
	opnsense.Change
	Password string `json:"password"`
	TotpCode string `json:"totp_code"`
}

func (s *Server) PlanOpnsenseChange(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in opnsense.Change
	if !decode(w, r, &in) {
		return
	}
	v, err := s.opnsense.PlanChange(r.Context(), nodeId, in)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) StartOpnsenseChange(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in opnChangeInput
	if !decode(w, r, &in) {
		return
	}
	prod, err := s.opnsense.IsProd(r.Context(), nodeId)
	if err != nil {
		s.opnError(w, err)
		return
	}
	if prod {
		if in.Password == "" && in.TotpCode == "" {
			writeError(w, http.StatusConflict, "needs_confirmation", "deze firewall zit in een prodcluster; geef de code van je tweestapsverificatie (of je wachtwoord)")
			return
		}
		if s.authError(w, r, s.auth.Confirm(r.Context(), p, in.Password, in.TotpCode, "opnsense_change")) {
			return
		}
	}
	j, err := s.opnsense.StartChange(r.Context(), events.User(p.User.ID), nodeId, in.Change)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) SyncOpnsense(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	j, err := s.opnsense.StartSync(r.Context(), events.User(p.User.ID), nodeId)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) SetOpnsenseKey(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in protocol.OPNKeyInput
	if !decode(w, r, &in) {
		return
	}
	j, err := s.opnsense.StartKey(r.Context(), events.User(p.User.ID), nodeId, in)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// opnConfirm vraagt de code of het wachtwoord als de firewall in een
// prodcluster zit (of altijd, met always). false: het antwoord is al
// geschreven.
func (s *Server) opnConfirm(w http.ResponseWriter, r *http.Request, p auth.Principal, nodeId uuid.UUID, password, code, purpose string, always bool) bool {
	if !always {
		prod, err := s.opnsense.IsProd(r.Context(), nodeId)
		if err != nil {
			s.opnError(w, err)
			return false
		}
		if !prod {
			return true
		}
	}
	if password == "" && code == "" {
		msg := "deze firewall zit in een prodcluster; geef de code van je tweestapsverificatie (of je wachtwoord)"
		if always {
			msg = "een back-up bevat alle wachtwoorden en sleutels; geef de code van je tweestapsverificatie (of je wachtwoord)"
		}
		writeError(w, http.StatusConflict, "needs_confirmation", msg)
		return false
	}
	return !s.authError(w, r, s.auth.Confirm(r.Context(), p, password, code, purpose))
}

type opnConfirmInput struct {
	Password string `json:"password"`
	TotpCode string `json:"totp_code"`
}

func (s *Server) StartOpnsenseCarp(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		opnsense.CarpInput
		opnConfirmInput
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.opnConfirm(w, r, p, nodeId, in.Password, in.TotpCode, "opnsense_carp", false) {
		return
	}
	j, err := s.opnsense.StartCarp(r.Context(), events.User(p.User.ID), nodeId, in.CarpInput)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) StartOpnsenseFirmware(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		opnsense.FirmwareInput
		opnConfirmInput
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Mode != "check" && !s.opnConfirm(w, r, p, nodeId, in.Password, in.TotpCode, "opnsense_firmware", false) {
		return
	}
	j, err := s.opnsense.StartFirmware(r.Context(), events.User(p.User.ID), nodeId, in.FirmwareInput)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) GetOpnsenseDrift(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	d, err := s.opnsense.Drift(r.Context(), nodeId)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) ListOpnsenseBackups(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	list, err := s.opnsense.Backups(r.Context(), nodeId)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "keep": opnsense.KeepBackups})
}

func (s *Server) StartOpnsenseBackup(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	j, err := s.opnsense.StartBackup(r.Context(), events.User(p.User.ID), nodeId)
	if err != nil {
		s.opnError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) DownloadOpnsenseBackup(w http.ResponseWriter, r *http.Request, backupId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in opnConfirmInput
	if !decode(w, r, &in) {
		return
	}
	if !s.opnConfirm(w, r, p, uuid.Nil, in.Password, in.TotpCode, "opnsense_backup_download", true) {
		return
	}
	name, data, err := s.opnsense.OpenBackup(r.Context(), events.User(p.User.ID), backupId)
	if err != nil {
		s.opnError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
