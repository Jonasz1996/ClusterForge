package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// writesView is de schrijftest in het rapport van een run.
type writesView struct {
	Node       string  `json:"node"`
	Title      string  `json:"title"`
	Vip        string  `json:"vip"`
	Port       int     `json:"port"`
	DowntimeMs *int64  `json:"downtime_ms"`
	Recovered  bool    `json:"recovered"`
	Attempts   int     `json:"attempts"`
	Failed     int     `json:"failed"`
	Confirmed  int     `json:"confirmed"`
	Lost       *int    `json:"lost"`
	LostSeqs   []int64 `json:"lost_seqs"`
	CheckError string  `json:"check_error"`
	Segments   []struct {
		OK bool `json:"ok"`
	} `json:"segments"`
}

type dbRunView struct {
	testRunView
	Measurements struct {
		TakeoverNode string      `json:"takeover_node"`
		ReturnedTo   string      `json:"returned_to"`
		Writes       *writesView `json:"writes"`
	} `json:"measurements"`
}

type dbFailoverOptions struct {
	Options struct {
		Scenarios []struct {
			Key       string   `json:"key"`
			Available bool     `json:"available"`
			Reason    string   `json:"reason"`
			Units     []string `json:"units"`
		} `json:"scenarios"`
		Vips []struct {
			ID       string `json:"id"`
			Address  string `json:"address"`
			Database string `json:"database"`
			OwnVip   bool   `json:"own_vip"`
			Probe    struct {
				TCP *struct {
					Port int `json:"port"`
				} `json:"tcp"`
			} `json:"probe"`
		} `json:"vips"`
	} `json:"options"`
}

func (o dbFailoverOptions) scenario(key string) (available bool, reason string, units []string) {
	for _, s := range o.Options.Scenarios {
		if s.Key == key {
			return s.Available, s.Reason, s.Units
		}
	}
	return false, "", nil
}

// vipNetwork routeert een VIP naar de machine die het in de groep heeft.
// De houder staat in een kopie die een lus bijhoudt: een machine vraagt het
// netwerk terwijl ze op slot is, en de groep zet de machines op slot.
type vipNetwork struct {
	owners atomic.Pointer[map[string]*agenttest.Host]
	nodes  map[string]*agenttest.Host
}

func newVIPNetwork(t *testing.T, g *agenttest.Group, nodes map[string]*agenttest.Host, vips ...string) *vipNetwork {
	n := &vipNetwork{nodes: nodes}
	look := func() {
		m := map[string]*agenttest.Host{}
		for _, v := range vips {
			m[v] = g.Owner(v)
		}
		n.owners.Store(&m)
	}
	look()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				look()
			}
		}
	})
	t.Cleanup(func() { cancel(); wg.Wait() })
	return n
}

func (n *vipNetwork) route(addr string) *agenttest.Host {
	host, _, _ := net.SplitHostPort(addr)
	if h, ok := (*n.owners.Load())[host]; ok {
		return h
	}
	return n.nodes[host]
}

// TestFailoverDatabaseGalera is de acceptatie van mijlpaal 13 van fase 5
// voor MariaDB: een failovertest op het Nextcloud-cluster stopt de database
// op de eigenaar van .185. Ondertussen schrijft nc2 elke tik een rij via het
// VIP; het rapport zegt hoe lang schrijven niet lukte en dat er niets
// verloren ging. Verdwijnt er een bevestigde rij, dan is het FAIL.
func TestFailoverDatabaseGalera(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	group := &agenttest.Group{Takeover: 200 * time.Millisecond}
	e.fo.Prober = groupProber{group}

	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "lab"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	var vip struct {
		ID string `json:"id"`
	}
	c.do("POST", "/api/v1/clusters/"+nc.ID+"/vips", map[string]any{"address": ncVIP}, &vip)

	g := &galeraHosts{hosts: map[string]*agenttest.Host{}}
	byIP := map[string]*agenttest.Host{}
	ids := map[string]string{}
	table := agenttest.NewProbeTable()
	var lose atomic.Bool
	for i, name := range []string{"nc1", "nc2", "nc3"} {
		ip := fmt.Sprintf("10.0.20.%d", 186+i)
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": nc.ID, "primary_ip": ip}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
			writeFile(t, filepath.Join(root, "proc/meminfo"), "MemTotal: 4194304 kB\nMemAvailable: 2097152 kB\n", 0o644)
			writeFile(t, filepath.Join(root, "etc/keepalived/keepalived.conf"), ncKeepalived(name, 150-10*i), 0o640)
			h := agenttest.NewHost(root, ip)
			if _, err := h.Exec(ctx, "apt-get", "install", "-y", "keepalived"); err != nil {
				t.Fatal(err)
			}
			h.SetUnit("apache2", agenttest.Unit{Enabled: true, Active: true})
			g.hosts[name], byIP[ip] = h, h
			base := g.after(name, h)
			h.SetAfter(func(call string, err error) {
				base(call, err)
				if err != nil {
					return
				}
				switch call {
				case "systemctl start mariadb":
					g.restart(name, h)
					if lose.Load() {
						// Een bevestigde rij is na de herstart weg.
						if runs := table.Runs(); len(runs) == 1 {
							table.Lose(runs[0], table.Rows(runs[0])[0])
						}
					}
				case "mariadb --batch --skip-print-query-on-error":
					// Galera repliceert gebruikers en databases naar de andere
					// nodes.
					var users []agenttest.MariaUser
					var dbs []agenttest.Database
					h.Locked(func() { users, dbs = slices.Clone(h.MariaDB.Users), slices.Clone(h.MariaDB.Databases) })
					for other, oh := range g.hosts {
						if other != name {
							oh.Locked(func() {
								if oh.MariaDB != nil {
									oh.MariaDB.Users, oh.MariaDB.Databases = slices.Clone(users), slices.Clone(dbs)
								}
							})
						}
					}
				}
			})
			ag.Exec, ag.ExecStdin, ag.Root, ag.Collector.Run, ag.Collector.Addresses = h.Exec, h.ExecStdin, root, h.Exec, h.Addresses
			ag.Collector.Interfaces, ag.Collector.PrimaryAddress = h.Interfaces, func() string { return ip }
			ag.Dial, ag.ProbeInterval = h.Dial, 50*time.Millisecond
			group.Join(h)
		})
	}
	network := newVIPNetwork(t, group, byIP, ncVIP)
	for _, h := range g.hosts {
		h.Locked(func() { h.Network = network.route })
	}
	eventually(t, "facts met keepalived van de drie nodes", func() bool {
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM node_facts f JOIN nodes n ON n.id = f.node_id WHERE n.cluster_id = $1 AND f.facts->'keepalived'->>'active' = 'active'", nc.ID).Scan(&n)
		return n == 3
	})

	// Een failovertest op het VIP van de app kan nog, zonder schrijftest.
	listURL := "/api/v1/clusters/" + nc.ID + "/failover-tests"
	var opts dbFailoverOptions
	c.do("GET", listURL, nil, &opts)
	if ok, reason, _ := opts.scenario("database_stop"); ok || reason != "dit cluster heeft geen onderdeel Database met een VIP" {
		t.Fatalf("database_stop zonder database: %v %q", ok, reason)
	}

	// MariaDB Galera met het VIP van Nextcloud.
	compBase := "/api/v1/clusters/" + nc.ID + "/components"
	in := map[string]any{"engine": "mariadb", "nodes": []string{ids["nc1"], ids["nc2"], ids["nc3"]}, "vip_id": vip.ID}
	var plan componentPlanView
	if s := c.do("POST", compBase+"/plan", in, &plan); s != http.StatusOK || plan.Blocked {
		t.Fatalf("plan: %d %+v", s, plan)
	}
	in["hash"] = plan.Hash
	var j job
	if s := c.do("POST", compBase, in, &j); s != http.StatusAccepted {
		t.Fatalf("toevoegen: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	for _, h := range g.hosts {
		h.Locked(func() { h.MariaDB.Probe = table })
	}
	h := healthHelpers{t: t, e: e, c: c, clusterID: nc.ID, base: compBase}
	h.check()
	h.clusterIs("healthy", "")

	c.do("GET", listURL, nil, &opts)
	if ok, _, units := opts.scenario("database_stop"); !ok || !slices.Equal(units, []string{"mariadb"}) {
		t.Fatalf("database_stop: %+v", opts.Options.Scenarios)
	}
	if v := opts.Options.Vips; len(v) != 1 || v[0].Database != "MariaDB Galera" || v[0].OwnVip {
		t.Fatalf("VIP's: %+v", v)
	}

	input := map[string]any{
		"name": "database stoppen", "vip_id": vip.ID, "scenario": "database_stop", "max_takeover_seconds": 20,
		"expect_failback": true, "probe": map[string]any{"tcp": map[string]any{"port": 3306}},
	}
	var ft failoverTestView
	if s := c.do("POST", listURL, input, &ft); s != http.StatusCreated || ft.Description != "de database (mariadb) stoppen op de eigenaar" {
		t.Fatalf("test maken: %d %+v", s, ft)
	}
	run := func(what string) dbRunView {
		t.Helper()
		var started testRunView
		if s := c.do("POST", "/api/v1/failover-tests/"+ft.ID+"/runs", nil, &started); s != http.StatusAccepted || started.JobID == nil {
			t.Fatalf("%s: starten: %d %+v", what, s, started)
		}
		waitJobLong(t, c, *started.JobID)
		var out dbRunView
		eventually(t, what+": run afgerond", func() bool {
			c.do("GET", "/api/v1/test-runs/"+started.ID, nil, &out)
			return out.Result != nil
		})
		return out
	}
	backOnNC1 := func(what string) {
		t.Helper()
		eventually(t, what+": het VIP terug op nc1", func() bool { return group.Owner(ncVIP) == g.hosts["nc1"] })
		h.check()
		h.clusterIs("healthy", "")
	}

	// Een node die niet meedoet: de database is niet gezond, de test start
	// niet.
	g.leave("nc3", g.hosts["nc3"])
	var pe precheckErr
	if s := c.do("POST", "/api/v1/failover-tests/"+ft.ID+"/runs", nil, &pe); s != http.StatusConflict || pe.Code != "precheck_failed" ||
		!slices.Contains(pe.failed(), "Database gezond") {
		t.Fatalf("zonder nc3: %d %+v", s, pe)
	}
	g.join("nc3", g.hosts["nc3"], false)

	// De test: MariaDB stopt op nc1, het VIP gaat naar nc2, en schrijven via
	// .185 lukt weer. Niets ging verloren.
	r := run("database stoppen")
	w := r.Measurements.Writes
	if *r.Result != "pass" || !strings.HasPrefix(r.Summary, "PASS: schrijven via "+ncVIP+" lukte ") ||
		!strings.Contains(r.Summary, ", er ging niets verloren; overgenomen door nc2, daarna terug op nc1, alles hersteld") {
		t.Fatalf("run: %+v", r)
	}
	if w == nil || w.Node != "nc2" || w.Title != "MariaDB Galera" || w.Vip != ncVIP || w.Port != 3306 || w.Lost == nil || *w.Lost != 0 ||
		w.Confirmed == 0 || w.DowntimeMs == nil || *w.DowntimeMs == 0 || w.Failed == 0 || !w.Recovered || len(w.Segments) < 2 || w.CheckError != "" {
		t.Fatalf("schrijftest: %+v", w)
	}
	t.Logf("%s; %d van de %d schrijfacties mislukten", r.Summary, w.Failed, w.Attempts)
	passed := map[string]bool{}
	for _, ch := range r.Checks {
		passed[ch.Name] = ch.OK
	}
	if !passed["Database gezond"] || !passed["Schrijftest"] {
		t.Errorf("voorcontrole: %+v", r.Checks)
	}
	if !slices.Contains(r.kinds(), "db_ready") {
		t.Errorf("tijdlijn: %v", r.kinds())
	}
	if runs := table.Runs(); len(runs) != 0 {
		t.Errorf("rijen na de test: %v", runs)
	}
	backOnNC1("na de test")

	// De database van de schrijftest staat niet tussen de databases van
	// Jonas, en cf_probe is een gebruiker van ClusterForge.
	var dbl componentDatabasesView
	var comps list[componentView]
	c.do("GET", compBase, nil, &comps)
	if s := c.do("GET", compBase+"/"+comps.Items[0].ID+"/databases", nil, &dbl); s != http.StatusOK {
		t.Fatalf("databases: %d", s)
	}
	dbs, _ := dbl.names()
	system := false
	for _, u := range dbl.Users {
		system = system || u.Name == protocol.DBProbeUser && u.System
	}
	if slices.ContainsFunc(dbs, func(d string) bool { return strings.HasPrefix(d, protocol.DBProbeDatabase) }) || !system {
		t.Fatalf("databases: %v %+v", dbs, dbl.Users)
	}

	// Gaat er een bevestigde rij verloren, dan is het FAIL.
	lose.Store(true)
	r = run("rij verloren")
	lose.Store(false)
	w = r.Measurements.Writes
	if *r.Result != "fail" || !strings.HasPrefix(r.Summary, "FAIL: 1 bevestigde rij verloren bij schrijven via "+ncVIP) ||
		w == nil || w.Lost == nil || *w.Lost != 1 || len(w.LostSeqs) != 1 {
		t.Fatalf("rij verloren: %+v %+v", r, w)
	}
	backOnNC1("na de tweede test")
}

// TestFailoverDatabasePatroni is hetzelfde voor PostgreSQL met Patroni: de
// database stoppen op de primary geeft een failover naar de synchrone
// replica, het eigen VIP gaat mee, en schrijven via dat VIP lukt weer.
// keepalived stoppen kan op dat VIP niet, want het volgt de primary, en
// het komt niet terug naar de eerste node.
func TestFailoverDatabasePatroni(t *testing.T) {
	lab := newPatroniHosts(t)
	e, c, g, nc, group := lab.e, lab.c, lab.g, lab.nc, lab.group
	const pgVIP = "10.0.20.190"
	e.fo.Prober = groupProber{group}
	lab.deploy(t)

	table := agenttest.NewProbeTable()
	byIP := map[string]*agenttest.Host{}
	for _, h := range g.hosts {
		byIP[h.Address] = h
	}
	network := newVIPNetwork(t, group, byIP, pgVIP)
	for name, h := range g.hosts {
		base := g.after(name, h)
		h.SetAfter(func(call string, err error) {
			base(call, err)
			if err != nil {
				return
			}
			switch {
			case call == "systemctl stop patroni":
				// Patroni stopt netjes en geeft de leiding door aan de
				// synchrone replica.
				g.mu.Lock()
				next := g.sync
				g.primary, g.sync = next, ""
				g.mu.Unlock()
				h.Locked(func() { h.Patroni, h.Postgres = nil, nil })
				nh := g.hosts[next]
				nh.Locked(func() {
					nh.Patroni.Role, nh.Patroni.Replication, nh.Patroni.Sync = "primary", "", false
					nh.Patroni.Timeline++
					nh.Postgres.Recovery = false
				})
			case call == "systemctl start patroni":
				g.mu.Lock()
				primary := g.primary
				g.mu.Unlock()
				h.Locked(func() { h.Postgres.Probe = table })
				g.replicate(primary)
			case strings.HasPrefix(call, "runuser") && strings.Contains(call, "psql"):
				g.mu.Lock()
				primary := g.primary
				g.mu.Unlock()
				if name == primary {
					g.replicate(primary)
				}
			}
		})
		h.Locked(func() {
			h.Network = network.route
			if h.Postgres != nil {
				h.Postgres.Probe = table
			}
		})
	}
	h := healthHelpers{t: t, e: e, c: c, clusterID: nc.ID, base: "/api/v1/clusters/" + nc.ID + "/components"}
	eventually(t, "het VIP op nc1", func() bool { return group.Owner(pgVIP) == g.hosts["nc1"] })
	h.check()
	h.clusterIs("healthy", "")

	listURL := "/api/v1/clusters/" + nc.ID + "/failover-tests"
	var opts dbFailoverOptions
	c.do("GET", listURL, nil, &opts)
	if ok, _, units := opts.scenario("database_stop"); !ok || !slices.Equal(units, []string{"patroni"}) {
		t.Fatalf("database_stop: %+v", opts.Options.Scenarios)
	}
	i := slices.IndexFunc(opts.Options.Vips, func(v struct {
		ID       string `json:"id"`
		Address  string `json:"address"`
		Database string `json:"database"`
		OwnVip   bool   `json:"own_vip"`
		Probe    struct {
			TCP *struct {
				Port int `json:"port"`
			} `json:"tcp"`
		} `json:"probe"`
	}) bool {
		return v.Address == pgVIP
	})
	if i < 0 || !opts.Options.Vips[i].OwnVip || opts.Options.Vips[i].Database != "PostgreSQL met Patroni" ||
		opts.Options.Vips[i].Probe.TCP == nil || opts.Options.Vips[i].Probe.TCP.Port != 5432 {
		t.Fatalf("VIP's: %+v", opts.Options.Vips)
	}
	vipID := opts.Options.Vips[i].ID
	input := func(scenario string, failback bool) map[string]any {
		return map[string]any{
			"name": scenario, "vip_id": vipID, "scenario": scenario, "max_takeover_seconds": 20,
			"expect_failback": failback, "probe": map[string]any{"tcp": map[string]any{"port": 5432}},
		}
	}
	// Het eigen VIP komt niet terug naar de eerste node.
	var fe fieldErr
	if s := c.do("POST", listURL, input("database_stop", true), &fe); s != http.StatusBadRequest || fe.Field != "expect_failback" ||
		!strings.Contains(fe.Message, "blijft waar de database schrijft") {
		t.Fatalf("met failback: %d %+v", s, fe)
	}
	// keepalived stoppen op het VIP van de primary: geen andere node neemt
	// het over zonder failover van Patroni.
	var ka failoverTestView
	if s := c.do("POST", listURL, input("keepalived_stop", false), &ka); s != http.StatusCreated {
		t.Fatalf("keepalived_stop: %d", s)
	}
	var pe precheckErr
	if s := c.do("POST", "/api/v1/failover-tests/"+ka.ID+"/runs", nil, &pe); s != http.StatusConflict || !slices.Equal(pe.failed(), []string{"Database"}) {
		t.Fatalf("keepalived_stop op het VIP van PostgreSQL: %d %+v", s, pe)
	}

	var ft failoverTestView
	if s := c.do("POST", listURL, input("database_stop", false), &ft); s != http.StatusCreated || ft.Description != "de database (patroni) stoppen op de eigenaar" {
		t.Fatalf("test maken: %d %+v", s, ft)
	}
	var started testRunView
	if s := c.do("POST", "/api/v1/failover-tests/"+ft.ID+"/runs", nil, &started); s != http.StatusAccepted || started.JobID == nil {
		t.Fatalf("starten: %d %+v", s, started)
	}
	waitJobLong(t, c, *started.JobID)
	var r dbRunView
	eventually(t, "run afgerond", func() bool {
		c.do("GET", "/api/v1/test-runs/"+started.ID, nil, &r)
		return r.Result != nil
	})
	w := r.Measurements.Writes
	if *r.Result != "pass" || !strings.HasPrefix(r.Summary, "PASS: schrijven via "+pgVIP+" lukte ") ||
		!strings.Contains(r.Summary, ", er ging niets verloren; overgenomen door nc2, daarna op nc2, alles hersteld") {
		t.Fatalf("run: %+v", r)
	}
	if w == nil || w.Node != "nc2" || w.Title != "PostgreSQL met Patroni" || w.Port != 5432 || w.Lost == nil || *w.Lost != 0 || w.Confirmed == 0 ||
		w.DowntimeMs == nil || *w.DowntimeMs == 0 || w.Failed == 0 || !w.Recovered {
		t.Fatalf("schrijftest: %+v", w)
	}
	t.Logf("%s; %d van de %d schrijfacties mislukten", r.Summary, w.Failed, w.Attempts)
	// nc1 is daarna een replica die bijhoudt; de database is gezond.
	h.check()
	h.clusterIs("healthy", "")
	if group.Owner(pgVIP) != g.hosts["nc2"] {
		t.Fatalf("VIP na de test op %v", group.Owner(pgVIP))
	}
}
