package soc

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/firewall"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Hoeveel er van elk in het antwoord komt.
const (
	attackersShown = 25
	// attackerCandidates per bron, voor het samenvoegen.
	attackerCandidates = 150
	targetsShown       = 8
	// CacheFor: zo lang wordt een overzicht hergebruikt.
	CacheFor = 15 * time.Second
)

// Service rekent het overzicht uit.
type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	Now  func() time.Time

	// Vulns geeft de kwetsbaarheden en verharding (fase 27); nil: niet
	// meetellen.
	Vulns func(ctx context.Context) (Vulns, error)

	mu    sync.Mutex
	cache map[Window]cached
}

type cached struct {
	at time.Time
	o  Overview
}

func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, log: log, Now: time.Now, cache: map[Window]cached{}}
}

// Overview geeft het overzicht voor het venster, hoogstens CacheFor oud.
func (s *Service) Overview(ctx context.Context, w Window) (Overview, error) {
	now := s.Now()
	s.mu.Lock()
	c, ok := s.cache[w]
	s.mu.Unlock()
	if ok && now.Sub(c.at) < CacheFor && now.Sub(c.at) >= 0 {
		return c.o, nil
	}
	o, err := s.build(ctx, w, now)
	if err != nil {
		return Overview{}, err
	}
	s.mu.Lock()
	s.cache[w] = cached{at: now, o: o}
	s.mu.Unlock()
	return o, nil
}

// Forget gooit de cache weg, bijvoorbeeld na een ban.
func (s *Service) Forget() {
	s.mu.Lock()
	clear(s.cache)
	s.mu.Unlock()
}

func (s *Service) build(ctx context.Context, w Window, now time.Time) (Overview, error) {
	since := now.Add(-w.Duration())
	prev := since.Add(-w.Duration())
	o := Overview{GeneratedAt: now, Window: w}

	nets, err := s.networks(ctx)
	if err != nil {
		return Overview{}, err
	}
	in := Inputs{SSHFromAny: nets.sshAny}

	if o.Coverage, err = s.coverage(ctx, now); err != nil {
		return Overview{}, fmt.Errorf("dekking: %w", err)
	}
	in.Coverage = o.Coverage
	for _, c := range o.Coverage {
		o.Stats.Nodes++
		if len(c.Gaps) == 0 {
			o.Stats.Protected++
		}
		if c.SecurityUpdates > 0 {
			o.Stats.SecurityNodes++
			o.Stats.SecurityUpdates += c.SecurityUpdates
		}
	}
	if in.Incidents, err = s.incidents(ctx); err != nil {
		return Overview{}, fmt.Errorf("incidenten: %w", err)
	}
	if err := s.stats(ctx, &o.Stats, since, prev, now); err != nil {
		return Overview{}, fmt.Errorf("cijfers: %w", err)
	}
	if o.Activity, err = s.activity(ctx, w, now); err != nil {
		return Overview{}, fmt.Errorf("activiteit: %w", err)
	}
	if o.Attackers, err = s.attackers(ctx, since, now, nets); err != nil {
		return Overview{}, fmt.Errorf("aanvallers: %w", err)
	}
	in.Attackers = o.Attackers
	if o.Targets, err = s.targets(ctx, since); err != nil {
		return Overview{}, fmt.Errorf("doelwitten: %w", err)
	}
	if err := s.threatInputs(ctx, &in, now, nets); err != nil {
		return Overview{}, fmt.Errorf("dreiging: %w", err)
	}
	// Mislukte logins van buiten in het venster.
	for _, a := range o.Attackers {
		if a.Kind == KindExternal {
			in.ExternalLoginFailures += a.Logins
		}
	}

	if s.Vulns != nil {
		v, err := s.Vulns(ctx)
		if err != nil {
			return Overview{}, fmt.Errorf("kwetsbaarheden: %w", err)
		}
		in.Vulns = &v
	}

	r := evaluate(in)
	o.Level, o.Score, o.Now = r.Level, r.Score, r.Now
	if err := s.trend(ctx, &o.Score, w, now); err != nil {
		return Overview{}, fmt.Errorf("trend: %w", err)
	}
	if len(o.Attackers) > attackersShown {
		o.Attackers = o.Attackers[:attackersShown]
	}
	return o, nil
}

// --- netwerken -------------------------------------------------------------

// nets zegt of een adres eigen is: in het LAN van de firewallinstellingen,
// of van een node of een VIP.
type nets struct {
	lan    []netip.Prefix
	own    map[netip.Addr]string
	sshAny bool
}

func (n nets) kind(ip string) (string, string) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		// Een range of iets anders dan een adres: wel van buiten.
		if p, err := netip.ParsePrefix(ip); err == nil {
			a = p.Addr()
		} else {
			return KindExternal, ""
		}
	}
	a = a.Unmap()
	if h, ok := n.own[a]; ok {
		return KindNode, h
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() {
		return KindInternal, ""
	}
	for _, p := range n.lan {
		if p.Contains(a) {
			return KindInternal, ""
		}
	}
	return KindExternal, ""
}

func (s *Service) networks(ctx context.Context) (nets, error) {
	out := nets{own: map[netip.Addr]string{}}
	var lan []string
	var ssh string
	if err := s.pool.QueryRow(ctx, `SELECT lan, ssh_from FROM firewall_settings`).Scan(&lan, &ssh); err != nil {
		lan, ssh = firewall.DefaultLAN, "lan"
	}
	if len(lan) == 0 {
		lan = firewall.DefaultLAN
	}
	for _, l := range lan {
		if p, err := netip.ParsePrefix(l); err == nil {
			out.lan = append(out.lan, p.Masked())
		} else if a, err := netip.ParseAddr(l); err == nil {
			out.lan = append(out.lan, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	// Alleen een beheerde node telt voor SSH voor iedereen.
	if ssh == "any" {
		var managed bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM firewall_nodes WHERE mode = 'managed')`).Scan(&managed); err != nil {
			return nets{}, err
		}
		out.sshAny = managed
	}
	rows, err := s.pool.Query(ctx, `
		SELECT n.hostname, coalesce(host(n.primary_ip), ''), coalesce(s.addresses, '{}')
		FROM nodes n LEFT JOIN node_status s ON s.node_id = n.id
		UNION ALL
		SELECT 'VIP ' || c.name, host(v.address), '{}' FROM vips v JOIN clusters c ON c.id = v.cluster_id`)
	if err != nil {
		return nets{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var host, primary string
		var addrs []string
		if err := rows.Scan(&host, &primary, &addrs); err != nil {
			return nets{}, err
		}
		for _, a := range append(addrs, primary) {
			if p, err := netip.ParsePrefix(a); err == nil {
				a = p.Addr().String()
			}
			if ip, err := netip.ParseAddr(a); err == nil && !ip.IsLoopback() {
				if _, taken := out.own[ip.Unmap()]; !taken {
					out.own[ip.Unmap()] = host
				}
			}
		}
	}
	return out, rows.Err()
}

// --- dekking ----------------------------------------------------------------

func (s *Service) coverage(ctx context.Context, now time.Time) ([]CoverageRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT n.id, n.hostname, n.cluster_id, coalesce(c.name, ''), a.protocol_version, a.last_seen_at, f.facts,
		       fn.node_id IS NOT NULL, coalesce(fn.mode, ''), fn.status, coalesce(fn.apply_error, ''),
		       cn.node_id IS NOT NULL, coalesce(cn.managed, false), coalesce(cn.bouncer, false), cn.status, coalesce(cn.apply_error, '')
		FROM nodes n
		JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
		LEFT JOIN clusters c ON c.id = n.cluster_id
		LEFT JOIN node_facts f ON f.node_id = n.id
		LEFT JOIN firewall_nodes fn ON fn.node_id = n.id
		LEFT JOIN crowdsec_nodes cn ON cn.node_id = n.id
		WHERE n.lifecycle = 'active'
		ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoverageRow{}
	for rows.Next() {
		var r CoverageRow
		var proto int32
		var hb *time.Time
		var facts, fwStatus, csStatus []byte
		var fw coverageFirewall
		var cs coverageCrowdSec
		if err := rows.Scan(&r.NodeID, &r.Hostname, &r.ClusterID, &r.ClusterName, &proto, &hb, &facts,
			&fw.row, &fw.mode, &fwStatus, &fw.applyError,
			&cs.row, &cs.managed, &cs.bouncer, &csStatus, &cs.applyError); err != nil {
			return nil, err
		}
		// Zoals de pagina Nodes: offline na HeartbeatDown zonder teken van leven.
		r.Online = hb != nil && now.Sub(*hb) <= status.HeartbeatDown
		var f protocol.Facts
		if len(facts) > 0 && json.Unmarshal(facts, &f) == nil {
			if f.Upgrades != nil {
				r.SecurityUpdates = f.Upgrades.Security
			}
			for _, sv := range f.Services {
				switch strings.TrimSuffix(sv.Name, ".service") {
				case "ufw":
					fw.serviceActive = sv.Active == "active"
				case "crowdsec":
					cs.serviceActive = sv.Active == "active"
				}
			}
		}
		if len(fwStatus) > 0 {
			fw.status = &protocol.FirewallStatus{}
			if json.Unmarshal(fwStatus, fw.status) != nil {
				fw.status = nil
			}
		}
		if len(csStatus) > 0 {
			cs.status = &protocol.CrowdSecStatus{}
			if json.Unmarshal(csStatus, cs.status) != nil {
				cs.status = nil
			}
		}
		fw.supported = int(proto) >= protocol.FirewallSince
		cs.supported = int(proto) >= protocol.CrowdSecSince
		r.Firewall = fw.state()
		r.CrowdSec, r.Bouncer = cs.state()
		r.Gaps = gaps(r)
		out = append(out, r)
	}
	return out, rows.Err()
}

type coverageFirewall struct {
	row           bool
	mode          string
	status        *protocol.FirewallStatus
	applyError    string
	supported     bool
	serviceActive bool
}

func (f coverageFirewall) state() string {
	switch {
	case !f.row && f.serviceActive:
		return StateUnmanaged
	case !f.row && !f.supported:
		return StateUnsupported
	case !f.row:
		return StateMissing
	case f.mode == "off":
		return StateOff
	case f.applyError != "":
		return StateError
	case f.mode == "preview":
		return StatePreview
	case f.status == nil:
		return StateUnknown
	case !f.status.Active:
		return StateDown
	}
	return StateOn
}

type coverageCrowdSec struct {
	row           bool
	managed       bool
	bouncer       bool
	status        *protocol.CrowdSecStatus
	applyError    string
	supported     bool
	serviceActive bool
}

// state geeft de stand van CrowdSec en van de bouncer; de bouncer is leeg
// als CrowdSec er niet (beheerd) staat.
func (c coverageCrowdSec) state() (string, string) {
	switch {
	case !c.row && c.serviceActive:
		return StateUnmanaged, ""
	case !c.row && !c.supported:
		return StateUnsupported, ""
	case !c.row:
		return StateMissing, ""
	case !c.managed:
		return StateOff, ""
	}
	bouncer := StateOn
	switch {
	case !c.bouncer:
		bouncer = StateOff
	case c.status == nil || !c.status.Installed:
		bouncer = StateUnknown
	case c.status.Bouncer != "active":
		bouncer = StateDown
	}
	switch {
	case c.applyError != "":
		return StateError, bouncer
	case c.status == nil || !c.status.Installed:
		return StateUnknown, bouncer
	case c.status.Active != "active":
		return StateDown, bouncer
	}
	return StateOn, bouncer
}

// gaps zijn de onbewuste gaten; bewust uit is geen gat.
func gaps(r CoverageRow) []string {
	g := []string{}
	if !r.Online {
		g = append(g, "agent offline")
	}
	switch r.Firewall {
	case StateMissing:
		g = append(g, "geen firewallbeheer")
	case StatePreview:
		g = append(g, "firewall alleen voorbeeld")
	case StateDown:
		g = append(g, "ufw staat uit")
	case StateError:
		g = append(g, "firewall niet toegepast")
	case StateUnsupported:
		g = append(g, "agent te oud voor de firewall")
	case StateUnmanaged:
		g = append(g, "ufw niet beheerd")
	}
	switch r.CrowdSec {
	case StateMissing:
		g = append(g, "geen CrowdSec")
	case StateDown:
		g = append(g, "CrowdSec draait niet")
	case StateError:
		g = append(g, "CrowdSec niet toegepast")
	case StateUnsupported:
		g = append(g, "agent te oud voor CrowdSec")
	case StateUnmanaged:
		g = append(g, "CrowdSec niet beheerd")
	}
	switch r.Bouncer {
	case StateDown:
		g = append(g, "bouncer draait niet")
	case StateOff:
		g = append(g, "geen bouncer")
	}
	if r.SecurityUpdates > 0 {
		g = append(g, plural(r.SecurityUpdates, "beveiligingsupdate", "beveiligingsupdates"))
	}
	return g
}

// --- incidenten en dreiging --------------------------------------------------

func (s *Service) incidents(ctx context.Context) ([]Incident, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, number, title, severity FROM incidents
		WHERE status = 'open' AND NOT caused_by_test ORDER BY started_at DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var i Incident
		var n int32
		if err := rows.Scan(&i.ID, &n, &i.Title, &i.Severity); err != nil {
			return nil, err
		}
		i.Number = int(n)
		out = append(out, i)
	}
	return out, rows.Err()
}

// loginActions zijn de events die als mislukte login tellen.
var loginActions = []string{"auth.login_failed", "auth.rate_limited"}

func (s *Service) threatInputs(ctx context.Context, in *Inputs, now time.Time, n nets) error {
	day := now.Add(-24 * time.Hour)
	in.AttackedNodes = map[uuid.UUID]int{}
	rows, err := s.pool.Query(ctx, `SELECT node_id, count(*) FROM crowdsec_alerts WHERE started_at > $1 AND NOT simulated GROUP BY node_id`, day)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var c int
		if err := rows.Scan(&id, &c); err != nil {
			rows.Close()
			return err
		}
		in.AttackedNodes[id] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var week int
	if err := s.pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE started_at > $1),
			count(*) FILTER (WHERE started_at <= $1 AND started_at > $2)
		FROM crowdsec_alerts WHERE started_at > $2 AND NOT simulated`, day, day.Add(-7*24*time.Hour)).Scan(&in.Attacks24h, &week); err != nil {
		return err
	}
	in.AttacksBaseline = float64(week) / 7

	// Brute force: het externe adres met de meeste mislukte logins in het
	// laatste uur.
	rows, err = s.pool.Query(ctx, `SELECT payload->>'ip', count(*) FROM events
		WHERE ts > $1 AND action = ANY($2) AND coalesce(payload->>'ip', '') <> ''
		GROUP BY 1 ORDER BY 2 DESC LIMIT 20`, now.Add(-time.Hour), loginActions)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ip string
		var c int
		if err := rows.Scan(&ip, &c); err != nil {
			rows.Close()
			return err
		}
		if k, _ := n.kind(ip); k == KindExternal && (in.BruteForce == nil || c > in.BruteForce.Count) {
			in.BruteForce = &Burst{IP: ip, Count: c}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = s.pool.Query(ctx, `SELECT username FROM users WHERE role = 'admin' AND disabled_at IS NULL AND totp_enabled_at IS NULL ORDER BY lower(username)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return err
		}
		in.AdminsWithoutTOTP = append(in.AdminsWithoutTOTP, u)
	}
	return rows.Err()
}

// --- cijfers en activiteit -------------------------------------------------------

func (s *Service) stats(ctx context.Context, st *Stats, since, prev, now time.Time) error {
	if err := s.pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE started_at > $1),
			count(*) FILTER (WHERE started_at <= $1)
		FROM crowdsec_alerts WHERE started_at > $2 AND NOT simulated`, since, prev).Scan(&st.Attacks, &st.AttacksPrev); err != nil {
		return err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT value) FROM crowdsec_decisions WHERE until > $1`, now).Scan(&st.ActiveBans); err != nil {
		return err
	}
	if err := s.pool.QueryRow(ctx, `SELECT
			coalesce(sum(count) FILTER (WHERE hour >= $1), 0)::bigint,
			coalesce(sum(count) FILTER (WHERE hour < $1), 0)::bigint
		FROM firewall_blocked WHERE hour >= $2`, since.Truncate(time.Hour), prev.Truncate(time.Hour)).Scan(&st.Blocked, &st.BlockedPrev); err != nil {
		return err
	}
	return s.pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE ts > $1),
			count(*) FILTER (WHERE ts <= $1)
		FROM events WHERE ts > $2 AND action = ANY($3)`, since, prev, loginActions).Scan(&st.LoginFailures, &st.LoginFailuresPrev)
}

func (s *Service) activity(ctx context.Context, w Window, now time.Time) ([]Bucket, error) {
	size := w.bucket()
	n := int(w.Duration() / size)
	// De laatste staaf eindigt na nu; de eerste begint n-1 staven eerder.
	start := now.Truncate(size).Add(-time.Duration(n-1) * size)
	buckets := make([]Bucket, n)
	for i := range buckets {
		buckets[i].At = start.Add(time.Duration(i) * size)
	}
	idx := func(t time.Time) int {
		if t.Before(start) {
			return -1
		}
		i := int(t.Sub(start) / size)
		if i >= n {
			return -1
		}
		return i
	}
	type src struct {
		sql string
		add func(b *Bucket, c int64)
	}
	for _, q := range []src{
		{`SELECT date_trunc('hour', started_at), count(*) FROM crowdsec_alerts WHERE started_at >= $1 AND NOT simulated GROUP BY 1`,
			func(b *Bucket, c int64) { b.CrowdSec += int(c) }},
		{`SELECT hour, sum(count)::bigint FROM firewall_blocked WHERE hour >= $1 GROUP BY 1`,
			func(b *Bucket, c int64) { b.Firewall += c }},
		{`SELECT date_trunc('hour', ts), count(*) FROM events WHERE ts >= $1 AND action IN ('auth.login_failed', 'auth.rate_limited') GROUP BY 1`,
			func(b *Bucket, c int64) { b.Logins += int(c) }},
	} {
		rows, err := s.pool.Query(ctx, q.sql, start)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t time.Time
			var c int64
			if err := rows.Scan(&t, &c); err != nil {
				rows.Close()
				return nil, err
			}
			if i := idx(t); i >= 0 {
				q.add(&buckets[i], c)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return buckets, nil
}

// --- aanvallers en doelwitten --------------------------------------------------

func (s *Service) attackers(ctx context.Context, since, now time.Time, n nets) ([]Attacker, error) {
	byIP := map[string]*Attacker{}
	get := func(ip string) *Attacker {
		a := byIP[ip]
		if a == nil {
			a = &Attacker{IP: ip, Sources: []string{}, Nodes: []string{}, Scenarios: []string{}, Ports: []string{}}
			a.Kind, a.Node = n.kind(ip)
			byIP[ip] = a
		}
		return a
	}
	seen := func(a *Attacker, first, last time.Time) {
		if a.FirstSeen.IsZero() || first.Before(a.FirstSeen) {
			a.FirstSeen = first
		}
		if last.After(a.LastSeen) {
			a.LastSeen = last
		}
	}
	addSource := func(a *Attacker, src string) {
		if !slices.Contains(a.Sources, src) {
			a.Sources = append(a.Sources, src)
		}
	}
	addNode := func(a *Attacker, host string) {
		if host != "" && !slices.Contains(a.Nodes, host) {
			a.Nodes = append(a.Nodes, host)
		}
	}

	// CrowdSec: per adres, scenario en node.
	rows, err := s.pool.Query(ctx, `
		WITH top AS (
			SELECT value FROM crowdsec_alerts WHERE started_at > $1 AND NOT simulated AND lower(scope) IN ('ip', 'range')
			GROUP BY value ORDER BY count(*) DESC LIMIT $2)
		SELECT a.value, a.scenario, n.hostname, max(a.country), max(a.as_name), count(*), min(a.started_at), max(a.stopped_at)
		FROM crowdsec_alerts a JOIN top t ON t.value = a.value JOIN nodes n ON n.id = a.node_id
		WHERE a.started_at > $1 AND NOT a.simulated
		GROUP BY a.value, a.scenario, n.hostname`, since, attackerCandidates)
	if err != nil {
		return nil, err
	}
	scen := map[string]map[string]int{}
	for rows.Next() {
		var ip, scenario, host, country, as string
		var c int
		var first, last time.Time
		if err := rows.Scan(&ip, &scenario, &host, &country, &as, &c, &first, &last); err != nil {
			rows.Close()
			return nil, err
		}
		a := get(ip)
		addSource(a, SourceCrowdSec)
		addNode(a, host)
		a.Alerts += c
		a.Country = cmp.Or(a.Country, country)
		a.AS = cmp.Or(a.AS, as)
		seen(a, first, last)
		if scen[ip] == nil {
			scen[ip] = map[string]int{}
		}
		scen[ip][scenario] += c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Firewall: per adres, poort en node.
	rows, err = s.pool.Query(ctx, `
		WITH top AS (
			SELECT src FROM firewall_blocked WHERE hour >= $1 GROUP BY src ORDER BY sum(count) DESC LIMIT $2)
		SELECT b.src, b.proto, b.port, n.hostname, sum(b.count)::bigint, min(b.first_seen), max(b.last_seen)
		FROM firewall_blocked b JOIN top t ON t.src = b.src JOIN nodes n ON n.id = b.node_id
		WHERE b.hour >= $1
		GROUP BY b.src, b.proto, b.port, n.hostname`, since.Truncate(time.Hour), attackerCandidates)
	if err != nil {
		return nil, err
	}
	ports := map[string]map[string]int64{}
	for rows.Next() {
		var ip, proto, host string
		var port int32
		var c int64
		var first, last time.Time
		if err := rows.Scan(&ip, &proto, &port, &host, &c, &first, &last); err != nil {
			rows.Close()
			return nil, err
		}
		a := get(ip)
		addSource(a, SourceFirewall)
		addNode(a, host)
		a.Blocked += c
		seen(a, first, last)
		if ports[ip] == nil {
			ports[ip] = map[string]int64{}
		}
		ports[ip][fmt.Sprintf("%d/%s", port, proto)] += c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Mislukte logins op ClusterForge.
	rows, err = s.pool.Query(ctx, `SELECT payload->>'ip', count(*), min(ts), max(ts) FROM events
		WHERE ts > $1 AND action = ANY($2) AND coalesce(payload->>'ip', '') <> ''
		GROUP BY 1 ORDER BY 2 DESC LIMIT $3`, since, loginActions, attackerCandidates)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ip string
		var c int
		var first, last time.Time
		if err := rows.Scan(&ip, &c, &first, &last); err != nil {
			rows.Close()
			return nil, err
		}
		a := get(ip)
		addSource(a, SourceLogin)
		addNode(a, "ClusterForge")
		a.Logins += c
		seen(a, first, last)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(byIP) == 0 {
		return []Attacker{}, nil
	}
	ips := make([]string, 0, len(byIP))
	for ip := range byIP {
		ips = append(ips, ip)
	}
	// Land en netwerk voor adressen die alleen de firewall zag, en of ze
	// nu geband zijn.
	rows, err = s.pool.Query(ctx, `
		SELECT value, max(country), max(as_name), max(until) FILTER (WHERE until > $2)
		FROM (SELECT value, country, as_name, NULL::timestamptz AS until FROM crowdsec_alerts WHERE value = ANY($1)
		      UNION ALL SELECT value, country, as_name, until FROM crowdsec_decisions WHERE value = ANY($1)) x
		GROUP BY value`, ips, now)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ip, country, as string
		var until *time.Time
		if err := rows.Scan(&ip, &country, &as, &until); err != nil {
			rows.Close()
			return nil, err
		}
		a := byIP[ip]
		a.Country = cmp.Or(a.Country, country)
		a.AS = cmp.Or(a.AS, as)
		if until != nil {
			a.Banned, a.BanUntil = true, until
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Attacker, 0, len(byIP))
	for ip, a := range byIP {
		a.Scenarios = topKeys(scen[ip], 5)
		a.Ports = topKeys(ports[ip], 5)
		slices.Sort(a.Nodes)
		out = append(out, *a)
	}
	slices.SortFunc(out, func(a, b Attacker) int {
		return cmp.Or(cmp.Compare(weight(b), weight(a)), b.LastSeen.Compare(a.LastSeen), cmp.Compare(a.IP, b.IP))
	})
	return out, nil
}

// weight rangschikt aanvallers: van buiten eerst, dan een alert of een
// login veel zwaarder dan pakketten op een dichte poort (die tellen
// logaritmisch: een monitoringscraper die elke minuut klopt, is geen
// aanvaller).
func weight(a Attacker) float64 {
	w := float64(a.Alerts)*20 + float64(a.Logins)*10 + 10*math.Log2(1+float64(a.Blocked)) + float64(len(a.Nodes))*5
	if a.Kind == KindExternal {
		w *= 4
	}
	return w
}

func topKeys[N int | int64](m map[string]N, k int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(cmp.Compare(m[b], m[a]), cmp.Compare(a, b)) })
	return firstN(keys, k)
}

func (s *Service) targets(ctx context.Context, since time.Time) (Targets, error) {
	t := Targets{Nodes: []Count{}, Ports: []Count{}, Scenarios: []Count{}}
	type q struct {
		sql string
		dst *[]Count
	}
	for _, x := range []q{
		{`SELECT n.id::text, n.hostname, sum(x.c)::bigint FROM (
			SELECT node_id, count(*) * 20 AS c FROM crowdsec_alerts WHERE started_at > $1 AND NOT simulated GROUP BY node_id
			UNION ALL SELECT node_id, sum(count) FROM firewall_blocked WHERE hour >= date_trunc('hour', $1::timestamptz) GROUP BY node_id) x
			JOIN nodes n ON n.id = x.node_id GROUP BY n.id, n.hostname ORDER BY 3 DESC, 2 LIMIT $2`, &t.Nodes},
		{`SELECT port || '/' || proto, port || '/' || proto, sum(count)::bigint FROM firewall_blocked
			WHERE hour >= date_trunc('hour', $1::timestamptz) GROUP BY port, proto ORDER BY 3 DESC, port LIMIT $2`, &t.Ports},
		{`SELECT scenario, scenario, count(*) FROM crowdsec_alerts WHERE started_at > $1 AND NOT simulated
			GROUP BY scenario ORDER BY 3 DESC, 1 LIMIT $2`, &t.Scenarios},
	} {
		rows, err := s.pool.Query(ctx, x.sql, since, targetsShown)
		if err != nil {
			return Targets{}, err
		}
		for rows.Next() {
			var c Count
			if err := rows.Scan(&c.Key, &c.Label, &c.Count); err != nil {
				rows.Close()
				return Targets{}, err
			}
			*x.dst = append(*x.dst, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Targets{}, err
		}
	}
	// De node-doelwitten tellen een alert als 20 pakketten voor de volgorde;
	// het getal dat je ziet is het echte aantal gebeurtenissen.
	for i, c := range t.Nodes {
		var alerts int64
		var blocked int64
		if err := s.pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM crowdsec_alerts WHERE node_id = $1 AND started_at > $2 AND NOT simulated),
				(SELECT coalesce(sum(count), 0)::bigint FROM firewall_blocked WHERE node_id = $1 AND hour >= date_trunc('hour', $2::timestamptz))`,
			c.Key, since).Scan(&alerts, &blocked); err != nil {
			return Targets{}, err
		}
		t.Nodes[i].Count = alerts + blocked
		t.Nodes[i].Detail = plural(int(alerts), "alert", "alerts") + ", " + plural(int(blocked), "pakket", "pakketten")
	}
	return t, nil
}
