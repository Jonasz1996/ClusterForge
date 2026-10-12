package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type moveRunView struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`
	Status        string  `json:"status"`
	Stage         string  `json:"stage"`
	Database      string  `json:"database"`
	TargetDB      string  `json:"target_database"`
	TargetNode    string  `json:"target_node"`
	TargetAddress string  `json:"target_address"`
	Force         bool    `json:"force"`
	Locked        bool    `json:"locked"`
	CopiedAt      *string `json:"copied_at"`
	SwitchedAt    *string `json:"switched_at"`
	Tables        int     `json:"tables"`
	RowsSource    int64   `json:"rows_source"`
	RowsTarget    int64   `json:"rows_target"`
	Differences   []struct {
		Name string
	} `json:"differences"`
	Accounts []struct {
		User       string   `json:"user"`
		OldHosts   []string `json:"old_hosts"`
		Hosts      []string `json:"hosts"`
		Privileges []string `json:"privileges"`
	} `json:"accounts"`
	Message string `json:"message"`
}

type movePlanView struct {
	Findings []struct {
		Level, Code, Message string
	} `json:"findings"`
	Blocked  bool `json:"blocked"`
	Accounts []struct {
		User, Host, Plugin string
		Privileges         []string
		Global             bool
	} `json:"accounts"`
	NewHosts    []string `json:"new_hosts"`
	Connections []struct {
		Server, User, Address, Hostname string
		Count                           int
	} `json:"connections"`
	MoveFindings []struct {
		Level, Code, Message string
	} `json:"move_findings"`
	MoveBlocked bool          `json:"move_blocked"`
	Runs        []moveRunView `json:"runs"`
	Active      *struct {
		Kind string `json:"kind"`
	} `json:"active"`
}

func (p movePlanView) moveCodes() string {
	var out []string
	for _, f := range p.MoveFindings {
		out = append(out, f.Level+":"+f.Code)
	}
	return strings.Join(out, " ")
}

func (p movePlanView) codes() string {
	var out []string
	for _, f := range p.Findings {
		out = append(out, f.Level+":"+f.Code)
	}
	return strings.Join(out, " ")
}

// TestDatabaseMove is de acceptatie van mijlpaal 9 van fase 5: de database
// van Nextcloud verhuist van db1 (MariaDB 10.11) naar het eigen
// Galera-cluster op nc1 tot nc3, met dezelfde gebruiker en hetzelfde
// wachtwoord. Een afwijking stopt de taak en ruimt op; annuleren haalt de
// kopie weg; overschakelen zet de oude database op slot, en het slot kan
// eraf. De hash van het wachtwoord staat nergens in ClusterForge.
func TestDatabaseMove(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g := lab.e, lab.c, lab.g
	ctx := context.Background()
	if s := c.do("POST", "/api/v1/clusters/"+lab.cluster.ID+"/vips", map[string]any{"address": "10.0.20.185"}, nil); s != http.StatusCreated {
		t.Fatalf("VIP: %d", s)
	}
	const pw = "nc-wachtwoord-van-de-app"
	hash := agenttest.NativeHash(pw)
	all := slices.Clone(agenttest.AllPrivileges)
	ncUser := func(host string) agenttest.MariaUser {
		return agenttest.MariaUser{User: "nextcloud", Host: host, Password: pw, Grants: map[string][]string{"nextcloud": all}}
	}

	var db1n node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db1", "primary_ip": "10.0.20.181"}, &db1n); s != http.StatusCreated {
		t.Fatalf("db1: %d", s)
	}
	var db1 *agenttest.Host
	startAgentWith(t, e, c, db1n.ID, strings.Repeat("d", 32), &addrs{}, func(ag *agent.Agent, root string) {
		db1 = agenttest.NewHost(root, "10.0.20.181")
		db1.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
		users := append(agenttest.DebianUsers(), ncUser("10.0.20.186"), ncUser("10.0.20.187"), ncUser("10.0.20.188"),
			agenttest.MariaUser{User: "rapport", Host: "10.0.30.5", Password: "rapport-wachtwoord", Grants: map[string][]string{"nextcloud": {"SELECT"}}},
			agenttest.MariaUser{User: "beheer", Host: "localhost", Plugin: "unix_socket", Grants: map[string][]string{"nextcloud": all}})
		db1.MariaDB = &agenttest.MariaDB{
			Version: "10.11.11-MariaDB-0+deb12u1", MaxConnections: 151, Uptime: 86400, Users: users,
			Databases: []agenttest.Database{{Name: "nextcloud", Size: 300 << 20, Tables: 6, Rows: 100}, {Name: "zabbix", Size: 2 << 30, Tables: 170}},
			Clients:   []agenttest.Client{{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 4}},
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
	planURL := "/api/v1/databases/migration?source=" + url.QueryEscape(source) + "&database=nextcloud&component=" + lab.cid
	plan := func() movePlanView {
		t.Helper()
		var p movePlanView
		var ae apiErr
		if s := c.do("GET", planURL, nil, &p); s != http.StatusOK {
			c.do("GET", planURL, nil, &ae)
			t.Fatalf("plan: %d %+v", s, ae)
		}
		return p
	}
	setClients := func(h *agenttest.Host, cs ...agenttest.Client) { h.Locked(func() { h.MariaDB.Clients = cs }) }

	// Het plan: de gebruikers van de app, waar ze straks vandaan mogen
	// inloggen, en wie er nu verbonden is.
	p := plan()
	var accs []string
	for _, a := range p.Accounts {
		accs = append(accs, a.User+"@"+a.Host+":"+a.Plugin+":"+strings.Join(a.Privileges, ","))
	}
	if strings.Join(accs, " ") != "beheer@localhost:unix_socket:"+strings.Join(sortedCopy(all), ",")+" nextcloud@10.0.20.186:mysql_native_password:"+
		strings.Join(sortedCopy(all), ",")+" nextcloud@10.0.20.187:mysql_native_password:"+strings.Join(sortedCopy(all), ",")+
		" nextcloud@10.0.20.188:mysql_native_password:"+strings.Join(sortedCopy(all), ",")+" rapport@10.0.30.5:mysql_native_password:SELECT" {
		t.Fatalf("gebruikers: %v", accs)
	}
	if strings.Join(p.NewHosts, " ") != "10.0.20.186 10.0.20.187 10.0.20.188 10.0.20.185 localhost" {
		t.Fatalf("nieuwe hosts: %v", p.NewHosts)
	}
	if len(p.Connections) != 1 || p.Connections[0].Server != "db1" || p.Connections[0].User != "nextcloud" || p.Connections[0].Hostname != "nc1" ||
		p.Connections[0].Count != 4 {
		t.Fatalf("verbindingen: %+v", p.Connections)
	}
	if got := p.moveCodes(); got != "warning:accounts_plugin info:no_trial" || p.MoveBlocked || p.Blocked {
		t.Fatalf("bevindingen verhuizing: %s %v", got, p.MoveBlocked)
	}

	// Een kijker verhuist niet; met de app nog verbonden kan het niet
	// zonder force.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	move := map[string]any{"source": source, "database": "nextcloud", "component_id": lab.cid}
	if s := viewer.do("POST", "/api/v1/databases/migration/move", move, nil); s != http.StatusForbidden {
		t.Fatalf("kijker: %d", s)
	}
	var ae apiErr
	if s := c.do("POST", "/api/v1/databases/migration/move", move, &ae); s != http.StatusConflict ||
		!strings.Contains(ae.Message, "nog open met nextcloud: 4 verbindingen (nextcloud vanaf nc1 op db1: 4)") {
		t.Fatalf("met verbindingen: %d %+v", s, ae)
	}
	// Op prod met de slug en tweestapsverificatie.
	if _, err := e.pool.Exec(ctx, "UPDATE clusters SET environment = 'prod' WHERE id = $1", lab.cluster.ID); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", "/api/v1/databases/migration/move", move, &ae); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("prod: %d %+v", s, ae)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE clusters SET environment = 'lab' WHERE id = $1", lab.cluster.ID); err != nil {
		t.Fatal(err)
	}

	// De app schrijft nog: na de dump komen er rijen bij. De verhuizing
	// stopt bij het vergelijken en ruimt alles op nc1 op.
	var mu sync.Mutex
	grow := false
	after := g.after("nc1", nc1)
	nc1.SetAfter(func(call string, err error) {
		after(call, err)
		mu.Lock()
		on := grow
		mu.Unlock()
		if on && strings.HasPrefix(call, "mariadb-dump --defaults-extra-file=") {
			db1.Locked(func() { db1.MariaDB.Databases[0].Rows += 3 })
		}
	})
	start := func(body map[string]any) job {
		t.Helper()
		var j job
		if s := c.do("POST", "/api/v1/databases/migration/move", body, &j); s != http.StatusAccepted {
			c.do("POST", "/api/v1/databases/migration/move", body, &ae)
			t.Fatalf("verhuizen: %d %+v", s, ae)
		}
		return waitJobLong(t, c, j.ID)
	}
	setClients(db1)
	mu.Lock()
	grow = true
	mu.Unlock()
	j := start(move)
	mu.Lock()
	grow = false
	mu.Unlock()
	if j.Status != "failed" || !strings.Contains(j.Error, "niet gelijk") {
		t.Fatalf("afwijking: %s %s", j.Status, j.Error)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	if strings.Join(names, " | ") != "Controle | Onderhoud: niemand meer verbonden | Gebruikers maken op nc1 | "+
		"nextcloud ophalen van db1 en importeren op nc1 | Rijen en objecten vergelijken" {
		t.Fatalf("stappen: %v", names)
	}
	steps := strings.Join(stepLogs(j), "\n")
	for _, want := range []string{
		"Let op: één gebruiker logt niet met een gewoon wachtwoord in: beheer@localhost (unix_socket)",
		"nc1 haalt de dump op van 10.0.20.181:3306 en importeert hem als nextcloud; de app vindt de database straks op 10.0.20.186.",
		"Niemand is nog met nextcloud op db1 verbonden.",
		"Gebruiker nextcloud op nc1, met hetzelfde wachtwoord als op db1 (10.0.20.186, 10.0.20.187, 10.0.20.188), vanaf 10.0.20.186, 10.0.20.187, " +
			"10.0.20.188, 10.0.20.185 en localhost, met alle rechten op nextcloud.",
		"Gebruiker rapport op nc1, met hetzelfde wachtwoord als op db1 (10.0.30.5)", "met SELECT op nextcloud.",
		"6 tabellen hebben een ander aantal rijen: t1 (103 oud, 100 nieuw)", "Dan schreef er tijdens de verhuizing nog iemand in de oude database",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("stappen missen %q:\n%s", want, steps)
		}
	}
	p = plan()
	if r := p.Runs[0]; len(p.Runs) != 1 || r.Kind != "migrate" || r.Status != "failed" || r.Stage != "copy" || r.TargetDB != "nextcloud" ||
		!strings.Contains(r.Message, "Dan schreef er tijdens de verhuizing nog iemand") ||
		!strings.Contains(r.Message, "De nieuwe database nextcloud, gebruiker nextcloud en gebruiker rapport weggehaald van nc1.") ||
		!strings.Contains(r.Message, "De app gebruikt nog de oude database; zet het onderhoud weer uit.") || p.Active != nil {
		t.Fatalf("mislukte verhuizing: %+v", p.Runs)
	}
	assertMoveGone(t, nc1)
	db1.Locked(func() { db1.MariaDB.Databases[0].Rows = 100 })

	// Toch verhuizen terwijl de app nog verbonden is: de kopie lukt en
	// wacht op overschakelen. Annuleren kan niet zolang de app de nieuwe
	// database gebruikt, en dan wel: de kopie en de nieuwe gebruikers zijn
	// weg.
	setClients(db1, agenttest.Client{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 1})
	force := map[string]any{"source": source, "database": "nextcloud", "component_id": lab.cid, "force": true}
	if j = start(force); j.Status != "succeeded" {
		t.Fatalf("toch verhuizen: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	if steps = strings.Join(stepLogs(j), "\n"); !strings.Contains(steps, "Toch verder, zoals gevraagd: nog open met nextcloud: één verbinding") ||
		!strings.Contains(steps, "Alle tabellen hebben aan beide kanten evenveel rijen.") {
		t.Errorf("toch verhuizen:\n%s", steps)
	}
	p = plan()
	run := p.Runs[0]
	if run.Status != "running" || run.Stage != "copied" || !run.Force || run.CopiedAt == nil || run.TargetAddress != "10.0.20.186" ||
		run.RowsSource != 600 || run.RowsTarget != 600 || len(run.Accounts) != 2 || run.Accounts[0].User != "nextcloud" ||
		strings.Join(run.Accounts[0].OldHosts, " ") != "10.0.20.186 10.0.20.187 10.0.20.188" || len(run.Accounts[0].Hosts) != 5 || p.Active != nil {
		t.Fatalf("gekopieerd: %+v", run)
	}
	if !p.Blocked || !p.MoveBlocked || !strings.Contains(p.codes(), "blocked:move_open") {
		t.Fatalf("open verhuizing houdt een nieuwe tegen: %s", p.codes())
	}
	trial := map[string]any{"source": source, "database": "nextcloud", "component_id": lab.cid}
	if s := c.do("POST", "/api/v1/databases/migration/trial", trial, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "wacht op overschakelen") {
		t.Fatalf("proef tijdens open verhuizing: %d %+v", s, ae)
	}
	// Op nc1: de database en de gebruikers met de hash van db1; de app
	// logt met haar oude wachtwoord in.
	nc1.Locked(func() {
		if !slices.ContainsFunc(nc1.MariaDB.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud" && d.Rows == 100 }) {
			t.Errorf("databases op nc1: %+v", nc1.MariaDB.Databases)
		}
		var got []string
		for _, u := range nc1.MariaDB.Users {
			if u.User == "nextcloud" || u.User == "rapport" {
				got = append(got, u.User+"@"+u.Host+":"+strings.Join(u.Grants["nextcloud"], ","))
				if u.User == "nextcloud" && !u.LogsIn(pw) || u.User == "rapport" && !u.LogsIn("rapport-wachtwoord") {
					t.Errorf("%s@%s logt niet in met zijn oude wachtwoord", u.User, u.Host)
				}
			}
		}
		if len(got) != 10 || !slices.Contains(got, "nextcloud@10.0.20.185:"+strings.Join(all, ",")) || !slices.Contains(got, "rapport@localhost:SELECT") {
			t.Errorf("gebruikers op nc1: %v", got)
		}
	})
	cancelURL := "/api/v1/databases/migrations/" + run.ID + "/cancel"
	if s := c.do("POST", cancelURL, map[string]any{"confirm": "zabbix"}, &ae); s != http.StatusBadRequest {
		t.Fatalf("annuleren met de verkeerde naam: %d %+v", s, ae)
	}
	setClients(nc1, agenttest.Client{User: "nextcloud", Host: "10.0.20.185", Database: "nextcloud", Count: 2})
	if s := c.do("POST", cancelURL, map[string]any{"confirm": "nextcloud"}, &ae); s != http.StatusConflict ||
		!strings.Contains(ae.Message, "de app gebruikt nextcloud op Nextcloud al: 2 verbindingen") {
		t.Fatalf("annuleren terwijl de app de nieuwe gebruikt: %d %+v", s, ae)
	}
	setClients(nc1)
	if s := viewer.do("POST", cancelURL, map[string]any{"confirm": "nextcloud"}, nil); s != http.StatusForbidden {
		t.Fatalf("kijker annuleert: %d", s)
	}
	var cancelled moveRunView
	if s := c.do("POST", cancelURL, map[string]any{"confirm": "nextcloud"}, &cancelled); s != http.StatusOK {
		c.do("POST", cancelURL, map[string]any{"confirm": "nextcloud"}, &ae)
		t.Fatalf("annuleren: %d %+v", s, ae)
	}
	if cancelled.Status != "failed" || cancelled.Stage != "cancelled" || !strings.Contains(cancelled.Message, "Geannuleerd") {
		t.Fatalf("geannuleerd: %+v", cancelled)
	}
	assertMoveGone(t, nc1)
	if s := c.do("POST", cancelURL, map[string]any{"confirm": "nextcloud"}, &ae); s != http.StatusConflict {
		t.Fatalf("twee keer annuleren: %d %+v", s, ae)
	}
	if s := c.do("POST", "/api/v1/databases/migrations/00000000-0000-4000-8000-000000000000/switch", nil, &ae); s != http.StatusNotFound {
		t.Fatalf("onbekende verhuizing: %d %+v", s, ae)
	}

	// De echte: de app staat in onderhoud. Overschakelen kan pas als de app
	// van de oude database af is en op de nieuwe zit.
	setClients(db1)
	if j = start(move); j.Status != "succeeded" {
		t.Fatalf("verhuizen: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	run = plan().Runs[0]
	switchURL := "/api/v1/databases/migrations/" + run.ID + "/switch"
	setClients(db1, agenttest.Client{User: "nextcloud", Host: "10.0.20.187", Database: "nextcloud", Count: 1})
	if s := c.do("POST", switchURL, nil, &ae); s != http.StatusConflict ||
		!strings.Contains(ae.Message, "Nog open met nextcloud op db1: één verbinding (nextcloud vanaf nc2 op db1: 1)") ||
		!strings.Contains(ae.Message, "op Nextcloud is nog niemand met nextcloud verbonden") ||
		!strings.Contains(ae.Message, "Wijs de app naar 10.0.20.186") {
		t.Fatalf("overschakelen te vroeg: %d %+v", s, ae)
	}
	setClients(db1)
	setClients(nc1, agenttest.Client{User: "nextcloud", Host: "10.0.20.185", Database: "nextcloud", Count: 3})
	if s := viewer.do("POST", switchURL, nil, nil); s != http.StatusForbidden {
		t.Fatalf("kijker schakelt over: %d", s)
	}
	var done moveRunView
	if s := c.do("POST", switchURL, nil, &done); s != http.StatusOK {
		c.do("POST", switchURL, nil, &ae)
		t.Fatalf("overschakelen: %d %+v", s, ae)
	}
	if done.Status != "ok" || done.Stage != "done" || !done.Locked || done.SwitchedAt == nil ||
		!strings.Contains(done.Message, "Overgeschakeld: de app gebruikt nextcloud op 10.0.20.186.") ||
		!strings.Contains(done.Message, "Op slot: nextcloud en rapport mogen nextcloud op db1 alleen nog lezen.") {
		t.Fatalf("overgeschakeld: %+v", done)
	}
	grants := func() string {
		var out []string
		db1.Locked(func() {
			for _, u := range db1.MariaDB.Users {
				if u.User == "nextcloud" || u.User == "rapport" || u.User == "beheer" {
					out = append(out, u.User+"@"+u.Host+":"+strings.Join(sortedCopy(u.Grants["nextcloud"]), ","))
				}
			}
		})
		return strings.Join(out, " ")
	}
	open := "nextcloud@10.0.20.186:" + strings.Join(sortedCopy(all), ",") + " nextcloud@10.0.20.187:" + strings.Join(sortedCopy(all), ",") +
		" nextcloud@10.0.20.188:" + strings.Join(sortedCopy(all), ",") + " rapport@10.0.30.5:SELECT beheer@localhost:" + strings.Join(sortedCopy(all), ",")
	locked := "nextcloud@10.0.20.186:SELECT,SHOW VIEW nextcloud@10.0.20.187:SELECT,SHOW VIEW nextcloud@10.0.20.188:SELECT,SHOW VIEW " +
		"rapport@10.0.30.5:SELECT beheer@localhost:" + strings.Join(sortedCopy(all), ",")
	if got := grants(); got != locked {
		t.Fatalf("op slot:\n%s\nwil\n%s", got, locked)
	}
	// Het slot eraf en er weer op.
	lockURL := "/api/v1/databases/migrations/" + run.ID + "/lock"
	var lr moveRunView
	if s := c.do("POST", lockURL, map[string]any{"locked": false}, &lr); s != http.StatusOK || lr.Locked {
		t.Fatalf("slot eraf: %d %+v", s, lr)
	}
	if got := grants(); got != open {
		t.Fatalf("open:\n%s\nwil\n%s", got, open)
	}
	if s := c.do("POST", lockURL, map[string]any{"locked": true}, &lr); s != http.StatusOK || !lr.Locked {
		t.Fatalf("slot erop: %d %+v", s, lr)
	}
	if s := c.do("POST", lockURL, map[string]any{"locked": true}, &lr); s != http.StatusOK || !lr.Locked {
		t.Fatalf("twee keer op slot: %d %+v", s, lr)
	}
	if got := grants(); got != locked {
		t.Fatalf("weer op slot:\n%s", got)
	}
	// Een nieuwe verhuizing van dezelfde database kan niet meer: ze staat
	// al op Nextcloud.
	resync()
	if p = plan(); !p.MoveBlocked || !strings.Contains(p.moveCodes(), "blocked:exists") {
		t.Fatalf("na de verhuizing: %s", p.moveCodes())
	}

	// De hash staat nergens in ClusterForge: niet in de taken, het
	// logboek, db_migrations of de API.
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
	var raw json.RawMessage
	c.do("GET", planURL, nil, &raw)
	dump.Write(raw)
	for _, h := range []string{hash, strings.TrimPrefix(hash, "*"), strings.ToLower(strings.TrimPrefix(hash, "*"))} {
		if strings.Contains(dump.String(), h) {
			t.Errorf("de hash staat ergens")
		}
	}

	var actions []string
	rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE action ~ '^cluster\\.database_(move|moved|locked|unlocked)' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if strings.Join(actions, " ") != "cluster.database_move_failed cluster.database_move_copied cluster.database_move_cancelled "+
		"cluster.database_move_copied cluster.database_moved cluster.database_unlocked cluster.database_locked" {
		t.Errorf("events: %v", actions)
	}
}

// assertMoveGone kijkt dat er na een mislukte of geannuleerde verhuizing
// geen database nextcloud en geen nieuwe gebruiker op nc1 staat.
func assertMoveGone(t *testing.T, nc1 *agenttest.Host) {
	t.Helper()
	nc1.Locked(func() {
		for _, d := range nc1.MariaDB.Databases {
			if d.Name == "nextcloud" {
				t.Errorf("database op nc1: %+v", d)
			}
		}
		for _, u := range nc1.MariaDB.Users {
			if u.User == "nextcloud" || u.User == "rapport" {
				t.Errorf("gebruiker op nc1: %s@%s", u.User, u.Host)
			}
		}
	})
}

func sortedCopy(v []string) []string {
	out := slices.Clone(v)
	slices.Sort(out)
	return out
}
