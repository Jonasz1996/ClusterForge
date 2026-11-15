package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/updates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Fase 15: automatisch bijwerken, het compose-bestand in de GUI en Fix
// voorstellen.

type policyView struct {
	Enabled       bool   `json:"enabled"`
	Apt           bool   `json:"apt"`
	AptFull       bool   `json:"apt_full"`
	AptAutoremove bool   `json:"apt_autoremove"`
	Docker        bool   `json:"docker"`
	DockerPrune   bool   `json:"docker_prune"`
	Reboot        bool   `json:"reboot"`
	Window        string `json:"window"`
}

func toPolicyView(p updates.Policy) policyView { return policyView(p) }

func fromPolicy(p gen.UpdatePolicy) updates.Policy {
	return updates.Policy{
		Enabled: p.Enabled, Apt: p.Apt, AptFull: p.AptFull, AptAutoremove: p.AptAutoremove, Docker: p.Docker,
		DockerPrune: p.DockerPrune, Reboot: p.Reboot, Window: p.Window,
	}
}

// GetUpdatePolicies toont het beleid voor automatisch bijwerken.
func (s *Server) GetUpdatePolicies(w http.ResponseWriter, r *http.Request) {
	s.writePolicies(w, r)
}

func (s *Server) writePolicies(w http.ResponseWriter, r *http.Request) {
	pol, err := s.updates.Policies(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type envView struct {
		Environment string     `json:"environment"`
		Policy      policyView `json:"policy"`
		UpdatedAt   *time.Time `json:"updated_at"`
		UpdatedBy   *string    `json:"updated_by"`
	}
	type clusterView struct {
		ClusterID   uuid.UUID   `json:"cluster_id"`
		Name        string      `json:"name"`
		Slug        string      `json:"slug"`
		Environment string      `json:"environment"`
		Mode        string      `json:"mode"`
		Own         *policyView `json:"own"`
		Effective   policyView  `json:"effective"`
		NextStart   *time.Time  `json:"next_start"`
		LastRunAt   *time.Time  `json:"last_run_at"`
		LastJobID   *uuid.UUID  `json:"last_job_id"`
		LastNote    string      `json:"last_note"`
	}
	out := struct {
		Timezone     string        `json:"timezone"`
		Environments []envView     `json:"environments"`
		Clusters     []clusterView `json:"clusters"`
	}{Timezone: time.Local.String(), Environments: []envView{}, Clusters: []clusterView{}}
	for _, e := range updates.Envs {
		v := envView{Environment: e, Policy: toPolicyView(pol.Envs[e]), UpdatedBy: pol.EnvBy[e]}
		if at, ok := pol.EnvAt[e]; ok {
			v.UpdatedAt = &at
		}
		out.Environments = append(out.Environments, v)
	}
	for _, c := range pol.Clusters {
		v := clusterView{
			ClusterID: c.ClusterID, Name: c.Name, Slug: c.Slug, Environment: c.Env, Mode: c.Mode,
			Effective: toPolicyView(c.Effective), NextStart: c.NextStart,
		}
		if c.Own != nil {
			o := toPolicyView(*c.Own)
			v.Own = &o
		}
		if lr := c.LastRun; lr != nil {
			at := lr.StartedAt
			v.LastRunAt, v.LastJobID, v.LastNote = &at, lr.JobID, lr.Note
		}
		out.Clusters = append(out.Clusters, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// SetEnvUpdatePolicy bewaart het beleid van een omgeving.
func (s *Server) SetEnvUpdatePolicy(w http.ResponseWriter, r *http.Request, environment string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.UpdatePolicyInput
	if !decode(w, r, &in) {
		return
	}
	if environment == "prod" && !confirmProd(w, p, true, "prod", in.Confirm, "automatisch bijwerken instellen") {
		return
	}
	pol := updates.Policy{
		Enabled: in.Enabled, Apt: in.Apt, AptFull: in.AptFull, AptAutoremove: in.AptAutoremove, Docker: in.Docker,
		DockerPrune: in.DockerPrune, Reboot: in.Reboot, Window: in.Window,
	}
	if err := s.updates.SetEnvPolicy(r.Context(), events.User(p.User.ID), &p.User.ID, environment, pol); err != nil {
		s.updateError(w, r, err)
		return
	}
	s.writePolicies(w, r)
}

// SetClusterUpdatePolicy zet een cluster op de omgeving, een eigen beleid
// of uit.
func (s *Server) SetClusterUpdatePolicy(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ClusterUpdatePolicyInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	if c.Environment == "prod" && !confirmProd(w, p, true, c.Slug, in.Confirm, "automatisch bijwerken instellen") {
		return
	}
	pol := updates.DefaultPolicy
	if in.Policy != nil {
		pol = fromPolicy(*in.Policy)
	}
	if err := s.updates.SetClusterPolicy(ctx, events.User(p.User.ID), &p.User.ID, clusterID, string(in.Mode), pol); err != nil {
		s.updateError(w, r, err)
		return
	}
	s.writePolicies(w, r)
}

// ReadComposeFiles leest de bestanden van een project via de agent.
func (s *Server) ReadComposeFiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.ComposeRef
	if !decode(w, r, &in) {
		return
	}
	files, err := s.updates.ReadCompose(r.Context(), in.NodeId, in.Project)
	if err != nil {
		var ce *updates.ConflictError
		var fe *updates.FieldError
		if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &ce) || errors.As(err, &fe) {
			s.updateError(w, r, err)
			return
		}
		writeError(w, http.StatusBadGateway, "agent", err.Error())
		return
	}
	type fileView struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		SHA256  string `json:"sha256"`
		Env     bool   `json:"env"`
		Secrets int    `json:"secrets"`
	}
	out := struct {
		Files []fileView `json:"files"`
	}{Files: []fileView{}}
	for _, f := range files {
		out.Files = append(out.Files, fileView{f.Path, f.Content, f.SHA256, f.Env, f.Secrets})
	}
	writeJSON(w, http.StatusOK, out)
}

// WriteComposeFile slaat een bestand op en start het project opnieuw.
func (s *Server) WriteComposeFile(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComposeWriteInput
	if !decode(w, r, &in) {
		return
	}
	edit := &protocol.ComposeEdit{Path: in.Path, Content: in.Content, OldSHA256: in.OldSha256}
	req := updates.Request{NodeIDs: []uuid.UUID{in.NodeId}, Docker: &updates.Docker{Projects: []string{in.Project}, Edit: edit}}
	if in.HealthSeconds != nil {
		req.Docker.HealthSeconds = *in.HealthSeconds
	}
	check := protocol.DockerRun{Smart: true, Projects: req.Docker.Projects, Edit: edit, HealthSeconds: req.Docker.HealthSeconds}
	if err := (protocol.RunSpec{ID: "x", Kind: protocol.RunDocker, Docker: &check}).Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	ctx := r.Context()
	nodes, err := s.updates.Select(ctx, req)
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	if nodes[0].Prod() && !confirmProd(w, p, true, nodes[0].ClusterSlug, in.Confirm, "een compose-bestand aanpassen") {
		return
	}
	started, _, err := s.updates.Start(ctx, events.User(p.User.ID), req, nodes)
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(started[0], &p.User.Username))
}

// ProposeDockerFix laat de assistent een fix voorstellen, met de release
// notes van de bron.
func (s *Server) ProposeDockerFix(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.ComposeRef
	if !decode(w, r, &in) {
		return
	}
	if !s.canAssist(w, r) {
		return
	}
	ctx := r.Context()
	task, err := s.updates.FixTask(ctx, in.NodeId, in.Project)
	switch {
	case errors.Is(err, updates.ErrNoReport), errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", updates.ErrNoReport.Error())
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	nodeID, project := in.NodeId, in.Project
	finish := task.Finish
	task.Finish = func(fctx context.Context, res assistant.Result) error {
		if res.ConversationID != uuid.Nil {
			_ = s.updates.SetFixConversation(fctx, nodeID, project, res.ConversationID)
		}
		if finish != nil {
			return finish(fctx, res)
		}
		return nil
	}
	turn, err := s.assistant.Start(ctx, caller(r), task)
	if s.assistantError(w, r, err) {
		return
	}
	s.streamTurn(w, r, turn)
}
