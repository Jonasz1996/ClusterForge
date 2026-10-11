package httpapi

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/docs"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
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

// GetDocsTarget toont de GitHub-koppeling van de documentatie. Ook voor
// viewers; het token komt nooit terug.
func (s *Server) GetDocsTarget(w http.ResponseWriter, r *http.Request) {
	s.writeDocsTarget(w, r)
}

func (s *Server) writeDocsTarget(w http.ResponseWriter, r *http.Request) {
	out := gen.DocsTargetStatus{Enabled: s.docs.Enabled(), Target: nullable.NewNullNullable[gen.DocsTarget]()}
	t, err := s.docs.Target(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if t != nil {
		out.Target = nullable.NewNullableWithValue(toAPIDocsTarget(*t))
		out.Pending = s.docs.Pending() && t.Enabled
	}
	writeJSON(w, http.StatusOK, out)
}

func toAPIDocsTarget(t store.DocsTarget) gen.DocsTarget {
	out := gen.DocsTarget{
		ApiUrl: t.ApiUrl, Repository: t.Owner + "/" + t.Name, Branch: t.Branch, Path: t.Path, WebUrl: docs.RepoURL(t),
		FolderUrl: docs.FolderURL(t), Enabled: t.Enabled, LastCommit: nullable.NewNullNullable[gen.DocsCommit](),
		LastCheckedAt: nullableOf(t.LastCheckedAt), LastAttemptAt: nullableOf(t.LastAttemptAt), LastError: t.LastError,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	if t.LastCommitSha != "" && t.LastPublishedAt != nil {
		out.LastCommit = nullable.NewNullableWithValue(gen.DocsCommit{
			Sha: t.LastCommitSha, Url: t.LastCommitUrl, At: *t.LastPublishedAt, Files: int(t.LastCommitFiles),
		})
	}
	return out
}

func docsTargetInput(in gen.DocsTargetInput) docs.TargetInput {
	owner, name := gitops.SplitRepository(in.Repository)
	return docs.TargetInput{
		APIURL: deref(in.ApiUrl), Owner: owner, Name: name, Branch: deref(in.Branch), Path: in.Path, Token: deref(in.Token),
		Enabled: in.Enabled,
	}
}

func (s *Server) SaveDocsTarget(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.DocsTargetInput
	if !decode(w, r, &in) {
		return
	}
	if _, err := s.docs.SaveTarget(r.Context(), events.User(p.User.ID), docsTargetInput(in)); err != nil {
		s.docsTargetError(w, r, err)
		return
	}
	s.writeDocsTarget(w, r)
}

func (s *Server) DeleteDocsTarget(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.docs.DeleteTarget(r.Context(), events.User(p.User.ID)); err != nil {
		s.docsTargetError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) TestDocsTarget(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.DocsTargetInput
	if !decode(w, r, &in) {
		return
	}
	pr, err := s.docs.TestTarget(r.Context(), docsTargetInput(in))
	if err != nil {
		s.docsTargetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.DocsTargetProbe{BranchExists: pr.BranchExists, Head: pr.Head, Files: pr.Files})
}

// PublishDocs schrijft de documentatie nu naar GitHub en wacht op de
// uitkomst.
func (s *Server) PublishDocs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	res, err := s.docs.Publish(r.Context(), docs.PublishManual)
	if err != nil {
		s.docsTargetError(w, r, err)
		return
	}
	out := gen.DocsPublishResult{Commit: nullable.NewNullNullable[gen.DocsCommit](), Files: []gen.DocsFileChange{}}
	if res.SHA != "" {
		out.Commit = nullable.NewNullableWithValue(gen.DocsCommit{Sha: res.SHA, Url: res.URL, At: s.docs.Now(), Files: len(res.Changes)})
	}
	for _, c := range res.Changes {
		out.Files = append(out.Files, gen.DocsFileChange{Path: c.Path, Change: gen.DocsFileChangeChange(c.Kind)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) docsTargetError(w http.ResponseWriter, r *http.Request, err error) {
	var ve docs.ValidationError
	var pe *docs.PublishError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: ve.Msg, Field: optional(ve.Field)})
	case errors.As(err, &pe):
		writeError(w, http.StatusConflict, "publish_failed", pe.Msg)
	case errors.Is(err, docs.ErrNoMasterKey):
		writeError(w, http.StatusBadRequest, "no_master_key", err.Error())
	case errors.Is(err, docs.ErrBusy):
		writeError(w, http.StatusConflict, "busy", err.Error())
	case errors.Is(err, docs.ErrNoTarget), errors.Is(err, docs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", docs.ErrNoTarget.Error())
	default:
		s.internalError(w, r, err)
	}
}
