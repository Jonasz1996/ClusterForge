package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) GetClusterDrift(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.writeDrift(w, r, id, nil)
}

func (s *Server) CheckClusterDrift(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if err := s.drift.CheckNow(r.Context(), id, nil); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeDrift(w, r, id, nil)
}

func (s *Server) GetNodeDrift(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ref, ok := s.nodeCluster(w, r, id)
	if !ok {
		return
	}
	if ref == nil {
		writeJSON(w, http.StatusOK, emptyDrift())
		return
	}
	s.writeDrift(w, r, *ref, &id)
}

func (s *Server) CheckNodeDrift(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	ref, ok := s.nodeCluster(w, r, id)
	if !ok {
		return
	}
	if ref == nil {
		writeJSON(w, http.StatusOK, emptyDrift())
		return
	}
	if err := s.drift.CheckNow(r.Context(), *ref, &id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeDrift(w, r, *ref, &id)
}

// nodeCluster geeft het cluster van een node, of nil zonder cluster.
func (s *Server) nodeCluster(w http.ResponseWriter, r *http.Request, id uuid.UUID) (*uuid.UUID, bool) {
	ref, err := s.q.GetNodeRef(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return ref.ClusterID, true
}

func emptyDrift() gen.DriftReport {
	return gen.DriftReport{Source: nullable.NewNullNullable[gen.DriftSource](), Notes: []string{}, Nodes: []gen.DriftNode{}}
}

func (s *Server) writeDrift(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, nodeID *uuid.UUID) {
	rep, err := s.drift.Report(r.Context(), clusterID, nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := emptyDrift()
	out.Notes = rep.Notes
	if src := rep.Source; src != nil {
		ds := gen.DriftSource{
			Kind: gen.DriftSourceKind(src.Kind), Template: src.Template, TemplateVersion: src.Version, SpecRevision: src.Revision,
			BaselineAt: nullableOf(src.BaselineAt), Items: nullable.NewNullNullable[gen.BaselineItems](),
		}
		if it := src.Items; it != nil {
			ds.Items = nullable.NewNullableWithValue(gen.BaselineItems{Packages: it.Packages, Services: it.Services, Files: it.Files})
		}
		out.Source = nullable.NewNullableWithValue(ds)
	}
	for _, n := range rep.Nodes {
		dn := gen.DriftNode{
			NodeId: n.NodeID, Hostname: n.Hostname, Status: gen.DriftNodeStatus(n.Status),
			CheckedAt: nullableOf(n.CheckedAt), DriftSince: nullableOf(n.DriftSince), SpecRevision: n.SpecRevision,
			Findings: make([]gen.DriftFinding, 0, len(n.Findings)), Unchecked: make([]gen.DriftUnchecked, 0, len(n.Unchecked)),
			Error: n.Error, Skipped: n.Skipped, AgentTooOld: n.AgentTooOld,
		}
		for _, f := range n.Findings {
			dn.Findings = append(dn.Findings, gen.DriftFinding{
				Key: f.Key, Step: f.Step, Kind: gen.DriftFindingKind(f.Kind), Title: f.Title, Aspect: gen.DriftFindingAspect(f.Aspect),
				Expected: f.Expected, Actual: f.Actual, Detail: f.Detail, Mtime: nullableOf(f.ModTime), Since: f.Since,
				Fingerprint: f.Fingerprint, Ignored: f.Ignored, IgnoreId: nullableOf(f.IgnoreID),
			})
		}
		for _, u := range n.Unchecked {
			dn.Unchecked = append(dn.Unchecked, gen.DriftUnchecked{Step: u.Step, Title: u.Title, Reason: u.Reason})
		}
		out.Nodes = append(out.Nodes, dn)
	}
	writeJSON(w, http.StatusOK, out)
}

// driftStale is hoe oud de oudste controle mag zijn voor de badge nog iets
// zegt.
const driftStale = time.Hour

// clusterDrift vat de drift van een cluster samen voor de lijst.
func clusterDrift(row store.ListClustersRow, now time.Time) gen.ClusterDriftSummary {
	// min() over geen rijen is NULL; sqlc kent daar geen type voor.
	var checked *time.Time
	if t, ok := row.DriftCheckedAt.(time.Time); ok {
		checked = &t
	}
	out := gen.ClusterDriftSummary{
		Status: gen.ClusterDriftSummaryStatusNone, NodesWithDrift: int(row.DriftNodes), CheckedAt: nullableOf(checked),
	}
	switch {
	case row.Cluster.TemplateName == nil && !hasBaseline(row.Cluster.Spec):
	case row.DriftNodes > 0:
		out.Status = gen.ClusterDriftSummaryStatusDrift
	case row.DriftUnknown > 0 || checked == nil || now.Sub(*checked) > driftStale:
		out.Status = gen.ClusterDriftSummaryStatusUnknown
	default:
		out.Status = gen.ClusterDriftSummaryStatusInSync
	}
	return out
}

func hasBaseline(spec []byte) bool {
	b, err := drift.ParseBaseline(spec)
	return err == nil && b != nil
}

func (s *Server) CaptureBaseline(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.BaselineInput
	if !decode(w, r, &req) {
		return
	}
	res, err := s.drift.Capture(r.Context(), events.User(p.User.ID), id, drift.CaptureInput{
		NodeIDs: req.NodeIds, Preview: req.Preview != nil && *req.Preview,
		Items: drift.BaselineItems{Packages: req.Packages, Services: req.Services, Files: req.Files},
	})
	if s.driftError(w, r, err) {
		return
	}
	out := gen.BaselineResult{Revision: res.Revision, Nodes: make([]gen.BaselineNode, 0, len(res.Nodes))}
	for _, n := range res.Nodes {
		items := make([]gen.BaselineItem, 0, len(n.Expect))
		for _, e := range n.Expect {
			items = append(items, gen.BaselineItem{
				Kind: gen.BaselineItemKind(e.Kind), Name: e.Name, Version: e.Version,
				Enabled: nullableOf(e.Enabled), Active: nullableOf(e.Active),
				Mode: e.Mode, Owner: e.Owner, Group: e.Group, Size: e.Size, Content: e.ContentHMAC != "",
			})
		}
		out.Nodes = append(out.Nodes, gen.BaselineNode{Hostname: n.Hostname, Items: items, NodeId: n.NodeID, Notes: n.Notes})
	}
	writeJSON(w, http.StatusOK, out)
}

// driftError schrijft de passende foutrespons en geeft true als err niet
// nil was.
func (s *Server) driftError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var fe *drift.FieldError
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: fe.Message, Field: optional(fe.Field)})
	case errors.Is(err, drift.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	case errors.Is(err, drift.ErrTemplateCluster):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		s.internalError(w, r, err)
	}
	return true
}

func (s *Server) ListDriftIgnores(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, err := s.q.GetCluster(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.q.ListDriftIgnores(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := time.Now()
	out := gen.DriftIgnoreList{Items: make([]gen.DriftIgnore, 0, len(rows))}
	for _, i := range rows {
		out.Items = append(out.Items, toAPIIgnore(store.DriftIgnore{
			ID: i.ID, ClusterID: i.ClusterID, NodeID: i.NodeID, Key: i.Key, Reason: i.Reason, ExpiresAt: i.ExpiresAt,
			CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt,
		}, i.NodeHostname, i.CreatedByName, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func toAPIIgnore(i store.DriftIgnore, hostname, by *string, now time.Time) gen.DriftIgnore {
	out := gen.DriftIgnore{
		Id: i.ID, NodeId: nullableOf(i.NodeID), Hostname: nullableOf(hostname), Key: i.Key, Reason: i.Reason,
		ExpiresAt: nullableOf(i.ExpiresAt), Expired: i.ExpiresAt != nil && !i.ExpiresAt.After(now),
		CreatedBy: nullable.NewNullNullable[gen.AuditRef](), CreatedAt: i.CreatedAt,
	}
	if i.CreatedBy != nil {
		ref := gen.AuditRef{Id: i.CreatedBy.String(), Deleted: by == nil}
		if by != nil {
			ref.Name = *by
		}
		out.CreatedBy = nullable.NewNullableWithValue(ref)
	}
	return out
}

func (s *Server) CreateDriftIgnore(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.DriftIgnoreInput
	if !decode(w, r, &req) {
		return
	}
	in := drift.IgnoreInput{Key: req.Key, Reason: req.Reason}
	if v, err := req.NodeId.Get(); err == nil {
		in.NodeID = &v
	}
	if v, err := req.ExpiresAt.Get(); err == nil {
		in.ExpiresAt = &v
	}
	ig, err := s.drift.AddIgnore(r.Context(), events.User(p.User.ID), id, in)
	if s.driftError(w, r, err) {
		return
	}
	var hostname *string
	if ig.NodeID != nil {
		if ref, err := s.q.GetNodeRef(r.Context(), *ig.NodeID); err == nil {
			hostname = &ref.Hostname
		}
	}
	name := p.User.Username
	writeJSON(w, http.StatusCreated, toAPIIgnore(ig, hostname, &name, time.Now()))
}

func (s *Server) DeleteDriftIgnore(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.driftError(w, r, s.drift.RemoveIgnore(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
