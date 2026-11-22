package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type riskFinding struct {
	Check    string `json:"check"`
	Card     string `json:"card"`
	Severity string `json:"severity"`
	Subject  struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"subject"`
	Text      string  `json:"text"`
	Link      string  `json:"link"`
	Points    int     `json:"points"`
	Gain      float64 `json:"gain"`
	FleetGain float64 `json:"fleet_gain"`
	Accepted  *struct {
		ID        string     `json:"id"`
		Reason    string     `json:"reason"`
		ExpiresAt *time.Time `json:"expires_at"`
		By        string     `json:"by"`
	} `json:"accepted"`
	Cluster *struct {
		Name string `json:"name"`
	} `json:"cluster"`
}

type riskCard struct {
	ID       string        `json:"id"`
	Weight   int           `json:"weight"`
	Score    *int          `json:"score"`
	Note     string        `json:"note"`
	Findings []riskFinding `json:"findings"`
}

type clusterRiskView struct {
	Score *int   `json:"score"`
	Grade string `json:"grade"`
	Cap   *struct {
		Max int `json:"max"`
	} `json:"cap"`
	Cards      []riskCard `json:"cards"`
	ComputedAt *time.Time `json:"computed_at"`
}

func (v clusterRiskView) card(t *testing.T, id string) riskCard {
	t.Helper()
	i := slices.IndexFunc(v.Cards, func(c riskCard) bool { return c.ID == id })
	if i < 0 {
		t.Fatalf("geen kaart %s: %+v", id, v.Cards)
	}
	return v.Cards[i]
}

func (v clusterRiskView) findings(check string) []riskFinding {
	var out []riskFinding
	for _, c := range v.Cards {
		for _, f := range c.Findings {
			if f.Check == check {
				out = append(out, f)
			}
		}
	}
	return out
}

type riskOverview struct {
	Score      *int          `json:"score"`
	Grade      string        `json:"grade"`
	ComputedAt *time.Time    `json:"computed_at"`
	Platform   *riskCard     `json:"platform"`
	Findings   []riskFinding `json:"findings"`
	Clusters   []struct {
		Name  string `json:"name"`
		Score *int   `json:"score"`
		Grade string `json:"grade"`
	} `json:"clusters"`
}

// TestHealthScore is het voorbeeld uit het ontwerp: beide web-nodes op pve1,
// back-ups van 40 uur en een failovertest van 41 dagen geleden.
func TestHealthScore(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}

	pve, srv, fp := newPVE(t)
	now := time.Now().Truncate(time.Second)
	pve.SetGuestStatus(102, "running", "pve1")
	pve.AddStorage(pvefake.Storage{Name: "pbs", Node: "pve1", Shared: true, Content: "backup"})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: 101, Time: now.Add(-40 * time.Hour), Size: 1 << 30})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: 102, Time: now.Add(-40 * time.Hour), Size: 1 << 30})
	pve.SetBackupJobs(101, 102)
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != http.StatusCreated {
		t.Fatalf("koppelen: %d", s)
	}
	var cl struct{ ID string }
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	for _, n := range []struct {
		name string
		vmid int
	}{{"web01", 101}, {"web02", 102}} {
		if s := c.do("POST", "/api/v1/nodes", map[string]any{
			"hostname": n.name, "cluster_id": cl.ID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": n.vmid},
		}, nil); s != http.StatusCreated {
			t.Fatalf("node %s: %d", n.name, s)
		}
	}
	var vip struct{ ID string }
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.0.10"}, &vip)
	var testID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO failover_tests (cluster_id, vip_id, name, scenario, max_takeover_seconds, expect_failback, probe)
		VALUES ($1, $2, 'Stop web01', 'stop_service', 10, true, '{}') RETURNING id`, cl.ID, vip.ID).Scan(&testID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO test_runs (kind, cluster_id, test_id, definition, result, restored, created_at, finished_at)
		VALUES ('failover.test', $1, $2, '{}', 'pass', true, $3, $3)`, cl.ID, testID, now.Add(-41*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != http.StatusOK {
		t.Fatalf("sync: %d", s)
	}
	if err := e.backups.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}

	// Voor de eerste berekening is er nog niets, maar wel een antwoord.
	var v clusterRiskView
	if s := c.do("GET", "/api/v1/clusters/"+cl.ID+"/risk", nil, &v); s != http.StatusOK || v.ComputedAt != nil || v.Score != nil || v.Cards == nil {
		t.Fatalf("voor de berekening: %d %+v", s, v)
	}
	e.risk.Warmup = 0
	compute := func() {
		t.Helper()
		if err := e.risk.Compute(ctx); err != nil {
			t.Fatal(err)
		}
	}
	compute()
	v = clusterRiskView{}
	c.do("GET", "/api/v1/clusters/"+cl.ID+"/risk", nil, &v)
	if v.ComputedAt == nil || v.Score == nil || v.Grade == "" || len(v.Cards) != 7 {
		t.Fatalf("na de berekening: %+v", v)
	}

	// De drie bevindingen uit het voorbeeld, met de ernst voor prod.
	same := v.findings("redundancy.same_host")
	if len(same) != 1 || same[0].Severity != "high" || same[0].Points != 20 ||
		!strings.Contains(same[0].Text, "web01 en web02 draaien allebei op pve1") || same[0].Link != "/clusters/detail?id="+cl.ID {
		t.Fatalf("zelfde host: %+v", same)
	}
	stale := v.findings("backups.stale")
	if len(stale) != 2 || stale[0].Severity != "high" || stale[0].Points != 20 ||
		!strings.Contains(stale[0].Text, "is 40 u oud; het beleid is 30 uur") || stale[0].Link != "/back-ups" {
		t.Fatalf("oude back-ups: %+v", stale)
	}
	old := v.findings("recovery.stale")
	if len(old) != 1 || old[0].Severity != "medium" || old[0].Points != 10 ||
		old[0].Text != "Laatste failovertest 'Stop web01' is 41 dagen geleden" || old[0].Gain <= 0 {
		t.Fatalf("oude failovertest: %+v", old)
	}
	if rc := v.card(t, "recovery"); rc.Score == nil || *rc.Score != 90 {
		t.Fatalf("kaart herstelbaarheid: %+v", rc)
	}
	// Zonder status, facts en metrics zijn die kaarten onbekend, met uitleg.
	for _, id := range []string{"availability", "capacity"} {
		if k := v.card(t, id); k.Score != nil || k.Note == "" {
			t.Fatalf("kaart %s zou onbekend moeten zijn: %+v", id, k)
		}
	}
	before := *v.Score

	// De clusterlijst heeft het cijfer.
	var list struct {
		Items []struct {
			ID    string `json:"id"`
			Score *int   `json:"score"`
			Grade string `json:"grade"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/clusters", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Score == nil || *list.Items[0].Score != before || list.Items[0].Grade != v.Grade {
		t.Fatalf("clusterlijst: %+v", list.Items)
	}

	// Een viewer ziet de bevindingen, maar niet welke beheerder geen
	// tweestapsverificatie heeft, en mag niets accepteren.
	var ov riskOverview
	c.do("GET", "/api/v1/risk", nil, &ov)
	i := slices.IndexFunc(ov.Findings, func(f riskFinding) bool { return f.Check == "platform.admin_totp" })
	if ov.Score == nil || ov.Platform == nil || i < 0 || ov.Findings[i].Text != "Beheerder admin heeft geen tweestapsverificatie" ||
		ov.Findings[i].Cluster != nil {
		t.Fatalf("overzicht als beheerder: %+v", ov)
	}
	// Gesorteerd op wat oplossen voor het geheel oplevert.
	if !slices.IsSortedFunc(ov.Findings, func(a, b riskFinding) int {
		switch {
		case a.FleetGain > b.FleetGain:
			return -1
		case a.FleetGain < b.FleetGain:
			return 1
		}
		return 0
	}) {
		t.Fatalf("niet gesorteerd: %+v", ov.Findings)
	}
	var vo riskOverview
	viewer.do("GET", "/api/v1/risk", nil, &vo)
	j := slices.IndexFunc(vo.Findings, func(f riskFinding) bool { return f.Check == "platform.admin_totp" })
	if j < 0 || vo.Findings[j].Text != "Een beheerder heeft geen tweestapsverificatie" || vo.Findings[j].Subject.Name != "" ||
		strings.Contains(mustJSON(t, vo), "Beheerder admin") {
		t.Fatalf("overzicht als viewer: %+v", vo.Findings)
	}
	accept := map[string]any{"check": "recovery.stale", "subject_type": "failover_test", "subject_id": testID, "reason": "Lab-test, bewust"}
	if s := viewer.do("POST", "/api/v1/risk/acceptances", accept, nil); s != http.StatusForbidden {
		t.Fatalf("viewer accepteert: %d", s)
	}

	// Foute invoer.
	for _, bad := range []map[string]any{
		{"check": "recovery.stale", "subject_type": "failover_test", "subject_id": testID, "reason": "x"},
		{"check": "recovery.stale", "subject_type": "failover_test", "subject_id": testID, "reason": "geldig", "expires_at": now.Add(-time.Hour)},
		{"check": "recovery.stale", "subject_type": "failover_test", "subject_id": testID, "reason": "geldig", "expires_at": now.AddDate(3, 0, 0)},
		{"check": "bestaat.niet", "subject_type": "failover_test", "subject_id": testID, "reason": "geldig"},
	} {
		if s := c.do("POST", "/api/v1/risk/acceptances", bad, nil); s != http.StatusBadRequest {
			t.Fatalf("%v: %d", bad, s)
		}
	}
	if s := c.do("POST", "/api/v1/risk/acceptances", map[string]any{
		"check": "recovery.stale", "subject_type": "failover_test", "subject_id": cl.ID, "reason": "geldig",
	}, nil); s != http.StatusNotFound {
		t.Fatalf("onbekende bevinding: %d", s)
	}

	// Accepteren haalt de punten weg; de bevinding blijft zichtbaar.
	until := now.AddDate(0, 6, 0)
	accept["expires_at"] = until
	if s := c.do("POST", "/api/v1/risk/acceptances", accept, nil); s != http.StatusNoContent {
		t.Fatalf("accepteren: %d", s)
	}
	v = clusterRiskView{}
	c.do("GET", "/api/v1/clusters/"+cl.ID+"/risk", nil, &v)
	old = v.findings("recovery.stale")
	if len(old) != 1 || old[0].Accepted == nil || old[0].Points != 0 || old[0].Gain != 0 || old[0].Accepted.Reason != "Lab-test, bewust" ||
		old[0].Accepted.By != "admin" || old[0].Accepted.ExpiresAt == nil || !old[0].Accepted.ExpiresAt.Equal(until) {
		t.Fatalf("na accepteren: %+v", old)
	}
	if rc := v.card(t, "recovery"); *rc.Score != 100 || *v.Score <= before {
		t.Fatalf("score na accepteren: %d -> %d, kaart %+v", before, *v.Score, rc)
	}
	var summary string
	if err := e.pool.QueryRow(ctx, `SELECT payload->>'text' FROM events WHERE action = 'risk.accepted'`).Scan(&summary); err != nil ||
		summary != "Laatste failovertest 'Stop web01' is 41 dagen geleden" {
		t.Fatalf("event: %q %v", summary, err)
	}
	// Een geaccepteerde bevinding staat in het overzicht achteraan.
	ov = riskOverview{}
	c.do("GET", "/api/v1/risk", nil, &ov)
	if last := ov.Findings[len(ov.Findings)-1]; last.Check != "recovery.stale" || last.Accepted == nil {
		t.Fatalf("geaccepteerde bevinding niet achteraan: %+v", ov.Findings)
	}

	// Intrekken zet de punten terug.
	id := old[0].Accepted.ID
	if s := viewer.do("DELETE", "/api/v1/risk/acceptances/"+id, nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer trekt in: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/risk/acceptances/"+id, nil, nil); s != http.StatusNoContent {
		t.Fatalf("intrekken: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/risk/acceptances/"+id, nil, nil); s != http.StatusNotFound {
		t.Fatalf("nog eens intrekken: %d", s)
	}
	v = clusterRiskView{}
	c.do("GET", "/api/v1/clusters/"+cl.ID+"/risk", nil, &v)
	if *v.Score != before || v.findings("recovery.stale")[0].Accepted != nil {
		t.Fatalf("na intrekken: %d, %+v", *v.Score, v.findings("recovery.stale"))
	}

	// De trend: vandaag is de huidige score; na de momentopname van vandaag
	// staat ze er morgen als gisteren.
	var h struct {
		Points []struct {
			Day   string `json:"day"`
			Score *int   `json:"score"`
		} `json:"points"`
	}
	c.do("GET", "/api/v1/risk/history?cluster="+cl.ID, nil, &h)
	if len(h.Points) != 1 || h.Points[0].Day != now.Format(time.DateOnly) || *h.Points[0].Score != before {
		t.Fatalf("trend: %+v", h.Points)
	}
	if err := e.risk.Snapshot(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := e.risk.Snapshot(ctx, now); err != nil {
		t.Fatal(err)
	}
	e.risk.Now = func() time.Time { return now.Add(24 * time.Hour) }
	h.Points = nil
	c.do("GET", "/api/v1/risk/history?cluster="+cl.ID, nil, &h)
	if len(h.Points) != 2 || h.Points[0].Day != now.Format(time.DateOnly) || *h.Points[0].Score != before ||
		h.Points[1].Day != now.Add(24*time.Hour).Format(time.DateOnly) {
		t.Fatalf("trend na een nacht: %+v", h.Points)
	}
	var fh struct {
		Points []struct{ Day string } `json:"points"`
	}
	viewer.do("GET", "/api/v1/risk/history", nil, &fh)
	if len(fh.Points) != 2 {
		t.Fatalf("trend van het geheel: %+v", fh.Points)
	}
	if s := c.do("GET", "/api/v1/risk/history?days=400", nil, nil); s != http.StatusBadRequest {
		t.Fatalf("days=400: %d", s)
	}
	if s := c.do("GET", "/api/v1/clusters/00000000-0000-0000-0000-000000000001/risk", nil, nil); s != http.StatusNotFound {
		t.Fatalf("onbekend cluster: %d", s)
	}
}

// TestHealthScoreGradeEvent: een ander cijfer wordt pas na tien minuten een
// event, en een cijfer dat terugkeert binnen die tijd niet.
func TestHealthScoreGradeEvent(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	var cl struct{ ID string }
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "prod"}, &cl)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb01", "cluster_id": cl.ID}, nil)
	// Vier VIP's zonder failovertest: op prod kost dat veel meer dan op lab.
	for _, addr := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"} {
		if s := c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": addr}, nil); s != http.StatusCreated {
			t.Fatalf("VIP %s: %d", addr, s)
		}
	}
	e.risk.Warmup = 0
	clock := time.Now()
	e.risk.Now = func() time.Time { return clock }
	compute := func() {
		t.Helper()
		if err := e.risk.Compute(ctx); err != nil {
			t.Fatal(err)
		}
	}
	grade := func() string {
		t.Helper()
		var v clusterRiskView
		c.do("GET", "/api/v1/clusters/"+cl.ID+"/risk", nil, &v)
		return v.Grade
	}
	events := func() []string {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT payload->>'from' || '>' || (payload->>'to') FROM events WHERE action = 'risk.grade_changed' AND subject_id = $1 ORDER BY ts`, cl.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	compute()
	first := grade()
	// Lab rekent milder dan prod: dat verandert het cijfer.
	setEnv := func(env string) {
		t.Helper()
		if s := c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": env}, nil); s != http.StatusOK {
			t.Fatalf("omgeving %s: %d", env, s)
		}
	}
	setEnv("lab")
	compute()
	second := grade()
	if second == first {
		t.Fatalf("het cijfer veranderde niet (%s); de test heeft een groter verschil nodig", first)
	}
	if ev := events(); len(ev) != 0 {
		t.Fatalf("meteen een event: %v", ev)
	}
	// Terug binnen tien minuten: geen event.
	clock = clock.Add(5 * time.Minute)
	setEnv("prod")
	compute()
	clock = clock.Add(6 * time.Minute)
	compute()
	if ev := events(); len(ev) != 0 {
		t.Fatalf("event voor een cijfer dat terugkeerde: %v", ev)
	}
	// Tien minuten anders: één event.
	setEnv("lab")
	compute()
	clock = clock.Add(9 * time.Minute)
	compute()
	if ev := events(); len(ev) != 0 {
		t.Fatalf("event na negen minuten: %v", ev)
	}
	clock = clock.Add(2 * time.Minute)
	compute()
	compute()
	if ev := events(); len(ev) != 1 || ev[0] != first+">"+second {
		t.Fatalf("events: %v", ev)
	}
	var summary string
	if err := e.pool.QueryRow(ctx, `SELECT payload::text FROM events WHERE action = 'risk.grade_changed'`).Scan(&summary); err != nil ||
		!strings.Contains(summary, `"name": "LB"`) {
		t.Fatalf("payload: %s %v", summary, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
