package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

type disasterPrediction struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type disasterClusterView struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Slug        string             `json:"slug"`
	Environment string             `json:"environment"`
	Role        string             `json:"role"`
	Before      string             `json:"before"`
	Predicted   disasterPrediction `json:"predicted"`
	Measured    disasterPrediction `json:"measured"`
	LongestMs   int64              `json:"longest_ms"`
	RecoveredMs *int64             `json:"recovered_ms"`
	Match       string             `json:"match"`
	Vips        []struct {
		Address string `json:"address"`
	} `json:"vips"`
}

type disasterTargetView struct {
	Hostname string `json:"hostname"`
	Cluster  string `json:"cluster"`
	Unit     string `json:"unit"`
	Vmid     *int   `json:"vmid"`
}

type disasterPreviewView struct {
	OK     bool `json:"ok"`
	Checks []struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	} `json:"checks"`
	Description string                `json:"description"`
	Targets     []disasterTargetView  `json:"targets"`
	Clusters    []disasterClusterView `json:"clusters"`
	Skipped     []string              `json:"skipped"`
	Notes       []string              `json:"notes"`
	Confirm     []string              `json:"confirm"`
}

func (p disasterPreviewView) failed() []string {
	var out []string
	for _, c := range p.Checks {
		if !c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

func (p disasterPreviewView) check(name string) string {
	for _, c := range p.Checks {
		if c.Name == name {
			return c.Detail
		}
	}
	return ""
}

type disasterRunView struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Result   *string `json:"result"`
	Restored *bool   `json:"restored"`
	Summary  string  `json:"summary"`
	JobID    *string `json:"job_id"`
	Timeline []struct {
		TMs  int64  `json:"t_ms"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"timeline"`
	Disaster *struct {
		Name          string               `json:"name"`
		Kind          string               `json:"kind"`
		Unit          string               `json:"unit"`
		FaultMs       int64                `json:"fault_ms"`
		ClearMs       *int64               `json:"clear_ms"`
		RecoveryMs    *int64               `json:"recovery_ms"`
		TotalMs       *int64               `json:"total_ms"`
		Targets       []disasterTargetView `json:"targets"`
		TargetResults []struct {
			Hostname string `json:"hostname"`
			Error    string `json:"error"`
		} `json:"target_results"`
		Clusters []disasterClusterView `json:"clusters"`
		Vips     []struct {
			Address  string `json:"address"`
			Segments []struct {
				OK bool `json:"ok"`
			} `json:"segments"`
		} `json:"vips"`
	} `json:"disaster"`
}

func (r disasterRunView) kinds() []string {
	var out []string
	for _, e := range r.Timeline {
		out = append(out, e.Kind)
	}
	return out
}

// rows geeft per cluster "naam: voorspeld/gemeten match".
func (r disasterRunView) rows() string {
	var out []string
	for _, c := range r.Disaster.Clusters {
		out = append(out, fmt.Sprintf("%s: %s/%s %s", c.Name, c.Predicted.Status, c.Measured.Status, c.Match))
	}
	return strings.Join(out, "\n")
}

type disasterScenarioView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Description     string `json:"description"`
	Unit            string `json:"unit"`
	FaultSeconds    int    `json:"fault_seconds"`
	RecoverySeconds int    `json:"recovery_seconds"`
	Host            *struct {
		Connection string `json:"connection"`
		Host       string `json:"host"`
	} `json:"host"`
	LastRun *disasterRunView `json:"last_run"`
}

type disasterList struct {
	Items   []disasterScenarioView `json:"items"`
	Options struct {
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason"`
		Slug    string `json:"slug"`
		Kinds   []struct {
			Key       string `json:"key"`
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
		} `json:"kinds"`
		Hosts []struct {
			Connection string   `json:"connection"`
			Host       string   `json:"host"`
			Nodes      []string `json:"nodes"`
			Others     []string `json:"others"`
		} `json:"hosts"`
		Units []string `json:"units"`
		Vips  []string `json:"vips"`
	} `json:"options"`
}

// TestDisaster is de acceptatie van mijlpaal 11 van fase 3: in lab en test
// laat een rampoefening een dienst, een VIP of een hele Proxmox-host
// uitvallen, meet ze elk geraakt cluster en zet ze de meting naast de
// voorspelling van de wat-als. Daarna draait alles weer, en lukt dat niet,
// dan blijft de rode balk tot Opnieuw herstellen.
func TestDisaster(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	group := &agenttest.Group{Takeover: 200 * time.Millisecond}
	e.dis.Prober = groupProber{group}
	e.createUser("kijker", "een-lang-wachtwoord", "viewer")
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")

	// Proxmox: web01, web02 en app01 staan op pve1, met een VM die niet in
	// ClusterForge staat.
	pve, srv, fp := newPVE(t)
	pve.SetGuestStatus(101, "running", "pve1")
	pve.SetGuestStatus(102, "running", "pve1")
	pve.AddGuest(pvefake.Guest{Type: "qemu", VMID: 201, Name: "app01", Node: "pve1", Status: "running", MaxCPU: 2, MaxMem: 2 << 30})
	pve.AddGuest(pvefake.Guest{Type: "qemu", VMID: 400, Name: "oud-ding", Node: "pve1", Status: "running", MaxMem: 1 << 30})
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != http.StatusCreated {
		t.Fatalf("koppelen: %d", s)
	}

	var lb, app cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "lab"}, &lb)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "app", "name": "App", "type": "generic", "environment": "test"}, &app)
	c.do("POST", "/api/v1/clusters/"+lb.ID+"/vips", map[string]any{"address": failoverVIP}, nil)

	hosts := map[string]*agenttest.Host{}
	vms := map[int]*vmSim{}
	addNode := func(name, clusterID string, vmid int, ip string, setup func(h *agenttest.Host, root string)) {
		t.Helper()
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{
			"hostname": name, "cluster_id": clusterID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": vmid},
		}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		vm := &vmSim{}
		vm.stop = startAgentWith(t, e, c, n.ID, fmt.Sprintf("%032x", vmid), &addrs{}, func(ag *agent.Agent, root string) {
			h := agenttest.NewHost(root, ip)
			setup(h, root)
			ag.Exec, ag.Root, ag.Collector.Run, ag.Collector.Addresses = h.Exec, root, h.Exec, h.Addresses
			vm.host, vm.tmpl = h, ag
			hosts[name] = h
		})
		vms[vmid] = vm
		t.Cleanup(vm.shutdown)
	}
	for i, name := range []string{"web01", "web02"} {
		addNode(name, lb.ID, 101+i, fmt.Sprintf("10.0.30.%d", 11+i), func(h *agenttest.Host, root string) {
			writeFile(t, filepath.Join(root, "etc/keepalived/keepalived.conf"), keepalivedConf(150-10*i), 0o640)
			if _, err := h.Exec(ctx, "apt-get", "install", "-y", "keepalived", "nginx"); err != nil {
				t.Fatal(err)
			}
			group.Join(h)
		})
	}
	addNode("app01", app.ID, 201, "10.0.31.11", func(h *agenttest.Host, _ string) {
		h.SetUnit("php8.2-fpm", agenttest.Unit{Enabled: true, Active: true})
	})
	pve.OnPower(func(vmid int, status string) {
		if vm := vms[vmid]; vm != nil {
			vm.power(status == "running")
		}
	})
	sync := func() {
		t.Helper()
		if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != http.StatusOK {
			t.Fatalf("sync: %d", s)
		}
	}
	sync()

	// App hangt hard af van nginx in LB.
	svc := func(clusterID, name, unit string) string {
		t.Helper()
		var sv depSvcView
		if s := c.do("POST", "/api/v1/services", map[string]any{"cluster_id": clusterID, "name": name, "kind": "web", "unit": unit}, &sv); s != 201 {
			t.Fatalf("dienst %s: %d", name, s)
		}
		return sv.ID
	}
	nginx, php := svc(lb.ID, "nginx", "nginx"), svc(app.ID, "php8.2-fpm", "php8.2-fpm")
	if s := c.do("POST", "/api/v1/dependencies", map[string]any{"from_service_id": php, "to_service_id": nginx, "strength": "hard"}, nil); s != 201 {
		t.Fatalf("pijl: %d", s)
	}
	healthy := func(what string) {
		t.Helper()
		var state string
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			var l, a statusView
			c.do("GET", "/api/v1/clusters/"+lb.ID, nil, &l)
			c.do("GET", "/api/v1/clusters/"+app.ID, nil, &a)
			var bad []string
			rows, err := e.pool.Query(ctx, `SELECT name || ' ' || coalesce(status, 'null') || ' ' || status_reason || ' ' || impact FROM services
				WHERE status IS DISTINCT FROM 'healthy' OR impact <> 'none'`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var b string
				_ = rows.Scan(&b)
				bad = append(bad, b)
			}
			rows.Close()
			if len(bad) == 0 && l.Status == "healthy" && a.Status == "healthy" && len(l.Vips) == 1 &&
				l.Vips[0].OwnerHostname != nil && *l.Vips[0].OwnerHostname == "web01" {
				return
			}
			state = fmt.Sprintf("LB %s (%s), App %s (%s), VIP's %+v, diensten %v", l.Status, l.StatusReason, a.Status, a.StatusReason, l.Vips, bad)
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s: %s", what, state)
			}
		}
	}
	healthy("LB en App gezond, het VIP op web01")

	// Wat het formulier krijgt.
	listURL := "/api/v1/clusters/" + lb.ID + "/disaster-scenarios"
	var list disasterList
	if s := v.do("GET", listURL, nil, &list); s != http.StatusOK {
		t.Fatalf("lijst: %d", s)
	}
	o := list.Options
	if !o.Allowed || o.Slug != "lb" || len(o.Kinds) != 4 || !o.Kinds[0].Available || o.Kinds[0].Key != "host_down" || o.Kinds[3].Key != "sequence" ||
		!slices.Equal(o.Units, []string{"nginx"}) || !slices.Equal(o.Vips, []string{failoverVIP}) || len(list.Items) != 0 {
		t.Fatalf("opties: %+v", o)
	}
	if len(o.Hosts) != 1 || o.Hosts[0].Host != "pve1" || o.Hosts[0].Connection != "Thuislab" ||
		!slices.Equal(o.Hosts[0].Nodes, []string{"web01 (101)", "web02 (102)"}) || !slices.Equal(o.Hosts[0].Others, []string{"app01 (201)"}) {
		t.Fatalf("hosts: %+v", o.Hosts)
	}

	input := func(change func(m map[string]any)) map[string]any {
		m := map[string]any{"name": "nginx weg", "kind": "service_down", "unit": "nginx", "fault_seconds": 60, "recovery_seconds": 120}
		if change != nil {
			change(m)
		}
		return m
	}
	for field, change := range map[string]func(m map[string]any){
		"name": func(m map[string]any) { m["name"] = " " },
		"kind": func(m map[string]any) { m["kind"] = "reboot" },
		"unit": func(m map[string]any) { m["unit"] = "mariadb" },
		"host": func(m map[string]any) {
			m["kind"], m["connection_id"], m["host"], m["fault_seconds"] = "host_down", conn.ID, "pve2", 120
		},
		"fault_seconds":    func(m map[string]any) { m["fault_seconds"] = 5 },
		"recovery_seconds": func(m map[string]any) { m["recovery_seconds"] = 4000 },
	} {
		var fe fieldErr
		if s := c.do("POST", listURL, input(change), &fe); s != http.StatusBadRequest || fe.Field != field {
			t.Errorf("%s: %d %+v", field, s, fe)
		}
	}
	if s := v.do("POST", listURL, input(nil), nil); s != http.StatusForbidden {
		t.Fatalf("viewer maakt een rampoefening: %d", s)
	}
	create := func(m map[string]any) disasterScenarioView {
		t.Helper()
		var sc disasterScenarioView
		if s := c.do("POST", listURL, m, &sc); s != http.StatusCreated {
			t.Fatalf("maken %v: %d", m, s)
		}
		// De storing duurt hier twee seconden; het formulier laat minstens
		// twintig toe.
		if _, err := e.pool.Exec(ctx, `UPDATE disaster_scenarios SET expect = '{"fault_seconds": 2, "recovery_seconds": 10}' WHERE id = $1`, sc.ID); err != nil {
			t.Fatal(err)
		}
		return sc
	}
	ng := create(input(nil))
	if ng.Description != "nginx stoppen op alle nodes" || ng.Unit != "nginx" || ng.Host != nil || ng.LastRun != nil || ng.FaultSeconds != 60 {
		t.Fatalf("nginx weg: %+v", ng)
	}

	// De voorspelling: LB down, en App via de harde afhankelijkheid ook.
	preview := func(id string) disasterPreviewView {
		t.Helper()
		var p disasterPreviewView
		if s := v.do("GET", "/api/v1/disaster-scenarios/"+id+"/preview", nil, &p); s != http.StatusOK {
			t.Fatalf("voorspelling: %d", s)
		}
		return p
	}
	p := preview(ng.ID)
	if !p.OK || !slices.Equal(p.Confirm, []string{"lb"}) || len(p.Targets) != 2 || p.Targets[0].Hostname != "web01" || p.Targets[0].Unit != "nginx" {
		t.Fatalf("voorspelling nginx weg: %+v", p)
	}
	if len(p.Clusters) != 2 || p.Clusters[0].Name != "LB" || p.Clusters[0].Role != "target" || p.Clusters[0].Predicted.Status != "down" ||
		p.Clusters[1].Name != "App" || p.Clusters[1].Role != "dependent" || p.Clusters[1].Predicted.Status != "down" ||
		!strings.Contains(p.Clusters[1].Predicted.Reason, "php8.2-fpm") {
		t.Fatalf("clusters: %+v", p.Clusters)
	}
	for _, name := range []string{"Agent", "Dienst draait", "Omgeving", "Prod niet geraakt", "Clusters gezond", "Verse heartbeats", "Clusterslot", "Testslot", "Probe"} {
		if !slices.ContainsFunc(p.Checks, func(ch struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		}) bool {
			return ch.Name == name && ch.OK
		}) {
			t.Errorf("controle %s ontbreekt of faalt: %+v", name, p.Checks)
		}
	}

	runURL := func(id string) string { return "/api/v1/disaster-scenarios/" + id + "/run" }
	if s := v.do("POST", runURL(ng.ID), map[string]any{"confirm": []string{"lb"}}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer start: %d", s)
	}
	for _, confirm := range [][]string{{}, {"app"}, {"lb", "app"}} {
		var pe precheckErr
		if s := c.do("POST", runURL(ng.ID), map[string]any{"confirm": confirm}, &pe); s != http.StatusConflict || pe.Code != "needs_confirmation" {
			t.Fatalf("bevestiging %v: %d %+v", confirm, s, pe)
		}
	}
	start := func(id string, confirm ...string) disasterRunView {
		t.Helper()
		var run disasterRunView
		if s := c.do("POST", runURL(id), map[string]any{"confirm": confirm}, &run); s != http.StatusAccepted || run.Result != nil ||
			run.JobID == nil || run.Kind != "disaster.run" || run.Disaster == nil {
			t.Fatalf("starten: %d %+v", s, run)
		}
		return run
	}
	finished := func(run disasterRunView) disasterRunView {
		t.Helper()
		for deadline := time.Now().Add(40 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			var j job
			c.do("GET", "/api/v1/jobs/"+*run.JobID, nil, &j)
			if j.Status != "queued" && j.Status != "running" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("rampoefening niet klaar:\n%s", stepLog(t, e, *run.JobID))
			}
		}
		var out disasterRunView
		eventually(t, "run afgerond", func() bool {
			c.do("GET", "/api/v1/test-runs/"+run.ID, nil, &out)
			return out.Result != nil
		})
		return out
	}

	// Tijdens de run staat de voorspelling er al, met de meting onbekend.
	run := start(ng.ID, "lb")
	if run.rows() != "LB: down/unknown unknown\nApp: down/unknown unknown" {
		t.Fatalf("tijdens de run:\n%s", run.rows())
	}
	run = finished(run)
	if *run.Result != "pass" || run.Restored == nil || !*run.Restored ||
		!strings.HasPrefix(run.Summary, "PASS: 2 clusters gedroegen zich zoals voorspeld, alles hersteld na ") {
		t.Fatalf("nginx weg: %+v\n%s", run, stepLog(t, e, *run.JobID))
	}
	if run.rows() != "LB: down/down as_predicted\nApp: down/down as_predicted" {
		t.Fatalf("voorspeld tegen gemeten:\n%s", run.rows())
	}
	d := run.Disaster
	if d.Clusters[0].LongestMs < 1500 || d.Clusters[0].RecoveredMs == nil || d.Clusters[1].RecoveredMs == nil || d.ClearMs == nil ||
		d.RecoveryMs == nil || d.TotalMs == nil || *d.TotalMs != *d.ClearMs+*d.RecoveryMs || len(d.Vips) != 1 || len(d.Vips[0].Segments) < 2 ||
		len(d.TargetResults) != 2 || d.TargetResults[0].Error != "" || d.FaultMs != 2000 || !strings.Contains(d.Clusters[1].Measured.Reason, "php8.2-fpm") {
		t.Fatalf("rapport: %+v", d)
	}
	for _, k := range []string{"fault", "down", "step", "clear", "ready", "return"} {
		if !slices.Contains(run.kinds(), k) {
			t.Errorf("tijdlijn zonder %s: %v", k, run.kinds())
		}
	}
	var jd job
	c.do("GET", "/api/v1/jobs/"+*run.JobID, nil, &jd)
	var steps []string
	for _, st := range jd.Steps {
		steps = append(steps, st.Name+":"+st.Status)
	}
	if !slices.Equal(steps, []string{"Voorcontrole:succeeded", "Storing en meten:succeeded", "Herstellen:succeeded",
		"Herstel controleren:succeeded", "Rapport:succeeded"}) || jd.Status != "succeeded" {
		t.Fatalf("stappen: %s %v", jd.Status, steps)
	}
	for _, h := range []string{"web01", "web02"} {
		calls := strings.Join(hosts[h].Calls(), "\n")
		if !strings.Contains(calls, "systemctl stop nginx") || !strings.Contains(calls, "systemctl start nginx") || strings.Contains(calls, "disable") {
			t.Fatalf("commando's op %s:\n%s", h, calls)
		}
	}
	healthy("na nginx weg")

	// De run staat bij elk gemeten cluster, en bij het scenario.
	var runs struct{ Items []disasterRunView }
	if s := v.do("GET", "/api/v1/test-runs?kind=disaster.run&cluster_id="+app.ID, nil, &runs); s != http.StatusOK || len(runs.Items) != 1 ||
		runs.Items[0].ID != run.ID {
		t.Fatalf("runs van App: %d %+v", s, runs)
	}
	c.do("GET", listURL, nil, &list)
	if len(list.Items) != 1 || list.Items[0].LastRun == nil || list.Items[0].LastRun.ID != run.ID {
		t.Fatalf("laatste run: %+v", list.Items)
	}
	var actions []string
	rows, err := e.pool.Query(ctx, `SELECT action FROM events WHERE subject_id = $1 ORDER BY id`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if !slices.Equal(actions, []string{"disaster.started", "disaster.fault_injected", "disaster.fault_cleared", "disaster.finished"}) {
		t.Fatalf("events: %v", actions)
	}

	// Een prodcluster dat afhangt van wat uitvalt: de voorcontrole weigert.
	if s := c.do("PATCH", "/api/v1/clusters/"+app.ID, map[string]any{"environment": "prod"}, nil); s != http.StatusOK {
		t.Fatalf("App naar prod: %d", s)
	}
	if p := preview(ng.ID); p.OK || !slices.Equal(p.failed(), []string{"Prod niet geraakt"}) {
		t.Fatalf("met App in prod: %v", p.failed())
	}
	var pe precheckErr
	if s := c.do("POST", runURL(ng.ID), map[string]any{"confirm": []string{"lb"}}, &pe); s != http.StatusConflict || pe.Code != "precheck_failed" {
		t.Fatalf("starten met App in prod: %d %+v", s, pe)
	}
	var appList disasterList
	c.do("GET", "/api/v1/clusters/"+app.ID+"/disaster-scenarios", nil, &appList)
	if appList.Options.Allowed || !strings.Contains(appList.Options.Reason, "alleen in lab en test") {
		t.Fatalf("opties op prod: %+v", appList.Options)
	}
	c.do("PATCH", "/api/v1/clusters/"+app.ID, map[string]any{"environment": "test"}, nil)

	// Het VIP valt weg: LB gaat down zoals voorspeld, maar nginx draait
	// gewoon, dus App blijft gezond: WARNING.
	vl := create(map[string]any{"name": "VIP weg", "kind": "vip_loss", "fault_seconds": 60, "recovery_seconds": 120})
	if vl.Description != "keepalived stoppen op alle nodes, zodat het VIP geen houder heeft" {
		t.Fatalf("VIP weg: %+v", vl)
	}
	if p := preview(vl.ID); !p.OK || p.check("VIP") == "" || p.Targets[0].Unit != "keepalived" {
		t.Fatalf("voorspelling VIP weg: %+v", p)
	}
	run = finished(start(vl.ID, "lb"))
	if *run.Result != "warning" || !*run.Restored || !strings.HasPrefix(run.Summary, "WARNING: App bleef gezond, maar zou down worden") {
		t.Fatalf("VIP weg: %+v\n%s", run, stepLog(t, e, *run.JobID))
	}
	if run.rows() != "LB: down/down as_predicted\nApp: down/healthy stayed_healthy" {
		t.Fatalf("VIP weg:\n%s", run.rows())
	}
	healthy("na VIP weg")

	// Lukt het herstel niet, dan blijft de rode balk bij elk gemeten cluster
	// tot Opnieuw herstellen.
	hosts["web01"].FailTimes("systemctl start nginx", "Job for nginx.service failed because the control process exited with error code.", 2)
	run = finished(start(ng.ID, "lb"))
	if *run.Result != "error" || run.Restored == nil || *run.Restored || !strings.Contains(run.Summary, "herstel mislukt") ||
		!strings.Contains(run.Summary, "nginx staat mogelijk nog uit op web01 en web02") {
		t.Fatalf("herstel mislukt: %+v", run)
	}
	for _, id := range []string{lb.ID, app.ID} {
		var fl failoverList
		c.do("GET", "/api/v1/clusters/"+id+"/failover-tests", nil, &fl)
		if len(fl.Unrestored) != 1 || fl.Unrestored[0].ID != run.ID {
			t.Fatalf("rode balk bij %s: %+v", id, fl.Unrestored)
		}
	}
	e.risk.Warmup = 0
	if err := e.risk.Compute(ctx); err != nil {
		t.Fatal(err)
	}
	var rv clusterRiskView
	c.do("GET", "/api/v1/clusters/"+app.ID+"/risk", nil, &rv)
	if f := rv.findings("recovery.unrestored"); len(f) != 1 || !strings.HasPrefix(f[0].Text, "Rampoefening 'nginx weg' van ") {
		t.Fatalf("health score: %+v", f)
	}
	if s := v.do("POST", "/api/v1/test-runs/"+run.ID+"/restore", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer herstelt: %d", s)
	}
	var rj job
	if s := c.do("POST", "/api/v1/test-runs/"+run.ID+"/restore", nil, &rj); s != http.StatusAccepted {
		t.Fatalf("opnieuw herstellen: %d", s)
	}
	if j := waitJob(t, c, rj.ID); j.Status != "succeeded" {
		t.Fatalf("herstellen: %s %s\n%s", j.Status, j.Error, stepLog(t, e, rj.ID))
	}
	c.do("GET", "/api/v1/test-runs/"+run.ID, nil, &run)
	if run.Restored == nil || !*run.Restored {
		t.Fatalf("na opnieuw herstellen: %+v", run)
	}
	healthy("na opnieuw herstellen")

	// pve1 valt uit. Een VM die niet in ClusterForge staat, blijft aan; omdat
	// ClusterForge niet weet waar hij zelf draait, start de oefening dan niet.
	hd := create(map[string]any{"name": "pve1 valt uit", "kind": "host_down", "connection_id": conn.ID, "host": "pve1",
		"fault_seconds": 120, "recovery_seconds": 600})
	if hd.Host == nil || hd.Host.Connection != "Thuislab" || hd.Host.Host != "pve1" || hd.Unit != "" {
		t.Fatalf("pve1 valt uit: %+v", hd)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE disaster_scenarios SET expect = '{"fault_seconds": 4, "recovery_seconds": 10}' WHERE id = $1`, hd.ID); err != nil {
		t.Fatal(err)
	}
	p = preview(hd.ID)
	if p.OK || !slices.Equal(p.failed(), []string{"ClusterForge zelf"}) || !slices.Equal(p.Skipped, []string{"oud-ding (400)"}) {
		t.Fatalf("met oud-ding: %v %+v", p.failed(), p.Skipped)
	}
	pve.SetGuestStatus(400, "stopped", "pve1")
	sync()
	p = preview(hd.ID)
	if !p.OK || !slices.Equal(p.Confirm, []string{"app", "lb"}) || len(p.Targets) != 3 || p.Targets[0].Hostname != "app01" ||
		p.Targets[0].Vmid == nil || *p.Targets[0].Vmid != 201 || len(p.Skipped) != 0 {
		t.Fatalf("pve1: %v %+v", p.failed(), p)
	}
	if len(p.Clusters) != 2 || p.Clusters[0].Role != "target" || p.Clusters[1].Role != "target" ||
		p.Clusters[0].Predicted.Status != "down" || p.Clusters[1].Predicted.Status != "down" {
		t.Fatalf("pve1 clusters: %+v", p.Clusters)
	}
	var ce precheckErr
	if s := c.do("POST", runURL(hd.ID), map[string]any{"confirm": []string{"lb"}}, &ce); s != http.StatusConflict || ce.Code != "needs_confirmation" {
		t.Fatalf("pve1 met één slug: %d %+v", s, ce)
	}
	run = start(hd.ID, "app", "lb")
	// De taak houdt het slot van beide clusters.
	var slots []string
	if err := e.pool.QueryRow(ctx, `SELECT coalesce(cluster_id::text, ''), slot_clusters::text[] FROM jobs WHERE id = $1`, *run.JobID).
		Scan(new(string), &slots); err != nil || !slices.Equal(slots, []string{app.ID}) {
		t.Fatalf("sloten: %v %v", slots, err)
	}
	if p := preview(ng.ID); !slices.Contains(p.failed(), "Clusterslot") || !slices.Contains(p.failed(), "Testslot") {
		t.Fatalf("tijdens pve1: %v", p.failed())
	}
	// Een node zonder heartbeat is pas na 90 s down; de klok van de
	// evaluator gaat zo lang vooruit als de VM's uit staan.
	eventually(t, "de VM's uit", func() bool {
		g, _ := pve.Guest(201)
		return g.Status == "stopped"
	})
	e.evalClock.Store(int64(100 * time.Second))
	e.eval.Kick()
	eventually(t, "App en LB down gezien", func() bool {
		var j job
		c.do("GET", "/api/v1/jobs/"+*run.JobID, nil, &j)
		var lines []string
		for _, st := range j.Steps {
			lines = append(lines, st.Log...)
		}
		log := strings.Join(lines, "\n")
		return strings.Contains(log, "App werd down") && strings.Contains(log, "LB werd down")
	})
	e.evalClock.Store(0)
	run = finished(run)
	if *run.Result != "pass" || !*run.Restored || run.rows() != "App: down/down as_predicted\nLB: down/down as_predicted" {
		t.Fatalf("pve1 valt uit: %+v\n%s\n%s", run, run.rows(), stepLog(t, e, *run.JobID))
	}
	for _, vmid := range []int{101, 102, 201} {
		if g, _ := pve.Guest(vmid); g.Status != "running" {
			t.Fatalf("VM %d: %s", vmid, g.Status)
		}
	}
	if g, _ := pve.Guest(400); g.Status != "stopped" {
		t.Fatalf("oud-ding: %s", g.Status)
	}
	healthy("na pve1")

	// Verwijderen: de runs blijven als geschiedenis.
	if s := c.do("DELETE", "/api/v1/disaster-scenarios/"+hd.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("verwijderen: %d", s)
	}
	var kept disasterRunView
	if s := v.do("GET", "/api/v1/test-runs/"+run.ID, nil, &kept); s != http.StatusOK || kept.Disaster == nil || kept.Disaster.Name != "pve1 valt uit" {
		t.Fatalf("run na verwijderen: %d %+v", s, kept)
	}
}
