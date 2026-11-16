package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func dashboardGet(t *testing.T, e *testEnv, path, header, token string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	if token != "" {
		req.Header.Set(header, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

type dashCluster struct {
	Slug           string   `json:"slug"`
	Name           string   `json:"name"`
	Url            string   `json:"url"`
	Status         string   `json:"status"`
	StatusLabel    string   `json:"status_label"`
	Ok             bool     `json:"ok"`
	Score          *int     `json:"score"`
	Nodes          string   `json:"nodes"`
	NodesOnline    int      `json:"nodes_online"`
	NodesTotal     int      `json:"nodes_total"`
	NodesDown      []string `json:"nodes_down"`
	Vip            string   `json:"vip"`
	VipOwner       string   `json:"vip_owner"`
	Db             string   `json:"db"`
	DbLabel        string   `json:"db_label"`
	IncidentsOpen  int      `json:"incidents_open"`
	IncidentsWorst string   `json:"incidents_worst"`
	Crowdsec       string   `json:"crowdsec"`
	CrowdsecBans   int      `json:"crowdsec_bans"`
	Firewall       string   `json:"firewall"`
	Problems       []string `json:"problems"`
}

func TestDashboardAPI(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("admin", "een-lang-wachtwoord", store.UserRoleAdmin)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO clusters (slug, name, type, environment, status, status_reason) VALUES
		('db', 'Databasecluster', 'database', 'prod', 'degraded', 'node3 reageert niet'),
		('web', 'Web', 'generic', 'lab', 'healthy', '')`)
	exec(`INSERT INTO nodes (hostname, cluster_id) SELECT h, (SELECT id FROM clusters WHERE slug = 'db')
		FROM unnest(ARRAY['db1', 'db2', 'db3']) h`)
	exec(`INSERT INTO nodes (hostname, cluster_id) VALUES ('web1', (SELECT id FROM clusters WHERE slug = 'web'))`)
	exec(`INSERT INTO node_status (node_id, heartbeat_at) SELECT id, now() FROM nodes WHERE hostname IN ('db1', 'db2', 'web1')`)
	exec(`INSERT INTO node_status (node_id, heartbeat_at) SELECT id, now() - interval '10 minutes' FROM nodes WHERE hostname = 'db3'`)
	exec(`INSERT INTO vips (cluster_id, address, owner_node_id) VALUES
		((SELECT id FROM clusters WHERE slug = 'db'), '192.168.10.180', (SELECT id FROM nodes WHERE hostname = 'db2'))`)
	exec(`INSERT INTO db_servers (node_id, engine, running, synced_at)
		SELECT id, 'mariadb', hostname <> 'db3', now() FROM nodes WHERE hostname LIKE 'db%'`)
	exec(`WITH i AS (INSERT INTO incidents (title, severity, status, started_at, detected_at)
		VALUES ('db3 down', 'high', 'open', now(), now()) RETURNING id)
		INSERT INTO incident_subjects (incident_id, subject_type, subject_id, name, cluster_id, first_status, worst_status, last_status, since)
		SELECT i.id, 'node', n.id, 'db3', n.cluster_id, 'down', 'down', 'down', now() FROM i, nodes n WHERE n.hostname = 'db3'`)
	exec(`INSERT INTO crowdsec_nodes (node_id, managed, bouncer, status)
		SELECT id, true, true, '{"installed": true, "active": "active", "bouncer": "active"}' FROM nodes WHERE hostname IN ('db1', 'db2')`)
	exec(`INSERT INTO crowdsec_decisions (node_id, decision_id, origin, type, scope, value, until)
		SELECT id, 1, 'crowdsec', 'ban', 'Ip', '1.2.3.4', now() + interval '1 hour' FROM nodes WHERE hostname = 'db1'`)

	admin := e.client()
	if s, _, _ := admin.login("admin", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatal(s)
	}
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatal(s)
	}

	// Zonder of met een fout token: 401.
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters", "", "", nil); s != 401 {
		t.Fatalf("zonder token: %d", s)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters", "Authorization", "Bearer cfa_fout", nil); s != 401 {
		t.Fatalf("fout token: %d", s)
	}
	// Een sessie geeft geen toegang tot de dashboard-API.
	if s := admin.do("GET", "/api/dashboard/v1/clusters", nil, nil); s != 401 {
		t.Fatalf("met sessie: %d", s)
	}

	// Alleen een beheerder maakt tokens.
	if s := viewer.do("POST", "/api/v1/api-tokens", map[string]any{"name": "homepage"}, nil); s != 403 {
		t.Fatalf("viewer maakt token: %d", s)
	}
	var created struct {
		Token string `json:"token"`
		Item  struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
		} `json:"item"`
	}
	if s := admin.do("POST", "/api/v1/api-tokens", map[string]any{"name": "homepage"}, &created); s != 201 ||
		!strings.HasPrefix(created.Token, "cfa_") || !strings.HasPrefix(created.Token, created.Item.Prefix) {
		t.Fatalf("token maken: %d %+v", s, created)
	}

	var all []dashCluster
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters", "Authorization", "Bearer "+created.Token, &all); s != 200 || len(all) != 2 {
		t.Fatalf("clusters: %d %+v", s, all)
	}
	var db dashCluster
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters/db", "X-API-Key", created.Token, &db); s != 200 {
		t.Fatalf("cluster db: %d", s)
	}
	if db.Nodes != "2/3" || db.NodesOnline != 2 || db.NodesTotal != 3 || len(db.NodesDown) != 1 || db.NodesDown[0] != "db3" ||
		db.Vip != "192.168.10.180" || db.VipOwner != "db2" || db.Db != "degraded" || db.DbLabel != "MariaDB 2/3" ||
		db.IncidentsOpen != 1 || db.IncidentsWorst != "high" || db.Ok || db.StatusLabel != "Verminderd" ||
		db.Crowdsec != "2/2 actief" || db.CrowdsecBans != 1 || db.Firewall != "—" || db.Score != nil ||
		!strings.HasPrefix(db.Url, "http://") || len(db.Problems) < 3 || db.Problems[0] != "node3 reageert niet" {
		t.Fatalf("cluster db: %+v", db)
	}
	var web dashCluster
	if dashboardGet(t, e, "/api/dashboard/v1/clusters/Web", "Authorization", "Bearer "+created.Token, &web); !web.Ok || web.Db != "none" ||
		web.Nodes != "1/1" || len(web.Problems) != 0 {
		t.Fatalf("cluster web: %+v", web)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters/db?strict=1", "Authorization", "Bearer "+created.Token, nil); s != 503 {
		t.Fatalf("strict db: %d", s)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters/web?strict=1", "Authorization", "Bearer "+created.Token, nil); s != 200 {
		t.Fatalf("strict web: %d", s)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters/bestaat-niet", "Authorization", "Bearer "+created.Token, nil); s != 404 {
		t.Fatalf("onbekend cluster: %d", s)
	}
	var overview struct {
		ClustersTotal   int    `json:"clusters_total"`
		ClustersHealthy int    `json:"clusters_healthy"`
		Nodes           string `json:"nodes"`
		IncidentsOpen   int    `json:"incidents_open"`
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/", "Authorization", "Bearer "+created.Token, &overview); s != 200 ||
		overview.ClustersTotal != 2 || overview.ClustersHealthy != 1 || overview.Nodes != "3/4" || overview.IncidentsOpen != 1 {
		t.Fatalf("overzicht: %d %+v", s, overview)
	}
	// Alleen lezen.
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/dashboard/v1/clusters", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 405 {
		t.Fatalf("POST: %v %v", resp, err)
	}

	// Een token voor één cluster ziet alleen dat cluster.
	var webID string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM clusters WHERE slug = 'web'`).Scan(&webID); err != nil {
		t.Fatal(err)
	}
	var scoped struct {
		Token string `json:"token"`
	}
	if s := admin.do("POST", "/api/v1/api-tokens", map[string]any{"name": "alleen web", "cluster_ids": []string{webID}, "ttl_days": 30}, &scoped); s != 201 {
		t.Fatalf("token voor web: %d", s)
	}
	var some []dashCluster
	if dashboardGet(t, e, "/api/dashboard/v1/clusters", "Authorization", "Bearer "+scoped.Token, &some); len(some) != 1 || some[0].Slug != "web" {
		t.Fatalf("token voor web: %+v", some)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters/db", "Authorization", "Bearer "+scoped.Token, nil); s != 404 {
		t.Fatalf("token voor web ziet db: %d", s)
	}

	// Het beveiligingsoverzicht (fase 25): alleen met een token voor alle clusters.
	var sec struct {
		OK         bool   `json:"ok"`
		Level      string `json:"level"`
		LevelLabel string `json:"level_label"`
		Score      int    `json:"score"`
		Protected  string `json:"protected"`
		URL        string `json:"url"`
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/soc", "Authorization", "Bearer "+created.Token, &sec); s != 200 ||
		sec.LevelLabel == "" || !strings.HasSuffix(sec.URL, "/soc") || sec.Protected == "" {
		t.Fatalf("soc: %d %+v", s, sec)
	}
	// Het open incident van db maakt het niveau verhoogd: met strict een 503.
	if s := dashboardGet(t, e, "/api/dashboard/v1/soc?strict=1", "Authorization", "Bearer "+created.Token, nil); sec.OK || s != 503 {
		t.Fatalf("soc strict: %d, niveau %s", s, sec.Level)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/soc", "Authorization", "Bearer "+scoped.Token, nil); s != 403 {
		t.Fatalf("soc met een token voor één cluster: %d", s)
	}

	// De lijst toont geen token, wel het laatste gebruik; het voorbeeld werkt voor een viewer.
	var list struct {
		Items []struct {
			Name         string   `json:"name"`
			LastUsedAt   *string  `json:"last_used_at"`
			ClusterNames []string `json:"cluster_names"`
		} `json:"items"`
		BasePath string `json:"base_path"`
	}
	if s := admin.do("GET", "/api/v1/api-tokens", nil, &list); s != 200 || len(list.Items) != 2 || list.Items[0].LastUsedAt == nil ||
		list.BasePath != "/api/dashboard/v1" || list.Items[1].ClusterNames[0] != "Web" {
		t.Fatalf("lijst: %d %+v", s, list)
	}
	var preview struct {
		ClustersTotal int `json:"clusters_total"`
	}
	if s := viewer.do("GET", "/api/v1/api-tokens/preview", nil, &preview); s != 200 || preview.ClustersTotal != 2 {
		t.Fatalf("voorbeeld: %d %+v", s, preview)
	}

	// Intrekken: het token werkt meteen niet meer.
	if s := admin.do("DELETE", "/api/v1/api-tokens/"+created.Item.ID, nil, nil); s != 204 {
		t.Fatalf("intrekken: %d", s)
	}
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters", "Authorization", "Bearer "+created.Token, nil); s != 401 {
		t.Fatalf("na intrekken: %d", s)
	}
	// Verlopen tokens werken niet.
	exec(`UPDATE api_tokens SET expires_at = now() - interval '1 minute'`)
	if s := dashboardGet(t, e, "/api/dashboard/v1/clusters", "Authorization", "Bearer "+scoped.Token, nil); s != 401 {
		t.Fatalf("verlopen: %d", s)
	}
}
