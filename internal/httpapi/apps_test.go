package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestClusterApps(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleViewer)
	c := e.client()
	c.login("jonas", "een-lang-wachtwoord", "")
	ctx := context.Background()
	var clusterID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment) VALUES ('media', 'Media', 'generic', 'prod') RETURNING id`).Scan(&clusterID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, role := range []string{"actief", "stand-by"} {
		var nodeID string
		host := []string{"media1", "media2"}[i]
		if err := e.pool.QueryRow(ctx, `INSERT INTO nodes (cluster_id, hostname, role) VALUES ($1, $2, 'node') RETURNING id`, clusterID, host).Scan(&nodeID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, last_seen_at) VALUES (gen_random_uuid(), $1, $2, now())`, nodeID, "U"+host); err != nil {
			t.Fatal(err)
		}
		ok := "true"
		if i == 1 {
			ok = "false"
		}
		facts := `{"apps":[{"app":"media-platform","title":"Mediaplatform","version":"aa6fa8b","role":"` + role + `","updated_at":"` + now +
			`","checks":[{"id":"mount","label":"Bestanden gemount","ok":` + ok + `,"severity":"red","detail":"niet gemount"}],"values":[]}]}`
		if _, err := e.pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'x', $2, now())`, nodeID, facts); err != nil {
			t.Fatal(err)
		}
	}
	var o struct {
		Apps []struct {
			App    string                            `json:"app"`
			Nodes  []struct{ Hostname, Role string } `json:"nodes"`
			Issues []struct{ Tone, Title string }    `json:"issues"`
		} `json:"apps"`
	}
	if s := c.do("GET", "/api/v1/clusters/"+clusterID+"/apps", nil, &o); s != 200 {
		t.Fatalf("apps: %d", s)
	}
	if len(o.Apps) != 1 || len(o.Apps[0].Nodes) != 2 || len(o.Apps[0].Issues) != 1 || o.Apps[0].Issues[0].Title != "Bestanden gemount op media2" {
		t.Fatalf("apps: %+v", o)
	}
	var a struct {
		Items []struct{ ID, Title, Href string } `json:"items"`
	}
	c.do("GET", "/api/v1/attention", nil, &a)
	found := false
	for _, it := range a.Items {
		if strings.HasPrefix(it.ID, "apps-red-") && it.Title == "Media: Bestanden gemount op media2" && strings.Contains(it.Href, "#applicatie") {
			found = true
		}
	}
	if !found {
		t.Fatalf("aandacht: %+v", a.Items)
	}
}
