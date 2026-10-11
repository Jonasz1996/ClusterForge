package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
)

type disasterStepView struct {
	Step      int    `json:"step"`
	AtSeconds int    `json:"at_seconds"`
	Action    string `json:"action"`
	On        string `json:"on"`
	Text      string `json:"text"`
	Nodes     []struct {
		Hostname string `json:"hostname"`
		CutOff   bool   `json:"cut_off"`
	} `json:"nodes"`
	RanMs   *int64 `json:"ran_ms"`
	Results []struct {
		Hostname string `json:"hostname"`
		Error    string `json:"error"`
	} `json:"results"`
}

// TestDisasterFaults is de acceptatie van mijlpaal 12 van fase 3: met
// protocol 6 zet de agent vaste storingen aan met een dodemansknop, en een
// rampoefening is een reeks van stappen. VRRP blokkeren op alle nodes geeft
// split-brain, en stopt ClusterForge midden in de test, dan ruimt de agent
// de storing zelf op. link_down op de interface naar ClusterForge komt na de
// bovengrens vanzelf terug, ook na een herstart van de machine. Een reeks
// van drie stappen loopt in volgorde.
func TestDisasterFaults(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	group := &agenttest.Group{Takeover: 200 * time.Millisecond}
	e.dis.Prober = groupProber{group}
	// Een lease van een seconde, elke 200 ms verlengd.
	e.dis.Lease, e.dis.RenewInterval = time.Second, 200*time.Millisecond

	var lb cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "lab"}, &lb)
	c.do("POST", "/api/v1/clusters/"+lb.ID+"/vips", map[string]any{"address": failoverVIP}, nil)
	hosts := map[string]*agenttest.Host{}
	vms := map[string]*vmSim{}
	roots := map[string]string{}
	ids := map[string]string{}
	for i, name := range []string{"web01", "web02"} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": lb.ID}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		ip := fmt.Sprintf("10.0.30.%d", 11+i)
		vm := &vmSim{}
		vm.stop = startAgentWith(t, e, c, n.ID, fmt.Sprintf("%032x", 500+i), &addrs{}, func(ag *agent.Agent, root string) {
			h := agenttest.NewHost(root, ip)
			writeFile(t, filepath.Join(root, "etc/keepalived/keepalived.conf"), keepalivedConf(150-10*i), 0o640)
			if _, err := h.Exec(ctx, "apt-get", "install", "-y", "keepalived", "nginx"); err != nil {
				t.Fatal(err)
			}
			// nft en tc staan op de machine.
			writeFile(t, filepath.Join(root, "usr/sbin/nft"), "", 0o755)
			writeFile(t, filepath.Join(root, "usr/sbin/tc"), "", 0o755)
			ag.Exec, ag.Root, ag.Collector.Run, ag.Collector.Addresses = h.Exec, root, h.Exec, h.Addresses
			ag.Collector.Interfaces, ag.Collector.PrimaryAddress = h.Interfaces, func() string { return ip }
			vm.host, vm.tmpl = h, ag
			hosts[name], roots[name] = h, root
			group.Join(h)
		})
		vms[name] = vm
		t.Cleanup(vm.shutdown)
	}
	faultsFile := func(name string) bool {
		_, err := os.Stat(filepath.Join(roots[name], "var/lib/clusterforge/faults.json"))
		return !errors.Is(err, fs.ErrNotExist)
	}
	healthy := func(what string) {
		t.Helper()
		eventually(t, what, func() bool {
			var l statusView
			c.do("GET", "/api/v1/clusters/"+lb.ID, nil, &l)
			return l.Status == "healthy" && len(l.Vips) == 1 && l.Vips[0].OwnerHostname != nil && *l.Vips[0].OwnerHostname == "web01"
		})
	}
	healthy("LB gezond, het VIP op web01")

	// Het formulier kent de nodes, hun interfaces en wat er kan.
	listURL := "/api/v1/clusters/" + lb.ID + "/disaster-scenarios"
	var list struct {
		Options struct {
			Kinds []struct {
				Key       string `json:"key"`
				Available bool   `json:"available"`
			} `json:"kinds"`
			Nodes []struct {
				Hostname   string   `json:"hostname"`
				Owner      bool     `json:"owner"`
				Faults     bool     `json:"faults"`
				Nft        bool     `json:"nft"`
				Tc         bool     `json:"tc"`
				Interfaces []string `json:"interfaces"`
				Primary    string   `json:"primary"`
				Units      []string `json:"units"`
			} `json:"nodes"`
			Actions []struct {
				Key       string `json:"key"`
				Available bool   `json:"available"`
			} `json:"actions"`
		} `json:"options"`
	}
	eventually(t, "facts met nft en tc", func() bool {
		c.do("GET", listURL, nil, &list)
		return len(list.Options.Nodes) == 2 && list.Options.Nodes[0].Nft && list.Options.Nodes[1].Tc
	})
	o := list.Options
	if n := o.Nodes[0]; n.Hostname != "web01" || !n.Owner || !n.Faults || n.Primary != "eth0" || !slices.Equal(n.Interfaces, []string{"eth0"}) ||
		!slices.Equal(n.Units, []string{"keepalived", "nginx"}) || o.Nodes[1].Owner {
		t.Fatalf("nodes: %+v", o.Nodes)
	}
	if len(o.Kinds) != 4 || o.Kinds[3].Key != "sequence" || !o.Kinds[3].Available || len(o.Actions) != 6 ||
		slices.ContainsFunc(o.Actions, func(a struct {
			Key       string `json:"key"`
			Available bool   `json:"available"`
		}) bool {
			return !a.Available
		}) {
		t.Fatalf("soorten en acties: %+v %+v", o.Kinds, o.Actions)
	}

	// Ongeldige stappen.
	seq := func(fault int, steps ...map[string]any) map[string]any {
		return map[string]any{"name": "reeks", "kind": "sequence", "fault_seconds": fault, "recovery_seconds": 60, "steps": steps}
	}
	for msg, in := range map[string]map[string]any{
		"minstens één stap": seq(60),
		"ten vroegste op":   seq(300, map[string]any{"at_seconds": 0, "action": "link_down", "on": "node", "node_id": ids["web02"], "interface": "eth0"}),
		"geen interface":    seq(60, map[string]any{"at_seconds": 0, "action": "netem", "on": "all", "interface": "eth7", "delay_ms": 100}),
	} {
		var fe fieldErr
		if s := c.do("POST", listURL, in, &fe); s != http.StatusBadRequest || fe.Field != "steps" || !strings.Contains(fe.Message, msg) {
			t.Errorf("%s: %d %+v", msg, s, fe)
		}
	}
	create := func(name string, fault int, in map[string]any) disasterScenarioView {
		t.Helper()
		in["name"] = name
		var sc disasterScenarioView
		if s := c.do("POST", listURL, in, &sc); s != http.StatusCreated {
			t.Fatalf("maken %s: %d", name, s)
		}
		// Hier duurt de storing maar een paar seconden.
		if _, err := e.pool.Exec(ctx, `UPDATE disaster_scenarios SET expect = jsonb_build_object('fault_seconds', $2::int, 'recovery_seconds', 10)
			WHERE id = $1`, sc.ID, fault); err != nil {
			t.Fatal(err)
		}
		return sc
	}
	var preview struct {
		disasterPreviewView
		Steps []disasterStepView `json:"steps"`
	}
	previewOf := func(id string) {
		t.Helper()
		preview.Steps = nil
		if s := c.do("GET", "/api/v1/disaster-scenarios/"+id+"/preview", nil, &preview); s != http.StatusOK {
			t.Fatalf("voorspelling: %d", s)
		}
	}
	var run struct {
		disasterRunView
		Disaster *struct {
			Steps    []disasterStepView    `json:"steps"`
			Clusters []disasterClusterView `json:"clusters"`
		} `json:"disaster"`
	}
	start := func(id string) {
		t.Helper()
		run.Disaster = nil
		if s := c.do("POST", "/api/v1/disaster-scenarios/"+id+"/run", map[string]any{"confirm": []string{"lb"}}, &run); s != http.StatusAccepted {
			t.Fatalf("starten: %d %+v", s, run)
		}
	}
	finished := func() {
		t.Helper()
		if j := waitJob(t, c, *run.JobID); j.Status == "queued" || j.Status == "running" {
			t.Fatalf("niet klaar: %s", j.Status)
		}
		eventually(t, "run afgerond", func() bool {
			c.do("GET", "/api/v1/test-runs/"+run.ID, nil, &run)
			return run.Result != nil
		})
	}
	rows := func() string {
		var out []string
		for _, cl := range run.Disaster.Clusters {
			out = append(out, fmt.Sprintf("%s: %s/%s %s", cl.Name, cl.Predicted.Status, cl.Measured.Status, cl.Match))
		}
		return strings.Join(out, "\n")
	}

	// 1. VRRP blokkeren op alle nodes: elke node neemt het VIP, dus
	// split-brain, zoals voorspeld.
	vrrp := create("VRRP weg", 3, seq(60, map[string]any{"at_seconds": 0, "action": "vrrp_block", "on": "all", "interface": "eth0"}))
	if vrrp.Description != "0 s: VRRP blokkeren op alle nodes" {
		t.Fatalf("beschrijving: %+v", vrrp)
	}
	previewOf(vrrp.ID)
	if !preview.OK || len(preview.Steps) != 1 || len(preview.Steps[0].Nodes) != 2 || preview.Clusters[0].Predicted.Status != "split_brain" ||
		!strings.Contains(preview.Clusters[0].Predicted.Reason, "VRRP geblokkeerd op web01 en web02") {
		t.Fatalf("voorspelling VRRP weg: %v %+v %+v", preview.failed(), preview.Steps, preview.Clusters)
	}
	for _, name := range []string{"Stappen", "Agent", "Gereedschap", "Geen storing actief", "Omgeving", "Clusterslot", "Testslot", "Probe"} {
		if preview.check(name) == "" {
			t.Errorf("controle %s ontbreekt: %+v", name, preview.Checks)
		}
	}
	start(vrrp.ID)
	finished()
	if *run.Result != "pass" || !*run.Restored || rows() != "LB: split_brain/split_brain as_predicted" {
		t.Fatalf("VRRP weg: %s %s\n%s\n%s", *run.Result, run.Summary, rows(), stepLog(t, e, *run.JobID))
	}
	if st := run.Disaster.Steps; len(st) != 1 || st[0].RanMs == nil || len(st[0].Results) != 2 || st[0].Results[0].Error != "" {
		t.Fatalf("stappen: %+v", st)
	}
	if !slices.Contains(run.kinds(), "fault") || !slices.Contains(run.kinds(), "step") {
		t.Fatalf("tijdlijn: %v", run.kinds())
	}
	if len(group.Splits()) == 0 {
		t.Fatal("geen split-brain gezien")
	}
	for _, h := range []string{"web01", "web02"} {
		calls := strings.Join(hosts[h].Calls(), "\n")
		if !strings.Contains(calls, "nft -f "+filepath.Join(roots[h], "var/lib/clusterforge/cf-fault.nft")) ||
			!strings.HasSuffix(calls, "nft delete table inet cf_fault") || hosts[h].VRRPBlocked() || faultsFile(h) {
			t.Fatalf("%s:\n%s", h, calls)
		}
	}
	healthy("na VRRP weg")

	// 2. ClusterForge stopt midden in de test, zonder noodherstel: de
	// agents verlengen niet meer en heffen de blokkade zelf op na de lease.
	e.stopRunner()
	e.dis.EmergencyTimeout = time.Nanosecond
	e.startRunner()
	if _, err := e.pool.Exec(ctx, `UPDATE disaster_scenarios SET expect = '{"fault_seconds": 60, "recovery_seconds": 10}' WHERE id = $1`, vrrp.ID); err != nil {
		t.Fatal(err)
	}
	start(vrrp.ID)
	eventually(t, "VRRP geblokkeerd", func() bool { return hosts["web01"].VRRPBlocked() && hosts["web02"].VRRPBlocked() })
	e.stopRunner()
	stopped := time.Now()
	eventually(t, "de agents heffen de blokkade zelf op", func() bool {
		return !hosts["web01"].VRRPBlocked() && !hosts["web02"].VRRPBlocked() && !faultsFile("web01") && !faultsFile("web02")
	})
	if d := time.Since(stopped); d > 5*time.Second {
		t.Fatalf("opgeheven na %s", d)
	}
	healthy("na de lease")
	e.dis.EmergencyTimeout = 3 * time.Second
	e.startRunner()
	finished()
	if *run.Result != "error" || !*run.Restored || !strings.Contains(run.Summary, "meting onderbroken door een herstart van ClusterForge") {
		t.Fatalf("na de herstart: %s %s\n%s", *run.Result, run.Summary, stepLog(t, e, *run.JobID))
	}

	// 3. link_down op de interface naar ClusterForge: de agent antwoordt
	// eerst en zet de interface na de bovengrens zelf weer aan, ook als
	// ClusterForge intussen stopt.
	link := create("eth0 weg", 2, seq(60, map[string]any{"at_seconds": 0, "action": "link_down", "on": "node", "node_id": ids["web02"], "interface": "eth0"}))
	previewOf(link.ID)
	if !preview.OK || !preview.Steps[0].Nodes[0].CutOff || !strings.Contains(preview.check("Beheerinterface"), "eth0 op web02 (2 s)") {
		t.Fatalf("voorspelling eth0 weg: %v %+v", preview.failed(), preview.Checks)
	}
	e.stopRunner()
	e.dis.EmergencyTimeout = time.Nanosecond
	e.startRunner()
	start(link.ID)
	eventually(t, "eth0 down op web02", func() bool { return hosts["web02"].LinkDown("eth0") })
	e.stopRunner()
	eventually(t, "eth0 weer aan na de bovengrens", func() bool { return !hosts["web02"].LinkDown("eth0") && !faultsFile("web02") })
	e.dis.EmergencyTimeout = 3 * time.Second
	e.startRunner()
	finished()
	if !*run.Restored {
		t.Fatalf("eth0 weg: %s %s", *run.Result, run.Summary)
	}

	// Na een herstart van de machine ruimt de agent haar bij de start op,
	// lang voor de bovengrens.
	if _, err := e.pool.Exec(ctx, `UPDATE disaster_scenarios SET expect = '{"fault_seconds": 60, "recovery_seconds": 10}' WHERE id = $1`, link.ID); err != nil {
		t.Fatal(err)
	}
	e.stopRunner()
	e.dis.EmergencyTimeout = time.Nanosecond
	e.startRunner()
	start(link.ID)
	eventually(t, "eth0 down op web02", func() bool { return hosts["web02"].LinkDown("eth0") })
	e.stopRunner()
	vms["web02"].power(false)
	if !hosts["web02"].LinkDown("eth0") || !faultsFile("web02") {
		t.Fatal("eth0 kwam vanzelf terug")
	}
	vms["web02"].power(true)
	eventually(t, "eth0 aan na de herstart", func() bool { return !hosts["web02"].LinkDown("eth0") && !faultsFile("web02") })
	e.dis.EmergencyTimeout = 3 * time.Second
	e.startRunner()
	finished()
	if !*run.Restored {
		t.Fatalf("eth0 weg na herstart: %s %s", *run.Result, run.Summary)
	}
	healthy("na eth0 weg")

	// 4. Een reeks van drie stappen loopt in volgorde: poort 8080 dicht op
	// de VIP-houder, vertraging op de andere node en dan keepalived weg op
	// web02. Het VIP blijft op web01; LB is verminderd.
	three := create("drie stappen", 3, seq(60,
		map[string]any{"at_seconds": 2, "action": "service_stop", "on": "node", "node_id": ids["web02"], "unit": "keepalived"},
		map[string]any{"at_seconds": 0, "action": "port_block", "on": "owner", "port": 8080},
		map[string]any{"at_seconds": 1, "action": "netem", "on": "others", "interface": "eth0", "delay_ms": 100},
	))
	if three.Description != "0 s: TCP-poort 8080 dicht op de VIP-houder; 1 s: 100 ms vertraging op eth0 van de andere nodes; 2 s: keepalived stoppen op web02" {
		t.Fatalf("drie stappen: %q", three.Description)
	}
	previewOf(three.ID)
	if !preview.OK || len(preview.Steps) != 3 || preview.Steps[0].Nodes[0].Hostname != "web01" || preview.Steps[1].Nodes[0].Hostname != "web02" ||
		preview.Clusters[0].Predicted.Status != "degraded" || !strings.Contains(preview.Clusters[0].Predicted.Reason, "na stap 3: keepalived draait niet op web02") {
		t.Fatalf("voorspelling drie stappen: %v %+v %+v", preview.failed(), preview.Steps, preview.Clusters)
	}
	start(three.ID)
	finished()
	if *run.Result != "pass" || rows() != "LB: degraded/degraded as_predicted" {
		t.Fatalf("drie stappen: %s %s\n%s\n%s", *run.Result, run.Summary, rows(), stepLog(t, e, *run.JobID))
	}
	st := run.Disaster.Steps
	if len(st) != 3 || st[0].RanMs == nil || st[1].RanMs == nil || st[2].RanMs == nil || *st[0].RanMs > 500 ||
		*st[1].RanMs < 900 || *st[2].RanMs < 1900 || *st[1].RanMs > *st[2].RanMs {
		t.Fatalf("tijdstippen: %+v", st)
	}
	var faults []string
	for _, ev := range run.Timeline {
		if ev.Kind == "fault" {
			faults = append(faults, ev.Text)
		}
	}
	if !slices.Equal(faults, []string{"Stap 1: TCP-poort 8080 dicht op de VIP-houder", "Stap 2: 100 ms vertraging op eth0 van de andere nodes",
		"Stap 3: keepalived stoppen op web02"}) {
		t.Fatalf("tijdlijn: %v", faults)
	}
	w2 := strings.Join(hosts["web02"].Calls(), "\n")
	i, j, k := strings.LastIndex(w2, "tc qdisc replace dev eth0 root netem delay 100ms"), strings.LastIndex(w2, "systemctl stop keepalived"),
		strings.LastIndex(w2, "tc qdisc del dev eth0 root")
	if i < 0 || j < i || k < j || !strings.HasSuffix(w2, "systemctl start keepalived") || hosts["web02"].Qdisc("eth0") != "noqueue" {
		t.Fatalf("web02:\n%s", w2)
	}
	if w1 := strings.Join(hosts["web01"].Calls(), "\n"); !strings.HasSuffix(w1, "nft delete table inet cf_fault") || hosts["web01"].Blocked(8080) {
		t.Fatalf("web01:\n%s", w1)
	}
	healthy("na drie stappen")

	// Het logboek heeft elk aanzetten en opheffen, het verlengen niet.
	var n, renew int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE payload->>'command' = 'fault.inject'),
		count(*) FILTER (WHERE payload->>'command' = 'fault.renew') FROM events WHERE action = 'agent.command'`).Scan(&n, &renew); err != nil {
		t.Fatal(err)
	}
	if n < 6 || renew != 0 {
		t.Fatalf("logboek: %d inject, %d renew", n, renew)
	}
}
