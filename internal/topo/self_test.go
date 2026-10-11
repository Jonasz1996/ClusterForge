package topo

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/proxmox"
)

func TestLocate(t *testing.T) {
	conn := uuid.New()
	guest := func(vmid int, mac string, addrs ...string) Guest {
		return Guest{Conn: conn, VMID: vmid, Type: "lxc", Host: "pve1", Config: &Config{
			NICs: []proxmox.NIC{{Key: "net0", Bridge: "vmbr0", MAC: mac}}, Addresses: addrs,
		}}
	}
	m := Model{Guests: []Guest{
		guest(101, "bc:24:11:00:00:01", "192.168.1.10"),
		guest(105, "bc:24:11:00:00:05", "192.168.1.50"),
		guest(106, "bc:24:11:00:00:06", "192.168.1.50"),
		{Conn: conn, VMID: 9000, Template: true, Config: &Config{NICs: []proxmox.NIC{{MAC: "bc:24:11:00:00:09"}}}},
	}}
	for _, tc := range []struct {
		name string
		own  Own
		want Self
	}{
		{"mac", Own{MACs: []string{"02:42:ac:11:00:02", "bc:24:11:00:00:05"}, IPs: []string{"192.168.1.10"}},
			Self{Source: "auto", Conn: conn, VMID: 105, MatchedOn: "MAC-adres bc:24:11:00:00:05"}},
		{"ip", Own{MACs: []string{"02:42:ac:11:00:02"}, IPs: []string{"172.17.0.2", "192.168.1.10"}},
			Self{Source: "auto", Conn: conn, VMID: 101, MatchedOn: "IP-adres 192.168.1.10"}},
		{"twee met hetzelfde adres", Own{IPs: []string{"192.168.1.50"}},
			Self{Note: "Meer dan één VM heeft een adres van ClusterForge. Kies de juiste bij Instellingen."}},
		{"template telt niet", Own{MACs: []string{"bc:24:11:00:00:09"}}, Self{}},
	} {
		got := Locate(m, tc.own, nil)
		if tc.name == "template telt niet" {
			if got.Found() || got.Note == "" {
				t.Errorf("%s: %+v", tc.name, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%s: %+v, wil %+v", tc.name, got, tc.want)
		}
	}
	if got := Locate(m, Own{MACs: []string{"bc:24:11:00:00:05"}}, &Manual{Conn: conn, VMID: 101}); got.Source != "manual" || got.VMID != 101 {
		t.Errorf("ingevuld wint: %+v", got)
	}
	unread := Model{Guests: []Guest{{Conn: conn, VMID: 101}}}
	if got := Locate(unread, Own{}, nil); got.Found() || got.Note != "De configuratie van de VM's is nog niet gelezen; dat gebeurt binnen tien minuten na de koppeling." {
		t.Errorf("niet gelezen: %+v", got)
	}
	if got := Locate(Model{}, Own{}, nil); got.Found() || got.Note == "" {
		t.Errorf("zonder Proxmox: %+v", got)
	}
}

func TestHintHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://cf.lan:8443/pad": "cf.lan",
		"cf.lan:4222":             "cf.lan",
		"cf.lan":                  "cf.lan",
		"192.168.1.50":            "192.168.1.50",
		"[fd00::5]:4222":          "fd00::5",
		"http://[fd00::5]":        "fd00::5",
	} {
		if got := hintHost(in); got != want {
			t.Errorf("%s: %q, wil %q", in, got, want)
		}
	}
	own := OwnAddresses(t.Context(), []string{"192.168.1.50:4222", "https://10.0.0.7"})
	for _, ip := range []string{"192.168.1.50", "10.0.0.7"} {
		found := false
		for _, o := range own.IPs {
			found = found || o == ip
		}
		if !found {
			t.Errorf("hint %s ontbreekt: %v", ip, own.IPs)
		}
	}
}
