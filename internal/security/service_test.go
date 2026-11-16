package security

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type fakeBus struct {
	mu   sync.Mutex
	inv  map[uuid.UUID]*protocol.SecurityInventory
	reqs int
}

func (b *fakeBus) Security(_ context.Context, id uuid.UUID, req protocol.SecurityRequest) (protocol.SecurityResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reqs++
	if req.Op != protocol.SecurityOpInventory {
		return protocol.SecurityResponse{Error: "?"}, nil
	}
	inv := b.inv[id]
	if inv == nil {
		return protocol.SecurityResponse{Error: "dpkg-query mislukt"}, nil
	}
	return protocol.SecurityResponse{Inventory: inv}, nil
}

func seed(t *testing.T, pool *pgxpool.Pool, host string, proto int) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, primary_ip) VALUES ($1, '192.168.10.191') RETURNING id`, host).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES (gen_random_uuid(), $1, $2, $3)`,
		id, "U"+strings.ToUpper(host), proto); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), '{}')`, id); err != nil {
		t.Fatal(err)
	}
	f, _ := json.Marshal(protocol.Facts{Hostname: host, OS: protocol.OSInfo{ID: "debian", Codename: "trixie"}})
	if _, err := pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'x', $2, now())`, id, f); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestServiceFlow(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)

	var mu sync.Mutex
	debian := debianSample
	hits := map[string]int{}
	feeds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/debian":
			if r.Header.Get("If-None-Match") == `"d1"` && debian == debianSample {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"d1"`)
			_, _ = w.Write([]byte(debian))
		case "/kev":
			_, _ = w.Write([]byte(`{"vulnerabilities":[{"cveID":"CVE-2025-0003","vendorProject":"GNU","product":"Bash","knownRansomwareCampaignUse":"Known"}]}`))
		}
	}))
	defer feeds.Close()

	web := seed(t, pool, "web01", protocol.SecuritySince)
	old := seed(t, pool, "oud", 39)
	bus := &fakeBus{inv: map[uuid.UUID]*protocol.SecurityInventory{
		web: {OS: protocol.OSInfo{ID: "debian", Codename: "trixie"}, Dpkg: true, Packages: []protocol.DebPackage{
			{Name: "openssl", Version: "3.5.1-1", Source: "openssl", SourceVersion: "3.5.1-1"},
			{Name: "bash", Version: "5.2.37-1", Source: "bash", SourceVersion: "5.2.37-1"},
		}},
	}}
	s := NewService(pool, ev, bus, log)
	s.DebianURL, s.KEVURL = feeds.URL+"/debian", feeds.URL+"/kev"

	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := s.Findings(ctx, FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("bevindingen = %+v", fs)
	}
	if fs[0].Key != "bash" || fs[0].Severity != SevCritical || fs[0].Hostname != "web01" {
		t.Errorf("eerste = %+v", fs[0])
	}
	if fs[1].Key != "openssl" || fs[1].Severity != SevHigh || !strings.Contains(fs[1].Fix, "3.5.1-1+deb13u1") {
		t.Errorf("tweede = %+v", fs[1])
	}
	o, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Stats.Open != 2 || o.Stats.KEV != 1 || o.Stats.NodesChecked != 1 || o.Stats.NodesOldAgent != 1 {
		t.Errorf("cijfers = %+v", o.Stats)
	}
	for _, n := range o.Nodes {
		if n.NodeID == old && !strings.Contains(n.Note, "Agent te oud") {
			t.Errorf("oude agent: %q", n.Note)
		}
	}
	var kevEvents int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = 'security.kev_found'`).Scan(&kevEvents)
	if kevEvents != 1 {
		t.Errorf("kev-events = %d", kevEvents)
	}

	// Aanvaarden en heropenen.
	if _, err := s.Accept(ctx, events.System(), nil, fs[1].ID, "x", nil); err == nil {
		t.Error("te korte reden moet geweigerd worden")
	}
	until := time.Now().Add(time.Hour)
	if f, err := s.Accept(ctx, events.System(), nil, fs[1].ID, "Alleen intern bereikbaar", &until); err != nil || f.Status != StatusAccepted {
		t.Fatalf("aanvaarden: %v %+v", err, f)
	}
	if o, _ = s.Overview(ctx); o.Stats.Open != 1 || o.Stats.Accepted != 1 {
		t.Errorf("na aanvaarden: %+v", o.Stats)
	}
	if f, err := s.Reopen(ctx, events.System(), fs[1].ID); err != nil || f.Status != StatusOpen {
		t.Fatalf("heropenen: %v %+v", err, f)
	}

	// openssl bijgewerkt: de bevinding is opgelost; tweede ronde gebruikt
	// de ETag en haalt de feed niet opnieuw op.
	bus.mu.Lock()
	bus.inv[web].Packages[0].SourceVersion = "3.5.1-1+deb13u1"
	bus.mu.Unlock()
	s.Refresh(false, true, &web)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	fs, _ = s.Findings(ctx, FindingFilter{Status: "alle"})
	if len(fs) != 2 {
		t.Fatalf("na update = %+v", fs)
	}
	for _, f := range fs {
		if f.Key == "openssl" && (f.Status != StatusResolved || f.ResolvedAt == nil) {
			t.Errorf("openssl moet opgelost zijn: %+v", f)
		}
	}
	mu.Lock()
	if hits["/debian"] != 1 {
		t.Errorf("tracker opgehaald: %d keer", hits["/debian"])
	}
	mu.Unlock()
	s.Refresh(true, false, nil)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	feedsNow, _ := s.feeds(ctx)
	if f := feedsNow[FeedDebian]; f.Error != "" || f.Entries != 3 || f.OKAt == nil {
		t.Errorf("feed na 304: %+v", f)
	}

	// Scopes.
	sc, err := s.SaveScope(ctx, events.System(), nil, nil, ScopeInput{Name: "Thuis", CIDRs: []string{"192.168.10.0/24"}})
	if err != nil || sc.Addresses != 256 {
		t.Fatalf("scope: %v %+v", err, sc)
	}
	if _, err := s.SaveScope(ctx, events.System(), nil, nil, ScopeInput{Name: "Thuis", CIDRs: []string{"192.168.11.0/24"}}); err == nil {
		t.Error("dubbele naam moet geweigerd worden")
	}
	if err := s.DeleteScope(ctx, events.System(), sc.ID); err != nil {
		t.Fatal(err)
	}
}
