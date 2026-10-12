package docs

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

const (
	// lockID is de advisory lock van het opslaan: ook met twee servers
	// schrijft er maar één tegelijk een nieuwe versie.
	lockID = 4242009
	// Keep is hoeveel versies per document bewaard blijven; Git heeft de
	// rest.
	Keep = 50
	// eventBatch is hoeveel events één ronde hoogstens leest.
	eventBatch = 2000
)

// De oorzaken van een nieuwe versie, naast een event.
const (
	CauseStart   = "start"
	CauseNight   = "nacht"
	CauseManual  = "handmatig"
	CauseSummary = "samenvatting"
)

// Service schrijft de documentatie opnieuw na een wijziging en elke nacht,
// en bewaart elke versie die anders is dan de vorige.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	log  *slog.Logger

	Deploy  *deploy.Service
	GitOps  *gitops.Service
	Backups *backups.Service
	Risk    *risk.Service
	// Box versleutelt het token van de GitHub-koppeling; zonder masterkey
	// is hij nil en staat de export uit.
	Box    *secrets.Box
	Events *events.Writer

	Opts Opts
	// Now is de klok; tests zetten hem vooruit.
	Now func() time.Time
	// Settle is hoe lang een cluster na de eerste wijziging wacht, zodat
	// een uitrol of een reeks wijzigingen één nieuwe versie geeft.
	Settle time.Duration
	// Interval is hoe vaak de lus naar nieuwe events kijkt.
	Interval time.Duration
	// Warmup is hoe lang de lus na de start wacht met de eerste ronde.
	Warmup time.Duration
	// NightHour is het uur waarop alles elke nacht opnieuw geschreven wordt,
	// op de klok van Opts.Loc.
	NightHour int
	// RetryAfter is hoe lang de export na een fout wacht.
	RetryAfter time.Duration

	mu     sync.Mutex
	cursor int64
	// queued is per cluster sinds wanneer en waarom het opnieuw moet;
	// uuid.Nil is het overzicht.
	queued    map[uuid.UUID]mark
	lastNight string
	kick      chan struct{}
	// gen telt wat er te publiceren valt, published tot waar dat gelukt is;
	// failedAt is de laatste mislukte poging.
	gen, published uint64
	failedAt       time.Time
}

type mark struct {
	since time.Time
	cause string
}

func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{
		pool: pool, q: store.New(pool), log: log, Now: time.Now, Settle: 10 * time.Minute, Interval: 30 * time.Second,
		Warmup: time.Minute, NightHour: 6, RetryAfter: 5 * time.Minute, queued: map[uuid.UUID]mark{}, kick: make(chan struct{}, 1),
	}
}

// Kick laat de lus meteen naar nieuwe events kijken.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run houdt de documentatie bij tot ctx afloopt: na de opwarmtijd schrijft
// ze alles één keer, daarna wat events raken, en elke nacht alles.
func (s *Service) Run(ctx context.Context) {
	cursor, err := s.q.MaxEventID(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("documentatie: events lezen mislukt", "err", err)
	}
	s.mu.Lock()
	s.cursor = cursor
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return
	case <-time.After(s.Warmup):
	}
	if _, err := s.GenerateAll(ctx, CauseStart); err != nil && ctx.Err() == nil {
		s.log.Warn("documentatie schrijven mislukt", "err", err)
	}
	s.markNight(s.Now())
	// Bij de start vergelijkt de export Git met de documentatie, ook als er
	// niets veranderde.
	s.markDirty()
	s.maybePublish(ctx)
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("documentatie bijwerken mislukt", "err", err)
		}
	}
}

// markNight onthoudt dat de nacht van vandaag al geweest is als het al na
// NightHour is.
func (s *Service) markNight(now time.Time) {
	l := now.In(s.Opts.loc())
	if l.Hour() >= s.NightHour {
		s.mu.Lock()
		s.lastNight = l.Format(time.DateOnly)
		s.mu.Unlock()
	}
}

// Tick leest de nieuwe events, zet de clusters die ze raken in de rij, en
// schrijft wat lang genoeg wacht. Na NightHour schrijft ze één keer per dag
// alles. Daarna publiceert ze naar GitHub als er iets veranderde.
func (s *Service) Tick(ctx context.Context) error {
	err := s.tick(ctx)
	s.maybePublish(ctx)
	return err
}

func (s *Service) tick(ctx context.Context) error {
	now := s.Now()
	if err := s.poll(ctx, now); err != nil {
		return err
	}
	l := now.In(s.Opts.loc())
	day := l.Format(time.DateOnly)
	s.mu.Lock()
	night := l.Hour() >= s.NightHour && s.lastNight != day
	s.mu.Unlock()
	if night {
		if _, err := s.GenerateAll(ctx, CauseNight); err != nil {
			return err
		}
		s.mu.Lock()
		s.lastNight = day
		s.mu.Unlock()
		// Elke nacht vergelijkt de export Git opnieuw met de documentatie.
		s.markDirty()
		return nil
	}
	due := map[uuid.UUID]string{}
	s.mu.Lock()
	for id, m := range s.queued {
		if now.Sub(m.since) >= s.Settle {
			due[id] = m.cause
		}
	}
	s.mu.Unlock()
	if len(due) == 0 {
		return nil
	}
	_, err := s.generate(ctx, due)
	return err
}

// quiet zijn events die niets aan de documentatie veranderen.
var quiet = map[string]bool{
	"agent.command": true, "agent.enroll_failed": true, "node.status_changed": true, "vip.owner_changed": true,
	"service.status_changed": true, "kube.app_status_changed": true, "vm.status_changed": true, "node.link_proposed": true, "node.link_ignored": true,
	"backup.sandbox_created": true, "backup.sandbox_destroyed": true, "backup.sandbox_destroy_failed": true,
	"backup.sandbox_connection_refused": true, "backup.inventory_failed": true, "backup.inventory_recovered": true,
	"failover.fault_injected": true, "failover.fault_cleared": true, "dr.started": true, "dr.aborted": true,
	"gitops.commit_seen": true, "gitops.sync_failed": true, "gitops.sync_recovered": true, "gitops.change_planned": true,
	"gitops.change_superseded": true, "gitops.change_approved": true, "gitops.change_rejected": true, "gitops.file_invalid": true,
	"gitops.file_missing": true, "gitops.reapply_requested": true,
	"proxmox.sync_requested": true, "proxmox.sync_failed": true, "proxmox.sync_recovered": true,
	"kube.access_issued": true, "kube.access_revoked": true, "kube.snapshot_saved": true,
	"kube.snapshot_restore_started": true,
}

var quietPrefixes = []string{"auth.", "user.", "audit.", "job.", "assistant.", "incident.", "drift.", "enrollment_token.", "docs."}

// overviewOnly raken alleen het overzicht.
var overviewOnly = map[string]bool{
	"cluster.status_changed": true, "risk.grade_changed": true, "risk.accepted": true, "risk.acceptance_removed": true,
}

// Affects zegt welke documenten een event raakt: het overzicht, een
// cluster, of alle clusters als het event bij geen cluster hoort, zoals
// een wijziging in Proxmox.
func Affects(action string, cluster *uuid.UUID) (touch bool, overview bool, all bool) {
	if quiet[action] {
		return false, false, false
	}
	for _, p := range quietPrefixes {
		if strings.HasPrefix(action, p) {
			return false, false, false
		}
	}
	if overviewOnly[action] {
		return true, true, false
	}
	return true, true, cluster == nil
}

// poll leest de events sinds de vorige ronde.
func (s *Service) poll(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	after := s.cursor
	s.mu.Unlock()
	rows, err := s.q.ListDocEvents(ctx, store.ListDocEventsParams{After: after, Lim: eventBatch})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	var clusters []uuid.UUID
	if needsAll := slicesAny(rows); needsAll {
		ids, err := s.q.ListDocClusters(ctx)
		if err != nil {
			return err
		}
		for _, c := range ids {
			clusters = append(clusters, c.ID)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	queue := func(id uuid.UUID, cause string) {
		if _, ok := s.queued[id]; !ok {
			s.queued[id] = mark{since: now, cause: cause}
		}
	}
	for _, e := range rows {
		s.cursor = max(s.cursor, e.ID)
		touch, overview, all := Affects(e.Action, e.ClusterID)
		if !touch {
			continue
		}
		cause := "event:" + strconv.FormatInt(e.ID, 10) + ":" + e.Action
		if overview {
			queue(uuid.Nil, cause)
		}
		switch {
		case overviewOnly[e.Action]:
		case all:
			for _, id := range clusters {
				queue(id, cause)
			}
		case e.ClusterID != nil:
			queue(*e.ClusterID, cause)
		}
	}
	return nil
}

func slicesAny(rows []store.ListDocEventsRow) bool {
	for _, e := range rows {
		if _, _, all := Affects(e.Action, e.ClusterID); all {
			return true
		}
	}
	return false
}

// GenerateAll schrijft alle documenten opnieuw en geeft hoeveel er
// veranderden.
func (s *Service) GenerateAll(ctx context.Context, cause string) (int, error) {
	return s.generate(ctx, nil, cause)
}

// GenerateCluster schrijft de hoofdstukken van één cluster meteen opnieuw,
// zonder de gewone wachttijd.
func (s *Service) GenerateCluster(ctx context.Context, id uuid.UUID, cause string) (int, error) {
	return s.generate(ctx, map[uuid.UUID]string{id: cause})
}

// generate schrijft de documenten van de clusters in due (uuid.Nil is het
// overzicht) met hun oorzaak; nil is alles met de oorzaak in all.
func (s *Service) generate(ctx context.Context, due map[uuid.UUID]string, all ...string) (int, error) {
	f, err := s.Gather(ctx)
	if err != nil {
		return 0, err
	}
	now := s.Now()
	var docs []Rendered
	var causes []string
	for _, r := range Render(f, s.Opts) {
		id := uuid.Nil
		if r.ClusterID != nil {
			id = *r.ClusterID
		}
		cause, ok := due[id]
		if due == nil {
			cause, ok = all[0], true
		}
		if ok {
			docs = append(docs, r)
			causes = append(causes, cause)
		}
	}
	changed, err := s.save(ctx, now, docs, causes)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if due == nil {
		s.queued = map[uuid.UUID]mark{}
	} else {
		for id := range due {
			// Wat intussen bijkwam, blijft in de rij.
			if m, ok := s.queued[id]; ok && !m.since.After(now) {
				delete(s.queued, id)
			}
		}
	}
	s.mu.Unlock()
	return changed, nil
}

// save bewaart elk document waarvan de inhoud veranderde, onder de
// advisory lock. Heeft een andere server de lock, dan doet die het.
func (s *Service) save(ctx context.Context, now time.Time, docs []Rendered, causes []string) (int, error) {
	changed := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", lockID).Scan(&locked); err != nil || !locked {
			return err
		}
		q := store.New(tx)
		latest, err := q.LatestDocSnapshots(ctx)
		if err != nil {
			return err
		}
		have := map[string]string{}
		for _, l := range latest {
			have[docKey(l.Scope, l.Kind, l.ClusterID)] = l.Sha256
		}
		for i, d := range docs {
			sum := Hash(d.Content)
			if have[docKey(d.Scope, d.Kind, d.ClusterID)] == sum {
				continue
			}
			if _, err := q.InsertDocSnapshot(ctx, store.InsertDocSnapshotParams{
				Scope: d.Scope, ClusterID: d.ClusterID, Kind: d.Kind, Content: d.Content, Sha256: sum, Cause: causes[i], GeneratedAt: now,
			}); err != nil {
				return err
			}
			if err := q.PruneDocSnapshots(ctx, store.PruneDocSnapshotsParams{Scope: d.Scope, Kind: d.Kind, ClusterID: d.ClusterID, Keep: Keep}); err != nil {
				return err
			}
			changed++
		}
		if changed > 0 {
			s.markDirty()
			// De webinterface laadt de documentatie opnieuw.
			if _, err := tx.Exec(ctx, `SELECT pg_notify('cf_events', '{"docs":true}')`); err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

func docKey(scope, kind string, cluster *uuid.UUID) string {
	id := ""
	if cluster != nil {
		id = cluster.String()
	}
	return scope + "/" + kind + "/" + id
}

// Hash is de sha256 van een document, als hex.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Queued geeft het moment waarop de documentatie van een cluster (of met
// uuid.Nil het overzicht) opnieuw geschreven wordt; nil als er niets in de
// rij staat.
func (s *Service) Queued(id uuid.UUID) *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.queued[id]
	if !ok {
		return nil
	}
	t := m.since.Add(s.Settle)
	return &t
}

// --- Lezen ---

// Meta beschrijft een document en zijn nieuwste versie.
type Meta struct {
	Scope     string
	Kind      string
	Title     string
	Path      string
	ClusterID *uuid.UUID
	// Version is nil als er nog geen versie bewaard is.
	Version     *int64
	GeneratedAt *time.Time
}

// ClusterIndex zijn de documenten van één cluster.
type ClusterIndex struct {
	ID         uuid.UUID
	Name, Slug string
	Env        string
	Docs       []Meta
	// Changed is de nieuwste versie van een van zijn documenten.
	Changed *time.Time
	Queued  *time.Time
	// Summary is de samenvatting van de assistent; nil zonder.
	Summary *Summary
}

// Index is de lijst van alle documenten.
type Index struct {
	Overview Meta
	Clusters []ClusterIndex
	Queued   *time.Time
}

// List geeft alle documenten met hun nieuwste versie.
func (s *Service) List(ctx context.Context) (Index, error) {
	latest, err := s.q.LatestDocSnapshots(ctx)
	if err != nil {
		return Index{}, err
	}
	byKey := map[string]store.LatestDocSnapshotsRow{}
	for _, l := range latest {
		byKey[docKey(l.Scope, l.Kind, l.ClusterID)] = l
	}
	meta := func(scope, kind, slug string, cluster *uuid.UUID) Meta {
		m := Meta{Scope: scope, Kind: kind, Title: Title(kind), Path: Path(scope, kind, slug), ClusterID: cluster}
		if l, ok := byKey[docKey(scope, kind, cluster)]; ok {
			id, at := l.ID, l.GeneratedAt
			m.Version, m.GeneratedAt = &id, &at
		}
		return m
	}
	out := Index{Overview: meta(ScopeOverview, KindOverview, "", nil), Clusters: []ClusterIndex{}, Queued: s.Queued(uuid.Nil)}
	rows, err := s.q.ListDocClusters(ctx)
	if err != nil {
		return Index{}, err
	}
	sums, err := s.q.ListDocSummaries(ctx)
	if err != nil {
		return Index{}, err
	}
	summaries := map[uuid.UUID]*Summary{}
	for _, r := range sums {
		summaries[r.ClusterID] = &Summary{Text: r.Text, Model: r.Model, At: r.CreatedAt, By: r.Username}
	}
	for _, r := range rows {
		id := r.ID
		ci := ClusterIndex{ID: id, Name: r.Name, Slug: r.Slug, Env: r.Environment, Queued: s.Queued(id), Summary: summaries[id]}
		for _, k := range Kinds(r.Type) {
			m := meta(ScopeCluster, k, r.Slug, &id)
			if m.GeneratedAt != nil && (ci.Changed == nil || m.GeneratedAt.After(*ci.Changed)) {
				ci.Changed = m.GeneratedAt
			}
			ci.Docs = append(ci.Docs, m)
		}
		out.Clusters = append(out.Clusters, ci)
	}
	return out, nil
}

// Doc is één versie van een document.
type Doc struct {
	Meta
	ClusterName string
	// Content is de Markdown zoals hij bewaard is; Text heeft de kopregel
	// met het moment van schrijven erbij.
	Content string
	Sha256  string
	Cause   string
}

// Text is het document met de kopregel, zoals het gedownload wordt.
func (d Doc) Text(o Opts) string {
	at := time.Now()
	if d.GeneratedAt != nil {
		at = *d.GeneratedAt
	}
	return "*Geschreven door ClusterForge op " + o.timeText(at) + ". Wijzig dit bestand niet met de hand.*\n\n" + d.Content
}

// Get geeft de nieuwste versie van een document, of version als die
// gegeven is. Is er nog geen versie bewaard, dan schrijft Get het document
// in het geheugen, zonder het te bewaren.
func (s *Service) Get(ctx context.Context, scope, kind string, cluster *uuid.UUID, version *int64) (Doc, error) {
	if !Valid(scope, kind) || (scope == ScopeCluster) != (cluster != nil) {
		return Doc{}, ErrNotFound
	}
	slug, name := "", ""
	if cluster != nil {
		c, err := s.q.GetCluster(ctx, *cluster)
		if errors.Is(err, pgx.ErrNoRows) {
			return Doc{}, ErrNotFound
		}
		if err != nil {
			return Doc{}, err
		}
		// Het hoofdstuk Kubernetes is er alleen bij een Kubernetes-cluster.
		if !slices.Contains(Kinds(c.Type), kind) {
			return Doc{}, ErrNotFound
		}
		slug, name = c.Slug, c.Name
	}
	d := Doc{Meta: Meta{Scope: scope, Kind: kind, Title: Title(kind), Path: Path(scope, kind, slug), ClusterID: cluster}, ClusterName: name}
	var row store.DocSnapshot
	var err error
	if version != nil {
		row, err = s.q.GetDocSnapshot(ctx, *version)
		if err == nil && (row.Scope != scope || row.Kind != kind || docKey(scope, kind, row.ClusterID) != docKey(scope, kind, cluster)) {
			err = pgx.ErrNoRows
		}
	} else {
		row, err = s.q.GetLatestDocSnapshot(ctx, store.GetLatestDocSnapshotParams{Scope: scope, Kind: kind, ClusterID: cluster})
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows) && version != nil:
		return Doc{}, ErrNotFound
	case errors.Is(err, pgx.ErrNoRows):
		content, err := s.renderNow(ctx, scope, kind, cluster)
		if err != nil {
			return Doc{}, err
		}
		d.Content, d.Sha256 = content, Hash(content)
		return d, nil
	case err != nil:
		return Doc{}, err
	}
	id, at := row.ID, row.GeneratedAt
	d.Version, d.GeneratedAt, d.Content, d.Sha256, d.Cause = &id, &at, row.Content, row.Sha256, row.Cause
	return d, nil
}

// renderNow schrijft één document zonder het te bewaren.
func (s *Service) renderNow(ctx context.Context, scope, kind string, cluster *uuid.UUID) (string, error) {
	if scope == ScopeOverview {
		f, err := s.Gather(ctx)
		if err != nil {
			return "", err
		}
		return Overview(f, s.Opts), nil
	}
	c, err := s.GatherCluster(ctx, *cluster)
	if err != nil {
		return "", err
	}
	return Chapter(&c, kind, s.Opts), nil
}

// Version is één bewaarde versie van een document.
type Version struct {
	ID          int64
	GeneratedAt time.Time
	Sha256      string
	Cause       string
}

// History geeft de bewaarde versies van een document, nieuwste eerst.
func (s *Service) History(ctx context.Context, scope, kind string, cluster *uuid.UUID) ([]Version, error) {
	if !Valid(scope, kind) || (scope == ScopeCluster) != (cluster != nil) {
		return nil, ErrNotFound
	}
	rows, err := s.q.ListDocHistory(ctx, store.ListDocHistoryParams{Scope: scope, Kind: kind, ClusterID: cluster})
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(rows))
	for _, r := range rows {
		out = append(out, Version{ID: r.ID, GeneratedAt: r.GeneratedAt, Sha256: r.Sha256, Cause: r.Cause})
	}
	return out, nil
}

// CauseText zegt in gewone taal waarom een versie geschreven werd.
func CauseText(cause string) string {
	switch cause {
	case CauseStart:
		return "Bij de start van de server"
	case CauseNight:
		return "Elke nacht opnieuw"
	case CauseManual:
		return "Met de hand opnieuw geschreven"
	case CauseSummary:
		return "Samenvatting van de assistent gewijzigd"
	}
	if rest, ok := strings.CutPrefix(cause, "event:"); ok {
		if _, action, ok := strings.Cut(rest, ":"); ok {
			return "Na: " + events.Lookup(action).Label
		}
	}
	return cause
}

// Diff vergelijkt twee versies van hetzelfde document.
func (s *Service) Diff(ctx context.Context, scope, kind string, cluster *uuid.UUID, from, to int64) (string, int, error) {
	a, err := s.Get(ctx, scope, kind, cluster, &from)
	if err != nil {
		return "", 0, err
	}
	b, err := s.Get(ctx, scope, kind, cluster, &to)
	if err != nil {
		return "", 0, err
	}
	diff, changed := gitops.Diff(a.Content, b.Content, "versie "+strconv.FormatInt(from, 10), "versie "+strconv.FormatInt(to, 10))
	return diff, changed, nil
}

// Zip schrijft de nieuwste versie van alle documenten in een zip, met de
// mappen zoals ze straks in Git staan. Een document zonder bewaarde versie
// wordt in het geheugen geschreven.
func (s *Service) Zip(ctx context.Context, w io.Writer) error {
	idx, err := s.List(ctx)
	if err != nil {
		return err
	}
	metas := []Meta{idx.Overview}
	for _, c := range idx.Clusters {
		metas = append(metas, c.Docs...)
	}
	var fleet *Fleet
	zw := zip.NewWriter(w)
	for _, m := range metas {
		var text string
		if m.Version != nil {
			d, err := s.Get(ctx, m.Scope, m.Kind, m.ClusterID, m.Version)
			if err != nil {
				return err
			}
			text = d.Text(s.Opts)
		} else {
			if fleet == nil {
				f, err := s.Gather(ctx)
				if err != nil {
					return err
				}
				fleet = &f
			}
			text = (Doc{Content: renderFrom(fleet, m, s.Opts)}).Text(s.Opts)
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: m.Path, Method: zip.Deflate, Modified: s.Now()})
		if err != nil {
			return err
		}
		if _, err := io.Copy(fw, bytes.NewReader([]byte(text))); err != nil {
			return err
		}
	}
	return zw.Close()
}

func renderFrom(f *Fleet, m Meta, o Opts) string {
	if m.Scope == ScopeOverview {
		return Overview(*f, o)
	}
	for i := range f.Clusters {
		if m.ClusterID != nil && f.Clusters[i].ID == *m.ClusterID {
			return Chapter(&f.Clusters[i], m.Kind, o)
		}
	}
	return fmt.Sprintf("# %s\n\nDit document is er niet meer.\n", m.Title)
}
