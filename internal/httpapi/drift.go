package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

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
		out.Source = nullable.NewNullableWithValue(gen.DriftSource{
			Kind: gen.DriftSourceKind(src.Kind), Template: src.Template, TemplateVersion: src.Version, SpecRevision: src.Revision,
		})
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
				Fingerprint: f.Fingerprint,
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
	case row.Cluster.TemplateName == nil:
	case row.DriftNodes > 0:
		out.Status = gen.ClusterDriftSummaryStatusDrift
	case row.DriftUnknown > 0 || checked == nil || now.Sub(*checked) > driftStale:
		out.Status = gen.ClusterDriftSummaryStatusUnknown
	default:
		out.Status = gen.ClusterDriftSummaryStatusInSync
	}
	return out
}
