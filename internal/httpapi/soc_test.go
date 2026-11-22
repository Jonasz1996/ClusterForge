package httpapi

import (
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestSoc(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	c := e.client()
	if s := c.do("GET", "/api/v1/soc", nil, nil); s != 401 {
		t.Fatalf("zonder login: %d", s)
	}
	c.login("kijker", "een-lang-wachtwoord", "")

	var o struct {
		Window string `json:"window"`
		Level  struct {
			Level   string   `json:"level"`
			Reasons []string `json:"reasons"`
		} `json:"level"`
		Score struct {
			Score int `json:"score"`
		} `json:"score"`
		Activity  []any `json:"activity"`
		Attackers []any `json:"attackers"`
		Coverage  []any `json:"coverage"`
	}
	if s := c.do("GET", "/api/v1/soc?window=7d", nil, &o); s != 200 {
		t.Fatalf("soc: %d", s)
	}
	if o.Window != "7d" || len(o.Activity) != 42 || o.Attackers == nil || o.Coverage == nil || len(o.Level.Reasons) == 0 {
		t.Fatalf("antwoord = %+v", o)
	}
	// Een onbekend venster wordt 24 uur.
	if s := c.do("GET", "/api/v1/soc?window=jaar", nil, &o); s != 200 || o.Window != "24h" || len(o.Activity) != 24 {
		t.Fatalf("onbekend venster: %d %+v", s, o.Window)
	}
}
