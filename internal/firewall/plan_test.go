package firewall

import (
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var testSettings = PlanSettings{LAN: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}, SSHFrom: ClassLAN, Unknown: ClassLAN, Strict: true}

func ruleStrings(p Plan) []string {
	var out []string
	for _, r := range p.Rules {
		out = append(out, strings.Join(r.Args(), " "))
	}
	return out
}

func findEntry(p Plan, proto, port string) *Entry {
	for i := range p.Entries {
		if p.Entries[i].Proto == proto && p.Entries[i].Port == port {
			return &p.Entries[i]
		}
	}
	return nil
}

// De Authentik-node .243 van Jonas: etcd en Patroni alleen voor de andere
// nodes, VRRP van elk clusterlid in plaats van 192.168.0.0/24.
func TestBuildAuthentik(t *testing.T) {
	st := &protocol.FirewallStatus{Listeners: []protocol.FirewallListener{
		{Proto: "tcp", Addr: "0.0.0.0", Port: 22, Process: "sshd", Unit: "ssh"},
		{Proto: "tcp", Addr: "127.0.0.1", Port: 8080, Process: "crowdsec", Unit: "crowdsec"},
		{Proto: "tcp", Addr: "0.0.0.0", Port: 9000, Process: "authentik", Container: "authentik-server-1", Image: "ghcr.io/goauthentik/server:2025.6.2"},
		{Proto: "tcp", Addr: "0.0.0.0", Port: 6379, Process: "redis-server", Image: "docker.io/library/redis:7-alpine"},
	}}
	in := Input{Template: "authentik", Status: st, Units: []string{"patroni", "etcd", "keepalived", "cf-authentik", "ssh"},
		Keepalived: true, Peers: []string{"192.168.103.242", "192.168.103.241"}, VIPs: []string{"192.168.103.244"}, Settings: testSettings}
	for _, l := range st.Listeners {
		in.Ports = append(in.Ports, Port{Proto: l.Proto, Port: l.Port, Addr: l.Addr, Process: l.Process, Image: l.Image, Listening: true})
	}
	p := Build(in)
	rules := ruleStrings(p)
	for _, want := range []string{
		"allow proto tcp from 192.168.0.0/16 to any port 22",
		"allow proto tcp from 192.168.103.242 to any port 2379",
		"allow proto tcp from 192.168.103.244 to any port 2380",
		"allow proto tcp from 192.168.103.241 to any port 8008",
		"allow proto tcp from 192.168.0.0/16 to any port 5432",
		"allow proto tcp from 192.168.103.242 to any port 6379",
		"allow proto tcp from 192.168.0.0/16 to any port 9000",
		"allow proto vrrp from 192.168.103.241 to any",
	} {
		if !slices.Contains(rules, want) {
			t.Errorf("geen %q in\n%s", want, strings.Join(rules, "\n"))
		}
	}
	for _, r := range rules {
		if strings.Contains(r, "8080") || strings.Contains(r, "192.168.0.0/24") {
			t.Errorf("onverwacht: %s", r)
		}
		if strings.Contains(r, "port 2379") && strings.Contains(r, "192.168.0.0/16") {
			t.Errorf("etcd voor het hele LAN: %s", r)
		}
	}
	// 172.16.0.0/12 voor PostgreSQL vanuit de containers valt al onder het LAN.
	if slices.Contains(rules, "allow proto tcp from 172.16.0.0/12 to any port 5432") && slices.Contains(rules, "allow proto tcp from 10.0.0.0/8 to any port 5432") {
		n := 0
		for _, r := range rules {
			if r == "allow proto tcp from 172.16.0.0/12 to any port 5432" {
				n++
			}
		}
		if n > 1 {
			t.Errorf("dubbel")
		}
	}
	for _, r := range p.Rules {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", r, err)
		}
	}
	if e := findEntry(p, "tcp", "6379"); e == nil || e.Software != "Redis" || e.Class != ClassCluster {
		t.Errorf("redis: %+v", e)
	}
	if len(p.Warnings) != 0 || p.Hash == "" {
		t.Errorf("waarschuwingen %v, hash %q", p.Warnings, p.Hash)
	}
	if Build(in).Hash != p.Hash {
		t.Error("hash niet stabiel")
	}
}

func TestBuildDockerAndUnknown(t *testing.T) {
	st := &protocol.FirewallStatus{
		Listeners: []protocol.FirewallListener{
			{Proto: "tcp", Addr: "0.0.0.0", Port: 22, Process: "sshd"},
			{Proto: "tcp", Addr: "0.0.0.0", Port: 81, Process: "docker-proxy"},
			{Proto: "tcp", Addr: "::", Port: 5555, Process: "mystery"},
		},
		Containers: []protocol.FirewallContainer{{Name: "npm", Image: "jc21/nginx-proxy-manager:2.12.3",
			Ports: []protocol.FirewallPublished{{Proto: "tcp", HostIP: "0.0.0.0", HostPort: 81, ContainerPort: 81}}}},
	}
	in := Input{Status: st, Settings: testSettings}
	for _, l := range st.Listeners {
		in.Ports = append(in.Ports, Port{Proto: l.Proto, Port: l.Port, Addr: l.Addr, Process: l.Process, Listening: true})
	}
	p := Build(in)
	if e := findEntry(p, "tcp", "81"); e == nil || e.State != "docker" || e.Software != "Nginx Proxy Manager" {
		t.Errorf("docker: %+v", e)
	}
	if e := findEntry(p, "tcp", "5555"); e == nil || !e.Unknown || e.State != "open" || len(p.Warnings) != 1 {
		t.Errorf("onbekend: %+v %v", e, p.Warnings)
	}
	if len(p.Rules) != 6 { // ssh en 5555, elk voor drie netwerken
		t.Errorf("regels: %v", ruleStrings(p))
	}
	in.Settings.Unknown = "closed"
	in.Custom = []Custom{{Action: "open", Proto: "tcp", Port: "8443", Source: "203.0.113.0/24", Comment: "partner"}, {Action: "close", Proto: "tcp", Port: "22"}}
	p = Build(in)
	if e := findEntry(p, "tcp", "5555"); e.State != "closed" {
		t.Errorf("onbekend dicht: %+v", e)
	}
	rules := ruleStrings(p)
	if !slices.Contains(rules, "allow proto tcp from 203.0.113.0/24 to any port 8443") || !slices.Contains(rules, "allow proto tcp from 10.0.0.0/8 to any port 22") {
		t.Errorf("eigen regels (SSH blijft): %v", rules)
	}
}

func TestBuildLoneNodeClusterPort(t *testing.T) {
	p := Build(Input{Units: []string{"etcd"}, Settings: testSettings})
	if e := findEntry(p, "tcp", "2379"); e == nil || e.State != "closed" || e.Note == "" {
		t.Errorf("etcd zonder cluster: %+v", e)
	}
}

func TestMinimal(t *testing.T) {
	got := minimal([]string{"192.168.103.242", "192.168.0.0/16", "192.168.103.242", "10.0.0.5", "fd00::1"})
	if strings.Join(got, ",") != "192.168.0.0/16,10.0.0.5,fd00::1" {
		t.Errorf("%v", got)
	}
	if strings.Join(minimal([]string{"10.0.0.0/8", "any"}), ",") != "any" {
		t.Error("any")
	}
}
