package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/zabbixagent"
)

// Zabbix-agents: zabbix-agent2 met een eigen PSK per node. De antwoorden
// zijn de structs van internal/zabbixagent; hun JSON volgt de schema's
// ZabbixAgent* in openapi.yaml.

func (s *Server) zabbixAgentError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *zabbixagent.FieldError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.Is(err, zabbixagent.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) GetZabbixAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	set, err := s.zabbixAgents.Settings(ctx)
	if err != nil {
		s.zabbixAgentError(w, r, err)
		return
	}
	nodes, err := s.zabbixAgents.Nodes(ctx)
	if err != nil {
		s.zabbixAgentError(w, r, err)
		return
	}
	if nodes == nil {
		nodes = []zabbixagent.Node{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": set, "nodes": nodes})
}

// prodConfirm vraagt de TOTP-code als een actie prod raakt; false als het
// antwoord al geschreven is.
func (s *Server) prodConfirm(w http.ResponseWriter, r *http.Request, p auth.Principal, code *string, action string) bool {
	if p.User.TotpEnabledAt == nil {
		writeError(w, http.StatusForbidden, "totp_required", "op prod kan alleen een beheerder met tweestapsverificatie dit doen; zet die aan bij Instellingen")
		return false
	}
	if code == nil || *code == "" {
		writeError(w, http.StatusConflict, "needs_confirmation", "dit raakt prod; geef de code van je tweestapsverificatie")
		return false
	}
	return !s.authError(w, r, s.auth.Confirm(r.Context(), p, "", *code, action))
}

func (s *Server) UpdateZabbixAgentSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ZabbixAgentSettingsInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	cur, err := s.zabbixAgents.Settings(ctx)
	if err != nil {
		s.zabbixAgentError(w, r, err)
		return
	}
	if in.AutoProd && !cur.AutoProd && !s.prodConfirm(w, r, p, in.TotpCode, "zabbix_agents_auto_prod") {
		return
	}
	uid := p.User.ID
	set, err := s.zabbixAgents.UpdateSettings(ctx, events.User(uid), &uid, zabbixagent.SettingsInput{
		Settings: zabbixagent.Settings{
			AutoApply: in.AutoApply, AutoProd: in.AutoProd, Server: in.Server, ServerActive: in.ServerActive, Version: in.Version,
			HostMetadata: in.HostMetadata, APIURL: in.ApiUrl, HostGroup: in.HostGroup, Templates: in.Templates,
			DockerTemplate: in.DockerTemplate, Proxy: in.Proxy,
		},
		Token: in.Token,
	})
	if err != nil {
		s.zabbixAgentError(w, r, err)
		return
	}
	s.kickZabbixAgents()
	writeJSON(w, http.StatusOK, set)
}

// kickZabbixAgents draait meteen een ronde.
func (s *Server) kickZabbixAgents() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.zabbixAgents.Tick(ctx); err != nil {
			s.log.Warn("Zabbix-agent-ronde mislukt", "err", err)
		}
	}()
}

func (s *Server) TestZabbixAgentAPI(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	v, err := s.zabbixAgents.Test(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, gen.ZabbixAgentTestResult{Ok: false, Version: v, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, gen.ZabbixAgentTestResult{Ok: true, Version: v, Message: "Zabbix " + v + " bereikbaar, het token werkt"})
}

func (s *Server) SetZabbixAgentNodes(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ZabbixAgentNodesInput
	if !decode(w, r, &in) {
		return
	}
	if !in.Action.Valid() {
		writeError(w, http.StatusBadRequest, "invalid", "action is manage, unmanage, apply of rotate")
		return
	}
	ctx := r.Context()
	nodes, err := s.zabbixAgents.Nodes(ctx)
	if err != nil {
		s.zabbixAgentError(w, r, err)
		return
	}
	all := in.All != nil && *in.All
	var want []string
	if in.NodeIds != nil {
		want = uuidStrings(*in.NodeIds)
	}
	if !all && len(want) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "kies nodes of alle nodes")
		return
	}
	var chosen []zabbixagent.Node
	prod := false
	for _, n := range nodes {
		switch {
		case !all && !contains(want, n.NodeID.String()):
			continue
		case all && in.Action == gen.ZabbixAgentNodesInputActionApply && n.Enabled && n.Managed && !n.Pending:
			continue
		case all && in.Action == gen.ZabbixAgentNodesInputActionUnmanage && !n.Enabled:
			continue
		case all && in.Action == gen.ZabbixAgentNodesInputActionRotate && !n.Enabled:
			continue
		}
		chosen = append(chosen, n)
		prod = prod || n.Prod()
	}
	if len(chosen) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "geen nodes om dit op te doen")
		return
	}
	actor := events.User(p.User.ID)
	ids := make([]uuid.UUID, 0, len(chosen))
	for _, n := range chosen {
		ids = append(ids, n.NodeID)
	}
	out := gen.ZabbixAgentNodesResult{Jobs: []uuid.UUID{}, Skipped: []string{}}
	switch in.Action {
	case gen.ZabbixAgentNodesInputActionManage:
		sk, err := s.zabbixAgents.Enable(ctx, actor, ids)
		if err != nil {
			s.zabbixAgentError(w, r, err)
			return
		}
		out.Skipped = append(out.Skipped, sk...)
		s.kickZabbixAgents()
	case gen.ZabbixAgentNodesInputActionUnmanage:
		if err := s.zabbixAgents.Disable(ctx, actor, ids); err != nil {
			s.zabbixAgentError(w, r, err)
			return
		}
	default:
		if prod && !s.prodConfirm(w, r, p, in.TotpCode, "zabbix_agents_prod") {
			return
		}
		for _, n := range chosen {
			if in.Action == gen.ZabbixAgentNodesInputActionRotate {
				if _, err := s.zabbixAgents.Rotate(ctx, actor, n.NodeID); err != nil {
					out.Skipped = append(out.Skipped, n.Hostname+": "+err.Error())
					continue
				}
			}
			id, err := s.zabbixAgents.Apply(ctx, actor, n.NodeID)
			var fe *zabbixagent.FieldError
			switch {
			case errors.As(err, &fe):
				out.Skipped = append(out.Skipped, n.Hostname+": "+fe.Msg)
			case err != nil:
				s.internalError(w, r, err)
				return
			default:
				out.Jobs = append(out.Jobs, id)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
