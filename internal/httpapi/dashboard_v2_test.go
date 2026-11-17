package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestDashboardV2Apps(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("admin", "een-lang-wachtwoord", store.UserRoleAdmin)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO clusters (slug, name, type, environment, template_name) VALUES ('hp', 'Homepage', 'generic', 'prod', 'homepage'), ('ander', 'Ander', 'generic', 'lab', '')`)
	exec(`INSERT INTO nodes (hostname, cluster_id, primary_ip) VALUES ('hp1', (SELECT id FROM clusters WHERE slug = 'hp'), '192.168.113.156')`)
	exec(`INSERT INTO nodes (hostname, primary_ip) VALUES ('plex1', '192.168.10.50')`)
	exec(`INSERT INTO node_status (node_id, heartbeat_at) SELECT id, now() FROM nodes`)
	exec(`INSERT INTO publish_services (node_id, services, error, checked_at) SELECT id,
		'[{"proto":"tcp","addr":"0.0.0.0","port":80,"unit":"nginx","kind":"http","status":200}]', '', now() FROM nodes WHERE hostname = 'hp1'`)
	exec(`INSERT INTO publish_services (node_id, services, error, checked_at) SELECT id,
		'[{"proto":"tcp","addr":"0.0.0.0","port":32400,"container":"plex","image":"plexinc/pms-docker:1.41.2","kind":"http","status":401},
		  {"proto":"tcp","addr":"0.0.0.0","port":9999,"container":"iets","kind":"http","status":200},
		  {"proto":"tcp","addr":"0.0.0.0","port":22,"unit":"ssh","kind":"tcp"}]', '', now() FROM nodes WHERE hostname = 'plex1'`)

	admin := e.client()
	if s, _, _ := admin.login("admin", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatal(s)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if s := admin.do("POST", "/api/v1/api-tokens", map[string]any{"name": "homepage"}, &tok); s != 201 {
		t.Fatalf("token: %d", s)
	}
	var scoped struct {
		Token string `json:"token"`
	}
	var hpID string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM clusters WHERE slug = 'hp'`).Scan(&hpID); err != nil {
		t.Fatal(err)
	}
	if s := admin.do("POST", "/api/v1/api-tokens", map[string]any{"name": "hp", "cluster_ids": []string{hpID}}, &scoped); s != 201 {
		t.Fatalf("token: %d", s)
	}
	auth := "Bearer " + tok.Token

	if s := dashboardGet(t, e, "/api/dashboard/v2/apps", "", "", nil); s != 401 {
		t.Fatalf("zonder token: %d", s)
	}
	var apps []v2Tile
	if s := dashboardGet(t, e, "/api/dashboard/v2/apps", "Authorization", auth, &apps); s != 200 || len(apps) != 2 {
		t.Fatalf("apps: %d %+v", s, apps)
	}
	by := map[string]v2Tile{}
	for _, a := range apps {
		by[a.Slug] = a
	}
	plex, hp := by["plex"], by["homepage"]
	if plex.Status != "ok" || plex.Values["version"] != "1.41.2" || plex.Where.Container != "plex" || plex.Where.Node != "plex1" ||
		len(plex.Fields) == 0 || plex.Fields[0].Key != "status" || plex.URL != "http://192.168.10.50:32400" {
		t.Errorf("plex: %+v", plex)
	}
	if hp.Where.Cluster != "Homepage" || hp.Status != "ok" || hp.Group != "Infrastructuur" {
		t.Errorf("homepage: %+v", hp)
	}
	// Token voor één cluster: alleen de homepage.
	var some []v2Tile
	if s := dashboardGet(t, e, "/api/dashboard/v2/apps", "X-API-Key", scoped.Token, &some); s != 200 || len(some) != 1 || some[0].Slug != "homepage" {
		t.Fatalf("scoped: %d %+v", s, some)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v2/apps/plex", "X-API-Key", scoped.Token, nil); s != 404 {
		t.Fatalf("scoped plex: %d", s)
	}
	var one v2Tile
	if s := dashboardGet(t, e, "/api/dashboard/v2/apps/plex?strict=1", "Authorization", auth, &one); s != 200 || one.Slug != "plex" {
		t.Fatalf("plex: %d", s)
	}

	// De indeling met ETag.
	var layout v2Layout
	if s := dashboardGet(t, e, "/api/dashboard/v2/homepage/layout", "Authorization", auth, &layout); s != 200 || layout.ManagedBy != "clusterforge" || len(layout.Groups) != 2 {
		t.Fatalf("layout: %d %+v", s, layout)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/dashboard/v2/homepage/layout", nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("If-None-Match", `"`+layout.Version+`"`)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 304 {
		t.Fatalf("If-None-Match: %d", res.StatusCode)
	}

	// Bijsturen in de GUI: Plex verbergen; twijfel tot app maken.
	var adm homeAppsAdmin
	if s := admin.do("GET", "/api/v1/home-apps", nil, &adm); s != 200 || len(adm.Apps) != 2 || len(adm.Doubts) != 1 {
		t.Fatalf("beheer: %d %+v", s, adm)
	}
	if s := admin.do("POST", "/api/v1/home-apps/override", map[string]any{"key": "plex@plex1", "override": map[string]any{"hidden": true}}, &adm); s != 200 {
		t.Fatalf("override: %d", s)
	}
	if s := admin.do("POST", "/api/v1/home-apps/override", map[string]any{"key": "plex@plex1", "override": map[string]any{"url": "javascript:x"}}, nil); s != 400 {
		t.Fatalf("slecht adres: %d", s)
	}
	if s := admin.do("POST", "/api/v1/home-apps/doubt", map[string]any{"key": adm.Doubts[0].Key, "choice": "app", "name": "Mijn ding"}, &adm); s != 200 || len(adm.Doubts) != 0 {
		t.Fatalf("twijfel: %d %+v", s, adm.Doubts)
	}
	e.api.invalidateHomeApps()
	apps = nil
	if s := dashboardGet(t, e, "/api/dashboard/v2/apps", "Authorization", auth, &apps); s != 200 || len(apps) != 2 {
		t.Fatalf("na bijsturen: %d %+v", s, apps)
	}
	for _, a := range apps {
		if a.Slug == "plex" {
			t.Error("plex is verborgen")
		}
	}
	var src map[string]any
	if s := dashboardGet(t, e, "/api/dashboard/v2/sources/agents", "Authorization", auth, &src); s != 200 || src["agents"] != "2/2" {
		t.Fatalf("agents: %d %+v", s, src)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v2/sources/npm", "Authorization", auth, &src); s != 200 {
		t.Fatalf("npm: %d", s)
	}
	var ov v2Overview
	if s := dashboardGet(t, e, "/api/dashboard/v2/", "Authorization", auth, &ov); s != 200 || ov.Apps != 2 {
		t.Fatalf("overzicht: %d %+v", s, ov)
	}
	if s := admin.do("POST", "/api/v1/home-apps/override", map[string]any{"key": "plex@plex1", "override": map[string]any{}}, nil); s != 200 {
		t.Fatal("terug op automatisch")
	}
}
