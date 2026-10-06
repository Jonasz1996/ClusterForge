package agent

import (
	"context"
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
