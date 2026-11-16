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
}
