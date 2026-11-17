package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/pbs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Proxmox Backup Servers met een agent (fase 32). De antwoorden zijn de
// structs van internal/pbs; de webinterface typt ze in web/src/lib/pbs.ts.

func (s *Server) pbsError(w http.ResponseWriter, err error) {
	var ve pbs.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid", ve.Msg)
	case errors.Is(err, pbs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.log.Warn("Proxmox Backup Server", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) ListPbsServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.pbs.Servers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "protocol": protocol.PBSSince})
}

func (s *Server) GetPbsOverview(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	o, err := s.pbs.Overview(r.Context(), nodeId)
	if err != nil {
		s.pbsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) GetPbsApi(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID, params gen.GetPbsApiParams) {
	p, _ := principalFrom(r.Context())
	var fields map[string]string
	if params.Params != nil && *params.Params != "" {
		if err := json.Unmarshal([]byte(*params.Params), &fields); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "params is geen JSON-object met tekst: "+err.Error())
			return
		}
	}
	data, err := s.pbs.Get(r.Context(), nodeId, params.Path, fields, p.User.Role == store.UserRoleAdmin)
	if err != nil {
		s.pbsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) CallPbsApi(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.PveCallInput
	if !decode(w, r, &in) {
		return
	}
	c := pbs.Call{Method: string(in.Method), Path: in.Path, Confirm: deref(in.Confirm)}
	if in.Params != nil {
		c.Params = *in.Params
	}
	j, err := s.pbs.StartCall(r.Context(), events.User(p.User.ID), nodeId, c)
	if err != nil {
		s.pbsError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}
