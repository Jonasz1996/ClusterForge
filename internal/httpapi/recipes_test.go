package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type recipeCheckView struct {
	Ok          bool `json:"ok"`
	Diagnostics []struct {
		Line    int    `json:"line"`
		Message string `json:"message"`
		Warning bool   `json:"warning"`
	} `json:"diagnostics"`
	Recipe *struct {
		Name  string `json:"name"`
		Needs []struct {
			Service string `json:"service"`
			Engine  string `json:"engine"`
		} `json:"needs"`
		Params []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"params"`
		Roles []struct {
			Name  string `json:"name"`
			Steps []struct {
				Kind   string         `json:"kind"`
				Once   bool           `json:"once"`
				Secret bool           `json:"secret"`
				Title  string         `json:"title"`
				Spec   map[string]any `json:"spec"`
			} `json:"steps"`
			Health []struct {
				Kind string `json:"kind"`
			} `json:"health"`
		} `json:"roles"`
	} `json:"recipe"`
}

type recipeVersionView struct {
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	Source    string          `json:"source"`
	CreatedBy *string         `json:"created_by"`
	Check     recipeCheckView `json:"check"`
}

func TestRecipes(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("admin", "een-lang-wachtwoord", store.UserRoleAdmin)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	c := e.client()
	if s, _, _ := c.login("admin", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatal(s)
	}

	var examples []struct{ Name, Title, Source string }
	if s := c.do("GET", "/api/v1/recipes/examples", nil, &examples); s != 200 || len(examples) == 0 || examples[0].Name != "zabbix" {
		t.Fatalf("voorbeelden: %d %+v", s, examples)
	}
	zabbix := examples[0].Source

	// Controleren bewaart niets.
	var chk recipeCheckView
	if s := c.do("POST", "/api/v1/recipes/check", map[string]any{"source": zabbix}, &chk); s != 200 || !chk.Ok || chk.Recipe == nil {
		t.Fatalf("check: %d %+v", s, chk)
	}
	if r := chk.Recipe; r.Name != "zabbix" || len(r.Roles) != 1 || len(r.Roles[0].Steps) != 12 || r.Needs[0].Engine != "mariadb" || r.Params[1].Type != "secret" {
		t.Fatalf("recept: %+v", r)
	}
	var list []struct {
		Name     string   `json:"name"`
		Versions []string `json:"versions"`
	}
	if c.do("GET", "/api/v1/recipes", nil, &list); len(list) != 0 {
		t.Fatalf("na check: %+v", list)
	}

	// Een fout geeft de regel.
	if s := c.do("POST", "/api/v1/recipes/check", map[string]any{"source": "RECIPE app 1.0\nFROM debian:13\nRUN apt-get update\n"}, &chk); s != 200 || chk.Ok || chk.Recipe != nil ||
		len(chk.Diagnostics) != 1 || chk.Diagnostics[0].Line != 3 {
		t.Fatalf("fout: %d %+v", s, chk)
	}

	// Bewaren.
	var v recipeVersionView
	if s := c.do("POST", "/api/v1/recipes", map[string]any{"source": zabbix}, &v); s != http.StatusCreated || v.Name != "zabbix" || v.Version != "1.0" || v.CreatedBy == nil || *v.CreatedBy != "admin" {
		t.Fatalf("bewaren: %d %+v", s, v)
	}
	// Dezelfde tekst nog eens: niets nieuws.
	if s := c.do("POST", "/api/v1/recipes", map[string]any{"source": zabbix}, &v); s != http.StatusOK {
		t.Fatalf("opnieuw: %d", s)
	}
	// Andere tekst, zelfde versie: 409.
	var ae apiErr
	if s := c.do("POST", "/api/v1/recipes", map[string]any{"source": zabbix + "APT fping\n"}, &ae); s != http.StatusConflict || !strings.Contains(ae.Message, "verhoog de versie") {
		t.Fatalf("conflict: %d %+v", s, ae)
	}
	// Met fouten: 400 met de regels.
	if s := c.do("POST", "/api/v1/recipes", map[string]any{"source": "RECIPE app 1.0\n"}, &chk); s != http.StatusBadRequest || chk.Ok || len(chk.Diagnostics) == 0 {
		t.Fatalf("met fouten: %d %+v", s, chk)
	}
	v2 := strings.Replace(zabbix, "RECIPE zabbix 1.0", "RECIPE zabbix 1.0.1", 1)
	if s := c.do("POST", "/api/v1/recipes", map[string]any{"source": v2}, &v); s != http.StatusCreated || v.Version != "1.0.1" {
		t.Fatalf("tweede versie: %d %+v", s, v)
	}
	if c.do("GET", "/api/v1/recipes", nil, &list); len(list) != 1 || strings.Join(list[0].Versions, ",") != "1.0.1,1.0" {
		t.Fatalf("lijst: %+v", list)
	}
	var d recipeVersionView
	if s := c.do("GET", "/api/v1/recipes/zabbix/versions/1.0", nil, &d); s != 200 || d.Source != zabbix || !d.Check.Ok || d.Check.Recipe == nil {
		t.Fatalf("versie: %d %+v", s, d)
	}

	// Een kijker leest maar bewaart niet.
	k := e.client()
	k.login("kijker", "een-lang-wachtwoord", "")
	if s := k.do("GET", "/api/v1/recipes/zabbix", nil, nil); s != 200 {
		t.Fatalf("kijker leest: %d", s)
	}
	if s := k.do("POST", "/api/v1/recipes", map[string]any{"source": v2}, nil); s != http.StatusForbidden {
		t.Fatalf("kijker bewaart: %d", s)
	}
	if s := k.do("DELETE", "/api/v1/recipes/zabbix/versions/1.0", nil, nil); s != http.StatusForbidden {
		t.Fatalf("kijker verwijdert: %d", s)
	}

	if s := c.do("DELETE", "/api/v1/recipes/zabbix/versions/1.0", nil, nil); s != http.StatusNoContent {
		t.Fatalf("verwijderen: %d", s)
	}
	if s := c.do("GET", "/api/v1/recipes/zabbix/versions/1.0", nil, nil); s != http.StatusNotFound {
		t.Fatalf("na verwijderen: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/recipes/zabbix/versions/1.0", nil, nil); s != http.StatusNotFound {
		t.Fatalf("twee keer verwijderen: %d", s)
	}
	if s := c.do("GET", "/api/v1/recipes/bestaat-niet", nil, nil); s != http.StatusNotFound {
		t.Fatalf("onbekend recept: %d", s)
	}
}
