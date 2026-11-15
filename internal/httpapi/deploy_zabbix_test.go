package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

// TestDeployZabbix: de template Zabbix krijgt altijd MariaDB Galera, ook
// zonder Database erbij, en alleen op de drie servers; de proxy krijgt er
// geen.
func TestDeployZabbix(t *testing.T) {
	e, c := adminClient(t)
	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "debian-13-cf", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"scsi0": "ceph:base-9001-disk-0,size=3G", "ide2": "ceph:vm-9001-cloudinit,media=cdrom",
			"boot": "order=scsi0", "agent": "enabled=1", "name": "debian-13-cf",
		},
	})
	f := newFleet(t, map[string]string{
		"zabbix-server-01": "10.0.20.41", "zabbix-server-02": "10.0.20.42", "zabbix-server-03": "10.0.20.43",
		"zabbix-proxy-01": "10.0.20.44",
	})
	g := &galeraHosts{hosts: map[string]*agenttest.Host{}, slug: "zabbix"}
	f.setup = func(name string, h *agenttest.Host) {
		writeFile(t, filepath.Join(h.Root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
		g.mu.Lock()
		g.hosts[name] = h
		g.mu.Unlock()
		h.SetAfter(g.after(name, h))
	}
	pve.OnFileWrite(f.boot)
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}

	// De lijst van templates zegt dat Zabbix een database meebrengt.
	var tpls list[struct {
		Name     string `json:"name"`
		Database *struct {
			Engine string `json:"engine"`
			Role   string `json:"role"`
		} `json:"database"`
	}]
	c.do("GET", "/api/v1/templates", nil, &tpls)
	for _, tp := range tpls.Items {
		want := map[string]string{"zabbix": "mariadb/server", "media-platform": "postgresql/node", "home-assistant": "mariadb/node"}[tp.Name]
		got := ""
		if tp.Database != nil {
			got = tp.Database.Engine + "/" + tp.Database.Role
		}
		if got != want {
			t.Fatalf("database van %s: %+v", tp.Name, tp.Database)
		}
	}

	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", map[string]any{
		"template": "zabbix",
		"cluster":  map[string]any{"name": "Zabbix", "slug": "zabbix", "environment": "lab"},
		"params":   map[string]any{"vip": "10.0.20.40"},
		"target": map[string]any{
			"proxmox_id": conn.ID, "image_vmid": 9001, "network": "static",
			"first_ip": "10.0.20.41/24", "gateway": "10.0.20.1", "dns": []string{"10.0.20.1"},
		},
		"server_url": e.srv.URL,
	}, &res); s != 202 {
		t.Fatalf("uitrollen: %d", s)
	}
	if j := waitJobLong(t, c, res.Job.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	proxy := f.host("zabbix-proxy-01")
	if b, err := os.ReadFile(filepath.Join(proxy.Root, "etc/zabbix/cf-zabbix_proxy.conf")); err != nil || !strings.Contains(string(b), "Server=10.0.20.41;10.0.20.42;10.0.20.43\n") {
		t.Fatalf("proxy: %v\n%s", err, b)
	}

	base := "/api/v1/clusters/" + res.ClusterID + "/components"
	var comps list[componentView]
	eventually(t, "database erbij", func() bool {
		c.do("GET", base, nil, &comps)
		return len(comps.Items) == 1
	})
	cv := comps.Items[0]
	if cv.JobID == nil {
		t.Fatalf("onderdeel zonder taak: %+v", cv)
	}
	if j := waitJobLong(t, c, *cv.JobID); j.Status != "succeeded" {
		t.Fatalf("database: %s %s", j.Status, j.Error)
	}
	c.do("GET", base, nil, &comps)
	cv = comps.Items[0]
	var hosts []string
	for _, nd := range cv.Nodes {
		hosts = append(hosts, nd.Hostname+"/"+nd.Role)
	}
	if cv.Engine != "mariadb" || cv.State != "ready" || strings.Join(hosts, " ") != "zabbix-server-01/data zabbix-server-02/data zabbix-server-03/data" {
		t.Fatalf("database: %+v", cv)
	}
}

// TestDeployZabbixExisting: Zabbix op bestaande nodes, zoals CT's waarop
// Jonas zelf de agent installeerde. Er komen geen VM's; de nodes krijgen
// hun rol en het nieuwe cluster, en de database komt op de drie servers.
func TestDeployZabbixExisting(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	g := &galeraHosts{hosts: map[string]*agenttest.Host{}, slug: "zabbix-ha"}
	vrrp := &agenttest.Group{}
	ids := map[string]string{}
	names := []string{"zbx1", "zbx2", "zbx3", "zbxproxy"}
	for i, name := range names {
		ip := fmt.Sprintf("10.0.20.%d", 51+i)
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "primary_ip": ip}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
			writeFile(t, filepath.Join(root, "proc/meminfo"), "MemTotal: 4194304 kB\nMemAvailable: 2097152 kB\n", 0o644)
			h := agenttest.NewHost(root, ip)
			vrrp.Join(h)
			g.mu.Lock()
			g.hosts[name] = h
			g.mu.Unlock()
			h.SetAfter(g.after(name, h))
			ag.Exec, ag.Root, ag.Collector.Run = h.Exec, root, h.Exec
			ag.Collector.Interfaces, ag.Collector.PrimaryAddress = h.Interfaces, func() string { return ip }
			ag.Collector.Addresses = h.Addresses
		})
	}
	eventually(t, "facts van de vier nodes", func() bool {
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM node_facts f JOIN nodes n ON n.id = f.node_id WHERE n.hostname LIKE 'zbx%' AND f.facts->'os'->>'id' = 'debian'").Scan(&n)
		return n == 4
	})

	in := func(proxy string) map[string]any {
		return map[string]any{
			"template": "zabbix",
			"cluster":  map[string]any{"name": "Zabbix HA", "slug": "zabbix-ha", "environment": "lab"},
			"params":   map[string]any{"vip": "10.0.20.50"},
			"existing_nodes": []map[string]any{
				{"node_id": ids["zbx1"], "role": "server"}, {"node_id": ids["zbx2"], "role": "server"},
				{"node_id": ids["zbx3"], "role": "server"}, {"node_id": ids[proxy], "role": "proxy"},
			},
		}
	}
	// Een rol met te weinig nodes, of een node die twee keer staat, kan niet.
	var ae apiErr
	if s := c.do("POST", "/api/v1/deployments", in("zbx3"), &ae); s != http.StatusBadRequest {
		t.Fatalf("dubbele node: %d %+v", s, ae)
	}
	// Het plan toont de bestaande hostnames en adressen.
	var plan struct {
		Nodes []struct {
			Hostname string `json:"hostname"`
			Role     string `json:"role"`
			Address  string `json:"address"`
		} `json:"nodes"`
	}
	if s := c.do("POST", "/api/v1/deployments/plan", in("zbxproxy"), &plan); s != http.StatusOK || len(plan.Nodes) != 4 ||
		plan.Nodes[3].Hostname != "zbxproxy" || plan.Nodes[3].Role != "proxy" || plan.Nodes[0].Address != "10.0.20.51/24" {
		t.Fatalf("plan: %d %+v", s, plan)
	}

	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", in("zbxproxy"), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	if j := waitJobLong(t, c, res.Job.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	var roles []string
	rows, _ := e.pool.Query(ctx, "SELECT hostname || '/' || role || '/' || lifecycle FROM nodes WHERE cluster_id = $1 ORDER BY hostname", res.ClusterID)
	for rows.Next() {
		var r string
		_ = rows.Scan(&r)
		roles = append(roles, r)
	}
	rows.Close()
	if strings.Join(roles, " ") != "zbx1/server/active zbx2/server/active zbx3/server/active zbxproxy/proxy/active" {
		t.Fatalf("nodes: %v", roles)
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
	var hosts []string
	for _, nd := range comps.Items[0].Nodes {
		hosts = append(hosts, nd.Hostname)
	}
	if strings.Join(hosts, " ") != "zbx1 zbx2 zbx3" {
		t.Fatalf("database op %v", hosts)
	}
}
