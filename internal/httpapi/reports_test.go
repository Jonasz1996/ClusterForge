package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type testReport struct {
	Month       string   `json:"month"`
	Saved       bool     `json:"saved"`
	Running     bool     `json:"running"`
	SavedMonths []string `json:"saved_months"`
	Clusters    []struct {
		Name             string `json:"name"`
		Environment      string `json:"environment"`
		RTOTargetMinutes int    `json:"rto_target_minutes"`
		RPOTargetHours   int    `json:"rpo_target_hours"`
		Runs             []struct {
			ID              string   `json:"id"`
			Kind            string   `json:"kind"`
			Name            string   `json:"name"`
			Result          string   `json:"result"`
			RecoverySeconds *float64 `json:"recovery_seconds"`
			TakeoverMS      *int64   `json:"takeover_ms"`
			RTOSeconds      *float64 `json:"rto_seconds"`
		} `json:"runs"`
		DR *struct {
			RunID      string   `json:"run_id"`
			Name       string   `json:"name"`
			RTOSeconds *float64 `json:"rto_seconds"`
			RPOSeconds *float64 `json:"rpo_seconds"`
			RTOMet     *bool    `json:"rto_met"`
			RPOMet     *bool    `json:"rpo_met"`
		} `json:"dr"`
		Risks []struct {
			Check string `json:"check"`
			Card  string `json:"card"`
		} `json:"risks"`
	} `json:"clusters"`
}

func (r testReport) names() []string {
	var out []string
	for _, c := range r.Clusters {
		out = append(out, c.Name)
	}
	return out
}

func TestRecoveryReport(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login: %d", s)
	}
	ctx := context.Background()
	ids := map[string]string{}
	for _, cl := range []struct{ slug, name, env, since string }{
		{"web", "Web", "prod", "2029-12-01"}, {"lab", "Lab", "lab", "2029-12-01"}, {"nieuw", "Nieuw", "test", "2030-03-10"},
	} {
		var out struct{ ID string }
		if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": cl.slug, "name": cl.name, "type": "nginx", "environment": cl.env}, &out); s != http.StatusCreated {
			t.Fatalf("cluster %s: %d", cl.name, s)
		}
		if _, err := e.pool.Exec(ctx, "UPDATE clusters SET created_at = $2 WHERE id = $1", out.ID, cl.since); err != nil {
			t.Fatal(err)
		}
		ids[cl.name] = out.ID
	}
	var scenario string
	if err := e.pool.QueryRow(ctx, `INSERT INTO disaster_scenarios (cluster_id, name, kind, params) VALUES ($1, 'DR Web', 'dr_rehearsal', '{"bridge": "vmbr99"}')
		RETURNING id`, ids["Web"]).Scan(&scenario); err != nil {
		t.Fatal(err)
	}
	day := func(m time.Month, d int) time.Time { return time.Date(2030, m, d, 4, 0, 0, 0, time.UTC) }
	runs := map[string]string{}
	for _, r := range []struct {
		key, kind, cluster, others, result, def, meas string
		at                                            time.Time
	}{
		{"verify-jan", "backup.verify", "Web", "{}", "pass", `{}`, `{"restore_seconds": 50}`, day(time.January, 20)},
		{"verify", "backup.verify", "Web", "{}", "pass", `{}`, `{"restore_seconds": 80, "boot_seconds": 40}`, day(time.February, 10)},
		{"failover", "failover.test", "Web", "{}", "pass", `{"name": "VIP naar web-02"}`, `{"takeover_ms": 2100, "downtime_ms": 1800}`, day(time.February, 11)},
		{"disaster", "disaster.run", "Lab", "{" + ids["Lab"] + "," + ids["Web"] + "}", "warning", `{"name": "Host weg"}`, `{}`, day(time.February, 12)},
		{"dr", "dr.rehearsal", "Web", "{}", "pass", `{"name": "DR Web"}`, `{"rto_seconds": 724, "rpo_seconds": 18720}`, day(time.February, 20)},
		{"dr-maart", "dr.rehearsal", "Web", "{}", "fail", `{"name": "DR Web"}`, `{"rpo_seconds": 3600}`, day(time.March, 5)},
	} {
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO test_runs (kind, cluster_id, cluster_ids, scenario_id, definition, measurements, result, summary, restored, created_at, finished_at)
			VALUES ($1, $2, $3::uuid[], CASE WHEN $1 = 'dr.rehearsal' THEN $4::uuid END, $5, $6, $7, upper($7) || ': test', true, $8, $8) RETURNING id`,
			r.kind, ids[r.cluster], r.others, scenario, r.def, r.meas, r.result, r.at).Scan(&id); err != nil {
			t.Fatalf("%s: %v", r.key, err)
		}
		runs[r.key] = id
	}
	// Een test die nog loopt, telt niet mee.
	if _, err := e.pool.Exec(ctx, `INSERT INTO test_runs (kind, cluster_id, definition, created_at) VALUES ('backup.verify', $1, '{}', $2)`,
		ids["Web"], day(time.February, 25)); err != nil {
		t.Fatal(err)
	}
	e.risk.Warmup = 0
	if err := e.risk.Compute(ctx); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2030, 3, 15, 12, 0, 0, 0, time.UTC)
	e.reports.Now, e.reports.Loc = func() time.Time { return clock }, time.UTC

	get := func(cl *client, month string) testReport {
		t.Helper()
		var r testReport
		if s := cl.do("GET", "/api/v1/reports/recovery?month="+month, nil, &r); s != http.StatusOK {
			t.Fatalf("%s: %d", month, s)
		}
		return r
	}

	// Februari, nu berekend: alleen de clusters die er toen al waren, prod
	// eerst, met de runs van die maand.
	feb := get(viewer, "2030-02")
	if feb.Month != "2030-02" || feb.Saved || feb.Running || len(feb.SavedMonths) != 0 || !slices.Equal(feb.names(), []string{"Web", "Lab"}) {
		t.Fatalf("februari: %+v", feb)
	}
	web, lab := feb.Clusters[0], feb.Clusters[1]
	var kinds []string
	for _, r := range web.Runs {
		kinds = append(kinds, r.Kind)
	}
	if !slices.Equal(kinds, []string{"backup.verify", "failover.test", "disaster.run", "dr.rehearsal"}) || web.RTOTargetMinutes != 60 || web.RPOTargetHours != 26 {
		t.Fatalf("web: %v %+v", kinds, web)
	}
	if v := web.Runs[0]; v.ID != runs["verify"] || v.RecoverySeconds == nil || *v.RecoverySeconds != 120 {
		t.Fatalf("controle: %+v", v)
	}
	if f := web.Runs[1]; f.Name != "VIP naar web-02" || f.TakeoverMS == nil || *f.TakeoverMS != 2100 {
		t.Fatalf("failovertest: %+v", f)
	}
	if d := web.Runs[2]; d.Name != "Host weg" || d.Result != "warning" || d.ID != runs["disaster"] {
		t.Fatalf("rampoefening: %+v", d)
	}
	if dr := web.DR; dr == nil || dr.RunID != runs["dr"] || dr.Name != "DR Web" || *dr.RTOSeconds != 724 || *dr.RPOSeconds != 18720 ||
		dr.RTOMet == nil || !*dr.RTOMet || dr.RPOMet == nil || !*dr.RPOMet {
		t.Fatalf("DR: %+v", web.DR)
	}
	if len(lab.Runs) != 1 || lab.Runs[0].ID != runs["disaster"] || lab.DR != nil || lab.RTOTargetMinutes != 0 {
		t.Fatalf("lab: %+v", lab)
	}
	// De risico's: alleen kaarten die bij herstel horen, en de mislukte
	// repetitie van maart is open.
	var checks []string
	for _, cl := range feb.Clusters {
		for _, r := range cl.Risks {
			if r.Card != "backups" && r.Card != "recovery" && r.Card != "redundancy" {
				t.Errorf("%s: kaart %s hoort niet in het rapport", cl.Name, r.Card)
			}
			checks = append(checks, r.Check)
		}
	}
	if !slices.Contains(checks, "recovery.dr_failed") {
		t.Fatalf("risico's: %v", checks)
	}

	// Maart loopt nog: de mislukte repetitie staat bij de runs, de RTO komt
	// nog van februari.
	for _, mar := range []testReport{get(viewer, ""), get(c, "2030-03")} {
		if mar.Month != "2030-03" || mar.Saved || !mar.Running || !slices.Equal(mar.names(), []string{"Web", "Nieuw", "Lab"}) {
			t.Fatalf("maart: %+v", mar)
		}
		if w := mar.Clusters[0]; len(w.Runs) != 1 || w.Runs[0].ID != runs["dr-maart"] || w.Runs[0].RTOSeconds != nil || w.DR == nil || w.DR.RunID != runs["dr"] {
			t.Fatalf("web in maart: %+v", w)
		}
	}
	for _, month := range []string{"2030-04", "2030-13", "maart", "2030-3"} {
		if s := viewer.do("GET", "/api/v1/reports/recovery?month="+month, nil, nil); s != http.StatusBadRequest {
			t.Errorf("%s: %d", month, s)
		}
	}

	// De planner bewaart in de eerste week het rapport van de maand ervoor,
	// één keer; een maand zonder clusters slaat hij over.
	if err := e.reports.Save(ctx, clock); err != nil {
		t.Fatal(err)
	}
	if err := e.reports.Save(ctx, time.Date(2029, 11, 2, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if r := get(viewer, "2030-02"); r.Saved || len(r.SavedMonths) != 0 {
		t.Fatalf("na de eerste week of zonder clusters bewaard: %+v", r.SavedMonths)
	}
	first := time.Date(2030, 3, 1, 0, 5, 0, 0, time.UTC)
	for range 2 {
		if err := e.reports.Save(ctx, first); err != nil {
			t.Fatal(err)
		}
	}
	// Daarna veranderen de doelen; het bewaarde rapport blijft zoals het was.
	if s := c.do("PUT", "/api/v1/clusters/"+ids["Web"]+"/recovery-targets", map[string]any{"rto_minutes": 10, "rpo_hours": 1}, nil); s != http.StatusOK {
		t.Fatalf("doelen: %d", s)
	}
	saved := get(viewer, "2030-02")
	if !saved.Saved || saved.Running || !slices.Equal(saved.SavedMonths, []string{"2030-02"}) || !slices.Equal(saved.names(), []string{"Web", "Lab"}) {
		t.Fatalf("bewaard: %+v", saved)
	}
	if w := saved.Clusters[0]; w.RTOTargetMinutes != 60 || !*w.DR.RTOMet || len(w.Runs) != 4 {
		t.Fatalf("bewaard web: %+v", w)
	}
	var n int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM recovery_reports").Scan(&n); err != nil || n != 1 {
		t.Fatalf("bewaarde rapporten: %d %v", n, err)
	}
	mar := get(viewer, "2030-03")
	if w := mar.Clusters[0]; w.RTOTargetMinutes != 10 || w.RPOTargetHours != 1 || *w.DR.RTOMet || *w.DR.RPOMet {
		t.Fatalf("maart met de nieuwe doelen: %+v", w.DR)
	}
	if !strings.Contains(strings.Join(mar.SavedMonths, ","), "2030-02") {
		t.Fatalf("maanden: %v", mar.SavedMonths)
	}
}
