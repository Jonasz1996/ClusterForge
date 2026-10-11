package httpapi

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/docs"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

func docMeta(m docs.Meta) gen.DocMeta {
	return gen.DocMeta{
		Scope: gen.DocMetaScope(m.Scope), Kind: m.Kind, Title: m.Title, Path: m.Path, ClusterId: m.ClusterID,
		Version: m.Version, GeneratedAt: m.GeneratedAt,
	}
}

func (s *Server) ListDocs(w http.ResponseWriter, r *http.Request) {
	idx, err := s.docs.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.DocIndex{Overview: docMeta(idx.Overview), Clusters: []gen.DocClusterIndex{}, QueuedUntil: idx.Queued}
	for _, c := range idx.Clusters {
		ci := gen.DocClusterIndex{
			Id: c.ID, Name: c.Name, Slug: c.Slug, Environment: c.Env, Docs: []gen.DocMeta{}, ChangedAt: c.Changed, QueuedUntil: c.Queued,
		}
		for _, m := range c.Docs {
			ci.Docs = append(ci.Docs, docMeta(m))
		}
		out.Clusters = append(out.Clusters, ci)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) DownloadDocs(w http.ResponseWriter, r *http.Request) {
	// Eerst in het geheugen, zodat een fout nog een foutmelding kan zijn.
	var buf bytes.Buffer
	if err := s.docs.Zip(r.Context(), &buf); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="clusterforge-documentatie.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) GenerateDocs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	n, err := s.docs.GenerateAll(r.Context(), docs.CauseManual)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.DocGenerateResult{Changed: n})
}

// docError schrijft 404 voor een document dat er niet is.
func (s *Server) docError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, docs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "document niet gevonden")
		return
	}
	s.internalError(w, r, err)
}

// docTarget controleert scope, kind en cluster samen.
func docTarget(w http.ResponseWriter, scope, kind string, cluster *uuid.UUID) bool {
	if !docs.Valid(scope, kind) {
		writeError(w, http.StatusNotFound, "not_found", "document niet gevonden")
		return false
	}
	if (scope == docs.ScopeCluster) != (cluster != nil) {
		writeError(w, http.StatusBadRequest, "bad_request", "cluster is verplicht bij scope cluster en kan alleen daar")
		return false
	}
	return true
}

func (s *Server) GetDoc(w http.ResponseWriter, r *http.Request, scope gen.DocScopeParam, kind gen.DocKindParam, params gen.GetDocParams) {
	if !docTarget(w, scope, kind, params.Cluster) {
		return
	}
	d, err := s.docs.Get(r.Context(), scope, kind, params.Cluster, params.Version)
	if err != nil {
		s.docError(w, r, err)
		return
	}
	out := gen.Doc{
		Scope: gen.DocScope(d.Scope), Kind: d.Kind, Title: d.Title, Path: d.Path, ClusterId: d.ClusterID, Version: d.Version,
		GeneratedAt: d.GeneratedAt, Content: d.Content, Sha256: d.Sha256, Cause: d.Cause, CauseText: docs.CauseText(d.Cause),
	}
	if d.ClusterName != "" {
		out.ClusterName = &d.ClusterName
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetDocHistory(w http.ResponseWriter, r *http.Request, scope gen.DocScopeParam, kind gen.DocKindParam, params gen.GetDocHistoryParams) {
	if !docTarget(w, scope, kind, params.Cluster) {
		return
	}
	vs, err := s.docs.History(r.Context(), scope, kind, params.Cluster)
	if err != nil {
		s.docError(w, r, err)
		return
	}
	out := gen.DocHistory{Versions: []gen.DocVersion{}, Keep: docs.Keep}
	for _, v := range vs {
		out.Versions = append(out.Versions, gen.DocVersion{
			Id: v.ID, GeneratedAt: v.GeneratedAt, Sha256: v.Sha256, Cause: v.Cause, CauseText: docs.CauseText(v.Cause),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetDocDiff(w http.ResponseWriter, r *http.Request, scope gen.DocScopeParam, kind gen.DocKindParam, params gen.GetDocDiffParams) {
	if !docTarget(w, scope, kind, params.Cluster) {
		return
	}
	diff, changed, err := s.docs.Diff(r.Context(), scope, kind, params.Cluster, params.From, params.To)
	if err != nil {
		s.docError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.DocDiff{From: params.From, To: params.To, Diff: diff, Changed: changed})
}
