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
