package docs

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

const (
	// publishLock is de advisory lock van het publiceren: met twee servers
	// schrijft er maar één tegelijk naar GitHub.
	publishLock = 4242010
	// authorName en authorEmail staan bij elke commit. Het adres eindigt op
	// .invalid, zodat GitHub de commit aan geen enkel account koppelt.
	authorName  = "ClusterForge"
	authorEmail = "noreply@clusterforge.invalid"
	// maxRetries is hoe vaak een commit opnieuw gebouwd wordt als de branch
	// intussen veranderde.
	maxRetries = 3
)

var (
	shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// docRe zijn de hoofdstukken van een cluster zoals Path ze schrijft.
	docRe = regexp.MustCompile(`^clusters/[^/]+/(architectuur|kubernetes|database|netwerk|configuratie|herstel|runbooks)\.md$`)
)

// De soorten wijziging van een bestand in een commit.
const (
	Added    = "added"
	Modified = "modified"
	Deleted  = "deleted"
)

// FileChange is één bestand in een commit, met zijn volledige pad in de
// repository.
type FileChange struct {
	Path string
	Kind string
}

// under geeft het pad binnen de map dir; dir leeg is de hoofdmap.
func under(dir, path string) (string, bool) {
	if dir == "" {
		return path, true
	}
	return strings.CutPrefix(path, dir+"/")
}

func join(dir, rel string) string {
	if dir == "" {
		return rel
	}
	return dir + "/" + rel
}

// ours zegt of een pad in de map een document van ClusterForge is. Alleen
// zo'n bestand verwijdert de export, als het document er niet meer is;
// andere bestanden in de map blijven staan.
func ours(rel string) bool { return rel == "README.md" || docRe.MatchString(rel) }

// blobSHA is de sha die Git aan een bestand geeft.
func blobSHA(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(sum[:])
}

// diffFiles vergelijkt wat er moet staan (pad naar inhoud) met wat er staat
// (pad naar blob-sha), binnen de map dir.
func diffFiles(have, want map[string]string, dir string) []FileChange {
	var out []FileChange
	for path, content := range want {
		sha, ok := have[path]
		switch {
		case !ok:
			out = append(out, FileChange{path, Added})
		case sha != blobSHA(content):
			out = append(out, FileChange{path, Modified})
		}
	}
	for path := range have {
		if _, keep := want[path]; keep {
			continue
		}
		if rel, ok := under(dir, path); ok && ours(rel) {
			out = append(out, FileChange{path, Deleted})
		}
	}
	slices.SortFunc(out, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// pushResult is een geslaagde ronde: de commit, of een lege SHA als er niets
// veranderde.
type pushResult struct {
	SHA, URL string
	Changes  []FileChange
}

// push zet de bestanden in want op branch, in één commit met alleen wat
// veranderde, en werkt de branch bij zonder force. Veranderde de branch
// intussen, dan leest het opnieuw en bouwt het de commit opnieuw. Bestaat
// de branch nog niet, dan maakt het hem met alleen de documentatie.
func push(ctx context.Context, g *gitHub, branch, dir string, want map[string]string, at time.Time) (pushResult, error) {
	made := map[string]string{}
	for attempt := 0; ; attempt++ {
		head, err := g.ref(ctx, branch)
		create := errors.Is(err, errNoBranch)
		if create {
			err = g.repo(ctx)
		}
		if err != nil {
			return pushResult{}, err
		}
		have, base := map[string]string{}, ""
		if !create {
			if base, err = g.commitTree(ctx, head); err != nil {
				return pushResult{}, err
			}
			if have, err = g.blobs(ctx, base); err != nil {
				return pushResult{}, err
			}
		}
		changes := diffFiles(have, want, dir)
		if len(changes) == 0 {
			return pushResult{}, nil
		}
		entries := make([]treeEntry, 0, len(changes))
		for _, c := range changes {
			e := treeEntry{Path: c.Path, Mode: "100644", Type: "blob"}
			if c.Kind != Deleted {
				content := want[c.Path]
				sha, ok := made[content]
				if !ok {
					if sha, err = g.blob(ctx, []byte(content)); err != nil {
						return pushResult{}, err
					}
					made[content] = sha
				}
				e.SHA = &sha
			}
			entries = append(entries, e)
		}
		tree, err := g.tree(ctx, base, entries)
		if err != nil {
			return pushResult{}, err
		}
		var parents []string
		if !create {
			parents = []string{head}
		}
		sha, url, err := g.commit(ctx, commitMessage(changes, dir), tree, parents, at)
		if err != nil {
			return pushResult{}, err
		}
		err = g.update(ctx, branch, sha, create)
		if errors.Is(err, errMoved) {
			if attempt < maxRetries {
				continue
			}
			return pushResult{}, errors.New("de branch verandert steeds terwijl ClusterForge schrijft; het probeert het straks opnieuw")
		}
		if err != nil {
			return pushResult{}, err
		}
		return pushResult{SHA: sha, URL: url, Changes: changes}, nil
	}
}

// docName is het overzicht of de slug van een cluster bij een pad.
func docName(rel string) string {
	if rel == "README.md" {
		return "overzicht"
	}
	if rest, ok := strings.CutPrefix(rel, "clusters/"); ok {
		slug, _, _ := strings.Cut(rest, "/")
		return slug
	}
	return rel
}

// changedNames zijn de documenten in een commit: het overzicht eerst, dan
// de clusters op slug.
func changedNames(changes []FileChange, dir string) []string {
	var names []string
	overview := false
	for _, c := range changes {
		rel, _ := under(dir, c.Path)
		n := docName(rel)
		if n == "overzicht" {
			overview = true
		} else if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	if overview {
		names = append([]string{"overzicht"}, names...)
	}
	return names
}

var kindWords = map[string]string{Added: "nieuw", Modified: "bijgewerkt", Deleted: "verwijderd"}

// commitMessage is bijvoorbeeld "Documentatie bijgewerkt: web-prod,
// db-prod", met daaronder elk bestand.
func commitMessage(changes []FileChange, dir string) string {
	names := changedNames(changes, dir)
	if len(names) > 5 {
		names = append(names[:4], fmt.Sprintf("en %d andere", len(names)-4))
	}
	var b strings.Builder
	b.WriteString("Documentatie bijgewerkt: " + strings.Join(names, ", ") + "\n\n")
	for i, c := range changes {
		if i == 50 {
			fmt.Fprintf(&b, "- en nog %d bestanden\n", len(changes)-50)
			break
		}
		rel, _ := under(dir, c.Path)
		fmt.Fprintf(&b, "- %s: %s\n", rel, kindWords[c.Kind])
	}
	b.WriteString("\nGeschreven door ClusterForge uit wat er draait. Wijzig deze bestanden niet met de hand.\n")
	return b.String()
}

// PublishResult is een geslaagde publicatie. SHA is leeg als er niets
// veranderde.
type PublishResult struct {
	SHA, URL string
	Changes  []FileChange
}

// De oorzaken van een publicatie, voor het logboek.
const (
	PublishAuto   = "automatisch"
	PublishManual = "handmatig"
)

// markDirty zegt dat er iets te publiceren valt.
func (s *Service) markDirty() {
	s.mu.Lock()
	s.gen++
	s.mu.Unlock()
}

// Pending zegt of er een publicatie wacht.
func (s *Service) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen != s.published
}

// maybePublish publiceert als er iets wacht. Na een fout wacht het
// RetryAfter.
func (s *Service) maybePublish(ctx context.Context) {
	now := s.Now()
	s.mu.Lock()
	due := s.gen != s.published && (s.failedAt.IsZero() || now.Sub(s.failedAt) >= s.RetryAfter)
	s.mu.Unlock()
	if !due || s.Box == nil {
		return
	}
	_, err := s.Publish(ctx, PublishAuto)
	if err != nil && ctx.Err() == nil && !errors.Is(err, ErrBusy) && !errors.Is(err, ErrNoTarget) {
		s.log.Warn("documentatie publiceren mislukt", "err", err)
	}
}

// Publish schrijft de nieuwste versie van elk document naar de gekoppelde
// repository. Zonder verschil komt er geen commit. Een uitgeschakelde
// koppeling publiceert alleen met de hand.
func (s *Service) Publish(ctx context.Context, cause string) (PublishResult, error) {
	if s.Box == nil {
		return PublishResult{}, ErrNoMasterKey
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", publishLock).Scan(&locked); err != nil {
		return PublishResult{}, err
	}
	if !locked {
		return PublishResult{}, ErrBusy
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", publishLock) }()

	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()
	t, err := s.q.GetDocsTarget(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		s.mu.Lock()
		s.published = gen
		s.mu.Unlock()
		return PublishResult{}, ErrNoTarget
	}
	if err != nil {
		return PublishResult{}, err
	}
	if !t.Enabled && cause != PublishManual {
		s.mu.Lock()
		s.published = gen
		s.mu.Unlock()
		return PublishResult{}, nil
	}
	res, err := s.publishTo(ctx, t, cause == PublishManual)
	if ctx.Err() != nil {
		return PublishResult{}, ctx.Err()
	}
	s.record(ctx, t, res, err, cause)
	s.mu.Lock()
	if err == nil {
		s.published, s.failedAt = gen, time.Time{}
	} else {
		// Na een fout probeert de lus het opnieuw, ook als er intussen niets
		// veranderde, zodat de fout niet blijft staan.
		s.failedAt = s.Now()
		if s.gen == s.published {
			s.gen++
		}
	}
	s.mu.Unlock()
	return res, err
}

// PublishError is een publicatie die mislukte om een reden die de gebruiker
// kan oplossen of die bij GitHub ligt; de melding komt bij de koppeling.
type PublishError struct{ Msg string }

func (e *PublishError) Error() string { return e.Msg }

func publishErr(err error) error {
	var ve ValidationError
	if errors.As(err, &ve) {
		return &PublishError{ve.Msg}
	}
	return &PublishError{err.Error()}
}

// publishTo leest de documenten en zet ze in de repository. Met generate
// schrijft het eerst de documenten die nog geen versie hebben.
func (s *Service) publishTo(ctx context.Context, t store.DocsTarget, generate bool) (PublishResult, error) {
	token, err := s.openToken(t)
	if err != nil {
		return PublishResult{}, publishErr(err)
	}
	if err := s.notGitOps(ctx, t.ApiUrl, t.Owner, t.Name, t.Branch); err != nil {
		var ve ValidationError
		if errors.As(err, &ve) {
			return PublishResult{}, publishErr(err)
		}
		return PublishResult{}, err
	}
	want, err := s.gitFiles(ctx, t.Path, generate)
	if err != nil {
		return PublishResult{}, err
	}
	g, err := newGitHub(t.ApiUrl, t.Owner, t.Name, token)
	if err != nil {
		return PublishResult{}, publishErr(err)
	}
	r, err := push(ctx, g, t.Branch, t.Path, want, s.Now())
	if err != nil {
		return PublishResult{}, publishErr(err)
	}
	return PublishResult(r), nil
}

// gitFiles geeft de nieuwste bewaarde versie van elk document met zijn
// pad in de repository. Een document zonder versie, zoals van een cluster
// van een minuut oud, komt in de volgende ronde; zonder overzicht komt er
// niets, zodat een lege database nooit alles in Git wist.
func (s *Service) gitFiles(ctx context.Context, dir string, generate bool) (map[string]string, error) {
	idx, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	metas := func() []Meta {
		out := []Meta{idx.Overview}
		for _, c := range idx.Clusters {
			out = append(out, c.Docs...)
		}
		return out
	}
	if generate && slices.ContainsFunc(metas(), func(m Meta) bool { return m.Version == nil }) {
		if _, err := s.GenerateAll(ctx, CauseManual); err != nil {
			return nil, err
		}
		if idx, err = s.List(ctx); err != nil {
			return nil, err
		}
	}
	if idx.Overview.Version == nil {
		return nil, &PublishError{"de documentatie is nog niet geschreven; dat gebeurt een minuut na de start van de server"}
	}
	want := map[string]string{}
	for _, m := range metas() {
		if m.Version == nil {
			continue
		}
		d, err := s.Get(ctx, m.Scope, m.Kind, m.ClusterID, m.Version)
		if errors.Is(err, ErrNotFound) {
			// Intussen opgeruimd; de volgende ronde neemt de nieuwe.
			continue
		}
		if err != nil {
			return nil, err
		}
		want[join(dir, m.Path)] = d.Text(s.Opts)
	}
	return want, nil
}

// record bewaart de uitkomst bij de koppeling. Een commit, een nieuwe fout
// en het herstel na een fout komen in het logboek; een ronde zonder
// verschil niet.
func (s *Service) record(ctx context.Context, t store.DocsTarget, res PublishResult, perr error, cause string) {
	now := s.Now()
	repo := t.Owner + "/" + t.Name
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		subject := func(action string, payload map[string]any) events.Event {
			payload["repo"] = repo
			return events.Event{Actor: events.System(), SubjectType: "docs_target", SubjectID: t.ID.String(), Action: action, Payload: payload}
		}
		if perr != nil {
			msg := "interne fout bij het lezen van de documentatie; zie het log van de server"
			var pe *PublishError
			if errors.As(perr, &pe) {
				msg = pe.Msg
			}
			if err := q.SetDocsPublishError(ctx, store.SetDocsPublishErrorParams{ID: t.ID, LastError: msg, At: &now}); err != nil {
				return err
			}
			if t.LastError != "" {
				return nil
			}
			return s.Events.Write(ctx, q, subject("docs.publish_failed", map[string]any{"branch": t.Branch, "error": msg}))
		}
		if res.SHA == "" {
			if err := q.SetDocsChecked(ctx, store.SetDocsCheckedParams{ID: t.ID, At: &now}); err != nil {
				return err
			}
		} else {
			if err := q.SetDocsPublished(ctx, store.SetDocsPublishedParams{
				ID: t.ID, Sha: res.SHA, Url: res.URL, Files: int32(len(res.Changes)), At: &now,
			}); err != nil {
				return err
			}
		}
		if t.LastError != "" {
			if err := s.Events.Write(ctx, q, subject("docs.publish_recovered", map[string]any{"branch": t.Branch, "sha": res.SHA})); err != nil {
				return err
			}
		}
		if res.SHA == "" {
			return nil
		}
		return s.Events.Write(ctx, q, subject("docs.published", map[string]any{
			"branch": t.Branch, "sha": res.SHA, "url": res.URL, "files": len(res.Changes),
			"documents": changedNames(res.Changes, t.Path), "cause": cause,
		}))
	})
	if err != nil && ctx.Err() == nil {
		s.log.Warn("uitkomst van het publiceren bewaren mislukt", "err", err)
	}
}
