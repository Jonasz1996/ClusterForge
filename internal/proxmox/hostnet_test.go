package proxmox

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseHostNetwork(t *testing.T) {
	// Zoals Proxmox VE 8 het geeft: getallen soms als tekst, vlaggen als 1.
	var raw []map[string]any
	err := json.Unmarshal([]byte(`[
		{"iface": "vmbr0", "type": "bridge", "active": 1, "bridge_ports": "bond0", "bridge_vlan_aware": 1,
		 "cidr": "192.168.1.11/24", "gateway": "192.168.1.1", "mtu": "9000", "comments": "LAN\n"},
		{"iface": "bond0", "type": "bond", "active": 1, "slaves": "eno1 eno2", "bond_mode": "802.3ad"},
		{"iface": "eno1", "type": "eth", "active": 1, "exists": 1},
		{"iface": "vmbr0.20", "type": "vlan", "active": 1, "address": "10.0.20.2", "netmask": "24"},
		{"iface": "vlan30", "type": "vlan", "vlan-id": "30", "vlan-raw-device": "bond0"},
		{"iface": "vmbr9", "type": "OVSBridge", "ovs_ports": "ovs1"},
		{"type": "eth"}
	]`), &raw)
	if err != nil {
		t.Fatal(err)
	}
	got := ParseHostNetwork(raw)
	want := []HostIface{
		{Iface: "bond0", Type: "bond", Active: true, Ports: []string{"eno1", "eno2"}},
		{Iface: "eno1", Type: "eth", Active: true},
		{Iface: "vlan30", Type: "vlan", VLANID: 30, VLANRaw: "bond0"},
		{Iface: "vmbr0", Type: "bridge", Active: true, Ports: []string{"bond0"}, VLANAware: true, CIDR: "192.168.1.11/24",
			Gateway: "192.168.1.1", MTU: 9000, Comments: "LAN"},
		{Iface: "vmbr0.20", Type: "vlan", Active: true, VLANID: 20, VLANRaw: "vmbr0", CIDR: "10.0.20.2/24"},
		{Iface: "vmbr9", Type: "OVSBridge", Ports: []string{"ovs1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	if !got[3].Bridge() || !got[5].Bridge() || got[0].Bridge() {
		t.Fatal("Bridge()")
	}
}

func TestOptionOr(t *testing.T) {
	for _, c := range []struct{ v, name, def, want string }{
		{"local-lvm:vm-1-disk-0,size=32G", "size", "", "32G"},
		{"current,serial=x", "current", "current", "current"},
		{"enabled=1", "enabled", "enabled", "1"},
		{"1", "enabled", "enabled", "1"},
		{"virtio=BC:24:11:00:00:01,bridge=vmbr0", "bridge", "", "vmbr0"},
	} {
		if got, _ := OptionOr(c.v, c.name, c.def); got != c.want {
			t.Errorf("OptionOr(%q, %q, %q) = %q, wil %q", c.v, c.name, c.def, got, c.want)
		}
	}
	if got := SetOption("virtio=x,bridge=vmbr0", "link_down", "1"); got != "virtio=x,bridge=vmbr0,link_down=1" {
		t.Fatal(got)
	}
	if got := SetOption("virtio=x,link_down=0", "link_down", "1"); got != "virtio=x,link_down=1" {
		t.Fatal(got)
	}
}
