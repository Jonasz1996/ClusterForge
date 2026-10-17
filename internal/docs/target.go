package docs

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

var (
	// ErrNoMasterKey: zonder masterkey kan het token niet versleuteld
	// bewaard worden, en staat de export uit.
	ErrNoMasterKey = errors.New("de server heeft geen masterkey; zet CF_MASTER_KEY om de documentatie naar GitHub te schrijven")
	// ErrNoTarget: er is geen repository gekoppeld.
	ErrNoTarget = errors.New("koppel eerst een GitHub-repository voor de documentatie")
	// ErrBusy: een andere server schrijft nu.
	ErrBusy = errors.New("een andere server schrijft de documentatie nu naar GitHub; probeer het zo opnieuw")
)

// ValidationError is een fout in de invoer of de verbinding die de
// gebruiker kan oplossen.
type ValidationError struct{ Field, Msg string }

func (e ValidationError) Error() string { return e.Msg }

// DefaultPath is de map in de repository als er geen gekozen is.
const DefaultPath = "docs"

// TargetInput zijn de velden van de koppeling. Een leeg token laat het
// bestaande staan.
type TargetInput struct {
	APIURL string
	Owner  string
	Name   string
	Branch string
	// Path nil is DefaultPath; leeg is de hoofdmap van de repository.
	Path    *string
	Token   string
	Enabled *bool
}

var pathRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// target is de genormaliseerde invoer.
type target struct {
	api, owner, name, branch, path, token string
}

func (in TargetInput) normalize() (target, error) {
	api, err := gitops.NormalizeAPIURL(in.APIURL)
	if err != nil {
		return target{}, ValidationError{"api_url", err.Error()}
	}
	t := target{api: api, owner: strings.TrimSpace(in.Owner), name: strings.TrimSuffix(strings.TrimSpace(in.Name), ".git")}
	if t.branch = strings.TrimSpace(in.Branch); t.branch == "" {
		t.branch = "main"
	}
	if err := gitops.CheckRepo(t.owner, t.name, t.branch); err != nil {
		field := "repository"
		if strings.Contains(err.Error(), "branch") {
			field = "branch"
		}
		return target{}, ValidationError{field, err.Error()}
	}
	t.path = DefaultPath
	if in.Path != nil {
		t.path = strings.Trim(strings.TrimSpace(*in.Path), "/")
	}
	if t.path != "" {
		segs := strings.Split(t.path, "/")
		if !pathRe.MatchString(t.path) || slices.Contains(segs, "..") || slices.Contains(segs, ".") || len(t.path) > 200 {
			return target{}, ValidationError{"path", "de map is ongeldig; gebruik bijvoorbeeld docs, of laat ze leeg voor de hoofdmap"}
		}
	}
	t.token = strings.TrimSpace(in.Token)
	if len(t.token) > 500 || strings.ContainsAny(t.token, " \t\r\n") {
		return target{}, ValidationError{"token", "het token is ongeldig"}
	}
	return t, nil
}

func targetAAD(id uuid.UUID) []byte { return []byte("docs:" + id.String()) }

// Enabled is false zonder masterkey.
func (s *Service) Enabled() bool { return s.Box != nil }

// Target geeft de koppeling, of nil als er geen is.
func (s *Service) Target(ctx context.Context) (*store.DocsTarget, error) {
	t, err := s.q.GetDocsTarget(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// token geeft het token uit de invoer of, als dat leeg is, het opgeslagen.
func (s *Service) token(in target, cur *store.DocsTarget) (string, error) {
	if in.token != "" {
		return in.token, nil
	}
	if cur == nil {
		return "", ValidationError{"token", "het token is verplicht"}
	}
	// Het opgeslagen token gaat nooit naar een ander adres: anders volstaat
	// een gestolen sessie om het naar een eigen server te laten sturen.
	if in.api != cur.ApiUrl {
		return "", ValidationError{"token", "het adres van de API is veranderd; vul het token opnieuw in"}
	}
	return s.openToken(*cur)
}

func (s *Service) openToken(t store.DocsTarget) (string, error) {
	b, err := s.Box.Open(t.TokenEnc, targetAAD(t.ID), t.KeyID)
	if err != nil {
		return "", ValidationError{"token", "het opgeslagen token is niet te ontsleutelen (is CF_MASTER_KEY veranderd?); vul het opnieuw in"}
	}
	return string(b), nil
}

// notGitOps weigert de repository en branch die GitOps leest: een commit
// van de documentatie mag nooit een GitOps-plan starten. Een andere branch
// in dezelfde repository mag.
func (s *Service) notGitOps(ctx context.Context, api, owner, name, branch string) error {
	r, err := s.q.GetGitRepo(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if gitops.SameBranch(r.ApiUrl, r.Owner, r.Name, r.Branch, api, owner, name, branch) {
		return ValidationError{"branch", "GitOps leest deze repository en branch; kies een eigen repository voor de documentatie, of een andere branch"}
	}
	return nil
}

// Probe is wat een test van de koppeling vond.
type Probe struct {
	// BranchExists is false als ClusterForge de branch bij de eerste
	// publicatie zelf maakt.
	BranchExists bool
	Head         string
	// Files is hoeveel documenten van ClusterForge er al in de map staan.
	Files int
}

// probe leest de branch en maakt een losse blob: zo blijkt of het token mag
// schrijven, zonder dat er aan een branch iets verandert.
func probe(ctx context.Context, g *gitHub, branch, dir string) (Probe, error) {
	var p Probe
	head, err := g.ref(ctx, branch)
	switch {
	case errors.Is(err, errNoBranch):
		if err := g.repo(ctx); err != nil {
			return p, err
		}
	case err != nil:
		return p, err
	default:
		p.BranchExists, p.Head = true, head
		tree, err := g.commitTree(ctx, head)
		if err != nil {
			return p, err
		}
		files, err := g.blobs(ctx, tree)
		if err != nil {
			return p, err
		}
		for path := range files {
			if rel, ok := under(dir, path); ok && ours(rel) {
				p.Files++
			}
		}
	}
	if _, err := g.blob(ctx, []byte("ClusterForge controleert of dit token mag schrijven.\n")); err != nil {
		return p, err
	}
	return p, nil
}

// TestTarget test token, repository en branch zonder iets op te slaan.
func (s *Service) TestTarget(ctx context.Context, in TargetInput) (Probe, error) {
	if s.Box == nil {
		return Probe{}, ErrNoMasterKey
	}
	t, err := in.normalize()
	if err != nil {
		return Probe{}, err
	}
	cur, err := s.Target(ctx)
	if err != nil {
		return Probe{}, err
	}
	token, err := s.token(t, cur)
	if err != nil {
		return Probe{}, err
	}
	if err := s.notGitOps(ctx, t.api, t.owner, t.name, t.branch); err != nil {
		return Probe{}, err
	}
	g, err := newGitHub(t.api, t.owner, t.name, token)
	if err != nil {
		return Probe{}, ValidationError{"repository", err.Error()}
	}
	p, err := probe(ctx, g, t.branch, t.path)
	if err != nil {
		return Probe{}, ValidationError{"repository", err.Error()}
	}
	return p, nil
}

// SaveTarget maakt of wijzigt de koppeling, nadat GitHub liet zien dat het
// token mag schrijven. Daarna publiceert de lus meteen.
func (s *Service) SaveTarget(ctx context.Context, actor events.Actor, in TargetInput) (store.DocsTarget, error) {
	if s.Box == nil {
		return store.DocsTarget{}, ErrNoMasterKey
	}
	t, err := in.normalize()
	if err != nil {
		return store.DocsTarget{}, err
	}
	cur, err := s.Target(ctx)
	if err != nil {
		return store.DocsTarget{}, err
	}
	token, err := s.token(t, cur)
	if err != nil {
		return store.DocsTarget{}, err
	}
	if err := s.notGitOps(ctx, t.api, t.owner, t.name, t.branch); err != nil {
		return store.DocsTarget{}, err
	}
	g, err := newGitHub(t.api, t.owner, t.name, token)
	if err != nil {
		return store.DocsTarget{}, ValidationError{"repository", err.Error()}
	}
	if _, err := probe(ctx, g, t.branch, t.path); err != nil {
		return store.DocsTarget{}, ValidationError{"repository", err.Error()}
	}
	repo := t.owner + "/" + t.name
	var out store.DocsTarget
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		locked, err := q.LockDocsTarget(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			id := uuid.New()
			enabled := in.Enabled == nil || *in.Enabled
			out, err = q.CreateDocsTarget(ctx, store.CreateDocsTargetParams{
				ID: id, ApiUrl: t.api, Owner: t.owner, Name: t.name, Branch: t.branch, Path: t.path,
				TokenEnc: s.Box.Seal([]byte(token), targetAAD(id)), KeyID: s.Box.KeyID, Enabled: enabled,
			})
			if err != nil {
				return err
			}
			return s.Events.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "docs_target", SubjectID: id.String(), Action: "docs.target_connected",
				Payload: map[string]any{"repo": repo, "api_url": t.api, "branch": t.branch, "path": t.path, "enabled": enabled},
			})
		}
		if err != nil {
			return err
		}
		before := map[string]string{"api_url": locked.ApiUrl, "repo": locked.Owner + "/" + locked.Name, "branch": locked.Branch, "path": locked.Path}
		after := map[string]string{"api_url": t.api, "repo": repo, "branch": t.branch, "path": t.path}
		diff := map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(after)) {
			if before[k] != after[k] {
				diff[k] = map[string]any{"from": before[k], "to": after[k]}
			}
		}
		reset := len(diff) > 0
		enabled := locked.Enabled
		if in.Enabled != nil && *in.Enabled != enabled {
			diff["enabled"] = map[string]any{"from": enabled, "to": *in.Enabled}
			enabled = *in.Enabled
		}
		enc, keyID := locked.TokenEnc, locked.KeyID
		if t.token != "" {
			enc, keyID = s.Box.Seal([]byte(token), targetAAD(locked.ID)), s.Box.KeyID
			// Nooit het token zelf, ook niet versleuteld.
			diff["token_changed"] = true
		}
		out, err = q.UpdateDocsTarget(ctx, store.UpdateDocsTargetParams{
			ID: locked.ID, ApiUrl: t.api, Owner: t.owner, Name: t.name, Branch: t.branch, Path: t.path,
			TokenEnc: enc, KeyID: keyID, Enabled: enabled, Reset: reset,
		})
		if err != nil || len(diff) == 0 {
			return err
		}
		diff["repo_name"] = repo
		return s.Events.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "docs_target", SubjectID: locked.ID.String(), Action: "docs.target_updated", Payload: diff,
		})
	})
	if err != nil {
		return store.DocsTarget{}, err
	}
	s.mu.Lock()
	s.failedAt = time.Time{}
	s.mu.Unlock()
	s.markDirty()
	s.Kick()
	return out, nil
}

// DeleteTarget verwijdert de koppeling. Wat al in Git staat, blijft daar.
func (s *Service) DeleteTarget(ctx context.Context, actor events.Actor) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		t, err := q.LockDocsTarget(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := q.DeleteDocsTarget(ctx, t.ID); err != nil {
			return err
		}
		return s.Events.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "docs_target", SubjectID: t.ID.String(), Action: "docs.target_disconnected",
			Payload: map[string]any{"repo": t.Owner + "/" + t.Name},
		})
	})
}

// RepoURL is de repository op de website.
func RepoURL(t store.DocsTarget) string {
	return gitops.WebURL(t.ApiUrl) + "/" + t.Owner + "/" + t.Name
}

// FolderURL is de map met de documentatie op de website.
func FolderURL(t store.DocsTarget) string {
	u := RepoURL(t) + "/tree/" + t.Branch
	if t.Path != "" {
		u += "/" + t.Path
	}
	return u
}
