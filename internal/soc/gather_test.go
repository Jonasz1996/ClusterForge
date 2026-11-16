package soc

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func seedNode(t *testing.T, pool *pgxpool.Pool, host, ip string, cluster *uuid.UUID, proto int, security int, services ...string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, cluster_id, primary_ip) VALUES ($1, $2, $3) RETURNING id`, host, cluster, ip).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES (gen_random_uuid(), $1, $2, $3)`,
		id, "U"+strings.ToUpper(host), proto); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), $2)`, id, []string{ip + "/24"}); err != nil {
		t.Fatal(err)
	}
	f := protocol.Facts{Hostname: host, Upgrades: &protocol.Upgrades{Total: security, Security: security}}
	for _, s := range services {
		f.Services = append(f.Services, protocol.Service{Name: s, Active: "active", Enabled: "enabled"})
	}
	b, _ := json.Marshal(f)
	if _, err := pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'x', $2, now())`, id, b); err != nil {
		t.Fatal(err)
	}
	return id
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestOverview(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	var cluster uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment) VALUES ('npm', 'NPM', 'generic', 'prod') RETURNING id`).Scan(&cluster); err != nil {
		t.Fatal(err)
	}
	web1 := seedNode(t, pool, "web1", "192.168.102.245", &cluster, 34, 0)
	web2 := seedNode(t, pool, "web2", "192.168.102.246", &cluster, 34, 3)
	seedNode(t, pool, "db1", "192.168.10.191", nil, 34, 0, "crowdsec", "ufw")
	seedNode(t, pool, "oud", "192.168.10.50", nil, 20, 0)

	csStatus := func(bouncer string) []byte {
		b, _ := json.Marshal(protocol.CrowdSecStatus{Installed: true, Active: "active", Bouncer: bouncer, CAPI: true})
		return b
	}
	fwStatus, _ := json.Marshal(protocol.FirewallStatus{Installed: true, Active: true})
	exec(t, pool, `INSERT INTO crowdsec_nodes (node_id, managed, bouncer, status) VALUES ($1, true, true, $2), ($3, true, true, $4)`,
		web1, csStatus("active"), web2, csStatus("failed"))
	exec(t, pool, `INSERT INTO firewall_nodes (node_id, mode, status) VALUES ($1, 'managed', $2), ($3, 'preview', $2)`, web1, fwStatus, web2)

	// Een aanvaller van buiten op twee nodes, geband op web1.
	attacker := "203.0.113.66"
	for i, n := range []uuid.UUID{web1, web2, web2} {
		exec(t, pool, `INSERT INTO crowdsec_alerts (node_id, alert_id, scenario, scope, value, country, as_name, events, started_at, stopped_at)
			VALUES ($1, $2, 'crowdsecurity/http-probing', 'Ip', $3, 'NL', 'Hosting BV', 10, $4, $4)`, n, i+1, attacker, now.Add(-time.Duration(i+1)*time.Hour))
	}
	exec(t, pool, `INSERT INTO crowdsec_decisions (node_id, decision_id, origin, type, scope, value, scenario, until)
		VALUES ($1, 1, 'crowdsec', 'ban', 'Ip', $2, 'crowdsecurity/http-probing', $3)`, web1, attacker, now.Add(3*time.Hour))
	// Een eigen node die op een dichte poort klopt, en een onbekend intern adres.
	hour := now.Truncate(time.Hour)
	exec(t, pool, `INSERT INTO firewall_blocked (node_id, hour, src, proto, port, count, first_seen, last_seen)
		VALUES ($1, $2, '192.168.10.191', 'tcp', 9100, 500, $2, $2), ($1, $2, '192.168.90.20', 'tcp', 22, 7, $2, $2),
		       ($3, $2, $4, 'tcp', 443, 30, $2, $2)`, web1, hour, web2, attacker)
	// Mislukte logins op ClusterForge vanaf een extern adres.
	for range BruteForceLogins {
		exec(t, pool, `INSERT INTO events (actor_type, subject_type, action, payload) VALUES ('system', 'user', 'auth.login_failed', '{"ip":"198.51.100.9","reason":"bad_password"}')`)
	}
	exec(t, pool, `INSERT INTO users (username, password_hash, role) VALUES ('jonas', 'x', 'admin')`)

	s := NewService(pool, slog.New(slog.DiscardHandler))
	o, err := s.Overview(ctx, Window24h)
	if err != nil {
		t.Fatal(err)
	}

	if o.Level.Level != LevelCritical || !strings.Contains(o.Level.Reasons[0], "web2") {
		t.Errorf("niveau = %+v (bouncer plat op aangevallen web2 hoort voorop)", o.Level)
	}
	if o.Stats.Attacks != 3 || o.Stats.ActiveBans != 1 || o.Stats.Blocked != 537 || o.Stats.LoginFailures != BruteForceLogins {
		t.Errorf("cijfers = %+v", o.Stats)
	}
	if o.Stats.Nodes != 4 || o.Stats.SecurityNodes != 1 || o.Stats.SecurityUpdates != 3 || o.Stats.Protected != 1 {
		t.Errorf("dekking in cijfers = %+v", o.Stats)
	}

	cov := map[string]CoverageRow{}
	for _, c := range o.Coverage {
		cov[c.Hostname] = c
	}
	check := func(host, fw, cs, b string) {
		t.Helper()
		c := cov[host]
		if c.Firewall != fw || c.CrowdSec != cs || c.Bouncer != b {
			t.Errorf("%s: firewall %s, crowdsec %s, bouncer %q; wil %s, %s, %q (gaten %v)", host, c.Firewall, c.CrowdSec, c.Bouncer, fw, cs, b, c.Gaps)
		}
	}
	check("web1", StateOn, StateOn, StateOn)
	check("web2", StatePreview, StateOn, StateDown)
	check("db1", StateUnmanaged, StateUnmanaged, "")
	check("oud", StateUnsupported, StateUnsupported, "")
	if len(cov["web1"].Gaps) != 0 {
		t.Errorf("web1 heeft gaten: %v", cov["web1"].Gaps)
	}
	if g := cov["web2"].Gaps; !slices.Contains(g, "bouncer draait niet") || !slices.Contains(g, "3 beveiligingsupdates") {
		t.Errorf("gaten web2 = %v", g)
	}

	byIP := map[string]Attacker{}
	for _, a := range o.Attackers {
		byIP[a.IP] = a
	}
	a := byIP[attacker]
	if a.Kind != KindExternal || a.Alerts != 3 || a.Blocked != 30 || !a.Banned || a.Country != "NL" ||
		!slices.Equal(a.Nodes, []string{"web1", "web2"}) || !slices.Equal(a.Sources, []string{SourceCrowdSec, SourceFirewall}) ||
		!slices.Equal(a.Ports, []string{"443/tcp"}) || !slices.Equal(a.Scenarios, []string{"crowdsecurity/http-probing"}) {
		t.Errorf("aanvaller = %+v", a)
	}
	if o.Attackers[0].IP != attacker {
		t.Errorf("de aanvaller van buiten met alerts hoort bovenaan, niet %s", o.Attackers[0].IP)
	}
	if n := byIP["192.168.10.191"]; n.Kind != KindNode || n.Node != "db1" {
		t.Errorf("eigen node = %+v", n)
	}
	if n := byIP["192.168.90.20"]; n.Kind != KindInternal {
		t.Errorf("intern = %+v", n)
	}
	if l := byIP["198.51.100.9"]; l.Logins != BruteForceLogins || l.Kind != KindExternal || !slices.Equal(l.Nodes, []string{"ClusterForge"}) {
		t.Errorf("login = %+v", l)
	}

	keys := map[string]bool{}
	for _, it := range o.Now {
		keys[it.Key] = true
	}
	for _, k := range []string{"bouncer-hit", "bruteforce-198.51.100.9", "totp", "security-updates", "firewall-preview", "crowdsec-unmanaged", "firewall-unmanaged"} {
		if !keys[k] {
			t.Errorf("punt %s ontbreekt in %v", k, keys)
		}
	}

	var total int64
	for _, b := range o.Activity {
		total += int64(b.CrowdSec) + b.Firewall + int64(b.Logins)
	}
	if len(o.Activity) != 24 || total != 3+537+BruteForceLogins {
		t.Errorf("activiteit: %d staven, totaal %d", len(o.Activity), total)
	}
	if len(o.Targets.Nodes) != 2 || o.Targets.Nodes[0].Label != "web1" || o.Targets.Nodes[0].Count != 508 {
		t.Errorf("doelwitten = %+v", o.Targets.Nodes)
	}
	if len(o.Score.Trend) != 1 || o.Score.Trend[0].Score != o.Score.Score || o.Score.Previous != nil {
		t.Errorf("trend zonder momentopnames = %+v, vorige %v", o.Score.Trend, o.Score.Previous)
	}

	// Momentopnames: een van gisteren wordt Previous, die van nu het laatste punt.
	exec(t, pool, `INSERT INTO soc_snapshots (hour, score, level) VALUES ($1, 77, 'waakzaam')`, now.Add(-24*time.Hour).Truncate(time.Hour))
	if err := s.snapshot(ctx, o.Score, o.Level, now); err != nil {
		t.Fatal(err)
	}
	s.Forget()
	o2, err := s.Overview(ctx, Window7d)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Score.Previous != nil {
		t.Errorf("7 dagen geleden is er geen opname, toch vorige %v", *o2.Score.Previous)
	}
	if len(o2.Activity) != 42 || len(o2.Score.Trend) != 2 {
		t.Errorf("7 d: %d staven, trend %+v", len(o2.Activity), o2.Score.Trend)
	}
	o3, err := s.Overview(ctx, Window24h)
	if err != nil {
		t.Fatal(err)
	}
	if o3.Score.Previous == nil || *o3.Score.Previous != 77 {
		t.Errorf("vorige = %v", o3.Score.Previous)
	}
}
