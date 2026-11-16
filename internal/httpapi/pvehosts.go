package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/pvehost"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Proxmox-hosts met een agent (fase 17). De antwoorden zijn de structs van
// internal/pvehost en pkg/protocol; de webinterface typt ze in
// web/src/lib/pvehosts.ts.

func (s *Server) pveHostError(w http.ResponseWriter, r *http.Request, err error) {
	var ve pvehost.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid", ve.Msg)
	case errors.Is(err, pvehost.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		// Een fout van de agent of de host (pvesh, offline) is geen fout
		// van de server.
		s.log.Warn("Proxmox-host", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) ListPveHosts(w http.ResponseWriter, r *http.Request) {
	hosts, missing, err := s.pvehosts.Hosts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts, "missing": missing, "protocol": protocol.PVESince})
}

func (s *Server) GetPveHostInventory(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	inv, err := s.pvehosts.Inventory(r.Context(), nodeId)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) GetPveHostAvailable(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	list, err := s.pvehosts.Available(r.Context(), nodeId)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	if list == nil {
		list = []protocol.PVEAppliance{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) StartPveHostAction(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.PveActionInput
	if !decode(w, r, &in) {
		return
	}
	var op protocol.PVERun
	raw, _ := json.Marshal(in.Op)
	if err := json.Unmarshal(raw, &op); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "ongeldige actie: "+err.Error())
		return
	}
	j, err := s.pvehosts.Start(r.Context(), events.User(p.User.ID), pvehost.Request{
		NodeID: nodeId, Op: op, Confirm: deref(in.Confirm), ServerURL: deref(in.ServerUrl),
	})
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// --- Fase 26: de Proxmox-API via de agents ---

func (s *Server) GetPveHostHardware(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	hw, err := s.pvehosts.Hardware(r.Context(), nodeId)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hw)
}

func (s *Server) ListPveDatacenters(w http.ResponseWriter, r *http.Request) {
	list, err := s.pvehosts.Datacenters(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "protocol": protocol.PVEAPISince})
}

func (s *Server) GetPveOverview(w http.ResponseWriter, r *http.Request, dcId string) {
	o, err := s.pvehosts.Overview(r.Context(), dcId)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) GetPveApi(w http.ResponseWriter, r *http.Request, dcId string, params gen.GetPveApiParams) {
	p, _ := principalFrom(r.Context())
	var fields map[string]string
	if params.Params != nil && *params.Params != "" {
		if err := json.Unmarshal([]byte(*params.Params), &fields); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "params is geen JSON-object met tekst: "+err.Error())
			return
		}
	}
	data, err := s.pvehosts.Get(r.Context(), dcId, params.Path, fields, p.User.Role == store.UserRoleAdmin)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) CallPveApi(w http.ResponseWriter, r *http.Request, dcId string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.PveCallInput
	if !decode(w, r, &in) {
		return
	}
	c := pvehost.Call{Datacenter: dcId, Method: string(in.Method), Path: in.Path, Confirm: deref(in.Confirm)}
	if in.Params != nil {
		c.Params = *in.Params
	}
	j, err := s.pvehosts.StartCall(r.Context(), events.User(p.User.ID), c)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// StartPveRestoreTest zet een back-up op proef terug (fase 26 M3).
func (s *Server) StartPveRestoreTest(w http.ResponseWriter, r *http.Request, dcId string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.PveRestoreTestInput
	if !decode(w, r, &in) {
		return
	}
	j, err := s.pvehosts.StartRestoreTest(r.Context(), events.User(p.User.ID), pvehost.RestoreTest{
		Datacenter: dcId, Node: in.Node, Archive: in.Archive, Type: string(in.Type), Storage: in.Storage,
	})
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}
