package databases

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeBus antwoordt per node met een vaste status of fout, en onthoudt wie
// er gevraagd werd.
type fakeBus struct {
	mu    sync.Mutex
	res   map[uuid.UUID]protocol.DBResponse
	err   map[uuid.UUID]error
	asked []uuid.UUID
}

func (b *fakeBus) DB(_ context.Context, nodeID uuid.UUID, req protocol.DBRequest) (protocol.DBResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if req.Op != protocol.DBStatus {
		return protocol.DBResponse{}, nil
	}
	b.asked = append(b.asked, nodeID)
	if err := b.err[nodeID]; err != nil {
		return protocol.DBResponse{}, err
	}
	return b.res[nodeID], nil
}

func (b *fakeBus) set(id uuid.UUID, res protocol.DBResponse, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.res[id], b.err[id] = res, err
}

func galeraNode(addr, state string, code int) protocol.DBServer {
	return protocol.DBServer{
		Engine: protocol.DBMariaDB, Unit: "mariadb", Active: "active", Running: true, Version: "11.8.3", Role: protocol.DBRoleMember,
		Connections: 10, MaxConnections: 151,
		Galera: &protocol.DBGalera{
			ClusterName: "nextcloud", ClusterStatus: "Primary", ClusterSize: 3, State: state, StateCode: code, Ready: true,
			Members: []string{"10.0.20.186", "10.0.20.187", "10.0.20.188"},
		},
		Databases:      []protocol.DBDatabase{{Name: "nextcloud", SizeBytes: 2 << 30, Tables: 158}},
		DatabasesTotal: 1,
	}
}

// seedNode maakt een node met een agent, een heartbeat van heartbeat
// geleden met adres addr, en facts met de units.
func seedNode(t *testing.T, pool *pgxpool.Pool, host string, cluster *uuid.UUID, protocolVersion int, heartbeat time.Duration,
	addr string, units ...string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, cluster_id) VALUES ($1, $2) RETURNING id`, host, cluster).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES (gen_random_uuid(), $1, $2, $3)`,
		id, "U"+strings.ToUpper(host), protocolVersion); err != nil {
		t.Fatal(err)
	}
	addrs := []string{}
	if addr != "" {
		addrs = append(addrs, addr)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, $2, $3)`, id, time.Now().Add(-heartbeat), addrs); err != nil {
		t.Fatal(err)
	}
	var services []protocol.Service
	for _, u := range append([]string{"ssh", "cf-agent"}, units...) {
		services = append(services, protocol.Service{Name: u, Active: "active", Enabled: "enabled"})
	}
	facts, _ := json.Marshal(map[string]any{"services": services})
	if _, err := pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'h', $2, now())`, id, facts); err != nil {
		t.Fatal(err)
	}
	return id
}

func group(t *testing.T, groups []Group, name, engine string) Group {
	t.Helper()
	for _, g := range groups {
		n := g.ClusterName
		if g.ClusterID == nil && len(g.Servers) > 0 {
			n = g.Servers[0].Hostname
		}
		if n == name && g.Engine == engine {
			return g
		}
	}
	t.Fatalf("geen groep %s/%s in %+v", name, engine, groups)
	return Group{}
}

// TestSyncAndOverview: drie Galera-nodes in één cluster, een primary met een
// replica, een losse PostgreSQL, een te oude agent en een agent die al
// minuten weg is. Het overzicht groepeert ze en zegt in één zin hoe het
// ervoor staat; een fout van één node houdt de andere niet tegen.
func TestSyncAndOverview(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()

	var nextcloud, gitea uuid.UUID
	for slug, dst := range map[string]*uuid.UUID{"nextcloud": &nextcloud, "gitea": &gitea} {
		if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment) VALUES ($1, $1, 'generic', 'prod') RETURNING id`, slug).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]uuid.UUID{}
	addNode := func(host string, cluster *uuid.UUID, protocolVersion int, heartbeat time.Duration, units ...string) {
		t.Helper()
		ids[host] = seedNode(t, pool, host, cluster, protocolVersion, heartbeat, "", units...)
	}
	addNode("nc1", &nextcloud, 9, time.Second, "mariadb")
	addNode("nc2", &nextcloud, 9, time.Second, "mariadb")
	addNode("nc3", &nextcloud, 9, time.Second, "mariadb")
	addNode("gt1", &gitea, 9, time.Second, "patroni", "etcd")
	addNode("gt2", &gitea, 9, time.Second, "postgresql@17-main")
	addNode("pg01", nil, 9, time.Second, "postgresql", "postgresql@17-main")
	addNode("oud01", nil, 8, time.Second, "mysql")
	addNode("weg01", nil, 9, 10*time.Minute, "mariadb")
	addNode("web01", nil, 9, time.Second, "nginx")

	bus := &fakeBus{res: map[uuid.UUID]protocol.DBResponse{}, err: map[uuid.UUID]error{}}
	bus.set(ids["nc1"], protocol.DBResponse{Servers: []protocol.DBServer{galeraNode("10.0.20.186", "Synced", 4)}}, nil)
	bus.set(ids["nc2"], protocol.DBResponse{Servers: []protocol.DBServer{galeraNode("10.0.20.187", "Synced", 4)}}, nil)
	bus.set(ids["nc3"], protocol.DBResponse{Servers: []protocol.DBServer{galeraNode("10.0.20.188", "Synced", 4)}}, nil)
	lag := int64(2)
	bus.set(ids["gt1"], protocol.DBResponse{Servers: []protocol.DBServer{{
		Engine: protocol.DBPostgreSQL, Unit: "patroni", Active: "active", Running: true, Version: "17.6", Role: protocol.DBRolePrimary,
		Patroni:   &protocol.DBPatroni{Scope: "gitea", Role: protocol.DBRolePrimary, State: "running", Timeline: 3},
		Replicas:  []protocol.DBReplica{{Address: "10.0.30.12", State: "streaming", Sync: "async"}},
		Databases: []protocol.DBDatabase{{Name: "gitea", SizeBytes: 50 << 20, Tables: 120}}, DatabasesTotal: 1,
	}}}, nil)
	bus.set(ids["gt2"], protocol.DBResponse{Servers: []protocol.DBServer{{
		Engine: protocol.DBPostgreSQL, Unit: "postgresql@17-main", Active: "active", Running: true, Version: "17.6", Role: protocol.DBRoleReplica,
		ReadOnly: true, Source: &protocol.DBSource{Host: "10.0.30.11", Running: true, LagSeconds: &lag},
		Databases: []protocol.DBDatabase{{Name: "gitea", SizeBytes: 50 << 20, Tables: 120}}, DatabasesTotal: 1,
	}}}, nil)
	bus.set(ids["pg01"], protocol.DBResponse{Servers: []protocol.DBServer{{
		Engine: protocol.DBPostgreSQL, Unit: "postgresql@17-main", Active: "active", Running: true, Version: "17.6", Role: protocol.DBRoleStandalone,
		Databases: []protocol.DBDatabase{{Name: "app", SizeBytes: 8 << 20, Tables: 12}, {Name: "logs", SizeBytes: 1 << 30, Tables: -1}}, DatabasesTotal: 2,
	}}}, nil)

	s := NewService(pool, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	// De te oude agent en die zonder heartbeat krijgen geen vraag, en een
	// node zonder databaseserver ook niet.
	bus.mu.Lock()
	for _, host := range []string{"oud01", "weg01", "web01"} {
		if slices.Contains(bus.asked, ids[host]) {
			t.Errorf("%s gevraagd", host)
		}
	}
	if len(bus.asked) != 6 {
		t.Errorf("%d nodes gevraagd", len(bus.asked))
	}
	bus.mu.Unlock()

	groups, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, g := range groups {
		name := g.ClusterName
		if g.ClusterID == nil {
			name = g.Servers[0].Hostname
		}
		keys = append(keys, name+"/"+g.Engine+"/"+g.Kind+"/"+g.Health)
	}
	want := []string{
		"gitea/postgresql/patroni/ok", "nextcloud/mariadb/galera/ok", "oud01/mariadb/single/unknown",
		"pg01/postgresql/single/ok", "weg01/mariadb/single/down",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("groepen:\n%q\nwil\n%q", keys, want)
	}

	nc := group(t, groups, "nextcloud", protocol.DBMariaDB)
	if nc.Summary != "3 van 3 nodes Synced." || nc.Version != "11.8.3" || len(nc.Servers) != 3 || nc.ClusterEnv != "prod" ||
		nc.DatabasesFrom != "nc1" || nc.DatabasesTotal != 1 || len(nc.Databases) != 1 || nc.Databases[0].Tables != 158 {
		t.Errorf("nextcloud: %+v", nc)
	}
	for _, srv := range nc.Servers {
		if srv.State != "Synced" || !srv.OK || srv.Galera == nil || srv.Connections != 10 || srv.OkAt == nil {
			t.Errorf("%s: %+v", srv.Hostname, srv)
		}
	}
	gt := group(t, groups, "gitea", protocol.DBPostgreSQL)
	if gt.Summary != "Primary gt1 met 1 replica." || gt.DatabasesFrom != "gt1" || gt.Servers[0].State != "primary, 1 replica" ||
		gt.Servers[1].State != "replica van 10.0.30.11, 2 s achter" || !gt.Servers[1].OK || !gt.Servers[1].ReadOnly {
		t.Errorf("gitea: %+v", gt)
	}
	pg := group(t, groups, "pg01", protocol.DBPostgreSQL)
	if pg.Summary != "Draait." || pg.DatabasesTotal != 2 || len(pg.Databases) != 2 || pg.Databases[1].Tables != -1 {
		t.Errorf("pg01: %+v", pg)
	}
	old := group(t, groups, "oud01", protocol.DBMariaDB)
	if old.Servers[0].State != "agent te oud" || !strings.Contains(old.Summary, "protocol 9") || !strings.Contains(old.Servers[0].Error, "protocol 8") {
		t.Errorf("oud01: %+v", old)
	}
	gone := group(t, groups, "weg01", protocol.DBMariaDB)
	if gone.Servers[0].State != "agent offline" || gone.Summary != "Geen enkele server antwoordt." {
		t.Errorf("weg01: %+v", gone)
	}

	// nc2 is donor voor een nieuwe node, nc3 antwoordt niet meer en pg01
	// heeft geen PostgreSQL meer.
	donor := galeraNode("10.0.20.187", "Donor/Desynced", 2)
	bus.set(ids["nc2"], protocol.DBResponse{Servers: []protocol.DBServer{donor}}, nil)
	bus.set(ids["nc3"], protocol.DBResponse{}, agentbus.ErrNoAnswer)
	bus.set(ids["pg01"], protocol.DBResponse{}, nil)
	if err := s.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if groups, err = s.Overview(ctx); err != nil {
		t.Fatal(err)
	}
	nc = group(t, groups, "nextcloud", protocol.DBMariaDB)
	if nc.Health != HealthWarning || nc.Summary != "1 van 3 nodes Synced; nc2: Donor/Desynced, nc3: geen antwoord." || nc.DatabasesFrom != "nc1" {
		t.Errorf("nextcloud na de donor: %+v", nc)
	}
	// De laatst bekende status van nc3 blijft, maar telt niet als draaiend.
	if s3 := nc.Servers[2]; s3.Running || s3.Galera == nil || s3.ErrorKind != ErrNoAnswer || s3.Version != "11.8.3" {
		t.Errorf("nc3: %+v", s3)
	}
	for _, g := range groups {
		if g.ClusterID == nil && g.Servers[0].Hostname == "pg01" {
			t.Errorf("pg01 staat er nog: %+v", g)
		}
	}
	var dbs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM db_databases WHERE node_id = $1`, ids["pg01"]).Scan(&dbs); err != nil || dbs != 0 {
		t.Errorf("databases van pg01: %d %v", dbs, err)
	}

	// Geen primary meer in gitea: down.
	bus.set(ids["gt1"], protocol.DBResponse{Servers: []protocol.DBServer{{
		Engine: protocol.DBPostgreSQL, Unit: "patroni", Active: "failed", Running: false, Version: "17.6", Error: "Patroni antwoordt niet",
	}}}, nil)
	if err := s.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if groups, err = s.Overview(ctx); err != nil {
		t.Fatal(err)
	}
	gt = group(t, groups, "gitea", protocol.DBPostgreSQL)
	if gt.Health != HealthDown || gt.Summary != "Er is geen primary: niemand neemt schrijfacties aan." || gt.Servers[0].State != "gestopt na een fout" {
		t.Errorf("gitea zonder primary: %+v", gt)
	}
}

func names(users []User) []string {
	var out []string
	for _, u := range users {
		out = append(out, u.Kind+":"+u.Name+":"+strings.Join(u.Nodes, ",")+":"+strings.Join(u.DBUsers, ",")+":"+u.Dependency)
	}
	return out
}

// TestUsers: de clients op een gedeeld Galera-cluster worden clusters,
// losse nodes, onbekende adressen en de databasenode zelf. Een database die
// twee clusters gebruiken, is gedeeld; een client die wegvalt, blijft
// dertig dagen staan met nul verbindingen.
func TestUsers(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	clusters := map[string]*uuid.UUID{}
	for _, slug := range []string{"db", "nextcloud", "wordpress"} {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment) VALUES ($1, $1, 'generic', 'prod') RETURNING id`, slug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		clusters[slug] = &id
	}
	db1 := seedNode(t, pool, "db1", clusters["db"], 9, time.Second, "10.0.20.181", "mariadb")
	seedNode(t, pool, "nc1", clusters["nextcloud"], 9, time.Second, "10.0.20.186")
	seedNode(t, pool, "nc2", clusters["nextcloud"], 9, time.Second, "10.0.20.187")
	seedNode(t, pool, "wp1.lan", clusters["wordpress"], 9, time.Second, "10.0.20.191")
	seedNode(t, pool, "backup01", nil, 9, time.Second, "10.0.20.199")

	// Nextcloud hangt volgens een voorstel af van de MariaDB van db.
	var php, maria uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO services (cluster_id, name, kind, unit, source, state) VALUES ($1, 'php8.2-fpm', 'app', 'php8.2-fpm', 'discovered', 'confirmed') RETURNING id`,
		clusters["nextcloud"]).Scan(&php); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO services (cluster_id, name, kind, unit, source, state) VALUES ($1, 'mariadb', 'database', 'mariadb', 'manual', 'confirmed') RETURNING id`,
		clusters["db"]).Scan(&maria); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO service_dependencies (from_service_id, to_service_id, strength, source, state) VALUES ($1, $2, 'hard', 'discovered', 'suggested')`,
		php, maria); err != nil {
		t.Fatal(err)
	}

	srv := galeraNode("10.0.20.181", "Synced", 4)
	srv.Databases = []protocol.DBDatabase{{Name: "nextcloud", SizeBytes: 1 << 30, Tables: 158}, {Name: "oud"}, {Name: "wordpress", SizeBytes: 1 << 20, Tables: 12}}
	srv.Clients = []protocol.DBClient{
		{User: "root", Count: 1},
		{User: "root", Database: "nextcloud", Count: 1},
		{User: "nextcloud", Address: "10.0.20.186", Database: "nextcloud", Count: 3},
		{User: "nextcloud", Address: "10.0.20.187", Database: "nextcloud", Count: 2},
		{User: "wp", Address: "10.0.20.191", Database: "nextcloud", Count: 1},
		{User: "wp", Address: "wp1", Database: "wordpress", Count: 4},
		{User: "backup", Address: "10.0.20.199", Database: "wordpress", Count: 1},
		{User: "wp", Address: "10.0.20.250", Database: "wordpress", Count: 1},
	}
	bus := &fakeBus{res: map[uuid.UUID]protocol.DBResponse{}, err: map[uuid.UUID]error{}}
	bus.set(db1, protocol.DBResponse{Servers: []protocol.DBServer{srv}}, nil)
	s := NewService(pool, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	groups, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := group(t, groups, "db", protocol.DBMariaDB)
	dbs := map[string]Database{}
	for _, d := range g.Databases {
		dbs[d.Name] = d
	}
	nc := dbs["nextcloud"]
	want := []string{
		"cluster:nextcloud:nc1,nc2:nextcloud:suggested", "cluster:wordpress:wp1.lan:wp:", "local::db1:root:",
	}
	if got := names(nc.Users); !slices.Equal(got, want) || !nc.Shared || nc.LastUsed == nil || nc.Users[0].Connections != 5 {
		t.Fatalf("nextcloud: %q gedeeld %v %+v", got, nc.Shared, nc.Users)
	}
	// wp1 met naamresolutie, backup01 zonder cluster, en een adres dat
	// ClusterForge niet kent.
	want = []string{"cluster:wordpress:wp1.lan:wp:", "node:backup01:backup01:backup:", "unknown:10.0.20.250:10.0.20.250:wp:"}
	if got := names(dbs["wordpress"].Users); !slices.Equal(got, want) || !dbs["wordpress"].Shared {
		t.Fatalf("wordpress: %q", got)
	}
	if oud := dbs["oud"]; len(oud.Users) != 0 || oud.Shared || oud.LastUsed != nil {
		t.Fatalf("oud: %+v", oud)
	}

	// nc2 en wordpress zijn weg: ze blijven staan, zonder verbindingen.
	srv.Clients = []protocol.DBClient{{User: "nextcloud", Address: "10.0.20.186", Database: "nextcloud", Count: 2}}
	bus.set(db1, protocol.DBResponse{Servers: []protocol.DBServer{srv}}, nil)
	if err := s.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if groups, err = s.Overview(ctx); err != nil {
		t.Fatal(err)
	}
	nc = group(t, groups, "db", protocol.DBMariaDB).Databases[0]
	if len(nc.Users) != 3 || nc.Users[0].Connections != 2 || nc.Users[1].Connections != 0 {
		t.Fatalf("na het wegvallen: %+v", nc.Users)
	}

	// Na dertig dagen zijn ze weg.
	if _, err := pool.Exec(ctx, `UPDATE db_clients SET last_seen = now() - interval '31 days' WHERE address <> '10.0.20.186'`); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM db_clients`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("na dertig dagen: %d %v", left, err)
	}
	if groups, err = s.Overview(ctx); err != nil {
		t.Fatal(err)
	}
	if nc = group(t, groups, "db", protocol.DBMariaDB).Databases[0]; nc.Shared || len(nc.Users) != 1 {
		t.Fatalf("nextcloud na dertig dagen: %+v", nc)
	}
}
