package firewall

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

// fakeBus speelt agents met ufw na.
type fakeBus struct {
	mu        sync.Mutex
	listeners map[uuid.UUID][]protocol.FirewallListener
	rules     map[uuid.UUID][]protocol.FirewallRule
	active    map[uuid.UUID]bool
	pending   map[uuid.UUID]string
	applied   map[uuid.UUID]string
	hits      map[uuid.UUID][]protocol.FirewallHit
	ops       []string
	// dropProbe: een probe naar dit doel krijgt geen antwoord.
	dropProbe string
}

func newFakeBus() *fakeBus {
	return &fakeBus{listeners: map[uuid.UUID][]protocol.FirewallListener{}, rules: map[uuid.UUID][]protocol.FirewallRule{},
		active: map[uuid.UUID]bool{}, pending: map[uuid.UUID]string{}, applied: map[uuid.UUID]string{}, hits: map[uuid.UUID][]protocol.FirewallHit{}}
}

func (b *fakeBus) Firewall(_ context.Context, id uuid.UUID, req protocol.FirewallRequest) (protocol.FirewallResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ops = append(b.ops, req.Op+" "+id.String())
	switch req.Op {
	case protocol.FirewallOpStatus:
		st := &protocol.FirewallStatus{Installed: true, Version: "0.36.2", Active: b.active[id], Incoming: "deny", Outgoing: "allow",
			Rules: slices.Clone(b.rules[id]), Listeners: b.listeners[id], Monitor: "nflog"}
		if h := b.applied[id]; h != "" {
			st.Applied = &protocol.FirewallApplied{Hash: h}
		}
		if p := b.pending[id]; p != "" {
			st.Pending = &protocol.FirewallPending{ApplyID: p, Deadline: time.Now().Add(time.Minute)}
		}
		if st.Rules == nil {
			st.Rules = []protocol.FirewallRule{}
		}
		return protocol.FirewallResponse{Status: st}, nil
	case protocol.FirewallOpApply:
		a := req.Apply
		d := protocol.DiffFirewall(b.rules[id], a.Rules, a.Strict)
		b.rules[id], b.active[id], b.pending[id] = slices.Clone(a.Rules), true, a.ID+"|"+a.Hash
		return protocol.FirewallResponse{Apply: &protocol.FirewallApplyResult{ApplyID: a.ID, Changed: true, Diff: d, Log: []string{"ufw aan"}}}, nil
	case protocol.FirewallOpConfirm:
		id0, hash, _ := strings.Cut(b.pending[id], "|")
		if id0 != req.ApplyID {
			return protocol.FirewallResponse{Error: "geen openstaande toepassing"}, nil
		}
		b.applied[id], b.pending[id] = hash, ""
		return protocol.FirewallResponse{}, nil
	case protocol.FirewallOpRollback:
		b.rules[id], b.pending[id], b.active[id] = nil, "", false
		return protocol.FirewallResponse{}, nil
	case protocol.FirewallOpProbe:
		var out []protocol.FirewallProbe
		for _, t := range req.Probe {
			out = append(out, protocol.FirewallProbe{Target: t, OK: t != b.dropProbe, Error: map[bool]string{true: "i/o timeout"}[t == b.dropProbe]})
		}
		return protocol.FirewallResponse{Probe: out}, nil
	case protocol.FirewallOpBlocked:
		h := b.hits[id]
		b.hits[id] = nil
		return protocol.FirewallResponse{Blocked: &protocol.FirewallBlocked{Source: "nflog", Hits: h}}, nil
	}
	return protocol.FirewallResponse{Error: "?"}, nil
}

func seed(t *testing.T, pool *pgxpool.Pool, host, ip string, cluster *uuid.UUID, proto int, services ...string) uuid.UUID {
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
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), '{}')`, id); err != nil {
		t.Fatal(err)
	}
	f := protocol.Facts{Hostname: host, PrimaryAddress: ip, Keepalived: &protocol.Keepalived{Active: "active"}}
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
	s.CheckDelay = 0
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go runner.Run(rctx)

	var cluster uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment, template_name) VALUES ('authentik', 'Authentik', 'generic', 'prod', 'authentik') RETURNING id`).Scan(&cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO vips (cluster_id, address, vrid) VALUES ($1, '192.168.103.244', 44)`, cluster); err != nil {
		t.Fatal(err)
	}
	units := []string{"ssh", "keepalived", "patroni", "etcd", "cf-authentik"}
	a := seed(t, pool, "authentik1", "192.168.103.242", &cluster, protocol.FirewallSince, units...)
	b := seed(t, pool, "authentik2", "192.168.103.243", &cluster, protocol.FirewallSince, units...)
	c := seed(t, pool, "authentik3", "192.168.103.241", &cluster, protocol.FirewallSince, units...)
	old := seed(t, pool, "oud", "192.168.10.5", nil, 29, "ssh")
	for _, id := range []uuid.UUID{a, b, c} {
		bus.listeners[id] = []protocol.FirewallListener{
			{Proto: "tcp", Addr: "0.0.0.0", Port: 22, Process: "sshd", Unit: "ssh"},
			{Proto: "tcp", Addr: "0.0.0.0", Port: 2379, Process: "etcd", Unit: "etcd"},
			{Proto: "tcp", Addr: "0.0.0.0", Port: 8008, Process: "patroni", Unit: "patroni"},
		}
	}

	skipped, err := s.SetMode(ctx, events.System(), []uuid.UUID{a, b, c, old}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "oud") {
		t.Errorf("overgeslagen %v", skipped)
	}
	// Voorbeeld: plan en verschil, niets toegepast.
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	na, err := s.Node(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if na.Plan == nil || na.Diff == nil || len(na.Diff.Add) == 0 || !na.Pending() {
		t.Fatalf("voorbeeld: %+v", na)
	}
	if slices.ContainsFunc(bus.ops, func(o string) bool { return strings.HasPrefix(o, "apply") }) {
		t.Fatal("toegepast in voorbeeld")
	}
	var etcd []string
	for _, r := range na.Plan.Rules {
		if r.Port == "2379" {
			etcd = append(etcd, r.From)
		}
	}
	slices.Sort(etcd)
	if !slices.Equal(etcd, []string{"192.168.103.241", "192.168.103.243", "192.168.103.244"}) {
		t.Errorf("etcd open voor %v", etcd)
	}

	// Beheerd: automatisch toepassen, controle vanaf de andere nodes, bevestigen.
	if _, err := s.SetMode(ctx, events.System(), []uuid.UUID{a}, "managed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	wait := func(id uuid.UUID, ok func(Node) bool) Node {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			n, err := s.Node(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if !n.ApplyRunning && ok(n) {
				return n
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %+v", n.Hostname, n)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	na = wait(a, func(n Node) bool { return n.AppliedHash != "" && !n.Pending() })
	if na.ApplyError != "" || !bus.active[a] || bus.applied[a] != na.PlanHash {
		t.Fatalf("toegepast: %+v", na)
	}
	if !slices.ContainsFunc(bus.ops, func(o string) bool { return strings.HasPrefix(o, "probe ") && !strings.HasSuffix(o, a.String()) }) {
		t.Errorf("geen controle vanaf een ander clusterlid: %v", bus.ops)
	}

	// Verkeer naar een dichte poort vanaf een clusterlid.
	now := time.Now().UTC()
	bus.mu.Lock()
	bus.hits[a] = []protocol.FirewallHit{{Src: "192.168.103.243", Proto: "tcp", Port: 6379, Count: 12, First: now.Add(-time.Minute), Last: now},
		{Src: "203.0.113.9", Proto: "tcp", Port: 3306, Count: 3, First: now, Last: now}}
	bus.mu.Unlock()
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	tr, err := s.Traffic(ctx, TrafficQuery{ClusterID: &cluster})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 2 || tr[0].Port != 6379 || tr[0].SrcNode != "authentik2" || !tr[0].SameCluster || tr[0].Software != "Redis" || tr[1].SameCluster {
		t.Fatalf("verkeer: %+v", tr)
	}
	o, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Stats.Blocked24h != 15 || o.Stats.Managed != 1 || o.Stats.Preview != 2 || len(o.Clusters) != 1 {
		t.Fatalf("overzicht: %+v", o.Stats)
	}
	var redis *PortRow
	for i, r := range o.Clusters[0].Ports {
		if r.Port == "6379" {
			redis = &o.Clusters[0].Ports[i]
		}
	}
	if redis == nil || redis.Cells[a.String()].Blocked24h != 12 {
		t.Errorf("redis-rij: %+v", redis)
	}

	// Een eigen regel zet 6379 open voor het cluster.
	if _, err := s.AddRule(ctx, events.System(), nil, Rule{Action: "open", Proto: "tcp", Port: "6380", Source: ClassCluster, ClusterID: &cluster, Comment: "redis extra"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRule(ctx, events.System(), nil, Rule{Action: "open", Proto: "tcp", Port: "80", Source: "host;rm"}); err == nil {
		t.Error("ongeldige bron aanvaard")
	}

	// Een controle die faalt, draait terug.
	bus.mu.Lock()
	bus.dropProbe = "192.168.103.242:2379"
	bus.mu.Unlock()
	if _, err := pool.Exec(ctx, `UPDATE firewall_nodes SET status_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	na = wait(a, func(n Node) bool { return n.ApplyError != "" })
	if !strings.Contains(na.ApplyError, "bereikt 192.168.103.242:2379 niet meer") || bus.active[a] {
		t.Errorf("terugdraaien: %q, actief %v", na.ApplyError, bus.active[a])
	}
}
