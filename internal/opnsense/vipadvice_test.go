package opnsense

import (
	"strings"
	"testing"
)

func TestPlanVIPAdvice(t *testing.T) {
	moves := map[string]vipMove{
		"192.168.113.153": {Old: "192.168.113.153", VIP: "192.168.113.160", Cluster: "ClusterForge", Why: "oud"},
		"192.168.10.21":   {Old: "192.168.10.21", VIP: "192.168.10.50", Cluster: "RustDesk", Why: "node"},
	}
	nat := []haveObj{
		{UUID: "11111111-0000-0000-0000-000000000001", F: map[string]string{"target": "192.168.10.21", "local-port": "21116", "descr": "rustdesk"}},
		{UUID: "11111111-0000-0000-0000-000000000002", F: map[string]string{"target": "192.168.10.99", "descr": "ander"}},
		{UUID: "11111111-0000-0000-0000-000000000003", F: map[string]string{"target": "192.168.10.21", "disabled": "1"}},
	}
	filter := []haveObj{
		{UUID: "22222222-0000-0000-0000-000000000001", F: map[string]string{"action": "pass", "interface": "opt3", "source_net": "lan",
			"destination_net": "192.168.113.153", "destination_port": "4222", "protocol": "TCP", "description": "agents naar NATS", "sequence": "10"}},
		// Naar het VIP op 8080 bestaat al: geen voorstel.
		{UUID: "22222222-0000-0000-0000-000000000002", F: map[string]string{"action": "pass", "destination_net": "192.168.113.153", "destination_port": "8080"}},
		{UUID: "22222222-0000-0000-0000-000000000003", F: map[string]string{"action": "pass", "destination_net": "192.168.113.160", "destination_port": "8080"}},
		{UUID: "22222222-0000-0000-0000-000000000004", F: map[string]string{"action": "block", "destination_net": "192.168.113.153", "destination_port": "22"}},
	}
	items := planVIPAdvice(nat, filter, moves)
	if len(items) != 2 {
		t.Fatalf("%d voorstellen: %+v", len(items), items)
	}
	natItem, ruleItem := items[0], items[1]
	if natItem.Key != "vipnat:11111111-0000-0000-0000-000000000001" || natItem.call.Path != "firewall/d_nat/setRule/11111111-0000-0000-0000-000000000001" ||
		!strings.Contains(natItem.Detail, "192.168.10.21 → 192.168.10.50") {
		t.Errorf("nat: %+v", natItem)
	}
	rule := ruleItem.call.Body["rule"].(map[string]any)
	if ruleItem.call.Path != "firewall/filter/addRule" || rule["destination_net"] != "192.168.113.160" || rule["destination_port"] != "4222" ||
		rule["interface"] != "opt3" || rule["sequence"] != "9" || !ruleItem.Manual {
		t.Errorf("regel: %+v %+v", ruleItem, rule)
	}
}
