package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// runHeal roept de tool heal aan als username en geeft het resultaat als
// JSON.
func runHeal(t *testing.T, e *testEnv, username, in string) (string, error) {
	t.Helper()
	ctx := context.Background()
	u, err := store.New(e.pool).GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range e.asst.Tools {
		if tool.Name != "heal" {
			continue
		}
		res, err := tool.Run(ctx, assistant.Caller{Principal: auth.Principal{User: u}}, json.RawMessage(in))
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(res)
		return string(b), nil
	}
	t.Fatal("geen tool heal")
	return "", nil
}

// TestAssistantHeal: bij Jonas' storing ziet de assistent met de tool heal
// wat Zelfherstel vaststelde en het plan, als beheerder en als kijker, maar
// niet de hash van het plan. Na Nu herstellen ziet hij de gelukte poging en
// de geschiedenis van het cluster.
func TestAssistantHeal(t *testing.T) {
	e, c := adminClient(t)
	d := newHealDB(t, e, c)
	r := d.heal(t, e, c, func(r healView) bool { return strings.Contains(r.codes(), "db1+db2+db3") })
	if r.Plan == nil || r.Plan.Hash == "" {
		t.Fatalf("plan: %+v", r.Plan)
	}
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	for _, who := range []string{"admin", "kijker"} {
		out, err := runHeal(t, e, who, `{"cluster":"database cluster"}`)
		if err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		for _, w := range []string{
			`"cluster":"database cluster"`, `"cluster_id":"` + d.cluster.ID + `"`, `"code":"galera_no_primary"`, `"nodes":["db1","db2","db3"]`,
			"pid 169", `"actie":"galera"`, `"mag_vanzelf":false`, `"waarom_vanzelf":"`, `"hostname":"db1"`, `"hostname":"db2"`, `"hostname":"db3"`,
			`"cluster_address":"` + healNewAddr + `"`, `"vanzelf":{"aan":false,"kan_aan":true,"nu_vanzelf":false}`,
			`"geschiedenis":{"opmerking":"Zelfherstel deed de laatste 30 dagen niets op dit cluster"}`,
		} {
			if !strings.Contains(out, w) {
				t.Errorf("%s mist %s:\n%s", who, w, out)
			}
		}
		if strings.Contains(out, `"hash"`) || strings.Contains(out, r.Plan.Hash) || strings.Contains(out, `"key"`) {
			t.Errorf("%s: de hash of de sleutel gaat mee:\n%s", who, out)
		}
	}
	var te assistant.ToolError
	if _, err := runHeal(t, e, "admin", `{"cluster":"Bestaat niet"}`); !errors.As(err, &te) || !strings.Contains(te.Msg, `geen cluster "Bestaat niet"`) {
		t.Fatalf("onbekend cluster: %v", err)
	}
	// Leeg is elke database, zonder geschiedenis.
	if all, err := runHeal(t, e, "kijker", `{"cluster":""}`); err != nil || !strings.Contains(all, `"cluster_id":"`+d.cluster.ID+`"`) ||
		!strings.Contains(all, `"code":"galera_no_primary"`) || strings.Contains(all, `"geschiedenis"`) {
		t.Fatalf("alle databases: %v\n%s", err, all)
	}

	// Na Nu herstellen: niets meer mis, de gelukte poging en de
	// geschiedenis.
	var j job
	if s := c.do("POST", "/api/v1/heal/run", map[string]any{"key": r.Key, "hash": r.Plan.Hash}, &j); s != http.StatusAccepted {
		t.Fatalf("herstellen: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("herstel: %s %s", j.Status, j.Error)
	}
	d.heal(t, e, c, func(r healView) bool { return r.codes() == "" && r.Plan == nil })
	out, err := runHeal(t, e, "admin", `{"cluster":"database-cluster"}`)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Databases []struct {
			Opmerking      string `json:"opmerking"`
			LaatsteHerstel struct {
				Taak   string `json:"taak"`
				Status string `json:"status"`
			} `json:"laatste_herstel"`
		} `json:"databases"`
		Geschiedenis []struct {
			Taak   string `json:"taak"`
			Titel  string `json:"titel"`
			Status string `json:"status"`
			Door   string `json:"door"`
		} `json:"geschiedenis"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if len(v.Databases) != 1 || v.Databases[0].Opmerking != "Zelfherstel ziet niets mis" || v.Databases[0].LaatsteHerstel.Taak != j.ID ||
		v.Databases[0].LaatsteHerstel.Status != "succeeded" {
		t.Errorf("na het herstel:\n%s", out)
	}
	if g := v.Geschiedenis; len(g) != 1 || g[0].Taak != j.ID || g[0].Titel != "Zelfherstel: MariaDB van database cluster" || g[0].Status != "succeeded" ||
		g[0].Door != "admin" {
		t.Errorf("geschiedenis:\n%s", out)
	}
}

// TestHealForModel: wat de agent meegeeft gaat gemaskeerd naar het model,
// zonder de hash van het plan, en als de ruimte op is, vallen eerst de
// gezonde databases weg.
func TestHealForModel(t *testing.T) {
	shop := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	jobID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	at := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	down := gen.HealReport{
		Key: "postgresql/" + shop.String(), ClusterId: nullable.NewNullableWithValue(shop), ClusterName: "Shop",
		ClusterEnvironment: nullable.NewNullableWithValue(gen.Environment("lab")), Engine: gen.HealReportEnginePostgresql, Kind: "patroni", Health: "down",
		Problems: []gen.HealProblem{{
			Code: "patroni_dcs", Level: gen.HealProblemLevelDown, Title: "Patroni op pg1 bereikt etcd niet",
			Why: "dial postgres://app:geheim@db:5432: connection refused", Nodes: []string{"pg1"},
		}},
		Plan: nullable.NewNullableWithValue(gen.HealPlan{
			Action: "restart", Steps: []string{"Op pg1 patroni starten."}, Hash: "4f1d2c9e0b7a", ManagedComponentId: nullable.NewNullNullable[uuid.UUID](),
			Nodes: []gen.HealPlanNode{{NodeId: uuid.New(), Hostname: "pg1", Unit: "patroni", Start: true, KillPids: []int{}}},
		}),
		Auto: nullable.NewNullableWithValue(gen.HealAuto{
			Allowed: true, Note: "Dit plan mag vanzelf, maar Vanzelf herstellen staat uit op dit cluster.", PausedAt: nullable.NewNullNullable[time.Time](),
		}),
		LastRun: nullable.NewNullableWithValue(gen.HealRun{
			JobId: jobID, Status: gen.JobStatusFailed, Error: "pg1: password=hunter22 werd geweigerd", CreatedAt: at,
			FinishedAt: nullable.NewNullableWithValue(at.Add(time.Minute)),
		}),
	}
	b, _ := json.Marshal(healForModel(down, "Shop"))
	got := string(b)
	for _, w := range []string{
		"postgres://app:[geheim]@db:5432", "password=[geheim]", `"cluster":"Shop"`, `"cluster_id":"` + shop.String() + `"`, `"omgeving":"lab"`,
		`"ernst":"down"`, `"actie":"restart"`, `"mag_vanzelf":false`, `"start":true`, `"kan_aan":true`, `"aan":false`,
		`"laatste_herstel":{"aangemaakt":"2026-10-18T03:00:00Z","fout":"pg1: password=[geheim] werd geweigerd","klaar":"2026-10-18T03:01:00Z","status":"failed","taak":"` + jobID.String() + `"}`,
	} {
		if !strings.Contains(got, w) {
			t.Errorf("mist %s:\n%s", w, got)
		}
	}
	for _, no := range []string{"geheim@", "hunter22", `"hash"`, "4f1d2c9e0b7a", `"key"`, "node_id", "gepauzeerd_sinds", "hoort_bij_onderdeel"} {
		if strings.Contains(got, no) {
			t.Errorf("%s staat erin:\n%s", no, got)
		}
	}

	// Een losse node heet naar zijn hostname.
	node := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	healthy := func(key string) gen.HealReport {
		return gen.HealReport{
			Key: key, ClusterId: nullable.NewNullNullable[uuid.UUID](), ClusterEnvironment: nullable.NewNullNullable[gen.Environment](),
			Engine: gen.HealReportEngineMariadb, Kind: "single", Health: "ok", Problems: []gen.HealProblem{},
			Plan: nullable.NewNullNullable[gen.HealPlan](), Auto: nullable.NewNullNullable[gen.HealAuto](), LastRun: nullable.NewNullNullable[gen.HealRun](),
		}
	}
	web1, web2 := healthy("mariadb/node/"+node.String()), healthy("mariadb/node/"+uuid.NewString())
	warn := healthy("mariadb/node/" + uuid.NewString())
	warn.Health, warn.Problems = "warning", []gen.HealProblem{{Code: "no_inspect", Level: gen.HealProblemLevelWarning, Title: "Zelfherstel kan db9 niet bekijken", Nodes: []string{"db9"}}}
	groups := map[string]gen.DatabaseGroup{web1.Key: {Servers: []gen.DatabaseServer{{Hostname: "web1", NodeId: node}}}}
	items, cut, worst := healItems([]gen.HealReport{web1, web2, warn, down}, groups, listBudget)
	if cut != 0 || worst != 2 || len(items) != 4 {
		t.Fatalf("%d weg: %v", cut, items)
	}
	var order []string
	for _, it := range items {
		m := it.(map[string]any)
		order = append(order, m["gezondheid"].(string))
	}
	if strings.Join(order, " ") != "down warning ok ok" {
		t.Errorf("volgorde: %v", order)
	}
	if b, _ := json.Marshal(items[1]); !strings.Contains(string(b), `"code":"no_inspect"`) {
		t.Errorf("tweede: %s", b)
	}
	if b, _ := json.Marshal(items[2]); string(b) != `{"engine":"mariadb","gezondheid":"ok","node":"web1","node_id":"`+node.String()+`","opmerking":"Zelfherstel ziet niets mis","soort":"single"}` {
		t.Errorf("losse node: %s", b)
	}

	// Met ruimte voor één database blijft die met het probleem.
	b, _ = json.Marshal(healForModel(down, "Shop"))
	items, cut, worst = healItems([]gen.HealReport{web1, web2, warn, down}, groups, len(b)+1)
	if cut != 3 || worst != 1 || len(items) != 1 || items[0].(map[string]any)["cluster"] != "Shop" {
		t.Errorf("afgekapt: %d %d %v", cut, worst, items)
	}
	// Twee databases die plat liggen: wat wegvalt, ligt ook plat.
	if _, cut, worst = healItems([]gen.HealReport{down, down}, groups, len(b)+1); cut != 1 || worst != 0 {
		t.Errorf("twee plat: %d %d", cut, worst)
	}
}
