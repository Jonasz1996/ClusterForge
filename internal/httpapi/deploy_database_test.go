package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

// TestDeployWithDatabase: 'Database erbij' bij Cluster uitrollen. Een nieuw
// cluster met drie nodes krijgt na de uitrol meteen MariaDB Galera, om er
// een app naartoe te verhuizen; met twee nodes zegt het logboek waarom
// niet.
func TestDeployWithDatabase(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "debian-13-cf", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"scsi0": "ceph:base-9001-disk-0,size=3G", "ide2": "ceph:vm-9001-cloudinit,media=cdrom",
			"boot": "order=scsi0", "agent": "enabled=1", "name": "debian-13-cf",
		},
	})
	f := newFleet(t, map[string]string{
		"nextcloud-01": "10.0.20.11", "nextcloud-02": "10.0.20.12", "nextcloud-03": "10.0.20.13",
		"zabbix-01": "10.0.20.21", "zabbix-02": "10.0.20.22",
	})
	g := &galeraHosts{hosts: map[string]*agenttest.Host{}}
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
	request := func(slug, name, vip, first string, nodes int, engine string) map[string]any {
		return map[string]any{
			"template": "keepalived-nginx",
			"cluster":  map[string]any{"name": name, "slug": slug, "environment": "lab"},
			"params":   map[string]any{"vip": vip, "auth_pass": "geheim12", "node_count": nodes},
			"target": map[string]any{
				"proxmox_id": conn.ID, "image_vmid": 9001, "network": "static",
				"first_ip": first, "gateway": "10.0.20.1", "dns": []string{"10.0.20.1"},
			},
			"server_url": e.srv.URL,
			"database":   map[string]any{"engine": engine},
		}
	}
	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}

	// PostgreSQL kan nog niet; dan komt er ook geen cluster.
	var ae apiErr
	if s := c.do("POST", "/api/v1/deployments", request("nextcloud", "Nextcloud", "10.0.20.185", "10.0.20.11/24", 3, "postgresql"), &ae); s != http.StatusBadRequest ||
		!strings.Contains(ae.Message, "PostgreSQL") {
		t.Fatalf("postgresql: %d %+v", s, ae)
	}
	// Een Kubernetes-cluster ook niet: daar horen de databases in
	// Kubernetes zelf.
	kube := request("leren", "Leren", "10.0.20.190", "10.0.20.31/24", 3, "mariadb")
	kube["template"], kube["params"] = "kubernetes", map[string]any{"vip": "10.0.20.190"}
	var ke struct {
		Message string `json:"message"`
		Field   string `json:"field"`
	}
	if s := c.do("POST", "/api/v1/deployments", kube, &ke); s != http.StatusBadRequest || !strings.Contains(ke.Message, "Kubernetes") || ke.Field != "database" {
		t.Fatalf("kubernetes: %d %+v", s, ke)
	}
	var n int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM clusters").Scan(&n)
	if n != 0 {
		t.Fatalf("%d clusters", n)
	}

	// Drie nodes: na de uitrol de database erbij.
	if s := c.do("POST", "/api/v1/deployments", request("nextcloud", "Nextcloud", "10.0.20.185", "10.0.20.11/24", 3, "mariadb"), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	if j := waitJobLong(t, c, res.Job.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
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
	if cv.Engine != "mariadb" || cv.State != "ready" || strings.Join(hosts, " ") != "nextcloud-01/data nextcloud-02/data nextcloud-03/data" {
		t.Fatalf("database: %+v", cv)
	}
	var by string
	if err := e.pool.QueryRow(ctx, "SELECT actor_type FROM events WHERE action = 'cluster.database_added' AND cluster_id = $1", res.ClusterID).Scan(&by); err != nil || by != "user" {
		t.Fatalf("event database_added: %v %s", err, by)
	}

	// Twee nodes: geen meerderheid zonder scheidsrechter, dus geen
	// database; de uitrol zelf lukt wel.
	if s := c.do("POST", "/api/v1/deployments", request("zabbix", "Zabbix", "10.0.20.180", "10.0.20.21/24", 2, "mariadb"), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen zabbix: %d", s)
	}
	if j := waitJobLong(t, c, res.Job.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol zabbix: %s %s", j.Status, j.Error)
	}
	var reason string
	eventually(t, "reden in het logboek", func() bool {
		_ = e.pool.QueryRow(ctx, "SELECT payload->>'reason' FROM events WHERE action = 'cluster.database_not_added' AND cluster_id = $1", res.ClusterID).Scan(&reason)
		return reason != ""
	})
	if !strings.Contains(reason, "scheidsrechter") {
		t.Fatalf("reden: %s", reason)
	}
	c.do("GET", "/api/v1/clusters/"+res.ClusterID+"/components", nil, &comps)
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM deploy_databases").Scan(&n)
	if len(comps.Items) != 0 || n != 0 {
		t.Fatalf("zabbix: %d onderdelen, %d wensen", len(comps.Items), n)
	}
}
