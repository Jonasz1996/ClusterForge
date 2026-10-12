package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type recoverPlanView struct {
	Primary string `json:"primary"`
	Begin   string `json:"begin"`
	Blocked bool   `json:"blocked"`
	Hash    string `json:"hash"`
	Nodes   []struct {
		Hostname string `json:"hostname"`
		Answered bool   `json:"answered"`
		State    string `json:"state"`
		Active   string `json:"active"`
		Seqno    int64  `json:"seqno"`
		From     string `json:"from"`
		Action   string `json:"action"`
	} `json:"nodes"`
	Findings []struct {
		Level   string `json:"level"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"findings"`
}

func (p recoverPlanView) actions() string {
	var out []string
	for _, n := range p.Nodes {
		out = append(out, n.Hostname+":"+n.Action)
	}
	return strings.Join(out, " ")
}

func (p recoverPlanView) codes() []string {
	var out []string
	for _, f := range p.Findings {
		out = append(out, f.Level+":"+f.Code)
	}
	return out
}

type componentHealthView struct {
	AutoRecover bool    `json:"auto_recover"`
	DownSince   *string `json:"down_since"`
	DownReason  string  `json:"down_reason"`
	NodesOut    []struct {
		Hostname string `json:"hostname"`
		Reason   string `json:"reason"`
	} `json:"nodes_out"`
	Recovery *struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	} `json:"recovery"`
}

// dbUnitsCall is hoe de agent vraagt welke databases er op de node staan;
// laat het mislukken en de node antwoordt niet.
const dbUnitsCall = "systemctl show --property=Id,LoadState,ActiveState,UnitFileState mariadb.service"

// TestClusterComponentRecover is de acceptatie van mijlpaal 5 van fase 5:
// na een volledige uitval begint ClusterForge de Galera-database van
// Nextcloud vanzelf opnieuw op de node met de nieuwste data, maar alleen als
// alle nodes er zijn en MariaDB nergens meer draait of start. Ontbreekt er
// een, dan wacht het, en kan Jonas zelf opnieuw starten zonder die node.
func TestClusterComponentRecover(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "lab"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	g := &galeraHosts{hosts: map[string]*agenttest.Host{}}
	ids := map[string]string{}
	for i, name := range []string{"nc1", "nc2", "nc3"} {
		ip := "10.0.20.18" + string(rune('6'+i))
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": nc.ID, "primary_ip": ip}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
			writeFile(t, filepath.Join(root, "proc/meminfo"), "MemTotal: 4194304 kB\nMemAvailable: 2097152 kB\n", 0o644)
			h := agenttest.NewHost(root, ip)
			g.hosts[name] = h
			h.SetAfter(g.after(name, h))
			ag.Exec, ag.Root, ag.Collector.Run = h.Exec, root, h.Exec
			ag.Collector.Interfaces, ag.Collector.PrimaryAddress = h.Interfaces, func() string { return ip }
		})
	}
	eventually(t, "facts van de drie nodes", func() bool {
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM node_facts f JOIN nodes n ON n.id = f.node_id WHERE n.cluster_id = $1 AND f.facts->'os'->>'id' = 'debian'", nc.ID).Scan(&n)
		return n == 3
	})
	base := "/api/v1/clusters/" + nc.ID + "/components"
	in := map[string]any{"engine": "mariadb", "nodes": []string{ids["nc1"], ids["nc2"], ids["nc3"]}}
	var plan componentPlanView
	if s := c.do("POST", base+"/plan", in, &plan); s != http.StatusOK {
		t.Fatalf("plan: %d", s)
	}
	in["hash"] = plan.Hash
	var j job
	if s := c.do("POST", base, in, &j); s != http.StatusAccepted {
		t.Fatalf("toevoegen: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	var comps list[componentView]
	c.do("GET", base, nil, &comps)
	cid := comps.Items[0].ID
	health := func() componentHealthView {
		t.Helper()
		var l list[componentHealthView]
		if s := c.do("GET", base, nil, &l); s != http.StatusOK || len(l.Items) != 1 {
			t.Fatalf("onderdelen: %d", s)
		}
		return l.Items[0]
	}
	check := func() {
		t.Helper()
		if err := e.cmps.CheckAll(ctx); err != nil {
			t.Fatalf("bewaking: %v", err)
		}
	}
	recoverJobs := func() []job {
		t.Helper()
		var out []job
		rows, err := e.pool.Query(ctx, "SELECT id FROM jobs WHERE kind = 'component.recover' ORDER BY created_at")
		if err != nil {
			t.Fatal(err)
		}
		var jids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			jids = append(jids, id)
		}
		rows.Close()
		for _, id := range jids {
			var jj job
			c.do("GET", "/api/v1/jobs/"+id, nil, &jj)
			out = append(out, jj)
		}
		return out
	}
	stepNames := func(j job) []string {
		var out []string
		for _, st := range j.Steps {
			out = append(out, st.Name)
		}
		return out
	}
	stepLog := func(j job, name string) string {
		for _, st := range j.Steps {
			if st.Name == name {
				return strings.Join(st.Log, "\n")
			}
		}
		return ""
	}
	began := func() []string {
		var out []string
		for _, name := range []string{"nc1", "nc2", "nc3"} {
			if slices.ContainsFunc(g.hosts[name].Calls(), func(c string) bool { return strings.Contains(c, "galera_new_cluster") }) {
				out = append(out, name)
			}
		}
		return out
	}
	callsBefore := map[string]int{}
	resetCalls := func() {
		for name, h := range g.hosts {
			callsBefore[name] = len(h.Calls())
		}
	}
	beganSince := func() []string {
		var out []string
		for _, name := range []string{"nc1", "nc2", "nc3"} {
			if slices.ContainsFunc(g.hosts[name].Calls()[callsBefore[name]:], func(c string) bool { return strings.Contains(c, "galera_new_cluster") }) {
				out = append(out, name)
			}
		}
		return out
	}
	if b := began(); !slices.Equal(b, []string{"nc1"}) {
		t.Fatalf("uitrol begon op %v", b)
	}

	// Alles draait: geen uitval, vanzelf opnieuw starten staat aan.
	check()
	if h := health(); !h.AutoRecover || h.DownSince != nil || len(h.NodesOut) != 0 || h.Recovery != nil {
		t.Fatalf("gezond: %+v", h)
	}

	// Een gewone stop van alle drie, en na de reboot startte MariaDB nergens:
	// nc2 stopte als laatste en heeft de nieuwste data.
	resetCalls()
	g.crash(agenttest.Unit{Enabled: true, Failed: true}, true, "nc2", map[string]int64{"nc1": 9, "nc2": 11, "nc3": 10})
	e.cmps.RecoverGrace = time.Hour
	check()
	h := health()
	if h.DownSince == nil || !strings.Contains(h.DownReason, "nc1: MariaDB startte niet (failed)") || len(recoverJobs()) != 0 {
		t.Fatalf("na de uitval: %+v", h)
	}
	// Het plan kiest nc2, zonder iets te veranderen.
	var rp recoverPlanView
	if s := c.do("POST", base+"/"+cid+"/recover/plan", nil, &rp); s != http.StatusOK {
		t.Fatalf("plan opnieuw starten: %d", s)
	}
	if rp.Begin != "nc2" || rp.Primary != "" || rp.Blocked || rp.actions() != "nc1:join nc2:begin nc3:join" || len(rp.Findings) != 0 ||
		rp.Nodes[1].Seqno != 11 || rp.Nodes[1].From != "grastate" || rp.Nodes[0].Active != "failed" {
		t.Fatalf("plan: %+v", rp)
	}
	// Na RecoverGrace begint ClusterForge vanzelf.
	e.cmps.RecoverGrace = 0
	check()
	rj := recoverJobs()
	if len(rj) != 1 || rj[0].Title != "Database vanzelf opnieuw starten: MariaDB Galera op Nextcloud" || rj[0].RequestedBy != nil {
		t.Fatalf("taken: %+v", rj)
	}
	j = waitJobLong(t, c, rj[0].ID)
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Posities lezen", "nc2: het cluster beginnen", "nc1: aansluiten", "nc3: aansluiten", "Controleren"}) {
		t.Fatalf("vanzelf: %s %s %v", j.Status, j.Error, stepNames(j))
	}
	if log := stepLog(j, "Posities lezen"); !strings.Contains(log, "nc2: seqno 11 uit grastate, stopte als laatste") || !strings.Contains(log, "nc2 heeft de nieuwste data (seqno 11)") {
		t.Errorf("posities:\n%s", log)
	}
	if b := beganSince(); !slices.Equal(b, []string{"nc2"}) {
		t.Fatalf("het cluster begon op %v", b)
	}
	if h := health(); h.DownSince != nil || h.Recovery == nil || h.Recovery.Status != "succeeded" {
		t.Fatalf("na het opnieuw starten: %+v", h)
	}
	check()
	if h := health(); h.DownSince != nil || len(h.NodesOut) != 0 {
		t.Fatalf("bewaking na het opnieuw starten: %+v", h)
	}

	// Een crash met nc3 weg: geen positie in grastate.dat, en ClusterForge
	// wacht, want nc3 kan de nieuwste data hebben.
	resetCalls()
	g.crash(agenttest.Unit{Enabled: true, Failed: true}, false, "", map[string]int64{"nc1": 20, "nc2": 22, "nc3": 21})
	g.hosts["nc3"].FailTimes(dbUnitsCall, "Failed to connect to bus: No such file or directory", 1<<20)
	e.cmps.AutoBackoff = 0
	check()
	h = health()
	if h.DownSince == nil || !strings.Contains(h.DownReason, "nc3: antwoordt niet") || len(recoverJobs()) != 1 {
		t.Fatalf("zonder nc3: %+v, %d taken", h, len(recoverJobs()))
	}
	c.do("POST", base+"/"+cid+"/recover/plan", nil, &rp)
	if rp.Begin != "" || rp.Blocked || rp.actions() != "nc1:later nc2:later nc3:skip" || !slices.Equal(rp.codes(), []string{"warning:missing", "warning:after_crash"}) ||
		rp.Nodes[0].Seqno != -1 || rp.Nodes[2].Answered {
		t.Fatalf("plan zonder nc3: %+v", rp)
	}
	// Een kijker mag het niet, en een oud plan gaat niet door.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := v.do("POST", base+"/"+cid+"/recover/plan", nil, nil); s != http.StatusForbidden {
		t.Fatalf("kijker plan: %d", s)
	}
	if s := v.do("PATCH", base+"/"+cid, map[string]any{"auto_recover": false}, nil); s != http.StatusForbidden {
		t.Fatalf("kijker schakelaar: %d", s)
	}
	var ae apiErr
	if s := c.do("POST", base+"/"+cid+"/recover", map[string]any{"hash": "oud"}, &ae); s != http.StatusConflict || ae.Code != "plan_stale" {
		t.Fatalf("oud plan: %d %+v", s, ae)
	}
	if s := c.do("POST", base+"/"+cid+"/recover", map[string]any{"hash": rp.Hash}, &j); s != http.StatusAccepted || j.Title != "Database opnieuw starten: MariaDB Galera op Nextcloud" {
		t.Fatalf("opnieuw starten: %d %+v", s, j)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Posities lezen", "nc2: het cluster beginnen", "nc1: aansluiten", "Controleren"}) {
		t.Fatalf("zonder nc3: %s %s %v", j.Status, j.Error, stepNames(j))
	}
	if log := stepLog(j, "Posities lezen"); !strings.Contains(log, "nc1: seqno 20 uit recovery") || !strings.Contains(log, "nc2: seqno 22 uit recovery") {
		t.Errorf("posities na een crash:\n%s", log)
	}
	if b := beganSince(); !slices.Equal(b, []string{"nc2"}) {
		t.Fatalf("het cluster begon op %v", b)
	}

	// nc3 is terug, maar MariaDB startte er niet: het cluster draait, dus
	// komt nc3 in de lijst, en na RecoverGrace sluit hij vanzelf aan.
	g.hosts["nc3"].Unfail(dbUnitsCall)
	e.cmps.RecoverGrace, e.cmps.NodeOutAfter = time.Hour, 0
	check()
	h = health()
	if h.DownSince != nil || len(h.NodesOut) != 1 || h.NodesOut[0].Hostname != "nc3" || h.NodesOut[0].Reason != "MariaDB startte niet (failed)" || len(recoverJobs()) != 2 {
		t.Fatalf("nc3 terug: %+v", h)
	}
	e.cmps.RecoverGrace = 0
	check()
	rj = recoverJobs()
	if len(rj) != 3 {
		t.Fatalf("%d taken", len(rj))
	}
	j = waitJobLong(t, c, rj[2].ID)
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Posities lezen", "nc1: aansluiten", "nc2: aansluiten", "nc3: aansluiten", "Controleren"}) {
		t.Fatalf("nc3 aansluiten: %s %s %v", j.Status, j.Error, stepNames(j))
	}
	if b := beganSince(); !slices.Equal(b, []string{"nc2"}) {
		t.Fatalf("nc3 begon een cluster: %v", b)
	}
	check()
	if h := health(); len(h.NodesOut) != 0 {
		t.Fatalf("nc3 doet weer mee: %+v", h)
	}

	// Na een stroomuitval starten de nodes nog: Galera vindt zijn cluster
	// zelf terug, dus ClusterForge wacht, tot ze te lang hangen.
	e.cmps.HungAfter = time.Hour
	g.crash(agenttest.Unit{Enabled: true, Activating: true}, false, "", map[string]int64{"nc1": 30, "nc2": 30, "nc3": 31})
	check()
	if h := health(); h.DownSince == nil || !strings.Contains(h.DownReason, "MariaDB start nog (activating)") || len(recoverJobs()) != 3 {
		t.Fatalf("starten nog: %+v", h)
	}
	e.cmps.HungAfter = 0
	check()
	rj = recoverJobs()
	if len(rj) != 4 {
		t.Fatalf("%d taken", len(rj))
	}
	j = waitJobLong(t, c, rj[3].ID)
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Posities lezen", "nc3: het cluster beginnen", "nc1: aansluiten", "nc2: aansluiten", "Controleren"}) {
		t.Fatalf("hangend: %s %s %v", j.Status, j.Error, stepNames(j))
	}
	if log := stepLog(j, "Posities lezen"); !strings.Contains(log, "nc1: MariaDB stoppen om de positie te lezen") {
		t.Errorf("hangend:\n%s", log)
	}

	// Met de hand overal gestopt: dat blijft zo. En wie het niet vanzelf
	// wil, zet het uit.
	g.crash(agenttest.Unit{Enabled: true}, true, "nc1", map[string]int64{"nc1": 40, "nc2": 40, "nc3": 40})
	check()
	if h := health(); h.DownSince == nil || len(recoverJobs()) != 4 {
		t.Fatalf("met de hand gestopt: %+v", h)
	}
	var cv componentHealthView
	if s := c.do("PATCH", base+"/"+cid, map[string]any{"auto_recover": false}, &cv); s != http.StatusOK || cv.AutoRecover {
		t.Fatalf("uitzetten: %d %+v", s, cv)
	}
	g.crash(agenttest.Unit{Enabled: true, Failed: true}, true, "nc1", map[string]int64{"nc1": 40, "nc2": 40, "nc3": 40})
	check()
	if len(recoverJobs()) != 4 {
		t.Fatal("vanzelf opnieuw gestart terwijl het uit staat")
	}

	// Het logboek vertelt het verhaal.
	var actions []string
	rows, _ := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE 'cluster.database_%' AND action NOT IN ('cluster.database_added', 'cluster.database_applied') ORDER BY id")
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	want := []string{
		"cluster.database_down", "cluster.database_recovering", "cluster.database_recovered",
		"cluster.database_down", "cluster.database_recovering", "cluster.database_recovered",
		"cluster.database_node_out", "cluster.database_recovering", "cluster.database_recovered",
		"cluster.database_down", "cluster.database_recovering", "cluster.database_recovered",
		"cluster.database_down", "cluster.database_auto_recover",
	}
	if !slices.Equal(actions, want) {
		t.Fatalf("events:\n%s", strings.Join(actions, "\n"))
	}
}
