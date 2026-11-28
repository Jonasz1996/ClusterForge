package httpapi

import (
	"net/http"
	"testing"
)

// TestPublishScanAPI: uitzonderingen en eigenaars; Scannen zelf wil een
// gekoppelde NPM (de logica staat in internal/publish).
func TestPublishScanAPI(t *testing.T) {
	_, c := adminClient(t)
	var view struct {
		Policies []struct {
			Name  string   `json:"name"`
			Mode  string   `json:"mode"`
			Paths []string `json:"paths"`
		} `json:"policies"`
		Disabled   []any `json:"disabled"`
		ManualOnly bool  `json:"manual_only"`
	}
	if s := c.do("GET", "/api/v1/publish/scan", nil, &view); s != http.StatusOK || !view.ManualOnly || view.Disabled == nil {
		t.Fatalf("scan: %d %+v", s, view)
	}
	seen := map[string]string{}
	for _, p := range view.Policies {
		seen[p.Name] = p.Mode
	}
	if seen["authentik"] != "never" || seen["vaultx"] != "own_login" || seen["epgiptv"] != "except_paths" {
		t.Errorf("uitzonderingen van Jonas: %v", seen)
	}
	if s := c.do("POST", "/api/v1/publish/scan", nil, nil); s != http.StatusBadRequest {
		t.Errorf("scannen zonder NPM: %d", s)
	}
	if s := c.do("PUT", "/api/v1/publish/policies", map[string]any{"name": "x", "mode": "except_paths"}, nil); s != http.StatusBadRequest {
		t.Errorf("geen paden: %d", s)
	}
	var pol map[string]any
	if s := c.do("PUT", "/api/v1/publish/policies", map[string]any{"name": "Vault-X", "mode": "except_paths", "paths": []string{"/api"}}, &pol); s != http.StatusOK || pol["name"] != "vault-x" {
		t.Errorf("bewaren: %d %v", s, pol)
	}
	if s := c.do("DELETE", "/api/v1/publish/policies/vault-x", nil, nil); s != http.StatusNoContent {
		t.Errorf("weghalen: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/publish/policies/vault-x", nil, nil); s != http.StatusNotFound {
		t.Errorf("twee keer weghalen: %d", s)
	}
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "wk", "name": "Wiki", "type": "keepalived", "environment": "test"}, &cl)
	if s := c.do("PUT", "/api/v1/publish/owners", map[string]any{"domain": "wiki.test.jbogaert.be", "cluster_id": cl.ID}, nil); s != http.StatusNoContent {
		t.Errorf("eigenaar: %d", s)
	}
	var owners struct {
		Owners []map[string]any `json:"owners"`
	}
	if s := c.do("GET", "/api/v1/publish/owners", nil, &owners); s != http.StatusOK || len(owners.Owners) != 1 || owners.Owners[0]["cluster_name"] != "Wiki" || owners.Owners[0]["source"] != "hand" {
		t.Errorf("eigenaars: %d %v", s, owners)
	}
	if s := c.do("PUT", "/api/v1/publish/owners", map[string]any{"domain": "wiki.test.jbogaert.be"}, nil); s != http.StatusBadRequest {
		t.Errorf("zonder cluster: %d", s)
	}
	if s := c.do("POST", "/api/v1/publish/plan", map[string]any{"action": "scan"}, nil); s != http.StatusBadRequest {
		t.Errorf("plan zonder NPM: %d", s)
	}
}
