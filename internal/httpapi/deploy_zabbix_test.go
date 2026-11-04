package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		if (tp.Name == "zabbix") != (tp.Database != nil) || (tp.Database != nil && (tp.Database.Engine != "mariadb" || tp.Database.Role != "server")) {
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
