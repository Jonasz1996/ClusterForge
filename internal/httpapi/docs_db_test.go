package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/docs"
)

// TestDatabaseDocs is de acceptatie van het hoofdstuk Database (mijlpaal
// 15 van fase 5): MariaDB Galera op nc1 tot nc3 met twee databases en een
// app die verbindt. Het hoofdstuk staat na architectuur, beschrijft wat
// ClusterForge las en verandert niet zolang er niets verandert.
func TestDatabaseDocs(t *testing.T) {
	lab := newGaleraLab(t)
	e, c := lab.e, lab.c
	ctx := context.Background()
	for _, h := range lab.g.hosts {
		h.Locked(func() {
			h.MariaDB.Databases = []agenttest.Database{{Name: "nextcloud", Size: 2468 << 20, Tables: 121}, {Name: "wiki", Size: 40 << 20, Tables: 12}}
		})
	}
	nc1 := lab.g.hosts["nc1"]
	nc1.Locked(func() {
		nc1.MariaDB.Clients = []agenttest.Client{{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 4}}
	})
	if err := e.dbs.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}

	var idx testDocIndex
	c.do("GET", "/api/v1/docs", nil, &idx)
	var kinds []string
	for _, x := range idx.Clusters {
		if x.ID == lab.cluster.ID {
			for _, d := range x.Docs {
				kinds = append(kinds, d.Kind)
			}
		}
	}
	if strings.Join(kinds, ",") != "architectuur,database,netwerk,configuratie,herstel,runbooks" {
		t.Fatalf("hoofdstukken: %v", kinds)
	}
	// Een cluster zonder database heeft het hoofdstuk niet.
	var other cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &other)
	if s := c.do("GET", "/api/v1/docs/cluster/database?cluster="+other.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("hoofdstuk Database zonder database: %d", s)
	}

	id := uuid.MustParse(lab.cluster.ID)
	if n, err := e.docs.GenerateCluster(ctx, id, docs.CauseManual); err != nil || n != 6 {
		t.Fatalf("schrijven: %d %v", n, err)
	}
	get := func(kind string) string {
		t.Helper()
		var d testDoc
		if s := c.do("GET", "/api/v1/docs/cluster/"+kind+"?cluster="+lab.cluster.ID, nil, &d); s != http.StatusOK || d.Version == nil {
			t.Fatalf("%s: %d %+v", kind, s, d.testDocMeta)
		}
		return d.Content
	}
	db := get("database")
	for _, w := range []string{
		"# Nextcloud: database",
		"Nextcloud heeft MariaDB Galera op nc1, nc2 en nc3, uitgerold en beheerd door ClusterForge.",
		"| **Galera-cluster** | `nextcloud` |",
		"| **nextcloud** | 2,4 GB | 121 |",
		"| **wiki** | 40 MB | 12 | – |",
		"| **Dumps** | elke nacht om 02:00, 7 dagen bewaard, in `/var/backups/clusterforge-db` op een datanode die Synced is |",
		"4. Wijs de app naar een datanode (nc1, nc2 of nc3) op poort 3306,",
	} {
		if !strings.Contains(db, w) {
			t.Errorf("database mist %q", w)
		}
	}
	// Niet de database van de failovertest.
	if strings.Contains(db, "clusterforge_probe") {
		t.Error("de database van de failovertest staat erin")
	}
	if t.Failed() {
		t.Fatalf("hoofdstuk:\n%s", db)
	}
	if rb := get("runbooks"); !strings.Contains(rb, "het hoofdstuk [Database](database.md)") {
		t.Fatalf("runbooks:\n%s", rb)
	}

	// Nog eens lezen verandert niets.
	if err := e.dbs.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := e.docs.GenerateCluster(ctx, id, docs.CauseManual); err != nil || n != 0 {
		t.Fatalf("opnieuw schrijven: %d %v", n, err)
	}
}
