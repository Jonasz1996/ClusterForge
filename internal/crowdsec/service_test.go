package crowdsec

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeBus speelt agents met CrowdSec na.
type fakeBus struct {
	mu        sync.Mutex
	installed map[uuid.UUID]bool
	applied   map[uuid.UUID]string
	alerts    map[uuid.UUID][]protocol.CrowdSecAlert
	bans      map[uuid.UUID][]protocol.CrowdSecActive
	decided   map[uuid.UUID][]protocol.CrowdSecDecision
	scripts   map[uuid.UUID]string
	failRun   bool
}

func newFakeBus() *fakeBus {
	return &fakeBus{installed: map[uuid.UUID]bool{}, applied: map[uuid.UUID]string{}, alerts: map[uuid.UUID][]protocol.CrowdSecAlert{},
		bans: map[uuid.UUID][]protocol.CrowdSecActive{}, decided: map[uuid.UUID][]protocol.CrowdSecDecision{}, scripts: map[uuid.UUID]string{}}
}

func (b *fakeBus) CrowdSec(_ context.Context, id uuid.UUID, req protocol.CrowdSecRequest) (protocol.CrowdSecResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch req.Op {
	case protocol.CrowdSecOpStatus:
		st := &protocol.CrowdSecStatus{Installed: b.installed[id], Probes: map[string][]string{}}
		if st.Installed {
			st.Version, st.Active, st.Bouncer, st.CAPI = "v1.6.4", "active", "active", true
			if h := b.applied[id]; h != "" {
				st.Applied = &protocol.CrowdSecAppliedState{Hash: h}
			}
		}
		return protocol.CrowdSecResponse{Status: st}, nil
	case protocol.CrowdSecOpPoll:
		return protocol.CrowdSecResponse{Poll: &protocol.CrowdSecPoll{Alerts: b.alerts[id], Decisions: b.bans[id], Blocklist: 15000}}, nil
	case protocol.CrowdSecOpDecide:
		b.decided[id] = append(b.decided[id], *req.Decide)
		return protocol.CrowdSecResponse{Count: 1}, nil
	}
	return protocol.CrowdSecResponse{Error: "?"}, nil
}

func (b *fakeBus) Run(_ context.Context, id uuid.UUID, req protocol.RunRequest) (protocol.RunResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if req.Op == protocol.RunStart {
		b.scripts[id] = req.Spec.Script
	}
	now := time.Now()
	exit := 0
	st := &protocol.RunStatus{State: protocol.RunOK, StartedAt: now, EndedAt: &now, Exit: &exit}
	if b.failRun {
		exit = 1
		st.State = protocol.RunFailed
	} else if s := b.scripts[id]; strings.Contains(s, "CrowdSec toepassen") {
		b.installed[id] = true
		i := strings.Index(s, "plan ")
		b.applied[id] = s[i+5 : i+21]
	}
	return protocol.RunResponse{Status: st, Output: "klaar\n", Offset: 6}, nil
}

func seed(t *testing.T, pool *pgxpool.Pool, host string, cluster *uuid.UUID, proto int, services ...string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, cluster_id) VALUES ($1, $2) RETURNING id`, host, cluster).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES (gen_random_uuid(), $1, $2, $3)`,
		id, "U"+strings.ToUpper(host), proto); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), '{}')`, id); err != nil {
		t.Fatal(err)
	}
	f := protocol.Facts{Hostname: host}
	for _, s := range services {
		f.Services = append(f.Services, protocol.Service{Name: s, Active: "active", Enabled: "enabled"})
	}
	b, _ := json.Marshal(f)
	if _, err := pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'x', $2, now())`, id, b); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestServiceFlow(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)
	runner := jobs.NewRunner(pool, ev, log)
	runner.Poll = 50 * time.Millisecond
	bus := newFakeBus()
	s := NewService(pool, ev, runner, bus, log)
	s.Run.Poll = 10 * time.Millisecond
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go runner.Run(rctx)

	var cluster uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment, template_name) VALUES ('npm', 'NPM', 'generic', 'prod', 'nginx-proxy-manager') RETURNING id`).Scan(&cluster); err != nil {
		t.Fatal(err)
	}
	a := seed(t, pool, "nginx-extern1", &cluster, protocol.CrowdSecSince, "ssh", "keepalived", "docker")
	b := seed(t, pool, "nginx-extern2", &cluster, protocol.CrowdSecSince, "ssh", "keepalived", "docker")
	c := seed(t, pool, "nc1", nil, protocol.CrowdSecSince, "ssh", "apache2", "mariadb")
	old := seed(t, pool, "oud", nil, 27, "ssh")

	skipped, err := s.Enable(ctx, events.System(), []uuid.UUID{a, b, c, old}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "oud") {
		t.Errorf("overgeslagen %v", skipped)
	}

	// Ronde 1: stand ophalen, plan uitrekenen, automatisch toepassen.
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	waitApplied := func(id uuid.UUID) Node {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			n, err := s.Node(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if n.AppliedHash != "" && !n.Pending() && !n.ApplyRunning {
				return n
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s niet toegepast: %+v", n.Hostname, n)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	na := waitApplied(a)
	waitApplied(b)
	nc := waitApplied(c)
	if na.Plan == nil || !slices.Contains(na.Plan.Collections, "crowdsecurity/nginx-proxy-manager") {
		t.Errorf("plan nginx-extern1: %+v", na.Plan)
	}
	if nc.Plan == nil || !slices.Equal(nc.Plan.Collections, []string{"crowdsecurity/apache2", "crowdsecurity/linux", "crowdsecurity/mariadb"}) {
		t.Errorf("plan nc1: %+v", nc.Plan)
	}
	if !strings.Contains(bus.scripts[a], "crowdsec-firewall-bouncer-nftables") {
		t.Error("bouncer staat standaard aan maar niet in het script")
	}

	// Ronde 2: stand met de toegepaste hash, alerts en een ban op nginx-extern1.
	now := time.Now().UTC().Truncate(time.Second)
	bus.mu.Lock()
	bus.alerts[a] = []protocol.CrowdSecAlert{{ID: 7, Scenario: "crowdsecurity/http-probing", Scope: "ip", Value: "203.0.113.9", Country: "CN",
		Events: 11, Decisions: 1, StartedAt: now.Add(-time.Minute), StoppedAt: now}}
	bus.bans[a] = []protocol.CrowdSecActive{{ID: 70, Origin: "crowdsec", Type: "ban", Scope: "ip", Value: "203.0.113.9",
		Scenario: "crowdsecurity/http-probing", Until: now.Add(4 * time.Hour), Country: "CN"}}
	bus.mu.Unlock()
	if _, err := pool.Exec(ctx, `UPDATE crowdsec_nodes SET status_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{b, c} {
		d := bus.decided[id]
		if len(d) != 1 || d[0].Value != "203.0.113.9" || !strings.HasPrefix(d[0].Reason, "cf:crowdsecurity/http-probing@nginx-extern1") {
			t.Errorf("gedeelde ban op %s: %+v", id, d)
		}
	}
	if len(bus.decided[a]) != 0 {
		t.Error("ban terug naar de bron gedeeld")
	}
	// Nog een ronde deelt dezelfde ban niet opnieuw; het alert komt er niet dubbel in.
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bus.decided[b]) != 1 {
		t.Errorf("ban opnieuw gedeeld: %+v", bus.decided[b])
	}
	alerts, err := s.Alerts(ctx, AlertFilter{ClusterID: &cluster})
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Hostname != "nginx-extern1" || alerts[0].ClusterName != "NPM" {
		t.Errorf("alerts %+v", alerts)
	}
	bans, err := s.Bans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bans) != 1 || !bans[0].Shared || bans[0].Scenario != "crowdsecurity/http-probing" {
		t.Errorf("bans %+v", bans)
	}
	o, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Stats.Managed != 3 || o.Stats.Running != 3 || o.Stats.Alerts24h != 1 || o.Stats.ActiveBans != 1 || o.Stats.Blocklist != 15000 {
		t.Errorf("stats %+v", o.Stats)
	}
	if len(o.Hourly) != 24 || len(o.TopIPs) != 1 || o.TopIPs[0].Country != "CN" {
		t.Errorf("grafiek/top %+v %+v", o.Hourly, o.TopIPs)
	}
	if len(o.Warnings) != 0 {
		t.Errorf("waarschuwingen %+v", o.Warnings)
	}
	if i := slices.IndexFunc(o.Nodes, func(n Node) bool { return n.Hostname == "oud" }); i < 0 || o.Nodes[i].Supported || o.Nodes[i].Enabled {
		t.Errorf("oude agent %+v", o.Nodes)
	}
	for _, e := range o.Catalog {
		if e.Key == "nginx-proxy-manager" && len(e.Nodes) != 2 {
			t.Errorf("NPM-profiel op %v", e.Nodes)
		}
	}

	// Handmatig deblokkeren op alle nodes; ban zetten in één cluster.
	res, err := s.Ban(ctx, events.System(), protocol.CrowdSecDecision{Delete: true, Scope: "ip", Value: "203.0.113.9"}, BanTarget{})
	if err != nil || len(res.Nodes) != 3 {
		t.Fatalf("deblokkeren %+v %v", res, err)
	}
	if bans, _ := s.Bans(ctx); len(bans) != 0 {
		t.Errorf("bans na deblokkeren %+v", bans)
	}
	res, err = s.Ban(ctx, events.System(), protocol.CrowdSecDecision{Scope: "range", Value: "198.51.100.0/24", Duration: "24h", Reason: "handmatig"},
		BanTarget{ClusterID: &cluster})
	if err != nil || len(res.Nodes) != 2 {
		t.Fatalf("blokkeren %+v %v", res, err)
	}
	if _, err := s.Ban(ctx, events.System(), protocol.CrowdSecDecision{Scope: "ip", Value: "x", Duration: "1h"}, BanTarget{}); err == nil {
		t.Error("ongeldig IP aanvaard")
	}

	// Profiel uitzetten maakt een nieuw plan; de bouncer uit ook.
	n, err := s.Configure(ctx, events.System(), a, false, nil, []string{"nginx-proxy-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if !n.Pending() || slices.Contains(n.Plan.Collections, "crowdsecurity/nginx-proxy-manager") || n.Plan.Bouncer {
		t.Errorf("na configureren %+v", n.Plan)
	}
	if _, err := s.Configure(ctx, events.System(), a, true, []string{"bestaat-niet"}, nil); err == nil {
		t.Error("onbekend profiel aanvaard")
	}

	// Mislukte apply: fout bewaard, geen nieuwe automatische poging binnen het uur.
	bus.mu.Lock()
	bus.failRun = true
	bus.mu.Unlock()
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		n, _ = s.Node(ctx, a)
		if n.ApplyError != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("geen fout na mislukte apply: %+v", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	job := *n.ApplyJobID
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n2, _ := s.Node(ctx, a); *n2.ApplyJobID != job {
		t.Error("na een mislukte apply meteen opnieuw geprobeerd")
	}

	// Uitzetten: geen rondes meer voor die node.
	if err := s.Disable(ctx, events.System(), []uuid.UUID{c}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Node(ctx, c); n.Managed {
		t.Error("nog beheerd na uitzetten")
	}
}
