package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/galera"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type manageView struct {
	Engine string `json:"engine"`
	Title  string `json:"title"`
	Vip    string `json:"vip"`
	Prod   bool   `json:"prod"`
	Hash   string `json:"hash"`
	Nodes  []struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		State    string `json:"state"`
		Healthy  bool   `json:"healthy"`
		Role     string `json:"role"`
		Sync     bool   `json:"sync"`
		LagBytes *int64 `json:"lag_bytes"`
		Vip      bool   `json:"vip"`
		Actions  []struct {
			Action  string   `json:"action"`
			Title   string   `json:"title"`
			Allowed bool     `json:"allowed"`
			Reason  string   `json:"reason"`
			Effects []string `json:"effects"`
		} `json:"actions"`
	} `json:"nodes"`
}

// can geeft de acties die kunnen, zoals "nc1:resync nc2:resync".
func (v manageView) can() string {
	var out []string
	for _, n := range v.Nodes {
		for _, a := range n.Actions {
			if a.Allowed {
				out = append(out, n.Hostname+":"+a.Action)
			}
		}
	}
	return strings.Join(out, " ")
}

// action geeft de reden en de gevolgen van een actie op een node, als
// tekst.
func (v manageView) action(host, action string) (reason, effects string) {
	for _, n := range v.Nodes {
		for _, a := range n.Actions {
			if n.Hostname == host && a.Action == action {
				return a.Reason, strings.Join(a.Effects, "\n")
			}
		}
	}
	return "geen actie " + action + " op " + host, ""
}

func (v manageView) node(host string) (healthy bool, role string, sync bool) {
	for _, n := range v.Nodes {
		if n.Hostname == host {
			return n.Healthy, n.Role, n.Sync
		}
	}
	return false, "", false
}

// clusterEvents geeft de payload van de events van het cluster met deze
// action, oudste eerst.
func clusterEvents(t *testing.T, e *testEnv, clusterID, action string) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), "SELECT payload::text FROM events WHERE cluster_id = $1 AND action = $2 ORDER BY id", clusterID, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		out = append(out, p)
	}
	return out
}

func stepNamesOf(j job) []string {
	var out []string
	for _, st := range j.Steps {
		out = append(out, st.Name)
	}
	return out
}

func stepLogOf(j job, name string) string {
	for _, st := range j.Steps {
		if st.Name == name {
			return strings.Join(st.Log, "\n")
		}
	}
	return ""
}

// TestDatabaseManageGalera is de acceptatie van Beheer bij Galera
// (mijlpaal 11 van fase 5): een node opnieuw synchroniseren kan alleen als
// een andere node Synced is, en een herstart van een node wacht tot hij
// weer Synced is. Een herstart van de laatste node die meedoet, vraagt om
// bevestiging.
func TestDatabaseManageGalera(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g, ids := lab.e, lab.c, lab.g, lab.ids
	url := lab.base + "/" + lab.cid + "/manage"
	get := func() manageView {
		t.Helper()
		var v manageView
		if s := c.do("GET", url, nil, &v); s != http.StatusOK {
			t.Fatalf("beheer: %d", s)
		}
		return v
	}
	act := func(action, host, hash string) (int, apiErr, job) {
		t.Helper()
		var raw struct {
			apiErr
			job
		}
		s := c.do("POST", url, map[string]any{"action": action, "node_id": ids[host], "hash": hash}, &raw)
		return s, raw.apiErr, raw.job
	}

	v := get()
	if v.Engine != "mariadb" || v.Title != "MariaDB Galera" || v.can() != "nc1:resync nc2:resync nc3:resync" || len(v.Nodes) != 3 {
		t.Fatalf("beheer: %+v", v)
	}
	if _, eff := v.action("nc2", "resync"); !strings.Contains(eff, "MariaDB op nc2 stopt, gooit zijn positie weg en haalt alle data opnieuw bij nc1 en nc3") ||
		!strings.Contains(eff, "nc2 is nu Synced") {
		t.Errorf("gevolgen: %s", eff)
	}
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	viewer.login("kijker", "een-lang-wachtwoord", "")
	if s := viewer.do("GET", url, nil, nil); s != http.StatusForbidden {
		t.Fatalf("kijker: %d", s)
	}

	// nc2 en nc3 vallen weg: nc1 kan niet, want zonder hem stopt de
	// database; nc2 en nc3 halen alles bij nc1.
	g.leave("nc2", g.hosts["nc2"])
	g.leave("nc3", g.hosts["nc3"])
	stale := v.Hash
	v = get()
	if v.can() != "nc2:resync nc3:resync" {
		t.Fatalf("met alleen nc1: %s", v.can())
	}
	if why, _ := v.action("nc1", "resync"); !strings.Contains(why, "nc1 is de enige node die Synced is") {
		t.Errorf("nc1: %s", why)
	}
	if _, eff := v.action("nc2", "resync"); !strings.Contains(eff, "Tijdens de kopie is nc1 de enige node met de database") {
		t.Errorf("nc2: %s", eff)
	}
	// Een herstart van nc1 legt de database stil; dat vraagt bevestiging.
	var ae apiErr
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc1"]+"/actions", map[string]any{"action": "reboot"}, &ae); s != http.StatusConflict || ae.Code != "needs_force" ||
		ae.Message != "MariaDB van Nextcloud doet alleen op nc1 nog mee; de database stopt tot nc1 terug is" {
		t.Fatalf("herstart van de laatste node: %d %+v", s, ae)
	}

	// Met het oude overzicht gaat het niet; met het nieuwe wel.
	if s, ae, _ := act("resync", "nc2", stale); s != http.StatusConflict || ae.Code != "plan_stale" {
		t.Fatalf("oud overzicht: %d %+v", s, ae)
	}
	if s, ae, _ := act("resync", "nc1", v.Hash); s != http.StatusConflict || !strings.Contains(ae.Message, "enige node") {
		t.Fatalf("nc1: %d %+v", s, ae)
	}
	s, ae, j := act("resync", "nc2", v.Hash)
	if s != http.StatusAccepted || j.Title != "Opnieuw synchroniseren: nc2 in MariaDB Galera op Nextcloud" {
		t.Fatalf("opnieuw synchroniseren: %d %+v %+v", s, ae, j)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), []string{"nc2: opnieuw synchroniseren", "Controleren"}) {
		t.Fatalf("taak: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if log := stepLogOf(j, "nc2: opnieuw synchroniseren"); !strings.Contains(log, "nc2 gooit zijn positie weg en haalt alle data opnieuw bij nc1") ||
		!strings.Contains(log, "nc2: Synced, Primary, 2 leden") {
		t.Errorf("log:\n%s", log)
	}
	if !slices.Contains(g.hosts["nc2"].Calls(), "sh -c rm -f /var/lib/clusterforge/galera-resync-* && systemctl stop mariadb && rm -f /var/lib/mysql/grastate.dat && "+
		"touch /var/lib/clusterforge/galera-resync-"+j.ID+" && "+galera.ClearNewCluster+" && systemctl start --no-block mariadb") {
		t.Errorf("nc2: %v", g.hosts["nc2"].Calls())
	}
	if ev := clusterEvents(t, e, lab.cluster.ID, "cluster.database_resynced"); len(ev) != 1 || !strings.Contains(ev[0], `"hostname": "nc2"`) {
		t.Errorf("events: %v", ev)
	}

	// Een herstart van nc1 wacht tot nc1 weer Synced is.
	g.join("nc3", g.hosts["nc3"], false)
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc1"]+"/actions", map[string]any{"action": "reboot", "reason": "kernelupdate"}, &j); s != http.StatusAccepted {
		t.Fatalf("herstart: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	want := []string{"Node in onderhoud zetten", "Herstarten en wachten tot de node terug is", "MariaDB Galera: wachten tot nc1 weer meedoet",
		"Keepalived weer aanzetten", "Node weer actief maken"}
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), want) {
		t.Fatalf("herstart: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if log := stepLogOf(j, want[2]); !strings.Contains(log, "nc1: Synced, Primary, 3 leden") {
		t.Errorf("wachten:\n%s", log)
	}
}

// TestDatabaseManagePatroni is de acceptatie van Beheer bij PostgreSQL met
// Patroni (mijlpaal 11 van fase 5): alleen de synchrone replica kan primary
// worden, en het VIP gaat mee; een replica opnieuw opbouwen wacht tot hij
// opnieuw gestart is en bijhoudt. Onderhoud van de primary schakelt eerst
// over, en het einde van het onderhoud wacht tot de node weer bijhoudt.
func TestDatabaseManagePatroni(t *testing.T) {
	lab := newPatroniHosts(t)
	e, c, g, nc, ids := lab.e, lab.c, lab.g, lab.nc, lab.ids
	ctx := context.Background()
	cid := lab.deploy(t)
	url := "/api/v1/clusters/" + nc.ID + "/components/" + cid + "/manage"
	get := func() manageView {
		t.Helper()
		var v manageView
		if s := c.do("GET", url, nil, &v); s != http.StatusOK {
			t.Fatalf("beheer: %d", s)
		}
		return v
	}
	act := func(action, host, hash string) job {
		t.Helper()
		var j job
		if s := c.do("POST", url, map[string]any{"action": action, "node_id": ids[host], "hash": hash}, &j); s != http.StatusAccepted {
			t.Fatalf("%s op %s: %d", action, host, s)
		}
		return waitJobLong(t, c, j.ID)
	}
	eventually(t, "het VIP op nc1", func() bool { return slices.Contains(g.hosts["nc1"].Addresses(), "10.0.20.190") })

	// nc1 is de primary; alleen nc2, de synchrone replica, kan primary
	// worden. Opnieuw opbouwen kan op beide replica's.
	var v manageView
	eventually(t, "Beheer met het VIP op nc1", func() bool { v = get(); return v.Nodes[0].Vip })
	if v.Engine != "postgresql" || v.Vip != "10.0.20.190" || v.can() != "nc2:switchover nc2:reinit nc3:reinit" || len(v.Nodes[0].Actions) != 0 {
		t.Fatalf("beheer: %+v", v)
	}
	if _, role, _ := v.node("nc1"); role != "primary" {
		t.Errorf("nc1: %s", role)
	}
	if why, _ := v.action("nc3", "switchover"); !strings.Contains(why, "Patroni maakt alleen de synchrone replica primary, en dat is nu nc2") {
		t.Errorf("nc3: %s", why)
	}
	if _, eff := v.action("nc2", "switchover"); !strings.Contains(eff, "Patroni stopt de schrijvers op nc1, wacht tot nc2 alles heeft en maakt nc2 de primary") ||
		!strings.Contains(eff, "Het VIP 10.0.20.190 gaat mee naar nc2") {
		t.Errorf("gevolgen: %s", eff)
	}
	// Een agent met een ouder protocol kan Patroni niet beheren. De
	// heartbeat zet het protocol steeds terug.
	eventually(t, "nc3 met protocol 11", func() bool {
		if _, err := e.pool.Exec(ctx, "UPDATE agents SET protocol_version = 11 WHERE node_id = $1", ids["nc3"]); err != nil {
			t.Fatal(err)
		}
		why, _ := get().action("nc3", "reinit")
		return strings.Contains(why, "de agent op nc3 heeft protocol 11; Patroni beheren vraagt protocol 12")
	})
	eventually(t, "nc3 weer met protocol 12", func() bool { return strings.Contains(get().can(), "nc3:reinit") })

	// nc2 primary maken: nc1 wordt replica en het VIP gaat mee.
	j := act("switchover", "nc2", get().Hash)
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), []string{"nc2: primary maken", "nc1: replica", "Controleren"}) {
		t.Fatalf("switchover: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if log := stepLogOf(j, "nc2: primary maken"); !strings.Contains(log, "nc1 is de primary; Patroni schakelt over naar nc2") ||
		!strings.Contains(log, `Patroni: Successfully switched over to "nc2"`) {
		t.Errorf("log:\n%s", log)
	}
	if log := stepLogOf(j, "Controleren"); !strings.Contains(log, "het VIP 10.0.20.190 staat op nc2") {
		t.Errorf("controleren:\n%s", log)
	}
	if !slices.ContainsFunc(g.hosts["nc2"].Calls(), func(c string) bool {
		return c == `patroni POST /switchover {"candidate":"nc2","leader":"nc1"}`
	}) {
		t.Errorf("nc2: %v", g.hosts["nc2"].Calls())
	}
	v = get()
	if _, role, sync := v.node("nc1"); role != "replica" || !sync || v.can() != "nc1:switchover nc1:reinit nc3:reinit" {
		t.Fatalf("na de switchover: %s", v.can())
	}
	if ev := clusterEvents(t, e, nc.ID, "cluster.database_switchover"); len(ev) != 1 || !strings.Contains(ev[0], `"to": "nc2"`) {
		t.Errorf("events: %v", ev)
	}

	// nc3 opnieuw opbouwen: de taak wacht tot PostgreSQL daar opnieuw
	// gestart is en bijhoudt.
	j = act("reinit", "nc3", v.Hash)
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), []string{"nc3: opnieuw opbouwen", "Controleren"}) {
		t.Fatalf("reinit: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if log := stepLogOf(j, "nc3: opnieuw opbouwen"); !strings.Contains(log, "Patroni: reinitialize started") || !strings.Contains(log, "nc3: replica, running, streaming") {
		t.Errorf("log:\n%s", log)
	}

	// Onderhoud van nc2, de primary: eerst overschakelen naar nc1, daarna
	// gaat keepalived uit.
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc2"]+"/actions", map[string]any{"action": "maintenance"}, &j); s != http.StatusAccepted {
		t.Fatalf("onderhoud: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || len(j.Steps) < 2 || j.Steps[0].Name != "PostgreSQL met Patroni: overschakelen als nc2 de primary is" {
		t.Fatalf("onderhoud: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if log := stepLogOf(j, j.Steps[0].Name); !strings.Contains(log, "nc2 is de primary; Patroni schakelt over naar nc1") || !strings.Contains(log, "het VIP 10.0.20.190 staat op nc1") {
		t.Errorf("overschakelen:\n%s", log)
	}
	if ev := clusterEvents(t, e, nc.ID, "cluster.database_switchover"); len(ev) != 2 || !strings.Contains(ev[1], `"maintenance": true`) {
		t.Errorf("events: %v", ev)
	}
	// Het einde van het onderhoud wacht tot nc2 weer bijhoudt.
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc2"]+"/actions", map[string]any{"action": "activate"}, &j); s != http.StatusAccepted {
		t.Fatalf("onderhoud beëindigen: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || j.Steps[0].Name != "PostgreSQL met Patroni: wachten tot nc2 weer meedoet" || !strings.Contains(stepLogOf(j, j.Steps[0].Name), "nc2: replica, running, streaming") {
		t.Fatalf("onderhoud beëindigen: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}

	// Zonder synchrone replica kan ClusterForge niet overschakelen; onderhoud
	// van de primary vraagt dan bevestiging.
	for _, h := range g.hosts {
		h.Locked(func() { h.Patroni.Sync = false })
	}
	var ae apiErr
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc1"]+"/actions", map[string]any{"action": "maintenance"}, &ae); s != http.StatusConflict || ae.Code != "needs_force" ||
		!strings.Contains(ae.Message, "ClusterForge kan niet overschakelen van nc1, de primary van PostgreSQL: er is geen synchrone replica") ||
		!strings.Contains(ae.Message, "heeft het VIP 10.0.20.190 geen node zolang nc1 in onderhoud is") {
		t.Fatalf("onderhoud zonder synchrone replica: %d %+v", s, ae)
	}
	// Een herstart van een replica wacht tot hij weer bijhoudt.
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc3"]+"/actions", map[string]any{"action": "reboot"}, &j); s != http.StatusAccepted {
		t.Fatalf("herstart van nc3: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || !slices.Contains(stepNamesOf(j), "PostgreSQL met Patroni: wachten tot nc3 weer meedoet") {
		t.Fatalf("herstart van nc3: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if log := stepLogOf(j, "PostgreSQL met Patroni: overschakelen als nc3 de primary is"); !strings.Contains(log, "nc3 is niet de primary van PostgreSQL") {
		t.Errorf("overschakelen bij nc3:\n%s", log)
	}

	// Met MariaDB en PostgreSQL op dezelfde nodes wacht het einde van het
	// onderhoud op allebei, MariaDB eerst. Op deze nodes draait MariaDB
	// niet, dus de taak stopt daar.
	if _, err := e.pool.Exec(ctx, `INSERT INTO cluster_components (cluster_id, engine, spec, applied_revision, auto_recover)
		SELECT cluster_id, 'mariadb', jsonb_set(spec, '{template}', '{"name": "mariadb-galera", "version": "1.0.0"}'), 1, false
		FROM cluster_components WHERE id = $1`, cid); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc3"]+"/actions", map[string]any{"action": "maintenance"}, &j); s != http.StatusAccepted {
		t.Fatalf("onderhoud van nc3: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || j.Steps[0].Name != "PostgreSQL met Patroni: overschakelen als nc3 de primary is" {
		t.Fatalf("onderhoud van nc3: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc3"]+"/actions", map[string]any{"action": "activate"}, &j); s != http.StatusAccepted {
		t.Fatalf("onderhoud van nc3 beëindigen: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "failed" || !slices.Equal(stepNamesOf(j), []string{"MariaDB Galera: wachten tot nc3 weer meedoet"}) || !strings.Contains(j.Error, "nc3") {
		t.Fatalf("onderhoud van nc3 beëindigen: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
}
