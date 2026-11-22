package risk

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/topo"
)

// ncLab is het Nextcloud-cluster nc-prod: nc1 op pve1, nc2 op pve2 en nc3
// op pve3, met het VIP 10.0.20.185, MariaDB Galera op alle drie en
// Nextcloud dat er hard van afhangt.
func ncLab() (*lab, Database) {
	l := newLab()
	l.cluster("nc-prod", "prod", deps.VIP{Address: "10.0.20.185", Owner: l.id("nc1")})
	for i, h := range []string{"pve1", "pve2", "pve3"} {
		name := fmt.Sprintf("nc%d", i+1)
		l.guest(501+i, name, "nc-prod", h, "local-lvm", "", 2*gib, "vmbr0")
		l.units(name, "mariadb", "nginx", "keepalived")
	}
	l.svc("nc-prod", "mariadb", "database", "mariadb")
	l.svc("nc-prod", "nextcloud", "app", "nginx")
	l.w.Deps = append(l.w.Deps, deps.Dep{
		ID: uuid.New(), From: l.id("nc-prod/nextcloud"), To: l.id("nc-prod/mariadb"), Strength: deps.Hard, Source: deps.SourceManual, State: deps.StateConfirmed,
	})
	db := Database{ClusterID: l.id("nc-prod"), Title: "MariaDB Galera", Unit: "mariadb", Nodes: []uuid.UUID{l.id("nc1"), l.id("nc2"), l.id("nc3")}}
	return l, db
}

// dbWorld is de wereld met de databases erbij.
func (l *lab) dbWorld(dbs ...Database) WhatIfWorld {
	w := l.world()
	w.Candidates[l.id("nc-prod")] = map[string][]uuid.UUID{"10.0.20.185": {l.id("nc1"), l.id("nc2"), l.id("nc3")}}
	w.Databases = map[uuid.UUID][]Database{}
	for _, d := range dbs {
		w.Databases[d.ClusterID] = append(w.Databases[d.ClusterID], d)
	}
	return w
}

// ncCluster is de uitkomst van nc-prod.
func ncCluster(t *testing.T, r WhatIf) ClusterOutcome {
	t.Helper()
	for _, c := range r.Clusters {
		if c.Name == "nc-prod" {
			return c
		}
	}
	t.Fatalf("nc-prod niet geraakt:\n%s", clusters(r))
	return ClusterOutcome{}
}

func runWorld(t *testing.T, w WhatIfWorld, keys ...string) WhatIf {
	t.Helper()
	comps, err := w.Resolve(keys)
	if err != nil {
		t.Fatal(err)
	}
	return w.Run(comps, false)
}

// ncServices zijn de diensten van nc-prod in de uitkomst.
func ncServices(r WhatIf) string {
	var out []string
	for _, line := range strings.Split(services(r), "\n") {
		if strings.HasPrefix(line, "nc-prod/") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// TestWhatIfGaleraMajority is de acceptatie van de wat-als in mijlpaal 13
// van fase 5: 'Vallen nc2 en nc3 weg, dan stopt MariaDB.'
func TestWhatIfGaleraMajority(t *testing.T) {
	l, db := ncLab()
	w := l.dbWorld(db)
	node := func(name string) string { return topo.NodeKey(l.id(name)) }

	// Valt nc3 weg, dan gaat Galera met nc1 en nc2 verder.
	r := runWorld(t, w, node("nc3"))
	if got, want := clusters(r), "nc-prod healthy→degraded: nc3: valt uit: gekozen; MariaDB Galera: nc3 valt uit; nog 2 van de 3 leden, genoeg voor een meerderheid"; got != want {
		t.Fatalf("nc3:\n%s\nwil\n%s", got, want)
	}
	if got := ncServices(r); got != "nc-prod/mariadb degraded: draait nog op nc1 en nc2\nnc-prod/nextcloud degraded: draait nog op nc1 en nc2" {
		t.Fatalf("nc3, diensten:\n%s", got)
	}

	// Vallen nc2 en nc3 weg, dan stopt MariaDB, ook al draait het op nc1
	// nog; Nextcloud gaat mee.
	r = runWorld(t, w, node("nc2"), node("nc3"))
	want := "nc-prod healthy→down: MariaDB Galera: zonder nc2 en nc3 is er geen meerderheid (1 van de 3 leden), dus MariaDB stopt"
	if got := clusters(r); got != want {
		t.Fatalf("nc2 en nc3:\n%s\nwil\n%s", got, want)
	}
	want = "nc-prod/mariadb down: zonder nc2 en nc3 is er geen meerderheid (1 van de 3 leden)"
	if got := ncServices(r); !strings.HasPrefix(got, want) || !strings.Contains(got, "nc-prod/nextcloud down: hangt hard af van mariadb") {
		t.Fatalf("nc2 en nc3, diensten:\n%s", got)
	}
	// Hetzelfde als hun hosts uitvallen.
	r = runWorld(t, w, l.host("pve2"), l.host("pve3"))
	if got := clusters(r); !strings.Contains(got, "nc-prod healthy→down: MariaDB Galera: zonder nc2 en nc3 is er geen meerderheid") {
		t.Fatalf("pve2 en pve3:\n%s", got)
	}
	// Alle drie weg: dan zegt de status van de nodes het al.
	r = runWorld(t, w, node("nc1"), node("nc2"), node("nc3"))
	if got := clusters(r); got != "nc-prod healthy→down: alle nodes zijn down" {
		t.Fatalf("alle drie:\n%s", got)
	}

	// Zonder de database ziet de wat-als alleen nodes: MariaDB draait nog
	// op nc1.
	plain := l.dbWorld()
	r = runWorld(t, plain, node("nc2"), node("nc3"))
	if got := clusters(r); !strings.HasPrefix(got, "nc-prod healthy→degraded") {
		t.Fatalf("zonder database:\n%s", got)
	}

	// SPOF's: over drie hosts geen. Staan nc1 en nc2 allebei op pve1, dan
	// stopt MariaDB als pve1 uitvalt.
	for _, sp := range w.SPOFs() {
		for _, c := range sp.Clusters {
			if c.Name == "nc-prod" {
				t.Fatalf("SPOF over drie hosts: %+v", sp)
			}
		}
	}
	for i := range l.m.Guests {
		if l.m.Guests[i].Name == "nc2" {
			l.m.Guests[i].Host = "pve1"
		}
	}
	found := false
	for _, sp := range l.dbWorld(db).SPOFs() {
		if sp.Component.Kind != KindHost || sp.Component.Name != "pve1" {
			continue
		}
		for _, c := range sp.Clusters {
			if c.Name == "nc-prod" && c.Reason == "MariaDB Galera: zonder nc1 en nc2 is er geen meerderheid (1 van de 3 leden), dus MariaDB stopt" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("pve1 als SPOF: %+v", l.dbWorld(db).SPOFs())
	}
}

// TestWhatIfGaleraArbiter: twee datanodes en een scheidsrechter.
func TestWhatIfGaleraArbiter(t *testing.T) {
	l, db := ncLab()
	db.Nodes, db.Arbiters = db.Nodes[:2], []uuid.UUID{l.id("nc3")}
	w := l.dbWorld(db)
	node := func(name string) string { return topo.NodeKey(l.id(name)) }

	r := runWorld(t, w, node("nc3"))
	if got := r.Clusters[0].During.Reason; got != "nc3: valt uit: gekozen; MariaDB Galera: de scheidsrechter nc3 valt uit; nog 2 van de 3 leden, genoeg voor een meerderheid" {
		t.Fatalf("scheidsrechter: %s", got)
	}
	r = runWorld(t, w, node("nc1"))
	if got := r.Clusters[0].During; got.Status != "degraded" || !strings.Contains(got.Reason, "MariaDB Galera: nc1 valt uit; nog 2 van de 3 leden") {
		t.Fatalf("nc1: %+v", got)
	}
	r = runWorld(t, w, node("nc1"), node("nc3"))
	if got := r.Clusters[0].During.Reason; got != "MariaDB Galera: zonder nc1 en nc3 is er geen meerderheid (1 van de 3 leden), dus MariaDB stopt" {
		t.Fatalf("nc1 en de scheidsrechter: %s", got)
	}
	r = runWorld(t, w, node("nc1"), node("nc2"))
	if got := r.Clusters[0].During.Reason; got != "MariaDB Galera: MariaDB draait op geen enkele datanode meer" {
		t.Fatalf("beide datanodes: %s", got)
	}

	// Twee datanodes zonder scheidsrechter: elke node is een SPOF.
	db.Arbiters = nil
	w = l.dbWorld(db)
	r = runWorld(t, w, node("nc2"))
	if got := r.Clusters[0].During.Reason; got != "MariaDB Galera: zonder nc2 is er geen meerderheid (1 van de 2 leden), dus MariaDB stopt" {
		t.Fatalf("zonder scheidsrechter: %s", got)
	}
}

// TestWhatIfGaleraNow: de wat-als rekent met wat de bewaking nu ziet.
func TestWhatIfGaleraNow(t *testing.T) {
	l, db := ncLab()
	node := func(name string) string { return topo.NodeKey(l.id(name)) }
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

	// MariaDB is op nc3 gestopt, de node draait. Galera gaat dan met twee
	// leden verder, en zonder nc2 stopt het.
	db.Health = &status.DBHealth{Status: status.Degraded, Reason: "MariaDB Galera: nc3 doet niet mee (MariaDB draait niet)", At: at}
	db.Members = []uuid.UUID{l.id("nc1"), l.id("nc2")}
	w := l.dbWorld(db)
	r := runWorld(t, w, node("nc2"))
	c := r.Clusters[0]
	if c.Before.Status != "degraded" || c.Before.Reason != "MariaDB Galera: nc3 doet niet mee (MariaDB draait niet)" {
		t.Fatalf("nu: %+v", c.Before)
	}
	if c.During.Reason != "MariaDB Galera: zonder nc2 is er geen meerderheid (1 van de 2 leden; nc3 doet nu niet mee), dus MariaDB stopt" {
		t.Fatalf("nc2 met nc3 al uit: %+v", c.During)
	}
	// De SPOF-lijst rekent alsof alles gezond is.
	for _, sp := range w.SPOFs() {
		for _, c := range sp.Clusters {
			if c.Name == "nc-prod" {
				t.Fatalf("SPOF: %+v", sp)
			}
		}
	}

	// Is de database nu down, dan blijft ze dat.
	db.Health = &status.DBHealth{Status: status.Down, Reason: "MariaDB Galera: geen enkele node zit in een Primary component; nc1: …", At: at}
	db.Members = []uuid.UUID{}
	r = runWorld(t, l.dbWorld(db), node("nc3"))
	if c := r.Clusters[0]; c.Before.Status != "down" || c.During.Reason != db.Health.Reason {
		t.Fatalf("al down: %+v", c)
	}

	// Gezond volgens de bewaking: dan telt alleen de uitval.
	db.Health = &status.DBHealth{Status: status.Healthy, At: at}
	db.Members = []uuid.UUID{l.id("nc1"), l.id("nc2"), l.id("nc3")}
	r = runWorld(t, l.dbWorld(db), node("nc2"), node("nc3"))
	if c := r.Clusters[0]; c.Before.Status != "healthy" || !strings.HasPrefix(c.During.Reason, "MariaDB Galera: zonder nc2 en nc3 is er geen meerderheid (1 van de 3 leden)") {
		t.Fatalf("gezond: %+v", c)
	}
}

// TestWhatIfPatroniMajority: etcd telt al zijn leden, ook die er nu niet
// zijn.
func TestWhatIfPatroniMajority(t *testing.T) {
	l, db := ncLab()
	l.svc("nc-prod", "postgresql", "database", "patroni")
	db.Title, db.Unit, db.Patroni = "PostgreSQL met Patroni", "patroni", true
	node := func(name string) string { return topo.NodeKey(l.id(name)) }
	for i := range l.w.Nodes {
		if l.w.Nodes[i].Hostname == "nc3" {
			l.w.Nodes[i].Status, l.w.Nodes[i].Reason, l.w.Nodes[i].Fresh = status.Down, "geen heartbeat meer", false
		}
	}
	w := l.dbWorld(db)
	r := runWorld(t, w, node("nc2"))
	c := r.Clusters[0]
	if c.Before.Status != "degraded" || !strings.HasSuffix(c.Before.Reason, "PostgreSQL met Patroni: nc3 doet niet mee; nog 2 van de 3 leden, genoeg voor een meerderheid") {
		t.Fatalf("nu: %+v", c.Before)
	}
	want := "PostgreSQL met Patroni: zonder nc2 en nc3 heeft etcd geen meerderheid (1 van de 3 leden), dus PostgreSQL stopt met schrijven"
	if c.During.Status != "down" || c.During.Reason != want {
		t.Fatalf("nc2:\n%+v\nwil %s", c.During, want)
	}
	if got := ncServices(r); !strings.Contains(got, "nc-prod/postgresql down: zonder nc2 en nc3 heeft etcd geen meerderheid (1 van de 3 leden)") {
		t.Fatalf("diensten:\n%s", got)
	}
	// In de SPOF-lijst draait nc3 weer: dan is één node geen SPOF.
	for _, sp := range w.SPOFs() {
		for _, c := range sp.Clusters {
			if c.Name == "nc-prod" {
				t.Fatalf("SPOF: %+v", sp)
			}
		}
	}
}

// TestWhatIfDatabaseHA: herstart HA de nodes elders, dan heeft de database
// daarna weer een meerderheid.
func TestWhatIfDatabaseHA(t *testing.T) {
	l, db := ncLab()
	for i := range l.m.Guests {
		if g := &l.m.Guests[i]; g.Name == "nc2" || g.Name == "nc3" {
			g.Host, g.HAState, g.Config.Disks[0].Storage = "pve2", "started", "ceph"
		}
	}
	r := runWorld(t, l.dbWorld(db), l.host("pve2"))
	c := ncCluster(t, r)
	if c.During.Status != "down" || c.AfterHA == nil || c.AfterHA.Status != "healthy" {
		t.Fatalf("pve2: %+v %+v", c.During, c.AfterHA)
	}
	if got := ncServices(r); !strings.Contains(got, "nc-prod/mariadb down: zonder nc2 en nc3 is er geen meerderheid (1 van de 3 leden) (komt terug)") {
		t.Fatalf("diensten:\n%s", got)
	}
}
