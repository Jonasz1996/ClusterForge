package opnsense

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const sysXML = `<?xml version="1.0"?>
<opnsense>
  <system><hostname>fw1</hostname><domain>lan.x.be</domain><dnsserver>9.9.9.9</dnsserver><dnsserver>1.1.1.1</dnsserver><timeservers>0.be.pool.ntp.org</timeservers><timezone>Europe/Brussels</timezone></system>
  <interfaces>
    <lan><if>vtnet1</if><descr>LAN</descr><enable>1</enable><ipaddr>192.168.10.2</ipaddr><subnet>24</subnet></lan>
    <wan><if>vtnet0</if><enable>1</enable><ipaddr>dhcp</ipaddr><blockpriv>1</blockpriv></wan>
    <opt2><if>vlan0.90</if><descr>PC</descr><ipaddr>192.168.90.2</ipaddr><subnet>24</subnet></opt2>
    <lo0><if>lo0</if></lo0>
  </interfaces>
</opnsense>`

func TestParseSystem(t *testing.T) {
	var n SysNode
	if err := parseSystem(sysXML, &n); err != nil {
		t.Fatal(err)
	}
	if n.Host != "fw1" || strings.Join(n.DNS, ",") != "9.9.9.9,1.1.1.1" || n.Timezone != "Europe/Brussels" || n.DNSOverride {
		t.Fatalf("systeem: %+v", n)
	}
	if len(n.Interfaces) != 3 || n.Interfaces[0].Key != "wan" || !n.Interfaces[0].BlockPriv || n.Interfaces[2].Enabled || n.NextKey != "opt3" {
		t.Fatalf("interfaces: %+v %s", n.Interfaces, n.NextKey)
	}
	free := freeDevices(json.RawMessage(`{"vtnet0":{},"vtnet1":{},"vtnet2":{},"vlan0.90":{},"vlan0.40":{},"lo0":{},"pfsync0":{},"enc0":{}}`), n.Interfaces)
	if strings.Join(free, ",") != "vlan0.40,vtnet2" {
		t.Fatalf("vrij: %v", free)
	}
}

func TestPlanLegacy(t *testing.T) {
	p, err := planFor(Change{Legacy: []protocol.OPNLegacyEdit{{Section: "interfaces", Key: "opt3", Field: "if", Value: "vtnet2"}, {Section: "system", Field: "hostname", Value: "fw1"}}})
	if err != nil || p.Level != LevelAccess || len(p.Titles) != 2 || p.Titles[0] != "interface opt3: if = vtnet2" {
		t.Fatalf("%v %+v", err, p)
	}
	if _, err := planFor(Change{Legacy: []protocol.OPNLegacyEdit{{Section: "system", Field: "webgui", Value: "x"}}}); err == nil {
		t.Fatal("webgui aanvaard")
	}
	if _, err := planFor(Change{Restore: "config-1763340000.5.xml", Legacy: []protocol.OPNLegacyEdit{{Section: "system", Field: "hostname", Value: "x"}}}); err == nil {
		t.Fatal("terugzetten met velden aanvaard")
	}
}
