package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

// vmSim speelt een VM met een agent na: Proxmox zet hem hard uit of start
// hem weer, en de agent gaat mee uit en aan.
type vmSim struct {
	host *agenttest.Host
	tmpl *agent.Agent

	mu   sync.Mutex
	stop func()
}

func (v *vmSim) power(on bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !on {
		// Proxmox doet even over de stop; intussen stuurt de agent nog een
		// heartbeat met het VIP, en daarna nooit meer.
		time.Sleep(300 * time.Millisecond)
		v.host.Power(false)
		if v.stop != nil {
			v.stop()
			v.stop = nil
		}
		return
	}
	if v.stop != nil {
		return
	}
	v.host.Power(true)
	a := &agent.Agent{
		Config: v.tmpl.Config, Version: v.tmpl.Version, Log: v.tmpl.Log, Collector: v.tmpl.Collector,
		HeartbeatInterval: v.tmpl.HeartbeatInterval, MetricsInterval: v.tmpl.MetricsInterval, StatePath: v.tmpl.StatePath,
		Exec: v.tmpl.Exec, Root: v.tmpl.Root,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(ctx) }()
	v.stop = func() { cancel(); <-done }
}

func (v *vmSim) shutdown() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stop != nil {
		v.stop()
	}
}

type plannedTest struct {
	ID        string      `json:"id"`
	Scheduled bool        `json:"scheduled"`
	NextRunAt *time.Time  `json:"next_run_at"`
	LastRun   *plannedRun `json:"last_run"`
	Scenario  string      `json:"scenario"`
	Probe     any         `json:"probe"`
	Name      string      `json:"name"`
	VipID     string      `json:"vip_id"`
	Max       int         `json:"max_takeover_seconds"`
	Failback  bool        `json:"expect_failback"`
}

func (p plannedTest) input(change func(m map[string]any)) map[string]any {
	m := map[string]any{
		"name": p.Name, "vip_id": p.VipID, "scenario": p.Scenario, "max_takeover_seconds": p.Max,
		"expect_failback": p.Failback, "probe": p.Probe, "scheduled": p.Scheduled,
	}
	if change != nil {
		change(m)
	}
	return m
}

type plannedRun struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Trigger  string  `json:"trigger"`
	Result   *string `json:"result"`
	Summary  string  `json:"summary"`
	JobID    *string `json:"job_id"`
	TestID   *string `json:"test_id"`
	Hostname string  `json:"hostname"`
	Checks   []struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
	} `json:"checks"`
	Measurements *struct {
		TakeoverNode string `json:"takeover_node"`
		ReturnedTo   string `json:"returned_to"`
	} `json:"measurements"`
	Timeline []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"timeline"`
}

type backupPolicyView struct {
	MaxAgeHours    int        `json:"max_age_hours"`
	VerifyEnabled  bool       `json:"verify_enabled"`
	NextRunAt      *time.Time `json:"next_run_at"`
	LastVerifiedAt *time.Time `json:"last_verified_at"`
	Window         string     `json:"window"`
}

// TestPlanning is de acceptatie van mijlpaal 11: de planner rekent "elke 7
// dagen om 04:00" goed uit over de overgang naar wintertijd op 25 oktober
// 2026, start nooit twee tests tegelijk en zet een gemiste run op skipped
// zonder mislukte taak. Een prod-test loopt alleen met de slug, en een
// geplande failovertest op prod is niet in te stellen.
func TestPlanning(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	brussels, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	window, err := planner.ParseWindow("zo 04:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	window = window.In(brussels)
	e.fo.Window, e.backups.Window = window, window
	group := &agenttest.Group{Takeover: 200 * time.Millisecond}
	e.fo.Prober = groupProber{group}

	// Proxmox met de VM's van web01 en web02 en hun back-ups.
	pve, srv, fp := newPVE(t)
	pve.SetGuestStatus(101, "running", "pve1")
	pve.AddStorage(pvefake.Storage{Name: "pbs", Node: "pve1", Shared: true, Content: "backup"})
	for _, vmid := range []int{101, 102} {
		pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: vmid, Time: time.Now().Add(-6 * time.Hour), Size: 4 << 30})
	}
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret",
		"tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}

	var cl cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "lab"}, &cl); s != 201 {
		t.Fatalf("cluster: %d", s)
	}
	var vip struct {
		ID string `json:"id"`
	}
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": failoverVIP}, &vip)

	vms := map[int]*vmSim{}
	for i, name := range []string{"web01", "web02"} {
		vmid := 101 + i
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{
			"hostname": name, "cluster_id": cl.ID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": vmid},
		}, &n); s != 201 {
			t.Fatalf("node %s: %d", name, s)
		}
		vm := &vmSim{}
		vm.stop = startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('c'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			h := agenttest.NewHost(root, fmt.Sprintf("10.0.30.%d", 11+i))
			writeFile(t, filepath.Join(root, "etc/keepalived/keepalived.conf"), keepalivedConf(150-10*i), 0o640)
			if _, err := h.Exec(ctx, "apt-get", "install", "-y", "keepalived", "nginx"); err != nil {
				t.Fatal(err)
			}
			ag.Exec, ag.Root, ag.Collector.Run, ag.Collector.Addresses = h.Exec, root, h.Exec, h.Addresses
			group.Join(h)
			vm.host, vm.tmpl = h, ag
		})
		vms[vmid] = vm
		t.Cleanup(vm.shutdown)
	}
	pve.OnPower(func(vmid int, status string) {
		if vm := vms[vmid]; vm != nil {
			vm.power(status == "running")
		}
	})
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != 200 {
		t.Fatalf("sync: %d", s)
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

	// De planner met een eigen klok. Een fout in een taak laat de test
	// mislukken in plaats van alleen te loggen.
	var now time.Time
	check := func(name string, f func(context.Context, time.Time) error) planner.Task {
		return planner.Task{Name: name, Tick: func(ctx context.Context, at time.Time) error {
			if err := f(ctx, at); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			return nil
		}}
	}
	sched := planner.NewScheduler(e.pool, e.api.log, check("failovertests", e.fo.RunScheduled), check("back-upcontroles", e.backups.RunScheduled))
	sched.Now = func() time.Time { return now }
	tick := func(at time.Time) {
		t.Helper()
		now = at
		sched.Tick(ctx)
	}
	testJobs := func() (out []job) {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT id::text, title, status::text FROM jobs
			WHERE kind IN ('failover.test', 'backup.verify') AND status IN ('queued', 'running') ORDER BY created_at`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.ID, &j.Title, &j.Status); err != nil {
				t.Fatal(err)
			}
			out = append(out, j)
		}
		return out
	}
	oneJob := func(what, title string) job {
		t.Helper()
		js := testJobs()
		if len(js) != 1 || js[0].Title != title {
			t.Fatalf("%s: %+v", what, js)
		}
		return js[0]
	}
	getTest := func(id string) plannedTest {
		t.Helper()
		var out plannedTest
		if s := c.do("GET", "/api/v1/failover-tests/"+id, nil, &out); s != 200 {
			t.Fatalf("test %s: %d", id, s)
		}
		return out
	}
	policyURL := "/api/v1/clusters/" + cl.ID + "/backup-policy"
	policy := func() (out backupPolicyView) {
		t.Helper()
		c.do("GET", policyURL, nil, &out)
		return out
	}
	runs := func(query string) (out []plannedRun) {
		t.Helper()
		var list struct {
			Items []plannedRun `json:"items"`
		}
		if s := c.do("GET", "/api/v1/test-runs?cluster_id="+cl.ID+query, nil, &list); s != 200 {
			t.Fatalf("runs: %d", s)
		}
		return list.Items
	}
	finished := func(j job) plannedRun {
		t.Helper()
		// Een VM die opnieuw opstart, duurt langer dan een dienst; bij een
		// timeout staan de stappen van de taak in de uitvoer.
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			var cur job
			c.do("GET", "/api/v1/jobs/"+j.ID, nil, &cur)
			if cur.Status != "queued" && cur.Status != "running" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s niet klaar:\n%s", j.Title, stepLog(t, e, j.ID))
			}
		}
		var out plannedRun
		eventually(t, "run van "+j.Title+" afgerond", func() bool {
			for _, r := range runs("&kind=failover.test") {
				if r.JobID != nil && *r.JobID == j.ID && r.Result != nil {
					out = r
					return true
				}
			}
			return false
		})
		return out
	}
	inWindow := func(at time.Time) bool {
		start, end := window.Next(at)
		return !at.Before(start) && at.Before(end)
	}

	// Planning aanzetten: de eerste run staat in het eerstvolgende venster.
	listURL := "/api/v1/clusters/" + cl.ID + "/failover-tests"
	probe := map[string]any{"http": map[string]any{"path": "/", "expect": 200}}
	var ka, hard plannedTest
	if s := c.do("POST", listURL, map[string]any{
		"name": "keepalived", "vip_id": vip.ID, "scenario": "keepalived_stop", "max_takeover_seconds": 2,
		"expect_failback": true, "probe": probe, "scheduled": true,
	}, &ka); s != 201 || !ka.Scheduled || ka.NextRunAt == nil || !inWindow(*ka.NextRunAt) {
		t.Fatalf("geplande test maken: %d %+v", s, ka)
	}
	var opts failoverList
	c.do("GET", listURL, nil, &opts)
	if vm := opts.Options.Scenarios[2]; vm.Key != "vm_hard_stop" || !vm.Available {
		t.Fatalf("vm_hard_stop met gekoppelde VM's: %+v", vm)
	}
	if opts.Options.Window != "zondag van 04:00 tot 06:00" {
		t.Fatalf("venster: %q", opts.Options.Window)
	}
	if s := c.do("POST", listURL, map[string]any{
		"name": "VM hard uit", "vip_id": vip.ID, "scenario": "vm_hard_stop", "max_takeover_seconds": 10,
		"expect_failback": true, "probe": probe, "scheduled": true,
	}, &hard); s != 201 || !hard.Scheduled || hard.Scenario != "vm_hard_stop" {
		t.Fatalf("vm_hard_stop maken: %d %+v", s, hard)
	}
	if p := policy(); p.VerifyEnabled || p.NextRunAt != nil || p.LastVerifiedAt != nil || p.Window != "zondag van 04:00 tot 06:00" {
		t.Fatalf("beleid voor het aanzetten: %+v", p)
	}
	var p backupPolicyView
	if s := c.do("PUT", policyURL, map[string]any{"verify_enabled": true}, &p); s != 200 || !p.VerifyEnabled || p.NextRunAt == nil ||
		!inWindow(*p.NextRunAt) || p.MaxAgeHours != 30 {
		t.Fatalf("back-upcontrole plannen: %d %+v", s, p)
	}

	// Alles staat op zondag 18 oktober 2026 om 04:00, een week voor de
	// overgang naar wintertijd.
	first := time.Date(2026, 10, 18, 4, 0, 0, 0, brussels)
	if _, err := e.pool.Exec(ctx, "UPDATE failover_tests SET next_run_at = $1 WHERE scheduled", first); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE backup_policies SET next_run_at = $1 WHERE verify_enabled", first); err != nil {
		t.Fatal(err)
	}
	tick(first.Add(-time.Minute))
	if js := testJobs(); len(js) != 0 {
		t.Fatalf("voor het venster: %+v", js)
	}

	// Drie runs zijn aan de beurt, maar er start er één. De runner staat
	// even stil, zodat de taak zeker nog in de wachtrij staat.
	e.stopRunner()
	tick(first.Add(time.Minute))
	j := oneJob("eerste tick", "Failovertest: keepalived (gepland)")
	tick(first.Add(2 * time.Minute))
	oneJob("tweede tick terwijl de test wacht", "Failovertest: keepalived (gepland)")
	e.startRunner()
	// Elke 7 dagen om 04:00: over de overgang naar wintertijd is dat
	// 03:00 UTC in plaats van 02:00 UTC.
	winter := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	if got := getTest(ka.ID); got.NextRunAt == nil || !got.NextRunAt.Equal(winter) {
		t.Fatalf("volgende run na 18 oktober: %v", got.NextRunAt)
	}
	if got := getTest(hard.ID); got.NextRunAt == nil || !got.NextRunAt.Equal(first) {
		t.Fatalf("vm_hard_stop wacht: %v", got.NextRunAt)
	}
	r := finished(j)
	if *r.Result != "pass" || r.Trigger != "schedule" {
		t.Fatalf("geplande keepalived-test: %+v", r)
	}
	healthy("terug op web01 na keepalived")

	// Het testslot is vrij: nu de VM van de eigenaar hard uit, via Proxmox.
	tick(first.Add(3 * time.Minute))
	j = oneJob("vm_hard_stop", "Failovertest: VM hard uit (gepland)")
	r = finished(j)
	if *r.Result != "pass" || r.Trigger != "schedule" || r.Measurements == nil || r.Measurements.TakeoverNode != "web02" ||
		r.Measurements.ReturnedTo != "web01" {
		t.Fatalf("vm_hard_stop: %+v %+v", r, r.Measurements)
	}
	if !slices.ContainsFunc(r.Timeline, func(ev struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}) bool {
		return ev.Kind == "fault" && ev.Text == "VM van web01 hard uitgezet"
	}) {
		t.Fatalf("tijdlijn: %+v", r.Timeline)
	}
	calls := pve.Calls()
	stop, start := slices.Index(calls, "POST /nodes/pve1/qemu/101/status/stop"), slices.Index(calls, "POST /nodes/pve1/qemu/101/status/start")
	if stop < 0 || start < stop {
		t.Fatalf("Proxmox-aanroepen: %v", calls)
	}
	if g, _ := pve.Guest(101); g.Status != "running" {
		t.Fatalf("VM 101 na de test: %s", g.Status)
	}
	if got := getTest(hard.ID); got.NextRunAt == nil || !got.NextRunAt.Equal(winter) {
		t.Fatalf("volgende vm_hard_stop: %v", got.NextRunAt)
	}
	healthy("terug op web01 na de VM")

	// De back-upcontrole kiest de node die het langst niet gecontroleerd
	// werd. De runner staat stil, zodat er geen sandbox komt.
	e.stopRunner()
	tick(first.Add(5 * time.Minute))
	j = oneJob("back-upcontrole", "Back-upcontrole: web01 (gepland)")
	if s := c.do("POST", "/api/v1/jobs/"+j.ID+"/cancel", nil, nil); s != http.StatusAccepted {
		t.Fatalf("annuleren: %d", s)
	}
	e.startRunner()
	if p := policy(); p.NextRunAt == nil || !p.NextRunAt.Equal(winter) {
		t.Fatalf("volgende back-upcontrole: %+v", p)
	}

	// Een week later is ClusterForge 45 minuten te laat: alle drie
	// overgeslagen, zonder taak.
	late := winter.Add(45 * time.Minute)
	tick(late)
	if js := testJobs(); len(js) != 0 {
		t.Fatalf("te laat maar toch gestart: %+v", js)
	}
	skipped := runs("&result=skipped")
	if len(skipped) != 3 {
		t.Fatalf("overgeslagen runs: %+v", skipped)
	}
	for _, r := range skipped {
		if r.JobID != nil || r.Trigger != "schedule" ||
			!strings.HasPrefix(r.Summary, "Overgeslagen: de run stond om 04:00 gepland en is meer dan 30 minuten te laat") {
			t.Fatalf("overgeslagen run: %+v", r)
		}
	}
	nov1 := time.Date(2026, 11, 1, 4, 0, 0, 0, brussels)
	if got := getTest(ka.ID); !got.Scheduled || got.NextRunAt == nil || !got.NextRunAt.Equal(nov1) {
		t.Fatalf("na te laat: %+v", got)
	}

	// De server lag twee weken stil: één overgeslagen run, en de volgende
	// staat in het eerstvolgende venster.
	down := time.Date(2026, 11, 10, 9, 0, 0, 0, brussels)
	tick(down)
	tick(down.Add(time.Minute))
	skipped = runs("&result=skipped&kind=failover.test")
	if len(skipped) != 4 || !strings.Contains(skipped[0].Summary, "het testvenster van zondag 01-11-2026 eindigde om 06:00") {
		t.Fatalf("na stilstand: %+v", skipped)
	}
	if got := getTest(hard.ID); got.NextRunAt == nil || !got.NextRunAt.Equal(time.Date(2026, 11, 15, 4, 0, 0, 0, brussels)) {
		t.Fatalf("na stilstand volgende run: %v", got.NextRunAt)
	}
	var failed int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE status = 'failed'").Scan(&failed); err != nil || failed != 0 {
		t.Fatalf("mislukte taken: %d %v", failed, err)
	}

	// Naar prod: de geplande tests worden overgeslagen en de planning gaat
	// uit; plannen kan daar niet meer.
	if s := c.do("PUT", policyURL, map[string]any{"verify_enabled": false}, &p); s != 200 || p.VerifyEnabled || p.NextRunAt != nil {
		t.Fatalf("back-upcontrole uit: %d %+v", s, p)
	}
	c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": "prod"}, nil)
	tick(time.Date(2026, 11, 15, 4, 1, 0, 0, brussels))
	for _, id := range []string{ka.ID, hard.ID} {
		if got := getTest(id); got.Scheduled || got.NextRunAt != nil || got.LastRun == nil || *got.LastRun.Result != "skipped" ||
			!strings.Contains(got.LastRun.Summary, "het cluster staat nu in prod") {
			t.Fatalf("op prod: %+v %+v", got, got.LastRun)
		}
	}
	ka = getTest(ka.ID)
	var fe fieldErr
	if s := c.do("PATCH", "/api/v1/failover-tests/"+ka.ID, ka.input(func(m map[string]any) { m["scheduled"] = true }), &fe); s != 400 || fe.Field != "scheduled" {
		t.Fatalf("plannen op prod: %d %+v", s, fe)
	}
	if s := c.do("POST", listURL, ka.input(func(m map[string]any) { m["name"], m["scenario"] = "nog een VM", "vm_hard_stop" }), &fe); s != 400 || fe.Field != "scenario" {
		t.Fatalf("vm_hard_stop op prod: %d %+v", s, fe)
	}

	// Met de hand op prod: tweestapsverificatie en de slug.
	runURL := "/api/v1/failover-tests/" + ka.ID + "/runs"
	var ae apiErr
	if s := c.do("POST", runURL, map[string]any{"confirm": "lb"}, &ae); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("zonder tweestapsverificatie: %d %+v", s, ae)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE users SET totp_enabled_at = now() WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{nil, map[string]any{}, map[string]any{"confirm": "LB"}, map[string]any{"confirm": "lb-prod"}} {
		if s := c.do("POST", runURL, body, &ae); s != http.StatusConflict || ae.Code != "needs_confirmation" || !strings.Contains(ae.Message, "lb") {
			t.Fatalf("bevestiging %v: %d %+v", body, s, ae)
		}
	}
	var pe precheckErr
	if s := c.do("POST", "/api/v1/failover-tests/"+hard.ID+"/runs", map[string]any{"confirm": "lb"}, &pe); s != http.StatusConflict ||
		pe.Code != "precheck_failed" || !slices.Equal(pe.failed(), []string{"Omgeving"}) {
		t.Fatalf("vm_hard_stop met de hand op prod: %d %+v", s, pe)
	}
	var manual plannedRun
	if s := c.do("POST", runURL, map[string]any{"confirm": "lb"}, &manual); s != http.StatusAccepted || manual.Trigger != "manual" {
		t.Fatalf("met de slug: %d %+v", s, manual)
	}
	r = finished(job{ID: *manual.JobID, Title: "met de hand op prod"})
	if *r.Result != "pass" {
		t.Fatalf("met de hand op prod: %+v", r)
	}
	if !slices.ContainsFunc(r.Checks, func(ch struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
	}) bool {
		return ch.Name == "Omgeving" && ch.OK
	}) {
		t.Fatalf("controles op prod: %+v", r.Checks)
	}
	c.do("GET", listURL, nil, &opts)
	if !opts.Options.Prod || opts.Options.LastTestedAt == nil || time.Since(*opts.Options.LastTestedAt) > time.Minute {
		t.Fatalf("opties na de prod-test: %+v", opts.Options)
	}
	healthy("terug op web01 na de prod-test")
}

// stepLog geeft de stappen van een taak met hun log, voor een test die
// vastloopt.
func stepLog(t *testing.T, e *testEnv, jobID string) string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT name, status::text, array_to_string(log, E'\n  '), error
		FROM job_steps WHERE job_id = $1 ORDER BY seq`, jobID)
	if err != nil {
		return err.Error()
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var name, status, log, er string
		if err := rows.Scan(&name, &status, &log, &er); err != nil {
			return err.Error()
		}
		fmt.Fprintf(&b, "%s (%s) %s\n  %s\n", name, status, er, log)
	}
	return b.String()
}
