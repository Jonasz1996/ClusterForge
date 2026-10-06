package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// retryableKinds zijn de taken die na een fout opnieuw kunnen.
var retryableKinds = map[string]bool{deploy.Kind: true}

var countParamRe = regexp.MustCompile(`^\{\{\s*\.params\.([a-z0-9_]+)\s*\}\}$`)

func (s *Server) ListTemplates(w http.ResponseWriter, r *http.Request) {
	items := []gen.Template{}
	for _, t := range templates.Builtin() {
		items = append(items, toAPITemplate(t))
	}
	writeJSON(w, http.StatusOK, list[gen.Template]{items})
}

func toAPITemplate(t *templates.Template) gen.Template {
	str := func(v any) nullable.Nullable[string] {
		if v == nil {
			return nullable.NewNullNullable[string]()
		}
		return nullable.NewNullableWithValue(fmt.Sprint(v))
	}
	out := gen.Template{
		Name: t.Name, Version: t.Version, Title: t.Title, Description: strings.TrimSpace(t.Description),
		ClusterType: gen.ClusterType(t.ClusterType), Params: []gen.TemplateParam{}, Roles: []gen.TemplateRole{},
		Services: []gen.TemplateService{},
	}
	for _, p := range t.Params {
		out.Params = append(out.Params, gen.TemplateParam{
			Name: p.Name, Type: gen.TemplateParamType(p.Type), Label: p.Label, Help: p.Help,
			Optional: p.Optional || p.Type == "secret", Immutable: p.Immutable, Default: str(p.Default), Min: str(p.Min), Max: str(p.Max),
		})
	}
	for _, role := range t.Roles {
		tr := gen.TemplateRole{Name: role.Name, Count: nullable.NewNullNullable[int](), CountParam: nullable.NewNullNullable[string]()}
		if m := countParamRe.FindStringSubmatch(role.Count); m != nil {
			tr.CountParam = nullable.NewNullableWithValue(m[1])
		} else if n, err := strconv.Atoi(strings.TrimSpace(role.Count)); err == nil {
			tr.Count = nullable.NewNullableWithValue(n)
		}
		out.Roles = append(out.Roles, tr)
	}
	for _, sv := range t.Services {
		ts := gen.TemplateService{
			Name: sv.Name, Kind: gen.ServiceKind(sv.Kind), DependsOn: append([]string{}, sv.DependsOn...),
			Unit: nullable.NewNullNullable[string](), Port: nullable.NewNullNullable[int](), PortParam: nullable.NewNullNullable[string](),
		}
		if sv.Unit != "" {
			ts.Unit = nullable.NewNullableWithValue(sv.Unit)
		}
		if p := sv.PortParam(); p != "" {
			ts.PortParam = nullable.NewNullableWithValue(p)
		} else if p := sv.FixedPort(); p > 0 {
			ts.Port = nullable.NewNullableWithValue(p)
		}
		out.Services = append(out.Services, ts)
	}
	return out
}

func (s *Server) PlanDeployment(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.DeployInput
	if !decode(w, r, &in) {
		return
	}
	plan, err := s.deploy.Plan(r.Context(), deployRequest(in))
	if err != nil {
		s.deployError(w, r, err)
		return
	}
	out := gen.DeployPlan{Nodes: []gen.DeployPlanNode{}, Vip: plan.VIP, Vrid: nullable.NewNullNullable[int]()}
	if plan.VRID > 0 {
		out.Vrid = nullable.NewNullableWithValue(plan.VRID)
	}
	for _, n := range plan.Nodes {
		addr := ""
		if n.Address != "" {
			addr = fmt.Sprintf("%s/%d", n.Address, n.Prefix)
		}
		out.Nodes = append(out.Nodes, gen.DeployPlanNode{
			Hostname: n.Hostname, Role: n.Role, Address: addr, Host: n.Host,
			Cpu: n.VM.CPU, MemoryMib: n.VM.MemoryMiB, DiskGib: n.VM.DiskGiB,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) DeployCluster(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.DeployInput
	if !decode(w, r, &in) {
		return
	}
	j, clusterID, err := s.deploy.Request(r.Context(), events.User(p.User.ID), deployRequest(in))
	if err != nil {
		s.deployError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, gen.DeployResult{Job: toAPIJob(j, &p.User.Username), ClusterId: clusterID})
}

func deployRequest(in gen.DeployInput) deploy.Request {
	req := deploy.Request{
		Template: in.Template,
		Cluster: deploy.ClusterInput{
			Name: in.Cluster.Name, Slug: in.Cluster.Slug, Environment: string(in.Cluster.Environment),
			Description: deref(in.Cluster.Description),
		},
		Params: in.Params,
		Target: deploy.Target{
			ProxmoxID: in.Target.ProxmoxId, ImageVMID: in.Target.ImageVmid, Storage: deref(in.Target.Storage),
			Bridge: deref(in.Target.Bridge), VLAN: nullablePtr(in.Target.Vlan), Network: string(in.Target.Network),
			FirstIP: deref(in.Target.FirstIp), Gateway: deref(in.Target.Gateway), DNS: deref(in.Target.Dns),
			SSHKeys: deref(in.Target.SshKeys),
		},
		ServerURL: in.ServerUrl,
	}
	if req.Params == nil {
		req.Params = map[string]any{}
	}
	return req
}

func (s *Server) deployError(w http.ResponseWriter, r *http.Request, err error) {
	var ve deploy.ValidationError
	var ice inventory.ConflictError
	var pve proxmox.ValidationError
	var up *proxmox.UpstreamError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: ve.Msg, Field: optional(ve.Field)})
	case errors.As(err, &ice):
		writeJSON(w, http.StatusConflict, gen.Error{Code: "conflict", Message: ice.Msg, Field: optional("cluster.slug")})
	case errors.As(err, &pve):
		writeError(w, http.StatusBadRequest, "validation", pve.Msg)
	case errors.As(err, &up):
		writeError(w, http.StatusBadGateway, "proxmox", up.Error())
	case errors.Is(err, proxmox.ErrNotFound):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: "Proxmox-koppeling niet gevonden", Field: optional("target.proxmox_id")})
	case errors.Is(err, proxmox.ErrNoMasterKey):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Server) RetryJob(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	j, err := s.jobs.Retry(r.Context(), id, events.User(p.User.ID))
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "taak niet gevonden")
	case errors.Is(err, jobs.ErrNotRetryable), errors.Is(err, jobs.ErrNotFailed), errors.As(err, new(jobs.BusyError)):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case err != nil:
		s.internalError(w, r, err)
	default:
		row, err := s.q.GetJob(r.Context(), j.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		// De taak zoals hij in de wachtrij kwam; de runner kan hem intussen
		// al genomen hebben.
		writeJSON(w, http.StatusAccepted, toAPIJob(j, row.RequestedByName))
	}
}

// ListSpecRevisions toont de gewenste staat van een cluster en haar
// revisies. Ook voor viewers: er staan geen geheimen in.
func (s *Server) ListSpecRevisions(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	h, err := s.deploy.History(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.SpecHistory{
		Template: nullable.NewNullNullable[gen.SpecTemplate](), Revision: h.Revision,
		Params: []gen.SpecParam{}, Notes: h.Notes, Items: []gen.SpecRevision{},
	}
	if t := h.Template; t != nil {
		st := gen.SpecTemplate{Name: t.Name, Version: t.Version, Available: t.Available, Latest: nullable.NewNullNullable[string]()}
		if t.Latest != "" {
			st.Latest = nullable.NewNullableWithValue(t.Latest)
		}
		out.Template = nullable.NewNullableWithValue(st)
	}
	for _, p := range h.Params {
		sp := gen.SpecParam{Name: p.Name, Label: p.Label, Secret: p.Secret, Value: nullable.NewNullNullable[string]()}
		if p.Value != nil {
			sp.Value = nullable.NewNullableWithValue(*p.Value)
		}
		out.Params = append(out.Params, sp)
	}
	// Een commit linkt naar de gekoppelde repository, als die er nog is.
	commitURL := func(string) string { return "" }
	if repo, err := s.q.GetGitRepo(r.Context()); err == nil {
		commitURL = func(sha string) string {
			return gitops.WebURL(repo.ApiUrl) + "/" + repo.Owner + "/" + repo.Name + "/commit/" + sha
		}
	}
	for _, rev := range h.Revisions {
		item := gen.SpecRevision{
			Revision: rev.Revision, Source: gen.SpecRevisionSource(rev.Source), CommitSha: nullableOf(rev.CommitSha), CreatedAt: rev.CreatedAt,
			CreatedBy: nullable.NewNullNullable[gen.AuditRef](), Template: rev.Template, TemplateVersion: rev.TemplateVersion,
			Nodes: rev.Nodes, Changes: []gen.SpecChange{},
		}
		if rev.CommitSha != nil {
			item.CommitUrl = commitURL(*rev.CommitSha)
		}
		// Een verwijderde gebruiker laat created_by leeg.
		if rev.CreatedBy != nil && rev.CreatedByName != nil {
			item.CreatedBy = nullable.NewNullableWithValue(gen.AuditRef{Id: rev.CreatedBy.String(), Name: *rev.CreatedByName})
		}
		for _, c := range rev.Changes {
			item.Changes = append(item.Changes, gen.SpecChange{Label: c.Label, From: c.From, To: c.To})
		}
		out.Items = append(out.Items, item)
	}
	writeJSON(w, http.StatusOK, out)
}
