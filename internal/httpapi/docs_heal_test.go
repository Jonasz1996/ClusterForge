package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/docs"
)

// TestHealDocs is de acceptatie van de sectie Zelfherstel in het hoofdstuk
// Herstel (mijlpaal 5 van fase 7): na Nu herstellen op Jonas' storing staat
// het herstel in de documentatie, met wat er mis was, wie het vroeg, wat
// Zelfherstel deed en de uitkomst. Opnieuw schrijven verandert niets.
func TestHealDocs(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	d := newHealDB(t, e, c)
	r := d.heal(t, e, c, func(r healView) bool { return strings.Contains(r.codes(), "db1+db2+db3") })
	if r.Plan == nil || r.Plan.Blocked != "" {
		t.Fatalf("plan: %+v", r.Plan)
	}
	var j job
	if s := c.do("POST", "/api/v1/heal/run", map[string]any{"key": r.Key, "hash": r.Plan.Hash}, &j); s != http.StatusAccepted {
		t.Fatalf("herstellen: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("taak: %s %s", j.Status, j.Error)
	}

	id := uuid.MustParse(d.cluster.ID)
	if _, err := e.docs.GenerateCluster(ctx, id, docs.CauseManual); err != nil {
		t.Fatal(err)
	}
	get := func(kind string) testDoc {
		t.Helper()
		var doc testDoc
		if s := c.do("GET", "/api/v1/docs/cluster/"+kind+"?cluster="+d.cluster.ID, nil, &doc); s != http.StatusOK || doc.Version == nil {
			t.Fatalf("%s: %d %+v", kind, s, doc.testDocMeta)
		}
		return doc
	}
	rec := get("herstel")
	md := rec.Content
	for _, w := range []string{
		"## Zelfherstel",
		"Vanzelf herstellen staat uit op dit cluster",
		"| Wanneer | Database | Wat er mis was | Wat Zelfherstel deed | Uitkomst |",
		" | MariaDB van database cluster | MariaDB op db1 hangt; ",
		"; Galera zoekt de andere nodes op verkeerde adressen; Geen node zit in een Primary component | ",
		" | met de hand door admin: Op db1, db2 en db3 de Galera-adressen bijwerken",
		" | gelukt |",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("herstel mist %q", w)
		}
	}
	// Elk probleem één keer, ook al geldt het voor drie nodes.
	if n := strings.Count(md, "Galera zoekt de andere nodes op verkeerde adressen"); n != 1 {
		t.Errorf("verkeerde adressen staat er %d keer", n)
	}
	if strings.Index(md, "## Zelfherstel") > strings.Index(md, "## Wat er uitvalt") {
		t.Error("Zelfherstel staat na Wat er uitvalt")
	}
	if t.Failed() {
		t.Fatalf("hoofdstuk:\n%s", md)
	}
	if db := get("database").Content; !strings.Contains(db, "[Herstel](herstel.md#zelfherstel)") {
		t.Errorf("database linkt niet naar Zelfherstel:\n%s", db)
	}

	// Nog eens schrijven geeft geen nieuwe versie van Herstel.
	if _, err := e.docs.GenerateCluster(ctx, id, docs.CauseManual); err != nil {
		t.Fatal(err)
	}
	if again := get("herstel"); *again.Version != *rec.Version {
		t.Errorf("nieuwe versie %d na %d:\n%s", *again.Version, *rec.Version, again.Content)
	}
}
