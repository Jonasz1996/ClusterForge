package opnsense

import (
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func fwRule(uuid, seq string, f map[string]string) haveObj {
	m := map[string]string{"uuid": uuid, "sequence": seq, "enabled": "1", "interface": "lan", "direction": "in", "ipprotocol": "inet", "protocol": "any", "action": "pass"}
	for k, v := range f {
		m[k] = v
	}
	return haveObj{UUID: uuid, F: m}
}

func TestPlanAdvice(t *testing.T) {
	in := adviceInput{
		Rules: []haveObj{
			fwRule(u1, "10", map[string]string{"source_net": "lan", "destination_net": "any", "description": "LAN naar alles", "log": "1"}),
			fwRule(u2, "20", map[string]string{"source_net": "lan", "destination_net": "192.168.10.50", "protocol": "TCP", "destination_port": "445", "description": "LAN naar NAS"}),
			fwRule(u3, "30", map[string]string{"interface": "opt1", "action": "block", "source_net": "any", "destination_net": "any", "description": "PC blok", "log": "1"}),
			fwRule(u4, "40", map[string]string{"interface": "opt1", "action": "block", "source_net": "any", "destination_net": "any", "description": "PC blok 2"}),
			fwRule(us, "50", map[string]string{"interface": "wan", "source_net": "any", "destination_net": "any", "description": "open huis"}),
		},
		Blocked: []blockedFlow{
			{Iface: "opt1", Src: "192.168.90.20", Dst: "192.168.10.191", Proto: "tcp", Port: 3306, Count: 12, SrcName: "pc"},
			{Iface: "opt1", Src: "192.168.90.20", Dst: "192.168.10.191", Proto: "tcp", Port: 9999, Count: 50},
			{Iface: "opt1", Src: "192.168.90.21", Dst: "192.168.10.192", Proto: "tcp", Port: 22, Count: 2},
			{Iface: "wan", Src: "45.1.2.3", Dst: "192.168.10.191", Proto: "tcp", Port: 3306, Count: 900},
			{Iface: "opt2", Src: "10.0.0.1", Dst: "192.168.10.192", Proto: "tcp", Port: 22, Count: 5},
			{Iface: "opt2", Src: "10.0.0.2", Dst: "192.168.10.192", Proto: "tcp", Port: 22, Count: 5},
			{Iface: "opt2", Src: "10.0.0.3", Dst: "192.168.10.192", Proto: "tcp", Port: 22, Count: 5},
		},
		Listen:    map[string][]string{"192.168.10.191": {"tcp/3306"}, "192.168.10.192": {"tcp/22"}},
		Owner:     map[string]string{"192.168.10.191": "db1", "192.168.10.192": "db2"},
		Hits:      map[string]int64{"LAN naar alles": 100},
		HitsKnown: true,
	}
	items := planAdvice(in)
	m := byKey(items)
	if it := m["shadow:"+u2]; it.Op != "set" || !strings.Contains(it.Detail, "LAN naar alles") {
		t.Fatalf("overschaduwd: %+v", items)
	}
	if it := m["dup:"+u4]; it.Op != "del" || it.call.Path != "firewall/filter/delRule/"+u4 {
		t.Fatalf("dubbel: %+v", items)
	}
	if it := m["wan:"+us]; it.call != nil {
		t.Fatalf("wan: %+v", it)
	}
	if _, ok := m["unused:"+u1]; ok {
		t.Fatal("LAN naar alles raakte wel")
	}
	if _, ok := m["unused:"+u3]; !ok {
		t.Fatalf("PC blok raakte niets: %+v", items)
	}
	it := m["open:opt1|192.168.10.191|tcp|3306"]
	if it.Op != "add" || !strings.Contains(it.Title, "pc → db1 3306/tcp") {
		t.Fatalf("open: %+v", items)
	}
	r := it.call.Body["rule"].(map[string]any)
	if r["source_net"] != "192.168.90.20" || r["protocol"] != "TCP" || r["interface"] != "opt1" {
		t.Fatalf("lichaam: %v", r)
	}
	if r["sequence"] != "29" {
		t.Fatalf("volgorde voor PC blok: %v", r["sequence"])
	}
	if g := m["open:opt2|192.168.10.192|tcp|22"]; g.call == nil || g.call.Body["rule"].(map[string]any)["source_net"] != "opt2" {
		t.Fatalf("drie bronnen: %+v", g)
	}
	for k := range m {
		if strings.Contains(k, "9999") || strings.Contains(k, "wan|") || k == "open:opt1|192.168.10.192|tcp|22" {
			t.Fatalf("onterecht voorstel %s", k)
		}
	}
	for _, it := range items {
		if it.call == nil {
			continue
		}
		pl, err := check([]protocol.OPNCall{*it.call})
		if err != nil || pl.Level != LevelAccess {
			t.Fatalf("%s: %v %v", it.Key, err, pl.Level)
		}
		if it.Kind != "open" && !it.Manual {
			t.Fatalf("%s hoort handmatig", it.Key)
		}
	}
}
