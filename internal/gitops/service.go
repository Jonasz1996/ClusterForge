package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

var (
	ErrNotFound = errors.New("niet gevonden")
	// ErrNoMasterKey: zonder masterkey staat GitOps uit, want het token
	// moet versleuteld bewaard worden.
	ErrNoMasterKey = errors.New("de server heeft geen masterkey; zet CF_MASTER_KEY om een Git-repository te koppelen")
)

// ValidationError is een fout in de invoer of de verbinding die de
// gebruiker kan oplossen.
type ValidationError struct{ Field, Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ConflictError betekent dat het nu niet kan, met een stabiele code. Diff
// is gezet als een bestand niet gelijk is aan de export.
type ConflictError struct {
	Code, Msg string
	Diff      string
}

func (e *ConflictError) Error() string { return e.Msg }

// lockID is de advisory lock van de poll-lus: één server synchroniseert
// tegelijk.
const lockID = 4242003

// Service leest de gekoppelde repository, valideert de clusterbestanden en
// plant wijzigingen. Er gaat niets naar een node.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	box  *secrets.Box
	dep  *deploy.Service

	// Interval is de tijd tussen twee polls; 0 laat Run niet pollen, alleen
	// op Kick reageren.
	Interval time.Duration
	// NewSource maakt de client; tests vervangen hem niet, maar wijzen het
	// API-adres naar de nep-GitHub.
	NewSource func(Config) (Source, error)

	kick  chan struct{}
	force atomic.Bool

	mu sync.Mutex
	// cache van de laatste scan: de tree van een commit en de blobs.
	treeOf string
	tree   []TreeEntry
	blobs  map[string][]byte
	// l2 onthoudt fouten van de uitrolcontroles per blob en revisie, zodat
	// de lus Proxmox niet elke minuut opnieuw vraagt.
	l2 map[string][]FieldError
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, box *secrets.Box, dep *deploy.Service) *Service {
	return &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, box: box, dep: dep,
		Interval:  time.Minute,
		NewSource: func(c Config) (Source, error) { return NewGitHub(c) },
		kick:      make(chan struct{}, 1),
		blobs:     map[string][]byte{}, l2: map[string][]FieldError{},
	}
}

// Enabled is false zonder masterkey.
func (s *Service) Enabled() bool { return s.box != nil }

func (s *Service) reg() *templates.Registry { return s.dep.Templates }

func aad(id uuid.UUID) []byte { return []byte("git:" + id.String()) }

// Kick stoot de lus aan. Met force leest hij de tree opnieuw en vergeet hij
// wat de uitrolcontroles eerder zeiden.
func (s *Service) Kick(force bool) {
	if force {
		s.force.Store(true)
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run synchroniseert meteen, daarna elke Interval en bij elke Kick.
func (s *Service) Run(ctx context.Context) {
	var tick <-chan time.Time
	if s.Interval > 0 {
		t := time.NewTicker(s.Interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			s.log.Debug("gitops: synchroniseren mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-s.kick:
		}
	}
}

// RepoInput zijn de velden van de koppeling. Een leeg token laat het
// bestaande staan.
type RepoInput struct {
	APIURL string
	Owner  string
	Name   string
	Branch string
	Path   string
	Token  string
}

var pathRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

func (in *RepoInput) normalize() error {
	api, err := NormalizeAPIURL(in.APIURL)
	if err != nil {
		return ValidationError{"api_url", err.Error()}
	}
	in.APIURL = api
	in.Owner, in.Name = strings.TrimSpace(in.Owner), strings.TrimSuffix(strings.TrimSpace(in.Name), ".git")
	in.Branch = strings.TrimSpace(in.Branch)
	if in.Branch == "" {
		in.Branch = "main"
	}
	if err := CheckRepo(in.Owner, in.Name, in.Branch); err != nil {
		field := "repository"
		if strings.Contains(err.Error(), "branch") {
			field = "branch"
		}
		return ValidationError{field, err.Error()}
	}
	in.Path = strings.Trim(strings.TrimSpace(in.Path), "/")
	if in.Path == "" {
		in.Path = "clusters"
	}
	if !pathRe.MatchString(in.Path) || slices.Contains(strings.Split(in.Path, "/"), "..") || len(in.Path) > 200 {
		return ValidationError{"path", "de map is ongeldig; gebruik bijvoorbeeld clusters"}
	}
	in.Token = strings.TrimSpace(in.Token)
	if len(in.Token) > 500 || strings.ContainsAny(in.Token, " \t\r\n") {
		return ValidationError{"token", "het token is ongeldig"}
	}
	return nil
}

// token geeft het token uit de invoer of, als dat leeg is, het opgeslagen.
func (s *Service) token(in RepoInput, cur *store.GitRepo) (string, error) {
	if in.Token != "" {
		return in.Token, nil
	}
	if cur == nil {
		return "", ValidationError{"token", "het token is verplicht"}
	}
	t, err := s.box.Open(cur.TokenEnc, aad(cur.ID), cur.KeyID)
	if err != nil {
		return "", ValidationError{"token", "het opgeslagen token is niet te ontsleutelen (is CF_MASTER_KEY veranderd?); vul het opnieuw in"}
	}
	return string(t), nil
}

func (s *Service) current(ctx context.Context) (*store.GitRepo, error) {
	r, err := s.q.GetGitRepo(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Probe is wat een test van de koppeling vond.
type Probe struct {
	Head  Commit
	Files []string
}

// Probe test token, branch en map zonder iets op te slaan.
func (s *Service) Probe(ctx context.Context, in RepoInput) (Probe, error) {
	if s.box == nil {
		return Probe{}, ErrNoMasterKey
	}
	if err := in.normalize(); err != nil {
		return Probe{}, err
	}
	cur, err := s.current(ctx)
	if err != nil {
		return Probe{}, err
	}
	token, err := s.token(in, cur)
	if err != nil {
		return Probe{}, err
	}
	src, err := s.NewSource(Config{APIURL: in.APIURL, Owner: in.Owner, Name: in.Name, Branch: in.Branch, Token: token})
	if err != nil {
		return Probe{}, ValidationError{"repository", err.Error()}
	}
	sha, _, err := src.Head(ctx, "")
	if err != nil {
		return Probe{}, upstream(err)
	}
	c, err := src.Commit(ctx, sha)
	if err != nil {
		return Probe{}, upstream(err)
	}
	tree, err := src.Tree(ctx, c.Tree)
	if err != nil {
		return Probe{}, upstream(err)
	}
	p := Probe{Head: c, Files: []string{}}
	for _, e := range clusterFiles(tree, in.Path) {
		p.Files = append(p.Files, e.Path)
	}
	return p, nil
}

// upstream maakt van een fout van GitHub een fout die de gebruiker kan
// oplossen.
func upstream(err error) error {
	return ValidationError{"repository", err.Error()}
}

// Save maakt of wijzigt de koppeling, nadat GitHub de branch gaf.
func (s *Service) Save(ctx context.Context, actor events.Actor, in RepoInput) (store.GitRepo, error) {
	if s.box == nil {
		return store.GitRepo{}, ErrNoMasterKey
	}
	if err := in.normalize(); err != nil {
		return store.GitRepo{}, err
	}
	cur, err := s.current(ctx)
	if err != nil {
		return store.GitRepo{}, err
	}
	token, err := s.token(in, cur)
	if err != nil {
		return store.GitRepo{}, err
	}
	src, err := s.NewSource(Config{APIURL: in.APIURL, Owner: in.Owner, Name: in.Name, Branch: in.Branch, Token: token})
	if err != nil {
		return store.GitRepo{}, ValidationError{"repository", err.Error()}
	}
	if _, _, err := src.Head(ctx, ""); err != nil {
		return store.GitRepo{}, upstream(err)
	}
	var out store.GitRepo
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if cur == nil {
			id := uuid.New()
			r, err := q.CreateGitRepo(ctx, store.CreateGitRepoParams{
				ID: id, ApiUrl: in.APIURL, Owner: in.Owner, Name: in.Name, Branch: in.Branch, Path: in.Path,
				TokenEnc: s.box.Seal([]byte(token), aad(id)), KeyID: s.box.KeyID,
			})
			if err != nil {
				return err
			}
			out = r
			return s.ev.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "git_repo", SubjectID: id.String(), Action: "gitops.repo_connected",
				Payload: map[string]any{"repo": in.Owner + "/" + in.Name, "api_url": in.APIURL, "branch": in.Branch, "path": in.Path},
			})
		}
		locked, err := q.LockGitRepo(ctx)
		if err != nil {
			return err
		}
		before := map[string]string{"api_url": locked.ApiUrl, "repo": locked.Owner + "/" + locked.Name, "branch": locked.Branch, "path": locked.Path}
		after := map[string]string{"api_url": in.APIURL, "repo": in.Owner + "/" + in.Name, "branch": in.Branch, "path": in.Path}
		diff := map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(after)) {
			if before[k] != after[k] {
				diff[k] = map[string]any{"from": before[k], "to": after[k]}
			}
		}
		reset := len(diff) > 0
		enc, keyID := locked.TokenEnc, locked.KeyID
		if in.Token != "" {
			enc, keyID = s.box.Seal([]byte(token), aad(locked.ID)), s.box.KeyID
			// Nooit het token zelf, ook niet versleuteld.
			diff["token_changed"] = true
		}
		out, err = q.UpdateGitRepo(ctx, store.UpdateGitRepoParams{
			ID: locked.ID, ApiUrl: in.APIURL, Owner: in.Owner, Name: in.Name, Branch: in.Branch, Path: in.Path,
			TokenEnc: enc, KeyID: keyID, Reset: reset,
		})
		if err != nil || len(diff) == 0 {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "git_repo", SubjectID: locked.ID.String(), Action: "gitops.repo_updated", Payload: diff,
		})
	})
	if err != nil {
		return store.GitRepo{}, err
	}
	s.forget()
	s.Kick(true)
	return out, nil
}

// Delete verwijdert de koppeling en ontkoppelt alle clusters. Hun
// wijzigingen verdwijnen mee; het logboek houdt ze.
func (s *Service) Delete(ctx context.Context, actor events.Actor) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.LockGitRepo(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		unlinked, err := q.UnlinkGitClusters(ctx, &r.ID)
		if err != nil {
			return err
		}
		for _, c := range unlinked {
			err := s.ev.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: "cluster.git_unlinked",
				Payload: map[string]any{"name": c.Name, "path": filePath(r.Path, c.Slug), "reason": "de koppeling met de repository is verwijderd"},
			})
			if err != nil {
				return err
			}
		}
		if _, err := q.DeleteGitRepo(ctx, r.ID); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "git_repo", SubjectID: r.ID.String(), Action: "gitops.repo_disconnected",
			Payload: map[string]any{"repo": r.Owner + "/" + r.Name, "clusters": len(unlinked)},
		})
	})
	if err == nil {
		s.forget()
	}
	return err
}

// forget wist de cache.
func (s *Service) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.treeOf, s.tree = "", nil
	s.blobs = map[string][]byte{}
	s.l2 = map[string][]FieldError{}
}

// filePath is het pad van het bestand van een cluster.
func filePath(dir, slug string) string { return dir + "/" + slug + "/cluster.yaml" }

// clusterFiles kiest uit een tree de bestanden <map>/<slug>/cluster.yaml.
// Andere bestanden worden genegeerd.
func clusterFiles(tree []TreeEntry, dir string) []TreeEntry {
	var out []TreeEntry
	prefix := dir + "/"
	for _, e := range tree {
		rest, ok := strings.CutPrefix(e.Path, prefix)
		if !ok || e.Type != "blob" {
			continue
		}
		slug, name, ok := strings.Cut(rest, "/")
		if ok && name == "cluster.yaml" && slug != "" {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b TreeEntry) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// slugOf is de map van een clusterbestand.
func slugOf(dir, p string) string {
	rest := strings.TrimPrefix(p, dir+"/")
	slug, _, _ := strings.Cut(rest, "/")
	return slug
}

// FileEntry is één bestand uit de laatste scan, of een gekoppeld cluster
// waarvan het bestand ontbreekt.
type FileEntry struct {
	Path    string `json:"path"`
	Slug    string `json:"slug"`
	BlobSHA string `json:"blob_sha,omitempty"`
	// State is in_sync, pending, invalid, new, unlinked of missing.
	State       string       `json:"state"`
	ClusterID   *uuid.UUID   `json:"cluster_id,omitempty"`
	ClusterName string       `json:"cluster_name,omitempty"`
	Errors      []FieldError `json:"errors"`
	ChangeID    *uuid.UUID   `json:"change_id,omitempty"`
	// Commit is de commit waarin ClusterForge deze inhoud voor het eerst
	// zag.
	Commit string `json:"commit,omitempty"`
	// Secret: er stond een geheim in het bestand; het wordt niet bewaard.
	Secret bool `json:"secret,omitempty"`
}

// Scan leest de bestanden uit de laatste scan.
func Scan(r store.GitRepo) []FileEntry {
	var out []FileEntry
	if err := json.Unmarshal(r.Scan, &out); err != nil || out == nil {
		return []FileEntry{}
	}
	return out
}

// HeadCommit is de laatste commit die de lus zag.
func HeadCommit(r store.GitRepo) *Commit {
	if len(r.HeadCommit) == 0 {
		return nil
	}
	var c Commit
	if json.Unmarshal(r.HeadCommit, &c) != nil {
		return nil
	}
	return &c
}

func (c Commit) String() string { return fmt.Sprintf("%s %q", short(c.SHA), c.Title()) }
