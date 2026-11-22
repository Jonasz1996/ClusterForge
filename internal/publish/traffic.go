package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Fase 39: verkeer van NPM en logins van Authentik. Elke minuut vraagt de
// server de agent op de NPM-node wat er in de toegangslogs bijkwam (alleen
// samengevat) en haalt hij de nieuwe events van Authentik op. De GUI en de
// homepage-API tonen verkeer per host, de drukste en verdachte IP's naast
// de bans van CrowdSec, en (mislukte) logins.

const (
	// FeedEvery is hoe vaak de server verkeer en logins ophaalt.
	FeedEvery = time.Minute
	// trafficBucket is de korrel van publish_traffic.
	trafficBucket = 5 * time.Minute
	// Hoe lang de gegevens blijven.
	keepTraffic     = 14 * 24 * time.Hour
	keepConnections = 2 * 24 * time.Hour
	keepAuthEvents  = 30 * 24 * time.Hour
	// firstAccept: bij de eerste lezing van een node alleen recente regels,
	// zodat een log die met rsync meekwam niet dubbel telt.
	firstAccept = 15 * time.Minute
	// akFirstWindow: de eerste keer haalt de server een dag events op.
	akFirstWindow = 24 * time.Hour
	akPageSize    = 100
	akMaxPages    = 5
)

// Acties van Authentik die ClusterForge bewaart.
var akActions = []string{"login", "login_failed", "logout", "authorize_application"}

// FeedLoop haalt elke minuut verkeer en logins op en ruimt oude gegevens op.
func (s *Service) FeedLoop(ctx context.Context) {
	t := time.NewTicker(FeedEvery)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		s.PullFeeds(ctx)
		if time.Since(lastPrune) > time.Hour {
			s.pruneFeeds(ctx)
			lastPrune = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// PullFeeds haalt één keer verkeer en logins op.
func (s *Service) PullFeeds(ctx context.Context) {
	st, err := s.Settings(ctx)
	if err != nil {
		return
	}
	if st.NPMLinked() {
		s.pullTraffic(ctx, st)
	}
	if st.AuthentikLinked() {
		s.pullAuthentik(ctx, st)
	}
}

func (s *Service) feedState(ctx context.Context, source string) (cursor string, okAt *time.Time) {
	_ = s.pool.QueryRow(ctx, `SELECT cursor, ok_at FROM publish_feed_state WHERE source = $1`, source).Scan(&cursor, &okAt)
	return
}

func (s *Service) saveFeed(ctx context.Context, source, cursor string, lines int, ferr error) {
	msg := ""
	if ferr != nil {
		msg = ferr.Error()
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO publish_feed_state (source, at, ok_at, cursor, error, lines)
		VALUES ($1, now(), CASE WHEN $3 = '' THEN now() END, $2, $3, $4)
		ON CONFLICT (source) DO UPDATE SET at = now(),
			ok_at = CASE WHEN $3 = '' THEN now() ELSE publish_feed_state.ok_at END,
			cursor = CASE WHEN $3 = '' THEN $2 ELSE publish_feed_state.cursor END,
			error = $3, lines = publish_feed_state.lines + $4`, source, cursor, msg, lines); err != nil {
		s.log.Warn("publicatie: stand van de feed bewaren", "source", source, "err", err)
	}
}

// pullTraffic vraagt de NPM-node om de nieuwe logregels.
func (s *Service) pullTraffic(ctx context.Context, st Settings) {
	node, err := s.npmNode(ctx, st)
	if err != nil {
		s.saveFeed(ctx, "npm", "", 0, err)
		return
	}
	source := "npm:" + node.ID.String()
	if node.Protocol < protocol.TrafficSince {
		s.saveFeed(ctx, "npm", "", 0, fmt.Errorf("de agent op %s is te oud voor verkeer (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", node.Hostname, node.Protocol, protocol.TrafficSince))
		return
	}
	_, okAt := s.feedState(ctx, source)
	total := 0
	for range 4 {
		res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMTraffic})
		if err != nil {
			s.saveFeed(ctx, "npm", "", 0, err)
			s.saveFeed(ctx, source, "", 0, err)
			return
		}
		tr := res.Traffic
		if tr == nil {
			break
		}
		var since time.Time
		if okAt == nil {
			since = s.now().Add(-firstAccept)
		}
		if err := s.storeTraffic(ctx, node.ID, tr, since); err != nil {
			s.saveFeed(ctx, source, "", 0, err)
			return
		}
		total += tr.Lines
		if !tr.More {
			break
		}
	}
	s.saveFeed(ctx, source, "", total, nil)
	s.saveFeed(ctx, "npm", node.Hostname, total, nil)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// storeTraffic telt een antwoord van de agent op bij de tabellen. Regels
// voor since tellen niet (eerste lezing).
func (s *Service) storeTraffic(ctx context.Context, nodeID uuid.UUID, tr *protocol.TrafficResult, since time.Time) error {
	b := &pgx.Batch{}
	for _, m := range tr.Minutes {
		if m.Minute.Before(since) {
			continue
		}
		b.Queue(`INSERT INTO publish_traffic (node_id, host_id, bucket, requests, s2xx, s3xx, s4xx, s5xx, bytes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (node_id, host_id, bucket) DO UPDATE SET requests = publish_traffic.requests + EXCLUDED.requests,
				s2xx = publish_traffic.s2xx + EXCLUDED.s2xx, s3xx = publish_traffic.s3xx + EXCLUDED.s3xx,
				s4xx = publish_traffic.s4xx + EXCLUDED.s4xx, s5xx = publish_traffic.s5xx + EXCLUDED.s5xx,
				bytes = publish_traffic.bytes + EXCLUDED.bytes`,
			nodeID, m.HostID, m.Minute.UTC().Truncate(trafficBucket), m.Requests, m.S2xx, m.S3xx, m.S4xx, m.S5xx, m.Bytes)
	}
	for _, p := range tr.IPs {
		if p.Last.Before(since) {
			continue
		}
		ip, err := netip.ParseAddr(p.IP)
		if err != nil {
			continue
		}
		b.Queue(`INSERT INTO publish_traffic_ips (host_id, hour, ip, requests, s4xx, s5xx, denied, bytes, first_at, last_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (host_id, hour, ip) DO UPDATE SET requests = publish_traffic_ips.requests + EXCLUDED.requests,
				s4xx = publish_traffic_ips.s4xx + EXCLUDED.s4xx, s5xx = publish_traffic_ips.s5xx + EXCLUDED.s5xx,
				denied = publish_traffic_ips.denied + EXCLUDED.denied, bytes = publish_traffic_ips.bytes + EXCLUDED.bytes,
				first_at = least(publish_traffic_ips.first_at, EXCLUDED.first_at),
				last_at = greatest(publish_traffic_ips.last_at, EXCLUDED.last_at)`,
			p.HostID, p.Last.UTC().Truncate(time.Hour), ip.Unmap().String(), p.Requests, p.S4xx, p.S5xx, p.Denied, p.Bytes, p.First, p.Last)
	}
	if tr.Connections >= 0 {
		b.Queue(`INSERT INTO publish_connections (node_id, at, connections) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			nodeID, s.now().UTC().Truncate(time.Second), tr.Connections)
	}
	if b.Len() == 0 {
		return nil
	}
	return s.pool.SendBatch(ctx, b).Close()
}

// akEvent is een event zoals de API van Authentik het geeft.
type akEvent struct {
	PK   string `json:"pk"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	Action   string         `json:"action"`
	ClientIP string         `json:"client_ip"`
	Created  time.Time      `json:"created"`
	Context  map[string]any `json:"context"`
}

// pullAuthentik haalt per actie de events op die nieuwer zijn dan de vorige
// keer.
func (s *Service) pullAuthentik(ctx context.Context, st Settings) {
	node, err := s.akNode(ctx, st)
	if err != nil {
		s.saveFeed(ctx, "authentik", "", 0, err)
		return
	}
	total := 0
	for _, action := range akActions {
		source := "authentik:" + action
		cursor, _ := s.feedState(ctx, source)
		since := s.now().Add(-akFirstWindow)
		if t, err := time.Parse(time.RFC3339Nano, cursor); err == nil {
			since = t
		}
		newest := since
		n := 0
		for page := 1; page <= akMaxPages; page++ {
			q := url.Values{"action": {action}, "ordering": {"-created"}, "page_size": {fmt.Sprint(akPageSize)}, "page": {fmt.Sprint(page)}}
			status, data, err := s.akCall(ctx, st, node, http.MethodGet, "/events/events/?"+q.Encode(), nil)
			if err == nil && status != http.StatusOK {
				err = fmt.Errorf("events: %s", akMessage(status, data))
			}
			if err != nil {
				s.saveFeed(ctx, "authentik", "", 0, err)
				return
			}
			var res struct {
				Pagination struct {
					Next float64 `json:"next"`
				} `json:"pagination"`
				Results []akEvent `json:"results"`
			}
			if err := json.Unmarshal(data, &res); err != nil {
				s.saveFeed(ctx, "authentik", "", 0, fmt.Errorf("events: %w", err))
				return
			}
			older := false
			for _, e := range res.Results {
				if !e.Created.After(since) {
					older = true
					break
				}
				if err := s.storeAuthEvent(ctx, e); err != nil {
					s.saveFeed(ctx, "authentik", "", 0, err)
					return
				}
				n++
				if e.Created.After(newest) {
					newest = e.Created
				}
			}
			if older || res.Pagination.Next == 0 || len(res.Results) < akPageSize {
				break
			}
		}
		s.saveFeed(ctx, source, newest.UTC().Format(time.RFC3339Nano), n, nil)
		total += n
	}
	s.saveFeed(ctx, "authentik", node.Hostname, total, nil)
}

func ctxString(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	switch v := cur.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (s *Service) storeAuthEvent(ctx context.Context, e akEvent) error {
	user := e.User.Username
	if e.Action == "login_failed" || user == "AnonymousUser" {
		if u := ctxString(e.Context, "username"); u != "" {
			user = u
		}
	}
	app := ctxString(e.Context, "authorized_application", "name")
	reason := ctxString(e.Context, "message")
	if reason == "" {
		reason = ctxString(e.Context, "stage", "name")
	}
	ip := e.ClientIP
	if a, err := netip.ParseAddr(ip); err == nil {
		ip = a.Unmap().String()
	} else {
		ip = ""
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO publish_auth_events (pk, created, action, username, client_ip, app, country, city, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (pk) DO NOTHING`,
		e.PK, e.Created, e.Action, trimTo(user, 200), ip, trimTo(app, 200),
		trimTo(ctxString(e.Context, "geo", "country"), 10), trimTo(ctxString(e.Context, "geo", "city"), 100), trimTo(reason, 300))
	return err
}

func trimTo(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Service) pruneFeeds(ctx context.Context) {
	now := s.now()
	for _, q := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM publish_traffic WHERE bucket < $1`, now.Add(-keepTraffic)},
		{`DELETE FROM publish_traffic_ips WHERE hour < $1`, now.Add(-keepTraffic)},
		{`DELETE FROM publish_connections WHERE at < $1`, now.Add(-keepConnections)},
		{`DELETE FROM publish_auth_events WHERE created < $1`, now.Add(-keepAuthEvents)},
	} {
		if _, err := s.pool.Exec(ctx, q.sql, q.at); err != nil {
			s.log.Warn("publicatie: opruimen", "err", err)
		}
	}
}

// --- weergave ---

// Windows die de GUI en de API kennen.
var trafficWindows = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

// ParseWindow geeft de duur van een venster; leeg is 24h.
func ParseWindow(w string) (string, time.Duration, error) {
	if w == "" {
		w = "24h"
	}
	d, ok := trafficWindows[w]
	if !ok {
		return "", 0, invalid("venster %q: kies 1h, 24h of 7d", w)
	}
	return w, d, nil
}

// Ban is een actieve ban van CrowdSec voor een IP.
type Ban struct {
	Scenario string    `json:"scenario"`
	Origin   string    `json:"origin"`
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	Country  string    `json:"country,omitempty"`
	AS       string    `json:"as,omitempty"`
	Nodes    []string  `json:"nodes"`
}

type banRow struct {
	prefix netip.Prefix
	ban    Ban
}

// bans leest de actieve bans van CrowdSec (IP en Range).
func (s *Service) bans(ctx context.Context) ([]banRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.scope, d.value, max(d.scenario), string_agg(DISTINCT d.origin, ', '), min(d.first_seen), max(d.until),
			max(d.country), max(d.as_name), array_agg(DISTINCT n.hostname ORDER BY n.hostname)
		FROM crowdsec_decisions d JOIN nodes n ON n.id = d.node_id
		WHERE d.until > now() AND d.type = 'ban'
		GROUP BY d.scope, d.value LIMIT 20000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []banRow
	for rows.Next() {
		var scope, value string
		var b Ban
		if err := rows.Scan(&scope, &value, &b.Scenario, &b.Origin, &b.Since, &b.Until, &b.Country, &b.AS, &b.Nodes); err != nil {
			return nil, err
		}
		var p netip.Prefix
		if strings.Contains(value, "/") {
			p, err = netip.ParsePrefix(value)
		} else {
			var a netip.Addr
			a, err = netip.ParseAddr(value)
			if err == nil {
				a = a.Unmap()
				p = netip.PrefixFrom(a, a.BitLen())
			}
		}
		if err != nil {
			continue
		}
		out = append(out, banRow{prefix: p.Masked(), ban: b})
	}
	return out, rows.Err()
}

func banFor(bans []banRow, ip string) *Ban {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	a = a.Unmap()
	for i := range bans {
		if bans[i].prefix.Contains(a) {
			return &bans[i].ban
		}
	}
	return nil
}

// TrafficPoint is één punt van de grafiek.
type TrafficPoint struct {
	T        time.Time `json:"t"`
	Requests int       `json:"requests"`
	S4xx     int       `json:"s4xx"`
	S5xx     int       `json:"s5xx"`
}

// HostTraffic is het verkeer van één proxy host.
type HostTraffic struct {
	HostID   int        `json:"host_id"`
	Domain   string     `json:"domain"`
	Requests int        `json:"requests"`
	Visitors int        `json:"visitors"`
	S4xx     int        `json:"s4xx"`
	S5xx     int        `json:"s5xx"`
	Denied   int        `json:"denied"`
	Bytes    int64      `json:"bytes"`
	Last     *time.Time `json:"last,omitempty"`
}

// IPTraffic is een client-IP met wat het deed en wat CrowdSec ervan vindt.
type IPTraffic struct {
	IP       string    `json:"ip"`
	Requests int       `json:"requests"`
	S4xx     int       `json:"s4xx"`
	S5xx     int       `json:"s5xx"`
	Denied   int       `json:"denied"`
	Bytes    int64     `json:"bytes"`
	Hosts    []string  `json:"hosts"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	Ban      *Ban      `json:"ban,omitempty"`
	// AfterBan: aanvragen in de uren na het begin van de ban (bij benadering,
	// per uur). Meer dan nul betekent dat NPM het IP nog doorlaat.
	AfterBan int `json:"after_ban"`
	// FailedLogins: mislukte logins in Authentik van dit IP in het venster.
	FailedLogins int `json:"failed_logins"`
	// Suspicious: veel geweigerd of veel fouten, maar geen ban.
	Suspicious bool   `json:"suspicious"`
	Why        string `json:"why,omitempty"`
}

// FeedState zegt hoe het lezen van een bron laatst ging.
type FeedState struct {
	Source string     `json:"source"`
	Detail string     `json:"detail,omitempty"`
	At     time.Time  `json:"at"`
	OKAt   *time.Time `json:"ok_at,omitempty"`
	Error  string     `json:"error,omitempty"`
	Lines  int64      `json:"lines"`
}

// TrafficView is de pagina Verkeer en de bron traffic van de homepage-API.
type TrafficView struct {
	Window string         `json:"window"`
	From   time.Time      `json:"from"`
	Totals TrafficTotals  `json:"totals"`
	Series []TrafficPoint `json:"series"`
	Hosts  []HostTraffic  `json:"hosts"`
	IPs    []IPTraffic    `json:"ips"`
	// Connections zijn de open verbindingen van NPM over de tijd.
	Connections []ConnPoint `json:"connections"`
	Compare     Compare     `json:"compare"`
	Feeds       []FeedState `json:"feeds"`
}

// TrafficTotals zijn de getallen bovenaan.
type TrafficTotals struct {
	Requests    int        `json:"requests"`
	Visitors    int        `json:"visitors"`
	S4xx        int        `json:"s4xx"`
	S5xx        int        `json:"s5xx"`
	Denied      int        `json:"denied"`
	Bytes       int64      `json:"bytes"`
	Connections int        `json:"connections"`
	ConnAt      *time.Time `json:"connections_at,omitempty"`
}

// ConnPoint is één meting van de open verbindingen.
type ConnPoint struct {
	T     time.Time `json:"t"`
	Count int       `json:"count"`
}

// Compare zet NPM naast CrowdSec.
type Compare struct {
	// BannedSeen: IP's met een ban die in het venster bij NPM kwamen.
	BannedSeen int `json:"banned_seen"`
	// BannedPassing: daarvan die na het begin van de ban nog binnenkwamen.
	BannedPassing int `json:"banned_passing"`
	// Suspicious: verdachte IP's zonder ban.
	Suspicious int `json:"suspicious"`
	// ActiveBans: alle actieve bans van CrowdSec.
	ActiveBans int `json:"active_bans"`
}

// Grenzen voor verdacht.
const (
	suspectDenied  = 20
	suspectErrors  = 100
	suspectFailed  = 5
	maxTrafficIPs  = 200
	maxSeriesSteps = 400
)

func (s *Service) hostDomains(ctx context.Context) map[int]string {
	out := map[int]string{}
	if ov, err := s.Overview(ctx); err == nil {
		for _, h := range ov.Hosts {
			if len(h.Domains) > 0 {
				out[h.ID] = h.Domains[0]
			}
		}
	}
	return out
}

func domainOr(m map[int]string, id int) string {
	if d, ok := m[id]; ok {
		return d
	}
	return fmt.Sprintf("host %d", id)
}

// Traffic bouwt de weergave voor een venster.
func (s *Service) Traffic(ctx context.Context, window string) (*TrafficView, error) {
	w, d, err := ParseWindow(window)
	if err != nil {
		return nil, err
	}
	now := s.now()
	from := now.Add(-d)
	v := &TrafficView{Window: w, From: from, Series: []TrafficPoint{}, Hosts: []HostTraffic{}, IPs: []IPTraffic{}, Connections: []ConnPoint{}}
	domains := s.hostDomains(ctx)

	// Grafiek: stappen van 5 minuten (1h), een uur (24h) of zes uur (7d).
	step := map[string]time.Duration{"1h": 5 * time.Minute, "24h": time.Hour, "7d": 6 * time.Hour}[w]
	rows, err := s.pool.Query(ctx, `SELECT to_timestamp(floor(extract(epoch FROM bucket) / $2) * $2) AS t,
			sum(requests), sum(s4xx), sum(s5xx)
		FROM publish_traffic WHERE bucket >= $1 GROUP BY 1 ORDER BY 1 LIMIT $3`, from.Truncate(trafficBucket), step.Seconds(), maxSeriesSteps)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.T, &p.Requests, &p.S4xx, &p.S5xx); err != nil {
			rows.Close()
			return nil, err
		}
		v.Series = append(v.Series, p)
	}
	rows.Close()

	// Per host.
	byHost := map[int]*HostTraffic{}
	rows, err = s.pool.Query(ctx, `SELECT host_id, sum(requests), sum(s4xx), sum(s5xx), sum(bytes), max(bucket)
		FROM publish_traffic WHERE bucket >= $1 GROUP BY host_id`, from.Truncate(trafficBucket))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		h := &HostTraffic{}
		var last time.Time
		if err := rows.Scan(&h.HostID, &h.Requests, &h.S4xx, &h.S5xx, &h.Bytes, &last); err != nil {
			rows.Close()
			return nil, err
		}
		last = last.Add(trafficBucket)
		if last.After(now) {
			last = now
		}
		h.Last = &last
		h.Domain = domainOr(domains, h.HostID)
		byHost[h.HostID] = h
		v.Totals.Requests += h.Requests
		v.Totals.S4xx += h.S4xx
		v.Totals.S5xx += h.S5xx
		v.Totals.Bytes += h.Bytes
	}
	rows.Close()
	hourFrom := from.Truncate(time.Hour)
	rows, err = s.pool.Query(ctx, `SELECT host_id, count(DISTINCT ip), sum(denied) FROM publish_traffic_ips WHERE hour >= $1 GROUP BY host_id`, hourFrom)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, visitors, denied int
		if err := rows.Scan(&id, &visitors, &denied); err != nil {
			rows.Close()
			return nil, err
		}
		if h, ok := byHost[id]; ok {
			h.Visitors, h.Denied = visitors, denied
		}
		v.Totals.Denied += denied
	}
	rows.Close()
	_ = s.pool.QueryRow(ctx, `SELECT count(DISTINCT ip) FROM publish_traffic_ips WHERE hour >= $1`, hourFrom).Scan(&v.Totals.Visitors)
	for _, h := range byHost {
		v.Hosts = append(v.Hosts, *h)
	}
	sort.Slice(v.Hosts, func(i, j int) bool {
		if v.Hosts[i].Requests != v.Hosts[j].Requests {
			return v.Hosts[i].Requests > v.Hosts[j].Requests
		}
		return v.Hosts[i].Domain < v.Hosts[j].Domain
	})

	// IP's: de drukste en de meest geweigerde, met de bans erbij.
	bans, err := s.bans(ctx)
	if err != nil {
		s.log.Warn("publicatie: bans van CrowdSec lezen", "err", err)
	}
	v.Compare.ActiveBans = len(bans)
	failed := s.failedByIP(ctx, from)
	rows, err = s.pool.Query(ctx, `SELECT ip, sum(requests), sum(s4xx), sum(s5xx), sum(denied), sum(bytes), min(first_at), max(last_at),
			array_agg(DISTINCT host_id), array_agg(hour ORDER BY hour), array_agg(requests ORDER BY hour)
		FROM publish_traffic_ips WHERE hour >= $1 GROUP BY ip
		ORDER BY sum(denied) DESC, sum(requests) DESC LIMIT 2000`, hourFrom)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p IPTraffic
		var hostIDs []int32
		var hours []time.Time
		var perHour []int32
		if err := rows.Scan(&p.IP, &p.Requests, &p.S4xx, &p.S5xx, &p.Denied, &p.Bytes, &p.First, &p.Last, &hostIDs, &hours, &perHour); err != nil {
			rows.Close()
			return nil, err
		}
		for _, id := range hostIDs {
			p.Hosts = append(p.Hosts, domainOr(domains, int(id)))
		}
		sort.Strings(p.Hosts)
		p.FailedLogins = failed[p.IP]
		if b := banFor(bans, p.IP); b != nil {
			p.Ban = b
			v.Compare.BannedSeen++
			for i, h := range hours {
				// Het uur van de ban zelf telt niet: daarin kan het verkeer
				// van voor de ban zitten.
				if h.After(b.Since.Truncate(time.Hour)) && i < len(perHour) {
					p.AfterBan += int(perHour[i])
				}
			}
			if p.AfterBan > 0 && p.Last.After(b.Since) {
				v.Compare.BannedPassing++
			} else {
				p.AfterBan = 0
			}
		} else {
			p.Suspicious, p.Why = suspect(p)
			if p.Suspicious {
				v.Compare.Suspicious++
			}
		}
		v.IPs = append(v.IPs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Eerst wat aandacht vraagt, dan de drukste.
	rank := func(p IPTraffic) int {
		switch {
		case p.Ban != nil && p.AfterBan > 0:
			return 0
		case p.Suspicious:
			return 1
		case p.Ban != nil:
			return 2
		}
		return 3
	}
	sort.SliceStable(v.IPs, func(i, j int) bool {
		ri, rj := rank(v.IPs[i]), rank(v.IPs[j])
		if ri != rj {
			return ri < rj
		}
		return v.IPs[i].Requests > v.IPs[j].Requests
	})
	if len(v.IPs) > maxTrafficIPs {
		v.IPs = v.IPs[:maxTrafficIPs]
	}

	// Open verbindingen: de laatste meting en de lijn.
	rows, err = s.pool.Query(ctx, `SELECT to_timestamp(floor(extract(epoch FROM at) / $2) * $2), max(connections)
		FROM publish_connections WHERE at >= $1 GROUP BY 1 ORDER BY 1 LIMIT $3`, maxTime(from, now.Add(-keepConnections)), step.Seconds(), maxSeriesSteps)
	if err == nil {
		for rows.Next() {
			var c ConnPoint
			if rows.Scan(&c.T, &c.Count) == nil {
				v.Connections = append(v.Connections, c)
			}
		}
		rows.Close()
	}
	var at time.Time
	if err := s.pool.QueryRow(ctx, `SELECT at, connections FROM publish_connections ORDER BY at DESC LIMIT 1`).Scan(&at, &v.Totals.Connections); err == nil {
		v.Totals.ConnAt = &at
	}
	v.Feeds = s.feeds(ctx)
	return v, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// suspect: veel 401/403 of veel fouten, of mislukte logins.
func suspect(p IPTraffic) (bool, string) {
	switch {
	case p.FailedLogins >= suspectFailed:
		return true, fmt.Sprintf("%d mislukte logins in Authentik", p.FailedLogins)
	case p.Denied >= suspectDenied:
		return true, fmt.Sprintf("%d keer geweigerd (401/403)", p.Denied)
	case p.S4xx >= suspectErrors && p.S4xx*2 > p.Requests:
		return true, fmt.Sprintf("%d van de %d aanvragen gaven een 4xx (scannen?)", p.S4xx, p.Requests)
	}
	return false, ""
}

func (s *Service) failedByIP(ctx context.Context, from time.Time) map[string]int {
	out := map[string]int{}
	rows, err := s.pool.Query(ctx, `SELECT client_ip, count(*) FROM publish_auth_events
		WHERE action = 'login_failed' AND created >= $1 AND client_ip <> '' GROUP BY client_ip`, from)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ip string
		var n int
		if rows.Scan(&ip, &n) == nil {
			out[ip] = n
		}
	}
	return out
}

func (s *Service) feeds(ctx context.Context) []FeedState {
	out := []FeedState{}
	rows, err := s.pool.Query(ctx, `SELECT source, cursor, at, ok_at, error, lines FROM publish_feed_state
		WHERE source IN ('npm', 'authentik') ORDER BY source`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var f FeedState
		if rows.Scan(&f.Source, &f.Detail, &f.At, &f.OKAt, &f.Error, &f.Lines) == nil {
			out = append(out, f)
		}
	}
	return out
}

// AuthEvent is een login of poging.
type AuthEvent struct {
	Created  time.Time `json:"created"`
	Action   string    `json:"action"`
	Username string    `json:"username"`
	IP       string    `json:"ip"`
	App      string    `json:"app,omitempty"`
	Country  string    `json:"country,omitempty"`
	City     string    `json:"city,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Banned   bool      `json:"banned"`
}

// UserLogins is een gebruiker met zijn logins.
type UserLogins struct {
	Username string     `json:"username"`
	Logins   int        `json:"logins"`
	Failed   int        `json:"failed"`
	Last     *time.Time `json:"last,omitempty"`
	LastIP   string     `json:"last_ip,omitempty"`
}

// FailedIP is een IP met mislukte logins.
type FailedIP struct {
	IP        string    `json:"ip"`
	Failed    int       `json:"failed"`
	Usernames []string  `json:"usernames"`
	Last      time.Time `json:"last"`
	Country   string    `json:"country,omitempty"`
	Ban       *Ban      `json:"ban,omitempty"`
	// Requests: aanvragen van dit IP bij NPM in het venster.
	Requests int `json:"requests"`
}

// AppLogins zijn de keren dat een app geopend werd via Authentik.
type AppLogins struct {
	App   string `json:"app"`
	Count int    `json:"count"`
	Users int    `json:"users"`
}

// LoginPoint is één punt van de grafiek.
type LoginPoint struct {
	T      time.Time `json:"t"`
	Logins int       `json:"logins"`
	Failed int       `json:"failed"`
}

// LoginsView is de pagina Logins en de bron logins van de homepage-API.
type LoginsView struct {
	Window string    `json:"window"`
	From   time.Time `json:"from"`
	Totals struct {
		Logins     int `json:"logins"`
		Failed     int `json:"failed"`
		Users      int `json:"users"`
		FailedIPs  int `json:"failed_ips"`
		Unbanned   int `json:"failed_ips_not_banned"`
		AppOpening int `json:"app_authorizations"`
	} `json:"totals"`
	Series    []LoginPoint `json:"series"`
	Users     []UserLogins `json:"users"`
	FailedIPs []FailedIP   `json:"failed_ips"`
	Apps      []AppLogins  `json:"apps"`
	Recent    []AuthEvent  `json:"recent"`
	Feed      *FeedState   `json:"feed,omitempty"`
}

// Logins bouwt de weergave voor een venster.
func (s *Service) Logins(ctx context.Context, window string) (*LoginsView, error) {
	w, d, err := ParseWindow(window)
	if err != nil {
		return nil, err
	}
	now := s.now()
	from := now.Add(-d)
	v := &LoginsView{Window: w, From: from, Series: []LoginPoint{}, Users: []UserLogins{}, FailedIPs: []FailedIP{}, Apps: []AppLogins{}, Recent: []AuthEvent{}}
	step := map[string]time.Duration{"1h": 5 * time.Minute, "24h": time.Hour, "7d": 6 * time.Hour}[w]
	rows, err := s.pool.Query(ctx, `SELECT to_timestamp(floor(extract(epoch FROM created) / $2) * $2),
			count(*) FILTER (WHERE action = 'login'), count(*) FILTER (WHERE action = 'login_failed')
		FROM publish_auth_events WHERE created >= $1 AND action IN ('login', 'login_failed') GROUP BY 1 ORDER BY 1 LIMIT $3`, from, step.Seconds(), maxSeriesSteps)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p LoginPoint
		if err := rows.Scan(&p.T, &p.Logins, &p.Failed); err != nil {
			rows.Close()
			return nil, err
		}
		v.Series = append(v.Series, p)
		v.Totals.Logins += p.Logins
		v.Totals.Failed += p.Failed
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT username, count(*) FILTER (WHERE action = 'login'), count(*) FILTER (WHERE action = 'login_failed'),
			max(created), (array_agg(client_ip ORDER BY created DESC))[1]
		FROM publish_auth_events WHERE created >= $1 AND action IN ('login', 'login_failed') AND username <> ''
		GROUP BY username ORDER BY 3 DESC, 2 DESC, 1 LIMIT 200`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u UserLogins
		var last time.Time
		if err := rows.Scan(&u.Username, &u.Logins, &u.Failed, &last, &u.LastIP); err != nil {
			rows.Close()
			return nil, err
		}
		u.Last = &last
		v.Users = append(v.Users, u)
		if u.Logins > 0 {
			v.Totals.Users++
		}
	}
	rows.Close()

	bans, err := s.bans(ctx)
	if err != nil {
		s.log.Warn("publicatie: bans van CrowdSec lezen", "err", err)
	}
	reqs := map[string]int{}
	if r2, err := s.pool.Query(ctx, `SELECT ip, sum(requests) FROM publish_traffic_ips WHERE hour >= $1 GROUP BY ip`, from.Truncate(time.Hour)); err == nil {
		for r2.Next() {
			var ip string
			var n int
			if r2.Scan(&ip, &n) == nil {
				reqs[ip] = n
			}
		}
		r2.Close()
	}
	rows, err = s.pool.Query(ctx, `SELECT client_ip, count(*), array_agg(DISTINCT username), max(created), max(country)
		FROM publish_auth_events WHERE created >= $1 AND action = 'login_failed' AND client_ip <> ''
		GROUP BY client_ip ORDER BY 2 DESC, 4 DESC LIMIT 200`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f FailedIP
		if err := rows.Scan(&f.IP, &f.Failed, &f.Usernames, &f.Last, &f.Country); err != nil {
			rows.Close()
			return nil, err
		}
		f.Ban = banFor(bans, f.IP)
		f.Requests = reqs[f.IP]
		if f.Ban == nil {
			v.Totals.Unbanned++
		}
		v.FailedIPs = append(v.FailedIPs, f)
	}
	rows.Close()
	v.Totals.FailedIPs = len(v.FailedIPs)

	rows, err = s.pool.Query(ctx, `SELECT app, count(*), count(DISTINCT username) FROM publish_auth_events
		WHERE created >= $1 AND action = 'authorize_application' AND app <> '' GROUP BY app ORDER BY 2 DESC, 1 LIMIT 100`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a AppLogins
		if err := rows.Scan(&a.App, &a.Count, &a.Users); err != nil {
			rows.Close()
			return nil, err
		}
		v.Apps = append(v.Apps, a)
		v.Totals.AppOpening += a.Count
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT created, action, username, client_ip, app, country, city, reason FROM publish_auth_events
		WHERE created >= $1 ORDER BY created DESC LIMIT 100`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e AuthEvent
		if err := rows.Scan(&e.Created, &e.Action, &e.Username, &e.IP, &e.App, &e.Country, &e.City, &e.Reason); err != nil {
			rows.Close()
			return nil, err
		}
		e.Banned = banFor(bans, e.IP) != nil
		v.Recent = append(v.Recent, e)
	}
	rows.Close()
	for _, f := range s.feeds(ctx) {
		if f.Source == "authentik" {
			f := f
			v.Feed = &f
		}
	}
	return v, rows.Err()
}

// HostUsage is het verkeer van één host voor de homepage-API.
type HostUsage struct {
	Requests int
	Visitors int
	S5xx     int
	Denied   int
}

// UsageByDomain geeft het verkeer per domein van de laatste 24 uur en de
// mislukte logins per app in Authentik, voor de tegels van de homepage-API.
func (s *Service) UsageByDomain(ctx context.Context) (map[string]HostUsage, map[string]int, error) {
	domains := s.hostDomains(ctx)
	from := s.now().Add(-24 * time.Hour)
	out := map[string]HostUsage{}
	rows, err := s.pool.Query(ctx, `SELECT t.host_id, t.req, t.s5, coalesce(i.visitors, 0), coalesce(i.denied, 0)
		FROM (SELECT host_id, sum(requests) AS req, sum(s5xx) AS s5 FROM publish_traffic WHERE bucket >= $1 GROUP BY host_id) t
		LEFT JOIN (SELECT host_id, count(DISTINCT ip) AS visitors, sum(denied) AS denied FROM publish_traffic_ips WHERE hour >= $2 GROUP BY host_id) i
		ON i.host_id = t.host_id`, from.Truncate(trafficBucket), from.Truncate(time.Hour))
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id int
		var u HostUsage
		if err := rows.Scan(&id, &u.Requests, &u.S5xx, &u.Visitors, &u.Denied); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if d, ok := domains[id]; ok {
			out[strings.ToLower(d)] = u
		}
	}
	rows.Close()
	apps := map[string]int{}
	rows, err = s.pool.Query(ctx, `SELECT lower(app), count(*) FROM publish_auth_events WHERE created >= $1 AND action = 'authorize_application' AND app <> '' GROUP BY 1`, from)
	if err != nil {
		return out, apps, nil
	}
	for rows.Next() {
		var a string
		var n int
		if rows.Scan(&a, &n) == nil {
			apps[a] = n
		}
	}
	rows.Close()
	return out, apps, nil
}

// feedNotices zijn de meldingen van verkeer en logins voor Aandacht nodig.
func (s *Service) feedNotices(ctx context.Context) []Notice {
	var out []Notice
	st, err := s.Settings(ctx)
	if err != nil || (!st.NPMLinked() && !st.AuthentikLinked()) {
		return nil
	}
	for _, f := range s.feeds(ctx) {
		if f.Error == "" || (f.OKAt != nil && time.Since(*f.OKAt) < 10*time.Minute) {
			continue
		}
		title := "Verkeer van NPM niet te lezen"
		if f.Source == "authentik" {
			title = "Logins van Authentik niet te lezen"
		}
		out = append(out, Notice{ID: "publish-feed-" + f.Source, Title: title, Detail: f.Error, Href: "/publicatie/verkeer", Count: 1})
	}
	from := s.now().Add(-time.Hour)
	bans, _ := s.bans(ctx)
	// Mislukte logins van één IP zonder ban.
	var names []string
	for ip, n := range s.failedByIP(ctx, from) {
		if n >= 10 && banFor(bans, ip) == nil {
			names = append(names, fmt.Sprintf("%s (%d)", ip, n))
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		out = append(out, Notice{ID: "publish-failed-logins", Red: true, Title: "Veel mislukte logins in Authentik zonder ban van CrowdSec",
			Names: names, Href: "/publicatie/logins", Count: len(names)})
	}
	// Geblokkeerde IP's die NPM nog doorlaat (het uur na het begin van de ban).
	var passing []string
	rows, err := s.pool.Query(ctx, `SELECT ip, sum(requests), max(last_at) FROM publish_traffic_ips WHERE hour >= $1 GROUP BY ip`, from.Truncate(time.Hour))
	if err == nil {
		for rows.Next() {
			var ip string
			var n int
			var last time.Time
			if rows.Scan(&ip, &n, &last) != nil {
				continue
			}
			if b := banFor(bans, ip); b != nil && last.After(b.Since.Add(10*time.Minute)) {
				passing = append(passing, ip)
			}
		}
		rows.Close()
	}
	if len(passing) > 0 {
		sort.Strings(passing)
		out = append(out, Notice{ID: "publish-ban-passing", Title: "Door CrowdSec geblokkeerde IP's komen nog binnen bij NPM",
			Detail: "Staat er een bouncer op de NPM-node?", Names: passing, Href: "/publicatie/verkeer", Count: len(passing)})
	}
	// Veel serverfouten op een host in het laatste kwartier.
	domains := s.hostDomains(ctx)
	var bad []string
	rows, err = s.pool.Query(ctx, `SELECT host_id, sum(requests), sum(s5xx) FROM publish_traffic WHERE bucket >= $1 GROUP BY host_id`,
		s.now().Add(-15*time.Minute).Truncate(trafficBucket))
	if err == nil {
		for rows.Next() {
			var id, n, s5 int
			if rows.Scan(&id, &n, &s5) == nil && n >= 50 && s5*5 >= n {
				bad = append(bad, fmt.Sprintf("%s (%d%% 5xx)", domainOr(domains, id), s5*100/n))
			}
		}
		rows.Close()
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		out = append(out, Notice{ID: "publish-5xx", Red: true, Title: "Veel serverfouten achter NPM", Names: bad, Href: "/publicatie/verkeer", Count: len(bad)})
	}
	return out
}
