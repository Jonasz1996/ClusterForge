package risk

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
)

func cardOf(r Result, id CardID) *Card {
	for i := range r.Cards {
		if r.Cards[i].ID == id {
			return &r.Cards[i]
		}
	}
	return nil
}

// dbFindings zet de bevindingen als "check severity: tekst", gesorteerd
// zoals de kaart ze toont.
func dbFindings(c *Card) []string {
	var out []string
	for _, f := range c.Findings {
		out = append(out, f.Check+" "+string(f.Severity)+": "+f.Text)
	}
	return out
}

// TestDatabaseCard: Nextcloud op prod met MariaDB Galera op nc1 tot nc3,
// waarvan twee op pve1, een node met weinig geheugen, oude dumps en een
// failovertest van 100 dagen geleden.
func TestDatabaseCard(t *testing.T) {
	in := healthy()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	mem := func(v float64) *NodeMetrics { return &NodeMetrics{MemoryP95: &v} }
	in.Nodes = []NodeInput{
		{ID: ids[0], Hostname: "nc1", Host: "pve1", Lifecycle: "active", Metrics: mem(60)},
		{ID: ids[1], Hostname: "nc2", Host: "pve1", Lifecycle: "active", Metrics: mem(88)},
		{ID: ids[2], Hostname: "nc3", Host: "pve2", Lifecycle: "active"},
	}
	comp := DBComponentInput{
		ID: uuid.New(), Title: "MariaDB Galera", Engine: "mariadb", Nodes: ids, VIP: "10.0.20.185", BackupOn: true,
		Health: &status.DBHealth{Status: status.Degraded, Reason: "nc3 doet niet mee"},
		Dumps: []DBDumpInput{
			{Database: "nextcloud", Latest: ago(3 * day), Stale: true, NoPrimaryKey: []string{"oc_collres_accesscache"}},
			{Database: "gitea", Stale: true},
			{Database: "wiki", Latest: ago(time.Hour), NoPrimaryKey: []string{}, NotInnoDB: []string{"logs"}},
		},
		LastTest: ago(100 * day),
	}
	moved := uuid.New()
	in.DB = &DatabaseCardInput{
		Components: []DBComponentInput{comp},
		Shared:     []SharedDBInput{{Engine: "mariadb", Database: "wiki", Users: []string{"Wiki", "Rapport"}}},
		Moved: []MovedDBInput{
			{ID: moved, Engine: "mariadb", Database: "oud", SourceKey: "mariadb/x", To: "Wiki", SwitchedAt: now.Add(-40 * day)},
			{ID: uuid.New(), Engine: "mariadb", Database: "recent", SourceKey: "mariadb/x", To: "Wiki", SwitchedAt: now.Add(-10 * day)},
		},
	}
	res := Score(in, nil, now)
	c := cardOf(res, DatabaseCard)
	if c == nil || c.Weight != 15 || c.Score == nil {
		t.Fatalf("kaart: %+v", res.Cards)
	}
	want := []string{
		"database.not_innodb high: 1 tabel staat niet in InnoDB: wiki.logs. Galera zet alleen InnoDB op de andere nodes, dus de rest staat alleen op de node waar hij geschreven werd. Zet ze om met ALTER TABLE … ENGINE=InnoDB",
		"database.no_test high: De laatste geslaagde failovertest van MariaDB Galera is 100 dagen geleden; test minstens elke 90 dagen of schrijven via 10.0.20.185 na een uitval nog lukt",
		"database.degraded high: MariaDB Galera: nc3 doet niet mee",
		"database.dump_stale high: Van 2 databases van MariaDB Galera is de nieuwste goede dump ouder dan een dag: nextcloud (3 dagen) en gitea (nog geen)",
		"database.same_host high: nc1 en nc2 van MariaDB Galera draaien op pve1: valt pve1 uit, dan is er geen meerderheid meer en stopt de database. Zet ze op verschillende hosts",
		"database.memory medium: Op nc2 was de afgelopen 7 dagen vaak minder dan 15 % geheugen vrij (p95 88 %). De database deelt het met de app; raakt het op, dan stopt de kernel meestal de database",
		"database.no_primary_key low: 1 tabel zonder primaire sleutel: nextcloud.oc_collres_accesscache. Een UPDATE of DELETE erop is traag op de andere nodes van Galera, want die zoeken de rij in de hele tabel. Nextcloud voegt ze zelf toe met occ db:add-missing-primary-keys",
		"database.not_retired low: oud is 40 dagen geleden naar Wiki verhuisd en staat nog op slot op " + in.Name + ": bouw de oude database af",
		"database.shared low: wiki wordt gebruikt door Wiki en Rapport: een fout of zware query van de ene raakt de andere, en verhuizen kan alleen voor allemaal tegelijk",
	}
	got := dbFindings(c)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bevindingen:\n%s\nwil:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if *c.Score != 0 {
		t.Errorf("score %d", *c.Score)
	}
	for _, f := range c.Findings {
		if f.Check == "database.not_retired" && (f.Subject.ID != moved || f.Link != "/databases/verhuizen?db=oud&source=mariadb%2Fx") {
			t.Errorf("verhuizing: %+v", f)
		}
	}

	// Een failovertest van vorige week, geen bevinding; zonder test wel.
	in.DB.Components[0].LastTest = ago(7 * day)
	if hasCheck(Score(in, nil, now), "database.no_test") {
		t.Error("recente failovertest")
	}
	in.DB.Components[0].LastTest = nil
	if f := findCheck(Score(in, nil, now), "database.no_test"); f == nil ||
		f.Text != "MariaDB Galera had nog geen geslaagde failovertest: of de app na een uitval weer kan schrijven via 10.0.20.185, is niet bewezen" {
		t.Errorf("geen failovertest: %+v", f)
	}
}

// TestDatabaseCardSmall: twee nodes zonder scheidsrechter op lab, dumps
// uit en geen VIP; en een cluster met alleen een databaseserver.
func TestDatabaseCardSmall(t *testing.T) {
	in := healthy()
	in.Env = "lab"
	a, b := in.Nodes[0], in.Nodes[1]
	in.DB = &DatabaseCardInput{Components: []DBComponentInput{{ID: uuid.New(), Title: "PostgreSQL met Patroni", Engine: "postgresql", Nodes: []uuid.UUID{a.ID, b.ID}}}}
	c := cardOf(Score(in, nil, now), DatabaseCard)
	want := []string{
		"database.dumps_off low: De nachtelijke dumps van PostgreSQL met Patroni staan uit: zonder dump is er niets om één database terug te zetten",
		"database.no_ha low: PostgreSQL met Patroni heeft twee nodes zonder scheidsrechter: valt " + a.Hostname + " of " + b.Hostname +
			" uit, dan heeft de andere geen meerderheid en stopt de database. Voeg een derde node of een scheidsrechter toe",
	}
	if got := dbFindings(c); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bevindingen:\n%s", strings.Join(got, "\n"))
	}
	// Met een scheidsrechter is het HA.
	in.DB.Components[0].Arbiters = []uuid.UUID{uuid.New()}
	if hasCheck(Score(in, nil, now), "database.no_ha") {
		t.Error("met scheidsrechter")
	}

	// Alleen een databaseserver, zonder onderdeel: de kaart zegt het.
	in.DB = &DatabaseCardInput{}
	c = cardOf(Score(in, nil, now), DatabaseCard)
	if c == nil || c.Score == nil || *c.Score != 100 || !strings.Contains(c.Note, "beheert de databases van dit cluster niet") {
		t.Fatalf("zonder onderdeel: %+v", c)
	}
	// Zonder database geen kaart.
	in.DB = nil
	if cardOf(Score(in, nil, now), DatabaseCard) != nil {
		t.Fatal("kaart zonder database")
	}
}

func findCheck(r Result, check string) *Finding {
	for _, c := range r.Cards {
		for i := range c.Findings {
			if c.Findings[i].Check == check {
				return &c.Findings[i]
			}
		}
	}
	return nil
}

func hasCheck(r Result, check string) bool { return findCheck(r, check) != nil }
