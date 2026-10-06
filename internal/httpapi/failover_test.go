package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// groupProber speelt de probe van de server na: het VIP antwoordt als een
// machine het heeft, en op HTTP alleen als nginx daar draait.
type groupProber struct{ g *agenttest.Group }

func (p groupProber) Probe(_ context.Context, vip string, pr failover.Probe) error {
	h := p.g.Owner(vip)
	if h == nil {
		return errors.New("geen antwoord van " + vip)
	}
	if u, ok := h.Unit("nginx"); pr.HTTP != nil && (!ok || !u.Active) {
		return errors.New("connection refused")
	}
	return nil
}

type failProber struct{}

func (failProber) Probe(context.Context, string, failover.Probe) error {
	return errors.New("i/o timeout")
}

type testRunView struct {
	ID        string  `json:"id"`
	Result    *string `json:"result"`
	Restored  *bool   `json:"restored"`
	Summary   string  `json:"summary"`
	JobID     *string `json:"job_id"`
	JobStatus *string `json:"job_status"`
	TestID    *string `json:"test_id"`
	Hostname  string  `json:"hostname"`
	Checks    []struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	} `json:"checks"`
	Timeline []struct {
		TMs  int64  `json:"t_ms"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"timeline"`
	Measurements struct {
		DowntimeMs   *int64 `json:"downtime_ms"`
		ExpectMs     int64  `json:"expect_ms"`
		TakeoverNode string `json:"takeover_node"`
		FailbackMs   *int64 `json:"failback_ms"`
		ReturnedTo   string `json:"returned_to"`
		EndMs        int64  `json:"end_ms"`
		Probe        []struct {
			FromMs int64 `json:"from_ms"`
			ToMs   int64 `json:"to_ms"`
			OK     bool  `json:"ok"`
		} `json:"probe"`
	} `json:"measurements"`
	Definition struct {
		Unit        string `json:"unit"`
		Vip         string `json:"vip"`
		Description string `json:"description"`
	} `json:"definition"`
}

func (r testRunView) kinds() []string {
	var out []string
	for _, e := range r.Timeline {
		out = append(out, e.Kind)
	}
	return out
}

type failoverTestView struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	VipAddress  string       `json:"vip_address"`
	VipOwner    *string      `json:"vip_owner"`
	LastRun     *testRunView `json:"last_run"`
}

type failoverList struct {
	Items   []failoverTestView `json:"items"`
	Options struct {
		Scenarios []struct {
			Key       string   `json:"key"`
			Available bool     `json:"available"`
			Reason    string   `json:"reason"`
			Units     []string `json:"units"`
		} `json:"scenarios"`
		Vips []struct {
			ID      string `json:"id"`
			Address string `json:"address"`
			Probe   struct {
				HTTP *struct {
					Path   string `json:"path"`
					Expect int    `json:"expect"`
				} `json:"http"`
			} `json:"probe"`
		} `json:"vips"`
		DefaultFailback bool       `json:"default_failback"`
		Prod            bool       `json:"prod"`
		Slug            string     `json:"slug"`
		Window          string     `json:"window"`
		NextWindow      time.Time  `json:"next_window"`
		LastTestedAt    *time.Time `json:"last_tested_at"`
	} `json:"options"`
	Unrestored []testRunView `json:"unrestored"`
}

type precheckErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Checks  []struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
	} `json:"checks"`
}

func (p precheckErr) failed() []string {
	var out []string
	for _, c := range p.Checks {
		if !c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

const failoverVIP = "10.0.30.100"

func keepalivedConf(priority int) string {
	return fmt.Sprintf(`vrrp_script chk_nginx {
    script "/usr/bin/systemctl is-active --quiet nginx"
    interval 2
}

vrrp_instance VI_30 {
    state BACKUP
    priority %d
    virtual_ipaddress {
        %s/24 dev eth0
    }
    track_script {
        chk_nginx
    }
}
`, priority, failoverVIP)
}

// TestFailover is de acceptatie van mijlpaal 7: een snelle overname geeft
// PASS en een trage FAIL, en daarna draait keepalived weer met het VIP
// terug, ook na annuleren of een herstart van de runner.
func TestFailover(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	group := &agenttest.Group{Takeover: 200 * time.Millisecond}
	e.fo.Prober = groupProber{group}

	var cl, other cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "lab"}, &cl); s != 201 {
		t.Fatalf("cluster: %d", s)
	}
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "ander", "name": "Ander", "type": "generic", "environment": "lab"}, &other)
	var vip, otherVIP struct {
		ID string `json:"id"`
	}
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": failoverVIP}, &vip)
	c.do("POST", "/api/v1/clusters/"+other.ID+"/vips", map[string]any{"address": "10.0.40.100"}, &otherVIP)

	hosts := map[string]*agenttest.Host{}
	for i, name := range []string{"web01", "web02"} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": cl.ID}, &n); s != 201 {
			t.Fatalf("node %s: %d", name, s)
		}
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			h := agenttest.NewHost(root, fmt.Sprintf("10.0.30.%d", 11+i))
			writeFile(t, filepath.Join(root, "etc/keepalived/keepalived.conf"), keepalivedConf(150-10*i), 0o640)
			if _, err := h.Exec(ctx, "apt-get", "install", "-y", "keepalived", "nginx"); err != nil {
				t.Fatal(err)
			}
			ag.Exec, ag.Root, ag.Collector.Run, ag.Collector.Addresses = h.Exec, root, h.Exec, h.Addresses
			group.Join(h)
			hosts[name] = h
		})
	}
	ownerIs := func(name string) bool { return group.Owner(failoverVIP) == hosts[name] }
	active := func(name, unit string) bool {
		u, ok := hosts[name].Unit(unit)
		return ok && u.Active
	}
	healthy := func(what string) {
		t.Helper()
		eventually(t, what, func() bool {
			var d statusView
			c.do("GET", "/api/v1/clusters/"+cl.ID, nil, &d)
			return d.Status == "healthy" && len(d.Vips) == 1 && d.Vips[0].OwnerHostname != nil && *d.Vips[0].OwnerHostname == "web01"
		})
	}
	healthy("cluster gezond met het VIP op web01")

	listURL := "/api/v1/clusters/" + cl.ID + "/failover-tests"
	var list failoverList
	if s := c.do("GET", listURL, nil, &list); s != 200 {
		t.Fatalf("lijst: %d", s)
	}
	if len(list.Items) != 0 || len(list.Options.Scenarios) != 3 || !list.Options.Scenarios[0].Available || !list.Options.Scenarios[1].Available ||
		!slices.Equal(list.Options.Scenarios[1].Units, []string{"nginx"}) || list.Options.Prod || list.Options.DefaultFailback ||
		list.Options.Slug != "lb" || list.Options.Window != "zondag van 03:00 tot 05:00" || list.Options.LastTestedAt != nil ||
		list.Options.NextWindow.IsZero() {
		t.Fatalf("opties: %+v", list.Options)
	}
	// Zonder koppeling aan een VM kan de VM niet hard uit.
	if vm := list.Options.Scenarios[2]; vm.Key != "vm_hard_stop" || vm.Available || !strings.Contains(vm.Reason, "geen Proxmox-koppeling voor web01, web02") {
		t.Fatalf("vm_hard_stop zonder koppeling: %+v", vm)
	}
	if v := list.Options.Vips; len(v) != 1 || v[0].Address != failoverVIP || v[0].Probe.HTTP == nil || v[0].Probe.HTTP.Path != "/" {
		t.Fatalf("VIP's: %+v", v)
	}

	input := func(change func(m map[string]any)) map[string]any {
		m := map[string]any{
			"name": "keepalived op de eigenaar", "vip_id": vip.ID, "scenario": "keepalived_stop", "max_takeover_seconds": 2,
			"expect_failback": true, "probe": map[string]any{"http": map[string]any{"path": "/", "expect": 200}},
		}
		if change != nil {
			change(m)
		}
		return m
	}
	for field, change := range map[string]func(m map[string]any){
		"name":                 func(m map[string]any) { m["name"] = "  " },
		"scenario":             func(m map[string]any) { m["scenario"] = "reboot" },
		"service":              func(m map[string]any) { m["scenario"], m["service"] = "service_stop", "apache2" },
		"max_takeover_seconds": func(m map[string]any) { m["max_takeover_seconds"] = 121 },
		"probe": func(m map[string]any) {
			m["probe"] = map[string]any{"http": map[string]any{"path": "http://elders/", "expect": 200}}
		},
		"vip_id": func(m map[string]any) { m["vip_id"] = otherVIP.ID },
	} {
		var fe fieldErr
		if s := c.do("POST", listURL, input(change), &fe); s != 400 || fe.Field != field {
			t.Errorf("%s: %d %+v", field, s, fe)
		}
	}
	var noProbe fieldErr
	if s := c.do("POST", listURL, input(func(m map[string]any) {
		m["probe"] = map[string]any{"http": map[string]any{"path": "/", "expect": 200}, "tcp": map[string]any{"port": 80}}
	}), &noProbe); s != 400 || noProbe.Field != "probe" {
		t.Errorf("HTTP en TCP samen: %d %+v", s, noProbe)
	}

	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("POST", listURL, input(nil), nil); s != http.StatusForbidden {
		t.Fatalf("viewer maakt een test: %d", s)
	}

	var ka failoverTestView
	if s := c.do("POST", listURL, input(nil), &ka); s != 201 || ka.Description != "keepalived stoppen op de eigenaar" ||
		ka.VipAddress != failoverVIP || ka.VipOwner == nil || *ka.VipOwner != "web01" || ka.LastRun != nil {
		t.Fatalf("test maken: %d %+v", s, ka)
	}
	testURL := "/api/v1/failover-tests/" + ka.ID
	if s := v.do("POST", testURL+"/runs", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer start een test: %d", s)
	}

	start := func(id string) testRunView {
		t.Helper()
		var run testRunView
		if s := c.do("POST", "/api/v1/failover-tests/"+id+"/runs", nil, &run); s != http.StatusAccepted || run.Result != nil || run.JobID == nil {
			t.Fatalf("test starten: %d %+v", s, run)
		}
		return run
	}
	finished := func(run testRunView) testRunView {
		t.Helper()
		waitJob(t, c, *run.JobID)
		var out testRunView
		eventually(t, "run "+run.ID+" afgerond", func() bool {
			c.do("GET", "/api/v1/test-runs/"+run.ID, nil, &out)
			return out.Result != nil
		})
		return out
	}
	restoredOnWeb01 := func(what string) {
		t.Helper()
		if !active("web01", "keepalived") || !active("web01", "nginx") || !ownerIs("web01") {
			k, _ := hosts["web01"].Unit("keepalived")
			t.Fatalf("%s: keepalived op web01 %+v, VIP op web01: %v", what, k, ownerIs("web01"))
		}
		healthy(what + ": cluster weer gezond")
	}

	// Een snelle overname: PASS.
	run := finished(start(ka.ID))
	if *run.Result != "pass" || run.Restored == nil || !*run.Restored || !strings.HasPrefix(run.Summary, "PASS: ") ||
		!strings.Contains(run.Summary, "overgenomen door web02, daarna terug op web01, alles hersteld") {
		t.Fatalf("snelle overname: %+v", run)
	}
	m := run.Measurements
	if m.TakeoverNode != "web02" || m.DowntimeMs == nil || *m.DowntimeMs > 2000 || m.ExpectMs != 2000 || m.ReturnedTo != "web01" ||
		m.FailbackMs == nil || len(m.Probe) == 0 || run.Hostname != "web01" {
		t.Fatalf("metingen: %+v", m)
	}
	for _, k := range []string{"fault", "takeover", "clear", "ready", "return"} {
		if !slices.Contains(run.kinds(), k) {
			t.Errorf("tijdlijn zonder %s: %v", k, run.kinds())
		}
	}
	if len(run.Checks) < 8 || slices.ContainsFunc(run.Checks, func(ch struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}) bool {
		return !ch.OK
	}) {
		t.Fatalf("voorcontrole: %+v", run.Checks)
	}
	restoredOnWeb01("na PASS")
	var jd job
	c.do("GET", "/api/v1/jobs/"+*run.JobID, nil, &jd)
	var steps []string
	for _, st := range jd.Steps {
		steps = append(steps, st.Name+":"+st.Status)
	}
	if !slices.Equal(steps, []string{"Voorcontrole:succeeded", "Storing en meten:succeeded", "Herstellen:succeeded",
		"Terugkeer controleren:succeeded", "Rapport:succeeded"}) || jd.Status != "succeeded" {
		t.Fatalf("stappen: %s %v", jd.Status, steps)
	}
	calls := strings.Join(hosts["web01"].Calls(), "\n")
	if !strings.Contains(calls, "systemctl stop keepalived") || !strings.Contains(calls, "systemctl start keepalived") ||
		strings.Contains(calls, "disable") {
		t.Fatalf("commando's op web01:\n%s", calls)
	}

	// Een trage overname: FAIL, en toch alles hersteld.
	group.Takeover = 2500 * time.Millisecond
	if s := c.do("PATCH", testURL, input(func(m map[string]any) { m["max_takeover_seconds"] = 1 }), nil); s != 200 {
		t.Fatalf("test wijzigen: %d", s)
	}
	run = finished(start(ka.ID))
	if *run.Result != "fail" || !*run.Restored || !strings.Contains(run.Summary, "meer dan de verwachte 1 s; overgenomen door web02") {
		t.Fatalf("trage overname: %+v", run)
	}
	if *run.Measurements.DowntimeMs < 2000 {
		t.Fatalf("onderbreking: %d ms", *run.Measurements.DowntimeMs)
	}
	restoredOnWeb01("na FAIL")

	// nginx stoppen: chk_nginx laat het VIP verhuizen.
	group.Takeover = 200 * time.Millisecond
	var ng failoverTestView
	if s := c.do("POST", listURL, input(func(m map[string]any) {
		m["name"], m["scenario"], m["service"], m["max_takeover_seconds"] = "nginx op de eigenaar", "service_stop", "nginx", 5
	}), &ng); s != 201 || ng.Description != "nginx stoppen op de eigenaar" {
		t.Fatalf("nginx-test: %d %+v", s, ng)
	}
	run = finished(start(ng.ID))
	if *run.Result != "pass" || !*run.Restored || run.Definition.Unit != "nginx" {
		t.Fatalf("nginx stoppen: %+v", run)
	}
	restoredOnWeb01("na nginx")

	// Annuleren tijdens de meting: het herstel loopt toch.
	group.Takeover = 4 * time.Second
	run = start(ka.ID)
	eventually(t, "keepalived gestopt op web01", func() bool { return !active("web01", "keepalived") })
	if s := c.do("POST", "/api/v1/jobs/"+*run.JobID+"/cancel", nil, nil); s != http.StatusAccepted {
		t.Fatalf("annuleren: %d", s)
	}
	run = finished(run)
	if *run.Result != "canceled" || !*run.Restored || !strings.HasPrefix(run.Summary, "Afgebroken tijdens de meting; daarna terug op web01") {
		t.Fatalf("na annuleren: %+v", run)
	}
	if j := waitJob(t, c, *run.JobID); j.Status != "canceled" {
		t.Fatalf("taak na annuleren: %s", j.Status)
	}
	restoredOnWeb01("na annuleren")

	// De runner stopt midden in de meting: noodherstel, en na de herstart
	// hervat de taak met 'meting onderbroken' en herstelt hij.
	run = start(ka.ID)
	eventually(t, "keepalived gestopt op web01", func() bool { return !active("web01", "keepalived") })
	e.restartRunner()
	run = finished(run)
	if *run.Result != "error" || !*run.Restored || !strings.Contains(run.Summary, "meting onderbroken") {
		t.Fatalf("na herstart: %+v", run)
	}
	if j := waitJob(t, c, *run.JobID); j.Status != "failed" || !strings.Contains(j.Error, "meting onderbroken") {
		t.Fatalf("taak na herstart: %s %s", j.Status, j.Error)
	}
	restoredOnWeb01("na herstart van de runner")
	group.Takeover = 200 * time.Millisecond

	// De voorcontrole heeft geen force.
	precheck := func(what string, wantCode string, wantFailed ...string) {
		t.Helper()
		var pe precheckErr
		if s := c.do("POST", testURL+"/runs", nil, &pe); s != http.StatusConflict || pe.Code != wantCode ||
			(len(wantFailed) > 0 && !slices.Equal(pe.failed(), wantFailed)) {
			t.Fatalf("%s: %d %+v (mislukt: %v)", what, s, pe, pe.failed())
		}
	}
	c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": "prod"}, nil)
	var ae apiErr
	if s := c.do("POST", testURL+"/runs", nil, &ae); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("prod zonder tweestapsverificatie: %d %+v", s, ae)
	}
	c.do("GET", listURL, nil, &list)
	if !list.Options.Prod || list.Options.Slug != "lb" || list.Options.LastTestedAt == nil {
		t.Fatalf("opties op prod: %+v", list.Options)
	}
	if vm := list.Options.Scenarios[2]; vm.Available || !strings.Contains(vm.Reason, "niet op prod") {
		t.Fatalf("vm_hard_stop op prod: %+v", vm)
	}
	c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": "lab"}, nil)

	hosts["web02"].SetUnit("postgresql", agenttest.Unit{Enabled: true, Active: true})
	eventually(t, "database gezien", func() bool {
		c.do("GET", listURL, nil, &list)
		return !list.Options.Scenarios[0].Available && strings.Contains(list.Options.Scenarios[0].Reason, "postgresql op web02")
	})
	precheck("database", "precheck_failed", "Geen database")
	hosts["web02"].SetUnit("postgresql", agenttest.Unit{Enabled: true})

	e.fo.Prober = failProber{}
	eventually(t, "geen database meer", func() bool {
		var pe precheckErr
		c.do("POST", testURL+"/runs", nil, &pe)
		return slices.Equal(pe.failed(), []string{"Probe"})
	})
	e.fo.Prober = groupProber{group}

	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (kind, title, status, heartbeat_at) VALUES ('backup.verify', 'Back-upcontrole: db01', 'running', now())`); err != nil {
		t.Fatal(err)
	}
	precheck("testslot", "precheck_failed", "Testslot")
	_, err := e.pool.Exec(ctx, `INSERT INTO jobs (kind, title) VALUES ('failover.test', 'Failovertest: tegelijk')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "jobs_test_slot" {
		t.Fatalf("tweede invasieve test: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET status = 'failed', finished_at = now() WHERE kind = 'backup.verify'`); err != nil {
		t.Fatal(err)
	}

	// Lukt het herstel niet, dan is restored false tot Opnieuw herstellen.
	hosts["web01"].FailTimes("systemctl start keepalived", "Job for keepalived.service failed because the control process exited with error code.", 2)
	run = finished(start(ka.ID))
	if *run.Result != "error" || run.Restored == nil || *run.Restored || !strings.Contains(run.Summary, "herstel mislukt") ||
		!strings.Contains(run.Summary, "keepalived staat mogelijk nog uit op web01") {
		t.Fatalf("herstel mislukt: %+v", run)
	}
	if active("web01", "keepalived") || !ownerIs("web02") {
		t.Fatal("keepalived draait weer op web01 terwijl het herstel mislukte")
	}
	c.do("GET", listURL, nil, &list)
	if len(list.Unrestored) != 1 || list.Unrestored[0].ID != run.ID {
		t.Fatalf("niet hersteld: %+v", list.Unrestored)
	}
	if s := v.do("POST", "/api/v1/test-runs/"+run.ID+"/restore", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer herstelt: %d", s)
	}
	var rj job
	if s := c.do("POST", "/api/v1/test-runs/"+run.ID+"/restore", nil, &rj); s != http.StatusAccepted {
		t.Fatalf("opnieuw herstellen: %d", s)
	}
	if j := waitJob(t, c, rj.ID); j.Status != "succeeded" {
		t.Fatalf("herstellen: %s %s", j.Status, j.Error)
	}
	c.do("GET", "/api/v1/test-runs/"+run.ID, nil, &run)
	if run.Restored == nil || !*run.Restored || !strings.HasSuffix(run.Summary, "Daarna opnieuw hersteld.") {
		t.Fatalf("na opnieuw herstellen: %+v", run)
	}
	restoredOnWeb01("na opnieuw herstellen")
	c.do("GET", listURL, nil, &list)
	if len(list.Unrestored) != 0 || list.Items[0].LastRun == nil || list.Items[0].LastRun.ID != run.ID {
		t.Fatalf("lijst na herstel: %+v", list)
	}
	var ce fieldErr
	if s := c.do("POST", "/api/v1/test-runs/"+run.ID+"/restore", nil, &ce); s != http.StatusConflict {
		t.Fatalf("herstellen van een herstelde run: %d", s)
	}

	var history struct{ Items []testRunView }
	if s := v.do("GET", testURL+"/runs", nil, &history); s != 200 || len(history.Items) != 5 || history.Items[0].ID != run.ID {
		t.Fatalf("geschiedenis: %d %d", s, len(history.Items))
	}

	// Verwijderen laat de runs staan.
	if s := c.do("DELETE", testURL, nil, nil); s != http.StatusNoContent {
		t.Fatalf("verwijderen: %d", s)
	}
	if s := c.do("GET", "/api/v1/test-runs/"+run.ID, nil, &run); s != 200 || run.TestID != nil {
		t.Fatalf("run na verwijderen: %d %+v", s, run.TestID)
	}

	var got []string
	rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE 'failover%' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		got = append(got, a)
	}
	rows.Close()
	for _, a := range []string{"failover_test.created", "failover_test.updated", "failover_test.deleted",
		"failover.fault_injected", "failover.fault_cleared", "failover.finished"} {
		if !slices.Contains(got, a) {
			t.Errorf("event %s ontbreekt: %v", a, got)
		}
	}
	var withRun int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action LIKE 'failover.%' AND payload->>'run_id' IS NULL`).Scan(&withRun); err != nil || withRun != 0 {
		t.Fatalf("failover-events zonder run_id: %d %v", withRun, err)
	}
}
