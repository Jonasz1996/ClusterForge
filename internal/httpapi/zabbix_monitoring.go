package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/zabbixagent"
	"github.com/Jonasz1996/clusterforge/internal/zabbixmon"
)

// Zabbix-monitoring (fase 33): de stand uit de Zabbix-API. De antwoorden zijn
// de structs van internal/zabbixmon; hun JSON volgt de schema's Zabbix* in
// openapi.yaml.

func (s *Server) zabbixMonError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *zabbixmon.FieldError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.Is(err, zabbixmon.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "host of node niet gevonden")
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) GetZabbixOverview(w http.ResponseWriter, r *http.Request, params gen.GetZabbixOverviewParams) {
	window := "24h"
	if params.Window != nil && params.Window.Valid() {
		window = string(*params.Window)
	}
	o, err := s.zabbixMon.Overview(r.Context(), window)
	if err != nil {
		s.zabbixMonError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) RefreshZabbix(w http.ResponseWriter, r *http.Request) {
	// Een mislukte ronde staat als fout in het overzicht.
	_ = s.zabbixMon.Refresh(r.Context())
	o, err := s.zabbixMon.Overview(r.Context(), "24h")
	if err != nil {
		s.zabbixMonError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) SetZabbixHostLink(w http.ResponseWriter, r *http.Request, hostid string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ZabbixHostLinkInput
	if !decode(w, r, &in) {
		return
	}
	if !in.NodeId.IsSpecified() {
		writeError(w, http.StatusBadRequest, "invalid", "node_id ontbreekt (null voor geen node)")
		return
	}
	var node *uuid.UUID
	if !in.NodeId.IsNull() {
		id, err := in.NodeId.Get()
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "ongeldige node_id")
			return
		}
		node = &id
	}
	uid := p.User.ID
	if err := s.zabbixMon.SetLink(r.Context(), events.User(uid), &uid, hostid, node); err != nil {
		s.zabbixMonError(w, r, err)
		return
	}
	s.kickZabbixMon()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DeleteZabbixHostLink(w http.ResponseWriter, r *http.Request, hostid string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.zabbixMon.DeleteLink(r.Context(), events.User(p.User.ID), hostid); err != nil {
		s.zabbixMonError(w, r, err)
		return
	}
	s.kickZabbixMon()
	w.WriteHeader(http.StatusNoContent)
}

// kickZabbixMon draait meteen een ronde, zodat een koppeling direct zichtbaar is.
func (s *Server) kickZabbixMon() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.zabbixMon.Tick(ctx); err != nil {
			s.log.Debug("Zabbix-monitoringronde mislukt", "err", err)
		}
	}()
}

// zabbixReadError: fouten van Zabbix zelf zijn 502, de rest zoals gewoonlijk.
func (s *Server) zabbixReadError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *zabbixagent.APIError
	switch {
	case errors.Is(err, zabbixmon.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "onbekende host of item (staat het nog in Zabbix?)")
	case errors.Is(err, zabbixmon.ErrNotNumeric):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, zabbixagent.ErrNotConfigured):
		writeError(w, http.StatusBadRequest, "not_configured", err.Error())
	case errors.As(err, &ae), strings.Contains(err.Error(), "Zabbix API"):
		writeError(w, http.StatusBadGateway, "zabbix", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) GetZabbixHostItems(w http.ResponseWriter, r *http.Request, hostid string) {
	out, err := s.zabbixMon.Items(r.Context(), hostid)
	if err != nil {
		s.zabbixReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetZabbixItemSeries(w http.ResponseWriter, r *http.Request, itemid string, params gen.GetZabbixItemSeriesParams) {
	rng := "24h"
	if params.Range != nil && params.Range.Valid() {
		rng = string(*params.Range)
	}
	out, err := s.zabbixMon.ItemSeries(r.Context(), itemid, rng)
	if err != nil {
		s.zabbixReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetZabbixMetrics(w http.ResponseWriter, r *http.Request) {
	out, err := s.zabbixMon.Metrics(r.Context())
	if err != nil {
		s.zabbixReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetZabbixEvents(w http.ResponseWriter, r *http.Request, params gen.GetZabbixEventsParams) {
	window := "24h"
	if params.Window != nil && params.Window.Valid() {
		window = string(*params.Window)
	}
	out, err := s.zabbixMon.Events(r.Context(), window)
	if err != nil {
		s.zabbixReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetZabbixLatest(w http.ResponseWriter, r *http.Request, params gen.GetZabbixLatestParams) {
	out, err := s.zabbixMon.Latest(r.Context(), strings.Split(params.Itemids, ","))
	if err != nil {
		var fe *zabbixmon.FieldError
		if errors.As(err, &fe) {
			writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
			return
		}
		s.zabbixReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- eigen dashboards (M4) ---

func (s *Server) zabbixBoardError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, zabbixmon.ErrBoardExists):
		writeError(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, zabbixmon.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "dashboard niet gevonden")
	default:
		s.zabbixMonError(w, r, err)
	}
}

func (s *Server) ListZabbixDashboards(w http.ResponseWriter, r *http.Request) {
	out, err := s.zabbixMon.Boards(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetZabbixDashboard(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	b, err := s.zabbixMon.Board(r.Context(), id)
	if err != nil {
		s.zabbixBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) CreateZabbixDashboard(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in zabbixmon.BoardInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	b, err := s.zabbixMon.CreateBoard(r.Context(), events.User(uid), &uid, in)
	if err != nil {
		s.zabbixBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) UpdateZabbixDashboard(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in zabbixmon.BoardInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	b, err := s.zabbixMon.UpdateBoard(r.Context(), events.User(uid), &uid, id, in)
	if err != nil {
		s.zabbixBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) DeleteZabbixDashboard(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.zabbixMon.DeleteBoard(r.Context(), events.User(p.User.ID), id); err != nil {
		s.zabbixBoardError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- schrijven naar Zabbix (M6) ---

func (s *Server) AcknowledgeZabbixProblem(w http.ResponseWriter, r *http.Request, eventid string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ZabbixAckInput
	if !decode(w, r, &in) {
		return
	}
	prob, prod, err := s.zabbixMon.AckPlan(r.Context(), eventid)
	if err != nil {
		if errors.Is(err, zabbixmon.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "dit probleem staat niet (meer) open in Zabbix")
			return
		}
		s.internalError(w, r, err)
		return
	}
	if prod {
		if deref(in.TotpCode) == "" && deref(in.Password) == "" {
			what := "het wachtwoord"
			if p.User.TotpEnabledAt != nil {
				what = "de code van je tweestapsverificatie"
			}
			writeError(w, http.StatusConflict, "needs_confirmation", "dit probleem raakt een prod-node: geef "+what)
			return
		}
		if s.authError(w, r, s.auth.Confirm(r.Context(), p, deref(in.Password), deref(in.TotpCode), "zabbix_ack")) {
			return
		}
	}
	ack := zabbixmon.AckInput{Message: in.Message, Close: in.Close}
	if in.Severity.IsSpecified() && !in.Severity.IsNull() {
		v, _ := in.Severity.Get()
		ack.Severity = &v
	}
	err = s.zabbixMon.Acknowledge(r.Context(), events.User(p.User.ID), p.User.Username, prob, ack)
	if err != nil {
		var fe *zabbixmon.FieldError
		if errors.As(err, &fe) {
			writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
			return
		}
		s.zabbixReadError(w, r, err)
		return
	}
	s.kickZabbixMon()
	w.WriteHeader(http.StatusNoContent)
}
