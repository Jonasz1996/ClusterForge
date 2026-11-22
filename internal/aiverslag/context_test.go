package aiverslag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

func TestExtBundled(t *testing.T) {
	if ExtVersion() == "" {
		t.Fatal("geen versie in ext56.py")
	}
	e := extSpec()
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	st := parseExt(json.RawMessage(`{"ext":"56.0","regels":12,"stand":{"nodes":3}}`))
	if !st.Installed || !st.Update || st.Rules != 12 || st.Version != "56.0" {
		t.Fatalf("%+v", st)
	}
	if parseExt(json.RawMessage(`{}`)).Installed {
		t.Fatal("niets geïnstalleerd")
	}
}

func TestBuildContext(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO clusters (id, slug, name, type, environment) VALUES ('00000000-0000-0000-0000-0000000000c1', 'db', 'Databasecluster', 'galera', 'prod')`)
	exec(`INSERT INTO nodes (id, cluster_id, hostname, role, lifecycle, primary_ip) VALUES
	 ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000c1', 'DatabaseClusterNode1', 'MariaDB Galera', 'active', '192.168.10.191'),
	 ('00000000-0000-0000-0000-0000000000a2', '00000000-0000-0000-0000-0000000000c1', 'DatabaseClusterNode2', '', 'maintenance', '192.168.10.192'),
	 ('00000000-0000-0000-0000-0000000000a3', NULL, 'oud', '', 'decommissioned', NULL)`)
	exec(`INSERT INTO vips (cluster_id, address, interface, owner_node_id) VALUES ('00000000-0000-0000-0000-0000000000c1', '192.168.10.180', 'eth0', '00000000-0000-0000-0000-0000000000a1')`)
	exec(`INSERT INTO node_changes (node_id, cluster_id, ts, observed_at, source, kind, subject, action, summary) VALUES
	 ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000c1', now() - interval '1 hour', now(), 'clusterforge', 'service', 'mariadb', 'restart', 'mariadb herstart'),
	 ('00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000c1', now() - interval '3 days', now(), 'agent', 'package', 'nginx', 'upgrade', 'oud')`)
	s := NewService(pool, nil, nil, nil, nil)
	c, err := s.BuildContext(ctx, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(c)
	txt := string(data)
	for _, want := range []string{`"naam":"DatabaseClusterNode1","ips":["192.168.10.191"],"cluster":"Databasecluster","omgeving":"productie","rol":"MariaDB Galera"`,
		`"ip":"192.168.10.180","cluster":"Databasecluster","omgeving":"productie","eigenaar":"DatabaseClusterNode1"`,
		`"nodes":["DatabaseClusterNode2"]`, `"tekst":"mariadb herstart (via ClusterForge)"`} {
		if !strings.Contains(txt, want) {
			t.Errorf("mist %s in %s", want, txt)
		}
	}
	if len(c.Nodes) != 2 || len(c.Wijzigingen) != 1 || len(c.Onderhoud) != 1 {
		t.Fatalf("%d nodes, %d wijzigingen, %d onderhoud", len(c.Nodes), len(c.Wijzigingen), len(c.Onderhoud))
	}
}
