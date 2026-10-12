package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type dbGroupView struct {
	Key            string  `json:"key"`
	ClusterID      *string `json:"cluster_id"`
	ClusterName    string  `json:"cluster_name"`
	Engine         string  `json:"engine"`
	Kind           string  `json:"kind"`
	Health         string  `json:"health"`
	Summary        string  `json:"summary"`
	Version        string  `json:"version"`
	DatabasesFrom  string  `json:"databases_from"`
	DatabasesTotal int     `json:"databases_total"`
	Servers        []struct {
		Hostname  string `json:"hostname"`
		Unit      string `json:"unit"`
		Running   bool   `json:"running"`
		Role      string `json:"role"`
		State     string `json:"state"`
		Ok        bool   `json:"ok"`
		ErrorKind string `json:"error_kind"`
		Galera    *struct {
			ClusterName string   `json:"cluster_name"`
			ClusterSize int      `json:"cluster_size"`
			Members     []string `json:"members"`
		} `json:"galera"`
	} `json:"servers"`
	Databases []struct {
		Name       string  `json:"name"`
		SizeBytes  int64   `json:"size_bytes"`
		Tables     int     `json:"tables"`
		Shared     bool    `json:"shared"`
		LastUsedAt *string `json:"last_used_at"`
		Users      []struct {
			Kind        string   `json:"kind"`
			ClusterID   *string  `json:"cluster_id"`
			Name        string   `json:"name"`
			Nodes       []string `json:"nodes"`
			DBUsers     []string `json:"db_users"`
			Connections int      `json:"connections"`
			Dependency  string   `json:"dependency"`
		} `json:"users"`
	} `json:"databases"`
}

// TestDatabases is de acceptatie van mijlpaal 1 van fase 5: drie
// Galera-nodes van Nextcloud en een losse PostgreSQL staan in het menu
// Databases, gelezen door echte agents met agentprotocol 9. Een kijker mag
// het zien, en een te oude agent krijgt een duidelijke melding in plaats
// van een vraag die hij niet begrijpt.
func TestDatabases(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "prod"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	members := []string{"10.0.20.186:3306", "10.0.20.187:3306", "10.0.20.188:3306"}
	ids := map[string]string{}
	for i, name := range []string{"nc1", "nc2", "nc3", "pg01"} {
		body := map[string]any{"hostname": name}
		if name != "pg01" {
			body["cluster_id"] = nc.ID
		}
		var n node
		if s := c.do("POST", "/api/v1/nodes", body, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			h := agenttest.NewHost(root, "10.0.20.18"+string(rune('6'+i)))
			ag.Exec, ag.Root, ag.Collector.Run = h.Exec, root, h.Exec
			if name == "pg01" {
				h.SetUnit("postgresql", agenttest.Unit{Enabled: true, Active: true})
				h.SetUnit("postgresql@17-main", agenttest.Unit{Enabled: true, Active: true})
				h.Postgres = &agenttest.Postgres{
					Version: "17.6 (Debian 17.6-0+deb13u1)", Connections: 4, MaxConnections: 100, Uptime: 7200,
					Databases: []agenttest.Database{{Name: "gitea", Size: 52 << 20, Tables: 117}},
				}
				return
			}
			h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
			h.MariaDB = &agenttest.MariaDB{
				Version: "11.8.3-MariaDB-0+deb13u1", Connections: 12, MaxConnections: 151, Uptime: 3600,
				Galera: &agenttest.Galera{
					ClusterName: "nextcloud", UUID: "5c3a1e0e-0000-11f0-9a3b-7e1c2d3f4a5b", Status: "Primary", Size: 3,
					State: 4, Comment: "Synced", Ready: true, Members: members,
				},
				Databases: []agenttest.Database{{Name: "nextcloud", Size: 2 << 30, Tables: 158}},
			}
		})
	}

	var groups list[dbGroupView]
	var raw json.RawMessage
	eventually(t, "de databases van de vier nodes", func() bool {
		if err := e.dbs.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
		if s := c.do("GET", "/api/v1/databases", nil, &raw); s != http.StatusOK {
			t.Fatalf("databases: %d", s)
		}
		if err := json.Unmarshal(raw, &groups); err != nil {
			t.Fatal(err)
		}
		return len(groups.Items) == 2 && len(groups.Items[0].Servers) == 3
	})

	g := groups.Items[0]
	if g.Key != "mariadb/"+nc.ID || g.ClusterName != "Nextcloud" || g.Kind != "galera" || g.Health != "ok" ||
		g.Summary != "3 van 3 nodes Synced." || g.Version != "11.8.3" || g.DatabasesFrom != "nc1" || g.DatabasesTotal != 1 ||
		len(g.Databases) != 1 || g.Databases[0].Name != "nextcloud" || g.Databases[0].Tables != 158 {
		t.Fatalf("nextcloud: %+v", g)
	}
	for i, s := range g.Servers {
		if s.Hostname != []string{"nc1", "nc2", "nc3"}[i] || s.Unit != "mariadb" || !s.Running || !s.Ok || s.State != "Synced" ||
			s.Galera == nil || s.Galera.ClusterSize != 3 || len(s.Galera.Members) != 3 || s.Galera.Members[0] != "10.0.20.186" {
			t.Errorf("server %d: %+v", i, s)
		}
	}
	pg := groups.Items[1]
	if pg.Key != "postgresql/node/"+ids["pg01"] || pg.ClusterID != nil || pg.Kind != "single" || pg.Health != "ok" || pg.Version != "17.6" ||
		pg.Servers[0].Unit != "postgresql@17-main" || pg.Servers[0].Role != "standalone" ||
		len(pg.Databases) != 1 || pg.Databases[0].Name != "gitea" || pg.Databases[0].SizeBytes != 52<<20 {
		t.Fatalf("pg01: %+v", pg)
	}

	// Een kijker ziet hetzelfde; zonder login niets.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	var viewer list[dbGroupView]
	if s := v.do("GET", "/api/v1/databases", nil, &viewer); s != http.StatusOK || len(viewer.Items) != 2 {
		t.Fatalf("kijker: %d %+v", s, viewer)
	}
	if s := e.client().do("GET", "/api/v1/databases", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("zonder login: %d", s)
	}

	// nc3 krijgt een agent van voor fase 5: ClusterForge vraagt hem niets
	// meer en zegt hoe het op te lossen is.
	if _, err := e.pool.Exec(ctx, "UPDATE agents SET protocol_version = 8 WHERE node_id = $1", ids["nc3"]); err != nil {
		t.Fatal(err)
	}
	if err := e.dbs.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/databases", nil, &groups)
	g = groups.Items[0]
	if s := g.Servers[2]; s.State != "agent te oud" || s.Ok || s.Running || s.ErrorKind != "old_agent" {
		t.Fatalf("nc3 met een oude agent: %+v", s)
	}
	if g.Health != "warning" || g.Summary != "2 van 3 nodes Synced; nc3: agent te oud." {
		t.Fatalf("nextcloud met een oude agent: %+v", g)
	}
}

// TestDatabaseUsers is de acceptatie van mijlpaal 2 van fase 5, met de
// opstelling van nu: een gedeeld Galera-cluster db en Nextcloud op drie
// eigen nodes. Op de pagina Databases staat bij de database nextcloud dat
// het cluster Nextcloud haar gebruikt, en dat die afhankelijkheid nog niet
// in de graaf staat; tot Gitea er vanaf een onbekend adres ook mee
// verbindt, dan is ze gedeeld.
func TestDatabaseUsers(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var db, nc cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "db", "type": "generic", "environment": "prod"}, &db)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "prod"}, &nc)
	for i, name := range []string{"nc1", "nc2", "nc3"} {
		body := map[string]any{"hostname": name, "cluster_id": nc.ID, "primary_ip": "10.0.20.18" + string(rune('6'+i))}
		if s := c.do("POST", "/api/v1/nodes", body, nil); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
	}
	var n node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db1", "cluster_id": db.ID}, &n); s != http.StatusCreated {
		t.Fatalf("db1: %d", s)
	}
	var host *agenttest.Host
	startAgentWith(t, e, c, n.ID, strings.Repeat("d", 32), &addrs{}, func(ag *agent.Agent, root string) {
		host = agenttest.NewHost(root, "10.0.20.181")
		ag.Exec, ag.Root, ag.Collector.Run = host.Exec, root, host.Exec
		host.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
		host.MariaDB = &agenttest.MariaDB{
			Version: "11.8.3-MariaDB-0+deb13u1", Connections: 9, MaxConnections: 151,
			Galera: &agenttest.Galera{
				ClusterName: "db", Status: "Primary", Size: 1, State: 4, Comment: "Synced", Ready: true, Members: []string{"10.0.20.181:3306"},
			},
			Databases: []agenttest.Database{{Name: "gitea", Size: 64 << 20, Tables: 117}, {Name: "nextcloud", Size: 2 << 30, Tables: 158}},
			Clients: []agenttest.Client{
				{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 4},
				{User: "nextcloud", Host: "10.0.20.187", Database: "nextcloud", Count: 3},
				{User: "nextcloud", Host: "10.0.20.188", Database: "nextcloud", Count: 2},
				{User: "root", Host: "localhost", Count: 1},
			},
		}
	})

	var groups list[dbGroupView]
	eventually(t, "de database nextcloud met haar gebruikers", func() bool {
		if err := e.dbs.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
		c.do("GET", "/api/v1/databases", nil, &groups)
		return len(groups.Items) == 1 && len(groups.Items[0].Databases) == 2 && len(groups.Items[0].Databases[1].Users) == 1
	})
	gitea, ncdb := groups.Items[0].Databases[0], groups.Items[0].Databases[1]
	u := ncdb.Users[0]
	if u.Kind != "cluster" || u.ClusterID == nil || *u.ClusterID != nc.ID || u.Name != "Nextcloud" || strings.Join(u.Nodes, ",") != "nc1,nc2,nc3" ||
		strings.Join(u.DBUsers, ",") != "nextcloud" || u.Connections != 9 || u.Dependency != "" || ncdb.Shared || ncdb.LastUsedAt == nil {
		t.Fatalf("nextcloud: %+v", ncdb)
	}
	if len(gitea.Users) != 0 || gitea.LastUsedAt != nil {
		t.Fatalf("gitea: %+v", gitea)
	}

	// Gitea verbindt vanaf een adres dat ClusterForge niet kent, ook met
	// nextcloud: die is nu gedeeld.
	host.Locked(func() {
		host.MariaDB.Clients = append(host.MariaDB.Clients,
			agenttest.Client{User: "gitea", Host: "10.0.30.40", Database: "gitea", Count: 2},
			agenttest.Client{User: "gitea", Host: "10.0.30.40", Database: "nextcloud", Count: 1})
	})
	if err := e.dbs.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/databases", nil, &groups)
	gitea, ncdb = groups.Items[0].Databases[0], groups.Items[0].Databases[1]
	if len(gitea.Users) != 1 || gitea.Users[0].Kind != "unknown" || gitea.Users[0].Name != "10.0.30.40" || gitea.Shared {
		t.Fatalf("gitea: %+v", gitea)
	}
	if !ncdb.Shared || len(ncdb.Users) != 2 || ncdb.Users[1].Kind != "unknown" {
		t.Fatalf("nextcloud gedeeld: %+v", ncdb)
	}
}
