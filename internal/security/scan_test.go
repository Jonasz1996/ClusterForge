package security

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestNextRun(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Brussels")
	now := time.Date(2026, 11, 16, 22, 30, 0, 0, loc) // maandag
	if n := nextRun(ScheduleWeekly, 0, 2, now); n == nil || !n.Equal(time.Date(2026, 11, 22, 2, 0, 0, 0, loc)) {
		t.Errorf("wekelijks = %v", n)
	}
	if n := nextRun(ScheduleDaily, 0, 23, now); n == nil || !n.Equal(time.Date(2026, 11, 16, 23, 0, 0, 0, loc)) {
		t.Errorf("dagelijks = %v", n)
	}
	if n := nextRun(ScheduleOff, 0, 2, now); n != nil {
		t.Errorf("uit = %v", n)
	}
}

func TestAnalyzeHosts(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	kn := &knownNode{ID: uuid.New(), Hostname: "web01", listen: map[string]string{"tcp/22": "sshd"},
		plan: []planEntry{{Proto: "tcp", Port: "8000:8010", State: "open"}, {Proto: "tcp", Port: "9000", State: "closed"}}, managed: true}
	res := &protocol.ScanResult{Hosts: []protocol.ScanHost{
		{Addr: "192.168.10.5", Ports: []protocol.ScanPort{
			{Proto: "tcp", Port: 22, Scripts: map[string]string{"ssh2-enum-algos": "  encryption_algorithms: (2)\n    aes128-cbc\n    chacha20-poly1305@openssh.com"}},
			{Proto: "tcp", Port: 8005},
			{Proto: "tcp", Port: 9000},
			{Proto: "tcp", Port: 443, Scripts: map[string]string{
				"ssl-enum-ciphers": "  TLSv1.0: \n    ciphers: x\n  TLSv1.2: \n  least strength: A",
				"ssl-cert":         "Subject: commonName=x\nNot valid after:  2026-11-01T00:00:00"}},
		}},
		{Addr: "192.168.10.9", Ports: []protocol.ScanPort{{Proto: "tcp", Port: 6379}}},
	}}
	hosts, drafts, open := analyzeHosts(res, map[string]*knownNode{"192.168.10.5": kn}, nil, now)
	if open != 5 || len(hosts) != 2 || hosts[0].Node != "web01" || hosts[1].NodeID != nil {
		t.Fatalf("hosts = %+v", hosts)
	}
	want := []string{"luistert", "firewall", "dicht", "onbekend"}
	for i, w := range want {
		if hosts[0].Expected[i].Known != w {
			t.Errorf("poort %d: %q, wil %q", hosts[0].Expected[i].Port, hosts[0].Expected[i].Known, w)
		}
	}
	keys := map[string]string{}
	for _, d := range drafts["192.168.10.5"] {
		keys[d.Key] = d.Severity
	}
	for k, sev := range map[string]string{"ssh-algos:22/tcp": SevLow, "firewall:9000/tcp": SevHigh, "onbekend:443/tcp": SevMedium,
		"tls-versie:443/tcp": SevMedium, "cert:443/tcp": SevMedium} {
		if keys[k] != sev {
			t.Errorf("%s = %q, wil %q (alle %v)", k, keys[k], sev, keys)
		}
	}
	if d := drafts["192.168.10.9"]; len(d) != 1 || d[0].Key != "poort:6379/tcp" {
		t.Errorf("onbekende host = %+v", d)
	}
}

func TestProbeTargetsAndDrafts(t *testing.T) {
	res := &protocol.ScanResult{Hosts: []protocol.ScanHost{{Addr: "192.168.10.180", Ports: []protocol.ScanPort{
		{Proto: "tcp", Port: 22, Service: "ssh"},
		{Proto: "tcp", Port: 7000, Service: "http"},
		{Proto: "tcp", Port: 443, Service: "http", Tunnel: "ssl"},
		{Proto: "tcp", Port: 6379, Service: "redis"},
	}}}}
	got := probeTargets(res)
	if len(got) != 3 || got[0].Kind != "http" || got[1].Kind != "https" || got[2].Kind != "redis" {
		t.Fatalf("doelen = %+v", got)
	}
	probes := []protocol.ProbeResult{{Addr: "192.168.10.180", Port: 7000, Kind: "http", Title: "Statistics Report for HAProxy",
		Hits: []protocol.ProbeHit{{ID: "haproxy-admin", Detail: "statistiekpagina met beheeracties, zonder login"}}}}
	hosts, drafts, _ := analyzeHosts(res, nil, probes, time.Now())
	if hosts[0].Expected[1].Title == "" || len(hosts[0].Expected[1].Hits) != 1 {
		t.Errorf("verwacht = %+v", hosts[0].Expected[1])
	}
	found := false
	for _, d := range drafts["192.168.10.180"] {
		if d.Key == "dienst:haproxy-admin:7000/tcp" && d.Severity == SevHigh {
			found = true
		}
	}
	if !found {
		t.Errorf("geen HAProxy-bevinding in %+v", drafts)
	}
}
