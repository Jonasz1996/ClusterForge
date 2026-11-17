package httpapi

import (
	"context"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestAttention(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleViewer)
	c := e.client()
	c.login("jonas", "een-lang-wachtwoord", "")

	type att struct {
		Items []struct {
			ID    string `json:"id"`
			Tone  string `json:"tone"`
			Title string `json:"title"`
			Href  string `json:"href"`
			Count int    `json:"count"`
		} `json:"items"`
		Badges map[string]struct {
			Count int    `json:"count"`
			Tone  string `json:"tone"`
		} `json:"badges"`
	}
	var a att
	if s := c.do("GET", "/api/v1/attention", nil, &a); s != 200 {
		t.Fatalf("attention: %d", s)
	}
	if len(a.Items) != 1 || a.Items[0].ID != "totp-off" || a.Badges["/instellingen"].Count != 1 {
		t.Fatalf("alleen TOTP verwacht: %+v", a)
	}

	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO clusters (slug, name, type, environment, status) VALUES ('web', 'Web', 'generic', 'lab', 'down'), ('db', 'DB', 'generic', 'lab', 'degraded')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (kind, title, status, finished_at) VALUES ('x', 'x', 'failed', now()), ('x', 'oud', 'failed', now() - interval '2 days')`); err != nil {
		t.Fatal(err)
	}
	a = att{}
	c.do("GET", "/api/v1/attention", nil, &a)
	got := map[string]int{}
	for _, it := range a.Items {
		got[it.ID] = it.Count
	}
	if got["clusters-down"] != 1 || got["clusters-degraded"] != 1 || got["jobs-failed"] != 1 {
		t.Fatalf("meldingen: %+v", a.Items)
	}
	if a.Items[0].Tone != "red" {
		t.Fatalf("rood eerst: %+v", a.Items)
	}
	if b := a.Badges["/clusters"]; b.Count != 2 || b.Tone != "red" {
		t.Fatalf("badge clusters: %+v", b)
	}

	// Schijfbewaking (fase 41): 85 % is let op, 93 % ernstig, met uitleg.
	// De badge van Nodes telt ook de drie agents zonder recente hartslag.
	if _, err := e.pool.Exec(ctx, `INSERT INTO nodes (hostname) VALUES ('ai1'), ('ai2'), ('ai3')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public) SELECT gen_random_uuid(), id, 'U' || hostname FROM nodes WHERE hostname LIKE 'ai%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, disk_used_ratio, disk_used_mount)
		SELECT id, now(), CASE hostname WHEN 'ai1' THEN 0.85 WHEN 'ai2' THEN 0.93 ELSE 0.79 END, '/' FROM nodes WHERE hostname LIKE 'ai%'`); err != nil {
		t.Fatal(err)
	}
	var full struct {
		Items []struct {
			ID     string `json:"id"`
			Tone   string `json:"tone"`
			Detail string `json:"detail"`
			Href   string `json:"href"`
			Help   string `json:"help"`
		} `json:"items"`
		Badges map[string]struct {
			Count int `json:"count"`
		} `json:"badges"`
	}
	c.do("GET", "/api/v1/attention", nil, &full)
	seen := map[string]bool{}
	for _, it := range full.Items {
		if it.Help == "" {
			t.Errorf("geen uitleg bij %s", it.ID)
		}
		switch it.ID {
		case "disk-warn":
			seen[it.ID] = it.Tone == "amber" && it.Detail == "ai1 (/ 85 %)"
		case "disk-crit":
			seen[it.ID] = it.Tone == "red" && it.Detail == "ai2 (/ 93 %)"
		}
	}
	if !seen["disk-warn"] || !seen["disk-crit"] || full.Badges["/nodes"].Count != 5 {
		t.Fatalf("schijfmeldingen: %+v", full)
	}
}
