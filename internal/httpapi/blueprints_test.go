package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/recipes"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestBlueprints(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("admin", "een-lang-wachtwoord", store.UserRoleAdmin)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	c := e.client()
	if s, _, _ := c.login("admin", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatal(s)
	}
	zabbix := recipes.Examples()[0].Source
	if s := c.do("POST", "/api/v1/recipes", map[string]any{"source": zabbix}, nil); s != http.StatusCreated {
		t.Fatal(s)
	}
	// Een unieke naam: het register van templates is gedeeld tussen tests.
	comp := map[string]any{
		"name": "zabbix-api-test", "title": "Zabbix (test)",
		"roles":    []map[string]any{{"name": "server", "recipe": "zabbix", "recipe_version": "1.0", "count": 3}},
		"vip":      map[string]any{"role": "server"},
		"database": map[string]any{"role": "server", "buffer_pool": "1G", "extra": "max_allowed_packet = 64M"},
	}
	type result struct {
		Template string   `json:"template"`
		Version  string   `json:"version"`
		Created  bool     `json:"created"`
		Warnings []string `json:"warnings"`
	}
	var res result
	if s := c.do("POST", "/api/v1/blueprints", comp, &res); s != http.StatusCreated || res.Template != "eigen-zabbix-api-test" || res.Version != "1.0.0" || !res.Created {
		t.Fatalf("samenstellen: %d %+v", s, res)
	}
	// Dezelfde samenstelling: dezelfde versie.
	if s := c.do("POST", "/api/v1/blueprints", comp, &res); s != http.StatusOK || res.Version != "1.0.0" || res.Created {
		t.Fatalf("nog eens: %d %+v", s, res)
	}
	// Een andere buffer pool: een nieuwe versie.
	comp["database"].(map[string]any)["buffer_pool"] = "2G"
	if s := c.do("POST", "/api/v1/blueprints", comp, &res); s != http.StatusCreated || res.Version != "1.0.1" {
		t.Fatalf("gewijzigd: %d %+v", s, res)
	}
	// De template staat tussen de andere, om mee uit te rollen.
	var tpls struct {
		Items []struct {
			Name     string `json:"name"`
			Version  string `json:"version"`
			Database *struct {
				Role string `json:"role"`
			} `json:"database"`
		} `json:"items"`
	}
	if s := c.do("GET", "/api/v1/templates", nil, &tpls); s != 200 {
		t.Fatal(s)
	}
	found := false
	for _, tp := range tpls.Items {
		if tp.Name == "eigen-zabbix-api-test" {
			found = tp.Version == "1.0.1" && tp.Database != nil && tp.Database.Role == "server"
		}
	}
	if !found {
		t.Errorf("template niet in de lijst: %+v", tpls.Items)
	}
	var list []struct {
		Template    string `json:"template"`
		Version     string `json:"version"`
		Composition struct {
			Database struct {
				BufferPool string `json:"buffer_pool"`
			} `json:"database"`
		} `json:"composition"`
	}
	if s := c.do("GET", "/api/v1/blueprints", nil, &list); s != 200 || len(list) != 1 || list[0].Version != "1.0.1" || list[0].Composition.Database.BufferPool != "2G" {
		t.Errorf("lijst: %d %+v", s, list)
	}
	// Fouten wijzen een veld aan.
	var ev struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	}
	delete(comp, "database")
	if s := c.do("POST", "/api/v1/blueprints", comp, &ev); s != http.StatusBadRequest || ev.Field != "database" || !strings.Contains(ev.Message, "database") {
		t.Errorf("zonder database: %d %+v", s, ev)
	}
	// Een kijker mag niet samenstellen.
	k := e.client()
	if s, _, _ := k.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatal(s)
	}
	if s := k.do("POST", "/api/v1/blueprints", comp, nil); s != http.StatusForbidden {
		t.Errorf("kijker: %d", s)
	}
}
