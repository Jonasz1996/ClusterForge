package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
)

// restartView is de herstart in het rapport van alles herstarten.
type restartView struct {
	Title  string `json:"title"`
	Engine string `json:"engine"`
	Nodes  []struct {
		Hostname string `json:"hostname"`
		BackMs   *int64 `json:"back_ms"`
		Error    string `json:"error"`
	} `json:"nodes"`
	BackMs       *int64   `json:"back_ms"`
	HealthyMs    *int64   `json:"healthy_ms"`
	Began        string   `json:"began"`
	Started      []string `json:"started"`
	RecoverError string   `json:"recover_error"`
}

type restartRunView struct {
	testRunView
	Measurements struct {
		DowntimeMs *int64       `json:"downtime_ms"`
		Writes     *writesView  `json:"writes"`
		Restart    *restartView `json:"restart"`
	} `json:"measurements"`
}

// freshUptime zet de uptime van de machines terug op een dag, zodat de
// volgende herstart te herkennen is.
func freshUptime(t *testing.T, hosts map[string]*agenttest.Host) {
	t.Helper()
	for _, h := range hosts {
		if err := os.WriteFile(filepath.Join(h.Root, "proc/uptime"), []byte("86400.00 1.00\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// runRestart start een failovertest en wacht op zijn rapport.
func runRestart(t *testing.T, c *client, testID, what string) (restartRunView, job) {
	t.Helper()
	var started testRunView
	if s := c.do("POST", "/api/v1/failover-tests/"+testID+"/runs", nil, &started); s != http.StatusAccepted || started.JobID == nil {
		t.Fatalf("%s: starten: %d %+v", what, s, started)
	}
	j := waitJobLong(t, c, *started.JobID)
	var out restartRunView
	eventually(t, what+": run afgerond", func() bool {
		c.do("GET", "/api/v1/test-runs/"+started.ID, nil, &out)
		return out.Result != nil
	})
	return out, j
}

// TestFailoverRestartAllGalera is de acceptatie van mijlpaal 13 deel 2 van
// fase 5: de test 'alles herstarten' herstart nc1, nc2 en nc3 tegelijk.
// Galera komt daarna niet vanzelf terug; de test doet wat de bewaking zou
// doen en begint het cluster opnieuw op de node met de nieuwste data. Het
// rapport zegt hoe lang dat duurde, en dat elke rij die de database voor de
// herstart bevestigde er nog is. Ontbreekt er een, dan is het FAIL.
func TestFailoverRestartAllGalera(t *testing.T) {
	var lose atomic.Bool
	var lab *ncGalera
	lab = newNCGalera(t, func(_ string, _ *agenttest.Host, call string) {
		if strings.Contains(call, "galera_new_cluster") && lose.Load() {
			// Het cluster begint op een node die een bevestigde rij mist.
			if runs := lab.table.Runs(); len(runs) == 1 {
				lab.table.Lose(runs[0], lab.table.Rows(runs[0])[0])
			}
		}
	})
	lab.deploy()
	e, c, nc, g := lab.e, lab.c, lab.nc, lab.g
	// Alle drie zijn weg voor de eerste weer opstart.
	g.bootFor = 1500 * time.Millisecond
	e.cmps.RecoverGrace = 0

	listURL := "/api/v1/clusters/" + nc.ID + "/failover-tests"
	var opts dbFailoverOptions
	c.do("GET", listURL, nil, &opts)
	if ok, _, units := opts.scenario("restart_all"); !ok || !slices.Equal(units, []string{"mariadb"}) {
		t.Fatalf("restart_all: %+v", opts.Options.Scenarios)
	}
	input := map[string]any{
		"name": "alles herstarten", "vip_id": lab.vipID, "scenario": "restart_all", "max_takeover_seconds": 1801,
		"expect_failback": true, "probe": map[string]any{"tcp": map[string]any{"port": 3306}},
	}
	var fe fieldErr
	if s := c.do("POST", listURL, input, &fe); s != http.StatusBadRequest || fe.Field != "max_takeover_seconds" || !strings.Contains(fe.Message, "1800") {
		t.Fatalf("1801 s: %d %+v", s, fe)
	}
	input["max_takeover_seconds"] = 60
	var ft failoverTestView
	if s := c.do("POST", listURL, input, &ft); s != http.StatusCreated || ft.Description != "alle nodes van de database (mariadb) herstarten" {
		t.Fatalf("test maken: %d %+v", s, ft)
	}

	// Zonder vanzelf opnieuw starten komt Galera na de herstart niet terug:
	// de test start niet.
	var comps list[componentView]
	c.do("GET", lab.compBase, nil, &comps)
	cid := comps.Items[0].ID
	c.do("PATCH", lab.compBase+"/"+cid, map[string]any{"auto_recover": false}, nil)
	var pe precheckErr
	if s := c.do("POST", "/api/v1/failover-tests/"+ft.ID+"/runs", nil, &pe); s != http.StatusConflict || !slices.Equal(pe.failed(), []string{"Vanzelf opnieuw starten"}) {
		t.Fatalf("zonder vanzelf opnieuw starten: %d %+v", s, pe)
	}
	c.do("PATCH", lab.compBase+"/"+cid, map[string]any{"auto_recover": true}, nil)

	r, j := runRestart(t, c, ft.ID, "alles herstarten")
	rm, w := r.Measurements.Restart, r.Measurements.Writes
	if *r.Result != "pass" || !strings.HasPrefix(r.Summary, "PASS: na het herstarten van nc1, nc2 en nc3 was MariaDB Galera na ") ||
		!strings.Contains(r.Summary, " weer gezond (ClusterForge begon het cluster opnieuw op nc") ||
		!strings.Contains(r.Summary, "), "+ncVIP+" antwoordde weer na ") || !strings.Contains(r.Summary, " bevestigde rijen waren er nog; ") {
		t.Fatalf("run: %+v\n%+v", r, j.Steps)
	}
	t.Logf("%s", r.Summary)
	if rm == nil || rm.Title != "MariaDB Galera" || rm.Engine != "mariadb" || len(rm.Nodes) != 3 || rm.BackMs == nil || rm.HealthyMs == nil ||
		rm.Began == "" || *rm.HealthyMs < *rm.BackMs || rm.RecoverError != "" {
		t.Fatalf("herstart: %+v", rm)
	}
	for _, n := range rm.Nodes {
		if n.BackMs == nil || n.Error != "" {
			t.Errorf("node: %+v", n)
		}
	}
	if w == nil || w.Node != "nc2" || w.Lost == nil || *w.Lost != 0 || w.Confirmed == 0 || w.CheckError != "" {
		t.Fatalf("schrijftest: %+v", w)
	}
	if d := r.Measurements.DowntimeMs; d == nil || *d == 0 {
		t.Errorf("het VIP was niet weg: %v", d)
	}
	for _, k := range []string{"fault", "node_back", "db_recover", "db_ready", "down", "up", "clear"} {
		if !slices.Contains(r.kinds(), k) {
			t.Errorf("tijdlijn zonder %s: %v", k, r.kinds())
		}
	}
	var steps []string
	for _, st := range j.Steps {
		steps = append(steps, st.Name)
	}
	if want := []string{"Voorcontrole", "Storing en meten", "Herstellen", "Terugkeer controleren", "Rijen nakijken", "Rapport"}; !slices.Equal(steps, want) {
		t.Errorf("stappen: %v", steps)
	}
	if runs := lab.table.Runs(); len(runs) != 0 {
		t.Errorf("rijen na de test: %v", runs)
	}
	lab.h.check()
	lab.h.clusterIs("healthy", "")
	if log := lab.h.log("cluster.database_recovered"); len(log) != 1 || !strings.Contains(log[0], "begon het cluster met de nieuwste data") {
		t.Errorf("logboek: %v", log)
	}

	// Mist de node waarop het cluster begint een bevestigde rij, dan is het
	// FAIL.
	freshUptime(t, g.hosts)
	lose.Store(true)
	r, _ = runRestart(t, c, ft.ID, "rij verloren")
	lose.Store(false)
	if w = r.Measurements.Writes; *r.Result != "fail" || !strings.HasPrefix(r.Summary, "FAIL: 1 bevestigde rij verloren bij schrijven via "+ncVIP) ||
		w == nil || w.Lost == nil || *w.Lost != 1 || r.Restored == nil || !*r.Restored {
		t.Fatalf("rij verloren: %+v %+v", r, w)
	}
	lab.h.check()
	lab.h.clusterIs("healthy", "")
}

// TestFailoverRestartAllPatroni: na een herstart van alle nodes kiest
// Patroni zelf weer een primary; ClusterForge hoeft niets te doen en meet
// alleen.
func TestFailoverRestartAllPatroni(t *testing.T) {
	lab := newPatroniHosts(t)
	e, c, g, nc, group := lab.e, lab.c, lab.g, lab.nc, lab.group
	const pgVIP = "10.0.20.190"
	e.fo.Prober = groupProber{group}
	lab.deploy(t)
	freshUptime(t, g.hosts)

	table := agenttest.NewProbeTable()
	byIP := map[string]*agenttest.Host{}
	for _, h := range g.hosts {
		byIP[h.Address] = h
	}
	network := newVIPNetwork(t, group, byIP, pgVIP)
	for _, h := range g.hosts {
		h.Locked(func() {
			h.Network = network.route
			h.Postgres.Probe = table
		})
	}
	h := healthHelpers{t: t, e: e, c: c, clusterID: nc.ID, base: "/api/v1/clusters/" + nc.ID + "/components"}
	eventually(t, "het VIP op nc1", func() bool { return group.Owner(pgVIP) == g.hosts["nc1"] })
	h.check()
	h.clusterIs("healthy", "")

	listURL := "/api/v1/clusters/" + nc.ID + "/failover-tests"
	var opts dbFailoverOptions
	c.do("GET", listURL, nil, &opts)
	vipID := ""
	for _, v := range opts.Options.Vips {
		if v.Address == pgVIP {
			vipID = v.ID
		}
	}
	var ft failoverTestView
	if s := c.do("POST", listURL, map[string]any{
		"name": "alles herstarten", "vip_id": vipID, "scenario": "restart_all", "max_takeover_seconds": 60,
		"expect_failback": false, "probe": map[string]any{"tcp": map[string]any{"port": 5432}},
	}, &ft); s != http.StatusCreated || ft.Description != "alle nodes van de database (patroni) herstarten" {
		t.Fatalf("test maken: %d %+v", s, ft)
	}
	r, j := runRestart(t, c, ft.ID, "alles herstarten")
	rm, w := r.Measurements.Restart, r.Measurements.Writes
	if *r.Result != "pass" || !strings.HasPrefix(r.Summary, "PASS: na het herstarten van nc1, nc2 en nc3 was PostgreSQL met Patroni na ") ||
		strings.Contains(r.Summary, "ClusterForge begon") {
		t.Fatalf("run: %+v\n%+v", r, j.Steps)
	}
	t.Logf("%s", r.Summary)
	if rm == nil || rm.Engine != "postgresql" || rm.HealthyMs == nil || rm.Began != "" || len(rm.Started) != 0 {
		t.Fatalf("herstart: %+v", rm)
	}
	if w == nil || w.Lost == nil || *w.Lost != 0 || w.Confirmed == 0 {
		t.Fatalf("schrijftest: %+v", w)
	}
	h.check()
	h.clusterIs("healthy", "")
}
