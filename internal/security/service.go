// Package security is de kwetsbaarheids- en verhardingsmodule van
// ClusterForge (fase 27). M1: eigen scopes, de pakketlijst per node via
// agentprotocol 40, en het vaststellen van bekende kwetsbaarheden met de
// Debian Security Tracker en de CISA-catalogus van actief misbruikte
// kwetsbaarheden. Er wordt niets aangevallen: een "exploit check" is hier
// vaststellen of een bekende kwetsbaarheid van toepassing is.
package security

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Bus stuurt beveiligingsverzoeken naar een agent.
type Bus interface {
	Security(ctx context.Context, nodeID uuid.UUID, req protocol.SecurityRequest) (protocol.SecurityResponse, error)
}

// Service houdt de feeds bij, haalt de pakketlijsten op en maakt de
// bevindingen.
type Service struct {
	pool *pgxpool.Pool
	ev   *events.Writer
	bus  Bus
	log  *slog.Logger

	HTTP      *http.Client
	DebianURL string
	KEVURL    string
	// FeedEvery: zo vaak de feeds ververst worden; FeedRetry: zo lang na
	// een fout. InventoryEvery en InventoryRetry hetzelfde voor de
	// pakketlijsten.
	FeedEvery      time.Duration
	FeedRetry      time.Duration
	InventoryEvery time.Duration
	InventoryRetry time.Duration
	TickEvery      time.Duration
	Now            func() time.Time

	tick sync.Mutex
	mu   sync.Mutex
	// catalogs per release, tot de volgende feedverversing.
	catalogs   map[string]*Catalog
	forceFeeds bool
	forceNodes map[uuid.UUID]bool
	forceAll   bool
	// running: scans die nu lopen; base: de context van Loop, waarin
	// scans verder lopen nadat het verzoek dat ze startte klaar is.
	running map[uuid.UUID]bool
	base    context.Context
	wg      sync.WaitGroup
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, bus Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, ev: ev, bus: bus, log: log,
		HTTP:      &http.Client{Timeout: 5 * time.Minute},
		DebianURL: DefaultDebianURL, KEVURL: DefaultKEVURL,
		FeedEvery: 24 * time.Hour, FeedRetry: time.Hour,
		InventoryEvery: 6 * time.Hour, InventoryRetry: time.Hour,
		TickEvery: time.Minute, Now: time.Now,
		catalogs: map[string]*Catalog{}, forceNodes: map[uuid.UUID]bool{}, running: map[uuid.UUID]bool{}}
}

func (s *Service) event(ctx context.Context, actor events.Actor, subjectType, subjectID string, cluster *uuid.UUID, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	if err := s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: subjectType, SubjectID: subjectID, ClusterID: cluster,
		Action: action, Payload: payload}); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

// Loop draait de ronde elke TickEvery, de eerste na een halve minuut zodat
// de agents eerst verbinden.
func (s *Service) Loop(ctx context.Context) {
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	if err := s.failStaleRuns(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("oude scans afsluiten mislukt", "err", err)
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	t := time.NewTicker(s.TickEvery)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("beveiligingsronde mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Refresh vraagt een ronde met verse feeds en/of pakketlijsten. Leeg
// nodeID met nodes=true: alle nodes.
func (s *Service) Refresh(feeds, nodes bool, nodeID *uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if feeds {
		s.forceFeeds = true
	}
	if nodes {
		if nodeID != nil {
			s.forceNodes[*nodeID] = true
		} else {
			s.forceAll = true
		}
	}
}

// Tick is één ronde.
func (s *Service) Tick(ctx context.Context) error {
	s.tick.Lock()
	defer s.tick.Unlock()
	s.mu.Lock()
	forceFeeds, forceAll, forceNodes := s.forceFeeds, s.forceAll, s.forceNodes
	s.forceFeeds, s.forceAll, s.forceNodes = false, false, map[uuid.UUID]bool{}
	s.mu.Unlock()

	now := s.Now()
	nodes, err := s.nodes(ctx, "")
	if err != nil {
		return err
	}
	releases, err := s.releases(ctx, nodes)
	if err != nil {
		return err
	}
	feeds, err := s.feeds(ctx)
	if err != nil {
		return err
	}
	loaded, err := s.loadedReleases(ctx)
	if err != nil {
		return err
	}
	changed := false
	due := func(f *Feed) bool {
		if forceFeeds {
			return true
		}
		if f.OKAt != nil && now.Sub(*f.OKAt) < s.FeedEvery {
			return false
		}
		return f.FetchedAt == nil || now.Sub(*f.FetchedAt) >= s.FeedRetry || f.OKAt == nil && f.Error == ""
	}
	missing := false
	for _, r := range releases {
		if !slices.Contains(loaded, r) {
			missing = true
		}
	}
	if f := feeds[FeedKEV]; due(f) {
		n, etag, err := s.refreshKEV(ctx, f.etag)
		if err := s.feedDone(ctx, FeedKEV, n, etag, err); err != nil {
			return err
		}
		if err != nil {
			s.log.Warn("CISA KEV ophalen mislukt", "err", err)
		} else if n >= 0 {
			changed = true
		}
	}
	if f := feeds[FeedDebian]; due(f) || missing && (f.FetchedAt == nil || now.Sub(*f.FetchedAt) >= s.FeedRetry) {
		etag := f.etag
		if missing {
			etag = "" // een nieuwe release vraagt het hele bestand
		}
		n, etag, err := s.refreshDebian(ctx, releases, etag)
		if err := s.feedDone(ctx, FeedDebian, n, etag, err); err != nil {
			return err
		}
		if err != nil {
			s.log.Warn("Debian Security Tracker ophalen mislukt", "err", err)
		} else if n >= 0 {
			changed = true
		}
	}
	if changed {
		s.mu.Lock()
		s.catalogs = map[string]*Catalog{}
		s.mu.Unlock()
	}

	for _, n := range nodes {
		if !n.AuditSupported || !n.Online {
			continue
		}
		force := forceAll || forceNodes[n.NodeID]
		if !force && (n.AuditAt != nil && now.Sub(*n.AuditAt) < s.InventoryEvery || n.auditErrorAt != nil && now.Sub(*n.auditErrorAt) < s.InventoryRetry) {
			continue
		}
		if err := s.harden(ctx, n); err != nil {
			s.log.Debug("verharding uitlezen mislukt", "node", n.Hostname, "err", err)
		}
	}
	for _, n := range nodes {
		if !n.Supported || !n.Online {
			continue
		}
		force := forceAll || forceNodes[n.NodeID]
		if !force {
			if n.CollectedAt != nil && now.Sub(*n.CollectedAt) < s.InventoryEvery {
				continue
			}
			if n.ErrorAt != nil && now.Sub(*n.ErrorAt) < s.InventoryRetry {
				continue
			}
		}
		if err := s.collect(ctx, n); err != nil {
			s.log.Debug("pakketlijst ophalen mislukt", "node", n.Hostname, "err", err)
			continue
		}
		if !changed {
			if err := s.evaluate(ctx, n); err != nil {
				return err
			}
		}
	}
	if changed {
		if nodes, err = s.nodes(ctx, ""); err != nil {
			return err
		}
		for _, n := range nodes {
			if err := s.evaluate(ctx, n); err != nil {
				return err
			}
		}
	}
	if err := s.scheduleScans(ctx); err != nil {
		s.log.Warn("geplande scans starten mislukt", "err", err)
	}
	if err := s.tickTools(ctx, nodes); err != nil {
		s.log.Warn("tools bijwerken mislukt", "err", err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE security_findings SET status = 'open', accepted_until = NULL
WHERE status = 'aanvaard' AND accepted_until IS NOT NULL AND accepted_until < $1`, now)
	return err
}

func (s *Service) collect(ctx context.Context, n Node) error {
	ctx, cancel := context.WithTimeout(ctx, protocol.SecurityRequestTimeout)
	defer cancel()
	res, err := s.bus.Security(ctx, n.NodeID, protocol.SecurityRequest{Op: protocol.SecurityOpInventory})
	if err == nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	if err == nil && res.Inventory == nil {
		err = errors.New("de agent stuurde geen pakketlijst")
	}
	if err != nil {
		if e := s.inventoryFailed(ctx, n.NodeID, err.Error()); e != nil {
			return e
		}
		return err
	}
	return s.saveInventory(ctx, n.NodeID, res.Inventory)
}

// harden leest de verharding van een node uit en legt de regels erop.
func (s *Service) harden(ctx context.Context, n Node) error {
	rctx, cancel := context.WithTimeout(ctx, protocol.SecurityRequestTimeout)
	defer cancel()
	res, err := s.bus.Security(rctx, n.NodeID, protocol.SecurityRequest{Op: protocol.SecurityOpAudit})
	if err == nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	if err == nil && res.Audit == nil {
		err = errors.New("de agent stuurde geen verharding")
	}
	if err != nil {
		if e := s.auditFailed(ctx, n.NodeID, err.Error()); e != nil {
			return e
		}
		return err
	}
	results, drafts, score := EvaluateHardening(HardeningContext{Audit: res.Audit, LXC: n.Virtualization == "lxc", Docker: n.docker, Router: n.router})
	if err := s.saveAudit(ctx, n.NodeID, res.Audit, results, score); err != nil {
		return err
	}
	return s.sync(ctx, n, KindHardening, drafts)
}

// releases zijn de Debian-releases op de nodes, plus DefaultReleases.
func (s *Service) releases(ctx context.Context, nodes []Node) ([]string, error) {
	out := slices.Clone(DefaultReleases)
	for _, n := range nodes {
		if n.OS == "debian" && n.Codename != "" && !slices.Contains(out, n.Codename) {
			out = append(out, n.Codename)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (s *Service) loadedReleases(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT release FROM security_debian`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// fetch haalt een feed op. Bij 304 is body nil.
func (s *Service) fetch(ctx context.Context, url, etag string) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ClusterForge-security-feed")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%s niet bereikbaar (mag de server naar buiten via HTTPS?): %w", url, err)
	}
	if res.StatusCode == http.StatusNotModified {
		_ = res.Body.Close()
		return nil, etag, nil
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		return nil, "", fmt.Errorf("%s antwoordde %s", url, res.Status)
	}
	return res.Body, res.Header.Get("ETag"), nil
}

// refreshKEV geeft het aantal regels, of -1 als er niets veranderde.
func (s *Service) refreshKEV(ctx context.Context, etag string) (int, string, error) {
	body, etag, err := s.fetch(ctx, s.KEVURL, etag)
	if err != nil || body == nil {
		return -1, etag, err
	}
	defer func() { _ = body.Close() }()
	list, err := ParseKEV(io.LimitReader(body, 64<<20))
	if err != nil {
		return 0, "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM security_kev`); err != nil {
		return 0, "", err
	}
	seen := map[string]bool{}
	rows := make([][]any, 0, len(list))
	for _, k := range list {
		if seen[k.CVE] {
			continue
		}
		seen[k.CVE] = true
		rows = append(rows, []any{k.CVE, k.Vendor, k.Product, k.Name, k.DateAdded, k.Ransomware})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"security_kev"}, []string{"cve", "vendor", "product", "name", "date_added", "ransomware"},
		pgx.CopyFromRows(rows)); err != nil {
		return 0, "", err
	}
	return len(rows), etag, tx.Commit(ctx)
}

// refreshDebian geeft het aantal regels, of -1 als er niets veranderde.
func (s *Service) refreshDebian(ctx context.Context, releases []string, etag string) (int, string, error) {
	body, etag, err := s.fetch(ctx, s.DebianURL, etag)
	if err != nil || body == nil {
		return -1, etag, err
	}
	defer func() { _ = body.Close() }()
	var entries, cves [][]any
	err = ParseDebian(io.LimitReader(body, 512<<20), releases,
		func(e DebianEntry) error {
			entries = append(entries, []any{e.Release, e.Package, e.CVE, e.Status, e.FixedVersion, e.Urgency})
			return nil
		},
		func(c CVEInfo) error {
			cves = append(cves, []any{c.CVE, c.Description, c.Scope})
			return nil
		})
	if err != nil {
		return 0, "", err
	}
	if len(entries) == 0 {
		return 0, "", errors.New("Security Tracker: geen enkele regel voor " + fmt.Sprint(releases))
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM security_debian`); err != nil {
		return 0, "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM security_cves`); err != nil {
		return 0, "", err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"security_debian"}, []string{"release", "package", "cve", "status", "fixed_version", "urgency"},
		pgx.CopyFromRows(entries)); err != nil {
		return 0, "", err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"security_cves"}, []string{"cve", "description", "scope"}, pgx.CopyFromRows(cves)); err != nil {
		return 0, "", err
	}
	return len(entries), etag, tx.Commit(ctx)
}

// catalog geeft de kennis van de feeds voor één release (gecachet).
func (s *Service) catalog(ctx context.Context, release string) (*Catalog, error) {
	s.mu.Lock()
	c := s.catalogs[release]
	s.mu.Unlock()
	if c != nil {
		return c, nil
	}
	c = &Catalog{Debian: map[string][]DebianEntry{}, KEV: map[string]KEVEntry{}, CVEs: map[string]CVEInfo{}}
	rows, err := s.pool.Query(ctx, `SELECT package, cve, status, fixed_version, urgency FROM security_debian WHERE release = $1`, release)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e := DebianEntry{Release: release}
		if err := rows.Scan(&e.Package, &e.CVE, &e.Status, &e.FixedVersion, &e.Urgency); err != nil {
			rows.Close()
			return nil, err
		}
		c.Debian[e.Package] = append(c.Debian[e.Package], e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT cve, vendor, product, name, ransomware FROM security_kev`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k KEVEntry
		if err := rows.Scan(&k.CVE, &k.Vendor, &k.Product, &k.Name, &k.Ransomware); err != nil {
			rows.Close()
			return nil, err
		}
		c.KEV[k.CVE] = k
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.catalogs[release] = c
	s.mu.Unlock()
	return c, nil
}

// describe vult de beschrijvingen aan voor de CVE's die een node raken.
func (s *Service) describe(ctx context.Context, cat *Catalog, pkgs []protocol.DebPackage) (Catalog, error) {
	ids := []string{}
	seen := map[string]bool{}
	for _, p := range pkgs {
		if seen[p.Source] {
			continue
		}
		seen[p.Source] = true
		for _, e := range cat.Debian[p.Source] {
			ids = append(ids, e.CVE)
		}
	}
	out := Catalog{Debian: cat.Debian, KEV: cat.KEV, CVEs: map[string]CVEInfo{}}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT cve, description, scope FROM security_cves WHERE cve = ANY($1)`, ids)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c CVEInfo
		if err := rows.Scan(&c.CVE, &c.Description, &c.Scope); err != nil {
			return out, err
		}
		out.CVEs[c.CVE] = c
	}
	return out, rows.Err()
}

// evaluate maakt de cve-bevindingen van één node opnieuw.
func (s *Service) evaluate(ctx context.Context, n Node) error {
	osID, codename, pkgs, ok, err := s.loadInventory(ctx, n.NodeID)
	if err != nil || !ok {
		return err
	}
	if osID != "debian" || codename == "" {
		// Geen Debian: geen cve-check; oude bevindingen weg.
		return s.sync(ctx, n, KindCVE, nil)
	}
	loaded, err := s.loadedReleases(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(loaded, codename) {
		return nil // nog geen feed voor deze release: niets veranderen
	}
	cat, err := s.catalog(ctx, codename)
	if err != nil {
		return err
	}
	full, err := s.describe(ctx, cat, pkgs)
	if err != nil {
		return err
	}
	return s.sync(ctx, n, KindCVE, MatchPackages(codename, pkgs, full))
}
