package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Sync leest de kop van de branch en evalueert elk clusterbestand. Zonder
// nieuwe commit antwoordt GitHub met 304 en komen de bestanden uit de cache;
// de evaluatie loopt toch, want een cluster kan intussen gekoppeld of
// gewijzigd zijn. Er verandert niets op een node.
func (s *Service) Sync(ctx context.Context) error {
	if s.box == nil {
		return nil
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&locked); err != nil || !locked {
		return err
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, "SELECT pg_advisory_unlock($1)", lockID)
	}()
	repo, err := s.current(ctx)
	if err != nil || repo == nil {
		return err
	}
	force := s.force.Swap(false)
	if force {
		s.mu.Lock()
		s.l2 = map[string][]FieldError{}
		s.mu.Unlock()
	}
	if err := s.sync(ctx, *repo, force); err != nil {
		s.failed(ctx, *repo, err)
		return err
	}
	return nil
}

func (s *Service) sync(ctx context.Context, repo store.GitRepo, force bool) error {
	token, err := s.box.Open(repo.TokenEnc, aad(repo.ID), repo.KeyID)
	if err != nil {
		return errors.New("het token is niet te ontsleutelen; is CF_MASTER_KEY veranderd? Sla het token opnieuw op")
	}
	src, err := s.NewSource(Config{APIURL: repo.ApiUrl, Owner: repo.Owner, Name: repo.Name, Branch: repo.Branch, Token: string(token)})
	if err != nil {
		return err
	}
	sha, etag, err := src.Head(ctx, repo.HeadEtag)
	if errors.Is(err, ErrNotModified) {
		sha, etag = repo.HeadSha, repo.HeadEtag
	} else if err != nil {
		return err
	}
	head := HeadCommit(repo)
	if sha != repo.HeadSha || head == nil || head.Tree == "" {
		c, err := src.Commit(ctx, sha)
		if err != nil {
			return err
		}
		head = &c
		raw, _ := json.Marshal(c)
		if err := s.q.SetGitHead(ctx, store.SetGitHeadParams{ID: repo.ID, HeadSha: sha, HeadEtag: etag, HeadCommit: raw}); err != nil {
			return err
		}
		if sha != repo.HeadSha {
			err := s.ev.Write(ctx, nil, events.Event{
				Actor: events.System(), SubjectType: "git_repo", SubjectID: repo.ID.String(), Action: "gitops.commit_seen",
				Payload: map[string]any{"repo": repo.Owner + "/" + repo.Name, "sha": sha, "message": c.Title(), "author": c.Author, "verified": c.Verified},
			})
			if err != nil {
				return err
			}
		}
	} else if etag != repo.HeadEtag {
		if err := s.q.SetGitHead(ctx, store.SetGitHeadParams{ID: repo.ID, HeadSha: sha, HeadEtag: etag, HeadCommit: repo.HeadCommit}); err != nil {
			return err
		}
	}

	s.mu.Lock()
	tree, have := s.tree, s.treeOf == sha && !force
	s.mu.Unlock()
	if !have {
		if tree, err = src.Tree(ctx, head.Tree); err != nil {
			return err
		}
	}
	files := clusterFiles(tree, repo.Path)
	if len(files) > MaxFiles {
		return fmt.Errorf("er staan %d clusterbestanden onder %s/; ClusterForge leest er hoogstens %d", len(files), repo.Path, MaxFiles)
	}
	blobs := map[string][]byte{}
	tooBig := map[string]bool{}
	for _, f := range files {
		if f.Size > MaxFileSize {
			tooBig[f.SHA] = true
			continue
		}
		s.mu.Lock()
		b, ok := s.blobs[f.SHA]
		s.mu.Unlock()
		if !ok {
			if b, err = src.Blob(ctx, f.SHA); err != nil {
				return err
			}
		}
		blobs[f.SHA] = b
	}
	s.mu.Lock()
	s.treeOf, s.tree, s.blobs = sha, tree, blobs
	s.mu.Unlock()
	return s.evaluate(ctx, repo, *head, files, blobs, tooBig)
}

// failed bewaart de fout; alleen bij de overgang komt er een event.
func (s *Service) failed(ctx context.Context, repo store.GitRepo, err error) {
	if ctx.Err() != nil {
		return
	}
	msg := err.Error()
	now := time.Now()
	if e := s.q.SetGitError(ctx, store.SetGitErrorParams{ID: repo.ID, LastError: msg, LastSyncAt: &now}); e != nil {
		s.log.Warn("gitops: fout bewaren mislukt", "err", e)
		return
	}
	if repo.LastError == "" {
		_ = s.ev.Write(ctx, nil, events.Event{
			Actor: events.System(), SubjectType: "git_repo", SubjectID: repo.ID.String(), Action: "gitops.sync_failed",
			Payload: map[string]any{"repo": repo.Owner + "/" + repo.Name, "error": msg},
		})
	}
}

// outcome is wat de evaluatie van één bestand wil vastleggen.
type outcome struct {
	entry FileEntry
	// plan is een nieuw plan om als wachtende wijziging op te slaan.
	plan     *Plan
	spec     deploy.Spec
	meta     Metadata
	kind     string
	cluster  *uuid.UUID
	revision int
	// supersede zegt waarom een wachtende wijziging voor deze slug
	// vervalt; leeg laat haar staan.
	supersede string
}

// evaluate beoordeelt elk bestand en legt de uitkomst in één transactie
// vast: de scan, nieuwe en vervallen wijzigingen en hun events.
func (s *Service) evaluate(ctx context.Context, repo store.GitRepo, head Commit, files []TreeEntry, blobs map[string][]byte, tooBig map[string]bool) error {
	rows, err := s.q.ListGitClusters(ctx)
	if err != nil {
		return err
	}
	clusters := map[string]store.ListGitClustersRow{}
	for _, r := range rows {
		clusters[r.Slug] = r
	}
	pendingRows, err := s.q.ListPendingGitChanges(ctx)
	if err != nil {
		return err
	}
	pending := map[string]store.GitChange{}
	for _, p := range pendingRows {
		pending[p.Slug] = p
	}
	latestRows, err := s.q.ListLatestGitChanges(ctx)
	if err != nil {
		return err
	}
	latest := map[string]store.GitChange{}
	for _, l := range latestRows {
		latest[l.Slug] = l
	}
	prev := map[string]FileEntry{}
	for _, e := range Scan(repo) {
		prev[e.Path] = e
	}

	var out []outcome
	seen := map[string]bool{}
	for _, f := range files {
		slug := slugOf(repo.Path, f.Path)
		seen[slug] = true
		o, err := s.evaluateFile(ctx, repo, head, f, slug, blobs[f.SHA], tooBig[f.SHA], clusters, pending, latest[slug], prev[f.Path])
		if err != nil {
			return err
		}
		out = append(out, o)
	}
	for _, r := range rows {
		if r.GitRepoID == nil || *r.GitRepoID != repo.ID || seen[r.Slug] {
			continue
		}
		id := r.ID
		o := outcome{entry: FileEntry{Path: filePath(repo.Path, r.Slug), Slug: r.Slug, State: "missing", ClusterID: &id, ClusterName: r.Name,
			Errors: []FieldError{}}, supersede: "het bestand staat niet meer in Git"}
		out = append(out, o)
	}

	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockGitRepo(ctx)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && cur.ID != repo.ID {
			// De koppeling verdween of veranderde intussen.
			return nil
		}
		if err != nil {
			return err
		}
		entries := make([]FileEntry, 0, len(out))
		for _, o := range out {
			e, err := s.record(ctx, q, repo, head, o, pending[o.entry.Slug], prev[o.entry.Path])
			if err != nil {
				return err
			}
			entries = append(entries, e)
		}
		raw, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		now := time.Now()
		if err := q.SetGitScan(ctx, store.SetGitScanParams{ID: repo.ID, SyncedSha: head.SHA, Scan: raw, LastSyncAt: &now}); err != nil {
			return err
		}
		if cur.LastError != "" {
			return s.ev.Write(ctx, q, events.Event{
				Actor: events.System(), SubjectType: "git_repo", SubjectID: repo.ID.String(), Action: "gitops.sync_recovered",
				Payload: map[string]any{"repo": repo.Owner + "/" + repo.Name, "sha": head.SHA},
			})
		}
		return nil
	})
}

// evaluateFile leest, controleert en plant één bestand. Een plan komt er
// alleen als er nog geen wachtende wijziging is voor deze inhoud en deze
// revisie van het cluster, en als die inhoud niet is afgewezen. latest is
// de nieuwste wijziging voor deze slug.
func (s *Service) evaluateFile(ctx context.Context, repo store.GitRepo, head Commit, f TreeEntry, slug string, data []byte, tooBig bool,
	clusters map[string]store.ListGitClustersRow, pending map[string]store.GitChange, latest store.GitChange, prev FileEntry) (outcome, error) {
	o := outcome{entry: FileEntry{Path: f.Path, Slug: slug, BlobSHA: f.SHA, Errors: []FieldError{}, Commit: head.SHA}}
	if prev.BlobSHA == f.SHA && prev.Commit != "" {
		o.entry.Commit = prev.Commit
	}
	row, exists := clusters[slug]
	var c *Cluster
	if exists {
		id := row.ID
		o.entry.ClusterID, o.entry.ClusterName = &id, row.Name
		c = s.clusterOf(ctx, row)
	}
	linked := exists && row.GitRepoID != nil && *row.GitRepoID == repo.ID
	p, hasPending := pending[slug]

	invalid := func(errs []FieldError, secret bool) (outcome, error) {
		o.entry.State, o.entry.Errors = "invalid", errs
		if secret {
			o.entry.Secret, o.entry.BlobSHA = true, ""
		}
		if hasPending {
			o.supersede = "het bestand is ongeldig in commit " + short(head.SHA)
		}
		return o, nil
	}
	if tooBig {
		return invalid([]FieldError{{Field: "bestand", Message: fmt.Sprintf("het bestand is groter dan %d KB", MaxFileSize>>10)}}, false)
	}
	file, errs := Parse(data)
	if len(errs) > 0 {
		return invalid(errs, false)
	}
	ch, errs, secret := Check(file, slug, c, s.reg())
	if len(errs) > 0 {
		return invalid(errs, secret)
	}
	if exists && !linked {
		// ClusterForge negeert het bestand tot het cluster gekoppeld is; of
		// het gelijk is aan de export, bepaalt Link.
		o.entry.State = "unlinked"
		if hasPending {
			o.supersede = "het cluster is niet aan Git gekoppeld"
		}
		return o, nil
	}

	if linked && ch.Same(c) {
		// Gelijk aan de spec; loopt de toepassing nog of mislukte ze, dan
		// staat dat erbij.
		o.entry.State = "in_sync"
		switch {
		case latest.Status == "applying":
			o.entry.State = "applying"
		case c.Applied < c.Revision:
			o.entry.State = "not_applied"
		}
		if o.entry.State != "in_sync" && latest.ID != uuid.Nil && latest.Revision != nil && int(*latest.Revision) == c.Revision {
			id := latest.ID
			o.entry.ChangeID = &id
		}
		if hasPending {
			o.supersede = "het bestand is weer gelijk aan het cluster"
		}
		return o, nil
	}
	revision := 0
	if linked {
		revision = c.Revision
	}
	if hasPending && p.BlobSha == f.SHA && int(p.BaseRevision) == revision && (p.ClusterID == nil) == !linked {
		id := p.ID
		o.entry.ChangeID = &id
		o.entry.State = map[bool]string{true: "pending", false: "new"}[linked]
		o.entry.Commit = p.CommitSha
		return o, nil
	}
	// Afgewezen blijft afgewezen tot een nieuwe commit het bestand wijzigt.
	if !hasPending && latest.Status == "rejected" && latest.BlobSha == f.SHA && int(latest.BaseRevision) == revision &&
		latest.CommitSha == o.entry.Commit && (latest.ClusterID == nil) == !linked {
		id := latest.ID
		o.entry.ChangeID = &id
		o.entry.State = "rejected"
		return o, nil
	}
	key := fmt.Sprintf("%s@%d", f.SHA, revision)
	s.mu.Lock()
	cached, known := s.l2[key]
	s.mu.Unlock()
	if known {
		return invalid(cached, false)
	}
	var plan *Plan
	var spec deploy.Spec
	var target deploy.Target
	var err error
	if linked {
		plan, spec, errs, err = s.planUpdate(ctx, c, ch, head.SHA)
	} else {
		plan, target, errs, err = s.planCreate(ctx, ch)
	}
	if err != nil {
		return o, err
	}
	if len(errs) > 0 {
		s.mu.Lock()
		s.l2[key] = errs
		s.mu.Unlock()
		return invalid(errs, false)
	}
	o.plan, o.spec, o.meta, o.revision = plan, spec, ch.Metadata, revision
	o.kind = map[bool]string{true: "update", false: "create"}[linked]
	if linked {
		o.cluster = o.entry.ClusterID
		o.entry.State = "pending"
	} else {
		o.entry.State, o.spec = "new", newSpec(ch, target)
	}
	if hasPending {
		o.supersede = "een nieuwere commit " + short(head.SHA) + " wijzigt het bestand"
		if int(p.BaseRevision) != revision {
			o.supersede = fmt.Sprintf("het cluster staat nu op revisie %d", revision)
		}
	}
	return o, nil
}

// newSpec is de gewenste staat van een nieuw cluster zoals Git haar
// beschrijft, met het doel zoals de uitrol het krijgt; de nodes vult de
// uitrol in.
func newSpec(ch *Checked, target deploy.Target) deploy.Spec {
	return deploy.Spec{
		Template: deploy.SpecTemplate{Name: ch.Template.Name, Version: ch.Template.Version},
		Cluster:  templates.ClusterInfo{Name: ch.Metadata.Name, Slug: ch.File.Cluster.Slug, Environment: ch.Metadata.Environment},
		Params:   ch.Params, Secrets: ch.Template.Secrets(), Target: target, Nodes: []deploy.SpecNode{},
	}
}

// clusterOf zet een rij om in wat Check en het plan nodig hebben.
func (s *Service) clusterOf(ctx context.Context, r store.ListGitClustersRow) *Cluster {
	c := &Cluster{
		ID: r.ID, Slug: r.Slug, Name: r.Name, Description: r.Description, Environment: string(r.Environment),
		Type: r.Type, Tags: r.Tags, Linked: r.GitRepoID != nil, Revision: int(r.SpecRevision), Applied: int(r.AppliedRevision),
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	if r.TemplateName == nil {
		return c
	}
	inv, _, err := s.dep.Nodes(ctx, r.ID)
	if err != nil {
		return c
	}
	spec, err := deploy.ParseSpec(r.Spec, templates.ClusterInfo{Name: r.Name, Slug: r.Slug, Environment: string(r.Environment)}, inv)
	if err == nil {
		c.Spec = &spec
	}
	return c
}

// record legt de uitkomst van één bestand vast.
func (s *Service) record(ctx context.Context, q *store.Queries, repo store.GitRepo, head Commit, o outcome, pend store.GitChange, prev FileEntry) (FileEntry, error) {
	e := o.entry
	if o.supersede != "" && pend.ID != uuid.Nil {
		old, err := q.SupersedeGitChange(ctx, store.SupersedeGitChangeParams{ID: pend.ID, Reason: o.supersede})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return e, err
		}
		if err == nil {
			err := s.ev.Write(ctx, q, events.Event{
				Actor: events.System(), SubjectType: "git_change", SubjectID: old.ID.String(), ClusterID: old.ClusterID,
				Action: "gitops.change_superseded",
				Payload: map[string]any{"slug": old.Slug, "path": old.Path, "commit": old.CommitSha, "author": old.CommitAuthor,
					"reason": o.supersede, "by_commit": head.SHA},
			})
			if err != nil {
				return e, err
			}
		}
	}
	if o.plan != nil {
		spec, err := json.Marshal(o.spec)
		if err != nil {
			return e, err
		}
		meta, _ := json.Marshal(o.meta)
		plan, _ := json.Marshal(o.plan)
		date := head.Date
		ch, err := q.InsertGitChange(ctx, store.InsertGitChangeParams{
			RepoID: repo.ID, ClusterID: o.cluster, Slug: e.Slug, Path: e.Path, Kind: o.kind,
			CommitSha: head.SHA, CommitMessage: head.Message, CommitAuthor: head.Author, CommitVerified: head.Verified,
			CommitUrl: head.URL, CommittedAt: &date, BlobSha: e.BlobSHA, BaseRevision: int32(o.revision),
			Spec: spec, Metadata: meta, Plan: plan,
		})
		if err != nil {
			return e, err
		}
		id := ch.ID
		e.ChangeID, e.Commit = &id, head.SHA
		err = s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "git_change", SubjectID: ch.ID.String(), ClusterID: o.cluster,
			Action: "gitops.change_planned",
			Payload: map[string]any{"slug": e.Slug, "path": e.Path, "kind": o.kind, "name": o.meta.Name, "commit": head.SHA,
				"author": head.Author, "verified": head.Verified, "summary": o.plan.Summary, "base_revision": o.revision},
		})
		if err != nil {
			return e, err
		}
	}
	switch {
	case e.State == "invalid" && (prev.State != "invalid" || prev.BlobSHA != e.BlobSHA || !sameErrors(prev.Errors, e.Errors)):
		errs := make([]map[string]any, 0, len(e.Errors))
		for _, fe := range e.Errors {
			errs = append(errs, map[string]any{"field": fe.Field, "line": fe.Line, "message": fe.Message})
		}
		err := s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "git_file", SubjectID: e.Path, ClusterID: e.ClusterID, Action: "gitops.file_invalid",
			Payload: map[string]any{"path": e.Path, "slug": e.Slug, "commit": head.SHA, "author": head.Author, "errors": errs, "secret": e.Secret},
		})
		if err != nil {
			return e, err
		}
	case e.State == "missing" && prev.State != "missing":
		err := s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "git_file", SubjectID: e.Path, ClusterID: e.ClusterID, Action: "gitops.file_missing",
			Payload: map[string]any{"path": e.Path, "slug": e.Slug, "name": e.ClusterName, "commit": head.SHA, "author": head.Author},
		})
		if err != nil {
			return e, err
		}
	}
	return e, nil
}

func sameErrors(a, b []FieldError) bool {
	return slices.EqualFunc(a, b, func(x, y FieldError) bool { return x == y })
}
