package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
)

// statusEvent is een service.status_changed uit de database.
type statusEvent struct {
	Name         string   `json:"name"`
	Scope        string   `json:"scope"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	ImpactFrom   string   `json:"impact_from"`
	Impact       string   `json:"impact"`
	ImpactReason string   `json:"impact_reason"`
	Cause        string   `json:"cause"`
	Path         []string `json:"path"`
}

func (e *testEnv) statusEvents(t *testing.T) []statusEvent {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT payload FROM events WHERE action = 'service.status_changed' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []statusEvent
	for rows.Next() {
		var b []byte
		var ev statusEvent
		if err := rows.Scan(&b); err != nil || json.Unmarshal(b, &ev) != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

// byService telt de events per dienst: "web-prod/nginx".
func byService(evs []statusEvent) map[string][]statusEvent {
	out := map[string][]statusEvent{}
	for _, ev := range evs {
		out[ev.Scope+"/"+ev.Name] = append(out[ev.Scope+"/"+ev.Name], ev)
	}
	return out
}

type clusterImpact struct {
	Name       string   `json:"name"`
	Impact     string   `json:"impact"`
	ImpactedBy []string `json:"impacted_by"`
}

func (c *client) clusterImpacts() map[string]clusterImpact {
	c.t.Helper()
	var l struct {
		Items []clusterImpact `json:"items"`
	}
	c.do("GET", "/api/v1/clusters", nil, &l)
	out := map[string]clusterImpact{}
	for _, it := range l.Items {
		out[it.Name] = it
	}
	return out
}

// TestImpact is de acceptatie van mijlpaal 12: valt db-prod uit, dan komt er
// per geraakte dienst precies één service.status_changed met oorzaak en pad,
// en bij herstel nog één. Het venster voor onderhoud van db01 toont welke
// diensten down raken, de clusterlijst zegt "geraakt door db-prod", en wie
// db-prod verwijdert, ziet de verdwenen afhankelijkheden in het logboek.
func TestImpact(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var db, web, app cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db-prod", "name": "db-prod", "type": "mariadb_ha", "environment": "prod"}, &db)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web-prod", "name": "web-prod", "type": "nginx", "environment": "prod"}, &web)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "app-test", "name": "app-test", "type": "generic", "environment": "test"}, &app)
	sims := map[string]*unitSim{
		"db01":  newUnitSim("mariadb=active"),
		"db02":  newUnitSim("mariadb=active"),
		"web01": newUnitSim("nginx=active", "php8.2-fpm=active"),
		"app01": newUnitSim("cron=active"),
	}
	clusterOf := map[string]string{"db01": db.ID, "db02": db.ID, "web01": web.ID, "app01": app.ID}
	nodeID := map[string]string{}
	for i, host := range []string{"db01", "db02", "web01", "app01"} {
		var n node
		c.do("POST", "/api/v1/nodes", map[string]any{"hostname": host, "cluster_id": clusterOf[host]}, &n)
		sim := sims[host]
		startAgentWith(t, e, c, n.ID, fmt.Sprintf("%032x", i+1), &addrs{}, func(ag *agent.Agent, _ string) {
			ag.Collector.Run = sim.run
		})
		nodeID[host] = n.ID
	}
	eventually(t, "de units in de heartbeats", func() bool {
		var n int
		err := e.pool.QueryRow(ctx, `SELECT count(*) FROM node_status WHERE services ? 'mariadb' OR services ? 'php8.2-fpm'`).Scan(&n)
		return err == nil && n == 3
	})

	svc := func(clusterID, name, kind, unit string) string {
		var sv depSvcView
		body := map[string]any{"cluster_id": clusterID, "name": name, "kind": kind}
		if unit != "" {
			body["unit"] = unit
		}
		if s := c.do("POST", "/api/v1/services", body, &sv); s != 201 {
			t.Fatalf("%s: %d", name, s)
		}
		return sv.ID
	}
	mariadb := svc(db.ID, "mariadb", "database", "mariadb")
	php := svc(web.ID, "php8.2-fpm", "app", "php8.2-fpm")
	nginx := svc(web.ID, "nginx", "web", "nginx")
	appSvc := svc(app.ID, "app", "app", "")
	for _, l := range [][3]string{{php, mariadb, "hard"}, {nginx, php, "hard"}, {appSvc, mariadb, "soft"}} {
		if s := c.do("POST", "/api/v1/dependencies", map[string]any{"from_service_id": l[0], "to_service_id": l[1], "strength": l[2]}, nil); s != 201 {
			t.Fatalf("pijl: %d", s)
		}
	}

	// De eerste berekening van een dienst geeft geen event.
	eventually(t, "de status van elke dienst opgeslagen", func() bool {
		var n int
		err := e.pool.QueryRow(ctx, `SELECT count(*) FROM services WHERE status IS NULL`).Scan(&n)
		return err == nil && n == 0
	})
	var stored string
	if err := e.pool.QueryRow(ctx, `SELECT status FROM services WHERE id = $1`, mariadb).Scan(&stored); err != nil || stored != "healthy" {
		t.Fatalf("mariadb opgeslagen als %q: %v", stored, err)
	}
	if evs := e.statusEvents(t); len(evs) != 0 {
		t.Fatalf("events bij de eerste berekening: %+v", evs)
	}
	if im := c.clusterImpacts(); im["web-prod"].Impact != "none" || len(im["web-prod"].ImpactedBy) != 0 {
		t.Fatalf("web-prod zonder uitval: %+v", im["web-prod"])
	}

	// Het venster voor onderhoud van db01: met mariadb nog op db02 raakt dat
	// alleen mariadb zelf.
	var imp impactView
	c.do("GET", "/api/v1/impact?node_id="+nodeID["db01"], nil, &imp)
	if imp.lines() != "db-prod mariadb degraded: draait nog op db02" {
		t.Fatalf("onderhoud van db01 met db02:\n%s", imp.lines())
	}
	// Staat mariadb op db02 stil, dan raakt onderhoud van db01 web-prod.
	sims["db02"].set("mariadb", "inactive")
	eventually(t, "mariadb verminderd", func() bool {
		evs := byService(e.statusEvents(t))["db-prod/mariadb"]
		return len(evs) == 1 && evs[0].To == "degraded"
	})
	c.do("GET", "/api/v1/impact?node_id="+nodeID["db01"], nil, &imp)
	want := strings.Join([]string{
		"db-prod mariadb down: mariadb draait op geen andere node",
		"web-prod nginx down: via php8.2-fpm",
		"web-prod php8.2-fpm down: hangt hard af van mariadb in db-prod · handmatig",
		"app-test app degraded: hangt zacht af van mariadb in db-prod · handmatig",
	}, "\n")
	if imp.lines() != want {
		t.Fatalf("onderhoud van db01 zonder db02:\n%s", imp.lines())
	}
	if n := len(byService(e.statusEvents(t))["web-prod/php8.2-fpm"]); n != 0 {
		t.Fatalf("php8.2-fpm kreeg al %d events terwijl mariadb nog draait", n)
	}

	// db-prod valt uit: per geraakte dienst precies één event.
	sims["db01"].set("mariadb", "failed")
	affected := []string{"web-prod/php8.2-fpm", "web-prod/nginx", "app-test/app"}
	eventually(t, "de geraakte diensten", func() bool {
		got := byService(e.statusEvents(t))
		for _, k := range affected {
			if len(got[k]) == 0 {
				return false
			}
		}
		return true
	})
	// Een paar rondes later is er niets bijgekomen.
	time.Sleep(time.Second)
	got := byService(e.statusEvents(t))
	for _, k := range affected {
		if len(got[k]) != 1 {
			t.Fatalf("%s: %d events: %+v", k, len(got[k]), got[k])
		}
	}
	p, n, a := got["web-prod/php8.2-fpm"][0], got["web-prod/nginx"][0], got["app-test/app"][0]
	if p.ImpactFrom != "none" || p.Impact != "down" || p.Cause != "mariadb in db-prod" || p.ImpactReason != "mariadb in db-prod is down" ||
		!slices.Equal(p.Path, []string{"mariadb in db-prod", "php8.2-fpm"}) || p.From != "healthy" || p.To != "healthy" {
		t.Fatalf("php8.2-fpm: %+v", p)
	}
	if n.Impact != "down" || n.ImpactReason != "mariadb in db-prod is down, via php8.2-fpm" ||
		!slices.Equal(n.Path, []string{"mariadb in db-prod", "php8.2-fpm", "nginx"}) {
		t.Fatalf("nginx: %+v", n)
	}
	if a.Impact != "degraded" || a.Cause != "mariadb in db-prod" {
		t.Fatalf("app: %+v", a)
	}
	if evs := got["db-prod/mariadb"]; len(evs) != 2 || evs[1].From != "degraded" || evs[1].To != "down" || evs[1].Impact != "none" {
		t.Fatalf("mariadb: %+v", evs)
	}
	im := c.clusterImpacts()
	if im["web-prod"].Impact != "down" || !slices.Equal(im["web-prod"].ImpactedBy, []string{"db-prod"}) ||
		im["app-test"].Impact != "degraded" || im["db-prod"].Impact != "none" {
		t.Fatalf("clusterlijst: %+v", im)
	}
	page := c.audit("category=dependencies&limit=100")
	var sentences []string
	for _, it := range page.Items {
		if it.Action == "service.status_changed" {
			sentences = append(sentences, it.Summary)
		}
	}
	for _, s := range []string{
		"nginx in web-prod down door een afhankelijkheid: mariadb in db-prod is down, via php8.2-fpm",
		"app in app-test verminderd door een afhankelijkheid: mariadb in db-prod is down",
		"mariadb in db-prod van verminderd naar down: mariadb draait op geen enkele node (db01: failed, db02: inactive)",
	} {
		if !slices.Contains(sentences, s) {
			t.Fatalf("zin %q ontbreekt in:\n%s", s, strings.Join(sentences, "\n"))
		}
	}

	// Herstel: per geraakte dienst nog één event.
	sims["db01"].set("mariadb", "active")
	sims["db02"].set("mariadb", "active")
	eventually(t, "herstel", func() bool {
		got := byService(e.statusEvents(t))
		for _, k := range affected {
			if len(got[k]) < 2 {
				return false
			}
		}
		return true
	})
	time.Sleep(time.Second)
	got = byService(e.statusEvents(t))
	for _, k := range affected {
		if evs := got[k]; len(evs) != 2 || evs[1].Impact != "none" || evs[1].Cause != "" || len(evs[1].Path) != 0 {
			t.Fatalf("%s na herstel: %+v", k, evs)
		}
	}
	if im := c.clusterImpacts(); im["web-prod"].Impact != "none" || len(im["web-prod"].ImpactedBy) != 0 {
		t.Fatalf("web-prod na herstel: %+v", im["web-prod"])
	}

	// db-prod verwijderen: de afhankelijkheden van buiten staan in het event.
	if s := c.do("DELETE", "/api/v1/clusters/"+db.ID, nil, nil); s != 204 {
		t.Fatalf("db-prod verwijderen: %d", s)
	}
	del := find(t, c.audit("limit=20").Items, "cluster.deleted")
	if del.Summary != "Cluster db-prod verwijderd; daarmee verdwenen de afhankelijkheden van app in app-test en php8.2-fpm in web-prod" {
		t.Fatalf("cluster.deleted: %q", del.Summary)
	}
	used, _ := del.Payload["used_by"].([]any)
	if len(used) != 2 {
		t.Fatalf("used_by: %+v", del.Payload)
	}
}
