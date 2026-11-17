package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/agentconf"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// GetAgentEndpoints geeft het adres voor agents en per agent het adres in
// zijn agent.json.
func (s *Server) GetAgentEndpoints(w http.ResponseWriter, r *http.Request) {
	s.writeAgentEndpoints(w, r, http.StatusOK)
}

func (s *Server) writeAgentEndpoints(w http.ResponseWriter, r *http.Request, code int) {
	ov, err := s.agentConf.Overview(r.Context(), r.Host)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, code, toAPIAgentEndpoints(ov))
}

// SetAgentEndpoints bewaart het adres voor agents.
func (s *Server) SetAgentEndpoints(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.SetAgentEndpointsJSONRequestBody
	if !decode(w, r, &in) {
		return
	}
	err := s.agentConf.SetEndpoints(r.Context(), events.User(p.User.ID), &p.User.ID, in.ServerUrl, in.NatsUrl)
	var ve agentconf.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Error())
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.writeAgentEndpoints(w, r, http.StatusOK)
}

// ApplyAgentEndpoints zet agents om naar het adres voor agents.
func (s *Server) ApplyAgentEndpoints(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ApplyAgentEndpointsJSONRequestBody
	if !decode(w, r, &in) {
		return
	}
	req := agentconf.Request{All: in.All != nil && *in.All, IncludeProd: in.IncludeProd != nil && *in.IncludeProd}
	if in.NodeIds != nil {
		req.NodeIDs = *in.NodeIds
	}
	if (len(req.NodeIDs) > 0) == req.All {
		writeError(w, http.StatusBadRequest, "invalid", "kies node_ids of all")
		return
	}
	ctx := r.Context()
	agents, e, err := s.agentConf.Select(ctx, r.Host, req)
	var ce *agentconf.ConflictError
	switch {
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	prod := false
	for _, a := range agents {
		prod = prod || a.Prod()
	}
	if prod {
		if p.User.TotpEnabledAt == nil {
			writeError(w, http.StatusForbidden, "totp_required", "op prod kan alleen een beheerder met tweestapsverificatie agents omzetten; zet die aan bij Instellingen")
			return
		}
		if in.TotpCode == nil || *in.TotpCode == "" {
			writeError(w, http.StatusConflict, "needs_confirmation", "dit raakt prod; geef de code van je tweestapsverificatie")
			return
		}
		if s.authError(w, r, s.auth.Confirm(ctx, p, "", *in.TotpCode, "agent_reconfigure_prod")) {
			return
		}
	}
	started, skipped, err := s.agentConf.Start(ctx, events.User(p.User.ID), agents, e)
	switch {
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	out := gen.AgentUpgradeStarted{Jobs: []gen.Job{}, Skipped: skipped, QueuedProd: []string{}}
	if out.Skipped == nil {
		out.Skipped = []string{}
	}
	for _, j := range started {
		out.Jobs = append(out.Jobs, toAPIJob(j, &p.User.Username))
	}
	writeJSON(w, http.StatusAccepted, out)
}

func toAPIAgentEndpoints(ov agentconf.Overview) gen.AgentEndpointsOverview {
	e := ov.Endpoints
	out := gen.AgentEndpointsOverview{
		ServerUrl: e.ServerURL, ServerSource: gen.AgentEndpointsOverviewServerSource(e.ServerSource),
		NatsUrl: e.NatsURL, NatsSource: gen.AgentEndpointsOverviewNatsSource(e.NatsSource),
		ServerSetting: e.ServerSetting, NatsSetting: e.NatsSetting, ServerDefault: e.ServerDefault, NatsDefault: e.NatsDefault,
		Protocol: protocol.ReconfigureSince, Agents: make([]gen.AgentEndpointAgent, 0, len(ov.Agents)),
	}
	for _, a := range ov.Agents {
		out.Agents = append(out.Agents, gen.AgentEndpointAgent{
			NodeId: a.NodeID, Hostname: a.Hostname, ClusterId: nullableOf(a.ClusterID), ClusterName: a.ClusterName, ClusterSlug: a.ClusterSlug,
			ClusterEnvironment: a.ClusterEnv, Protocol: a.Protocol, ServerUrl: a.ServerURL, NatsUrl: a.NatsURL, Online: a.Online,
			State: gen.AgentEndpointAgentState(a.State), Why: a.Why,
		})
	}
	type run = struct {
		ClusterId nullable.Nullable[uuid.UUID] `json:"cluster_id"`
		JobId     uuid.UUID                    `json:"job_id"`
	}
	out.Running = []run{}
	for c, j := range ov.Running {
		item := run{ClusterId: nullable.NewNullNullable[uuid.UUID](), JobId: j}
		if c != uuid.Nil {
			item.ClusterId = nullable.NewNullableWithValue(c)
		}
		out.Running = append(out.Running, item)
	}
	return out
}
