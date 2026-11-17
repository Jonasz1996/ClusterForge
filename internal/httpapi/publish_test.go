package httpapi

import (
	"net/http"
	"testing"
)

// TestPublishAPI controleert de randen; het werk tegen NPM en Authentik
// staat in internal/publish (ook tegen een echte NPM).
func TestPublishAPI(t *testing.T) {
	_, c := adminClient(t)
	var st struct {
		Domain   string `json:"domain"`
		Defaults struct {
			Authentik     bool     `json:"authentik"`
			AuthentikSkip []string `json:"authentik_skip"`
		} `json:"defaults"`
		NPMHasSecret bool `json:"npm_has_secret"`
	}
	if s := c.do("GET", "/api/v1/publish/settings", nil, &st); s != http.StatusOK || st.Domain != "jbogaert.be" || !st.Defaults.Authentik || len(st.Defaults.AuthentikSkip) == 0 {
		t.Fatalf("instellingen: %d %+v", s, st)
	}
	if s := c.do("PUT", "/api/v1/publish/settings", map[string]any{"domain": "niet geldig"}, nil); s != http.StatusBadRequest {
		t.Errorf("ongeldig domein: %d", s)
	}
	if s := c.do("PUT", "/api/v1/publish/settings", map[string]any{"domain": "Example.ORG.", "npm_identity": "cf@example.org", "npm_secret": "geheim"}, &st); s != http.StatusOK || st.Domain != "example.org" || !st.NPMHasSecret {
		t.Fatalf("bewaren: %d %+v", s, st)
	}
	var raw map[string]any
	if c.do("GET", "/api/v1/publish/settings", nil, &raw); raw["npm_secret"] != nil || raw["npm_secret_enc"] != nil {
		t.Errorf("geheim in het antwoord: %v", raw)
	}
	if s := c.do("GET", "/api/v1/publish/overview", nil, &raw); s != http.StatusOK {
		t.Fatalf("overzicht: %d", s)
	}
	if s := c.do("POST", "/api/v1/publish/test/npm", nil, nil); s == http.StatusOK {
		t.Errorf("test zonder NPM-node lukte")
	}
	if s := c.do("POST", "/api/v1/publish/plan", map[string]any{"action": "explode"}, nil); s != http.StatusBadRequest {
		t.Errorf("onbekende actie: %d", s)
	}
	if s := c.do("POST", "/api/v1/publish/actions", map[string]any{"action": "delete", "host_id": 99}, nil); s != http.StatusBadRequest && s != http.StatusNotFound {
		t.Errorf("verwijderen onbekende host: %d", s)
	}
	var tv struct {
		Window string `json:"window"`
		Series []any  `json:"series"`
		IPs    []any  `json:"ips"`
	}
	if s := c.do("GET", "/api/v1/publish/traffic?window=7d", nil, &tv); s != http.StatusOK || tv.Window != "7d" || tv.Series == nil || tv.IPs == nil {
		t.Fatalf("verkeer: %d %+v", s, tv)
	}
	if s := c.do("GET", "/api/v1/publish/logins", nil, &raw); s != http.StatusOK || raw["window"] != "24h" {
		t.Fatalf("logins: %d %+v", s, raw)
	}
	if s := c.do("POST", "/api/v1/publish/hosts/99/release", nil, nil); s != http.StatusNotFound {
		t.Errorf("loslaten onbekende host: %d", s)
	}
}
