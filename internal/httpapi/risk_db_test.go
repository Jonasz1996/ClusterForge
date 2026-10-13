package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
)

// TestDatabaseScoreCard is de acceptatie van de kaart Database (mijlpaal 15
// van fase 5): MariaDB Galera op nc1 tot nc3 zonder dumps, dan een dump die
// de tabellen zonder primaire sleutel en buiten InnoDB vastlegt, en een VIP
// zonder en met geslaagde failovertest.
func TestDatabaseScoreCard(t *testing.T) {
	lab := newGaleraLab(t)
	e, c := lab.e, lab.c
	ctx := context.Background()
	for _, h := range lab.g.hosts {
		h.Locked(func() {
			h.MariaDB.Databases = []agenttest.Database{
				{Name: "nextcloud", Size: 2 << 30, Tables: 3, NoPK: []string{"t2"}, Engines: map[string]string{"t3": "Aria"}},
				{Name: "wiki", Size: 40 << 20, Tables: 2},
			}
		})
	}
	if err := e.dbs.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	e.risk.Warmup = 0
	read := func() riskCard {
		t.Helper()
		if err := e.risk.Compute(ctx); err != nil {
			t.Fatal(err)
		}
		var v clusterRiskView
		if s := c.do("GET", "/api/v1/clusters/"+lab.cluster.ID+"/risk", nil, &v); s != http.StatusOK {
			t.Fatalf("risico: %d", s)
		}
		return v.card(t, "database")
	}
	summary := func(rc riskCard) string {
		var out []string
		for _, f := range rc.Findings {
			out = append(out, f.Check+" "+f.Severity)
		}
		slices.Sort(out)
		return strings.Join(out, ", ")
	}

	// Nog geen dump van nextcloud en wiki; drie nodes, dus HA. Op lab is het
	// laag.
	rc := read()
	if rc.Weight != 15 || summary(rc) != "database.dump_stale low" ||
		rc.Findings[0].Text != "Van 2 databases van MariaDB Galera is de nieuwste goede dump ouder dan een dag: nextcloud (nog geen) en wiki (nog geen)" ||
		rc.Findings[0].Link != "/back-ups" {
		t.Fatalf("zonder dumps: %+v", rc)
	}

	// Een dump van nextcloud legt de tabellen vast; wiki heeft er nog geen.
	dumps := lab.base + "/" + lab.cid + "/dumps"
	var j job
	if s := c.do("POST", dumps, map[string]any{"database": "nextcloud"}, &j); s != http.StatusAccepted {
		t.Fatalf("dump: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("dump: %s %s", j.Status, j.Error)
	}
	var noPK, other []string
	if err := e.pool.QueryRow(ctx, "SELECT no_primary_key, not_innodb FROM db_dumps WHERE component_id = $1 AND database = 'nextcloud'",
		lab.cid).Scan(&noPK, &other); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(noPK, []string{"t2"}) || !slices.Equal(other, []string{"t3"}) {
		t.Fatalf("tabellen: %v %v", noPK, other)
	}
	rc = read()
	if summary(rc) != "database.dump_stale low, database.no_primary_key low, database.not_innodb low" {
		t.Fatalf("na de dump: %s", summary(rc))
	}
	for _, f := range rc.Findings {
		switch f.Check {
		case "database.dump_stale":
			if !strings.HasSuffix(f.Text, "dag: wiki (nog geen)") {
				t.Errorf("dump: %s", f.Text)
			}
		case "database.no_primary_key":
			if !strings.HasPrefix(f.Text, "1 tabel zonder primaire sleutel: nextcloud.t2.") || !strings.HasSuffix(f.Text, "occ db:add-missing-primary-keys") ||
				f.Subject.Type != "component" || f.Subject.ID != lab.cid {
				t.Errorf("primaire sleutel: %+v", f)
			}
			// Bewust laten staan kan, zoals elke bevinding.
			accept := map[string]any{"check": f.Check, "subject_type": f.Subject.Type, "subject_id": f.Subject.ID, "reason": "Nextcloud voegt ze bij de volgende update toe"}
			if s := c.do("POST", "/api/v1/risk/acceptances", accept, nil); s != http.StatusNoContent {
				t.Fatalf("accepteren: %d", s)
			}
		case "database.not_innodb":
			if !strings.HasPrefix(f.Text, "1 tabel staat niet in InnoDB: nextcloud.t3.") {
				t.Errorf("InnoDB: %s", f.Text)
			}
		}
	}

	rc = read()
	if i := slices.IndexFunc(rc.Findings, func(f riskFinding) bool { return f.Check == "database.no_primary_key" }); i < 0 ||
		rc.Findings[i].Accepted == nil || rc.Findings[i].Points != 0 {
		t.Fatalf("geaccepteerd: %+v", rc.Findings)
	}

	// Met het VIP .185 hoort er een failovertest bij.
	if _, err := e.pool.Exec(ctx, "UPDATE cluster_components SET spec = jsonb_set(spec, '{vip}', jsonb_build_object('id', gen_random_uuid()::text, 'address', '10.0.20.185')) WHERE id = $1",
		lab.cid); err != nil {
		t.Fatal(err)
	}
	rc = read()
	i := slices.IndexFunc(rc.Findings, func(f riskFinding) bool { return f.Check == "database.no_test" })
	if i < 0 || rc.Findings[i].Severity != "low" || rc.Findings[i].Link != "/tests" ||
		rc.Findings[i].Text != "MariaDB Galera had nog geen geslaagde failovertest: of de app na een uitval weer kan schrijven via 10.0.20.185, is niet bewezen" {
		t.Fatalf("zonder failovertest: %+v", rc.Findings)
	}
	// Een geslaagde test met de schrijftest van een ander onderdeel telt niet,
	// die van MariaDB Galera wel.
	insert := func(title string, ago time.Duration) {
		t.Helper()
		at := time.Now().Add(-ago)
		if _, err := e.pool.Exec(ctx, `INSERT INTO test_runs (kind, cluster_id, definition, result, restored, created_at, finished_at, measurements)
			VALUES ('failover.test', $1, '{}', 'pass', true, $2, $2, jsonb_build_object('writes', jsonb_build_object('title', $3::text)))`,
			lab.cluster.ID, at, title); err != nil {
			t.Fatal(err)
		}
	}
	insert("PostgreSQL met Patroni", time.Hour)
	if summary(read()) != "database.dump_stale low, database.no_primary_key low, database.no_test low, database.not_innodb low" {
		t.Fatal("test van een ander onderdeel telt")
	}
	insert("MariaDB Galera", 100*24*time.Hour)
	rc = read()
	if i := slices.IndexFunc(rc.Findings, func(f riskFinding) bool { return f.Check == "database.no_test" }); i < 0 ||
		!strings.HasPrefix(rc.Findings[i].Text, "De laatste geslaagde failovertest van MariaDB Galera is 100 dagen geleden") {
		t.Fatalf("oude failovertest: %+v", rc.Findings)
	}
	insert("MariaDB Galera", 2*24*time.Hour)
	if summary(read()) != "database.dump_stale low, database.no_primary_key low, database.not_innodb low" {
		t.Fatal("recente failovertest telt niet")
	}
}
