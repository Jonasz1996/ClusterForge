package httpapi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/components"
)

// De database-instructies van het Zabbix-recept op een echt (nagespeeld)
// Galera-cluster: database met collatie, gebruiker met rechten, schema
// laden, en een tweede keer verandert niets.
func TestRecipeDatabase(t *testing.T) {
	lab := newGaleraLab(t)
	for _, h := range lab.g.hosts {
		writeFile(t, filepath.Join(h.Root, "usr/share/zabbix/sql-scripts/mysql/server.sql"),
			"-- Table structure for table `config`\nCREATE TABLE config (id int);\n-- Table structure for table `users`\nCREATE TABLE users (id int);\n", 0o644)
	}
	steps := []components.RecipeDBStep{
		{Kind: "db_database", Database: "zabbix", Charset: "utf8mb4", Collation: "utf8mb4_bin"},
		{Kind: "db_user", User: "zabbix", Password: "Abcdefgh12345678Abcdefgh12345678", GrantOn: "zabbix"},
		{Kind: "db_import", Database: "zabbix", Path: "/usr/share/zabbix/sql-scripts/mysql/server.sql", TrustFunctions: true, Unless: "SELECT 1 FROM config"},
	}
	ctx := context.Background()
	cid, comp := uuid.MustParse(lab.cluster.ID), uuid.MustParse(lab.cid)
	res, err := lab.e.cmps.RunRecipeDatabase(ctx, cid, comp, steps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || !res[2].Changed || !strings.Contains(res[2].Message, "geladen in zabbix") {
		t.Fatalf("eerste keer: %+v", res)
	}
	for _, r := range res {
		if strings.Contains(r.Message, "Abcdefgh") {
			t.Errorf("wachtwoord in het resultaat: %s", r.Message)
		}
	}
	var v componentDatabasesView
	lab.c.do("GET", lab.base+"/"+lab.cid+"/databases", nil, &v)
	dbs, _ := v.names()
	if !strings.Contains(strings.Join(dbs, " "), "zabbix(zabbix)") {
		t.Fatalf("databases: %v", dbs)
	}
	for name, h := range lab.g.hosts {
		if h.MariaDB.TrustFunctions {
			t.Errorf("%s: log_bin_trust_function_creators staat nog aan", name)
		}
	}

	res, err = lab.e.cmps.RunRecipeDatabase(ctx, cid, comp, steps)
	if err != nil || len(res) != 3 || res[2].Changed || !strings.Contains(res[2].Message, "niet geladen") {
		t.Fatalf("tweede keer: %+v %v", res, err)
	}
}
