package crowdsec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Centrale blocklists (fase 47). ClusterForge bewaart de lijsten en zet ze
// op elke beheerde node met cscli decisions import; de reden (in CrowdSec:
// het scenario) is de tag cf-list:<slug>:<stempel>. Zo werkt alles lokaal,
// zonder de CrowdSec Console.

// ListDuration is hoe lang een geïmporteerde lijst op een node geldt. De
// server ververst hem ruim op tijd (ListRefresh); valt ClusterForge weg,
// dan verdwijnen de bans vanzelf na deze duur.
const (
	ListDuration = "168h"
	ListRefresh  = 72 * time.Hour
)

// MaxListEntries is het grootste aantal waarden in één lijst.
const MaxListEntries = 100000

// Soorten lijsten.
const (
	ListOwn    = "own"
	ListAuto   = "auto"
	ListPublic = "public"
)

// List is een centrale blocklist.
type List struct {
	ID             uuid.UUID  `json:"id"`
	Slug           string     `json:"slug"`
	Kind           string     `json:"kind"`
	Name           string     `json:"name"`
	Source         string     `json:"source"`
	URL            string     `json:"url"`
	Enabled        bool       `json:"enabled"`
	Scope          string     `json:"scope"`
	ClusterID      *uuid.UUID `json:"cluster_id"`
	RefreshMinutes int        `json:"refresh_minutes"`
	Pinned         bool       `json:"pinned"`
	Generation     int64      `json:"generation"`
	Count          int        `json:"count"`
	Dropped        int        `json:"dropped"`
	FetchedAt      *time.Time `json:"fetched_at"`
	FetchError     string     `json:"fetch_error"`
	FetchFailedAt  *time.Time `json:"fetch_failed_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// Info komt uit de catalogus: wie de lijst maakt en de voorwaarden.
	Info string `json:"info"`
	// Nodes: op hoeveel nodes de lijst hoort en op hoeveel de huidige
	// inhoud staat; Failed zijn nodes met een fout.
	NodesWanted int         `json:"nodes_wanted"`
	NodesSynced int         `json:"nodes_synced"`
	Failed      []NodeError `json:"failed"`
	hash        string
	fetchETag   string
	fetchMod    string
}

// NodeError is een fout op een node.
type NodeError struct {
	Hostname string `json:"hostname"`
	Error    string `json:"error"`
}

// Entry is een waarde in een lijst.
type Entry struct {
	Value     string     `json:"value"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	AddedBy   string     `json:"added_by"`
}

// Version is een bewaarde inhoud van een lijst.
type Version struct {
	ID        int64     `json:"id"`
	Count     int       `json:"count"`
	Added     int       `json:"added"`
	Removed   int       `json:"removed"`
	Note      string    `json:"note"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

// Dropped is een waarde die nooit geblokkeerd wordt, met de reden.
type Dropped struct {
	Value string `json:"value"`
	Why   string `json:"why"`
}

// Diff is het voorbeeld van een wijziging.
type Diff struct {
	Added     []string  `json:"added"`
	Removed   []string  `json:"removed"`
	Updated   []string  `json:"updated"`
	Refused   []Dropped `json:"refused"`
	AddedN    int       `json:"added_count"`
	RemovedN  int       `json:"removed_count"`
	UpdatedN  int       `json:"updated_count"`
	RefusedN  int       `json:"refused_count"`
	Count     int       `json:"count"`
	Nodes     int       `json:"nodes"`
	Applied   bool      `json:"applied"`
	VersionID int64     `json:"version_id,omitempty"`
}

const diffSample = 50

func (d *Diff) finish() {
	d.AddedN, d.RemovedN, d.UpdatedN, d.RefusedN = len(d.Added), len(d.Removed), len(d.Updated), len(d.Refused)
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Updated)
	if len(d.Added) > diffSample {
		d.Added = d.Added[:diffSample]
	}
	if len(d.Removed) > diffSample {
		d.Removed = d.Removed[:diffSample]
	}
	if len(d.Updated) > diffSample {
		d.Updated = d.Updated[:diffSample]
	}
	if len(d.Refused) > diffSample {
		d.Refused = d.Refused[:diffSample]
	}
}

// neverBlock zijn netwerken die geen lijst ooit blokkeert, ook niet als
// de whitelist ze mist: privé, loopback, link-local, CGNAT en multicast
// (VRRP van Keepalived gebruikt 224.0.0.18).
var neverBlock = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/3",
	"::/128", "::1/128", "::ffff:0:0/96", "fc00::/7", "fe80::/10", "ff00::/8",
}

// Guard weigert waarden die eigen of speciale netwerken raken.
type Guard struct {
	nets []guardNet
}

type guardNet struct {
	p   netip.Prefix
	why string
}

// NewGuard maakt een Guard met de whitelist, de adressen van de nodes en
// de vaste netwerken.
func NewGuard(whitelist, own []string) Guard {
	var g Guard
	add := func(v, why string) {
		if p, err := netip.ParsePrefix(v); err == nil {
			g.nets = append(g.nets, guardNet{p.Masked(), why})
			return
		}
		if a, err := netip.ParseAddr(v); err == nil {
			g.nets = append(g.nets, guardNet{netip.PrefixFrom(a.WithZone(""), a.BitLen()), why})
		}
	}
	for _, w := range whitelist {
		add(w, "whitelist "+w)
	}
	for _, o := range own {
		// node_status.addresses kan 192.0.2.4/24 zijn: het adres telt.
		if p, err := netip.ParsePrefix(o); err == nil {
			o = p.Addr().String()
		}
		add(o, "adres van een eigen node")
	}
	for _, n := range neverBlock {
		add(n, "eigen of speciaal netwerk "+n)
	}
	return g
}

// Check geeft de reden waarom een waarde niet mag, of "".
func (g Guard) Check(v string) (norm, why string) {
	_, norm, err := protocol.ListValue(v)
	if err != nil {
		return "", "geen IP-adres of range"
	}
	var p netip.Prefix
	if a, err := netip.ParseAddr(norm); err == nil {
		p = netip.PrefixFrom(a, a.BitLen())
	} else {
		p = netip.MustParsePrefix(norm)
	}
	if p.Addr().Is4() && p.Bits() < 8 || p.Addr().Is6() && p.Bits() < 16 {
		return norm, "te breed (kleiner dan /8 of /16)"
	}
	for _, n := range g.nets {
		if n.p.Addr().Is4() == p.Addr().Is4() && n.p.Overlaps(p) {
			return norm, n.why
		}
	}
	return norm, ""
}

func (s *Service) guard(ctx context.Context) (Guard, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return Guard{}, err
	}
	var own []string
	rows, err := s.pool.Query(ctx, `SELECT unnest(addresses) FROM node_status`)
	if err != nil {
		return Guard{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return Guard{}, err
		}
		own = append(own, a)
	}
	if err := rows.Err(); err != nil {
		return Guard{}, err
	}
	return NewGuard(set.Whitelist, own), nil
}

const listSQL = `SELECT id, slug, kind, name, source, url, enabled, scope, cluster_id, refresh_minutes, pinned, generation, hash,
	count, dropped, fetched_at, fetch_etag, fetch_modified, fetch_error, fetch_failed_at, updated_at FROM crowdsec_lists`

func scanList(row pgx.Row) (List, error) {
	var l List
	err := row.Scan(&l.ID, &l.Slug, &l.Kind, &l.Name, &l.Source, &l.URL, &l.Enabled, &l.Scope, &l.ClusterID, &l.RefreshMinutes,
		&l.Pinned, &l.Generation, &l.hash, &l.Count, &l.Dropped, &l.FetchedAt, &l.fetchETag, &l.fetchMod, &l.FetchError,
		&l.FetchFailedAt, &l.UpdatedAt)
	if p, ok := PublicByKey(l.Source); ok {
		l.Info = p.Info
	}
	l.Failed = []NodeError{}
	return l, err
}

// ErrListNotFound: geen lijst met dat id.
var ErrListNotFound = errors.New("lijst niet gevonden")

func (s *Service) list(ctx context.Context, q pgx.Tx, id uuid.UUID) (List, error) {
	var row pgx.Row
	if q != nil {
		row = q.QueryRow(ctx, listSQL+` WHERE id = $1 FOR UPDATE`, id)
	} else {
		row = s.pool.QueryRow(ctx, listSQL+` WHERE id = $1`, id)
	}
	l, err := scanList(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return List{}, ErrListNotFound
	}
	return l, err
}

func (s *Service) listBySlug(ctx context.Context, slug string) (List, error) {
	l, err := scanList(s.pool.QueryRow(ctx, listSQL+` WHERE slug = $1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return List{}, ErrListNotFound
	}
	return l, err
}

// Lists geeft alle lijsten met hun stand op de nodes.
func (s *Service) Lists(ctx context.Context) ([]List, error) {
	rows, err := s.pool.Query(ctx, listSQL+` ORDER BY CASE kind WHEN 'own' THEN 0 WHEN 'auto' THEN 1 ELSE 2 END, lower(name)`)
	if err != nil {
		return nil, err
	}
	var out []List
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	state, err := s.listNodeState(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		l := &out[i]
		for _, n := range nodes {
			if !l.wants(n) {
				continue
			}
			l.NodesWanted++
			st := state[[2]uuid.UUID{n.NodeID, l.ID}]
			switch {
			case l.Count > 0 && st.gen == l.Generation && st.Error == "":
				l.NodesSynced++
			case l.Count == 0 && st.Error == "":
				l.NodesSynced++
			}
			if st.Error != "" {
				l.Failed = append(l.Failed, NodeError{Hostname: n.Hostname, Error: st.Error})
			} else if n.Protocol < protocol.CrowdSecListsSince {
				l.Failed = append(l.Failed, NodeError{Hostname: n.Hostname,
					Error: fmt.Sprintf("agent te oud (protocol %d nodig): Nodes → Agents bijwerken", protocol.CrowdSecListsSince)})
			}
		}
	}
	return out, nil
}

// wants: de lijst hoort op deze node. Een node telt als CrowdSec er door
// ClusterForge beheerd wordt en geïnstalleerd is.
func (l List) wants(n Node) bool {
	if !l.Enabled || !n.Enabled || !n.Managed || n.Status == nil || !n.Status.Installed {
		return false
	}
	if l.Scope == "cluster" {
		return l.ClusterID != nil && n.ClusterID != nil && *l.ClusterID == *n.ClusterID
	}
	return true
}

type listNodeRow struct {
	Tag       string
	gen       int64
	Count     int
	AppliedAt *time.Time
	Error     string
	FailedAt  *time.Time
}

func (s *Service) listNodeState(ctx context.Context) (map[[2]uuid.UUID]listNodeRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT node_id, list_id, tag, count, applied_at, error, failed_at FROM crowdsec_list_nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]uuid.UUID]listNodeRow{}
	for rows.Next() {
		var n, l uuid.UUID
		var r listNodeRow
		if err := rows.Scan(&n, &l, &r.Tag, &r.Count, &r.AppliedAt, &r.Error, &r.FailedAt); err != nil {
			return nil, err
		}
		r.gen = tagGen(r.Tag)
		out[[2]uuid.UUID{n, l}] = r
	}
	return out, rows.Err()
}

// De tag op een node is cf-list:<slug>:<generatie*1000 + teller>; de
// teller maakt een nieuwe tag bij het verversen van dezelfde inhoud.
func listTag(slug string, gen int64, n int) string {
	return fmt.Sprintf("%s%s:%d", protocol.CrowdSecListPrefix, slug, gen*1000+int64(n%1000))
}

func tagGen(tag string) int64 {
	i := strings.LastIndexByte(tag, ':')
	if i < 0 {
		return -1
	}
	var v int64
	if _, err := fmt.Sscan(tag[i+1:], &v); err != nil {
		return -1
	}
	return v / 1000
}

func tagSlug(tag string) string {
	t := strings.TrimPrefix(tag, protocol.CrowdSecListPrefix)
	if i := strings.LastIndexByte(t, ':'); i >= 0 {
		return t[:i]
	}
	return ""
}

// Entries geeft de waarden van een lijst; q filtert op een stuk van de
// waarde of de reden.
func (s *Service) Entries(ctx context.Context, id uuid.UUID, q string, limit int) ([]Entry, int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	q = "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	rows, err := s.pool.Query(ctx, `SELECT e.value, e.reason, e.expires_at, e.created_at, coalesce(u.username, ''), count(*) OVER ()
		FROM crowdsec_list_entries e LEFT JOIN users u ON u.id = e.added_by
		WHERE e.list_id = $1 AND (e.value ILIKE $2 OR e.reason ILIKE $2)
		ORDER BY e.created_at DESC, e.value LIMIT $3`, id, q, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Entry{}
	total := 0
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Value, &e.Reason, &e.ExpiresAt, &e.CreatedAt, &e.AddedBy, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// Versions geeft de bewaarde versies van een lijst, nieuwste eerst.
func (s *Service) Versions(ctx context.Context, id uuid.UUID) ([]Version, error) {
	rows, err := s.pool.Query(ctx, `SELECT v.id, v.count, v.added, v.removed, v.note, coalesce(u.username, ''), v.created_at
		FROM crowdsec_list_versions v LEFT JOIN users u ON u.id = v.actor WHERE v.list_id = $1 ORDER BY v.id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.Count, &v.Added, &v.Removed, &v.Note, &v.Actor, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type storedEntry struct {
	Value     string     `json:"v"`
	Reason    string     `json:"r,omitempty"`
	ExpiresAt *time.Time `json:"e,omitempty"`
}

func (s *Service) entriesTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (map[string]storedEntry, error) {
	rows, err := tx.Query(ctx, `SELECT value, reason, expires_at FROM crowdsec_list_entries WHERE list_id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]storedEntry{}
	for rows.Next() {
		var e storedEntry
		if err := rows.Scan(&e.Value, &e.Reason, &e.ExpiresAt); err != nil {
			return nil, err
		}
		out[e.Value] = e
	}
	return out, rows.Err()
}

// keepVersions is hoeveel versies een lijst bewaart.
func keepVersions(kind string) int {
	if kind == ListPublic {
		return 5
	}
	return 30
}

// replaceEntries zet de inhoud van een lijst, bewaart een versie en rekent
// de werkzame inhoud opnieuw uit.
func (s *Service) replaceEntries(ctx context.Context, tx pgx.Tx, l List, want map[string]storedEntry, added, removed int,
	note string, actor *uuid.UUID, g Guard) (int64, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM crowdsec_list_entries WHERE list_id = $1`, l.ID); err != nil {
		return 0, err
	}
	rows := make([][]any, 0, len(want))
	snap := make([]storedEntry, 0, len(want))
	for _, e := range want {
		rows = append(rows, []any{l.ID, e.Value, e.Reason, e.ExpiresAt, actor})
		snap = append(snap, e)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"crowdsec_list_entries"}, []string{"list_id", "value", "reason", "expires_at", "added_by"},
		pgx.CopyFromRows(rows)); err != nil {
		return 0, err
	}
	sort.Slice(snap, func(i, j int) bool { return snap[i].Value < snap[j].Value })
	b, err := json.Marshal(snap)
	if err != nil {
		return 0, err
	}
	var vid int64
	if err := tx.QueryRow(ctx, `INSERT INTO crowdsec_list_versions (list_id, count, added, removed, entries, note, actor)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, l.ID, len(want), added, removed, b, note, actor).Scan(&vid); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM crowdsec_list_versions WHERE list_id = $1 AND id NOT IN
		(SELECT id FROM crowdsec_list_versions WHERE list_id = $1 ORDER BY id DESC LIMIT $2)`, l.ID, keepVersions(l.Kind)); err != nil {
		return 0, err
	}
	if err := s.recompute(ctx, tx, l, g); err != nil {
		return 0, err
	}
	return vid, nil
}

// effective geeft de waarden die echt op de nodes horen: niet verlopen en
// niet geweigerd door de Guard.
func effective(entries map[string]storedEntry, g Guard, now time.Time) (vals []string, dropped []Dropped) {
	for _, e := range entries {
		if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			continue
		}
		norm, why := g.Check(e.Value)
		if why != "" {
			dropped = append(dropped, Dropped{Value: e.Value, Why: why})
			continue
		}
		vals = append(vals, norm)
	}
	sort.Strings(vals)
	vals = slices.Compact(vals)
	sort.Slice(dropped, func(i, j int) bool { return dropped[i].Value < dropped[j].Value })
	return vals, dropped
}

func hashValues(vals []string) string {
	h := sha256.Sum256([]byte(strings.Join(vals, "\n")))
	return hex.EncodeToString(h[:8])
}

// recompute rekent de werkzame inhoud uit; een andere inhoud is een nieuwe
// generatie, die de synchronisatie naar de nodes brengt.
func (s *Service) recompute(ctx context.Context, tx pgx.Tx, l List, g Guard) error {
	entries, err := s.entriesTx(ctx, tx, l.ID)
	if err != nil {
		return err
	}
	vals, dropped := effective(entries, g, s.Now())
	h := hashValues(vals)
	if h == l.hash && len(vals) == l.Count && len(dropped) == l.Dropped {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE crowdsec_lists SET generation = generation + CASE WHEN hash = $2 THEN 0 ELSE 1 END,
		hash = $2, count = $3, dropped = $4, updated_at = now() WHERE id = $1`, l.ID, h, len(vals), len(dropped))
	return err
}

// Recompute rekent alle lijsten opnieuw uit, bv. na een andere whitelist
// of als er waarden verlopen zijn.
func (s *Service) RecomputeAll(ctx context.Context) error {
	g, err := s.guard(ctx)
	if err != nil {
		return err
	}
	ls, err := s.allLists(ctx)
	if err != nil {
		return err
	}
	for _, l := range ls {
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return s.recompute(ctx, tx, l, g) }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) allLists(ctx context.Context) ([]List, error) {
	rows, err := s.pool.Query(ctx, listSQL+` ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []List
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// EffectiveValues geeft de werkzame inhoud van een lijst.
func (s *Service) EffectiveValues(ctx context.Context, id uuid.UUID) ([]string, []Dropped, error) {
	g, err := s.guard(ctx)
	if err != nil {
		return nil, nil, err
	}
	var vals []string
	var dropped []Dropped
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		entries, err := s.entriesTx(ctx, tx, id)
		if err != nil {
			return err
		}
		vals, dropped = effective(entries, g, s.Now())
		return nil
	})
	return vals, dropped, err
}

// EntryInput is een waarde om toe te voegen.
type EntryInput struct {
	Value  string
	Reason string
	// ExpiresIn: na hoeveel tijd de waarde vervalt; 0 is nooit.
	ExpiresIn time.Duration
}

// ChangeEntries voegt waarden toe of haalt ze weg in een eigen of de
// automatische lijst. Met dryRun is het alleen een voorbeeld.
func (s *Service) ChangeEntries(ctx context.Context, actor events.Actor, userID *uuid.UUID, id uuid.UUID, add []EntryInput,
	remove []string, dryRun bool) (Diff, error) {
	if len(add)+len(remove) == 0 {
		return Diff{}, &FieldError{"geen waarden om toe te voegen of weg te halen"}
	}
	g, err := s.guard(ctx)
	if err != nil {
		return Diff{}, err
	}
	var d Diff
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := s.list(ctx, tx, id)
		if err != nil {
			return err
		}
		if l.Kind == ListPublic {
			return &FieldError{"een publieke lijst komt van de bron; zet waarden in de eigen blocklist"}
		}
		cur, err := s.entriesTx(ctx, tx, id)
		if err != nil {
			return err
		}
		want := make(map[string]storedEntry, len(cur))
		for k, v := range cur {
			want[k] = v
		}
		now := s.Now()
		for _, r := range remove {
			_, norm, err := protocol.ListValue(strings.TrimSpace(r))
			if err != nil {
				return &FieldError{err.Error()}
			}
			if _, ok := want[norm]; ok {
				delete(want, norm)
				d.Removed = append(d.Removed, norm)
			}
		}
		for _, a := range add {
			norm, why := g.Check(strings.TrimSpace(a.Value))
			if norm == "" {
				return &FieldError{fmt.Sprintf("ongeldig IP-adres of range %q", a.Value)}
			}
			if why != "" {
				d.Refused = append(d.Refused, Dropped{Value: norm, Why: why})
				continue
			}
			e := storedEntry{Value: norm, Reason: reason(strings.TrimSpace(a.Reason))}
			if a.ExpiresIn > 0 {
				t := now.Add(a.ExpiresIn).Truncate(time.Second)
				e.ExpiresAt = &t
			}
			old, ok := want[norm]
			want[norm] = e
			switch {
			case !ok:
				d.Added = append(d.Added, norm)
			case old.Reason != e.Reason || !sameTime(old.ExpiresAt, e.ExpiresAt):
				d.Updated = append(d.Updated, norm)
			}
		}
		if len(want) > MaxListEntries {
			return &FieldError{fmt.Sprintf("een lijst heeft hoogstens %d waarden", MaxListEntries)}
		}
		d.Count = len(want)
		d.Nodes, err = s.nodesFor(ctx, l)
		if err != nil {
			return err
		}
		if dryRun || len(d.Added)+len(d.Removed)+len(d.Updated) == 0 {
			return nil
		}
		note := fmt.Sprintf("%d erbij, %d weg", len(d.Added), len(d.Removed))
		if len(d.Updated) > 0 {
			note += fmt.Sprintf(", %d gewijzigd", len(d.Updated))
		}
		d.VersionID, err = s.replaceEntries(ctx, tx, l, want, len(d.Added), len(d.Removed), note, userID, g)
		if err != nil {
			return err
		}
		d.Applied = true
		if len(d.Removed) > 0 {
			// Wie met de hand weghaalt, wil niet dat de automatische
			// regels het IP meteen opnieuw blokkeren.
			if _, err := tx.Exec(ctx, `INSERT INTO crowdsec_offenders (value, offenses, last_at) SELECT unnest($1::text[]), 0, now()
				ON CONFLICT (value) DO UPDATE SET last_at = now()`, d.Removed); err != nil {
				return err
			}
		}
		s.listEvent(ctx, actor, l, "crowdsec.list_changed", map[string]any{
			"added": first(d.Added, 20), "removed": first(d.Removed, 20), "added_count": len(d.Added),
			"removed_count": len(d.Removed), "updated_count": len(d.Updated), "version_id": d.VersionID,
		})
		return nil
	})
	d.finish()
	if err == nil && d.Applied {
		s.kickLists()
	}
	return d, err
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func first(v []string, n int) []string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

func (s *Service) nodesFor(ctx context.Context, l List) (int, error) {
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	l.Enabled = true
	for _, nd := range nodes {
		if l.wants(nd) {
			n++
		}
	}
	return n, nil
}

func (s *Service) listEvent(ctx context.Context, actor events.Actor, l List, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["list"], payload["name"] = l.Slug, l.Name
	if err := s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "crowdsec", SubjectID: "list:" + l.Slug,
		Action: action, Payload: payload}); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

// Rollback zet een lijst terug naar een bewaarde versie. Een publieke lijst
// wordt dan vastgezet tot "Opnieuw ophalen".
func (s *Service) Rollback(ctx context.Context, actor events.Actor, userID *uuid.UUID, id uuid.UUID, versionID int64, dryRun bool) (Diff, error) {
	g, err := s.guard(ctx)
	if err != nil {
		return Diff{}, err
	}
	var d Diff
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := s.list(ctx, tx, id)
		if err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT entries FROM crowdsec_list_versions WHERE id = $1 AND list_id = $2`, versionID, id).Scan(&raw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &FieldError{"die versie bestaat niet (meer)"}
			}
			return err
		}
		var snap []storedEntry
		if err := json.Unmarshal(raw, &snap); err != nil {
			return err
		}
		cur, err := s.entriesTx(ctx, tx, id)
		if err != nil {
			return err
		}
		want := make(map[string]storedEntry, len(snap))
		for _, e := range snap {
			want[e.Value] = e
			if old, ok := cur[e.Value]; !ok {
				d.Added = append(d.Added, e.Value)
			} else if old.Reason != e.Reason || !sameTime(old.ExpiresAt, e.ExpiresAt) {
				d.Updated = append(d.Updated, e.Value)
			}
		}
		for v := range cur {
			if _, ok := want[v]; !ok {
				d.Removed = append(d.Removed, v)
			}
		}
		d.Count = len(want)
		if d.Nodes, err = s.nodesFor(ctx, l); err != nil {
			return err
		}
		if dryRun {
			return nil
		}
		note := fmt.Sprintf("teruggedraaid naar versie %d", versionID)
		if d.VersionID, err = s.replaceEntries(ctx, tx, l, want, len(d.Added), len(d.Removed), note, userID, g); err != nil {
			return err
		}
		if l.Kind == ListPublic {
			if _, err := tx.Exec(ctx, `UPDATE crowdsec_lists SET pinned = true WHERE id = $1`, id); err != nil {
				return err
			}
		}
		d.Applied = true
		s.listEvent(ctx, actor, l, "crowdsec.list_rolled_back", map[string]any{
			"to_version": versionID, "version_id": d.VersionID, "added_count": len(d.Added), "removed_count": len(d.Removed),
		})
		return nil
	})
	d.finish()
	if err == nil && d.Applied {
		s.kickLists()
	}
	return d, err
}

// ListInput maakt of wijzigt een lijst.
type ListInput struct {
	Source         string
	URL            string
	Name           string
	Enabled        *bool
	Scope          string
	ClusterID      *uuid.UUID
	RefreshMinutes int
}

// AddPublic zet een publieke lijst aan (uit de catalogus of een eigen URL).
// De eerste keer ophalen gebeurt meteen bij de volgende ronde.
func (s *Service) AddPublic(ctx context.Context, actor events.Actor, in ListInput) (List, error) {
	src, err := s.checkSource(in.Source, in.URL)
	if err != nil {
		return List{}, err
	}
	scope, err := checkScope(in.Scope, in.ClusterID)
	if err != nil {
		return List{}, err
	}
	refresh := in.RefreshMinutes
	if refresh == 0 {
		refresh = src.DefaultMinutes
	}
	if refresh < src.MinMinutes {
		return List{}, &FieldError{fmt.Sprintf("%s mag hoogstens om de %d minuten opgehaald worden (voorwaarden van de bron)", src.Name, src.MinMinutes)}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = src.Name
	}
	slug := src.Key
	if src.Key == "custom" {
		slug = "url-" + hashValues([]string{src.URL})[:8]
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx, `INSERT INTO crowdsec_lists (slug, kind, name, source, url, scope, cluster_id, refresh_minutes, hash)
		VALUES ($1, 'public', $2, $3, $4, $5, $6, $7, '') ON CONFLICT (slug) DO NOTHING RETURNING id`,
		slug, name, src.Key, src.URL, scope, in.ClusterID, refresh).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return List{}, &FieldError{"die lijst staat er al"}
	}
	if err != nil {
		return List{}, err
	}
	l, err := s.list(ctx, nil, id)
	if err != nil {
		return List{}, err
	}
	s.listEvent(ctx, actor, l, "crowdsec.list_added", map[string]any{"url": src.URL, "refresh_minutes": refresh, "scope": scope})
	s.kickLists()
	return l, nil
}

func checkScope(scope string, cluster *uuid.UUID) (string, error) {
	switch scope {
	case "", "all":
		return "all", nil
	case "cluster":
		if cluster == nil {
			return "", &FieldError{"kies een cluster"}
		}
		return "cluster", nil
	}
	return "", &FieldError{"bereik is all of cluster"}
}

// UpdateList wijzigt aan/uit, naam, bereik of verversing.
func (s *Service) UpdateList(ctx context.Context, actor events.Actor, id uuid.UUID, in ListInput) (List, error) {
	l, err := s.list(ctx, nil, id)
	if err != nil {
		return List{}, err
	}
	scope, err := checkScope(in.Scope, in.ClusterID)
	if err != nil {
		return List{}, err
	}
	if in.Scope == "" {
		scope, in.ClusterID = l.Scope, l.ClusterID
	}
	enabled := l.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = l.Name
	}
	refresh := l.RefreshMinutes
	if in.RefreshMinutes != 0 {
		refresh = in.RefreshMinutes
		if p, ok := PublicByKey(l.Source); ok && refresh < p.MinMinutes {
			return List{}, &FieldError{fmt.Sprintf("%s mag hoogstens om de %d minuten opgehaald worden", p.Name, p.MinMinutes)}
		}
		if refresh < 60 {
			return List{}, &FieldError{"ophalen hoogstens om het uur"}
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_lists SET enabled = $2, name = $3, scope = $4, cluster_id = $5, refresh_minutes = $6,
		updated_at = now() WHERE id = $1`, id, enabled, name, scope, in.ClusterID, refresh); err != nil {
		return List{}, err
	}
	s.listEvent(ctx, actor, l, "crowdsec.list_updated", map[string]any{"enabled": enabled, "scope": scope, "refresh_minutes": refresh})
	s.kickLists()
	return s.list(ctx, nil, id)
}

// DeleteList haalt een publieke lijst weg; de nodes verliezen de bans bij
// de volgende ronde.
func (s *Service) DeleteList(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	l, err := s.list(ctx, nil, id)
	if err != nil {
		return err
	}
	if l.Kind != ListPublic {
		return &FieldError{"de eigen en de automatische lijst kun je uitzetten, niet weghalen"}
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM crowdsec_lists WHERE id = $1`, id); err != nil {
		return err
	}
	s.listEvent(ctx, actor, l, "crowdsec.list_deleted", map[string]any{"count": l.Count})
	s.kickLists()
	return nil
}

// kickLists laat de lijstronde meteen draaien.
func (s *Service) kickLists() {
	select {
	case s.listKick <- struct{}{}:
	default:
	}
}
