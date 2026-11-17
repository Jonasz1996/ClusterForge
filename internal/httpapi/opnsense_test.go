package httpapi

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestOPNsenseAPI controleert de randen; de wijzigingstaak zelf staat in
// internal/opnsense en de agentkant in internal/agent.
func TestOPNsenseAPI(t *testing.T) {
	_, c := adminClient(t)
	var list struct {
		Items    []any `json:"items"`
		Protocol int   `json:"protocol"`
	}
	if s := c.do("GET", "/api/v1/opnsense/clusters", nil, &list); s != http.StatusOK || list.Items == nil || list.Protocol != 50 {
		t.Fatalf("clusters: %d %+v", s, list)
	}
	base := "/api/v1/opnsense/firewalls/" + uuid.NewString()
	if s := c.do("GET", base+"/api?path=firewall/alias/searchItem", nil, nil); s != http.StatusNotFound {
		t.Errorf("onbekende firewall: %d", s)
	}
	if s := c.do("GET", base+"/api?path=auth/user/searchApiKey", nil, nil); s != http.StatusBadRequest {
		t.Errorf("API-sleutels lezen: %d", s)
	}
	if s := c.do("GET", base+"/configctl?action=system%20reboot", nil, nil); s != http.StatusBadRequest {
		t.Errorf("configctl reboot: %d", s)
	}
	bad := map[string]any{"calls": []map[string]any{{"path": "core/firmware/poweroff"}}}
	if s := c.do("POST", base+"/plan", bad, nil); s != http.StatusBadRequest {
		t.Errorf("plan met verboden pad: %d", s)
	}
	if s := c.do("POST", base+"/changes", bad, nil); s != http.StatusBadRequest && s != http.StatusNotFound {
		t.Errorf("wijziging met verboden pad: %d", s)
	}
	if s := c.do("POST", base+"/sync", nil, nil); s != http.StatusNotFound {
		t.Errorf("sync onbekende firewall: %d", s)
	}
}
