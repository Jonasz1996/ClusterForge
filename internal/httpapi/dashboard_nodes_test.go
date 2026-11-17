package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type dashNode struct {
	ID           string   `json:"id"`
	Hostname     string   `json:"hostname"`
	ClusterID    *string  `json:"cluster_id"`
	ClusterSlug  *string  `json:"cluster_slug"`
	ClusterName  *string  `json:"cluster_name"`
	Role         string   `json:"role"`
	Description  string   `json:"description"`
	Lifecycle    string   `json:"lifecycle"`
	PrimaryIP    *string  `json:"primary_ip"`
	Tags         []string `json:"tags"`
	Status       string   `json:"status"`
	StatusLabel  string   `json:"status_label"`
	StatusReason string   `json:"status_reason"`
	Online       bool     `json:"online"`
	Maintenance  bool     `json:"maintenance"`
	Updates      int      `json:"updates"`
	UpdatesSec   int      `json:"updates_security"`
	Reboot       bool     `json:"reboot_required"`
	Proxmox      *struct {
		Host string `json:"host"`
		VMID int    `json:"vmid"`
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"proxmox"`
	URL string `json:"url"`
}

func TestDashboardNodes(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("admin", "een-lang-wachtwoord", store.UserRoleAdmin)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO clusters (slug, name, type, environment) VALUES ('db', 'Databasecluster', 'database', 'prod'), ('web', 'Web', 'generic', 'lab')`)
	exec(`INSERT INTO proxmox_connections (id, name, api_url, token_id, token_secret_enc, key_id)
		VALUES ('00000000-0000-0000-0000-0000000000aa', 'pve', 'https://pve:8006', 'cf@pve!t', '\x00', 'k')`)
	exec(`INSERT INTO proxmox_resources (connection_id, pve_id, type, pve_node, vmid, name, data, synced_at)
		VALUES ('00000000-0000-0000-0000-0000000000aa', 'lxc/101', 'lxc', 'pve1', 101, 'db1-ct', '{}', now())`)
	exec(`INSERT INTO nodes (hostname, cluster_id, role, description, primary_ip, tags, status, status_reason, proxmox_id, pve_vmid)
		VALUES ('db1', (SELECT id FROM clusters WHERE slug = 'db'), 'primary', 'MariaDB primair', '192.168.10.181', '{mariadb,prod}',
			'healthy', '', '00000000-0000-0000-0000-0000000000aa', 101)`)
	exec(`INSERT INTO nodes (hostname, cluster_id, lifecycle, status, status_reason)
		VALUES ('db2', (SELECT id FROM clusters WHERE slug = 'db'), 'maintenance', 'down', 'geen heartbeat')`)
	exec(`INSERT INTO nodes (hostname, cluster_id, lifecycle) VALUES ('db0', (SELECT id FROM clusters WHERE slug = 'db'), 'decommissioned')`)
	exec(`INSERT INTO nodes (hostname, cluster_id) VALUES ('web1', (SELECT id FROM clusters WHERE slug = 'web'))`)
	exec(`INSERT INTO nodes (hostname) VALUES ('los')`)
	exec(`INSERT INTO node_status (node_id, heartbeat_at) SELECT id, now() FROM nodes WHERE hostname IN ('db1', 'web1')`)
	exec(`INSERT INTO node_status (node_id, heartbeat_at) SELECT id, now() - interval '10 minutes' FROM nodes WHERE hostname = 'db2'`)
	exec(`INSERT INTO agents (id, node_id, nkey_public, protocol_version) SELECT gen_random_uuid(), id, 'U' || hostname, 29 FROM nodes WHERE hostname = 'db1'`)
	exec(`INSERT INTO node_facts (node_id, hash, facts, collected_at)
		SELECT id, 'h', '{"upgrades": {"total": 7, "security": 2, "packages": []}, "reboot_required": true}', now() FROM nodes WHERE hostname = 'db1'`)

	admin := e.client()
	if s, _, _ := admin.login("admin", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatal(s)
	}
	newToken := func(body map[string]any) string {
		t.Helper()
		var out struct {
			Token string `json:"token"`
		}
		if s := admin.do("POST", "/api/v1/api-tokens", body, &out); s != 201 {
			t.Fatalf("token maken: %d", s)
		}
		return out.Token
	}
	all := newToken(map[string]any{"name": "homepage"})

	if s := dashboardGet(t, e, "/api/dashboard/v1/nodes", "", "", nil); s != 401 {
		t.Fatalf("zonder token: %d", s)
	}

	var nodes []dashNode
	if s := dashboardGet(t, e, "/api/dashboard/v1/nodes", "Authorization", "Bearer "+all, &nodes); s != 200 {
		t.Fatalf("nodes: %d", s)
	}
	// Geen afgebouwde node; de node zonder cluster achteraan; per cluster op hostname.
	var hosts []string
	for _, n := range nodes {
		hosts = append(hosts, n.Hostname)
	}
	// De clusters volgen elkaar op id (willekeurig): db1, db2, web1 of web1, db1, db2.
	ordered := len(nodes) == 4 && nodes[3].Hostname == "los" &&
		(nodes[0].Hostname == "db1" && nodes[1].Hostname == "db2" && nodes[2].Hostname == "web1" ||
			nodes[0].Hostname == "web1" && nodes[1].Hostname == "db1" && nodes[2].Hostname == "db2")
	if !ordered {
		t.Fatalf("volgorde: %v", hosts)
	}
	byHost := map[string]dashNode{}
	for _, n := range nodes {
		byHost[n.Hostname] = n
	}
	db1 := byHost["db1"]
	if db1.ClusterSlug == nil || *db1.ClusterSlug != "db" || db1.ClusterName == nil || *db1.ClusterName != "Databasecluster" ||
		db1.ClusterID == nil || db1.Role != "primary" || db1.Description != "MariaDB primair" || db1.Lifecycle != "active" ||
		db1.PrimaryIP == nil || *db1.PrimaryIP != "192.168.10.181" || len(db1.Tags) != 2 ||
		db1.Status != "healthy" || db1.StatusLabel != "Gezond" || !db1.Online || db1.Maintenance ||
		db1.Updates != 7 || db1.UpdatesSec != 2 || !db1.Reboot ||
		db1.Proxmox == nil || db1.Proxmox.Host != "pve1" || db1.Proxmox.VMID != 101 || db1.Proxmox.Type != "lxc" || db1.Proxmox.Name != "db1-ct" ||
		!strings.HasPrefix(db1.URL, "http://") || !strings.HasSuffix(db1.URL, "/nodes/detail?id="+db1.ID) {
		t.Fatalf("db1: %+v", db1)
	}
	db2 := byHost["db2"]
	if db2.Online || !db2.Maintenance || db2.Lifecycle != "maintenance" || db2.StatusLabel != "Down" || db2.StatusReason != "geen heartbeat" ||
		db2.Proxmox != nil || db2.PrimaryIP != nil || db2.Tags == nil || db2.Updates != 0 {
		t.Fatalf("db2: %+v", db2)
	}
	los := byHost["los"]
	if los.ClusterID != nil || los.ClusterSlug != nil || los.ClusterName != nil {
		t.Fatalf("los: %+v", los)
	}

	// ?cluster= op slug, naam of id.
	var dbNodes []dashNode
	if s := dashboardGet(t, e, "/api/dashboard/v1/nodes?cluster=db", "Authorization", "Bearer "+all, &dbNodes); s != 200 || len(dbNodes) != 2 {
		t.Fatalf("cluster=db: %d %+v", s, dbNodes)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/nodes?cluster=Databasecluster", "Authorization", "Bearer "+all, &dbNodes); s != 200 || len(dbNodes) != 2 {
		t.Fatalf("cluster op naam: %d", s)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/nodes?cluster="+*db1.ClusterID, "Authorization", "Bearer "+all, &dbNodes); s != 200 || len(dbNodes) != 2 {
		t.Fatalf("cluster op id: %d", s)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/nodes?cluster=bestaat-niet", "Authorization", "Bearer "+all, nil); s != 404 {
		t.Fatalf("onbekend cluster: %d", s)
	}

	// Een token voor één cluster ziet alleen de nodes daarvan, niet de losse node.
	scoped := newToken(map[string]any{"name": "alleen web", "cluster_ids": []string{*byHost["web1"].ClusterID}})
	var some []dashNode
	if s := dashboardGet(t, e, "/api/dashboard/v1/nodes", "X-API-Key", scoped, &some); s != 200 || len(some) != 1 || some[0].Hostname != "web1" {
		t.Fatalf("token voor web: %d %+v", s, some)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/nodes?cluster=db", "X-API-Key", scoped, nil); s != 404 {
		t.Fatalf("token voor web ziet db: %d", s)
	}
}
