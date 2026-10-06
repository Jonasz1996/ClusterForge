package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// unitSim speelt systemctl show voor de collector van één node.
type unitSim struct {
	mu    sync.Mutex
	units map[string]string
}

func newUnitSim(units ...string) *unitSim {
	u := &unitSim{units: map[string]string{}}
	for _, kv := range units {
		k, v, _ := strings.Cut(kv, "=")
		u.units[k] = v
	}
	return u
}

func (u *unitSim) set(unit, state string) {
	u.mu.Lock()
	u.units[unit] = state
	u.mu.Unlock()
}

func (u *unitSim) run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "systemctl" || len(args) == 0 || args[0] != "show" {
		return nil, errors.New("geen commando's in tests")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	var b strings.Builder
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--") {
			continue
		}
		for unit, st := range u.units {
			if ok, _ := path.Match(a, unit+".service"); ok {
				b.WriteString("Id=" + unit + ".service\nLoadState=loaded\nActiveState=" + st + "\nUnitFileState=enabled\n\n")
			}
		}
	}
	return []byte(b.String()), nil
}

type depSvcView struct {
	ID           string   `json:"id"`
	GroupID      string   `json:"group_id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Unit         string   `json:"unit"`
	Port         *int     `json:"port"`
	Source       string   `json:"source"`
	State        string   `json:"state"`
	Status       string   `json:"status"`
	StatusReason string   `json:"status_reason"`
	Impact       string   `json:"impact"`
	ImpactReason string   `json:"impact_reason"`
	ImpactPath   []string `json:"impact_path"`
	Instances    []struct {
		Hostname string `json:"hostname"`
		State    string `json:"state"`
		Running  bool   `json:"running"`
	} `json:"instances"`
}

type depGraph struct {
	Level  string `json:"level"`
	Groups []struct {
		ID          string   `json:"id"`
		Kind        string   `json:"kind"`
		Name        string   `json:"name"`
		Status      string   `json:"status"`
		Impact      string   `json:"impact"`
		ImpactedBy  []string `json:"impacted_by"`
		Suggestions int      `json:"suggestions"`
	} `json:"groups"`
	Services []depSvcView `json:"services"`
	Edges    []struct {
		ID       string `json:"id"`
		From     string `json:"from"`
		To       string `json:"to"`
		Strength string `json:"strength"`
		Source   string `json:"source"`
		Affected bool   `json:"affected"`
		Count    int    `json:"count"`
	} `json:"edges"`
	Suggestions int `json:"suggestions"`
}

func (g depGraph) svc(t *testing.T, group, name string) depSvcView {
	t.Helper()
	for _, s := range g.Services {
		if s.Name == name && g.groupName(s.GroupID) == group {
			return s
		}
	}
	t.Fatalf("dienst %s in %s ontbreekt: %+v", name, group, g.Services)
	return depSvcView{}
}

func (g depGraph) groupName(id string) string {
	for _, gr := range g.Groups {
		if gr.ID == id {
			return gr.Name
		}
	}
	return ""
}

func (g depGraph) names() []string {
	var out []string
	for _, s := range g.Services {
		out = append(out, g.groupName(s.GroupID)+"/"+s.Name)
	}
	slices.Sort(out)
	return out
}

type impactView struct {
	Target struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"target"`
	Items []struct {
		Name      string   `json:"name"`
		GroupName string   `json:"group_name"`
		Impact    string   `json:"impact"`
		Direct    bool     `json:"direct"`
		Reason    string   `json:"reason"`
		Path      []string `json:"path"`
	} `json:"items"`
	Groups []struct {
		Name   string `json:"name"`
		Impact string `json:"impact"`
		Count  int    `json:"count"`
	} `json:"groups"`
}

func (v impactView) lines() string {
	var out []string
	for _, it := range v.Items {
		out = append(out, it.GroupName+" "+it.Name+" "+it.Impact+": "+it.Reason)
	}
	return strings.Join(out, "\n")
}

// TestDependencies is de acceptatie van mijlpaal 9: Jonas koppelt web-prod
// aan mariadb in db-prod, impact op mariadb noemt web-prod met het pad, en
// stopt mariadb op alle db-nodes, dan wordt de dienst rood en krijgt
// web-prod een rode rand.
func TestDependencies(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var db, web, app cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db-prod", "name": "db-prod", "type": "mariadb_ha", "environment": "prod"}, &db)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web-prod", "name": "web-prod", "type": "nginx", "environment": "prod"}, &web)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "app-test", "name": "app-test", "type": "generic", "environment": "test"}, &app)
	sims := map[string]*unitSim{
		"db01":  newUnitSim("mariadb=active", "ssh=active", "cf-agent=active"),
		"db02":  newUnitSim("mariadb=active", "ssh=active", "cf-agent=active"),
		"web01": newUnitSim("nginx=active", "php8.2-fpm=active", "redis-server=active", "qemu-guest-agent=active"),
		"app01": newUnitSim("cron=active"),
	}
	clusterOf := map[string]string{"db01": db.ID, "db02": db.ID, "web01": web.ID, "app01": app.ID}
	var ids []string
	for i, host := range []string{"db01", "db02", "web01", "app01"} {
		var n node
		c.do("POST", "/api/v1/nodes", map[string]any{"hostname": host, "cluster_id": clusterOf[host]}, &n)
		sim := sims[host]
		startAgentWith(t, e, c, n.ID, fmt.Sprintf("%032x", i+1), &addrs{}, func(ag *agent.Agent, _ string) {
			ag.Collector.Run = sim.run
		})
		ids = append(ids, n.ID)
	}
	eventually(t, "de units in de heartbeats", func() bool {
		var n int
		err := e.pool.QueryRow(ctx, `SELECT count(*) FROM node_status WHERE services ? 'mariadb' OR services ? 'php8.2-fpm'`).Scan(&n)
		return err == nil && n == 3
	})

	// Voorstellen uit de facts: bekende units, geen infrastructuur, en cron
	// alleen in een cluster van type cron.
	if err := e.deps.Resolve(ctx); err != nil {
		t.Fatal(err)
	}
	var g depGraph
	if s := c.do("GET", "/api/v1/dependency-graph", nil, &g); s != 200 || len(g.Services) != 0 || g.Suggestions != 4 {
		t.Fatalf("zonder voorstellen: %d %+v", s, g)
	}
	c.do("GET", "/api/v1/dependency-graph?include_suggested=true", nil, &g)
	if got := strings.Join(g.names(), " "); got != "db-prod/mariadb web-prod/nginx web-prod/php8.2-fpm web-prod/redis-server" {
		t.Fatalf("voorstellen: %s", got)
	}
	if s := g.svc(t, "web-prod", "redis-server"); s.Kind != "cache" || s.Source != "discovered" || s.State != "suggested" || s.Status != "healthy" {
		t.Fatalf("redis-server: %+v", s)
	}

	// Een viewer ziet de graaf maar wijzigt niets.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	viewer.login("kijker", "een-lang-wachtwoord", "")
	if s := viewer.do("GET", "/api/v1/dependency-graph?include_suggested=true", nil, &depGraph{}); s != 200 {
		t.Fatalf("viewer leest: %d", s)
	}
	mariadb := g.svc(t, "db-prod", "mariadb")
	if s := viewer.do("PATCH", "/api/v1/services/"+mariadb.ID, map[string]any{"state": "confirmed"}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer bevestigt: %d", s)
	}
	if s := viewer.do("POST", "/api/v1/services", map[string]any{"name": "x", "kind": "app", "cluster_id": app.ID}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer maakt aan: %d", s)
	}

	// Bevestigen en negeren; een nieuwe ronde verandert geen staat.
	for _, name := range []string{"nginx", "php8.2-fpm"} {
		if s := c.do("PATCH", "/api/v1/services/"+g.svc(t, "web-prod", name).ID, map[string]any{"state": "confirmed"}, nil); s != 200 {
			t.Fatalf("%s bevestigen: %d", name, s)
		}
	}
	var sv depSvcView
	if s := c.do("PATCH", "/api/v1/services/"+mariadb.ID, map[string]any{"state": "confirmed", "port": 3306}, &sv); s != 200 || sv.State != "confirmed" || sv.Port == nil || *sv.Port != 3306 {
		t.Fatalf("mariadb bevestigen: %d %+v", s, sv)
	}
	redis := g.svc(t, "web-prod", "redis-server")
	c.do("PATCH", "/api/v1/services/"+redis.ID, map[string]any{"state": "ignored"}, nil)
	if err := e.deps.Resolve(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/dependency-graph?include_suggested=true&include_ignored=true", nil, &g)
	if g.svc(t, "web-prod", "redis-server").State != "ignored" || g.Suggestions != 0 || len(g.Services) != 4 {
		t.Fatalf("na de tweede ronde: %+v", g)
	}

	// Met de hand: een dienst zonder unit en een externe NFS-server.
	var appSvc, nfs depSvcView
	if s := c.do("POST", "/api/v1/services", map[string]any{"cluster_id": app.ID, "name": "app", "kind": "app"}, &appSvc); s != 201 || appSvc.Status != "unknown" {
		t.Fatalf("app: %d %+v", s, appSvc)
	}
	if s := c.do("POST", "/api/v1/services", map[string]any{"name": "nfs", "kind": "storage", "address": "10.0.0.60", "port": 2049}, &nfs); s != 201 || nfs.Source != "manual" {
		t.Fatalf("nfs: %d %+v", s, nfs)
	}
	var er deployErr
	for what, tc := range map[string]struct {
		body   map[string]any
		status int
		field  string
	}{
		"zelfde adres":     {map[string]any{"name": "nfs2", "kind": "storage", "address": "10.0.0.60", "port": 2049}, 409, ""},
		"zelfde naam":      {map[string]any{"cluster_id": web.ID, "name": "NGINX", "kind": "web"}, 409, ""},
		"extern met unit":  {map[string]any{"name": "x", "kind": "storage", "address": "10.0.0.61", "port": 22, "unit": "nfs"}, 400, "unit"},
		"extern zonder":    {map[string]any{"name": "x", "kind": "storage", "address": "10.0.0.61"}, 400, "port"},
		"adres in cluster": {map[string]any{"cluster_id": web.ID, "name": "x", "kind": "web", "address": "10.0.0.61"}, 400, "address"},
		"ongeldige naam":   {map[string]any{"cluster_id": web.ID, "name": "a b", "kind": "web"}, 400, "name"},
		"onbekende soort":  {map[string]any{"cluster_id": web.ID, "name": "x", "kind": "magie"}, 400, "kind"},
		"node in cluster":  {map[string]any{"node_id": ids[0], "name": "x", "kind": "app"}, 400, "node_id"},
	} {
		er = deployErr{}
		if s := c.do("POST", "/api/v1/services", tc.body, &er); s != tc.status || (tc.field != "" && (er.Field == nil || *er.Field != tc.field)) {
			t.Errorf("%s: %d %+v", what, s, er)
		}
	}

	// De pijlen.
	php := g.svc(t, "web-prod", "php8.2-fpm")
	nginx := g.svc(t, "web-prod", "nginx")
	link := func(from, to, strength string) int {
		return c.do("POST", "/api/v1/dependencies", map[string]any{"from_service_id": from, "to_service_id": to, "strength": strength, "note": "handmatig"}, nil)
	}
	for _, l := range [][3]string{{php.ID, mariadb.ID, "hard"}, {nginx.ID, php.ID, "hard"}, {appSvc.ID, mariadb.ID, "soft"}, {mariadb.ID, nfs.ID, "soft"}} {
		if s := link(l[0], l[1], l[2]); s != 201 {
			t.Fatalf("pijl: %d", s)
		}
	}
	if s := link(php.ID, mariadb.ID, "soft"); s != 409 {
		t.Fatalf("dubbele pijl: %d", s)
	}
	if s := link(php.ID, php.ID, "hard"); s != 400 {
		t.Fatalf("pijl naar zichzelf: %d", s)
	}
	if s := link(php.ID, redis.ID, "hard"); s != 400 {
		t.Fatalf("pijl naar een genegeerde dienst: %d", s)
	}

	// Wat raakt uitval van mariadb? Prod bovenaan, met het pad.
	var imp impactView
	if s := c.do("GET", "/api/v1/impact?service_id="+mariadb.ID, nil, &imp); s != 200 || imp.Target.Name != "mariadb in db-prod" {
		t.Fatalf("impact: %d %+v", s, imp)
	}
	want := strings.Join([]string{
		"web-prod nginx down: via php8.2-fpm",
		"web-prod php8.2-fpm down: hangt hard af van mariadb in db-prod · handmatig",
		"app-test app degraded: hangt zacht af van mariadb in db-prod · handmatig",
	}, "\n")
	if imp.lines() != want || strings.Join(imp.Items[0].Path, " > ") != "mariadb in db-prod > php8.2-fpm > nginx" ||
		len(imp.Groups) != 2 || imp.Groups[0].Name != "web-prod" || imp.Groups[0].Count != 2 {
		t.Fatalf("impact van mariadb:\n%s\n%+v", imp.lines(), imp)
	}
	if s := c.do("GET", "/api/v1/impact?service_id="+mariadb.ID+"&node_id="+ids[0], nil, nil); s != 400 {
		t.Fatalf("twee doelen: %d", s)
	}
	// Valt db01 uit, dan draait mariadb nog op db02.
	c.do("GET", "/api/v1/impact?node_id="+ids[0], nil, &imp)
	if imp.lines() != "db-prod mariadb degraded: draait nog op db02" {
		t.Fatalf("impact van db01:\n%s", imp.lines())
	}

	// mariadb stopt op alle db-nodes: rood, en web-prod krijgt een rode rand.
	sims["db01"].set("mariadb", "failed")
	sims["db02"].set("mariadb", "inactive")
	eventually(t, "mariadb down in de graaf", func() bool {
		c.do("GET", "/api/v1/dependency-graph", nil, &g)
		return g.svc(t, "db-prod", "mariadb").Status == "down"
	})
	if s := g.svc(t, "db-prod", "mariadb"); s.StatusReason != "mariadb draait op geen enkele node (db01: failed, db02: inactive)" {
		t.Fatalf("mariadb: %+v", s)
	}
	n, p, a := g.svc(t, "web-prod", "nginx"), g.svc(t, "web-prod", "php8.2-fpm"), g.svc(t, "app-test", "app")
	if p.Impact != "down" || p.ImpactReason != "mariadb in db-prod is down" || n.Impact != "down" ||
		n.ImpactReason != "mariadb in db-prod is down, via php8.2-fpm" || a.Impact != "degraded" || n.Status != "healthy" {
		t.Fatalf("doorgegeven: nginx %+v php %+v app %+v", n, p, a)
	}
	for _, gr := range g.Groups {
		switch gr.Name {
		case "web-prod":
			if gr.Impact != "down" || !slices.Equal(gr.ImpactedBy, []string{"db-prod"}) {
				t.Fatalf("web-prod: %+v", gr)
			}
		case "db-prod", "Extern":
			if gr.Impact != "none" {
				t.Fatalf("%s: %+v", gr.Name, gr)
			}
		}
	}
	var affected int
	for _, ed := range g.Edges {
		if ed.Affected {
			affected++
		}
	}
	if affected != 3 {
		t.Fatalf("geraakte pijlen: %d, wil php→mariadb, nginx→php en app→mariadb", affected)
	}

	// Alleen clusters: samengevoegde pijlen tussen groepen.
	c.do("GET", "/api/v1/dependency-graph?level=cluster", nil, &g)
	if len(g.Services) != 0 || len(g.Edges) != 3 {
		t.Fatalf("alleen clusters: %+v", g)
	}
	for _, ed := range g.Edges {
		from, to := g.groupName(ed.From), g.groupName(ed.To)
		if (from == "web-prod" && (to != "db-prod" || ed.Strength != "hard" || !ed.Affected)) || (from == "app-test" && ed.Strength != "soft") {
			t.Fatalf("pijl %s → %s: %+v", from, to, ed)
		}
	}
	// Filter op cluster: dat cluster plus zijn directe buren.
	c.do("GET", "/api/v1/dependency-graph?cluster_id="+web.ID, nil, &g)
	if got := strings.Join(g.names(), " "); got != "db-prod/mariadb web-prod/nginx web-prod/php8.2-fpm" {
		t.Fatalf("web-prod met buren: %s", got)
	}
	c.do("GET", "/api/v1/dependency-graph?environment=test", nil, &g)
	if got := strings.Join(g.names(), " "); got != "app-test/app db-prod/mariadb" {
		t.Fatalf("test met buren: %s", got)
	}

	// Verwijderen: nginx heeft een unit en blijft als genegeerde rij staan.
	if s := c.do("DELETE", "/api/v1/services/"+nginx.ID, nil, nil); s != 204 {
		t.Fatalf("nginx verwijderen: %d", s)
	}
	if err := e.deps.Resolve(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/dependency-graph?include_suggested=true&include_ignored=true&cluster_id="+web.ID, nil, &g)
	if s := g.svc(t, "web-prod", "nginx"); s.State != "ignored" || g.Suggestions != 0 {
		t.Fatalf("nginx na verwijderen: %+v %d", s, g.Suggestions)
	}
	// Wie hem opnieuw aanmaakt, krijgt dezelfde rij terug.
	if s := c.do("POST", "/api/v1/services", map[string]any{"cluster_id": web.ID, "name": "nginx", "kind": "web", "unit": "nginx.service", "port": 80}, &sv); s != 201 ||
		sv.ID != nginx.ID || sv.State != "confirmed" || sv.Source != "manual" || sv.Unit != "nginx" {
		t.Fatalf("nginx opnieuw: %d %+v", s, sv)
	}
	// Een dienst zonder unit verdwijnt echt, met zijn pijlen.
	if s := c.do("DELETE", "/api/v1/services/"+appSvc.ID, nil, nil); s != 204 {
		t.Fatalf("app verwijderen: %d", s)
	}
	if s := c.do("PATCH", "/api/v1/services/"+appSvc.ID, map[string]any{"name": "x"}, nil); s != 404 {
		t.Fatalf("app na verwijderen: %d", s)
	}

	// Elke wijziging staat in het logboek, onder Afhankelijkheden.
	page := c.audit("category=dependencies&limit=100")
	got := map[string]int{}
	for _, it := range page.Items {
		got[it.Action]++
	}
	// 4 voorstellen, app, nfs en nginx opnieuw; 4 pijlen.
	if got["service.created"] != 7 || got["service.updated"] != 4 || got["service.deleted"] != 2 || got["dependency.created"] != 4 {
		t.Fatalf("events: %v", got)
	}
	del := find(t, page.Items, "service.deleted")
	if del.Summary != "Dienst app in app-test verwijderd met 1 afhankelijkheid" {
		t.Fatalf("zin: %q", del.Summary)
	}
}
