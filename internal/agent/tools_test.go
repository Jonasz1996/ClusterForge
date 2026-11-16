package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestToolDefsComplete(t *testing.T) {
	for _, tool := range protocol.SecurityTools {
		d, ok := toolDefs[tool]
		if !ok || d.pkg == "" || d.install == "" || d.remove == "" {
			t.Errorf("tool %s onvolledig", tool)
		}
		if d.run == "" && tool != protocol.ToolAuditd {
			t.Errorf("tool %s zonder run", tool)
		}
	}
}

func TestParseToolItems(t *testing.T) {
	clam := "/tmp/eicar.com: Win.Test.EICAR_HDB-1 FOUND\n/home/x: ok\n"
	if got := parseToolItems(protocol.ToolClamAV, clam); len(got) != 1 || !strings.HasPrefix(got[0], "/tmp/eicar.com") {
		t.Fatalf("clamav: %v", got)
	}
	aide := "Summary:\n  Total number of entries:\t1000\n\n---------------------------------------------------\nAdded entries:\n---------------------------------------------------\n\nf++++++++++++++++: /etc/cron.d/nieuw\n\n" +
		"---------------------------------------------------\nChanged entries:\n---------------------------------------------------\n\nf   ...    .C... : /etc/passwd\n\n" +
		"---------------------------------------------------\nDetailed information about changes:\n---------------------------------------------------\n\nFile: /etc/passwd\n  Size      : 1 | 2\n"
	got := parseToolItems(protocol.ToolAIDE, aide)
	if len(got) != 2 || got[0] != "toegevoegd /etc/cron.d/nieuw" || got[1] != "gewijzigd /etc/passwd" {
		t.Fatalf("aide: %q", got)
	}
	rk := "Checking for hidden files [ Warning ]\nWarning: Hidden file found: /etc/.updated\n"
	if got := parseToolItems(protocol.ToolRkhunter, rk); len(got) != 1 {
		t.Fatalf("rkhunter: %v", got)
	}
}

func TestToolsStartAndState(t *testing.T) {
	root := t.TempDir()
	var calls []string
	running := ""
	a := &Agent{Root: root, Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		c := name + " " + strings.Join(args, " ")
		calls = append(calls, c)
		switch {
		case strings.HasPrefix(c, "dpkg-query") && strings.HasSuffix(c, " clamav"):
			return []byte("ii  1.4.3+dfsg-1"), nil
		case c == "systemctl is-active cf-tool-clamav.service":
			return []byte(running + "\n"), nil
		case strings.HasPrefix(c, "systemd-run --unit=cf-tool-clamav"):
			running = "active"
		}
		return nil, nil
	}}
	if _, err := a.tools(context.Background(), &protocol.ToolsSpec{Action: protocol.ToolRun, Tool: "rm"}); err == nil {
		t.Fatal("onbekende tool aanvaard")
	}
	if _, err := a.tools(context.Background(), &protocol.ToolsSpec{Action: protocol.ToolBaseline, Tool: protocol.ToolClamAV}); err == nil {
		t.Fatal("baseline voor clamav aanvaard")
	}
	res, err := a.tools(context.Background(), &protocol.ToolsSpec{Action: protocol.ToolRun, Tool: protocol.ToolClamAV})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools[0].Tool != protocol.ToolClamAV || !res.Tools[0].Installed || res.Tools[0].Version != "1.4.3+dfsg-1" || res.Tools[0].Running == "" {
		t.Fatalf("stand: %+v", res.Tools[0])
	}
	b, err := os.ReadFile(filepath.Join(root, toolsDir, "clamav", "run.sh"))
	if err != nil || !strings.Contains(string(b), "clamscan -r -i") {
		t.Fatalf("script: %q %v", b, err)
	}
	if _, err := a.tools(context.Background(), &protocol.ToolsSpec{Action: protocol.ToolRun, Tool: protocol.ToolClamAV}); err == nil {
		t.Fatal("tweede run terwijl de eerste loopt")
	}
	// Klaar: het resultaat staat in de bestanden.
	running = ""
	d := filepath.Join(root, toolsDir, "clamav")
	_ = os.WriteFile(filepath.Join(d, "run.started"), []byte("2026-11-16T22:00:00Z\n"), 0o600)
	_ = os.WriteFile(filepath.Join(d, "run.ended"), []byte("2026-11-16T22:30:00Z\n"), 0o600)
	_ = os.WriteFile(filepath.Join(d, "run.exit"), []byte("1\n"), 0o600)
	_ = os.WriteFile(filepath.Join(d, "run.log"), []byte("/srv/x.php: Php.Webshell FOUND\n"), 0o600)
	res, _ = a.tools(context.Background(), &protocol.ToolsSpec{Action: protocol.ToolStatus})
	r := res.Tools[0].Last[protocol.ToolRun]
	if r.Exit == nil || *r.Exit != 1 || len(r.Items) != 1 || res.Tools[0].Running != "" {
		t.Fatalf("resultaat: %+v", res.Tools[0])
	}
}

func TestReadTrivy(t *testing.T) {
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "nginx_1.27.name"), []byte("nginx:1.27"), 0o600)
	_ = os.WriteFile(filepath.Join(d, "nginx_1.27.json"), []byte(`{"Results":[{"Vulnerabilities":[
{"VulnerabilityID":"CVE-1","PkgName":"openssl","InstalledVersion":"3.0","FixedVersion":"3.1","Severity":"HIGH","Title":"x"},
{"VulnerabilityID":"CVE-2","PkgName":"zlib","InstalledVersion":"1","Severity":"CRITICAL"},
{"VulnerabilityID":"CVE-2","PkgName":"zlib","InstalledVersion":"1","Severity":"CRITICAL"}]}]}`), 0o600)
	_ = os.WriteFile(filepath.Join(d, "kapot.name"), []byte("kapot:latest"), 0o600)
	_ = os.WriteFile(filepath.Join(d, "kapot.err"), []byte("FATAL unable to pull\n"), 0o600)
	imgs := readTrivy(d)
	if len(imgs) != 2 || imgs[0].Image != "kapot:latest" || imgs[0].Error == "" {
		t.Fatalf("%+v", imgs)
	}
	n := imgs[1]
	if n.Counts["critical"] != 1 || n.Counts["high"] != 1 || n.Vulns[0].ID != "CVE-2" {
		t.Fatalf("%+v", n)
	}
}
