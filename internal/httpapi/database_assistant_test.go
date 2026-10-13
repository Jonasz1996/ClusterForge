package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// TestDatabaseAssistant is de acceptatie van de assistent bij databases
// (mijlpaal 15 van fase 5): MariaDB Galera op nc1 tot nc3 met de database
// nextcloud van Nextcloud zelf en wiki, die het cluster Wiki en een onbekend
// adres gebruiken. De tool database toont de toestand, database_clients wie
// welke database gebruikt, geen van beide een wachtwoord of hash, en de
// analyse van een incident op Nextcloud vraagt naar de database.
func TestDatabaseAssistant(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g := lab.e, lab.c, lab.g
	ctx := context.Background()

	var created struct{ Password string }
	if s := c.do("POST", lab.base+"/"+lab.cid+"/databases", map[string]any{"name": "nextcloud"}, &created); s != http.StatusCreated || created.Password == "" {
		t.Fatalf("database aanmaken: %d", s)
	}
	var wiki cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "wiki", "name": "Wiki", "type": "generic", "environment": "lab"}, &wiki)
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "wiki1", "cluster_id": wiki.ID, "primary_ip": "10.0.20.50"}, nil); s != http.StatusCreated {
		t.Fatalf("wiki1: %d", s)
	}
	for _, h := range g.hosts {
		h.Locked(func() {
			h.MariaDB.Databases = []agenttest.Database{{Name: "nextcloud", Size: 2468 << 20, Tables: 121}, {Name: "wiki", Size: 40 << 20, Tables: 12}}
		})
	}
	nc1 := g.hosts["nc1"]
	nc1.Locked(func() {
		nc1.MariaDB.Clients = []agenttest.Client{
			{User: "nextcloud", Host: "10.0.20.186", Database: "nextcloud", Count: 4},
			{User: "wiki", Host: "10.0.20.50", Database: "wiki", Count: 2},
			{User: "wiki", Host: "10.0.99.9", Database: "wiki", Count: 1},
		}
	})
	if err := e.dbs.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	var j job
	if s := c.do("POST", lab.base+"/"+lab.cid+"/dumps", map[string]any{"database": "nextcloud"}, &j); s != http.StatusAccepted {
		t.Fatalf("dump: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("dump: %s %s", j.Status, j.Error)
	}

	// Geen wachtwoord of hash van een gebruiker in MariaDB, ook niet dat
	// van de nieuwe gebruiker nextcloud.
	secrets := []string{created.Password}
	for _, h := range g.hosts {
		h.Locked(func() {
			for _, u := range h.MariaDB.Users {
				for _, v := range []string{u.Password, u.Hash} {
					if len(v) >= 8 {
						secrets = append(secrets, v)
					}
				}
			}
		})
	}
	leaked := func(who string, out map[string]string) {
		t.Helper()
		for name, o := range out {
			for _, v := range secrets {
				if strings.Contains(o, v) {
					t.Errorf("%s: %s bevat een wachtwoord of hash:\n%s", who, name, o)
				}
			}
		}
	}

	inputs := toolInputs("Nextcloud", "nc1", uuid.NewString())
	inputs["database"] = append(inputs["database"], `{"cluster":"Wiki"}`, `{"cluster":"Bestaat niet"}`)
	inputs["database_clients"] = append(inputs["database_clients"], `{"cluster":"Wiki"}`)
	out := e.runTools(t, "admin", inputs)
	db, clients := strings.Split(out["database"], "\n"), strings.Split(out["database_clients"], "\n")
	if len(db) != 4 || len(clients) != 4 {
		t.Fatalf("resultaten: %q %q", out["database"], out["database_clients"])
	}
	check := func(what, got string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s mist %s:\n%s", what, w, got)
			}
		}
	}
	check("database van Nextcloud", db[0],
		`"titel":"MariaDB Galera"`, `"onderdeel":"mariadb-galera"`, `"samenvatting":"3 van 3 nodes Synced."`, `"soort":"galera"`,
		`{"hostname":"nc1","rol":"data"}`, `"state":"Synced"`, `"cluster_status":"Primary"`, `"bytes":2587885568,`, `"naam":"nextcloud","tabellen":121`,
		`"naam":"wiki"`, `"gedeeld":true`, `"Wiki"`, `"nachtelijk":{"at":"02:00","keep_days":7`, `"database":"nextcloud","freshness":"ok"`,
		`"database":"wiki","freshness":"missing"`, `"vanzelf_herstarten":true`, `"versie":"11.8.3"`, `"versie_onderdeel":"1.0.0"`)
	check("database van Wiki", db[1], "geen database die ClusterForge kent", "database_clients")
	check("onbekend cluster", db[2], `FOUT geen cluster "Bestaat niet"`)
	check("alle databases", clients[0], `"database":"nextcloud"`, `"database":"wiki","engine":"mariadb","gedeeld":true`, `"soort":"cluster","verbindingen":2,"wie":"Wiki"`,
		`"nodes":["wiki1"]`, `"gebruikers":["wiki"]`, `"verbindingen":2`, `"soort":"unknown"`, `"10.0.99.9"`, `"server":"Nextcloud"`)
	check("databases van Nextcloud", clients[1], `"op_dit_cluster":[`, `"rechten":[{`, `"onderdeel":"MariaDB Galera"`, `"gebruiker":"nextcloud"`,
		`"databases":[{"alles":true,"database":"nextcloud"}]`, `"systeemgebruikers":[`, `"root"`, `"gelezen_op":"nc1"`)
	check("databases die Wiki gebruikt", clients[2], `"gebruikt_elders":[{"clients":[`, `"database":"wiki"`, `"server":"Nextcloud"`)
	if strings.Contains(clients[2], "op_dit_cluster\":[{") || strings.Contains(clients[2], `"database":"nextcloud"`) {
		t.Errorf("Wiki heeft geen eigen database en gebruikt nextcloud niet:\n%s", clients[2])
	}
	leaked("admin", out)

	// Een kijker ziet hetzelfde.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	vout := e.runTools(t, "kijker", inputs)
	check("database als kijker", vout["database"], `"samenvatting":"3 van 3 nodes Synced."`, `"database":"wiki","freshness":"missing"`)
	check("database_clients als kijker", vout["database_clients"], `"gebruiker":"nextcloud"`, `"wie":"Wiki"`)
	leaked("kijker", vout)

	// Een incident op Nextcloud: de analyse vraagt naar de database, en het
	// model kijkt met beide tools.
	fc := enableAssistant(t, e, c, false)
	q := store.New(e.pool)
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	inc, err := q.CreateIncident(ctx, store.CreateIncidentParams{Title: "Cluster Nextcloud is verminderd", Severity: "high", StartedAt: t0, DetectedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	clusterID := uuid.MustParse(lab.cluster.ID)
	sub := store.UpsertIncidentSubjectParams{
		IncidentID: inc.ID, SubjectType: "cluster", SubjectID: clusterID, Name: "Nextcloud", ClusterID: &clusterID, Since: t0,
		FirstStatus: "degraded", WorstStatus: "degraded", LastStatus: "degraded", Reason: "MariaDB Galera: nc3 is Donor/Desynced",
	}
	if err := q.UpsertIncidentSubject(ctx, sub); err != nil {
		t.Fatal(err)
	}
	fc.then(
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "tool_use", 4000, 150,
				block{id: "toolu_1", name: "database", input: `{"cluster":"Nextcloud"}`},
				block{id: "toolu_2", name: "database_clients", input: `{"cluster":"Nextcloud"}`})
		},
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "end_turn", 6000, 400, block{text: analysisAnswer})
		},
	)
	s, evs := c.streamPost("/api/v1/incidents/"+inc.ID.String()+"/analyze", nil)
	if s != http.StatusOK || len(streamOf(evs, "error")) != 0 {
		t.Fatalf("analyse: %d %+v", s, evs)
	}
	var labels []string
	for _, tl := range streamOf(evs, "tool") {
		l, _ := tl["label"].(string)
		labels = append(labels, l)
	}
	if strings.Join(labels, "; ") != "Database van Nextcloud bekijken; Gebruikers van de databases van Nextcloud bekijken" {
		t.Fatalf("tools: %v", labels)
	}
	reqs := fc.requests()
	_, prompt, _ := firstPrompt(t, reqs[0])
	check("opdracht", prompt, "Het incident raakte Nextcloud, met een database.", "met de tool database", "database_clients",
		"hoofdstuk Database van de documentatie", "Beheer bij het onderdeel Database")
	// De tweede ronde heeft de resultaten van de tools, zonder geheimen.
	if len(reqs) != 2 || !strings.Contains(reqs[1].Raw, `\"samenvatting\":\"3 van 3 nodes Synced.\"`) {
		t.Fatalf("tweede ronde: %d", len(reqs))
	}
	for _, v := range secrets {
		if strings.Contains(reqs[1].Raw, v) {
			t.Errorf("een wachtwoord of hash ging naar de API")
		}
	}
}
