package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
)

const ncVIP = "10.0.20.185"

// ncKeepalived is keepalived.conf zoals Jonas hem op de Nextcloud-nodes
// schreef: het VIP volgt Apache.
func ncKeepalived(name string, prio int) string {
	return fmt.Sprintf(`global_defs {
   router_id %s
}

vrrp_script chk_apache {
    script "/usr/bin/systemctl is-active --quiet apache2"
    interval 2
}

vrrp_instance VI_1 {
    state BACKUP
    interface eth0
    virtual_router_id 85
    priority %d
    advert_int 1
    authentication {
        auth_type PASS
        auth_pass geheim12
    }
    virtual_ipaddress {
        %s/24
    }
    track_script {
        chk_apache
    }
}
`, name, prio, ncVIP)
}

type componentVIPPlan struct {
	componentPlanView
	VIP *struct {
		Address string `json:"address"`
		Owner   string `json:"owner"`
	} `json:"vip"`
}

// TestClusterComponentVIP is de acceptatie van mijlpaal 4 van fase 5: de
// database krijgt het VIP van Nextcloud. Het plan toont per node wat er in
// keepalived.conf verandert; de taak zet track_script cf_db op elke node,
// met de eigenaar van het VIP laatst. Daarna verhuist het VIP als MariaDB
// op zijn node niet meer mag schrijven.
func TestClusterComponentVIP(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	group := &agenttest.Group{Takeover: 200 * time.Millisecond}

	var nc, web cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "lab"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "generic", "environment": "lab"}, &web)
	var vip, webVIP struct {
		ID string `json:"id"`
	}
	c.do("POST", "/api/v1/clusters/"+nc.ID+"/vips", map[string]any{"address": ncVIP}, &vip)
	c.do("POST", "/api/v1/clusters/"+web.ID+"/vips", map[string]any{"address": "10.0.20.190"}, &webVIP)

	g := &galeraHosts{hosts: map[string]*agenttest.Host{}}
	ids := map[string]string{}
	// nc4 heeft het VIP ook in keepalived, maar krijgt geen database.
	for i, name := range []string{"nc1", "nc2", "nc3", "nc4"} {
		ip := fmt.Sprintf("10.0.20.%d", 186+i)
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": nc.ID, "primary_ip": ip}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
			writeFile(t, filepath.Join(root, "proc/meminfo"), "MemTotal: 4194304 kB\nMemAvailable: 2097152 kB\n", 0o644)
			writeFile(t, filepath.Join(root, "etc/keepalived/keepalived.conf"), ncKeepalived(name, 150-10*i), 0o640)
			h := agenttest.NewHost(root, ip)
			if _, err := h.Exec(ctx, "apt-get", "install", "-y", "keepalived"); err != nil {
				t.Fatal(err)
			}
			h.SetUnit("apache2", agenttest.Unit{Enabled: true, Active: true})
			g.hosts[name] = h
			h.SetAfter(g.after(name, h))
			ag.Exec, ag.Root, ag.Collector.Run, ag.Collector.Addresses = h.Exec, root, h.Exec, h.Addresses
			ag.Collector.Interfaces, ag.Collector.PrimaryAddress = h.Interfaces, func() string { return ip }
			if name != "nc4" {
				group.Join(h)
			}
		})
	}
	eventually(t, "facts met keepalived van de vier nodes", func() bool {
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM node_facts f JOIN nodes n ON n.id = f.node_id WHERE n.cluster_id = $1 AND f.facts->'keepalived'->>'active' = 'active'", nc.ID).Scan(&n)
		return n == 4
	})
	owner := func(name string) bool {
		var d statusView
		c.do("GET", "/api/v1/clusters/"+nc.ID, nil, &d)
		return len(d.Vips) == 1 && d.Vips[0].OwnerHostname != nil && *d.Vips[0].OwnerHostname == name
	}
	eventually(t, "het VIP op nc1", func() bool { return owner("nc1") })

	base := "/api/v1/clusters/" + nc.ID + "/components"
	nodes := []string{ids["nc1"], ids["nc2"], ids["nc3"]}
	in := map[string]any{"engine": "mariadb", "nodes": nodes, "vip_id": vip.ID}
	plan := func(body map[string]any) componentVIPPlan {
		t.Helper()
		var p componentVIPPlan
		if s := c.do("POST", base+"/plan", body, &p); s != http.StatusOK {
			t.Fatalf("plan: %d", s)
		}
		return p
	}

	// Zonder VIP waarschuwt het plan en noemt het het VIP van het cluster.
	p := plan(map[string]any{"engine": "mariadb", "nodes": nodes})
	if !slices.Equal(p.codes(), []string{"warning:no_vip"}) || !strings.Contains(p.Findings[0].Message, ncVIP) || p.VIP != nil {
		t.Fatalf("zonder VIP: %+v", p)
	}
	// Het VIP van een ander cluster kan niet.
	var ae apiErr
	if s := c.do("POST", base+"/plan", map[string]any{"engine": "mariadb", "nodes": nodes, "vip_id": webVIP.ID}, &ae); s != http.StatusBadRequest {
		t.Fatalf("VIP van Web: %d %+v", s, ae)
	}
	// nc4 heeft het VIP maar geen database.
	if p = plan(in); !p.Blocked || !slices.Equal(p.codes(), []string{"blocked:vip_outside"}) || !strings.Contains(p.Findings[0].Message, "nc4") {
		t.Fatalf("met nc4: %+v", p)
	}
	if s := c.do("PATCH", "/api/v1/nodes/"+ids["nc4"], map[string]any{"cluster_id": web.ID}, nil); s != http.StatusOK {
		t.Fatalf("nc4 naar Web: %d", s)
	}
	// Een cluster uit een template houdt keepalived.conf zelf bij.
	if _, err := e.pool.Exec(ctx, "UPDATE clusters SET template_name = 'keepalived-nginx' WHERE id = $1", nc.ID); err != nil {
		t.Fatal(err)
	}
	if p = plan(in); !slices.Equal(p.codes(), []string{"blocked:vip_template"}) {
		t.Fatalf("template: %+v", p)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE clusters SET template_name = NULL WHERE id = $1", nc.ID); err != nil {
		t.Fatal(err)
	}

	p = plan(in)
	if p.Blocked || len(p.codes()) != 0 || p.VIP == nil || p.VIP.Address != ncVIP || p.VIP.Owner != "nc1" {
		t.Fatalf("plan: %+v", p)
	}
	for _, n := range p.Nodes {
		if n.VIPInstance != "VI_1" || !strings.Contains(n.VIPDiff, "+include /etc/keepalived/conf.d/*.conf") ||
			!strings.Contains(n.VIPDiff, "         chk_apache\n+        cf_db\n") || strings.Contains(n.VIPDiff, "geheim12") {
			t.Errorf("%s: %+v", n.Hostname, n)
		}
		if steps := n.Steps; !slices.Equal(steps[len(steps)-4:], []string{"map /etc/keepalived/conf.d", "bestand /etc/keepalived/conf.d/clusterforge-db.conf",
			"keepalived.conf: track_script cf_db in VI_1", "keepalived herladen"}) {
			t.Errorf("stappen op %s: %v", n.Hostname, steps)
		}
	}

	var j job
	body := map[string]any{"engine": "mariadb", "nodes": nodes, "vip_id": vip.ID, "hash": p.Hash}
	if s := c.do("POST", base, body, &j); s != http.StatusAccepted {
		t.Fatalf("toevoegen: %d %+v", s, j)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("taak: %s %s\n%+v", j.Status, j.Error, j.Steps)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	// nc1 heeft het VIP en komt laatst.
	want := []string{"nc1: MariaDB", "nc2: MariaDB", "nc3: MariaDB", "nc2: VIP " + ncVIP, "nc3: VIP " + ncVIP, "nc1: VIP " + ncVIP, "Controleren"}
	if !slices.Equal(names, want) {
		t.Fatalf("stappen: %v", names)
	}
	if log := strings.Join(j.Steps[5].Log, "\n"); !strings.Contains(log, "keepalived.conf: track_script cf_db in VI_1 gezet") ||
		!strings.Contains(log, "het VIP "+ncVIP+" staat op nc1") || strings.Contains(log, "geheim12") {
		t.Errorf("nc1:\n%s", log)
	}
	if log := strings.Join(j.Steps[6].Log, "\n"); !strings.Contains(log, "het VIP "+ncVIP+" staat op nc1, een node met de database") {
		t.Errorf("controleren:\n%s", log)
	}
	for _, name := range []string{"nc1", "nc2", "nc3"} {
		h := g.hosts[name]
		conf, _ := os.ReadFile(filepath.Join(h.Root, "etc/keepalived/keepalived.conf"))
		if !strings.Contains(string(conf), "\ninclude /etc/keepalived/conf.d/*.conf\n") || !strings.Contains(string(conf), "        chk_apache\n        cf_db\n") {
			t.Errorf("keepalived.conf op %s:\n%s", name, conf)
		}
		script, _ := os.ReadFile(filepath.Join(h.Root, "etc/keepalived/conf.d/clusterforge-db.conf"))
		if !strings.Contains(string(script), `script "/usr/local/bin/cf-agent check db"`) {
			t.Errorf("cf_db op %s:\n%s", name, script)
		}
		calls := strings.Join(h.Calls(), "\n")
		if !strings.Contains(calls, "keepalived -t -f ") || !strings.Contains(calls, "systemctl reload-or-restart keepalived") {
			t.Errorf("%s:\n%s", name, calls)
		}
	}
	var comps list[struct {
		VIP *struct {
			Address string   `json:"address"`
			Nodes   []string `json:"nodes"`
			Owner   string   `json:"owner"`
		} `json:"vip"`
	}]
	c.do("GET", base, nil, &comps)
	if len(comps.Items) != 1 || comps.Items[0].VIP == nil || comps.Items[0].VIP.Address != ncVIP ||
		!slices.Equal(comps.Items[0].VIP.Nodes, []string{"nc1", "nc2", "nc3"}) || comps.Items[0].VIP.Owner != "nc1" {
		t.Fatalf("onderdeel: %+v", comps)
	}

	// Mag MariaDB op nc1 niet meer schrijven, dan gaat het VIP naar nc2, en
	// daarna terug.
	nc1 := g.hosts["nc1"]
	nc1.Locked(func() { nc1.MariaDB.Galera.State, nc1.MariaDB.Galera.Comment = 2, "Donor/Desynced" })
	eventually(t, "het VIP op nc2", func() bool { return group.Owner(ncVIP) == g.hosts["nc2"] && owner("nc2") })
	nc1.Locked(func() { nc1.MariaDB.Galera.State, nc1.MariaDB.Galera.Comment = 4, "Synced" })
	eventually(t, "het VIP terug op nc1", func() bool { return group.Owner(ncVIP) == nc1 && owner("nc1") })

	// De drift ziet een aangepaste cf_db; opnieuw toepassen zet hem terug
	// en herlaadt keepalived alleen daar.
	driftURL := "/api/v1/clusters/" + nc.ID + "/drift"
	var rep baselineReport
	c.do("POST", driftURL+"/check", nil, &rep)
	if rep.status("nc1") != "in_sync" || rep.status("nc3") != "in_sync" {
		t.Fatalf("drift na de uitrol: %+v", rep)
	}
	writeFile(t, filepath.Join(g.hosts["nc3"].Root, "etc/keepalived/conf.d/clusterforge-db.conf"), "vrrp_script cf_db {\n    script \"/bin/true\"\n}\n", 0o644)
	c.do("POST", driftURL+"/check", nil, &rep)
	if f := rep.findings("nc3"); rep.status("nc3") != "drift" || len(f) != 1 || f[0].Key != "file:/etc/keepalived/conf.d/clusterforge-db.conf:content" {
		t.Fatalf("drift na de wijziging: %+v", rep)
	}
	before := map[string]int{}
	for name, h := range g.hosts {
		before[name] = len(h.Calls())
	}
	var comp list[componentView]
	c.do("GET", base, nil, &comp)
	if s := c.do("POST", base+"/"+comp.Items[0].ID+"/apply", map[string]any{}, &j); s != http.StatusAccepted {
		t.Fatalf("opnieuw: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("opnieuw: %s %s\n%+v", j.Status, j.Error, j.Steps)
	}
	for _, name := range []string{"nc1", "nc2", "nc3"} {
		calls := strings.Join(g.hosts[name].Calls()[before[name]:], "\n")
		if strings.Contains(calls, "reload-or-restart keepalived") != (name == "nc3") || strings.Contains(calls, "keepalived -t") {
			t.Errorf("%s bij opnieuw toepassen:\n%s", name, calls)
		}
	}
}
