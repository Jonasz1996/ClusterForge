package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/commands"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/updates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ListUpdates toont per node de updates uit de facts (fase 10).
func (s *Server) ListUpdates(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.updates.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.UpdateOverview{RunProtocol: protocol.RunSince, Nodes: make([]gen.UpdateNode, 0, len(nodes))}
	for _, n := range nodes {
		un := gen.UpdateNode{
			NodeId: n.NodeID, Hostname: n.Hostname, ClusterId: nullableOf(n.ClusterID), ClusterName: n.ClusterName,
			ClusterSlug: n.ClusterSlug, ClusterEnvironment: n.ClusterEnv, Online: n.Online, Protocol: n.Protocol,
			Supported: n.Supported, Packages: []string{}, RebootRequired: n.RebootRequired, HasDatabase: n.HasDatabase,
			VipHolder: n.VIPHolder, FactsAt: nullableOf(n.FactsAt), UpgradesTotal: nullable.NewNullNullable[int](),
			AptUpdatedAt: nullable.NewNullNullable[time.Time](),
		}
		if !n.AptUpdatedAt.IsZero() {
			un.AptUpdatedAt = nullable.NewNullableWithValue(n.AptUpdatedAt)
		}
		if u := n.Upgrades; u != nil {
			un.UpgradesTotal = nullable.NewNullableWithValue(u.Total)
			un.UpgradesSecurity = u.Security
			if u.Packages != nil {
				un.Packages = u.Packages
			}
		}
		un.ComposeProjects = make([]struct {
			ConfigFiles []string `json:"config_files"`
			Name        string   `json:"name"`
			Status      string   `json:"status"`
		}, 0)
		if d := n.Docker; d != nil {
			un.DockerVersion = d.Version
			for _, p := range d.Projects {
				files := p.ConfigFiles
				if files == nil {
					files = []string{}
				}
				un.ComposeProjects = append(un.ComposeProjects, struct {
					ConfigFiles []string `json:"config_files"`
					Name        string   `json:"name"`
					Status      string   `json:"status"`
				}{files, p.Name, p.Status})
			}
		}
		out.Nodes = append(out.Nodes, un)
	}
	writeJSON(w, http.StatusOK, out)
}

// StartUpdates werkt pakketten en/of Docker bij, met per cluster één taak.
func (s *Server) StartUpdates(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.UpdateInput
	if !decode(w, r, &in) {
		return
	}
	req := updates.Request{ClusterID: in.ClusterId, Reboot: in.Reboot != nil && *in.Reboot}
	if in.NodeIds != nil {
		req.NodeIDs = *in.NodeIds
	}
	if in.Apt != nil {
		req.Apt = &updates.Apt{Full: in.Apt.Full != nil && *in.Apt.Full, Autoremove: in.Apt.Autoremove != nil && *in.Apt.Autoremove}
	}
	if in.Docker != nil {
		req.Docker = &updates.Docker{Prune: in.Docker.Prune != nil && *in.Docker.Prune}
		if in.Docker.Projects != nil {
			req.Docker.Projects = *in.Docker.Projects
		}
		for _, name := range req.Docker.Projects {
			if err := (protocol.RunSpec{ID: "x", Kind: protocol.RunDocker, Docker: &protocol.DockerRun{Projects: []string{name}}}).Validate(); err != nil {
				writeError(w, http.StatusBadRequest, "invalid", err.Error())
				return
			}
		}
	}
	ctx := r.Context()
	nodes, err := s.updates.Select(ctx, req)
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	var prod []string
	for _, n := range nodes {
		if n.Prod() && !contains(prod, n.ClusterSlug) {
			prod = append(prod, n.ClusterSlug)
		}
	}
	if len(prod) > 1 {
		writeError(w, http.StatusBadRequest, "invalid", "werk de nodes van één prodcluster tegelijk bij")
		return
	}
	if len(prod) == 1 && !confirmProd(w, p, true, prod[0], in.Confirm, "bijwerken") {
		return
	}
	started, skipped, err := s.updates.Start(ctx, events.User(p.User.ID), req, nodes)
	if err != nil {
		s.updateError(w, r, err)
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

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (s *Server) updateError(w http.ResponseWriter, r *http.Request, err error) {
	var ce *updates.ConflictError
	var fe *updates.FieldError
	var cce *commands.ConflictError
	var cfe *commands.FieldError
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, commands.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	case errors.As(err, &cce):
		writeError(w, http.StatusConflict, "conflict", cce.Msg)
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.As(err, &cfe):
		writeError(w, http.StatusBadRequest, "invalid", cfe.Msg)
	default:
		s.internalError(w, r, err)
	}
}

// ListCommands geeft de eigen commando's met hun laatste run.
func (s *Server) ListCommands(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.q.ListCommands(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	last, err := s.q.ListLastCommandRuns(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	lastBy := map[uuid.UUID]gen.Job{}
	for _, l := range last {
		lastBy[l.CommandID] = toAPIJob(l.Job, l.RequestedByName)
	}
	items := make([]gen.Command, 0, len(rows))
	for _, c := range rows {
		items = append(items, toAPICommand(store.Command{
			ID: c.ID, Name: c.Name, Description: c.Description, Script: c.Script, RunAs: c.RunAs, TimeoutSeconds: c.TimeoutSeconds,
			Revision: c.Revision, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, UpdatedBy: c.UpdatedBy,
		}, c.UpdatedByName, lastBy))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func toAPICommand(c store.Command, by *string, last map[uuid.UUID]gen.Job) gen.Command {
	out := gen.Command{
		Id: c.ID, Name: c.Name, Description: c.Description, RunAs: c.RunAs, TimeoutSeconds: int(c.TimeoutSeconds),
		Revision: int(c.Revision), UpdatedAt: c.UpdatedAt, UpdatedBy: nullableOf(by), LastRun: nullable.NewNullNullable[gen.Job](),
	}
	if j, ok := last[c.ID]; ok {
		out.LastRun = nullable.NewNullableWithValue(j)
	}
	return out
}

func (s *Server) writeCommand(w http.ResponseWriter, r *http.Request, id uuid.UUID, code int) {
	ctx := r.Context()
	c, err := s.q.GetCommand(ctx, id)
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	revs, err := s.q.ListCommandRevisions(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	runs, err := s.q.ListCommandRuns(ctx, store.ListCommandRunsParams{CommandID: id.String(), MaxRows: 20})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	last := map[uuid.UUID]gen.Job{}
	apiRuns := make([]gen.Job, 0, len(runs))
	for i, j := range runs {
		aj := toAPIJob(j.Job, j.RequestedByName)
		if i == 0 {
			last[id] = aj
		}
		apiRuns = append(apiRuns, aj)
	}
	base := toAPICommand(store.Command{
		ID: c.ID, Name: c.Name, Description: c.Description, Script: c.Script, RunAs: c.RunAs, TimeoutSeconds: c.TimeoutSeconds,
		Revision: c.Revision, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, UpdatedBy: c.UpdatedBy,
	}, c.UpdatedByName, last)
	out := gen.CommandDetail{
		Id: base.Id, Name: base.Name, Description: base.Description, RunAs: base.RunAs, TimeoutSeconds: base.TimeoutSeconds,
		Revision: base.Revision, UpdatedAt: base.UpdatedAt, UpdatedBy: base.UpdatedBy, LastRun: base.LastRun,
		Script: c.Script, Runs: apiRuns,
	}
	for _, rv := range revs {
		out.Revisions = append(out.Revisions, struct {
			CreatedAt time.Time                 `json:"created_at"`
			CreatedBy nullable.Nullable[string] `json:"created_by"`
			Revision  int                       `json:"revision"`
			Script    string                    `json:"script"`
			Sha256    string                    `json:"sha256"`
		}{rv.CreatedAt, nullableOf(rv.CreatedByName), int(rv.Revision), rv.Script, commands.Hash(rv.Script)})
	}
	if out.Revisions == nil {
		out.Revisions = out.Revisions[:0:0]
	}
	writeJSON(w, code, out)
}

func commandInput(in gen.CommandInput) commands.Input {
	out := commands.Input{Name: in.Name, Script: in.Script}
	if in.Description != nil {
		out.Description = *in.Description
	}
	if in.RunAs != nil {
		out.RunAs = *in.RunAs
	}
	if in.TimeoutSeconds != nil {
		out.TimeoutSeconds = *in.TimeoutSeconds
	}
	return out
}

func (s *Server) CreateCommand(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CommandInput
	if !decode(w, r, &in) {
		return
	}
	c, err := s.commands.Create(r.Context(), p.User.ID, commandInput(in))
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	s.writeCommand(w, r, c.ID, http.StatusCreated)
}

func (s *Server) GetCommand(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.writeCommand(w, r, id, http.StatusOK)
}

func (s *Server) UpdateCommand(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CommandInput
	if !decode(w, r, &in) {
		return
	}
	if _, err := s.commands.Update(r.Context(), p.User.ID, id, commandInput(in)); err != nil {
		s.updateError(w, r, err)
		return
	}
	s.writeCommand(w, r, id, http.StatusOK)
}

func (s *Server) DeleteCommand(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.commands.Delete(r.Context(), events.User(p.User.ID), id); err != nil {
		s.updateError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunCommand draait een commando op nodes of een cluster.
func (s *Server) RunCommand(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CommandRunInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	if _, err := s.q.GetCommand(ctx, id); err != nil {
		s.updateError(w, r, err)
		return
	}
	req := commands.RunRequest{ClusterID: in.ClusterId, ContinueOnError: in.ContinueOnError != nil && *in.ContinueOnError}
	if in.NodeIds != nil {
		req.NodeIDs = *in.NodeIds
	}
	t, err := s.commands.Select(ctx, req)
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	if len(t.Prod) > 1 {
		writeError(w, http.StatusBadRequest, "invalid", "draai een commando op één prodcluster tegelijk")
		return
	}
	if len(t.Prod) == 1 && !confirmProd(w, p, true, t.Prod[0], in.Confirm, "een commando draaien") {
		return
	}
	j, err := s.commands.Start(ctx, events.User(p.User.ID), id, req, t)
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}
