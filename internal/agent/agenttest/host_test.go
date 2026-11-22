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

// TestGroupTrackDB: met track_script cf_db staat het VIP alleen op een
// node waarvan MariaDB mag schrijven; het script staat in conf.d.
func TestGroupTrackDB(t *testing.T) {
	const a, b = "10.0.20.11", "10.0.20.12"
	g := &Group{}
	h1, h2 := machine(t, g, a), machine(t, g, b)
	galera := func() *MariaDB {
		return &MariaDB{Galera: &Galera{Status: "Primary", State: 4, Comment: "Synced", Ready: true}}
	}
	h1.MariaDB, h2.MariaDB = galera(), galera()
	for _, h := range []*Host{h1, h2} {
		p := filepath.Join(h.Root, "etc/keepalived/conf.d/clusterforge-db.conf")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("vrrp_script cf_db {\n    script \"/usr/local/bin/cf-agent check db\"\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tracked := func(src string, prio int, peer string) string {
		return "include /etc/keepalived/conf.d/*.conf\n" +
			strings.Replace(conf(src, prio, peer), "    virtual_ipaddress {", "    track_script {\n        cf_db\n    }\n    virtual_ipaddress {", 1)
	}
	h1.setConf(t, tracked(a, 150, b))
	h2.setConf(t, tracked(b, 140, a))
	if got := holders(h1, h2); !slices.Equal(got, []string{a}) {
		t.Fatalf("gezond: %v", got)
	}
	h1.Locked(func() { h1.MariaDB.Galera.State, h1.MariaDB.Galera.Comment = 2, "Donor/Desynced" })
	if got := holders(h1, h2); !slices.Equal(got, []string{b}) {
		t.Fatalf("nc1 Donor: %v", got)
	}
	h1.Locked(func() { h1.MariaDB.Galera.State = 4 })
	if got := holders(h1, h2); !slices.Equal(got, []string{a}) {
		t.Fatalf("nc1 terug: %v", got)
	}
	h1.SetUnit("mariadb", Unit{Enabled: true})
	if got := holders(h1, h2); !slices.Equal(got, []string{b}) {
		t.Fatalf("mariadb gestopt: %v", got)
	}

	// keepalived -t: het script moet vóór de instance staan.
	p := filepath.Join(h1.Root, "etc/keepalived/test.conf")
	for c, ok := range map[string]bool{
		tracked(a, 150, b): true,
		strings.TrimPrefix(tracked(a, 150, b), "include /etc/keepalived/conf.d/*.conf\n") + "include /etc/keepalived/conf.d/*.conf\n": false,
		conf(a, 150, b) + "}\n": false,
	} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := h1.Exec(t.Context(), "keepalived", "-t", "-f", p)
		if (err == nil) != ok {
			t.Errorf("keepalived -t: %v %s\n%s", err, out, c)
		}
	}
}
