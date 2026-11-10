package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
)

// TestDeployMediaPlatformExisting: het platform op drie bestaande nodes
// krijgt na de uitrol vanzelf PostgreSQL met Patroni op die nodes, met het
// eigen VIP uit de template (db_vip), en daarna de gebruiker platform met
// zijn twee databases.
func TestDeployMediaPlatformExisting(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	g := &patroniHosts{hosts: map[string]*agenttest.Host{}, slug: "media"}
	group := &agenttest.Group{Takeover: 200 * time.Millisecond}
	ids := map[string]string{}
	names := []string{"mp1", "mp2", "mp3"}
	for i, name := range names {
		ip := fmt.Sprintf("10.0.20.%d", 71+i)
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "primary_ip": ip}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
			writeFile(t, filepath.Join(root, "proc/meminfo"), "MemTotal: 8388608 kB\nMemAvailable: 4194304 kB\n", 0o644)
			h := agenttest.NewHost(root, ip)
			g.mu.Lock()
			g.hosts[name] = h
			g.mu.Unlock()
			h.SetAfter(g.after(name, h))
			ag.Exec, ag.Root, ag.Collector.Run, ag.Collector.Addresses = h.Exec, root, h.Exec, h.Addresses
			ag.ExecStdin, ag.Stream, ag.DumpDir = h.ExecStdin, h.Stream, filepath.Join(root, "var/backups/clusterforge-db")
			ag.Collector.Interfaces, ag.Collector.PrimaryAddress = h.Interfaces, func() string { return ip }
			ag.PatroniURL, ag.Dial = h.ServePatroni(t), h.Dial
			ag.ProbeInterval = 50 * time.Millisecond
			group.Join(h)
		})
	}
	network := func(addr string) *agenttest.Host {
		for i, name := range names {
			if addr == fmt.Sprintf("10.0.20.%d:5432", 71+i) {
				return g.hosts[name]
			}
		}
		return nil
	}
	for _, h := range g.hosts {
		h.Locked(func() { h.Network = network })
	}
	eventually(t, "facts van de drie nodes", func() bool {
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM node_facts f JOIN nodes n ON n.id = f.node_id WHERE n.hostname LIKE 'mp%' AND f.facts->'os'->>'id' = 'debian'").Scan(&n)
		return n == 3
	})

	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", map[string]any{
		"template": "media-platform",
		"cluster":  map[string]any{"name": "Media", "slug": "media", "environment": "lab"},
		"params":   map[string]any{"vip": "10.0.20.70", "db_vip": "10.0.20.69", "platform_ref": "main"},
		"existing_nodes": []map[string]any{
			{"node_id": ids["mp1"], "role": "node"}, {"node_id": ids["mp2"], "role": "node"}, {"node_id": ids["mp3"], "role": "node"},
		},
	}, &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	if j := waitJobLong(t, c, res.Job.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	conf, err := os.ReadFile(filepath.Join(g.hosts["mp1"].Root, "etc/keepalived/keepalived.conf"))
	if err != nil || !strings.Contains(string(conf), "include /etc/keepalived/conf.d/*.conf\n") {
		t.Fatalf("keepalived.conf: %v\n%s", err, conf)
	}

	base := "/api/v1/clusters/" + res.ClusterID + "/components"
	var comps list[componentView]
	eventually(t, "database erbij", func() bool {
		c.do("GET", base, nil, &comps)
		return len(comps.Items) == 1 && comps.Items[0].JobID != nil
	})
	if j := waitJobLong(t, c, *comps.Items[0].JobID); j.Status != "succeeded" {
		t.Fatalf("database: %s %s", j.Status, j.Error)
	}
	c.do("GET", base, nil, &comps)
	cv := comps.Items[0]
	var hosts []string
	for _, nd := range cv.Nodes {
		hosts = append(hosts, nd.Hostname)
	}
	if cv.Engine != "postgresql" || cv.State != "ready" || strings.Join(hosts, " ") != "mp1 mp2 mp3" {
		t.Fatalf("database: %+v", cv)
	}
	eventually(t, "eigen VIP van PostgreSQL in conf.d", func() bool {
		ents, _ := os.ReadDir(filepath.Join(g.hosts["mp1"].Root, "etc/keepalived/conf.d"))
		for _, en := range ents {
			b, _ := os.ReadFile(filepath.Join(g.hosts["mp1"].Root, "etc/keepalived/conf.d", en.Name()))
			if strings.Contains(string(b), "10.0.20.69") {
				return true
			}
		}
		return false
	})
	// Het platform in Docker mag erbij, ook op de node met het VIP.
	if b, err := os.ReadFile(filepath.Join(g.hosts["mp1"].Root, "etc/patroni/clusterforge.yml")); err != nil ||
		!strings.Contains(string(b), "- host all all 172.16.0.0/12 scram-sha-256\n") || !strings.Contains(string(b), "- host all all 10.0.20.69/32 scram-sha-256\n") {
		t.Fatalf("pg_hba: %v\n%s", err, b)
	}
	// De gebruiker platform met zijn databases, op de primary.
	eventually(t, "gebruiker en databases", func() bool {
		var ok bool
		g.hosts["mp1"].Locked(func() {
			p := g.hosts["mp1"].Postgres
			if p == nil {
				return
			}
			dbs := map[string]bool{}
			for _, d := range p.Databases {
				dbs[d.Name] = true
			}
			user := false
			for _, r := range p.Roles {
				user = user || r.Name == "platform"
			}
			ok = dbs["platform"] && dbs["seaweedfs"] && user
		})
		return ok
	})
}
