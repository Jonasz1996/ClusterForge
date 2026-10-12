package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type migrationPlanView struct {
	Source struct {
		Key, Name, Node, Address, Via, Kind string
		Port                                int
	} `json:"source"`
	Database string `json:"database"`
	Info     *struct {
		Version                           string
		Tables                            int
		SizeBytes                         int64 `json:"size_bytes"`
		Views, Routines, Triggers, Events int
	} `json:"info"`
	Clients []struct {
		Kind, Name string
	} `json:"clients"`
	Targets []struct {
		ClusterName string `json:"cluster_name"`
		ComponentID string `json:"component_id"`
		Suggested   bool   `json:"suggested"`
	} `json:"targets"`
	Target *struct {
		ClusterName string   `json:"cluster_name"`
		ComponentID string   `json:"component_id"`
		Node        string   `json:"node"`
		Version     string   `json:"version"`
		Address     string   `json:"address"`
		Hosts       []string `json:"hosts"`
		Exists      bool     `json:"exists"`
	} `json:"target"`
	Reach *struct {
		Reached bool
		Version string
		TLS     bool `json:"tls"`
		Code    int
	} `json:"reach"`
	Findings []struct {
		Level, Code, Hostname, Message, Command string
	} `json:"findings"`
	Blocked bool `json:"blocked"`
	Runs    []struct {
		ID             string `json:"id"`
		Kind           string `json:"kind"`
		Status         string `json:"status"`
		ClusterName    string `json:"cluster_name"`
		SourceNode     string `json:"source_node"`
		SourceAddress  string `json:"source_address"`
		TargetDatabase string `json:"target_database"`
		TargetNode     string `json:"target_node"`
		SizeBytes      int64  `json:"size_bytes"`
		DumpBytes      int64  `json:"dump_bytes"`
		ImportMs       *int64 `json:"import_ms"`
		CountMs        *int64 `json:"count_ms"`
		Tables         int    `json:"tables"`
		RowsSource     int64  `json:"rows_source"`
		RowsTarget     int64  `json:"rows_target"`
		Differences    []struct {
			Name           string
			Source, Target int64
		} `json:"differences"`
		Objects []struct {
			Kind           string
			Source, Target int
		} `json:"objects"`
		Message string  `json:"message"`
		JobID   *string `json:"job_id"`
	} `json:"runs"`
	Active *struct {
		JobID string `json:"job_id"`
	} `json:"active"`
}

func (p migrationPlanView) codes() string {
	var out []string
	for _, f := range p.Findings {
		out = append(out, f.Level+":"+f.Code)
	}
	return strings.Join(out, " ")
}

func (p migrationPlanView) finding(code string) string {
	for _, f := range p.Findings {
		if f.Code == code {
			return f.Message + " | " + f.Command
		}
	}
	return ""
}

// TestDatabaseMigrationTrial is de acceptatie van mijlpaal 8 van fase 5:
// Jonas' oude databaseserver db1 met MariaDB 10.11 en de database van
// Nextcloud, en het nieuwe Galera-cluster op nc1 tot nc3. Het plan toont
// grootte, versies, wie de database gebruikt en wat Galera anders doet; de
// proef haalt een kopie op terwijl de app doorwerkt, telt, meet en ruimt
// op. Het wachtwoord van de tijdelijke gebruiker staat nergens.
func TestDatabaseMigrationTrial(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g := lab.e, lab.c, lab.g
	ctx := context.Background()
	if s := c.do("POST", "/api/v1/clusters/"+lab.cluster.ID+"/vips", map[string]any{"address": "10.0.20.185"}, nil); s != http.StatusCreated {
		t.Fatalf("VIP: %d", s)
	}

	// db1: een losse node met de oude MariaDB.
	var db1n node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db1", "primary_ip": "10.0.20.181"}, &db1n); s != http.StatusCreated {
		t.Fatalf("db1: %d", s)
	}
	var db1 *agenttest.Host
	startAgentWith(t, e, c, db1n.ID, strings.Repeat("d", 32), &addrs{}, func(ag *agent.Agent, root string) {
		db1 = agenttest.NewHost(root, "10.0.20.181")
		db1.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
		db1.MariaDB = &agenttest.MariaDB{
			Version: "10.11.11-MariaDB-0+deb12u1", MaxConnections: 151, Uptime: 86400, Users: agenttest.DebianUsers(),
			Databases: []agenttest.Database{
				{
					Name: "nextcloud", Size: 300 << 20, Tables: 6, Rows: 100, NoPK: []string{"t3"}, Engines: map[string]string{"t5": "MyISAM"},
					Views: 1, Routines: 1, Triggers: 1,
				},
				{Name: "zabbix", Size: 2 << 30, Tables: 170},
			},
			Clients: []agenttest.Client{
				{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 4},
				{User: "backup", Host: "10.0.30.5", Database: "nextcloud", Count: 1},
			},
		}
		ag.Exec, ag.ExecStdin, ag.Stream, ag.Root, ag.Collector.Run = db1.Exec, db1.ExecStdin, db1.Stream, root, db1.Exec
	})
	network := func(addr string) *agenttest.Host {
		if addr == "10.0.20.181:3306" {
			return db1
		}
		return nil
	}
	for _, h := range g.hosts {
		h.Locked(func() { h.Network = network })
	}
	nc1 := g.hosts["nc1"]
	source := "mariadb/node/" + db1n.ID
	resync := func() {
		t.Helper()
		if err := e.dbs.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "db1 onder Databases", func() bool {
		resync()
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM db_databases WHERE node_id = $1", db1n.ID).Scan(&n)
		return n == 2
	})
	base := "/api/v1/databases/migration?source=" + url.QueryEscape(source) + "&database="
	plan := func(cl *client, db, comp string) migrationPlanView {
		t.Helper()
		var p migrationPlanView
		var ae apiErr
		u := base + db
		if comp != "" {
			u += "&component=" + comp
		}
		if s := cl.do("GET", u, nil, &p); s != http.StatusOK {
			cl.do("GET", u, nil, &ae)
			t.Fatalf("plan: %d %+v", s, ae)
		}
		return p
	}

	// Het plan zonder keuze: Nextcloud gebruikt de database al, dus daar
	// gaat ze heen.
	p := plan(c, "nextcloud", "")
	if p.Source.Name != "db1" || p.Source.Node != "db1" || p.Source.Address != "10.0.20.181" || p.Source.Via != "node" || p.Source.Port != 3306 {
		t.Fatalf("bron: %+v", p.Source)
	}
	if i := p.Info; i == nil || !strings.HasPrefix(i.Version, "10.11.11") || i.Tables != 6 || i.SizeBytes != 300<<20 || i.Views != 1 || i.Routines != 1 || i.Triggers != 1 {
		t.Fatalf("info: %+v", p.Info)
	}
	if len(p.Targets) != 1 || p.Targets[0].ClusterName != "Nextcloud" || !p.Targets[0].Suggested || p.Target == nil ||
		p.Target.ComponentID != lab.cid || p.Target.Node != "nc1" || !strings.HasPrefix(p.Target.Version, "11.8.3") ||
		p.Target.Address != "10.0.20.186" || len(p.Target.Hosts) == 0 || p.Target.Hosts[0] != "10.0.20.186" || p.Target.Exists {
		t.Fatalf("doel: %+v %+v", p.Targets, p.Target)
	}
	if r := p.Reach; r == nil || !r.Reached || !strings.HasPrefix(r.Version, "10.11.11") || r.TLS {
		t.Fatalf("bereikbaar: %+v", p.Reach)
	}
	if len(p.Clients) != 2 || p.Clients[0].Name != "Nextcloud" || p.Clients[1].Name != "10.0.30.5" {
		t.Fatalf("clients: %+v", p.Clients)
	}
	if got := p.codes(); got != "warning:no_primary_key warning:not_innodb info:routines_proc warning:clients_elsewhere info:no_tls info:upgrade warning:buffer_pool" || p.Blocked {
		t.Fatalf("bevindingen: %s %v", got, p.Blocked)
	}
	if f := p.finding("not_innodb"); !strings.Contains(f, "t5 (MyISAM)") || !strings.Contains(f, "ALTER TABLE `nextcloud`.`t5` ENGINE=InnoDB;") {
		t.Errorf("niet InnoDB: %s", f)
	}
	if f := p.finding("no_primary_key"); !strings.Contains(f, "één tabel zonder primaire sleutel: t3") || !strings.Contains(f, "occ db:add-missing-primary-keys") {
		t.Errorf("zonder sleutel: %s", f)
	}
	if f := p.finding("clients_elsewhere"); !strings.Contains(f, "10.0.30.5") {
		t.Errorf("andere clients: %s", f)
	}
	if f := p.finding("buffer_pool"); !strings.Contains(f, "300,0 MB aan data in de MariaDB van Nextcloud, en de buffer pool is 256,0 MB") {
		t.Errorf("buffer pool: %s", f)
	}

	// Een kijker ziet het plan, maar begint geen proef.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	_ = plan(viewer, "nextcloud", lab.cid)
	trial := map[string]any{"source": source, "database": "nextcloud", "component_id": lab.cid}
	if s := viewer.do("POST", "/api/v1/databases/migration/trial", trial, nil); s != http.StatusForbidden {
		t.Fatalf("kijker: %d", s)
	}

	// Wat niet kan.
	var ae apiErr
	if s := c.do("GET", "/api/v1/databases/migration?source=mariadb/onbekend&database=nextcloud", nil, &ae); s != http.StatusNotFound {
		t.Errorf("onbekende bron: %d %+v", s, ae)
	}
	if s := c.do("GET", base+"Next-Cloud", nil, &ae); s != http.StatusBadRequest {
		t.Errorf("ongeldige naam: %d %+v", s, ae)
	}
	if p := plan(c, "bestaatniet", lab.cid); !p.Blocked || p.codes() != "blocked:not_found" {
		t.Errorf("onbekende database: %s", p.codes())
	}
	// Een firewall tussen nc1 en db1.
	nc1.Locked(func() { nc1.Network = nil })
	if p := plan(c, "nextcloud", lab.cid); !p.Blocked || !strings.Contains(p.codes(), "blocked:unreachable") || p.Reach != nil ||
		!strings.Contains(p.finding("unreachable"), "connection refused") ||
		!strings.Contains(p.finding("unreachable"), "Zet in de firewall poort 3306 open van 10.0.20.186 naar 10.0.20.181") {
		t.Fatalf("firewall: %s %s", p.codes(), p.finding("unreachable"))
	}
	nc1.Locked(func() { nc1.Network = network })
	// Een nextcloud_proef die ClusterForge niet maakte, houdt de proef
	// tegen.
	nc1.Locked(func() {
		nc1.MariaDB.Databases = append(nc1.MariaDB.Databases, agenttest.Database{Name: "nextcloud_proef", Tables: 1})
	})
	resync()
	if p := plan(c, "nextcloud", lab.cid); !p.Blocked || !strings.Contains(p.codes(), "blocked:trial_exists") {
		t.Fatalf("eigen nextcloud_proef: %s", p.codes())
	}
	if s := c.do("POST", "/api/v1/databases/migration/trial", trial, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "nextcloud_proef") {
		t.Fatalf("proef met eigen nextcloud_proef: %d %+v", s, ae)
	}
	nc1.Locked(func() {
		nc1.MariaDB.Databases = slices.DeleteFunc(nc1.MariaDB.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud_proef" })
	})
	resync()

	// Het wachtwoord van de tijdelijke gebruiker uit het optiebestand van
	// de agent, om te zien dat het nergens anders staat.
	var mu sync.Mutex
	var passwords []string
	var grow bool
	after := g.after("nc1", nc1)
	pwRe := regexp.MustCompile(`(?m)^password=(.+)$`)
	nc1.SetAfter(func(call string, err error) {
		after(call, err)
		if !strings.HasPrefix(call, "mariadb-dump --defaults-extra-file=") {
			return
		}
		cnf := strings.TrimPrefix(strings.Fields(call)[1], "--defaults-extra-file=")
		if b, rerr := os.ReadFile(cnf); rerr == nil {
			if m := pwRe.FindSubmatch(b); m != nil {
				mu.Lock()
				passwords = append(passwords, string(m[1]))
				mu.Unlock()
			}
		}
		// De app werkt door: na de dump komen er rijen bij.
		mu.Lock()
		on := grow
		mu.Unlock()
		if on {
			db1.Locked(func() { db1.MariaDB.Databases[0].Rows += 3 })
		}
	})
	start := func() job {
		t.Helper()
		var j job
		if s := c.do("POST", "/api/v1/databases/migration/trial", trial, &j); s != http.StatusAccepted {
			c.do("POST", "/api/v1/databases/migration/trial", trial, &ae)
			t.Fatalf("proef: %d %+v", s, ae)
		}
		return waitJobLong(t, c, j.ID)
	}

	// De eerste proef: alles gelijk.
	j := start()
	if j.Status != "succeeded" {
		t.Fatalf("proef: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	if strings.Join(names, " | ") != "Controle | Vorige proef opruimen | nextcloud ophalen van db1 en importeren als nextcloud_proef op nc1 | "+
		"Rijen en objecten vergelijken | nextcloud_proef weghalen" {
		t.Fatalf("stappen: %v", names)
	}
	steps := strings.Join(stepLogs(j), "\n")
	for _, want := range []string{
		"Let op: één tabel zonder primaire sleutel", "nc1 haalt de dump op van 10.0.20.181:3306 en importeert hem als nextcloud_proef.",
		"Tijdelijke gebruiker cf_verhuizing_", "alleen om nextcloud te lezen, vanaf 10.0.20.186", "zonder TLS", "Tijdelijke gebruiker cf_verhuizing_",
		"weggehaald.", "6 tabellen, 600 rijen oud en 600 nieuw.", "Alle tabellen hebben aan beide kanten evenveel rijen.",
		"nextcloud_proef weggehaald van nc1; aan nextcloud op db1 is niets veranderd.",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("stappen missen %q:\n%s", want, steps)
		}
	}
	p = plan(c, "nextcloud", lab.cid)
	if len(p.Runs) != 1 || p.Active != nil {
		t.Fatalf("proeven: %+v", p.Runs)
	}
	r := p.Runs[0]
	if r.Kind != "trial" || r.Status != "ok" || r.ClusterName != "Nextcloud" || r.SourceNode != "db1" || r.SourceAddress != "10.0.20.181" ||
		r.TargetDatabase != "nextcloud_proef" || r.TargetNode != "nc1" || r.SizeBytes != 300<<20 || r.DumpBytes == 0 || r.ImportMs == nil ||
		r.CountMs == nil || r.Tables != 6 || r.RowsSource != 600 || r.RowsTarget != 600 || len(r.Differences) != 0 || len(r.Objects) != 4 ||
		r.JobID == nil || *r.JobID != j.ID {
		t.Fatalf("proef: %+v", r)
	}
	for _, o := range r.Objects {
		if o.Source != o.Target {
			t.Errorf("objecten: %+v", r.Objects)
		}
	}
	// De kopie en de tijdelijke gebruiker zijn weg; aan db1 is niets
	// veranderd.
	assertMigrationClean(t, nc1, db1)

	// Een kopie van een vorige proef die bleef staan, haalt de volgende
	// eerst weg. De app werkt door, dus de oude kant telt meer rijen.
	nc1.Locked(func() {
		nc1.MariaDB.Databases = append(nc1.MariaDB.Databases, agenttest.Database{Name: "nextcloud_proef", Tables: 1})
	})
	resync()
	if p := plan(c, "nextcloud", lab.cid); p.Blocked || !strings.Contains(p.codes(), "info:trial_leftover") {
		t.Fatalf("achtergebleven kopie: %s", p.codes())
	}
	mu.Lock()
	grow = true
	mu.Unlock()
	if j = start(); j.Status != "succeeded" {
		t.Fatalf("tweede proef: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	mu.Lock()
	grow = false
	mu.Unlock()
	steps = strings.Join(stepLogs(j), "\n")
	if !strings.Contains(steps, "nextcloud_proef van een vorige proef weggehaald.") ||
		!strings.Contains(steps, "6 tabellen hebben een ander aantal rijen: t1 (103 oud, 100 nieuw)") ||
		!strings.Contains(steps, "de app werkte door") {
		t.Errorf("tweede proef:\n%s", steps)
	}
	p = plan(c, "nextcloud", lab.cid)
	if r := p.Runs[0]; len(p.Runs) != 2 || r.Status != "ok" || len(r.Differences) != 6 || r.RowsSource != 618 || r.RowsTarget != 600 ||
		!strings.Contains(r.Message, "bij de echte verhuizing staat ze stil") {
		t.Fatalf("tweede proef: %+v", p.Runs[0])
	}
	assertMigrationClean(t, nc1, db1)

	// Een dump zonder slotregel: de proef mislukt, de kopie en de
	// gebruiker zijn weg.
	db1.Locked(func() { db1.MariaDB.DumpIncomplete = true })
	if j = start(); j.Status != "failed" || !strings.Contains(j.Error, "niet compleet") {
		t.Fatalf("onvolledige dump: %s %s", j.Status, j.Error)
	}
	db1.Locked(func() { db1.MariaDB.DumpIncomplete = false })
	p = plan(c, "nextcloud", lab.cid)
	if r := p.Runs[0]; r.Status != "failed" || !strings.Contains(r.Message, "niet compleet") || p.Active != nil {
		t.Fatalf("mislukte proef: %+v", r)
	}
	assertMigrationClean(t, nc1, db1)

	// Het wachtwoord: drie keer een nieuw, en nergens bewaard.
	mu.Lock()
	pws := slices.Clone(passwords)
	mu.Unlock()
	if len(pws) != 3 || pws[0] == pws[1] || len(pws[0]) < 20 {
		t.Fatalf("wachtwoorden: %d", len(pws))
	}
	var dump strings.Builder
	for _, q := range []string{
		"SELECT coalesce(string_agg(payload::text, ' '), '') FROM events",
		"SELECT coalesce(string_agg(params::text || ' ' || coalesce(error, ''), ' '), '') FROM jobs",
		"SELECT coalesce(string_agg(state::text || ' ' || log::text || ' ' || coalesce(error, ''), ' '), '') FROM job_steps",
		"SELECT coalesce(string_agg(to_jsonb(m)::text, ' '), '') FROM db_migrations m",
	} {
		var s string
		if err := e.pool.QueryRow(ctx, q).Scan(&s); err != nil {
			t.Fatal(err)
		}
		dump.WriteString(s)
	}
	for _, h := range append([]*agenttest.Host{db1}, g.hosts["nc1"], g.hosts["nc2"], g.hosts["nc3"]) {
		for _, call := range h.Calls() {
			dump.WriteString(call)
		}
	}
	for _, pw := range pws {
		if strings.Contains(dump.String(), pw) {
			t.Errorf("wachtwoord %s… staat ergens", pw[:4])
		}
	}

	// Het logboek.
	var actions []string
	rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE 'cluster.database_trial%' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if strings.Join(actions, " ") != "cluster.database_trial_ok cluster.database_trial_ok cluster.database_trial_failed" {
		t.Errorf("events: %v", actions)
	}
}

// assertMigrationClean kijkt dat er geen kopie op nc1 en geen tijdelijke
// gebruiker op db1 staat, en dat de database op db1 er nog is.
func assertMigrationClean(t *testing.T, nc1, db1 *agenttest.Host) {
	t.Helper()
	nc1.Locked(func() {
		for _, d := range nc1.MariaDB.Databases {
			if strings.HasSuffix(d.Name, "_proef") {
				t.Errorf("kopie op nc1: %s", d.Name)
			}
		}
	})
	db1.Locked(func() {
		for _, u := range db1.MariaDB.Users {
			if strings.HasPrefix(u.User, "cf_verhuizing_") {
				t.Errorf("tijdelijke gebruiker op db1: %+v", u)
			}
		}
		if len(db1.MariaDB.Databases) != 2 || db1.MariaDB.Databases[0].Name != "nextcloud" {
			t.Errorf("databases op db1: %+v", db1.MariaDB.Databases)
		}
	})
}

func stepLogs(j job) []string {
	var out []string
	for _, st := range j.Steps {
		out = append(out, st.Log...)
	}
	return out
}
