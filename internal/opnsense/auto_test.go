package opnsense

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	u1 = "11111111-1111-4111-8111-111111111111"
	u2 = "22222222-2222-4222-8222-222222222222"
	u3 = "33333333-3333-4333-8333-333333333333"
	u4 = "44444444-4444-4444-8444-444444444444"
	us = "55555555-5555-4555-8555-555555555555"
)

func mustNet(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func byKey(items []AutoItem) map[string]AutoItem {
	m := map[string]AutoItem{}
	for _, it := range items {
		m[it.Key] = it
	}
	return m
}

func TestPlanAuto(t *testing.T) {
	in := autoInput{
		Domain: "lan.x.be",
		Hosts: []autoHost{
			{Name: "db1", Cluster: "db", IP: "192.168.10.191", MAC: "BC:24:11:00:00:01"},
			{Name: "db2", Cluster: "db", IP: "192.168.10.192", MAC: "bc:24:11:00:00:02"},
			{Name: "nc1", Cluster: "Next Cloud", IP: "192.168.10.186", MAC: "bc:24:11:00:00:03"},
			{Name: "far", IP: "10.9.9.9", MAC: "bc:24:11:00:00:09"},
			{Name: "nomac", IP: "192.168.10.50"},
		},
		VIPs: []autoVIP{{Cluster: "db", IP: "192.168.10.180"}},
		Nets: []*net.IPNet{mustNet("192.168.10.2/24")},
	}
	have := autoHave{
		AliasOK: true,
		Aliases: []haveObj{
			{UUID: u1, F: map[string]string{"name": "cf_db", "type": "host", "content": "192.168.10.191,192.168.10.180", "description": "cf: cluster db (ClusterForge)", "enabled": "1"}},
			{UUID: u2, F: map[string]string{"name": "cf_oud", "type": "host", "content": "1.2.3.4", "description": "cf: cluster oud", "enabled": "1"}},
			{UUID: u3, F: map[string]string{"name": "lan_servers", "type": "host", "content": "1.1.1.1"}},
		},
		DNSOK: true,
		DNS: []haveObj{
			{UUID: u1, F: map[string]string{"hostname": "db1", "domain": "lan.x.be", "rr": "A (IPv4 address)", "server": "192.168.10.191", "description": "cf: node db1", "enabled": "1"}},
			{UUID: u2, F: map[string]string{"hostname": "nc1", "domain": "lan.x.be", "rr": "A", "server": "192.168.10.99", "description": "handmatig"}},
			{UUID: u3, F: map[string]string{"hostname": "weg", "domain": "lan.x.be", "rr": "A", "server": "192.168.10.77", "description": "cf: node weg"}},
		},
		KeaOK:   true,
		Subnets: []keaSubnet{{UUID: us, Net: mustNet("192.168.10.0/24")}},
		Kea: []haveObj{
			{UUID: u1, F: map[string]string{"subnet": "192.168.10.0/24", "ip_address": "192.168.10.191", "hw_address": "bc:24:11:00:00:01", "hostname": "db1", "description": "cf: node db1"}},
			{UUID: u2, F: map[string]string{"subnet": "192.168.10.0/24", "ip_address": "192.168.10.186", "hw_address": "aa:aa:aa:aa:aa:aa", "description": "printer"}},
			{UUID: u4, F: map[string]string{"subnet": "192.168.10.0/24", "ip_address": "192.168.10.30", "hw_address": "bc:24:11:00:00:30", "description": "cf: node weg"}},
		},
	}
	st := defaultAuto()
	p := planAuto(in, have, st)
	m := byKey(p.items)

	// Aliassen.
	if it := m["alias:cf_nodes"]; it.Op != "add" || !strings.Contains(it.Detail, "192.168.10.186") {
		t.Fatalf("cf_nodes: %+v", it)
	}
	if it := m["alias:cf_db"]; it.Op != "set" || !strings.Contains(it.Detail, "+192.168.10.192") {
		t.Fatalf("cf_db: %+v", it)
	}
	if it := m["alias:cf_next_cloud"]; it.Op != "add" {
		t.Fatalf("cf_next_cloud: %+v", m)
	}
	if it := m["alias:cf_oud"]; it.Op != "del" || !it.Manual {
		t.Fatalf("cf_oud: %+v", it)
	}
	if _, ok := m["alias:lan_servers"]; ok {
		t.Fatal("een alias zonder cf_ mag ClusterForge niet raken")
	}

	// DNS.
	if _, ok := m["dns:db1.lan.x.be"]; ok {
		t.Fatal("db1 klopt al")
	}
	if p.inOrder["dns"] != 1 {
		t.Fatalf("in orde: %v", p.inOrder)
	}
	if it := m["dns:nc1.lan.x.be"]; it.Op != "conflict" || it.call != nil {
		t.Fatalf("nc1 hoort een conflict te zijn: %+v", it)
	}
	if it := m["dns:db.lan.x.be"]; it.Op != "add" || it.call.Path != "unbound/settings/addHostOverride" {
		t.Fatalf("VIP db: %+v", it)
	}
	if it := m["dns:weg.lan.x.be"]; it.Op != "del" || it.call.Path != "unbound/settings/delHostOverride/"+u3 {
		t.Fatalf("weg: %+v", it)
	}

	// DHCP.
	if _, ok := m["dhcp:bc:24:11:00:00:01"]; ok {
		t.Fatal("reservatie db1 klopt al")
	}
	it := m["dhcp:bc:24:11:00:00:02"]
	if it.Op != "add" || it.call.Path != "kea/dhcpv4/addReservation" {
		t.Fatalf("db2: %+v", it)
	}
	if r := it.call.Body["reservation"].(map[string]any); r["subnet"] != us || r["hw_address"] != "bc:24:11:00:00:02" {
		t.Fatalf("lichaam: %v", r)
	}
	if it := m["dhcp:bc:24:11:00:00:03"]; it.Op != "conflict" {
		t.Fatalf("nc1 heeft het IP van de printer: %+v", it)
	}
	if it := m["dhcp:kea:"+u4]; it.Op != "del" {
		t.Fatalf("oude reservatie: %+v", it)
	}
	if _, ok := m["dhcp:bc:24:11:00:00:09"]; ok {
		t.Fatal("far zit niet in een net van de firewall")
	}
	if len(p.notes) != 2 {
		t.Fatalf("notities: %v", p.notes)
	}

	// Elk voorstel past door de policy.
	for _, it := range p.items {
		if it.call == nil {
			continue
		}
		if _, err := check(nil); err == nil {
			t.Fatal("lege wijziging hoort te falen")
		}
		pl, err := check([]protocol.OPNCall{*it.call})
		if err != nil {
			t.Fatalf("%s: %v", it.Key, err)
		}
		if pl.Level != LevelNormal {
			t.Fatalf("%s: niveau %s", it.Key, pl.Level)
		}
	}

	// Negeren, handmatig en beheerd.
	st.Ignored = []string{"alias:cf_nodes"}
	st.DNS = ModeManaged
	p = planAuto(in, have, st)
	calls, keys := pick(p.items, nil, st, true)
	for _, k := range keys {
		if !strings.HasPrefix(k, "dns:") {
			t.Fatalf("alleen DNS is beheerd: %v", keys)
		}
	}
	if len(calls) != 5 {
		t.Fatalf("DNS: vier toevoegen en weg verwijderen, kreeg %v", keys)
	}
	_, keys = pick(p.items, nil, st, false)
	for _, k := range keys {
		if k == "alias:cf_nodes" || k == "alias:cf_oud" {
			t.Fatalf("genegeerd of handmatig toegepast: %v", keys)
		}
	}
	_, keys = pick(p.items, []string{"alias:cf_oud"}, st, false)
	if len(keys) != 1 {
		t.Fatalf("gevraagde sleutel: %v", keys)
	}

	// Uit: niets.
	st = AutoSettings{Aliases: ModeOff, DNS: ModeOff, DHCP: ModeOff}
	if p := planAuto(in, have, st); len(p.items) != 0 {
		t.Fatalf("uit: %v", p.items)
	}
}

func TestPlanAutoDnsmasq(t *testing.T) {
	in := autoInput{
		Domain: "lan.x.be",
		Hosts:  []autoHost{{Name: "plex1.home", IP: "192.168.20.10", MAC: "bc:24:11:00:00:10"}},
		Nets:   []*net.IPNet{mustNet("192.168.20.1/24")},
	}
	have := autoHave{DnsmasqOK: true, Ranges: []net.IP{net.ParseIP("192.168.20.100")},
		Dnsmasq: []haveObj{{UUID: u1, F: map[string]string{"host": "plex1", "ip": "192.168.20.11", "hwaddr": "bc:24:11:00:00:10", "descr": "cf: node plex1.home"}}}}
	p := planAuto(in, have, AutoSettings{Aliases: ModeOff, DNS: ModeOff, DHCP: ModePreview})
	it := byKey(p.items)["dhcp:bc:24:11:00:00:10"]
	if it.Op != "set" || it.call.Path != "dnsmasq/settings/setHost/"+u1 {
		t.Fatalf("dnsmasq: %+v", p.items)
	}
	if _, err := check([]protocol.OPNCall{*it.call}); err != nil {
		t.Fatal(err)
	}
}

func TestAutoHelpers(t *testing.T) {
	if aliasName("Next Cloud-Prod") != "cf_next_cloud_prod" {
		t.Fatal(aliasName("Next Cloud-Prod"))
	}
	if n := aliasName(strings.Repeat("a", 40)); len(n) != 32 {
		t.Fatal(n)
	}
	if dnsLabel("Plex_1.lan") != "plex-1" {
		t.Fatal(dnsLabel("Plex_1.lan"))
	}
	s := AutoSettings{Aliases: "beheerd", DNS: "uit", DHCP: "voorbeeld", Domain: " Lan.X.be. "}
	if err := s.validate(); err != nil || s.Domain != "lan.x.be" {
		t.Fatal(err, s.Domain)
	}
	s.DNS = "aan"
	if s.validate() == nil {
		t.Fatal("ongeldige stand")
	}
	rows, err := parseRows(json.RawMessage(`{"rows":[{"uuid":"` + u1 + `","enabled":"1","n":3,"l":["a","b"],"x":null}]}`))
	if err != nil || rows[0].UUID != u1 || rows[0].F["n"] != "3" || rows[0].F["l"] != "a,b" {
		t.Fatal(rows, err)
	}
}
