package agenttest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// conf is een keepalived-configuratie met unicast, zoals keepalived-nginx
// haar rendert.
func conf(src string, prio int, peers ...string) string {
	return fmt.Sprintf(`vrrp_instance VI_51 {
    priority %d
    unicast_src_ip %s
    unicast_peer {
        %s
    }
    virtual_ipaddress {
        10.0.20.100/24 dev eth0
    }
}
`, prio, src, strings.Join(peers, "\n        "))
}

func machine(t *testing.T, g *Group, addr string) *Host {
	t.Helper()
	h := NewHost(t.TempDir(), addr)
	g.Join(h)
	return h
}

func (h *Host) setConf(t *testing.T, c string) {
	t.Helper()
	p := filepath.Join(h.Root, "etc/keepalived/keepalived.conf")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(c), 0o640); err != nil {
		t.Fatal(err)
	}
	// Een reload laat keepalived de configuratie opnieuw lezen.
	h.SetUnit("keepalived", Unit{Active: true, Enabled: true})
}

func holders(hosts ...*Host) []string {
	var out []string
	for _, h := range hosts {
		if slices.Contains(h.Addresses()[1:], "10.0.20.100") {
			out = append(out, h.Address)
		}
	}
	return out
}

// TestGroupUnicast: een nieuwe node die start voor de andere hem als peer
// kennen, hoort de houder niet en neemt het VIP er ook bij. Kennen de
// bestaande nodes hem eerst, dan blijft er één houder.
func TestGroupUnicast(t *testing.T) {
	const a, b, c = "10.0.20.11", "10.0.20.12", "10.0.20.13"
	for _, tc := range []struct {
		name   string
		order  []string
		splits int
	}{
		{"nieuwe node eerst", []string{c, b, a}, 1},
		{"bestaande nodes eerst", []string{b, a, c}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &Group{}
			hosts := map[string]*Host{a: machine(t, g, a), b: machine(t, g, b), c: machine(t, g, c)}
			hosts[a].setConf(t, conf(a, 150, b))
			hosts[b].setConf(t, conf(b, 140, a))
			if got := holders(hosts[a], hosts[b], hosts[c]); !slices.Equal(got, []string{a}) {
				t.Fatalf("twee nodes: %v", got)
			}
			prio := map[string]int{a: 150, b: 140, c: 130}
			for _, addr := range tc.order {
				var peers []string
				for _, p := range []string{a, b, c} {
					if p != addr {
						peers = append(peers, p)
					}
				}
				hosts[addr].setConf(t, conf(addr, prio[addr], peers...))
			}
			if got := holders(hosts[a], hosts[b], hosts[c]); !slices.Equal(got, []string{a}) {
				t.Fatalf("na het schalen: %v", got)
			}
			if got := g.Splits(); len(got) != tc.splits {
				t.Fatalf("split-brain: %v", got)
			}
			if tc.splits > 0 && g.Splits()[0] != "10.0.20.100 op 10.0.20.11 en 10.0.20.13" {
				t.Fatalf("split-brain: %v", g.Splits())
			}
		})
	}
	// Zonder unicast_peer hoort iedereen iedereen.
	g := &Group{}
	h1, h2 := machine(t, g, a), machine(t, g, b)
	h1.setConf(t, "vrrp_instance X {\n priority 150\n virtual_ipaddress {\n  10.0.20.100/24\n }\n}\n")
	h2.setConf(t, "vrrp_instance X {\n priority 140\n virtual_ipaddress {\n  10.0.20.100/24\n }\n}\n")
	if got := holders(h1, h2); !slices.Equal(got, []string{a}) || len(g.Splits()) != 0 {
		t.Fatalf("multicast: %v %v", got, g.Splits())
	}
}
