package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
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
	}
	for _, p := range t.Params {
		out.Params = append(out.Params, gen.TemplateParam{
			Name: p.Name, Type: gen.TemplateParamType(p.Type), Label: p.Label, Help: p.Help,
			Optional: p.Optional || p.Type == "secret", Default: str(p.Default), Min: str(p.Min), Max: str(p.Max),
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
	case errors.Is(err, jobs.ErrNotRetryable), errors.Is(err, jobs.ErrNotFailed):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case err != nil:
		s.internalError(w, r, err)
	default:
		row, err := s.q.GetJob(r.Context(), j.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, toAPIJob(row.Job, row.RequestedByName))
	}
}
