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
		Name      string `json:"name"`
		SizeBytes int64  `json:"size_bytes"`
		Tables    int    `json:"tables"`
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
