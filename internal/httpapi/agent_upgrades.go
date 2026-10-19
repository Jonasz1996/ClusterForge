package httpapi

import (
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/agentupgrade"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

func (s *Server) ListAgentUpgrades(w http.ResponseWriter, r *http.Request) {
	s.writeAgentUpgrades(w, r, http.StatusOK)
}

func (s *Server) writeAgentUpgrades(w http.ResponseWriter, r *http.Request, code int) {
	ov, err := s.upgrades.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, code, toAPIAgentUpgrades(ov))
}

// StartAgentUpgrades werkt agents bij: per cluster één taak, één node
// tegelijk. Prod vraagt de slug van het cluster, en dan maar één
// prodcluster per keer.
func (s *Server) StartAgentUpgrades(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.AgentUpgradeInput
	if !decode(w, r, &in) {
		return
	}
	req := agentupgrade.Request{ClusterID: in.ClusterId, All: in.All != nil && *in.All}
	if in.NodeIds != nil {
		req.NodeIDs = *in.NodeIds
	}
	n := 0
	for _, set := range []bool{len(req.NodeIDs) > 0, req.ClusterID != nil, req.All} {
		if set {
			n++
		}
	}
	if n != 1 {
		writeError(w, http.StatusBadRequest, "invalid", "kies node_ids, cluster_id of all")
		return
	}
	ctx := r.Context()
	agents, err := s.upgrades.Select(ctx, req)
	if err != nil {
		s.upgradeError(w, r, err)
		return
	}
	var prod []agentupgrade.Agent
	for _, a := range agents {
		if a.Prod() && !slices.ContainsFunc(prod, func(b agentupgrade.Agent) bool { return *b.ClusterID == *a.ClusterID }) {
			prod = append(prod, a)
		}
	}
	if len(prod) > 1 {
		writeError(w, http.StatusBadRequest, "invalid", "werk de agents van één prodcluster tegelijk bij")
		return
	}
	if len(prod) == 1 && !confirmProd(w, p, true, prod[0].ClusterSlug, in.Confirm, "agents bijwerken") {
		return
	}
	started, skipped, err := s.upgrades.Start(ctx, events.User(p.User.ID), agents, false)
	if err != nil {
		s.upgradeError(w, r, err)
		return
	}
	out := gen.AgentUpgradeStarted{Jobs: make([]gen.Job, 0, len(started)), Skipped: skipped}
	if out.Skipped == nil {
		out.Skipped = []string{}
	}
	for _, j := range started {
		out.Jobs = append(out.Jobs, toAPIJob(j, &p.User.Username))
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (s *Server) SetAgentUpgradeAuto(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.SetAgentUpgradeAutoJSONRequestBody
	if !decode(w, r, &in) {
		return
	}
	if err := s.upgrades.SetAuto(r.Context(), events.User(p.User.ID), &p.User.ID, in.Enabled); err != nil {
		s.upgradeError(w, r, err)
		return
	}
	s.writeAgentUpgrades(w, r, http.StatusOK)
}

func (s *Server) SetClusterAgentUpgradeAuto(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.SetClusterAgentUpgradeAutoJSONRequestBody
	if !decode(w, r, &in) {
		return
	}
	if err := s.upgrades.SetClusterAuto(r.Context(), events.User(p.User.ID), &p.User.ID, clusterID, in.Enabled); err != nil {
		s.upgradeError(w, r, err)
		return
	}
	s.writeAgentUpgrades(w, r, http.StatusOK)
}

func (s *Server) upgradeError(w http.ResponseWriter, r *http.Request, err error) {
	var ce *agentupgrade.ConflictError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	default:
		s.internalError(w, r, err)
	}
}

func toAPIAgentUpgrades(ov agentupgrade.Overview) gen.AgentUpgradeOverview {
	out := gen.AgentUpgradeOverview{
		Version: ov.Offer.Version, Protocol: ov.Offer.Protocol, Archs: []string{}, Auto: ov.Auto, OffClusterIds: ov.Off,
		Agents: make([]gen.AgentUpgradeAgent, 0, len(ov.Agents)),
	}
	if out.OffClusterIds == nil {
		out.OffClusterIds = []uuid.UUID{}
	}
	for _, arch := range agentupgrade.Archs {
		if ov.Offer.SHA256[arch] != "" {
			out.Archs = append(out.Archs, arch)
		}
	}
	for _, a := range ov.Agents {
		out.Agents = append(out.Agents, gen.AgentUpgradeAgent{
			NodeId: a.NodeID, Hostname: a.Hostname, ClusterId: nullableOf(a.ClusterID), ClusterName: a.ClusterName,
			ClusterSlug: a.ClusterSlug, ClusterEnvironment: a.ClusterEnv, Version: a.Version, Protocol: a.Protocol, Arch: a.Arch,
			Online: a.Online, State: gen.AgentUpgradeAgentState(a.State), Why: a.Why, Auto: a.Auto,
		})
	}
	out.Running = make([]struct {
		ClusterId nullable.Nullable[uuid.UUID] `json:"cluster_id"`
		JobId     uuid.UUID                    `json:"job_id"`
	}, 0, len(ov.Running))
	for cluster, job := range ov.Running {
		c := nullable.NewNullNullable[uuid.UUID]()
		if cluster != uuid.Nil {
			c = nullable.NewNullableWithValue(cluster)
		}
		out.Running = append(out.Running, struct {
			ClusterId nullable.Nullable[uuid.UUID] `json:"cluster_id"`
			JobId     uuid.UUID                    `json:"job_id"`
		}{c, job})
	}
	return out
}
