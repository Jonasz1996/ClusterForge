package upkeep

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/internal/updates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeBus speelt agents na die het onderhoudsscript draaien.
type fakeBus struct {
	mu      sync.Mutex
	order   []uuid.UUID
	scripts map[uuid.UUID]string
	active  map[uuid.UUID]bool
	overlap bool
}

func (b *fakeBus) Run(_ context.Context, id uuid.UUID, req protocol.RunRequest) (protocol.RunResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if req.Op == protocol.RunStart {
		for other, on := range b.active {
			if on && other != id {
				b.overlap = true
			}
		}
		b.active[id] = true
		b.order = append(b.order, id)
		b.scripts[id] = req.Spec.Script
		return protocol.RunResponse{Status: &protocol.RunStatus{State: protocol.RunRunning, StartedAt: time.Now()}}, nil
	}
	b.active[id] = false
	now := time.Now()
	exit := 0
	out := "Bezig: systeemlogboek\nCFUPKEEP\tused\tbefore\t5000\nCFUPKEEP\tsize\tjournal\t1000\nCFUPKEEP\titem\tjournal\tok\t\n" +
		"CFUPKEEP\tdb\tmariadb\tgalera\tanalyze\t3\tstatistieken van 3 tabellen bijgewerkt\nCFUPKEEP\tused\tafter\t4000\nKlaar.\n"
	return protocol.RunResponse{Status: &protocol.RunStatus{State: protocol.RunOK, StartedAt: now, EndedAt: &now, Exit: &exit},
		Output: out, Offset: int64(len(out))}, nil
}

func seed(t *testing.T, pool *pgxpool.Pool, host string, cluster *uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, cluster_id) VALUES ($1, $2) RETURNING id`, host, cluster).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES (gen_random_uuid(), $1, $2, $3)`,
		id, "U"+strings.ToUpper(host), protocol.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), '{}')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRollingFlow(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)
	runner := jobs.NewRunner(pool, ev, log)
	runner.Poll = 50 * time.Millisecond
	bus := &fakeBus{scripts: map[uuid.UUID]string{}, active: map[uuid.UUID]bool{}}
	s := NewService(pool, ev, runner, bus, log)
	s.Run.Poll = 10 * time.Millisecond
	s.Gate.Poll = 20 * time.Millisecond
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Verse heartbeats, zoals echte agents.
	go func() {
		for rctx.Err() == nil {
			_, _ = pool.Exec(rctx, `UPDATE node_status SET heartbeat_at = clock_timestamp()`)
			time.Sleep(30 * time.Millisecond)
		}
	}()

	var cluster uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment, template_name) VALUES ('db', 'Databasecluster', 'generic', 'prod', 'mariadb-galera') RETURNING id`).Scan(&cluster); err != nil {
		t.Fatal(err)
	}
	db1 := seed(t, pool, "db1", &cluster)
	db2 := seed(t, pool, "db2", &cluster)
	db3 := seed(t, pool, "db3", &cluster)
	loose := seed(t, pool, "docker1", nil)
	s.Nodes = func(context.Context) ([]updates.Node, error) {
		c := &cluster
		return []updates.Node{
			{NodeID: db1, Hostname: "db1", ClusterID: c, Online: true, VIPHolder: true},
			{NodeID: db2, Hostname: "db2", ClusterID: c, Online: true},
			{NodeID: db3, Hostname: "db3", ClusterID: c, Online: true},
			{NodeID: loose, Hostname: "docker1", Online: true},
		}, nil
	}
	s.DB = func(context.Context) ([]databases.Group, error) {
		gal := &protocol.DBGalera{}
		return []databases.Group{{ClusterID: &cluster, Engine: protocol.DBMariaDB, Servers: []databases.Server{
			{NodeID: db1, Role: protocol.DBRoleMember, Galera: gal}, {NodeID: db2, Role: protocol.DBRoleMember, Galera: gal},
			{NodeID: db3, Role: protocol.DBRoleMember, Galera: gal}}}}, nil
	}

	// Prod aanzetten, nachtvenster rond nu.
	p := DefaultPolicy
	p.Enabled, p.PauseSeconds = true, 0
	now := time.Now()
	p.Window = "dagelijks " + now.Add(-time.Hour).Format("15:04") + "-" + now.Add(time.Hour).Format("15:04")
	if now.Hour() == 0 || now.Hour() == 23 {
		t.Skip("venster rond middernacht")
	}
	if err := s.SetPolicy(ctx, events.System(), nil, EnvScope("prod"), "", p); err != nil {
		t.Fatal(err)
	}
	sc, err := s.Scope(ctx, ClusterScope(cluster))
	if err != nil {
		t.Fatal(err)
	}
	if sc.Mode != ModeInherit || !sc.Effective.Enabled || sc.NextStart == nil || len(sc.Nodes) != 3 || sc.Nodes[2].NodeID != db1 || sc.Nodes[0].GaleraExpect != 3 {
		t.Fatalf("beleid: %+v", sc)
	}

	// Een andere taak op het cluster: de planner wacht.
	busy, err := runner.EnqueueForCluster(ctx, jobs.Spec{Kind: "test.wacht", Title: "bezet", ClusterID: &cluster, Actor: events.System()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	if sc, _ := s.Scope(ctx, ClusterScope(cluster)); sc.RunningJob != nil || sc.LastRun != nil {
		t.Fatalf("mocht niet starten naast een andere taak: %+v", sc)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'succeeded', finished_at = now() WHERE id = $1`, busy.ID); err != nil {
		t.Fatal(err)
	}
	// De runner pas nu: de bezette taak hierboven mocht niet door hem
	// afgehandeld worden.
	go runner.Run(rctx)

	if err := s.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	sc, _ = s.Scope(ctx, ClusterScope(cluster))
	if sc.LastRun == nil || sc.LastRun.JobID == nil {
		t.Fatalf("niet gestart: %+v", sc)
	}
	// Nog eens in hetzelfde venster: niets nieuws.
	if err := s.Tick(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	waitJob(t, pool, *sc.LastRun.JobID)

	bus.mu.Lock()
	order, overlap := append([]uuid.UUID(nil), bus.order...), bus.overlap
	script := bus.scripts[db2]
	bus.mu.Unlock()
	if overlap {
		t.Fatal("twee nodes van hetzelfde cluster tegelijk")
	}
	if len(order) != 3 || order[2] != db1 || order[0] != db2 || order[1] != db3 {
		t.Fatalf("volgorde: %v (db1 %s heeft de VIP)", order, db1)
	}
	if !strings.Contains(script, "GALERA_EXPECT=3") || !strings.Contains(script, "MODE=run") {
		t.Fatalf("script: %s", script[:200])
	}
	hist, err := s.History(ctx, nil, &cluster, 0)
	if err != nil || len(hist) != 3 || hist[0].Freed != 1000 || !hist[0].OK || hist[0].Result.DB[0].Count != 3 {
		t.Fatalf("logboek: %+v %v", hist, err)
	}

	// Voorbeeld voor de nodes zonder cluster: geen slot, mode preview.
	if err := s.SetPolicy(ctx, events.System(), nil, ScopeStandalone, "", Policy{Journal: true}); err != nil {
		t.Fatal(err)
	}
	id, err := s.Start(ctx, events.System(), ScopeStandalone, ModePreview, false)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, pool, id)
	sa, _ := s.Scope(ctx, ScopeStandalone)
	if len(sa.Nodes) != 1 || sa.Nodes[0].Preview == nil || sa.Nodes[0].Preview.Mode != ModePreview || sa.Nodes[0].Last != nil {
		t.Fatalf("voorbeeld: %+v", sa.Nodes)
	}
	bus.mu.Lock()
	if !strings.Contains(bus.scripts[loose], "MODE=preview") || strings.Contains(bus.scripts[loose], "MariaDB") {
		t.Fatal("voorbeeld met alleen het journal verwacht")
	}
	bus.mu.Unlock()

	// Cluster uit: de planner laat het staan.
	if err := s.SetPolicy(ctx, events.System(), nil, ClusterScope(cluster), ModeOff, DefaultPolicy); err != nil {
		t.Fatal(err)
	}
	if sc, _ := s.Scope(ctx, ClusterScope(cluster)); sc.Mode != ModeOff || sc.Effective.Enabled {
		t.Fatalf("uit: %+v", sc)
	}
}

func waitJob(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var st, msg string
		if err := pool.QueryRow(context.Background(), `SELECT status::text, error FROM jobs WHERE id = $1`, id).Scan(&st, &msg); err != nil {
			t.Fatal(err)
		}
		switch st {
		case "succeeded":
			return
		case "failed", "cancelled":
			t.Fatalf("taak %s: %s", st, msg)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("taak niet klaar")
}
