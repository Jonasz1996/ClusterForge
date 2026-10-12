package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
)

type pgMigrationPlanView struct {
	movePlanView
	Source struct {
		Port int    `json:"port"`
		Node string `json:"node"`
	} `json:"source"`
	Info *struct {
		Owner      string   `json:"owner"`
		Extensions []string `json:"extensions"`
		Charset    string   `json:"charset"`
	} `json:"info"`
	Target *struct {
		Node    string `json:"node"`
		Address string `json:"address"`
		Port    int    `json:"port"`
	} `json:"target"`
	Reach *struct {
		Code int    `json:"code"`
		Seen string `json:"seen"`
	} `json:"reach"`
	PGAccounts []struct {
		User       string   `json:"user"`
		Plugin     string   `json:"plugin"`
		Privileges []string `json:"privileges"`
		MemberOf   []string `json:"member_of"`
		Moves      bool     `json:"moves"`
	} `json:"accounts"`
	Findings []struct {
		Level, Code, Message, Command string
	} `json:"findings"`
}

func (p pgMigrationPlanView) codes() string {
	var out []string
	for _, f := range p.Findings {
		out = append(out, f.Level+":"+f.Code)
	}
	return strings.Join(out, " ")
}

func (p pgMigrationPlanView) command(code string) string {
	for _, f := range p.Findings {
		if f.Code == code {
			return f.Command
		}
	}
	return ""
}

// TestDatabaseMigrationPostgres is de acceptatie van het laatste deel van
// mijlpaal 10 van fase 5: de database van Nextcloud verhuist van de oude
// PostgreSQL 15 op db1 naar PostgreSQL met Patroni op nc1 tot nc3. Het plan
// ziet pg_hba.conf, een ontbrekende extensie en een md5-wachtwoord, met
// wat eraan te doen is. De proef kopieert zonder eigenaars; de verhuizing
// neemt de rollen mee met hun SCRAM-verifier, en annuleren haalt alleen
// weg wat ClusterForge nieuw maakte. Overschakelen zet de oude database op
// slot. De verifiers staan nergens in ClusterForge.
func TestDatabaseMigrationPostgres(t *testing.T) {
	lab := newPatroniHosts(t)
	e, c, g := lab.e, lab.c, lab.g
	ctx := context.Background()
	cid := lab.deploy(t)
	nc1 := g.hosts["nc1"]

	const pw, rapportPW = "nc-wachtwoord-van-de-app", "rapport-wachtwoord"
	var db1n node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db1", "primary_ip": "10.0.20.181"}, &db1n); s != http.StatusCreated {
		t.Fatalf("db1: %d", s)
	}
	var db1 *agenttest.Host
	startAgentWith(t, e, c, db1n.ID, strings.Repeat("d", 32), &addrs{}, func(ag *agent.Agent, root string) {
		db1 = agenttest.NewHost(root, "10.0.20.181")
		db1.SetUnit("postgresql", agenttest.Unit{Enabled: true, Active: true})
		db1.SetUnit("postgresql@15-main", agenttest.Unit{Enabled: true, Active: true})
		db1.Postgres = &agenttest.Postgres{
			Version: "15.8 (Debian 15.8-0+deb12u1)", MaxConnections: 100, Uptime: 86400,
			Roles: append(agenttest.PatroniRoles()[2:],
				agenttest.PGRole{Name: "nextcloud", Verifier: agenttest.SCRAMVerifier(pw), MemberOf: []string{"nc_grp"}},
				agenttest.PGRole{Name: "nc_grp", NoLogin: true},
				agenttest.PGRole{Name: "nc_lid", Verifier: agenttest.SCRAMVerifier("lid-wachtwoord"), MemberOf: []string{"nc_grp"}},
				agenttest.PGRole{Name: "rapport", Verifier: "md5" + strings.Repeat("ab", 16)},
			),
			Databases: []agenttest.Database{
				{
					Name: "nextcloud", Owner: "nextcloud", Size: 300 << 20, Tables: 6, Rows: 100, Views: 1, Routines: 2, Triggers: 1,
					Extensions: []string{"pg_trgm", "postgis"}, ACL: map[string][]string{"rapport": {"CONNECT"}}, Grantees: []string{"nc_grp"},
				},
				{Name: "zabbix", Owner: "zabbix", Size: 2 << 30, Tables: 170},
			},
			Clients: []agenttest.Client{{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 4}},
			// pg_hba.conf kent nc1 nog niet.
			HBADeny: []string{"10.0.20.186"},
		}
		ag.Exec, ag.ExecStdin, ag.Stream, ag.Root, ag.Collector.Run = db1.Exec, db1.ExecStdin, db1.Stream, root, db1.Exec
	})
	network := func(addr string) *agenttest.Host {
		if addr == "10.0.20.181:5432" {
			return db1
		}
		for name, h := range g.hosts {
			if addr == map[string]string{"nc1": "10.0.20.186", "nc2": "10.0.20.187", "nc3": "10.0.20.188"}[name]+":5432" {
				return h
			}
		}
		return nil
	}
	for _, h := range g.hosts {
		h.Locked(func() { h.Network = network })
	}
	source := "postgresql/node/" + db1n.ID
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
	planURL := "/api/v1/databases/migration?source=" + url.QueryEscape(source) + "&database=nextcloud&component=" + cid
	plan := func() pgMigrationPlanView {
		t.Helper()
		var p pgMigrationPlanView
		var ae apiErr
		if s := c.do("GET", planURL, nil, &p); s != http.StatusOK {
			c.do("GET", planURL, nil, &ae)
			t.Fatalf("plan: %d %+v", s, ae)
		}
		return p
	}
	setClients := func(h *agenttest.Host, cs ...agenttest.Client) { h.Locked(func() { h.Postgres.Clients = cs }) }

	// Het plan: de eigenaar, de extensies, de primary nc1 en het eigen VIP
	// voor de app. pg_hba.conf van db1 laat nc1 niet binnen, postgis is op
	// nc1 niet te installeren, en rapport heeft een md5-hash.
	p := plan()
	if p.Source.Port != 5432 || p.Source.Node != "db1" || p.Info == nil || p.Info.Owner != "nextcloud" || p.Info.Charset != "UTF8" ||
		!slices.Equal(p.Info.Extensions, []string{"pg_trgm", "postgis"}) {
		t.Fatalf("bron: %+v %+v", p.Source, p.Info)
	}
	if p.Target == nil || p.Target.Node != "nc1" || p.Target.Address != "10.0.20.190" || p.Target.Port != 5432 || len(p.NewHosts) != 0 {
		t.Fatalf("doel: %+v %v", p.Target, p.NewHosts)
	}
	if p.Reach == nil || p.Reach.Code != 28000 || p.Reach.Seen != "10.0.20.186" {
		t.Fatalf("bereikbaar: %+v", p.Reach)
	}
	if got := p.codes(); got != "blocked:pg_hba blocked:extensions info:upgrade" || !p.Blocked {
		t.Fatalf("bevindingen: %s", got)
	}
	if cmd := p.command("pg_hba"); !strings.Contains(cmd, `echo "host nextcloud all 10.0.20.186/32 scram-sha-256" | sudo tee -a`) ||
		!strings.Contains(cmd, "pg_reload_conf()") {
		t.Errorf("commando voor pg_hba.conf: %q", cmd)
	}
	var accs []string
	for _, a := range p.PGAccounts {
		accs = append(accs, a.User+":"+a.Plugin+":"+strings.Join(a.Privileges, ",")+":"+strings.Join(a.MemberOf, ",")+":"+map[bool]string{true: "mee", false: "niet"}[a.Moves])
	}
	if strings.Join(accs, " ") != "nc_grp:nologin:OBJECTS::mee nc_lid:scram-sha-256::nc_grp:mee nextcloud:scram-sha-256:OWNER:nc_grp:mee rapport:md5:CONNECT::niet" {
		t.Fatalf("rollen: %v", accs)
	}
	if got := p.moveCodes(); got != "blocked:accounts_md5 info:no_trial" || !p.MoveBlocked {
		t.Fatalf("bevindingen verhuizing: %s", got)
	}
	for _, f := range p.MoveFindings {
		if f.Code == "accounts_md5" && !strings.Contains(f.Message, "rapport") {
			t.Errorf("md5: %s", f.Message)
		}
	}

	// Jonas zet de regel in pg_hba.conf en installeert postgis op de nodes.
	db1.Locked(func() { db1.Postgres.HBADeny = nil })
	for _, h := range g.hosts {
		h.Locked(func() { h.Postgres.Available = []string{"pg_trgm", "pgcrypto", "plpgsql", "postgis"} })
	}
	if p = plan(); p.codes() != "info:no_tls info:upgrade" || p.Blocked || p.Reach.Code != 0 {
		t.Fatalf("na pg_hba.conf en postgis: %s %+v", p.codes(), p.Reach)
	}

	// De proef: zonder eigenaars en rechten, terwijl de app doorwerkt.
	trial := map[string]any{"source": source, "database": "nextcloud", "component_id": cid}
	var j job
	if s := c.do("POST", "/api/v1/databases/migration/trial", trial, &j); s != http.StatusAccepted {
		t.Fatalf("proef: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("proef: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	steps := strings.Join(stepLogs(j), "\n")
	for _, want := range []string{
		"op db1 gemaakt: lid van nextcloud, de eigenaar van nextcloud, en alleen om te lezen; hij verloopt na 12 uur.",
		"nc1 haalt de dump op van 10.0.20.181:5432 en importeert hem als nextcloud_proef.",
		"Alle tabellen hebben aan beide kanten evenveel rijen.", "nextcloud_proef weggehaald van nc1",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("proef mist %q:\n%s", want, steps)
		}
	}
	if !slices.ContainsFunc(nc1.Calls(), func(c string) bool {
		return strings.HasPrefix(c, "pg_dump -d host=10.0.20.181 port=5432 dbname=nextcloud user=cf_verhuizing_") && strings.HasSuffix(c, " --no-owner --no-privileges")
	}) {
		t.Errorf("pg_dump van de proef: %q", nc1.Calls())
	}
	p = plan()
	if r := p.Runs[0]; r.Kind != "trial" || r.Status != "ok" || r.Tables != 6 || r.RowsSource != 600 || r.RowsTarget != 600 {
		t.Fatalf("proef: %+v", r)
	}
	var objects string
	if err := e.pool.QueryRow(ctx, "SELECT objects::text FROM db_migrations WHERE kind = 'trial'").Scan(&objects); err != nil || strings.Contains(objects, "events") ||
		!strings.Contains(objects, `"kind": "routines", "source": 2, "target": 2`) {
		t.Errorf("objecten van de proef: %s %v", objects, err)
	}
	pgGone(t, db1, "cf_verhuizing_")
	nc1.Locked(func() {
		if slices.ContainsFunc(nc1.Postgres.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud_proef" }) {
			t.Errorf("de proef staat nog op nc1")
		}
	})

	// De verhuizing kan pas als rapport een wachtwoord met scram-sha-256
	// heeft, en de app niet meer verbonden is.
	move := map[string]any{"source": source, "database": "nextcloud", "component_id": cid}
	var ae apiErr
	if s := c.do("POST", "/api/v1/databases/migration/move", move, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "md5-hash") {
		t.Fatalf("met md5: %d %+v", s, ae)
	}
	db1.Locked(func() {
		i := slices.IndexFunc(db1.Postgres.Roles, func(r agenttest.PGRole) bool { return r.Name == "rapport" })
		db1.Postgres.Roles[i].Verifier = agenttest.SCRAMVerifier(rapportPW)
	})
	if s := c.do("POST", "/api/v1/databases/migration/move", move, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "nog open met nextcloud") {
		t.Fatalf("met verbindingen: %d %+v", s, ae)
	}
	setClients(db1)
	// nc_grp staat al op het nieuwe cluster; annuleren laat hem staan.
	nc1.Locked(func() {
		nc1.Postgres.Roles = append(nc1.Postgres.Roles, agenttest.PGRole{Name: "nc_grp", NoLogin: true})
	})
	start := func() job {
		t.Helper()
		var j job
		if s := c.do("POST", "/api/v1/databases/migration/move", move, &j); s != http.StatusAccepted {
			c.do("POST", "/api/v1/databases/migration/move", move, &ae)
			t.Fatalf("verhuizen: %d %+v", s, ae)
		}
		return waitJobLong(t, c, j.ID)
	}
	if j = start(); j.Status != "succeeded" {
		t.Fatalf("verhuizen: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	if strings.Join(names, " | ") != "Controle | Onderhoud: niemand meer verbonden | Gebruikers maken op nc1 | "+
		"nextcloud ophalen van db1 en importeren op nc1 | Rijen en objecten vergelijken" {
		t.Fatalf("stappen: %v", names)
	}
	steps = strings.Join(stepLogs(j), "\n")
	for _, want := range []string{
		"nc1 haalt de dump op van 10.0.20.181:5432 en importeert hem als nextcloud; de app vindt de database straks op 10.0.20.190.",
		"Rol nc_grp op nc1 (bestond al), zonder inloggen.",
		"Rol nc_lid op nc1 (nieuw), met hetzelfde wachtwoord als op db1, lid van nc_grp.",
		"Rol nextcloud op nc1 (nieuw), met hetzelfde wachtwoord als op db1, lid van nc_grp.",
		"Rol rapport op nc1 (nieuw), met hetzelfde wachtwoord als op db1.",
		"Alle tabellen hebben aan beide kanten evenveel rijen.",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("verhuizing mist %q:\n%s", want, steps)
		}
	}
	if !slices.ContainsFunc(nc1.Calls(), func(c string) bool {
		return strings.HasPrefix(c, "pg_dump -d host=10.0.20.181 port=5432 dbname=nextcloud user=cf_verhuizing_") &&
			strings.HasSuffix(c, " --no-password --lock-wait-timeout=5min")
	}) {
		t.Errorf("pg_dump van de verhuizing: %q", nc1.Calls())
	}
	// Op nc1: de database met dezelfde eigenaar en het recht van rapport,
	// en de rollen loggen in met hun oude wachtwoord.
	nc1.Locked(func() {
		pg := nc1.Postgres
		i := slices.IndexFunc(pg.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud" })
		if i < 0 || pg.Databases[i].Owner != "nextcloud" || pg.Databases[i].Tables != 6 || !slices.Equal(pg.Databases[i].ACL["rapport"], []string{"CONNECT"}) {
			t.Errorf("nextcloud op nc1: %+v", pg.Databases)
		}
		for name, password := range map[string]string{"nextcloud": pw, "rapport": rapportPW, "nc_lid": "lid-wachtwoord"} {
			j := slices.IndexFunc(pg.Roles, func(r agenttest.PGRole) bool { return r.Name == name })
			if j < 0 || !pg.Roles[j].LogsIn(password, time.Now()) || pg.Roles[j].Super {
				t.Errorf("%s op nc1: %+v", name, pg.Roles)
			}
		}
	})
	p = plan()
	run := p.Runs[0]
	var made []string
	for _, a := range run.Accounts {
		made = append(made, a.User)
	}
	if run.Stage != "copied" || run.TargetAddress != "10.0.20.190" || strings.Join(made, " ") != "nc_grp nc_lid nextcloud rapport" {
		t.Fatalf("gekopieerd: %+v", run)
	}
	var raw json.RawMessage
	c.do("GET", planURL, nil, &raw)
	if !strings.Contains(string(raw), `{"hosts":[],"new":false,"old_hosts":[],"privileges":["OBJECTS"],"user":"nc_grp"}`) ||
		!strings.Contains(string(raw), `{"hosts":[],"new":true,"old_hosts":[],"privileges":["OWNER"],"user":"nextcloud"}`) {
		t.Errorf("rollen van de verhuizing: %s", raw)
	}

	// Annuleren: de kopie en de nieuwe rollen gaan weg, nc_grp blijft.
	var cancelled moveRunView
	if s := c.do("POST", "/api/v1/databases/migrations/"+run.ID+"/cancel", map[string]any{"confirm": "nextcloud"}, &cancelled); s != http.StatusOK {
		t.Fatalf("annuleren: %d", s)
	}
	nc1.Locked(func() {
		pg := nc1.Postgres
		var roles []string
		for _, r := range pg.Roles {
			roles = append(roles, r.Name)
		}
		if slices.ContainsFunc(pg.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud" }) ||
			strings.Join(roles, " ") != "cf_replicatie cf_rewind postgres nc_grp" {
			t.Errorf("na annuleren op nc1: %v %+v", roles, pg.Databases)
		}
	})

	// De echte: overschakelen zet de oude database op slot, voor iedereen.
	if j = start(); j.Status != "succeeded" {
		t.Fatalf("verhuizen: %s %s", j.Status, j.Error)
	}
	run = plan().Runs[0]
	setClients(nc1, agenttest.Client{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 3})
	var done moveRunView
	if s := c.do("POST", "/api/v1/databases/migrations/"+run.ID+"/switch", nil, &done); s != http.StatusOK {
		c.do("POST", "/api/v1/databases/migrations/"+run.ID+"/switch", nil, &ae)
		t.Fatalf("overschakelen: %d %+v", s, ae)
	}
	if done.Stage != "done" || !done.Locked || !strings.Contains(done.Message, "Overgeschakeld: de app gebruikt nextcloud op 10.0.20.190.") ||
		!strings.Contains(done.Message, "Op slot: wie nu met nextcloud op db1 verbindt, kan alleen nog lezen.") {
		t.Fatalf("overgeschakeld: %+v", done)
	}
	readOnly := func() (ro bool) {
		db1.Locked(func() { ro = db1.Postgres.Databases[0].ReadOnly })
		return ro
	}
	if !readOnly() {
		t.Fatalf("de oude database staat niet op slot")
	}
	lockURL := "/api/v1/databases/migrations/" + run.ID + "/lock"
	var lr moveRunView
	if s := c.do("POST", lockURL, map[string]any{"locked": false}, &lr); s != http.StatusOK || lr.Locked || readOnly() {
		t.Fatalf("slot eraf: %d %+v", s, lr)
	}
	if s := c.do("POST", lockURL, map[string]any{"locked": true}, &lr); s != http.StatusOK || !lr.Locked || !readOnly() {
		t.Fatalf("slot erop: %d %+v", s, lr)
	}
	pgGone(t, db1, "cf_verhuizing_")

	// De verifiers staan nergens in ClusterForge.
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
	c.do("GET", planURL, nil, &raw)
	dump.Write(raw)
	if strings.Contains(dump.String(), "SCRAM-SHA-256$") {
		t.Errorf("een verifier staat in ClusterForge")
	}
}

// pgGone kijkt dat er op h geen rol meer met prefix staat.
func pgGone(t *testing.T, h *agenttest.Host, prefix string) {
	t.Helper()
	h.Locked(func() {
		for _, r := range h.Postgres.Roles {
			if strings.HasPrefix(r.Name, prefix) {
				t.Errorf("rol %s staat nog op %s", r.Name, h.Address)
			}
		}
	})
}
