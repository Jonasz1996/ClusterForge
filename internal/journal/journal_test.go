package journal

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestDescribe(t *testing.T) {
	for _, tc := range []struct {
		e    Entry
		want string
	}{
		{Entry{Kind: KindPackage, Subject: "nginx", Action: "upgrade", From: "1.24.0-1", To: "1.24.0-2", By: "root"},
			"nginx bijgewerkt van 1.24.0-1 naar 1.24.0-2 (root)"},
		{Entry{Kind: KindPackage, Subject: "htop", Action: "install", To: "3.2.2-2", Detail: map[string]string{"via": "dpkg"}},
			"htop 3.2.2-2 geïnstalleerd (via dpkg)"},
		{Entry{Kind: KindPackage, Subject: "nginx", Action: "install", By: "clusterforge"}, "nginx geïnstalleerd (ClusterForge)"},
		{Entry{Kind: KindPackage, Subject: "telnet", Action: "purge", From: "0.17"}, "telnet 0.17 met configuratie verwijderd"},
		{Entry{Kind: KindBoot, Action: "boot", From: "6.1.0-25-amd64", To: "6.1.0-26-amd64"},
			"Herstart, kernel van 6.1.0-25-amd64 naar 6.1.0-26-amd64"},
		{Entry{Kind: KindBoot, Action: "boot", To: "6.1.0-26-amd64"}, "Herstart, kernel 6.1.0-26-amd64"},
		{Entry{Kind: KindBoot, Action: "requested", By: "clusterforge"}, "Herstart gevraagd door ClusterForge"},
		{Entry{Kind: KindService, Subject: "nginx.service", Action: "failed", Detail: map[string]string{"result": "exit-code"}},
			"nginx.service mislukt: afgesloten met een fout"},
		{Entry{Kind: KindService, Subject: "nginx.service", Action: "auto_restarted", Detail: map[string]string{"restarts": "3"}},
			"nginx.service automatisch herstart door systemd (3 keer sinds de laatste start)"},
		{Entry{Kind: KindService, Subject: "keepalived", Action: "stopped", By: "clusterforge", Detail: map[string]string{"reason": "onderhoud"}},
			"keepalived gestopt door ClusterForge (onderhoud)"},
		{Entry{Kind: KindFile, Subject: "/etc/nginx/nginx.conf", Action: "modified"}, "/etc/nginx/nginx.conf aangepast"},
		{Entry{Kind: KindFile, Subject: "/etc/nginx/nginx.conf", Action: "written", By: "clusterforge"},
			"/etc/nginx/nginx.conf geschreven door ClusterForge"},
		{Entry{Kind: KindFile, Subject: "/etc/ssh/sshd_config", Action: "mode", From: "0644", To: "0666"},
			"Rechten van /etc/ssh/sshd_config van 0644 naar 0666"},
		{Entry{Kind: KindFile, Subject: "/etc/nginx", Action: "changed", Detail: map[string]string{"count": "12"}},
			"12 bestanden aangepast in /etc/nginx"},
		{Entry{Kind: KindLogin, Subject: "root", Action: "login", Detail: map[string]string{"ip": "10.0.0.5", "method": "publickey"}},
			"SSH-login als root vanaf 10.0.0.5 (sleutel)"},
		{Entry{Kind: KindOOM, Subject: "php-fpm8.2", Action: "killed"}, "php-fpm8.2 gestopt door geheugentekort (OOM-killer)"},
		{Entry{Kind: KindVM, Subject: "101", Action: "qmstop", By: "root@pam", Detail: map[string]string{"status": "OK"}},
			"VM 101 hard gestopt in Proxmox door root@pam"},
		{Entry{Kind: KindVM, Subject: "101", Action: "qmstart", By: "clusterforge@pve!cf", Detail: map[string]string{"clusterforge": "true"}},
			"VM 101 gestart in Proxmox door ClusterForge"},
		{Entry{Kind: KindVM, Subject: "101", Action: "vzdump", By: "root@pam", Detail: map[string]string{"status": "job errors"}},
			"Back-up van VM 101 in Proxmox door root@pam, mislukt: job errors"},
		{Entry{Kind: KindVM, Subject: "101", Action: "qmigrate", By: "root@pam", Detail: map[string]string{"host": "pve1", "status": "OK"}},
			"VM 101 verhuisd (van pve1) in Proxmox door root@pam"},
		{Entry{Kind: KindAgent, Action: "upgrade", From: "0.4.0", To: "0.5.0"}, "Agent bijgewerkt van 0.4.0 naar 0.5.0"},
		{Entry{Kind: KindAgent, Action: "gap", Detail: map[string]string{"seconds": "330"}}, "Geen contact met de agent gedurende 5 min"},
		{Entry{Kind: KindFacts, Subject: "updates", Action: "changed", From: "3", To: "12", Detail: map[string]string{"security": "2"}},
			"12 updates open, waarvan 2 voor beveiliging (was 3)"},
		{Entry{Kind: KindNetwork, Subject: "eth0", Action: "addresses", Detail: map[string]string{"added": "10.0.0.6/24", "removed": "10.0.0.5/24"}},
			"Adressen van eth0: 10.0.0.6/24 erbij, 10.0.0.5/24 weg"},
		{Entry{Kind: KindNetwork, Subject: "gateway", Action: "gateway", From: "10.0.0.1"}, "Standaardgateway van 10.0.0.1 naar geen"},
		{Entry{Kind: KindUser, Subject: "app", Action: "created", By: "clusterforge"}, "Gebruiker app aangemaakt door ClusterForge"},
		{Entry{Kind: KindCommand, Subject: "/opt/app/.ready", Action: "ran", By: "clusterforge"},
			"Commando uitgevoerd (maakt /opt/app/.ready) door ClusterForge"},
	} {
		if got := Describe(tc.e, false); got != tc.want {
			t.Errorf("%s %s:\n kreeg %q\n wilde %q", tc.e.Kind, tc.e.Action, got, tc.want)
		}
	}
	login := Entry{Kind: KindLogin, Subject: "root", Action: "login", Detail: map[string]string{"ip": "10.0.0.5", "method": "password"}}
	if got := Describe(login, true); got != "SSH-login als root (wachtwoord)" {
		t.Errorf("voor een viewer: %q", got)
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second:               "45 s",
		5*time.Minute + 30*time.Second: "5 min",
		2 * time.Hour:                  "2 u",
		2*time.Hour + 10*time.Minute:   "2 u 10 min",
		72 * time.Hour:                 "3 dagen",
	} {
		if got := Duration(d); got != want {
			t.Errorf("%s: %q, wilde %q", d, got, want)
		}
	}
}

func TestSanitize(t *testing.T) {
	e := Sanitize(Entry{
		Subject: "deb https://jonas:geheim@apt.example/debian\x1b[31m",
		Action:  "install",
		To:      strings.Repeat("é", 300),
		Detail: map[string]string{
			"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8", "i": "9", "j": "10",
		},
	})
	if strings.Contains(e.Subject, "geheim") || strings.Contains(e.Subject, "jonas") || strings.ContainsRune(e.Subject, 0x1b) {
		t.Errorf("subject: %q", e.Subject)
	}
	if !strings.Contains(e.Subject, "https://apt.example/debian") {
		t.Errorf("url kwijt: %q", e.Subject)
	}
	if len(e.To) > maxValue+len("…") || !strings.HasSuffix(e.To, "…") || !utf8Valid(e.To) {
		t.Errorf("to niet goed ingekort: %d bytes", len(e.To))
	}
	if len(e.Detail) != maxDetail {
		t.Errorf("detail: %d velden", len(e.Detail))
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "?") == s }

func TestFromRowRoundTrip(t *testing.T) {
	e := Entry{Kind: KindPackage, Subject: "nginx", Action: "upgrade", From: "1", To: "2", By: "root", Detail: map[string]string{"via": "apt"}}
	b := []byte(`{"from":"1","to":"2","by":"root","via":"apt"}`)
	got := FromRow(e.Kind, e.Subject, e.Action, b)
	if got.From != "1" || got.To != "2" || got.By != "root" || len(got.Detail) != 1 || got.Detail["via"] != "apt" {
		t.Fatalf("FromRow: %+v", got)
	}
	if d := e.detail(); d["from"] != "1" || d["by"] != "root" || d["via"] != "apt" {
		t.Fatalf("detail: %v", d)
	}
}

func actions(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Kind+":"+e.Subject+":"+e.Action)
	}
	return out
}

func TestFactsChanges(t *testing.T) {
	boot := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	old := protocol.Facts{
		Kernel: "6.1.0-25-amd64", BootTime: boot, OS: protocol.OSInfo{PrettyName: "Debian GNU/Linux 12 (bookworm)"},
		Upgrades: &protocol.Upgrades{Total: 3},
		Interfaces: []protocol.Interface{
			{Name: "eth0", Kind: "ether", MTU: 1500, Addresses: []string{"10.0.0.5/24", "10.0.0.100/32", "fe80::1/64"}},
			{Name: "veth1234", Kind: "veth", Addresses: []string{"172.17.0.1/16"}},
			{Name: "eth1", Kind: "ether", MTU: 1500},
		},
		DefaultGateway: "10.0.0.1", DNS: []string{"10.0.0.53"},
		Routes:    []protocol.Route{{Dest: "10.20.0.0/16", Gateway: "10.0.0.254", Interface: "eth0"}, {Dest: "172.18.0.0/16", Interface: "br-abc"}},
		Listeners: []protocol.Listener{{Proto: "tcp", Port: 22, Unit: "ssh.service"}, {Proto: "tcp", Port: 80, Unit: "nginx.service"}},
		Keepalived: &protocol.Keepalived{VIPs: []string{"10.0.0.100"}, Instances: []protocol.VRRPInstance{
			{Name: "VI_1", Interface: "eth0", VRID: 51, State: "BACKUP", Priority: 100, VIPs: []string{"10.0.0.100"}},
		}},
	}
	cur := old
	cur.Kernel, cur.BootTime = "6.1.0-26-amd64", boot.Add(24*time.Hour)
	cur.OS.PrettyName = "Debian GNU/Linux 13 (trixie)"
	cur.Upgrades = &protocol.Upgrades{Total: 0}
	cur.Interfaces = []protocol.Interface{
		// De VIP verhuist en de veth verdwijnt: geen van beide is een
		// wijziging.
		{Name: "eth0", Kind: "ether", MTU: 9000, Addresses: []string{"10.0.0.5/24", "10.0.0.6/24"}},
		{Name: "bond0", Kind: "bond", MTU: 1500},
		{Name: "eth1", Kind: "ether", MTU: 1500, Master: "bond0"},
	}
	cur.DefaultGateway, cur.DNS = "10.0.0.2", []string{"10.0.0.53", "1.1.1.1"}
	cur.Routes = []protocol.Route{{Dest: "172.19.0.0/16", Interface: "br-def"}}
	cur.Listeners = []protocol.Listener{{Proto: "tcp", Port: 22, Unit: "ssh.service"}, {Proto: "tcp", Port: 8080, Unit: "nginx.service"}}
	cur.Keepalived = &protocol.Keepalived{VIPs: []string{"10.0.0.100"}, Instances: []protocol.VRRPInstance{
		{Name: "VI_1", Interface: "eth0", VRID: 51, State: "BACKUP", Priority: 150, VIPs: []string{"10.0.0.100"}},
	}}

	got := FactsChanges(old, cur, false)
	want := []string{
		"boot:boot:boot", "facts:os:changed", "facts:updates:changed",
		"network:eth0:addresses", "network:eth0:mtu", "network:bond0:interface_added", "network:eth1:master",
		"network:gateway:gateway", "network:dns:dns", "network:routes:routes", "network:listeners:listeners", "network:VI_1:keepalived",
	}
	if a := actions(got); !slices.Equal(a, want) {
		t.Fatalf("wijzigingen:\n kreeg %v\n wilde %v", a, want)
	}
	if got[0].From != "6.1.0-25-amd64" || got[0].To != "6.1.0-26-amd64" || !got[0].TS.Equal(cur.BootTime) {
		t.Errorf("boot: %+v", got[0])
	}
	if d := got[3].Detail; d["added"] != "10.0.0.6/24" || d["removed"] != "" {
		t.Errorf("adressen: %v", d)
	}
	if d := got[9].Detail; d["added"] != "" || d["removed"] != "10.20.0.0/16 via 10.0.0.254 (eth0)" {
		t.Errorf("routes: %v", d)
	}
	if s := Describe(got[10], false); s != "Luisterende poorten: tcp 8080 (nginx.service) erbij, tcp 80 (nginx.service) weg" {
		t.Errorf("poorten: %q", s)
	}

	// Met een journaal in de agent komt de herstart van de agent zelf.
	if a := actions(FactsChanges(old, cur, true)); slices.Contains(a, "boot:boot:boot") {
		t.Errorf("herstart dubbel: %v", a)
	}

	// Facts van protocol 4 hebben geen soort per interface: dan alleen
	// interfaces en adressen.
	strip := func(f protocol.Facts) protocol.Facts {
		f.Interfaces = slices.Clone(f.Interfaces)
		for i := range f.Interfaces {
			f.Interfaces[i].Kind, f.Interfaces[i].MTU, f.Interfaces[i].Master = "", 0, ""
		}
		f.DefaultGateway, f.DNS, f.Routes, f.Listeners = "", nil, nil, nil
		f.Keepalived = &protocol.Keepalived{VIPs: f.Keepalived.VIPs}
		return f
	}
	want = []string{"network:eth0:addresses", "network:bond0:interface_added"}
	if a := actions(networkChanges(strip(old), strip(cur))); !slices.Equal(a, want) {
		t.Fatalf("protocol 4:\n kreeg %v\n wilde %v", a, want)
	}
	if got := FactsChanges(old, old, false); len(got) != 0 {
		t.Fatalf("zonder verschil: %v", actions(got))
	}
}

func TestServiceChanges(t *testing.T) {
	old := map[string]string{"nginx": "active", "keepalived": "active", "haproxy": "failed", "redis": "inactive", "php": "active"}
	cur := map[string]string{"nginx": "failed", "keepalived": "inactive", "haproxy": "active", "redis": "activating", "php": "reloading", "new": "active"}
	want := []string{"service:haproxy:started", "service:keepalived:stopped", "service:nginx:failed"}
	if a := actions(ServiceChanges(old, cur)); !slices.Equal(a, want) {
		t.Fatalf("kreeg %v, wilde %v", a, want)
	}
}
