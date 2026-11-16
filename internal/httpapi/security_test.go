package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

const scanXML = `<nmaprun><host><status state="up"/><address addr="192.168.10.191" addrtype="ipv4"/><ports>
<port protocol="tcp" portid="22"><state state="open"/><service name="ssh" product="OpenSSH"/></port>
<port protocol="tcp" portid="23"><state state="open"/><service name="telnet"/></port>
</ports></host><host><status state="up"/><address addr="192.168.10.50" addrtype="ipv4"/><ports>
<port protocol="tcp" portid="2375"><state state="open"/><service name="docker"/></port></ports></host>
<runstats><hosts up="2" total="254"/></runstats></nmaprun>`

func TestSecurityEndToEnd(t *testing.T) {
	var nmapArgs string
	var sysMu sync.Mutex
	rootLogin, timer, clam := "yes", "", ""
	e, c := adminClient(t)
	feeds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/debian":
			_, _ = w.Write([]byte(`{"openssl":{"CVE-2025-1111":{"description":"lek","scope":"remote","releases":{"trixie":{"status":"resolved","fixed_version":"3.5.1-1+deb13u1","urgency":"high"}}}}}`))
		case "/kev":
			_, _ = w.Write([]byte(`{"vulnerabilities":[{"cveID":"CVE-2099-0001"}]}`))
		}
	}))
	defer feeds.Close()
	e.api.security.DebianURL, e.api.security.KEVURL = feeds.URL+"/debian", feeds.URL+"/kev"

	var web01 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "primary_ip": "192.168.10.191"}, &web01)
	var tok newToken
	if s := c.do("POST", "/api/v1/enrollment-tokens", map[string]any{"node_id": web01.ID}, &tok); s != http.StatusCreated {
		t.Fatalf("token: %d", s)
	}
	root := procRoot(t, strings.Repeat("5", 32))
	writeFile(t, filepath.Join(root, "etc/ssh/sshd_config"), "Include /etc/ssh/sshd_config.d/*.conf\n", 0o644)
	writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nVERSION_CODENAME=trixie\n", 0o644)
	cfg, err := agent.Enroll(context.Background(), agent.EnrollOptions{ServerURL: e.srv.URL, AllowHTTP: true, Token: tok.Token, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: fakeCollector(root),
		HeartbeatInterval: 100_000_000, StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"), Root: root,
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "dpkg-query" && len(args) == 3 && args[1] == "-f=${db:Status-Abbrev} ${Version}" {
				if args[2] == "clamav" {
					return []byte("ii  1.4.3+dfsg-1"), nil
				}
				return nil, errors.New("niet geïnstalleerd")
			}
			if name == "dpkg-query" {
				return []byte("ii \topenssl\t3.5.1-1\tamd64\topenssl\t3.5.1-1\nii \tlibssl3t64\t3.5.1-1\tamd64\topenssl\t3.5.1-1\n" +
					"ii \tnmap\t7.95+dfsg-3\tamd64\tnmap\t7.95+dfsg-3\n"), nil
			}
			if name == "nmap" && len(args) == 1 {
				return []byte("Nmap version 7.95 ( https://nmap.org )\n"), nil
			}
			if name == "nmap" {
				nmapArgs = strings.Join(args, " ")
				return nil, os.WriteFile(args[slices.Index(args, "-oX")+1], []byte(scanXML), 0o600)
			}
			sysMu.Lock()
			defer sysMu.Unlock()
			switch c := name + " " + strings.Join(args, " "); {
			case c == "sshd -T":
				return []byte("permitrootlogin " + rootLogin + "\npermitemptypasswords no\n"), nil
			case c == "systemctl reload ssh":
				rootLogin = "without-password"
			case strings.HasPrefix(c, "systemd-run --unit=cf-ssh-rollback"):
				timer = "active"
			case c == "systemctl stop cf-ssh-rollback.timer":
				timer = ""
			case c == "systemctl is-active cf-tool-clamav.service":
				return []byte(clam + "\n"), nil
			case strings.HasPrefix(c, "systemd-run --unit=cf-tool-clamav"):
				clam = "active"
			case c == "systemctl is-active cf-ssh-rollback.timer":
				return []byte(timer + "\n"), nil
			}
			return nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = ag.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	type finding struct {
		ID       string `json:"id"`
		Key      string `json:"key"`
		Severity string `json:"severity"`
		Hostname string `json:"hostname"`
		Status   string `json:"status"`
	}
	var list struct {
		Items []finding `json:"items"`
	}
	eventually(t, "bevinding voor openssl", func() bool {
		if s := c.do("POST", "/api/v1/security/refresh", map[string]any{}, nil); s != http.StatusAccepted {
			t.Fatalf("verversen: %d", s)
		}
		c.do("GET", "/api/v1/security/findings?kind=cve", nil, &list)
		return len(list.Items) == 1
	})
	if f := list.Items[0]; f.Key != "openssl" || f.Severity != "hoog" || f.Hostname != "web01" {
		t.Fatalf("bevinding = %+v", f)
	}
	var o struct {
		Stats struct {
			Open         int `json:"open"`
			NodesChecked int `json:"nodes_checked"`
		} `json:"stats"`
		Feeds []struct {
			Name    string `json:"name"`
			Entries int    `json:"entries"`
		} `json:"feeds"`
	}
	if s := c.do("GET", "/api/v1/security", nil, &o); s != 200 || o.Stats.Open != 1 || o.Stats.NodesChecked != 1 || o.Feeds[0].Entries != 1 {
		t.Fatalf("overzicht: %d %+v", s, o)
	}
	var h struct {
		Rules []struct {
			ID string `json:"id"`
		} `json:"rules"`
		Nodes []struct {
			HardeningScore *int `json:"hardening_score"`
			Results        []struct {
				ID     string `json:"id"`
				Result string `json:"result"`
			} `json:"results"`
		} `json:"nodes"`
	}
	eventually(t, "verharding", func() bool {
		c.do("GET", "/api/v1/security/hardening", nil, &h)
		return len(h.Nodes) == 1 && h.Nodes[0].HardeningScore != nil
	})
	if len(h.Nodes[0].Results) != len(h.Rules) || h.Nodes[0].Results[0].ID != "ssh-root-login" || h.Nodes[0].Results[0].Result != "fout" {
		t.Fatalf("verharding = %+v", h.Nodes[0])
	}
	var f finding
	if s := c.do("POST", "/api/v1/security/findings/"+list.Items[0].ID+"/accept", map[string]any{"note": "Alleen intern", "days": 30}, &f); s != 200 || f.Status != "aanvaard" {
		t.Fatalf("aanvaarden: %d %+v", s, f)
	}

	// Scopes: publiek adres vraagt een bevestiging; een kijker mag niets veranderen.
	if s := c.do("POST", "/api/v1/security/scopes", map[string]any{"name": "VPS", "cidrs": []string{"203.0.113.9"}}, nil); s != 400 {
		t.Fatalf("publiek zonder bevestiging: %d", s)
	}
	var sc struct {
		ID        string  `json:"id"`
		Addresses float64 `json:"addresses"`
	}
	if s := c.do("POST", "/api/v1/security/scopes", map[string]any{"name": "Thuis", "cidrs": []string{"192.168.10.0/24", "192.168.113.0/24"}}, &sc); s != 201 || sc.Addresses != 512 {
		t.Fatalf("scope: %d %+v", s, sc)
	}
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("DELETE", "/api/v1/security/scopes/"+sc.ID, nil, nil); s != 403 {
		t.Fatalf("kijker verwijdert: %d", s)
	}
	if s := v.do("GET", "/api/v1/security/scopes", nil, nil); s != 200 {
		t.Fatalf("kijker leest: %d", s)
	}

	// Netwerkscan: web01 heeft nmap en is dus scanner; de scan blijft binnen de scope.
	var scan struct {
		ID string `json:"id"`
	}
	if s := c.do("POST", "/api/v1/security/scans", map[string]any{"name": "Thuis wekelijks", "scope_id": sc.ID, "profile": "snel", "schedule": "wekelijks"}, &scan); s != 201 {
		t.Fatalf("scan: %d", s)
	}
	if s := v.do("POST", "/api/v1/security/scans/"+scan.ID+"/run", nil, nil); s != 403 {
		t.Fatalf("kijker scant: %d", s)
	}
	var run struct {
		RunID string `json:"run_id"`
	}
	eventually(t, "scan gestart", func() bool {
		return c.do("POST", "/api/v1/security/scans/"+scan.ID+"/run", nil, &run) == http.StatusAccepted
	})
	var detail struct {
		Status    string `json:"status"`
		Error     string `json:"error"`
		HostsUp   int    `json:"hosts_up"`
		OpenPorts int    `json:"open_ports"`
		Result    *struct {
			Hosts []struct {
				Addr     string `json:"addr"`
				Node     string `json:"node"`
				Expected []struct {
					Known string `json:"known"`
				} `json:"expected"`
			} `json:"hosts"`
		} `json:"result"`
	}
	eventually(t, "scan klaar", func() bool {
		c.do("GET", "/api/v1/security/scan-runs/"+run.RunID, nil, &detail)
		return detail.Status != "bezig"
	})
	if detail.Status != "klaar" || detail.HostsUp != 2 || detail.OpenPorts != 3 || detail.Result == nil || detail.Result.Hosts[0].Node != "web01" ||
		detail.Result.Hosts[0].Expected[0].Known != "onbekend" {
		t.Fatalf("scan = %+v", detail)
	}
	if !strings.Contains(nmapArgs, "192.168.10.0/24") || !strings.Contains(nmapArgs, "192.168.113.0/24") {
		t.Errorf("nmap kreeg %q", nmapArgs)
	}
	var net struct {
		Items []finding `json:"items"`
	}
	c.do("GET", "/api/v1/security/findings?kind=netwerk", nil, &net)
	sev := map[string]string{}
	for _, f := range net.Items {
		sev[f.Key] = f.Severity
	}
	if sev["poort:2375/tcp"] != "kritiek" || sev["poort:23/tcp"] != "hoog" || sev["onbekend:22/tcp"] != "middel" {
		t.Fatalf("netwerkbevindingen = %v", sev)
	}

	var rep struct {
		Summary struct {
			Open    int `json:"open"`
			HostsUp int `json:"hosts_up"`
			Unknown int `json:"unknown_hosts"`
		} `json:"summary"`
		Nodes []struct {
			Hostname string `json:"hostname"`
		} `json:"nodes"`
	}
	if s := c.do("GET", "/api/v1/security/report?scope_id="+sc.ID, nil, &rep); s != 200 || rep.Summary.HostsUp != 2 || rep.Summary.Unknown != 1 ||
		len(rep.Nodes) != 1 || rep.Nodes[0].Hostname != "web01" || rep.Summary.Open < 3 {
		t.Fatalf("rapport: %d %+v", s, rep)
	}
	var att struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		Badges map[string]struct {
			Count int `json:"count"`
		} `json:"badges"`
	}
	c.do("GET", "/api/v1/attention", nil, &att)
	if att.Badges["/kwetsbaarheden"].Count == 0 {
		t.Errorf("aandacht: %+v", att)
	}

	// M6: oplossen met één klik; SSH wacht op bevestiging.
	type fixRun struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Outcomes []struct {
			Rule  string `json:"rule"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"outcomes"`
	}
	var fr fixRun
	if s := c.do("POST", "/api/v1/security/fixes", map[string]any{"node_id": web01.ID, "rules": []string{"rm -rf"}}, nil); s != 400 {
		t.Fatalf("onbekende regel: %d", s)
	}
	if s := c.do("POST", "/api/v1/security/fixes", map[string]any{"node_id": web01.ID, "rules": []string{"ssh-root-login", "net-syncookies"}}, &fr); s != 200 ||
		fr.Status != "wacht op bevestiging" || len(fr.Outcomes) != 2 || !fr.Outcomes[0].OK || !fr.Outcomes[1].OK {
		t.Fatalf("oplossen: %d %+v", s, fr)
	}
	if b, err := os.ReadFile(filepath.Join(root, "etc/ssh/sshd_config.d/01-clusterforge.conf")); err != nil || !strings.Contains(string(b), "PermitRootLogin prohibit-password") {
		t.Fatalf("drop-in: %q %v", b, err)
	}
	if s := c.do("GET", "/api/v1/security/hardening", nil, &h); s != 200 || h.Nodes[0].Results[0].Result != "ok" {
		t.Fatalf("na oplossen niet opnieuw gecontroleerd: %+v", h.Nodes[0].Results[0])
	}
	if s := c.do("POST", "/api/v1/security/fixes/"+fr.ID+"/confirm", nil, &fr); s != 200 || fr.Status != "bevestigd" {
		t.Fatalf("bevestigen: %d %+v", s, fr)
	}
	if s := c.do("POST", "/api/v1/security/fixes/"+fr.ID+"/rollback", nil, nil); s != 409 {
		t.Fatalf("terugdraaien na bevestigen: %d", s)
	}
	var runs []fixRun
	if c.do("GET", "/api/v1/security/fixes", nil, &runs); len(runs) != 1 {
		t.Fatalf("lijst: %+v", runs)
	}

	// M7: tools. Run start op de achtergrond; het resultaat komt bij de
	// volgende stand en wordt een bevinding.
	if s := c.do("POST", "/api/v1/security/tools/"+web01.ID+"/clamav/baseline", nil, nil); s != 400 {
		t.Fatalf("baseline voor clamav: %d", s)
	}
	if s := c.do("POST", "/api/v1/security/tools/"+web01.ID+"/trivy/install", nil, nil); s != 400 {
		t.Fatalf("trivy zonder docker: %d", s)
	}
	if s := c.do("POST", "/api/v1/security/tools/"+web01.ID+"/clamav/run", nil, nil); s != 202 {
		t.Fatalf("clamav run: %d", s)
	}
	if s := c.do("POST", "/api/v1/security/tools/"+web01.ID+"/clamav/run", nil, nil); s != 409 {
		t.Fatalf("tweede run: %d", s)
	}
	if s := c.do("PUT", "/api/v1/security/tools/"+web01.ID+"/clamav/schedule", map[string]any{"schedule": "wekelijks", "weekday": 0, "hour": 2}, nil); s != 204 {
		t.Fatalf("plannen: %d", s)
	}
	td := filepath.Join(root, "var/lib/clusterforge/tools/clamav")
	writeFile(t, filepath.Join(td, "run.started"), "2026-11-16T22:00:00Z\n", 0o600)
	writeFile(t, filepath.Join(td, "run.ended"), "2026-11-16T22:30:00Z\n", 0o600)
	writeFile(t, filepath.Join(td, "run.exit"), "1\n", 0o600)
	writeFile(t, filepath.Join(td, "run.log"), "/srv/www/shell.php: Php.Webshell-1 FOUND\n", 0o600)
	sysMu.Lock()
	clam = ""
	sysMu.Unlock()
	if err := e.api.security.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var tools struct {
		Nodes []struct {
			Tools []struct {
				Tool      string `json:"tool"`
				Installed bool   `json:"installed"`
				Running   string `json:"running"`
				Schedule  string `json:"schedule"`
			} `json:"tools"`
		} `json:"nodes"`
	}
	if c.do("GET", "/api/v1/security/tools", nil, &tools); len(tools.Nodes) != 1 || tools.Nodes[0].Tools[0].Tool != "clamav" ||
		!tools.Nodes[0].Tools[0].Installed || tools.Nodes[0].Tools[0].Running != "" || tools.Nodes[0].Tools[0].Schedule != "wekelijks" {
		t.Fatalf("tools: %+v", tools)
	}
	c.do("GET", "/api/v1/security/findings?kind=tools", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Severity != "kritiek" {
		t.Fatalf("malwarebevinding: %+v", list.Items)
	}
	c.do("GET", "/api/v1/attention", nil, &att)
	found := false
	for _, it := range att.Items {
		found = found || it.ID == "security-malware"
	}
	if !found {
		t.Errorf("geen aandacht voor malware: %+v", att.Items)
	}

	e.api.security.Wait()
	if s := c.do("DELETE", "/api/v1/security/scopes/"+sc.ID, nil, nil); s != 204 {
		t.Fatalf("verwijderen: %d", s)
	}
}
