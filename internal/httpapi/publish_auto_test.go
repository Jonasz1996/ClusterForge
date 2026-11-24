package httpapi

import (
	"net/http"
	"testing"
)

// TestPublishAutoAPI: regels bewaren en lezen; het werk zelf staat in
// internal/publish.
func TestPublishAutoAPI(t *testing.T) {
	_, c := adminClient(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "rd", "name": "RustDesk", "type": "keepalived", "environment": "lab"}, &cl)
	var view struct {
		Rules      []map[string]any `json:"rules"`
		Items      []any            `json:"items"`
		Candidates []any            `json:"candidates"`
		Ready      string           `json:"ready"`
	}
	if s := c.do("GET", "/api/v1/publish/auto", nil, &view); s != http.StatusOK || view.Ready == "" || view.Rules == nil {
		t.Fatalf("overzicht: %d %+v", s, view)
	}
	if s := c.do("PUT", "/api/v1/publish/auto/rules", map[string]any{"enabled": true}, nil); s != http.StatusBadRequest {
		t.Errorf("zonder cluster of dienst: %d", s)
	}
	var rule map[string]any
	if s := c.do("PUT", "/api/v1/publish/auto/rules", map[string]any{"cluster_id": cl.ID, "enabled": true}, &rule); s != http.StatusOK || rule["cluster_name"] != "RustDesk" || rule["cleanup"] != true {
		t.Fatalf("regel: %d %+v", s, rule)
	}
	if s := c.do("PUT", "/api/v1/publish/auto/rules", map[string]any{"cluster_id": cl.ID, "enabled": false, "cleanup": false}, &rule); s != http.StatusOK || rule["enabled"] != false || rule["cleanup"] != false {
		t.Fatalf("regel uit: %d %+v", s, rule)
	}
	c.do("GET", "/api/v1/publish/auto", nil, &view)
	if len(view.Rules) != 1 {
		t.Fatalf("regels: %+v", view.Rules)
	}
	if s := c.do("POST", "/api/v1/publish/auto/retry", map[string]any{"service_key": "x/tcp/80"}, nil); s != http.StatusNotFound {
		t.Errorf("opnieuw onbekend: %d", s)
	}
	if s := c.do("GET", "/api/v1/publish/auto/backup?service_key=x", nil, nil); s != http.StatusNotFound {
		t.Errorf("kopie onbekend: %d", s)
	}
}
