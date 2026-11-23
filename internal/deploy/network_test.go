package deploy

import (
	"slices"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestNetworkHint(t *testing.T) {
	fw := []protocol.OPNsenseFacts{{
		Interfaces: []protocol.OPNsenseInterface{
			{Name: "wan", Device: "vtnet0", Addresses: []string{"81.82.83.84/24"}},
			{Name: "opt5", Descr: "SERVERS", Device: "vlan0.105", Addresses: []string{"192.168.105.2/24"}},
		},
		VIPs: []protocol.OPNsenseVIP{
			{Interface: "opt5", Address: "192.168.105.1/24", Mode: "carp"},
			{Interface: "wan", Address: "81.82.83.80/24", Mode: "carp"},
		},
	}}
	used := map[string]bool{"192.168.105.20": true, "192.168.105.22": true}
	h := networkHint("192.168.105.20", fw, nil, used, 3)
	if h.Prefix != "192.168.105.0/24" || h.FirstIP != "192.168.105.20/24" || h.VLAN == nil || *h.VLAN != 105 {
		t.Fatalf("subnet: %+v", h)
	}
	if h.Gateway != "192.168.105.1" || h.Source["gateway"] != "firewall" || !slices.Equal(h.DNS, []string{"192.168.105.1"}) {
		t.Fatalf("gateway/dns: %+v", h)
	}
	if h.WAN != "81.82.83.80" {
		t.Fatalf("wan: %q", h.WAN)
	}
	if len(h.Free) < 3 || h.Free[0] != "192.168.105.21" || h.Free[1] != "192.168.105.23" {
		t.Fatalf("vrij: %v", h.Free)
	}
	// .1-.3, .20, .22, netwerk en broadcast niet: 256-2-3-2 = 249.
	if h.FreeCount != 249 {
		t.Fatalf("aantal vrij: %d", h.FreeCount)
	}

	// Zonder firewall: een eerdere uitrol in het subnet, anders .1.
	past := []pastTarget{{FirstIP: "10.0.20.11/24", VLAN: ptr(20), Gateway: "10.0.20.254", DNS: []string{"10.0.20.53"}, Bridge: "vmbr1"}}
	h = networkHint("10.0.20.40/24", nil, past, map[string]bool{}, 1)
	if *h.VLAN != 20 || h.Gateway != "10.0.20.254" || h.DNS[0] != "10.0.20.53" || h.Bridge != "vmbr1" || h.Source["vlan"] != "uitrol" {
		t.Fatalf("eerdere uitrol: %+v", h)
	}
	h = networkHint("10.0.30.40", nil, past, map[string]bool{}, 1)
	if h.Gateway != "10.0.30.1" || h.Source["gateway"] != "regel" || h.VLAN != nil || h.WAN != "" {
		t.Fatalf("regel: %+v", h)
	}
	for dev, want := range map[string]int{"vlan0.40": 40, "vtnet1_vlan30": 30, "vlan12": 12, "igb0.7": 7} {
		if v, ok := vlanOf(dev); !ok || v != want {
			t.Errorf("%s: %d %v", dev, v, ok)
		}
	}
	if _, ok := vlanOf("vtnet1"); ok {
		t.Error("vtnet1 is geen VLAN")
	}
}

func ptr[T any](v T) *T { return &v }
