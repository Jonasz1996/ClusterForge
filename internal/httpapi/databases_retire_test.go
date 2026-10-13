package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type retireRunView struct {
	moveRunView
	RetireJobID   *string `json:"retire_job_id"`
	RetiredAt     *string `json:"retired_at"`
	RetireNode    string  `json:"retire_node"`
	RetireFile    string  `json:"retire_file"`
	RetireBytes   int64   `json:"retire_bytes"`
	RetireMessage string  `json:"retire_message"`
}

type retireSourceView struct {
	Name      string   `json:"name"`
	Servers   []string `json:"servers"`
	Readable  bool     `json:"readable"`
	Empty     bool     `json:"empty"`
	Remaining []struct {
		Database string `json:"database"`
		Engine   string `json:"engine"`
		Moved    bool   `json:"moved"`
	} `json:"remaining"`
	Moves []struct {
		Run       retireRunView `json:"run"`
		Exists    bool          `json:"exists"`
		LastUsed  *time.Time    `json:"last_used"`
		LastBy    string        `json:"last_by"`
		LastLocal *time.Time    `json:"last_local"`
		LocalBy   string        `json:"local_by"`
		ReadyAt   *time.Time    `json:"ready_at"`
		Ready     bool          `json:"ready"`
		Active    *struct {
			Kind string `json:"kind"`
		} `json:"active"`
	} `json:"moves"`
	Rules []struct {
		ClusterName string     `json:"cluster_name"`
		Engine      string     `json:"engine"`
		Address     string     `json:"address"`
		Port        int        `json:"port"`
		LastSeen    *time.Time `json:"last_seen"`
		State       string     `json:"state"`
	} `json:"rules"`
}

func (v retireSourceView) remaining() string {
	var out []string
	for _, d := range v.Remaining {
		s := d.Database
		if d.Moved {
			s += " (verhuisd)"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// TestDatabaseRetire is de acceptatie van mijlpaal 14 van fase 5: na de
// verhuizing van nextcloud van db1 naar Nextcloud kan de oude database pas
// weg als er zeven dagen niemand van buiten db1 mee verbonden was, en nu
// niemand verbonden is. Afbouwen maakt een laatste dump op db1 die blijft
// staan, haalt de database en de gebruikers van de app weg en laat een
// gebruiker met rechten op een andere database staan. Is db1 leeg, dan zegt
// ClusterForge dat, en de firewallregel van Nextcloud naar db1 kan weg.
func TestDatabaseRetire(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g := lab.e, lab.c, lab.g
	ctx := context.Background()
	const pw = "nc-wachtwoord-van-de-app"
	all := slices.Clone(agenttest.AllPrivileges)
	ncUser := func(host string) agenttest.MariaUser {
		return agenttest.MariaUser{User: "nextcloud", Host: host, Password: pw, Grants: map[string][]string{"nextcloud": all}}
	}
	var db1n node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db1", "primary_ip": "10.0.20.181"}, &db1n); s != http.StatusCreated {
		t.Fatalf("db1: %d", s)
	}
	var db1 *agenttest.Host
	var dumps string
	startAgentWith(t, e, c, db1n.ID, strings.Repeat("d", 32), &addrs{}, func(ag *agent.Agent, root string) {
		db1 = agenttest.NewHost(root, "10.0.20.181")
		db1.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
		users := append(agenttest.DebianUsers(), ncUser("10.0.20.186"), ncUser("10.0.20.187"), ncUser("10.0.20.188"),
			agenttest.MariaUser{User: "rapport", Host: "10.0.30.5", Password: "rapport-wachtwoord",
				Grants: map[string][]string{"nextcloud": {"SELECT"}, "zabbix": {"SELECT"}}})
		db1.MariaDB = &agenttest.MariaDB{
			Version: "10.11.11-MariaDB-0+deb12u1", MaxConnections: 151, Uptime: 86400, Users: users,
			Databases: []agenttest.Database{{Name: "nextcloud", Size: 300 << 20, Tables: 6, Rows: 100}, {Name: "zabbix", Size: 2 << 30, Tables: 170}},
			Clients:   []agenttest.Client{{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 4}},
		}
		dumps = filepath.Join(root, "var/backups/clusterforge-db")
		ag.Exec, ag.ExecStdin, ag.Stream, ag.Root, ag.Collector.Run, ag.DumpDir = db1.Exec, db1.ExecStdin, db1.Stream, root, db1.Exec, dumps
	})
	for _, h := range g.hosts {
		h.Locked(func() {
			h.Network = func(addr string) *agenttest.Host {
				if addr == "10.0.20.181:3306" {
					return db1
				}
				return nil
			}
		})
	}
	nc1 := g.hosts["nc1"]
	source := "mariadb/node/" + db1n.ID
	resync := func() {
		t.Helper()
		if err := e.dbs.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	setClients := func(h *agenttest.Host, cs ...agenttest.Client) { h.Locked(func() { h.MariaDB.Clients = cs }) }
	eventually(t, "db1 onder Databases", func() bool {
		resync()
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM db_databases WHERE node_id = $1", db1n.ID).Scan(&n)
		return n == 2
	})
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}

	// Nextcloud verhuist naar nc1 tot nc3 en schakelt over.
	setClients(db1)
	resync()
	var j job
	var ae apiErr
	move := map[string]any{"source": source, "database": "nextcloud", "component_id": lab.cid}
	if s := c.do("POST", "/api/v1/databases/migration/move", move, &j); s != http.StatusAccepted {
		t.Fatalf("verhuizen: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("verhuizen: %s %s", j.Status, j.Error)
	}
	var p movePlanView
	c.do("GET", "/api/v1/databases/migration?source="+url.QueryEscape(source)+"&database=nextcloud&component="+lab.cid, nil, &p)
	id := p.Runs[0].ID
	setClients(nc1, agenttest.Client{User: "nextcloud", Host: "10.0.20.185", Database: "nextcloud", Count: 3})
	if s := c.do("POST", "/api/v1/databases/migrations/"+id+"/switch", nil, &ae); s != http.StatusOK {
		t.Fatalf("overschakelen: %d %+v", s, ae)
	}
	setClients(nc1)

	overview := func() retireSourceView {
		t.Helper()
		var v struct {
			Items []retireSourceView `json:"items"`
		}
		if s := c.do("GET", "/api/v1/databases/retire", nil, &v); s != http.StatusOK || len(v.Items) != 1 {
			t.Fatalf("overzicht: %d %+v", s, v)
		}
		return v.Items[0]
	}
	v := overview()
	mv := v.Moves[0]
	if v.Name != "db1" || strings.Join(v.Servers, " ") != "db1" || !v.Readable || v.Empty || len(v.Moves) != 1 || mv.Ready || !mv.Exists ||
		mv.ReadyAt == nil || mv.Run.SwitchedAt == nil || mv.Run.RetiredAt != nil || v.remaining() != "nextcloud (verhuisd) zabbix" {
		t.Fatalf("na het overschakelen: %+v", v)
	}
	switched, _ := time.Parse(time.RFC3339Nano, *mv.Run.SwitchedAt)
	if d := mv.ReadyAt.Sub(switched); d != 7*24*time.Hour {
		t.Fatalf("afbouwen kan na %s", d)
	}
	if len(v.Rules) != 1 || v.Rules[0].ClusterName != "Nextcloud" || v.Rules[0].Address != "10.0.20.181" || v.Rules[0].Port != 3306 ||
		v.Rules[0].LastSeen == nil || v.Rules[0].State != "until_retired" {
		t.Fatalf("regel: %+v", v.Rules)
	}

	retireURL := "/api/v1/databases/migrations/" + id + "/retire"
	retire := map[string]any{"name": "nextcloud"}
	if s := c.do("POST", retireURL, retire, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "afbouwen kan vanaf ") ||
		!strings.Contains(ae.Message, ": zeven dagen na het overschakelen op ") {
		t.Fatalf("te vroeg: %d %+v", s, ae)
	}

	// Acht dagen later. Nextcloud verbond twee dagen geleden nog van nc2:
	// dan kan het pas vijf dagen later.
	exec("UPDATE db_migrations SET switched_at = switched_at - interval '8 days' WHERE id = $1", id)
	exec("UPDATE db_clients SET first_seen = first_seen - interval '9 days', last_seen = last_seen - interval '9 days' WHERE node_id = $1", db1n.ID)
	if v = overview(); !v.Moves[0].Ready {
		t.Fatalf("na acht dagen: %+v", v.Moves[0])
	}
	setClients(db1, agenttest.Client{User: "nextcloud", Host: "10.0.20.187", Database: "nextcloud", Count: 1})
	resync()
	setClients(db1)
	exec("UPDATE db_clients SET last_seen = now() - interval '2 days' WHERE node_id = $1 AND address = '10.0.20.187'", db1n.ID)
	v = overview()
	if mv = v.Moves[0]; mv.Ready || mv.LastBy != "Nextcloud (nc1, nc2) als nextcloud" || mv.LastUsed == nil ||
		mv.ReadyAt.Sub(*mv.LastUsed) != 7*24*time.Hour || v.Rules[0].State != "in_use" {
		t.Fatalf("na een verbinding van nc2: %+v %+v", mv, v.Rules)
	}
	if s := c.do("POST", retireURL, retire, &ae); s != http.StatusConflict ||
		!strings.Contains(ae.Message, "zeven dagen nadat Nextcloud (nc1, nc2) als nextcloud op ") || !strings.Contains(ae.Message, "nog met nextcloud verbonden was") {
		t.Fatalf("na een verbinding van nc2: %d %+v", s, ae)
	}
	exec("UPDATE db_clients SET last_seen = last_seen - interval '9 days' WHERE node_id = $1 AND address = '10.0.20.187'", db1n.ID)

	// Een back-up op db1 zelf houdt het niet tegen, maar zolang hij
	// verbonden is, gaat de database niet weg.
	setClients(db1, agenttest.Client{User: "root", Host: "localhost", Database: "nextcloud", Count: 1})
	resync()
	if mv = overview().Moves[0]; !mv.Ready || mv.LastLocal == nil || mv.LocalBy != "de databasenode zelf (db1) als root" {
		t.Fatalf("met een back-up op db1: %+v", mv)
	}
	if s := c.do("POST", retireURL, retire, &ae); s != http.StatusConflict ||
		!strings.Contains(ae.Message, "nog open met nextcloud: één verbinding (root vanaf de server zelf op db1: 1)") {
		t.Fatalf("met iemand verbonden: %d %+v", s, ae)
	}
	setClients(db1)

	// Een kijker, de verkeerde naam, en prod zonder tweestapsverificatie.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := viewer.do("POST", retireURL, retire, nil); s != http.StatusForbidden {
		t.Fatalf("kijker: %d", s)
	}
	if s := viewer.do("GET", "/api/v1/databases/retire", nil, nil); s != http.StatusOK {
		t.Fatalf("kijker leest: %d", s)
	}
	if s := c.do("POST", retireURL, map[string]any{"name": "zabbix"}, &ae); s != http.StatusBadRequest || !strings.Contains(ae.Message, "nextcloud") {
		t.Fatalf("verkeerde naam: %d %+v", s, ae)
	}
	exec("UPDATE clusters SET environment = 'prod' WHERE id = $1", lab.cluster.ID)
	if s := c.do("POST", retireURL, retire, &ae); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("prod: %d %+v", s, ae)
	}
	exec("UPDATE clusters SET environment = 'lab' WHERE id = $1", lab.cluster.ID)

	// Afbouwen.
	if s := c.do("POST", retireURL, retire, &j); s != http.StatusAccepted {
		c.do("POST", retireURL, retire, &ae)
		t.Fatalf("afbouwen: %d %+v", s, ae)
	}
	if j.Title != "Afbouwen van nextcloud op db1" {
		t.Fatalf("titel: %s", j.Title)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("afbouwen: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	if strings.Join(names, " | ") != "Controle | Laatste dump | Database weghalen | Gebruikers weghalen | Wat er nog op de oude server staat" {
		t.Fatalf("stappen: %v", names)
	}
	steps := strings.Join(stepLogs(j), "\n")
	for _, want := range []string{
		"Niemand is met nextcloud op db1 verbonden.", "begonnen op db1.", "Klaar: ", "6 tabellen", "in /var/backups/clusterforge-db/nextcloud/nextcloud-",
		"nextcloud is weg van db1.", "nextcloud@10.0.20.186, nextcloud@10.0.20.187 en nextcloud@10.0.20.188 weggehaald.",
		"rapport@10.0.30.5 blijft: hij heeft ook rechten op zabbix.", "Nog op db1: zabbix (MariaDB).",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("stappen missen %q:\n%s", want, steps)
		}
	}

	// Op db1: zabbix en rapport blijven, de dump staat er.
	db1.Locked(func() {
		var dbs, users []string
		for _, d := range db1.MariaDB.Databases {
			dbs = append(dbs, d.Name)
		}
		for _, u := range db1.MariaDB.Users {
			if u.User == "nextcloud" || u.User == "rapport" {
				users = append(users, u.User+"@"+u.Host)
			}
		}
		if strings.Join(dbs, " ") != "zabbix" || strings.Join(users, " ") != "rapport@10.0.30.5" {
			t.Errorf("op db1: %v %v", dbs, users)
		}
	})
	files, _ := filepath.Glob(filepath.Join(dumps, "nextcloud", "nextcloud-*.sql.zst"))
	if len(files) != 1 {
		t.Fatalf("dumps op db1: %v", files)
	}
	if st, err := os.Stat(files[0]); err != nil || st.Size() == 0 {
		t.Fatalf("dump: %v", err)
	}

	resync()
	v = overview()
	mv = v.Moves[0]
	r := mv.Run
	if r.RetiredAt == nil || r.RetireNode != "db1" || r.RetireFile != filepath.Base(files[0]) || r.RetireBytes <= 0 || mv.ReadyAt != nil || mv.Ready ||
		mv.Exists || mv.Active != nil {
		t.Fatalf("afgebouwd: %+v", mv)
	}
	for _, want := range []string{
		"Laatste dump op db1: /var/backups/clusterforge-db/nextcloud/" + r.RetireFile + " (", "Hij blijft staan tot je hem weghaalt.",
		"nextcloud is weg van db1.", "Weggehaald: nextcloud@10.0.20.186, nextcloud@10.0.20.187 en nextcloud@10.0.20.188.",
		"Blijft staan: rapport@10.0.30.5: hij heeft ook rechten op zabbix.",
		"Wie blijft, houdt zijn rechten op nextcloud tot je ze weghaalt: REVOKE ALL PRIVILEGES ON `nextcloud`.* FROM 'rapport'@'10.0.30.5';",
		"Nog op db1: zabbix (MariaDB).",
	} {
		if !strings.Contains(r.RetireMessage, want) {
			t.Errorf("bericht mist %q:\n%s", want, r.RetireMessage)
		}
	}
	if v.Empty || v.remaining() != "zabbix" || v.Rules[0].State != "unused" {
		t.Fatalf("na het afbouwen: %+v", v)
	}
	if s := c.do("POST", retireURL, retire, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "al afgebouwd") {
		t.Fatalf("twee keer: %d %+v", s, ae)
	}
	if s := c.do("POST", "/api/v1/databases/migrations/"+id+"/lock", map[string]any{"locked": false}, &ae); s != http.StatusConflict {
		t.Fatalf("slot na het afbouwen: %d %+v", s, ae)
	}
	// Het plan kent de database niet meer, maar toont de verhuizing nog.
	var after struct {
		Info     *struct{} `json:"info"`
		Findings []struct{ Code string }
		Runs     []struct {
			RetiredAt *time.Time `json:"retired_at"`
		} `json:"runs"`
	}
	c.do("GET", "/api/v1/databases/migration?source="+url.QueryEscape(source)+"&database=nextcloud&component="+lab.cid, nil, &after)
	if after.Info != nil || len(after.Findings) == 0 || after.Findings[0].Code != "not_found" || after.Runs[0].RetiredAt == nil {
		t.Fatalf("plan na het afbouwen: %+v", after)
	}

	// Zabbix verhuist ook (hier met de hand): db1 is leeg.
	db1.Locked(func() { db1.MariaDB.Databases = nil })
	resync()
	if v = overview(); !v.Empty || len(v.Remaining) != 0 {
		t.Fatalf("leeg: %+v", v)
	}

	var audit struct {
		Items []struct {
			Action  string `json:"action"`
			Summary string `json:"summary"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/audit?category=inventory", nil, &audit)
	i := slices.IndexFunc(audit.Items, func(x struct {
		Action  string `json:"action"`
		Summary string `json:"summary"`
	}) bool {
		return x.Action == "cluster.database_retired"
	})
	if i < 0 || !strings.HasPrefix(audit.Items[i].Summary, "Oude database nextcloud op db1 afgebouwd; laatste dump op db1 (") ||
		!strings.HasSuffix(audit.Items[i].Summary, "); weggehaald: nextcloud@10.0.20.186, nextcloud@10.0.20.187, nextcloud@10.0.20.188; 1 gebruiker blijft") {
		t.Fatalf("logboek: %+v", audit.Items)
	}
}
