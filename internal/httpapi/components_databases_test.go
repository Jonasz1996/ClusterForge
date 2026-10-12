package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// galeraLab is Nextcloud met MariaDB Galera op nc1 tot nc3
// (10.0.20.186-188), uitgerold door ClusterForge.
type galeraLab struct {
	e       *testEnv
	c       *client
	cluster cluster
	g       *galeraHosts
	ids     map[string]string
	base    string
	cid     string
}

func newGaleraLab(t *testing.T) galeraLab {
	t.Helper()
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
			ag.Exec, ag.ExecStdin, ag.Root, ag.Collector.Run = h.Exec, h.ExecStdin, root, h.Exec
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
	return galeraLab{e: e, c: c, cluster: nc, g: g, ids: ids, base: base, cid: comps.Items[0].ID}
}

type componentDatabasesView struct {
	Node      string   `json:"node"`
	Address   string   `json:"address"`
	Port      int      `json:"port"`
	Hosts     []string `json:"hosts"`
	Databases []struct {
		Name        string   `json:"name"`
		Connections int      `json:"connections"`
		Users       []string `json:"users"`
	} `json:"databases"`
	Users []struct {
		Name      string   `json:"name"`
		Hosts     []string `json:"hosts"`
		System    bool     `json:"system"`
		Databases []struct {
			Database string `json:"database"`
			All      bool   `json:"all"`
		} `json:"databases"`
	} `json:"users"`
}

func (v componentDatabasesView) names() (dbs, users []string) {
	for _, d := range v.Databases {
		dbs = append(dbs, d.Name+"("+strings.Join(d.Users, ",")+")")
	}
	for _, u := range v.Users {
		s := u.Name
		if u.System {
			s += "*"
		}
		users = append(users, s)
	}
	return dbs, users
}

// TestComponentDatabases is de acceptatie van mijlpaal 6 van fase 5: in
// de Galera-database van Nextcloud een database met een eigen gebruiker
// aanmaken, waarvan het wachtwoord één keer in het antwoord staat en
// nergens anders; een nieuw wachtwoord geven; en weghalen met de naam.
func TestComponentDatabases(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g := lab.e, lab.c, lab.g
	ctx := context.Background()
	if s := c.do("POST", "/api/v1/clusters/"+lab.cluster.ID+"/vips", map[string]any{"address": "10.0.20.185"}, nil); s != http.StatusCreated {
		t.Fatalf("VIP: %d", s)
	}
	dbs := lab.base + "/" + lab.cid + "/databases"
	users := lab.base + "/" + lab.cid + "/users/"
	read := func() componentDatabasesView {
		t.Helper()
		var v componentDatabasesView
		var ae apiErr
		if s := c.do("GET", dbs, nil, &v); s != http.StatusOK {
			c.do("GET", dbs, nil, &ae)
			t.Fatalf("databases: %d %+v", s, ae)
		}
		return v
	}

	// Leeg: alleen de gebruikers van MariaDB zelf. Een nieuwe gebruiker mag
	// inloggen vanaf de nodes en het VIP.
	v := read()
	if d, u := v.names(); v.Node != "nc1" || len(d) != 0 || strings.Join(u, " ") != "mariadb.sys* mysql* root*" {
		t.Fatalf("leeg: %+v %v %v", v, d, u)
	}
	if strings.Join(v.Hosts, " ") != "10.0.20.186 10.0.20.187 10.0.20.188 10.0.20.185" || v.Address != "10.0.20.186" || v.Port != 3306 {
		t.Fatalf("hosts: %+v", v)
	}

	// Een kijker ziet de lijst maar maakt niets aan.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := viewer.do("GET", dbs, nil, nil); s != http.StatusOK {
		t.Fatalf("kijker lezen: %d", s)
	}
	if s := viewer.do("POST", dbs, map[string]any{"name": "nextcloud"}, nil); s != http.StatusForbidden {
		t.Fatalf("kijker aanmaken: %d", s)
	}

	// Aanmaken.
	var created struct {
		Database, User, Password, Address, Node string
		Hosts                                   []string
		Port                                    int
	}
	if s := c.do("POST", dbs, map[string]any{"name": "nextcloud"}, &created); s != http.StatusCreated {
		t.Fatalf("aanmaken: %d", s)
	}
	pw := created.Password
	if created.Database != "nextcloud" || created.User != "nextcloud" || len(pw) < 20 || created.Node != "nc1" || created.Address != "10.0.20.186" ||
		len(created.Hosts) != 4 {
		t.Fatalf("aangemaakt: %+v", created)
	}
	nc1 := g.hosts["nc1"]
	var got []string
	for _, u := range nc1.MariaDB.Users {
		if u.User == "nextcloud" {
			got = append(got, u.Host)
			if u.Password != pw || len(u.Grants["nextcloud"]) == 0 {
				t.Errorf("gebruiker op %s: %+v", u.Host, u)
			}
		}
	}
	if slices.Sort(got); strings.Join(got, " ") != "10.0.20.185 10.0.20.186 10.0.20.187 10.0.20.188" {
		t.Errorf("hosts in MariaDB: %v", got)
	}
	// Het wachtwoord staat in geen enkel argument en geen event.
	for _, h := range g.hosts {
		for _, call := range h.Calls() {
			if strings.Contains(call, pw) {
				t.Errorf("wachtwoord in %q", call)
			}
		}
	}
	var payloads []string
	rows, err := e.pool.Query(ctx, "SELECT action || ' ' || payload::text FROM events WHERE action LIKE 'cluster.database_%' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		payloads = append(payloads, p)
	}
	rows.Close()
	if all := strings.Join(payloads, "\n"); strings.Contains(all, pw) || !strings.Contains(all, "cluster.database_created") {
		t.Errorf("events: %s", all)
	}

	v = read()
	if d, u := v.names(); strings.Join(d, " ") != "nextcloud(nextcloud)" || strings.Join(u, " ") != "nextcloud mariadb.sys* mysql* root*" ||
		len(v.Users[0].Databases) != 1 || !v.Users[0].Databases[0].All || len(v.Users[0].Hosts) != 4 {
		t.Fatalf("na aanmaken: %v %v %+v", d, u, v.Users)
	}

	// Wat niet kan.
	var ae apiErr
	if s := c.do("POST", dbs, map[string]any{"name": "nextcloud"}, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "bestaat al") {
		t.Errorf("twee keer: %d %+v", s, ae)
	}
	if s := c.do("POST", dbs, map[string]any{"name": "zabbix", "user": "nextcloud"}, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "Nieuw wachtwoord") {
		t.Errorf("gebruiker bestaat: %d %+v", s, ae)
	}
	if s := c.do("POST", dbs, map[string]any{"name": "Next-Cloud"}, &ae); s != http.StatusBadRequest {
		t.Errorf("ongeldige naam: %d %+v", s, ae)
	}
	if s := c.do("POST", dbs, map[string]any{"name": "mysql"}, &ae); s != http.StatusBadRequest {
		t.Errorf("mysql: %d %+v", s, ae)
	}
	if s := c.do("POST", users+"root/password", nil, &ae); s != http.StatusBadRequest {
		t.Errorf("root: %d %+v", s, ae)
	}

	// Een nieuw wachtwoord.
	var np struct {
		User, Password string
		Hosts          []string
	}
	if s := c.do("POST", users+"nextcloud/password", nil, &np); s != http.StatusOK || np.Password == pw || len(np.Hosts) != 4 {
		t.Fatalf("nieuw wachtwoord: %d %+v", s, np)
	}
	for _, u := range nc1.MariaDB.Users {
		if u.User == "nextcloud" && u.Password != np.Password {
			t.Errorf("wachtwoord op %s: %q", u.Host, u.Password)
		}
	}

	// Staat nc1 niet Synced, dan schrijft ClusterForge op nc2.
	nc1.Locked(func() { nc1.MariaDB.Galera.State, nc1.MariaDB.Galera.Comment = 2, "Donor/Desynced" })
	if s := c.do("POST", dbs, map[string]any{"name": "zabbix"}, &created); s != http.StatusCreated || created.Node != "nc2" {
		t.Fatalf("op nc2: %d %+v", s, created)
	}
	if !slices.ContainsFunc(g.hosts["nc2"].MariaDB.Databases, func(d agenttest.Database) bool { return d.Name == "zabbix" }) {
		t.Errorf("zabbix niet op nc2: %+v", g.hosts["nc2"].MariaDB.Databases)
	}
	nc1.Locked(func() { nc1.MariaDB.Galera.State, nc1.MariaDB.Galera.Comment = 4, "Synced" })

	// Weghalen alleen met de naam.
	if s := c.do("POST", dbs+"/nextcloud/drop", map[string]any{"name": "zabbix"}, &ae); s != http.StatusBadRequest || !strings.Contains(ae.Message, "naam van de database") {
		t.Errorf("verkeerde naam: %d %+v", s, ae)
	}
	if s := c.do("POST", dbs+"/nextcloud/drop", map[string]any{"name": "nextcloud"}, nil); s != http.StatusNoContent {
		t.Fatalf("database weghalen: %d", s)
	}
	if s := c.do("POST", users+"nextcloud/drop", map[string]any{"name": "nextcloud"}, nil); s != http.StatusNoContent {
		t.Fatalf("gebruiker weghalen: %d", s)
	}
	if slices.ContainsFunc(nc1.MariaDB.Users, func(u agenttest.MariaUser) bool { return u.User == "nextcloud" }) ||
		slices.ContainsFunc(nc1.MariaDB.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud" }) {
		t.Errorf("nog op nc1: %+v %+v", nc1.MariaDB.Users, nc1.MariaDB.Databases)
	}
	if s := c.do("POST", dbs+"/nextcloud/drop", map[string]any{"name": "nextcloud"}, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "bestaat niet") {
		t.Errorf("twee keer weghalen: %d %+v", s, ae)
	}

	// Op prod vraagt weghalen en een nieuw wachtwoord tweestapsverificatie.
	if _, err := e.pool.Exec(ctx, "UPDATE clusters SET environment = 'prod' WHERE id = $1", lab.cluster.ID); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", users+"zabbix/password", nil, &ae); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Errorf("prod wachtwoord: %d %+v", s, ae)
	}
	if s := c.do("POST", dbs+"/zabbix/drop", map[string]any{"name": "zabbix"}, &ae); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Errorf("prod weghalen: %d %+v", s, ae)
	}

	// Draait de database nergens, dan zegt ClusterForge dat.
	for _, h := range g.hosts {
		h.Locked(func() { h.MariaDB.Down, h.MariaDB.Galera = true, nil })
	}
	if s := c.do("GET", dbs, nil, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "geen enkele datanode is Synced") {
		t.Errorf("database weg: %d %+v", s, ae)
	}

	var actions []string
	rows, err = e.pool.Query(ctx, "SELECT action, payload FROM events WHERE action IN ('cluster.database_created', 'cluster.database_password', "+
		"'cluster.database_dropped', 'cluster.database_user_dropped') ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		var p json.RawMessage
		_ = rows.Scan(&a, &p)
		actions = append(actions, a)
		if strings.Contains(string(p), np.Password) || strings.Contains(string(p), pw) {
			t.Errorf("wachtwoord in %s: %s", a, p)
		}
	}
	rows.Close()
	want := "cluster.database_created cluster.database_password cluster.database_created cluster.database_dropped cluster.database_user_dropped"
	if strings.Join(actions, " ") != want {
		t.Errorf("events: %v", actions)
	}
}
