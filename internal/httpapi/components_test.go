package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type componentPlanView struct {
	Slug    string `json:"slug"`
	Prod    bool   `json:"prod"`
	Title   string `json:"title"`
	Blocked bool   `json:"blocked"`
	Hash    string `json:"hash"`
	Nodes   []struct {
		Hostname    string   `json:"hostname"`
		Role        string   `json:"role"`
		Index       int      `json:"index"`
		Address     string   `json:"address"`
		Os          string   `json:"os"`
		MemoryBytes int64    `json:"memory_bytes"`
		NeedBytes   int64    `json:"need_bytes"`
		DiskMount   string   `json:"disk_mount"`
		Steps       []string `json:"steps"`
		VIPInstance string   `json:"vip_instance"`
		VIPDiff     string   `json:"vip_diff"`
	} `json:"nodes"`
	Findings []struct {
		Level    string `json:"level"`
		Code     string `json:"code"`
		Hostname string `json:"hostname"`
		Message  string `json:"message"`
	} `json:"findings"`
}

func (p componentPlanView) codes() []string {
	var out []string
	for _, f := range p.Findings {
		// Hoeveel schijf er vrij is, hangt af van de machine van de test.
		if f.Code != "disk_low" {
			out = append(out, f.Level+":"+f.Code)
		}
	}
	return out
}

type componentView struct {
	ID              string  `json:"id"`
	Engine          string  `json:"engine"`
	Title           string  `json:"title"`
	State           string  `json:"state"`
	Revision        int     `json:"revision"`
	AppliedRevision *int    `json:"applied_revision"`
	JobID           *string `json:"job_id"`
	Nodes           []struct {
		Hostname string `json:"hostname"`
		Role     string `json:"role"`
		Index    int    `json:"index"`
	} `json:"nodes"`
}

// galeraHosts speelt drie Debian 13-nodes na waarop apt MariaDB kan
// installeren, galera_new_cluster een cluster begint en een herstart van
// MariaDB erbij aansluit, zoals Galera dat doet. Een stop van MariaDB
// schrijft de positie in grastate.dat.
type galeraHosts struct {
	mu      sync.Mutex
	hosts   map[string]*agenttest.Host
	members []string
}

const galeraUUID = "5c3a1e0e-0000-11f0-9a3b-7e1c2d3f4a5b"

func (g *galeraHosts) after(name string, h *agenttest.Host) func(call string, err error) {
	return func(call string, err error) {
		if err != nil {
			return
		}
		switch {
		case strings.HasPrefix(call, "apt-get") && strings.Contains(call, " mariadb-server"):
			h.Locked(func() {
				h.MariaDB = &agenttest.MariaDB{Version: "11.8.3-MariaDB-0+deb13u1", MaxConnections: 151, Uptime: 5, Users: agenttest.DebianUsers()}
			})
		case strings.Contains(call, "galera_new_cluster"):
			g.join(name, h, true)
		case strings.HasPrefix(call, "sh -c systemctl restart mariadb && touch "):
			g.join(name, h, false)
		case call == "systemctl restart mariadb":
			g.restart(name, h)
		case call == "systemctl stop mariadb":
			g.leave(name, h)
		}
	}
}

// join zet de node in het cluster en maakt het merkteken, zoals touch.
func (g *galeraHosts) join(name string, h *agenttest.Host, first bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if first {
		g.members = nil
	}
	if !slices.Contains(g.members, name) {
		g.members = append(g.members, name)
	}
	h.Locked(func() { h.MariaDB.Down = false })
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	g.update()
	p := filepath.Join(h.Root, components.Marker("nextcloud"))
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, nil, 0o644)
}

// update geeft elk lid de toestand van het cluster; g.mu is vast.
func (g *galeraHosts) update() {
	var addrs []string
	for _, m := range g.members {
		addrs = append(addrs, g.hosts[m].Address+":3306")
	}
	for _, m := range g.members {
		mh := g.hosts[m]
		mh.Locked(func() {
			mh.MariaDB.Galera = &agenttest.Galera{
				ClusterName: "nextcloud", UUID: galeraUUID, Status: "Primary", Size: len(g.members),
				State: 4, Comment: "Synced", Ready: true, Members: addrs,
			}
		})
	}
}

// restart sluit een node die niet meedoet aan bij het cluster; zonder
// cluster startt hij niet, zoals Galera zonder Primary component.
func (g *galeraHosts) restart(name string, h *agenttest.Host) {
	g.mu.Lock()
	member, alive := slices.Contains(g.members, name), len(g.members) > 0
	g.mu.Unlock()
	switch {
	case member:
	case alive:
		g.join(name, h, false)
	default:
		h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Failed: true})
	}
}

// leave stopt MariaDB gewoon: de node verlaat het cluster en schrijft zijn
// positie; de laatste mag het cluster weer beginnen.
func (g *galeraHosts) leave(name string, h *agenttest.Host) {
	g.mu.Lock()
	defer g.mu.Unlock()
	last := len(g.members) == 1 && g.members[0] == name
	g.members = slices.DeleteFunc(g.members, func(m string) bool { return m == name })
	var seqno int64
	member := false
	h.Locked(func() {
		if h.MariaDB.Galera != nil {
			seqno, member = h.MariaDB.Galera.Seqno, true
		}
		h.MariaDB.Down, h.MariaDB.Galera = true, nil
	})
	// Een node die nog niet meedeed, heeft geen positie om te schrijven.
	if member {
		writeGrastate(h, seqno, last)
	}
	g.update()
}

// crash laat alle nodes tegelijk uitvallen en zo opstarten: MariaDB
// draait nergens, unit is de toestand van mariadb erna, en seqnos de
// positie per node. Met clean stopten ze gewoon en staat de positie in
// grastate.dat; anders vindt alleen galera_recovery haar.
func (g *galeraHosts) crash(unit agenttest.Unit, clean bool, safe string, seqnos map[string]int64) {
	g.mu.Lock()
	g.members = nil
	g.mu.Unlock()
	for name, h := range g.hosts {
		seqno, ok := seqnos[name]
		if !ok {
			continue
		}
		h.Locked(func() {
			h.MariaDB.Down, h.MariaDB.Galera = true, nil
			h.MariaDB.Recovered = galeraUUID + ":" + strconv.FormatInt(seqno, 10)
		})
		h.SetUnit("mariadb", unit)
		if clean {
			writeGrastate(h, seqno, name == safe)
		} else {
			writeGrastate(h, -1, false)
		}
	}
}

func writeGrastate(h *agenttest.Host, seqno int64, safe bool) {
	s := "0"
	if safe {
		s = "1"
	}
	p := filepath.Join(h.Root, "var/lib/mysql/grastate.dat")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("# GALERA saved state\nversion: 2.1\nuuid:    "+galeraUUID+"\nseqno:   "+strconv.FormatInt(seqno, 10)+
		"\nsafe_to_bootstrap: "+s+"\n"), 0o660)
}

// TestClusterComponent is de acceptatie van mijlpaal 3 van fase 5: op
// Nextcloud met nc1 tot nc3 toont 'Database toevoegen' een plan per node,
// en rolt ClusterForge daarna MariaDB Galera uit, node voor node, tot alle
// drie Synced zijn. MariaDB komt in de graaf, en de drift ziet een met de
// hand aangepaste configuratie.
func TestClusterComponent(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "prod"}, &nc); s != http.StatusCreated {
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
	in := map[string]any{"engine": "mariadb", "nodes": []string{ids["nc1"], ids["nc2"], ids["nc3"]}, "settings": map[string]any{"buffer_pool": "512M"}}
	var plan componentPlanView
	if s := c.do("POST", base+"/plan", in, &plan); s != http.StatusOK {
		t.Fatalf("plan: %d", s)
	}
	// Nextcloud heeft hier geen VIP; dat zegt het plan.
	if plan.Blocked || !slices.Equal(plan.codes(), []string{"warning:no_vip"}) || plan.Slug != "nextcloud" || !plan.Prod || plan.Title != "MariaDB Galera" || len(plan.Nodes) != 3 {
		t.Fatalf("plan: %+v", plan)
	}
	for i, n := range plan.Nodes {
		join := "bij het Galera-cluster aansluiten"
		if i == 0 {
			join = "het Galera-cluster beginnen"
		}
		if n.Hostname != []string{"nc1", "nc2", "nc3"}[i] || n.Role != "data" || n.Index != i+1 || n.Address != "10.0.20.18"+string(rune('6'+i)) ||
			n.Os != "Debian GNU/Linux 13 (trixie)" || n.MemoryBytes != 4<<30 || n.NeedBytes != 812<<20 || n.DiskMount != "/" ||
			!slices.Equal(n.Steps, []string{"pakketten: mariadb-server, mariadb-backup, galera-4, socat", "map /var/lib/clusterforge",
				"bestand /etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf", join, "service mariadb",
				"SST-gebruiker mysql via de socket"}) {
			t.Errorf("plan voor %d: %+v", i, n)
		}
	}

	// Twee nodes zonder scheidsrechter kan niet; een geheim als instelling
	// ook niet, en een kijker mag geen plan maken.
	var two componentPlanView
	c.do("POST", base+"/plan", map[string]any{"engine": "mariadb", "nodes": []string{ids["nc1"], ids["nc2"]}}, &two)
	if !two.Blocked || !slices.Contains(two.codes(), "blocked:two_without_arbiter") {
		t.Fatalf("twee nodes: %+v", two)
	}
	var ae apiErr
	if s := c.do("POST", base+"/plan", map[string]any{"engine": "postgresql", "nodes": []string{ids["nc1"]}}, &ae); s != http.StatusBadRequest || ae.Code != "validation" {
		t.Fatalf("postgresql: %d %+v", s, ae)
	}
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := v.do("POST", base+"/plan", in, nil); s != http.StatusForbidden {
		t.Fatalf("kijker plan: %d", s)
	}
	var tpls list[struct {
		Engine string `json:"engine"`
		Title  string `json:"title"`
		Params []struct {
			Name string `json:"name"`
		} `json:"params"`
	}]
	if s := v.do("GET", "/api/v1/component-templates", nil, &tpls); s != http.StatusOK || len(tpls.Items) != 1 || tpls.Items[0].Engine != "mariadb" ||
		tpls.Items[0].Title != "MariaDB Galera" || len(tpls.Items[0].Params) != 2 {
		t.Fatalf("onderdelen om te kiezen: %d %+v", s, tpls)
	}

	// Prod vraagt tweestapsverificatie en de slug; een ander plan dan wat
	// je zag, gaat niet door.
	apply := func(hash, confirm string) (int, apiErr, job) {
		body := map[string]any{"engine": "mariadb", "nodes": in["nodes"], "settings": in["settings"], "hash": hash, "confirm": confirm}
		var raw struct {
			apiErr
			job
		}
		s := c.do("POST", base, body, &raw)
		return s, raw.apiErr, raw.job
	}
	if s, ae, _ := apply(plan.Hash, "nextcloud"); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("zonder tweestapsverificatie: %d %+v", s, ae)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE users SET totp_enabled_at = now() WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	if s, ae, _ := apply(plan.Hash, "nc"); s != http.StatusConflict || ae.Code != "needs_confirmation" {
		t.Fatalf("verkeerde slug: %d %+v", s, ae)
	}
	if s, ae, _ := apply(two.Hash, "nextcloud"); s != http.StatusConflict || ae.Code != "plan_stale" {
		t.Fatalf("ander plan: %d %+v", s, ae)
	}
	s, ae, j := apply(plan.Hash, "nextcloud")
	if s != http.StatusAccepted || j.Title != "Database toevoegen: MariaDB Galera op Nextcloud" {
		t.Fatalf("toevoegen: %d %+v %+v", s, ae, j)
	}
	if s, ae, _ := apply(plan.Hash, "nextcloud"); s != http.StatusConflict {
		t.Fatalf("tweede keer: %d %+v", s, ae)
	}

	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" {
		t.Fatalf("taak: %s %s\n%+v", j.Status, j.Error, j.Steps)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	if !slices.Equal(names, []string{"nc1: MariaDB", "nc2: MariaDB", "nc3: MariaDB", "Controleren"}) {
		t.Fatalf("stappen: %v", names)
	}
	if log := strings.Join(j.Steps[0].Log, "\n"); !strings.Contains(log, "nc1 begint het Galera-cluster nextcloud") {
		t.Errorf("nc1:\n%s", log)
	}
	if log := strings.Join(j.Steps[3].Log, "\n"); !strings.Contains(log, "alle 3 datanodes Synced in één cluster met 3 leden") {
		t.Errorf("controleren:\n%s", log)
	}
	// Alleen nc1 begon het cluster; de andere sloten aan.
	for name, h := range g.hosts {
		calls := strings.Join(h.Calls(), "\n")
		if strings.Contains(calls, "galera_new_cluster") != (name == "nc1") {
			t.Errorf("%s:\n%s", name, calls)
		}
	}
	conf, err := os.ReadFile(filepath.Join(g.hosts["nc2"].Root, "etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf"))
	if err != nil || !strings.Contains(string(conf), "wsrep_cluster_address = gcomm://10.0.20.186,10.0.20.187,10.0.20.188\n") ||
		!strings.Contains(string(conf), "innodb_buffer_pool_size = 512M\n") {
		t.Fatalf("configuratie op nc2: %v\n%s", err, conf)
	}

	// Het onderdeel is klaar, ook voor een kijker te zien.
	var comps list[componentView]
	if s := v.do("GET", base, nil, &comps); s != http.StatusOK || len(comps.Items) != 1 {
		t.Fatalf("onderdelen: %d %+v", s, comps)
	}
	cv := comps.Items[0]
	if cv.Engine != "mariadb" || cv.State != "ready" || cv.Revision != 1 || cv.AppliedRevision == nil || *cv.AppliedRevision != 1 || len(cv.Nodes) != 3 {
		t.Fatalf("onderdeel: %+v", cv)
	}
	// Weghalen kan alleen bij een database die nooit draaide.
	if s := c.do("DELETE", base+"/"+cv.ID, nil, &ae); s != http.StatusConflict {
		t.Fatalf("weghalen: %d %+v", s, ae)
	}

	// MariaDB staat in de graaf, en het menu Databases ziet de drie nodes.
	var kind string
	if err := e.pool.QueryRow(ctx, "SELECT kind FROM services WHERE cluster_id = $1 AND name = 'mariadb'", nc.ID).Scan(&kind); err != nil || kind != "database" {
		t.Fatalf("dienst mariadb: %q %v", kind, err)
	}
	var actions []string
	rows, _ := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE 'cluster.database%' ORDER BY id")
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if !slices.Equal(actions, []string{"cluster.database_added", "cluster.database_applied"}) {
		t.Fatalf("events: %v", actions)
	}

	// De drift vergelijkt de nodes met wat ClusterForge zette.
	driftURL := "/api/v1/clusters/" + nc.ID + "/drift"
	var rep baselineReport
	if s := c.do("POST", driftURL+"/check", nil, &rep); s != http.StatusOK {
		t.Fatalf("drift: %d", s)
	}
	if rep.Source == nil || rep.Source.Kind != "component" || rep.status("nc1") != "in_sync" || rep.status("nc2") != "in_sync" {
		t.Fatalf("drift na de uitrol: %+v", rep)
	}
	writeFile(t, filepath.Join(g.hosts["nc2"].Root, "etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf"), string(conf)+"max_connections = 999\n", 0o644)
	c.do("POST", driftURL+"/check", nil, &rep)
	f := rep.findings("nc2")
	if rep.status("nc2") != "drift" || rep.status("nc1") != "in_sync" || len(f) != 1 || f[0].Key != "file:/etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf:content" {
		t.Fatalf("drift na de wijziging: %+v", rep)
	}

	// Opnieuw toepassen zet het bestand terug en herstart MariaDB op nc2,
	// en alleen daar.
	if s := c.do("POST", base+"/"+cv.ID+"/apply", map[string]any{}, &ae); s != http.StatusConflict || ae.Code != "needs_confirmation" {
		t.Fatalf("opnieuw zonder slug: %d %+v", s, ae)
	}
	before := map[string]int{}
	for name, h := range g.hosts {
		before[name] = len(h.Calls())
	}
	if s := c.do("POST", base+"/"+cv.ID+"/apply", map[string]any{"confirm": "nextcloud"}, &j); s != http.StatusAccepted {
		t.Fatalf("opnieuw: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("opnieuw: %s %s\n%+v", j.Status, j.Error, j.Steps)
	}
	for name, h := range g.hosts {
		calls := strings.Join(h.Calls()[before[name]:], "\n")
		if strings.Contains(calls, "systemctl restart mariadb") != (name == "nc2") || strings.Contains(calls, "galera_new_cluster") {
			t.Errorf("%s bij opnieuw toepassen:\n%s", name, calls)
		}
	}
	c.do("POST", driftURL+"/check", nil, &rep)
	if rep.status("nc2") != "in_sync" {
		t.Fatalf("drift na opnieuw toepassen: %+v", rep)
	}
}
