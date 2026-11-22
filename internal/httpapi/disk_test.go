package httpapi

import (
	"context"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestDiskWatch(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	c := e.client()
	c.login("jonas", "een-lang-wachtwoord", "")

	var st struct {
		Warn   int      `json:"warn_pct"`
		Crit   int      `json:"crit_pct"`
		Ignore []string `json:"ignore_mounts"`
	}
	if s := c.do("GET", "/api/v1/disk/settings", nil, &st); s != 200 || st.Warn != 80 || st.Crit != 90 {
		t.Fatalf("standaard: %d %+v", s, st)
	}
	if s := c.do("PUT", "/api/v1/disk/settings", map[string]any{"warn_pct": 90, "crit_pct": 85, "ignore_mounts": []string{}}, nil); s != 400 {
		t.Fatalf("ernstig onder let op: %d", s)
	}
	if s := c.do("PUT", "/api/v1/disk/settings", map[string]any{"warn_pct": 75, "crit_pct": 92, "ignore_mounts": []string{" /mnt/backup ", "geen-map"}}, nil); s != 400 {
		t.Fatalf("mount zonder /: %d", s)
	}
	if s := c.do("PUT", "/api/v1/disk/settings", map[string]any{"warn_pct": 75, "crit_pct": 92, "ignore_mounts": []string{" /mnt/backup "}}, &st); s != 200 || st.Ignore[0] != "/mnt/backup" {
		t.Fatalf("bewaren: %d %+v", s, st)
	}

	ctx := context.Background()
	var id string
	if err := e.pool.QueryRow(ctx, `INSERT INTO nodes (hostname) VALUES ('ai1') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public) VALUES (gen_random_uuid(), $1, 'Uai1')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, disk_used_ratio, disk_used_mount) VALUES ($1, now() - interval '1 hour', 0.8, '/')`, id); err != nil {
		t.Fatal(err)
	}
	var nd struct {
		Level  string `json:"level"`
		Online bool   `json:"online"`
	}
	if s := c.do("GET", "/api/v1/nodes/"+id+"/disk", nil, &nd); s != 200 || nd.Level != "warn" || nd.Online {
		t.Fatalf("node: %d %+v", s, nd)
	}
	if s := c.do("POST", "/api/v1/nodes/"+id+"/disk/scan", nil, nil); s != 400 {
		t.Fatalf("meten op een offline node: %d", s)
	}
	if s := c.do("POST", "/api/v1/nodes/"+id+"/disk/cleanup", map[string]any{"items": []string{"alles"}}, nil); s != 400 {
		t.Fatalf("onbekend onderdeel: %d", s)
	}
}
