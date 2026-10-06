package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// GetGitRepo toont de koppeling en de stand van de laatste synchronisatie.
// Ook voor viewers; het token komt nooit terug.
func (s *Server) GetGitRepo(w http.ResponseWriter, r *http.Request) {
	s.writeGitStatus(w, r, http.StatusOK)
}

func (s *Server) writeGitStatus(w http.ResponseWriter, r *http.Request, status int) {
	ctx := r.Context()
	out := gen.GitRepoStatus{Enabled: s.git.Enabled(), IntervalSeconds: int(s.git.Interval.Seconds()), Repo: nullable.NewNullNullable[gen.GitRepo]()}
	repo, err := s.q.GetGitRepo(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		s.internalError(w, r, err)
		return
	default:
		out.Repo = nullable.NewNullableWithValue(toAPIGitRepo(repo))
		n, err := s.q.CountPendingGitChanges(ctx)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out.PendingChanges = int(n)
	}
	writeJSON(w, status, out)
}

func toAPIGitRepo(r store.GitRepo) gen.GitRepo {
	out := gen.GitRepo{
		Id: r.ID, ApiUrl: r.ApiUrl, Repository: r.Owner + "/" + r.Name, Branch: r.Branch, Path: r.Path,
		WebUrl: gitops.WebURL(r.ApiUrl) + "/" + r.Owner + "/" + r.Name, Head: nullable.NewNullNullable[gen.GitCommit](),
		LastSyncAt: nullableOf(r.LastSyncAt), LastError: r.LastError, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if c := gitops.HeadCommit(r); c != nil {
		out.Head = nullable.NewNullableWithValue(toAPIGitCommit(*c))
	}
	return out
}

func toAPIGitCommit(c gitops.Commit) gen.GitCommit {
	return gen.GitCommit{Sha: c.SHA, Message: c.Message, Author: c.Author, Date: c.Date, Verified: c.Verified, Url: c.URL}
}

func gitInput(in gen.GitRepoInput) gitops.RepoInput {
	owner, name := gitops.SplitRepository(in.Repository)
	return gitops.RepoInput{
		APIURL: deref(in.ApiUrl), Owner: owner, Name: name, Branch: deref(in.Branch), Path: deref(in.Path), Token: deref(in.Token),
	}
}

func (s *Server) SaveGitRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.GitRepoInput
	if !decode(w, r, &in) {
		return
	}
	if _, err := s.git.Save(r.Context(), events.User(p.User.ID), gitInput(in)); err != nil {
		s.gitError(w, r, err)
		return
	}
	s.writeGitStatus(w, r, http.StatusOK)
}

func (s *Server) DeleteGitRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.git.Delete(r.Context(), events.User(p.User.ID)); err != nil {
		s.gitError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ProbeGitRepo(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.GitRepoInput
	if !decode(w, r, &in) {
		return
	}
	pr, err := s.git.Probe(r.Context(), gitInput(in))
	if err != nil {
		s.gitError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.GitProbe{Head: toAPIGitCommit(pr.Head), Files: pr.Files})
}

// SyncGit stoot de lus aan. Het lezen zelf gebeurt op de achtergrond; de
// events en de pagina tonen de uitkomst.
func (s *Server) SyncGit(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if _, err := s.q.GetGitRepo(r.Context()); err != nil {
		s.gitError(w, r, err)
		return
	}
	s.git.Kick(true)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) ListGitFiles(w http.ResponseWriter, r *http.Request) {
	items := []gen.GitFile{}
	repo, err := s.q.GetGitRepo(r.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, list[gen.GitFile]{items})
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, f := range gitops.Scan(repo) {
		errs := make([]gen.GitFieldError, 0, len(f.Errors))
		for _, e := range f.Errors {
			errs = append(errs, gen.GitFieldError{Field: e.Field, Line: e.Line, Message: e.Message})
		}
		items = append(items, gen.GitFile{
			Path: f.Path, Slug: f.Slug, State: gen.GitFileState(f.State), ClusterId: nullableOf(f.ClusterID),
			ClusterName: f.ClusterName, Errors: errs, ChangeId: nullableOf(f.ChangeID), Commit: f.Commit, Secret: f.Secret,
			Url: gitops.FileURL(repo.ApiUrl, repo.Owner, repo.Name, repo.Branch, f.Path),
		})
	}
	writeJSON(w, http.StatusOK, list[gen.GitFile]{items})
}

func (s *Server) ListGitChanges(w http.ResponseWriter, r *http.Request, params gen.ListGitChangesParams) {
	arg := store.ListGitChangesParams{ClusterID: params.ClusterId, Max: 50}
	if params.Status != nil {
		st := string(*params.Status)
		arg.Status = &st
	}
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 200 {
			writeError(w, http.StatusBadRequest, "validation", "limit moet tussen 1 en 200 liggen")
			return
		}
		arg.Max = int32(*params.Limit)
	}
	rows, err := s.q.ListGitChanges(r.Context(), arg)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.GitChange, 0, len(rows))
	for _, row := range rows {
		c, _, err := toAPIGitChange(store.GetGitChangeRow(row))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		items = append(items, c)
	}
	writeJSON(w, http.StatusOK, list[gen.GitChange]{items})
}

// toAPIGitChange zet een rij om; het plan komt mee voor de detailpagina.
func toAPIGitChange(row store.GetGitChangeRow) (gen.GitChange, gitops.Plan, error) {
	var plan gitops.Plan
	if err := json.Unmarshal(row.Plan, &plan); err != nil {
		return gen.GitChange{}, plan, fmt.Errorf("plan van wijziging %s: %w", row.ID, err)
	}
	var meta gitops.Metadata
	_ = json.Unmarshal(row.Metadata, &meta)
	c := gen.GitChange{
		Id: row.ID, ClusterId: nullableOf(row.ClusterID), ClusterName: meta.Name, ClusterEnvironment: gen.Environment(meta.Environment),
		Slug: row.Slug, Path: row.Path, Kind: gen.GitChangeKind(row.Kind), Status: gen.GitChangeStatus(row.Status),
		Commit: gen.GitCommit{
			Sha: row.CommitSha, Message: row.CommitMessage, Author: row.CommitAuthor, Verified: row.CommitVerified, Url: row.CommitUrl,
			Date: deref(row.CommittedAt),
		},
		BaseRevision: int(row.BaseRevision), Summary: plan.Summary, JobId: nullableOf(row.JobID),
		DecidedBy: nullableOf(row.DecidedByName), DecidedAt: nullableOf(row.DecidedAt), Reason: row.Reason,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.ClusterName != nil {
		c.ClusterName = *row.ClusterName
	}
	if row.ClusterEnvironment.Valid {
		c.ClusterEnvironment = gen.Environment(row.ClusterEnvironment.Environment)
	}
	return c, plan, nil
}

func (s *Server) GetGitChange(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	row, err := s.q.GetGitChange(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "wijziging niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	c, plan, err := toAPIGitChange(row)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	local, err := s.git.Local(ctx, store.GitChange{ID: row.ID, ClusterID: row.ClusterID, Status: row.Status}, plan)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// Het plan gaat via JSON naar het API-type; de velden heten gelijk.
	var p gen.GitPlan
	if err := json.Unmarshal(row.Plan, &p); err != nil {
		s.internalError(w, r, err)
		return
	}
	fileURL := ""
	if repo, err := s.q.GetGitRepo(ctx); err == nil && repo.ID == row.RepoID {
		fileURL = gitops.FileURL(repo.ApiUrl, repo.Owner, repo.Name, repo.Branch, row.Path)
	}
	writeJSON(w, http.StatusOK, gen.GitChangeDetail{
		Id: c.Id, ClusterId: c.ClusterId, ClusterName: c.ClusterName, ClusterEnvironment: c.ClusterEnvironment,
		Slug: c.Slug, Path: c.Path, Kind: gen.GitChangeDetailKind(c.Kind), Status: c.Status, Commit: c.Commit,
		BaseRevision: c.BaseRevision, Summary: c.Summary, JobId: c.JobId, DecidedBy: c.DecidedBy, DecidedAt: c.DecidedAt,
		Reason: c.Reason, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		Plan: p, Local: local, FileUrl: fileURL,
	})
}

// ExportClusterGit geeft cluster.yaml als download. Ook voor viewers: er
// staan geen geheimen in.
func (s *Server) ExportClusterGit(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	name, data, err := s.git.Export(r.Context(), id)
	if err != nil {
		s.gitError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	_, _ = w.Write(data)
}

func (s *Server) LinkClusterGit(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.git.Link(r.Context(), events.User(p.User.ID), id); err != nil {
		s.gitError(w, r, err)
		return
	}
	s.writeCluster(w, r, id)
}

func (s *Server) UnlinkClusterGit(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.git.Unlink(r.Context(), events.User(p.User.ID), id); err != nil {
		s.gitError(w, r, err)
		return
	}
	s.writeCluster(w, r, id)
}

func (s *Server) writeCluster(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	c, err := s.q.GetCluster(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPICluster(c))
}

func (s *Server) gitError(w http.ResponseWriter, r *http.Request, err error) {
	var ve gitops.ValidationError
	var ce *gitops.ConflictError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: ve.Msg, Field: optional(ve.Field)})
	case errors.As(err, &ce):
		writeJSON(w, http.StatusConflict, gen.Error{Code: ce.Code, Message: ce.Msg, Diff: optional(ce.Diff)})
	case errors.Is(err, gitops.ErrNoMasterKey):
		writeError(w, http.StatusBadRequest, "no_master_key", err.Error())
	case errors.Is(err, gitops.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	default:
		s.internalError(w, r, err)
	}
}
