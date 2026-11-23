package firewall

import (
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestMissingVRRP: de fout van 22/11 (B4.9): ufw liet alleen multicast VRRP
// van een ander netwerk toe, unicast van de peer viel weg.
func TestMissingVRRP(t *testing.T) {
	st := &protocol.FirewallStatus{Active: true, Incoming: "deny", Rules: []protocol.FirewallRule{
		{Action: "allow", Proto: "vrrp", From: "192.168.0.0/24"},
		{Action: "allow", Proto: "tcp", Port: "22", From: "any"},
	}}
	peers := []string{"192.168.102.246", "192.168.0.7"}
	if got := MissingVRRP(st, peers); len(got) != 1 || got[0] != "192.168.102.246" {
		t.Fatalf("MissingVRRP = %v", got)
	}
	st.Rules = append(st.Rules, protocol.FirewallRule{Action: "allow", Proto: "vrrp", From: "192.168.102.246"})
	if got := MissingVRRP(st, peers); len(got) != 0 {
		t.Fatalf("na de regel: %v", got)
	}
	if got := MissingVRRP(&protocol.FirewallStatus{Active: false, Incoming: "deny"}, peers); got != nil {
		t.Fatalf("ufw uit: %v", got)
	}
	p := Build(Input{Status: &protocol.FirewallStatus{Active: true, Incoming: "deny"}, Keepalived: true, Unicast: []string{"10.0.0.2"}})
	if len(p.VRRPMissing) != 1 || !strings.Contains(strings.Join(p.Warnings, " "), "split-brain") {
		t.Fatalf("plan: %+v", p)
	}
	if s := VRRPScript(p.VRRPMissing); !strings.Contains(s, "ufw allow proto vrrp from 10.0.0.2 comment 'cf:VRRP keepalived'") {
		t.Fatalf("script:\n%s", s)
	}
}
