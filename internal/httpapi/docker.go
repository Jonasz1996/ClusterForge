package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/dockerhost"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Docker zoals Portainer (fase 28).

func (s *Server) dockerError(w http.ResponseWriter, r *http.Request, err error) {
	var ve dockerhost.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid", ve.Msg)
	case errors.Is(err, dockerhost.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		// Een fout van de agent of van Docker is geen fout van de server.
		s.log.Warn("Docker", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) ListDockerHosts(w http.ResponseWriter, r *http.Request, params gen.ListDockerHostsParams) {
	if params.Refresh != nil && *params.Refresh {
		s.docker.Refresh(r.Context())
	}
	hosts, err := s.docker.Hosts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts, "protocol": protocol.DockerSince})
}

func (s *Server) GetDockerApi(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID, params gen.GetDockerApiParams) {
	p, _ := principalFrom(r.Context())
	var query map[string]string
	if params.Query != nil && *params.Query != "" {
		if err := json.Unmarshal([]byte(*params.Query), &query); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "query is geen JSON-object met tekst: "+err.Error())
			return
		}
	}
	data, err := s.docker.Get(r.Context(), nodeId, params.Path, query, p.User.Role == store.UserRoleAdmin)
	if err != nil {
		s.dockerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) GetDockerStats(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID, params gen.GetDockerStatsParams) {
	var only []string
	if params.Containers != nil && *params.Containers != "" {
		only = strings.Split(*params.Containers, ",")
	}
	stats, err := s.docker.Stats(r.Context(), nodeId, only)
	if err != nil {
		s.dockerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
}

func (s *Server) CallDockerApi(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.DockerCallInput
	if !decode(w, r, &in) {
		return
	}
	c := dockerhost.Call{Title: deref(in.Title), Confirm: deref(in.Confirm)}
	for _, raw := range in.Calls {
		data, _ := json.Marshal(raw)
		var call protocol.DockerCall
		if err := json.Unmarshal(data, &call); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "ongeldige aanroep: "+err.Error())
			return
		}
		c.Calls = append(c.Calls, call)
	}
	j, err := s.docker.StartCall(r.Context(), events.User(p.User.ID), nodeId, c)
	if err != nil {
		s.dockerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) GetDockerAll(w http.ResponseWriter, r *http.Request, params gen.GetDockerAllParams) {
	var query map[string]string
	if params.Query != nil && *params.Query != "" {
		if err := json.Unmarshal([]byte(*params.Query), &query); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "query is geen JSON-object met tekst: "+err.Error())
			return
		}
	}
	var nodes []uuid.UUID
	if params.Nodes != nil && *params.Nodes != "" {
		for _, n := range strings.Split(*params.Nodes, ",") {
			id, err := uuid.Parse(strings.TrimSpace(n))
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid", "ongeldig node-id "+n)
				return
			}
			nodes = append(nodes, id)
		}
	}
	res, err := s.docker.All(r.Context(), params.Path, query, nodes)
	if err != nil {
		s.dockerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

// OpenDockerConsole opent een shell in een container (M5). De sessie en de
// websocket zijn die van de Proxmox-console (/pve/console/{session}).
func (s *Server) OpenDockerConsole(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.DockerConsoleInput
	if !decode(w, r, &in) {
		return
	}
	ci := dockerhost.ConsoleInput{Container: in.Container, User: deref(in.User)}
	if in.Shell != nil {
		ci.Shell = string(*in.Shell)
	}
	if in.Cols != nil {
		ci.Cols = *in.Cols
	}
	if in.Rows != nil {
		ci.Rows = *in.Rows
	}
	plan, err := s.docker.PrepareConsole(r.Context(), nodeId, ci)
	if err != nil {
		s.dockerError(w, r, err)
		return
	}
	if plan.Prod() {
		if deref(in.TotpCode) == "" && deref(in.Password) == "" {
			what := "het wachtwoord"
			if p.User.TotpEnabledAt != nil {
				what = "de code van je tweestapsverificatie"
			}
			writeError(w, http.StatusConflict, "needs_confirmation", plan.Host.Hostname+" is productie: geef "+what+" om een shell te openen")
			return
		}
		if s.authError(w, r, s.auth.Confirm(r.Context(), p, deref(in.Password), deref(in.TotpCode), "docker_console")) {
			return
		}
	}
	c, _, err := s.pvehosts.StartConsole(r.Context(), plan.Host.NodeID, p.User.ID, plan.Request)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	s.docker.ConsoleOpened(r.Context(), events.User(p.User.ID), plan)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, gen.PveConsoleOpened{Session: c.ID, Kind: protocol.ConsoleDocker, Title: plan.Title()})
}
