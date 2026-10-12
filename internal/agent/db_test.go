package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func dbAgent(h *agenttest.Host) *Agent {
	return &Agent{Exec: h.Exec, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// queries geeft de SQL van elke aanroep van mariadb en psql.
func queries(h *agenttest.Host) []string {
	var out []string
	for _, c := range h.Calls() {
		switch {
		case strings.HasPrefix(c, "mariadb "):
			_, q, _ := strings.Cut(c, " -e ")
			out = append(out, q)
		case strings.HasPrefix(c, "runuser "):
			_, q, _ := strings.Cut(c, " -c ")
			out = append(out, q)
		}
	}
	return out
}

func TestDBStatusGalera(t *testing.T) {
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	h.MariaDB = &agenttest.MariaDB{
		Version: "11.8.3-MariaDB-0+deb13u1", Connections: 12, MaxConnections: 151, Uptime: 3600,
		Galera: &agenttest.Galera{
			ClusterName: "nextcloud", UUID: "5c3a1e0e-0000-11f0-9a3b-7e1c2d3f4a5b", Status: "Primary", Size: 3,
			State: 4, Comment: "Synced", Ready: true, Members: []string{"10.0.20.186:3306", "10.0.20.187:3306", "10.0.20.188:3306"},
		},
		Databases: []agenttest.Database{{Name: "nextcloud", Size: 2 << 30, Tables: 158}, {Name: "rare\tnaam", Size: 16384, Tables: 1}},
		Clients: []agenttest.Client{
			{User: "nextcloud", Host: "10.0.20.187", Database: "nextcloud", Count: 2},
			{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 3},
			{User: "root", Host: "localhost", Count: 1},
		},
	}
	res := dbAgent(h).DBStatus(context.Background())
	if res.Error != "" || len(res.Servers) != 1 {
		t.Fatalf("antwoord: %+v", res)
	}
	s := res.Servers[0]
	if s.Engine != protocol.DBMariaDB || s.Unit != "mariadb" || s.Active != "active" || !s.Running || s.Error != "" ||
		s.Version != "11.8.3" || s.Role != protocol.DBRoleMember || s.Connections != 12 || s.MaxConnections != 151 || s.UptimeSeconds != 3600 {
		t.Errorf("server: %+v", s)
	}
	g := s.Galera
	if g == nil || g.ClusterName != "nextcloud" || g.ClusterStatus != "Primary" || g.ClusterSize != 3 || g.State != "Synced" ||
		g.StateCode != 4 || !g.Ready || !slices.Equal(g.Members, []string{"10.0.20.186", "10.0.20.187", "10.0.20.188"}) {
		t.Errorf("galera: %+v", g)
	}
	want := []protocol.DBDatabase{{Name: "nextcloud", SizeBytes: 2 << 30, Tables: 158}, {Name: "rare\tnaam", SizeBytes: 16384, Tables: 1}}
	if !slices.Equal(s.Databases, want) || s.DatabasesTotal != 2 {
		t.Errorf("databases: %+v", s.Databases)
	}
	// Per gebruiker, adres en database, zonder poort; de socket is leeg.
	wantClients := []protocol.DBClient{
		{User: "root", Count: 1},
		{User: "nextcloud", Address: "10.0.20.186", Database: "nextcloud", Count: 3},
		{User: "nextcloud", Address: "10.0.20.187", Database: "nextcloud", Count: 2},
	}
	if !slices.Equal(s.Clients, wantClients) {
		t.Errorf("clients: %+v", s.Clients)
	}
	// Galera-nodes hebben geen replicatie; de agent vraagt er dan ook niet
	// naar.
	for _, q := range queries(h) {
		if !slices.Contains(dbsql.All, q) {
			t.Errorf("onbekende query: %s", q)
		}
		if q == dbsql.MariaSource {
			t.Error("replicatie gevraagd op een Galera-node")
		}
	}
}

func TestDBStatusMariaDBReplication(t *testing.T) {
	lag := int64(4)
	h := agenttest.NewHost(t.TempDir(), "10.0.40.12")
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	h.MariaDB = &agenttest.MariaDB{
		Version: "10.11.11-MariaDB-0+deb12u1", ReadOnly: true,
		Source: &agenttest.MariaSource{Host: "10.0.40.11", IO: true, SQL: true, Lag: &lag},
	}
	s := dbAgent(h).DBStatus(context.Background()).Servers[0]
	if s.Role != protocol.DBRoleReplica || !s.ReadOnly || s.Version != "10.11.11" || s.Source == nil ||
		s.Source.Host != "10.0.40.11" || !s.Source.Running || s.Source.LagSeconds == nil || *s.Source.LagSeconds != 4 {
		t.Errorf("replica: %+v %+v", s, s.Source)
	}

	h.Locked(func() {
		h.MariaDB.Source, h.MariaDB.ReadOnly, h.MariaDB.Replicas = nil, false, []string{"10.0.40.12:51234"}
	})
	s = dbAgent(h).DBStatus(context.Background()).Servers[0]
	if s.Role != protocol.DBRolePrimary || len(s.Replicas) != 1 || s.Replicas[0].Address != "10.0.40.12" {
		t.Errorf("primary: %+v", s)
	}

	h.Locked(func() { h.MariaDB.Replicas = nil })
	if s = dbAgent(h).DBStatus(context.Background()).Servers[0]; s.Role != protocol.DBRoleStandalone {
		t.Errorf("los: %+v", s)
	}
}

func TestDBStatusPostgresPatroni(t *testing.T) {
	patroni := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/patroni" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"state": "running", "role": "master", "timeline": 3, "patroni": {"version": "4.0.4", "scope": "nextcloud", "name": "nc1"}}`)
	}))
	defer patroni.Close()

	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	h.SetUnit("postgresql", agenttest.Unit{Enabled: true, Active: true})
	h.SetUnit("patroni", agenttest.Unit{Enabled: true, Active: true})
	h.Postgres = &agenttest.Postgres{
		Version: "17.6 (Debian 17.6-1)", Connections: 8, MaxConnections: 100, Uptime: 600,
		Replicas: []agenttest.PGReplica{
			{Address: "10.0.20.187", State: "streaming", Sync: "sync"},
			{Address: "10.0.20.188", State: "streaming", Sync: "async", Lag: 4096},
		},
		Databases: []agenttest.Database{{Name: "nextcloud", Size: 900 << 20, Tables: 120}, {Name: "naam=met-is", Size: 8000}},
		Clients: []agenttest.Client{
			{User: "nextcloud", Host: "10.0.20.190", Database: "nextcloud", Count: 4},
			{User: "nextcloud", Host: "127.0.0.1", Database: "nextcloud", Count: 1},
			{User: "postgres", Database: "postgres", Count: 1},
		},
	}
	a := dbAgent(h)
	a.PatroniURL = patroni.URL
	res := a.DBStatus(context.Background())
	if len(res.Servers) != 1 {
		t.Fatalf("servers: %+v", res)
	}
	s := res.Servers[0]
	if s.Engine != protocol.DBPostgreSQL || s.Unit != "patroni" || !s.Running || s.Error != "" || s.Version != "17.6" ||
		s.Role != protocol.DBRolePrimary || s.ReadOnly || s.Connections != 8 || s.MaxConnections != 100 {
		t.Errorf("server: %+v", s)
	}
	if p := s.Patroni; p == nil || p.Role != "primary" || p.State != "running" || p.Scope != "nextcloud" || p.Timeline != 3 {
		t.Errorf("patroni: %+v", s.Patroni)
	}
	if len(s.Replicas) != 2 || s.Replicas[0] != (protocol.DBReplica{Address: "10.0.20.187", State: "streaming", Sync: "sync"}) ||
		s.Replicas[1].LagBytes != 4096 {
		t.Errorf("replica's: %+v", s.Replicas)
	}
	// De lege database postgres valt weg. Een naam met = telt de agent
	// niet, want psql zou hem als verbindingsgegevens lezen.
	want := []protocol.DBDatabase{{Name: "naam=met-is", SizeBytes: 8000, Tables: -1}, {Name: "nextcloud", SizeBytes: 900 << 20, Tables: 120}}
	if !slices.Equal(s.Databases, want) {
		t.Errorf("databases: %+v", s.Databases)
	}
	wantClients := []protocol.DBClient{
		{User: "nextcloud", Database: "nextcloud", Count: 1},
		{User: "nextcloud", Address: "10.0.20.190", Database: "nextcloud", Count: 4},
		{User: "postgres", Database: "postgres", Count: 1},
	}
	if !slices.Equal(s.Clients, wantClients) {
		t.Errorf("clients: %+v", s.Clients)
	}
	for _, c := range h.Calls() {
		if strings.Contains(c, "naam=met-is") {
			t.Errorf("psql met een naam met =: %s", c)
		}
	}
	for _, q := range queries(h) {
		if !slices.Contains(dbsql.All, q) {
			t.Errorf("onbekende query: %s", q)
		}
	}

	// Een replica.
	h.Locked(func() {
		h.Postgres.Recovery, h.Postgres.Replicas = true, nil
		h.Postgres.Source = &agenttest.PGSource{Host: "10.0.20.186", Status: "streaming", Lag: 2}
	})
	s = a.DBStatus(context.Background()).Servers[0]
	if s.Role != protocol.DBRoleReplica || !s.ReadOnly || s.Source == nil || s.Source.Host != "10.0.20.186" || !s.Source.Running ||
		s.Source.LagSeconds == nil || *s.Source.LagSeconds != 2 || s.Error != "" {
		t.Errorf("replica: %+v %+v", s, s.Source)
	}

	// Patroni antwoordt niet: PostgreSQL wordt toch gelezen.
	patroni.Close()
	s = a.DBStatus(context.Background()).Servers[0]
	if s.Patroni == nil || !strings.Contains(s.Patroni.Error, "antwoordt niet") || !s.Running {
		t.Errorf("zonder Patroni: %+v %+v", s, s.Patroni)
	}
}

func TestDBStatusDownAndAbsent(t *testing.T) {
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	// Alleen de clients, zoals op een app-node die een database elders
	// gebruikt: geen server, dus geen antwoord.
	h.MariaDB = &agenttest.MariaDB{Version: "11.8.3-MariaDB"}
	if res := dbAgent(h).DBStatus(context.Background()); res.Error != "" || len(res.Servers) != 0 {
		t.Errorf("zonder server: %+v", res)
	}

	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Failed: true})
	h.SetUnit("postgresql", agenttest.Unit{Enabled: true, Active: true})
	h.SetUnit("postgresql@17-main", agenttest.Unit{Enabled: true, Failed: true})
	h.MariaDB.Down = true
	res := dbAgent(h).DBStatus(context.Background())
	if len(res.Servers) != 2 {
		t.Fatalf("servers: %+v", res)
	}
	m, p := res.Servers[0], res.Servers[1]
	if m.Engine != protocol.DBMariaDB || m.Running || m.Active != "failed" || !strings.Contains(m.Error, "Can't connect") {
		t.Errorf("mariadb: %+v", m)
	}
	if p.Engine != protocol.DBPostgreSQL || p.Unit != "postgresql@17-main" || p.Active != "failed" || p.Running ||
		!strings.Contains(p.Error, "failed") {
		t.Errorf("postgresql: %+v", p)
	}
}

func TestHandleDB(t *testing.T) {
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	a := dbAgent(h)
	req := func(r protocol.DBRequest) protocol.DBResponse {
		body, _ := json.Marshal(r)
		data, _ := json.Marshal(protocol.Envelope{Type: protocol.TypeDBRequest, Body: body})
		return a.handleDB(data)
	}
	if res := req(protocol.DBRequest{ID: "1", Op: protocol.DBStatus}); res.Error != "" || res.Servers == nil {
		t.Errorf("status: %+v", res)
	}
	if res := req(protocol.DBRequest{ID: "2", Op: "query"}); !strings.Contains(res.Error, "onbekend") {
		t.Errorf("onbekend verzoek: %+v", res)
	}
	if res := req(protocol.DBRequest{ID: "3", Op: protocol.DBStatus, Deadline: time.Now().Add(-time.Second)}); !strings.Contains(res.Error, "verlopen") {
		t.Errorf("verlopen: %+v", res)
	}
}

func TestMariaClientHost(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.20.186:51234": "10.0.20.186",
		"localhost":         "",
		"127.0.0.1:40000":   "",
		"[fd00::1]:3306":    "fd00::1",
		"::1:3306":          "",
		"nc1.lan:5555":      "nc1.lan",
	} {
		if got := mariaClientHost(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestShortVersion(t *testing.T) {
	for in, want := range map[string]string{
		"11.8.3-MariaDB-0+deb13u1":              "11.8.3",
		"10.11.11-MariaDB-0+deb12u1-log":        "10.11.11",
		"17.6 (Debian 17.6-1.pgdg13+1)":         "17.6",
		"16.14 (Ubuntu 16.14-0ubuntu0.24.04.1)": "16.14",
		"15.13":                                 "15.13",
	} {
		if got := shortVersion(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestDBStatusRealPostgres draait de query's tegen een echte PostgreSQL:
// psql met de gegevens uit CF_TEST_DATABASE_URL in plaats van runuser.
// Een rij met een geheim mag nergens in het antwoord staan.
func TestDBStatusRealPostgres(t *testing.T) {
	raw := os.Getenv("CF_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("CF_TEST_DATABASE_URL niet gezet")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql ontbreekt")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	pass, _ := u.User.Password()
	env := append(os.Environ(), "PGHOST="+u.Hostname(), "PGPORT="+u.Port(), "PGUSER="+u.User.Username(), "PGPASSWORD="+pass,
		"PGDATABASE="+strings.TrimPrefix(u.Path, "/"), "PGSSLMODE=disable")
	psql := func(args ...string) ([]byte, error) {
		cmd := exec.Command("psql", args...)
		cmd.Env = env
		return cmd.Output()
	}
	const db, secret = "cf_agent_dbtest", "Sup3rGeheim-rij"
	_, _ = psql("-c", "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	if out, err := psql("-c", "CREATE DATABASE "+db); err != nil {
		t.Fatalf("CREATE DATABASE: %v %s", err, out)
	}
	// WITH (FORCE): de verbinding van de app kan nog open staan.
	t.Cleanup(func() { _, _ = psql("-c", "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)") })
	if out, err := psql("-d", db, "-c", "CREATE TABLE gebruikers (naam text, wachtwoord text); INSERT INTO gebruikers VALUES ('jonas', '"+secret+"')"); err != nil {
		t.Fatalf("tabel: %v %s", err, out)
	}

	// Een app die verbonden is met de database, zolang de test loopt.
	app := exec.Command("psql", "-d", db, "-c", "SELECT pg_sleep(20)")
	app.Env = env
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Process.Kill(); _ = app.Wait() })

	a := &Agent{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return []byte("Id=postgresql.service\nLoadState=loaded\nActiveState=active\n\nId=postgresql@16-main.service\nLoadState=loaded\nActiveState=active\n\n"), nil
		case "runuser":
			i := slices.Index(args, "psql")
			out, err := psql(args[i+1:]...)
			if ee, ok := err.(*exec.ExitError); ok {
				err = fmt.Errorf("%w: %s", err, ee.Stderr)
			}
			return out, err
		}
		return nil, fmt.Errorf("onbekend commando %s", name)
	}
	res := realStatus(t, a, u.User.Username(), db)
	s := res.Servers[0]
	if !s.Running || s.Error != "" || s.Version == "" || s.Role != protocol.DBRoleStandalone || s.MaxConnections == 0 || s.UptimeSeconds <= 0 {
		t.Errorf("server: %+v", s)
	}
	i := slices.IndexFunc(s.Databases, func(d protocol.DBDatabase) bool { return d.Name == db })
	if i < 0 || s.Databases[i].Tables != 1 || s.Databases[i].SizeBytes <= 0 {
		t.Errorf("databases: %+v", s.Databases)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), secret) || strings.Contains(string(b), "jonas") {
		t.Errorf("rijen in het antwoord: %s", b)
	}
}

// TestDBStatusRealMariaDB draait de query's tegen een echte MariaDB in de
// container CF_TEST_MARIADB, zoals de CI die start.
func TestDBStatusRealMariaDB(t *testing.T) {
	container := os.Getenv("CF_TEST_MARIADB")
	if container == "" {
		t.Skip("CF_TEST_MARIADB niet gezet")
	}
	maria := func(args ...string) ([]byte, error) {
		out, err := exec.Command("docker", append([]string{"exec", container, "mariadb"}, args...)...).Output()
		if ee, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%w: %s", err, ee.Stderr)
		}
		return out, err
	}
	const db, secret = "cf_agent_dbtest", "Sup3rGeheim-rij"
	if out, err := maria("-e", "DROP DATABASE IF EXISTS "+db+"; CREATE DATABASE "+db+
		"; CREATE TABLE "+db+".gebruikers (id int PRIMARY KEY, naam text, wachtwoord text); INSERT INTO "+db+".gebruikers VALUES (1, 'jonas', '"+secret+"')"); err != nil {
		t.Fatalf("database: %v %s", err, out)
	}
	t.Cleanup(func() { _, _ = maria("-e", "DROP DATABASE IF EXISTS "+db) })

	app := exec.Command("docker", "exec", container, "mariadb", "-e", "SELECT SLEEP(20)", db)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Process.Kill(); _ = app.Wait() })

	a := &Agent{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return []byte("Id=mariadb.service\nLoadState=loaded\nActiveState=active\n\n"), nil
		case "mariadb":
			return maria(args...)
		}
		return nil, fmt.Errorf("onbekend commando %s", name)
	}
	res := realStatus(t, a, "root", db)
	s := res.Servers[0]
	if !s.Running || s.Error != "" || s.Version == "" || s.Role != protocol.DBRoleStandalone || s.Galera != nil || s.MaxConnections == 0 {
		t.Errorf("server: %+v", s)
	}
	i := slices.IndexFunc(s.Databases, func(d protocol.DBDatabase) bool { return d.Name == db })
	if i < 0 || s.Databases[i].Tables != 1 || s.Databases[i].SizeBytes <= 0 {
		t.Errorf("databases: %+v", s.Databases)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), secret) || strings.Contains(string(b), "jonas") {
		t.Errorf("rijen in het antwoord: %s", b)
	}
}

// realStatus vraagt de status tot de verbinding van de app met db erin
// staat, met gebruiker user.
func realStatus(t *testing.T, a *Agent, user, db string) protocol.DBResponse {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res := a.DBStatus(context.Background())
		if len(res.Servers) != 1 {
			t.Fatalf("antwoord: %+v", res)
		}
		if slices.ContainsFunc(res.Servers[0].Clients, func(c protocol.DBClient) bool {
			return c.User == user && c.Database == db && c.Count >= 1
		}) {
			return res
		}
		if time.Now().After(deadline) {
			t.Fatalf("de verbinding van de app staat niet in de clients: %+v", res.Servers[0].Clients)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
