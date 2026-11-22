package security

import (
	"net/netip"
	"testing"
)

func TestNormalizeScope(t *testing.T) {
	c, ex, pub, err := NormalizeScope(ScopeInput{Name: "Thuis", CIDRs: []string{"192.168.10.0/24", " 192.168.113.153 ", "192.168.10.7/24"}, Exclude: []string{"192.168.10.1"}})
	if err != nil || pub {
		t.Fatalf("err %v, publiek %v", err, pub)
	}
	if len(c) != 2 || c[0] != "192.168.10.0/24" || c[1] != "192.168.113.153/32" || ex[0] != "192.168.10.1/32" {
		t.Errorf("netwerken %v, uitsluitingen %v", c, ex)
	}
	for _, bad := range []ScopeInput{
		{Name: "", CIDRs: []string{"10.0.0.0/24"}},
		{Name: "x", CIDRs: []string{"10.0.0.0/8"}},
		{Name: "x", CIDRs: []string{"geen netwerk"}},
		{Name: "x", CIDRs: nil},
		{Name: "x", CIDRs: []string{"203.0.113.10"}},
		{Name: "x", CIDRs: []string{"203.0.113.10"}, PublicConfirm: PublicConfirmText, PublicNote: "kort"},
	} {
		if _, _, _, err := NormalizeScope(bad); err == nil {
			t.Errorf("%+v had geweigerd moeten worden", bad)
		}
	}
	_, _, pub, err = NormalizeScope(ScopeInput{Name: "VPS", CIDRs: []string{"203.0.113.10"}, PublicConfirm: PublicConfirmText, PublicNote: "Mijn eigen VPS bij de hoster"})
	if err != nil || !pub {
		t.Errorf("publiek met bevestiging: err %v, publiek %v", err, pub)
	}
}

func TestInScope(t *testing.T) {
	scopes := []Scope{{CIDRs: []string{"192.168.10.0/24"}, Exclude: []string{"192.168.10.1/32"}}}
	for addr, want := range map[string]bool{"192.168.10.191": true, "192.168.10.1": false, "192.168.11.5": false, "8.8.8.8": false} {
		if got := InScope(scopes, netip.MustParseAddr(addr)); got != want {
			t.Errorf("InScope(%s) = %v", addr, got)
		}
	}
}
