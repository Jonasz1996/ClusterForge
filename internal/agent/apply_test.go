package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func applyAgent(t *testing.T) (*Agent, *agenttest.Host) {
	t.Helper()
	root := t.TempDir()
	h := agenttest.NewHost(root, "192.0.2.10")
	return &Agent{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Exec: h.Exec, Root: root,
		StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"),
	}, h
}

func yes() *bool { v := true; return &v }

func TestApplyKeepalivedNginx(t *testing.T) {
	a, h := applyAgent(t)
	steps := []protocol.Step{
		{Package: &protocol.PackageStep{Names: []string{"keepalived", "nginx"}}},
		{Directory: &protocol.DirectoryStep{Path: "/var/www/html", Mode: "0755"}},
		{File: &protocol.FileStep{Path: "/etc/keepalived/keepalived.conf", Content: "vrrp_instance VI_1 {\n  priority 150\n}\n", Mode: "0640"}},
		{Service: &protocol.ServiceStep{Name: "keepalived", Enabled: yes(), State: "started"}},
		{User: &protocol.UserStep{Name: "deploy", System: true}},
		{Command: &protocol.CommandStep{Run: "touch /var/www/html/klaar", Creates: "/var/www/html/klaar"}},
	}
	res := a.apply(context.Background(), steps)
	if !res.OK || len(res.Steps) != len(steps) {
		t.Fatalf("eerste keer: %+v", res)
	}
	for i, s := range res.Steps {
		if !s.Changed {
			t.Errorf("stap %d zou iets veranderen: %+v", i, s)
		}
	}
	if !h.Installed("nginx") || !h.Installed("keepalived") {
		t.Fatal("pakketten niet geïnstalleerd")
	}
	if u, _ := h.Unit("keepalived"); !u.Enabled || !u.Active {
		t.Fatalf("keepalived: %+v", u)
	}
	fi, err := os.Stat(filepath.Join(a.Root, "etc/keepalived/keepalived.conf"))
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("bestand: %v %v", fi, err)
	}
	if !slices.Contains(h.Calls(), "useradd --system --shell /usr/sbin/nologin deploy") {
		t.Fatalf("useradd: %v", h.Calls())
	}

	// De tweede keer staat alles goed. Het commando zelf voert de nephost
	// niet uit, dus het bestand maken we hier.
	if err := os.WriteFile(filepath.Join(a.Root, "var/www/html/klaar"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	before := len(h.Calls())
	res = a.apply(context.Background(), steps)
	if !res.OK {
		t.Fatalf("tweede keer: %+v", res)
	}
	for i, s := range res.Steps {
		if s.Changed {
			t.Errorf("stap %d veranderde de tweede keer: %+v", i, s)
		}
	}
	if extra := h.Calls()[before:]; len(extra) != 0 {
		t.Fatalf("tweede keer toch commando's: %v", extra)
	}

	// Een gewijzigd bestand wordt herschreven.
	steps[2].File.Content = "vrrp_instance VI_1 {\n  priority 100\n}\n"
	res = a.apply(context.Background(), steps[2:3])
	if !res.OK || !res.Steps[0].Changed || !strings.Contains(res.Steps[0].Output[0], "bijgewerkt") {
		t.Fatalf("bijwerken: %+v", res)
	}
}

func TestApplyStopsAtFailure(t *testing.T) {
	a, h := applyAgent(t)
	h.Fail("apt-get install", "E: Unable to locate package nginxx\n")
	res := a.apply(context.Background(), []protocol.Step{
		{Package: &protocol.PackageStep{Names: []string{"nginxx"}}},
		{Service: &protocol.ServiceStep{Name: "nginx", State: "started"}},
	})
	if res.OK || len(res.Steps) != 1 || !strings.Contains(res.Error, "apt-get install") {
		t.Fatalf("%+v", res)
	}
	if !slices.Contains(res.Steps[0].Output, "E: Unable to locate package nginxx") {
		t.Fatalf("uitvoer van apt ontbreekt: %+v", res.Steps[0])
	}
}

func TestApplyRejects(t *testing.T) {
	a, _ := applyAgent(t)
	for name, s := range map[string]protocol.Step{
		"leeg":            {},
		"twee soorten":    {Package: &protocol.PackageStep{Names: []string{"nginx"}}, Service: &protocol.ServiceStep{Name: "nginx"}},
		"pakketnaam":      {Package: &protocol.PackageStep{Names: []string{"-o=APT::foo"}}},
		"relatief pad":    {File: &protocol.FileStep{Path: "etc/x"}},
		"pad met ..":      {File: &protocol.FileStep{Path: "/etc/../root/x"}},
		"mode":            {File: &protocol.FileStep{Path: "/etc/x", Mode: "999"}},
		"service bestaat": {Service: &protocol.ServiceStep{Name: "nginx", State: "started"}},
		"servicenaam":     {Service: &protocol.ServiceStep{Name: "nginx; rm -rf /"}},
		"commando zonder": {Command: &protocol.CommandStep{Run: "true"}},
		"gebruikersnaam":  {User: &protocol.UserStep{Name: "Root!"}},
	} {
		if res := a.apply(context.Background(), []protocol.Step{s}); res.OK {
			t.Errorf("%s: aanvaard: %+v", name, res)
		}
	}
}

func TestApplyCommand(t *testing.T) {
	a, h := applyAgent(t)
	res := a.apply(context.Background(), []protocol.Step{{Command: &protocol.CommandStep{Run: "make-db", Unless: "test -f /db"}}})
	// De nephost laat "sh -c" altijd lukken, dus unless lukt en het commando
	// wordt overgeslagen.
	if !res.OK || res.Steps[0].Changed || slices.Contains(h.Calls(), "sh -c make-db") {
		t.Fatalf("unless: %+v %v", res, h.Calls())
	}
	h.Fail("sh -c test -f /db", "")
	res = a.apply(context.Background(), []protocol.Step{{Command: &protocol.CommandStep{Run: "make-db", Unless: "test -f /db"}}})
	if !res.OK || !res.Steps[0].Changed || !slices.Contains(h.Calls(), "sh -c make-db") {
		t.Fatalf("uitvoeren: %+v %v", res, h.Calls())
	}
}

// Een nieuwe of gewijzigde unit leest systemd pas na daemon-reload; zonder
// reload bestaat cf-app niet voor systemctl.
func TestApplyUnitFileReloads(t *testing.T) {
	a, h := applyAgent(t)
	unit := protocol.Step{File: &protocol.FileStep{Path: "/etc/systemd/system/cf-app.service", Content: "[Service]\nExecStart=/bin/true\n"}}
	start := protocol.Step{Service: &protocol.ServiceStep{Name: "cf-app", Enabled: yes(), State: "started"}}
	if res := a.apply(context.Background(), []protocol.Step{start}); res.OK {
		t.Fatalf("cf-app zonder unit: %+v", res)
	}
	reloads := func() int {
		n := 0
		for _, c := range h.Calls() {
			if c == "systemctl daemon-reload" {
				n++
			}
		}
		return n
	}
	res := a.apply(context.Background(), []protocol.Step{unit, start})
	if !res.OK || !slices.Contains(res.Steps[0].Output, "systemctl daemon-reload") || reloads() != 1 {
		t.Fatalf("nieuwe unit: %+v %v", res, h.Calls())
	}
	if u, _ := h.Unit("cf-app"); !u.Enabled || !u.Active {
		t.Fatalf("cf-app: %+v", u)
	}
	// Ongewijzigd: geen reload. Een gewone file ook niet, een drop-in wel.
	res = a.apply(context.Background(), []protocol.Step{
		unit,
		{File: &protocol.FileStep{Path: "/etc/docker/daemon.json", Content: "{}\n"}},
		{File: &protocol.FileStep{Path: "/etc/systemd/system/cf-app.service.d/limiet.conf", Content: "[Service]\nMemoryMax=1G\n"}},
	})
	if !res.OK || res.Steps[0].Changed || reloads() != 2 || slices.Contains(res.Steps[1].Output, "systemctl daemon-reload") {
		t.Fatalf("tweede keer: %+v %v", res, h.Calls())
	}
	h.Fail("systemctl daemon-reload", "Failed to reload daemon: Access denied\n")
	unit.File.Content += "Restart=always\n"
	if res := a.apply(context.Background(), []protocol.Step{unit}); res.OK || !strings.Contains(res.Error, "daemon-reload") {
		t.Fatalf("mislukte reload: %+v", res)
	}
}

// Uitvoer van apt-get update op een node van Jonas, met een kapotte bron van
// Zabbix naast werkende bronnen.
const aptZabbix404 = `Hit:1 http://security.debian.org trixie-security InRelease
Hit:2 http://deb.debian.org/debian trixie InRelease
Hit:4 http://repo.zabbix.com/zabbix/8.0/release/debian trixie InRelease
Ign:6 http://repo.zabbix.com/zabbix/8.0/stable/debian trixie InRelease
Err:7 http://repo.zabbix.com/zabbix/8.0/stable/debian trixie Release
  404  Not Found [IP: 178.128.6.101 80]
Reading package lists...
E: The repository 'http://repo.zabbix.com/zabbix/8.0/stable/debian trixie Release' does not have a Release file.
N: Updating from such a repository can't be done securely, and is therefore disabled by default.
`

const aptDebian404 = `Err:2 http://deb.debian.org/debian trixie InRelease
  Could not resolve 'deb.debian.org'
Reading package lists...
W: Failed to fetch http://deb.debian.org/debian/dists/trixie/InRelease  Could not resolve 'deb.debian.org'
E: Some index files failed to download. They have been ignored, or old ones used instead.
`

const aptLock = `Reading package lists...
E: Could not get lock /var/lib/apt/lists/lock. It is held by process 123 (apt-get)
`

const aptPolicyDebian = `mariadb-server:
  Installed: (none)
  Candidate: 1:11.8.3-0+deb13u1
  Version table:
     1:11.8.3-0+deb13u1 500
        500 http://deb.debian.org/debian trixie/main amd64 Packages
`

func TestApplyPackageBrokenSource(t *testing.T) {
	for name, tc := range map[string]struct {
		update, policy string
		ok             bool
		want           string
	}{
		"zabbix kapot":           {update: aptZabbix404, policy: aptPolicyDebian, ok: true, want: "waarschuwing: de pakketbron http://repo.zabbix.com/zabbix/8.0/stable/debian trixie werkt niet (404 Not Found [IP: 178.128.6.101 80])"},
		"debian kapot":           {update: aptDebian404, policy: aptPolicyDebian, want: "Some index files failed to download"},
		"slot":                   {update: aptLock, want: "Could not get lock"},
		"bron biedt pakket aan":  {update: aptZabbix404, policy: "        500 http://repo.zabbix.com/zabbix/8.0/stable/debian trixie/main amd64 Packages\n", want: "biedt mariadb-server aan"},
		"apt-cache policy faalt": {update: aptZabbix404, want: "does not have a Release file"},
	} {
		t.Run(name, func(t *testing.T) {
			a, h := applyAgent(t)
			a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				switch {
				case name == "env" && slices.Contains(args, "update"):
					return []byte(tc.update), errors.New("exit status 100")
				case name == "apt-cache":
					if tc.policy == "" {
						return nil, errors.New("exit status 1")
					}
					return []byte(tc.policy), nil
				}
				return h.Exec(ctx, name, args...)
			}
			res := a.apply(context.Background(), []protocol.Step{{Package: &protocol.PackageStep{Names: []string{"mariadb-server"}}}})
			out := strings.Join(res.Steps[0].Output, "\n") + "\n" + res.Steps[0].Error
			if res.OK != tc.ok || !strings.Contains(out, tc.want) {
				t.Fatalf("ok=%v, uitvoer:\n%s", res.OK, out)
			}
			if tc.ok && !strings.Contains(out, "geïnstalleerd: mariadb-server") {
				t.Fatalf("niet geïnstalleerd:\n%s", out)
			}
		})
	}
}

func TestApplyPackageDpkgInterrupted(t *testing.T) {
	a, h := applyAgent(t)
	installs, configured := 0, false
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "env" && slices.Contains(args, "dpkg"):
			configured = true
			return []byte("Setting up mariadb-server ...\n"), nil
		case name == "env" && slices.Contains(args, "install") && !configured:
			installs++
			return []byte("E: dpkg was interrupted, you must manually run 'dpkg --configure -a' to correct the problem.\n"), errors.New("exit status 100")
		}
		return h.Exec(ctx, name, args...)
	}
	res := a.apply(context.Background(), []protocol.Step{{Package: &protocol.PackageStep{Names: []string{"mariadb-server"}}}})
	out := strings.Join(res.Steps[0].Output, "\n") + "\n" + res.Steps[0].Error
	if !res.OK || !configured || installs != 1 || !strings.Contains(out, "dpkg --configure -a") {
		t.Fatalf("ok=%v configured=%v installs=%d, uitvoer:\n%s", res.OK, configured, installs, out)
	}
}
