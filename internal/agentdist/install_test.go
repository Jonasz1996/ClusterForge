package agentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installEnv is een machine in een map: een aangemelde agent van versie
// 0.1.0, nep-systemctl en nep-id, en een server met versie 0.2.0.
type installEnv struct {
	t          *testing.T
	root, srv  string
	script     string
	downloads  string
	path       string
	systemctls string
}

func newInstallEnv(t *testing.T) *installEnv {
	t.Helper()
	for _, tool := range []string{"curl", "sha256sum", "install", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s ontbreekt", tool)
		}
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("architectuur niet ondersteund door het script")
	}
	e := &installEnv{t: t, root: t.TempDir(), downloads: t.TempDir()}
	fake := t.TempDir()
	e.systemctls = filepath.Join(e.root, "systemctl.log")
	write(t, filepath.Join(fake, "id"), "#!/bin/sh\necho 0\n", 0o755)
	write(t, filepath.Join(fake, "systemctl"), "#!/bin/sh\necho \"$*\" >>"+e.systemctls+"\n", 0o755)
	e.path = fake + string(os.PathListSeparator) + os.Getenv("PATH")
	write(t, filepath.Join(e.root, "usr/local/bin/cf-agent"), "#!/bin/sh\necho 0.1.0\n", 0o755)
	write(t, filepath.Join(e.root, "etc/clusterforge/agent.json"), `{"node_id":"n1"}`, 0o600)
	e.serve("#!/bin/sh\necho 0.2.0\n", "")
	srv := httptest.NewServer(Downloads(e.downloads))
	t.Cleanup(srv.Close)
	e.srv = srv.URL
	e.script = filepath.Join(t.TempDir(), "agent.sh")
	write(t, e.script, string(installScript), 0o755)
	return e
}

// serve zet de nieuwe agent klaar; sum leeg rekent de juiste checksum.
func (e *installEnv) serve(bin, sum string) {
	if sum == "" {
		s := sha256.Sum256([]byte(bin))
		sum = hex.EncodeToString(s[:])
	}
	name := "cf-agent-linux-" + runtime.GOARCH
	write(e.t, filepath.Join(e.downloads, name), bin, 0o644)
	write(e.t, filepath.Join(e.downloads, name+".sha256"), sum+"  "+name+"\n", 0o644)
}

func (e *installEnv) run(args ...string) (string, int) {
	// De testserver is http.
	cmd := exec.Command("sh", append([]string{e.script, "--allow-http"}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+e.path, "CF_INSTALL_ROOT="+e.root)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return string(out), code
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestInstallUpgrade(t *testing.T) {
	e := newInstallEnv(t)
	bin := filepath.Join(e.root, "usr/local/bin/cf-agent")

	if out, code := e.run("--server", e.srv, "--upgrade", "--token", "cfe_x"); code != 2 {
		t.Fatalf("--upgrade met --token: %d %s", code, out)
	}

	// Een verkeerde checksum laat de oude agent staan.
	e.serve("#!/bin/sh\necho kwaad\n", strings.Repeat("0", 64))
	if out, code := e.run("--server", e.srv, "--upgrade"); code != 1 || !strings.Contains(out, "checksum klopt niet") {
		t.Fatalf("checksum: %d %s", code, out)
	}
	if b, _ := os.ReadFile(bin); !strings.Contains(string(b), "0.1.0") {
		t.Fatalf("agent vervangen ondanks checksum: %s", b)
	}

	e.serve("#!/bin/sh\necho 0.2.0\n", "")
	out, code := e.run("--server", e.srv+"/", "--upgrade")
	if code != 0 || !strings.Contains(out, "bijgewerkt van 0.1.0 naar 0.2.0") {
		t.Fatalf("upgrade: %d %s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "etc/clusterforge/agent.json")); string(b) != `{"node_id":"n1"}` {
		t.Fatalf("aanmelding veranderd: %s", b)
	}
	if fi, err := os.Stat(bin); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("binary: %v %v", fi, err)
	}
	if _, err := os.Stat(bin + ".new"); err == nil {
		t.Fatal("tijdelijke binary blijft staan")
	}
	if b, _ := os.ReadFile(e.systemctls); string(b) != "daemon-reload\nenable cf-agent-fault-clear\nrestart cf-agent\n" {
		t.Fatalf("systemctl: %q", b)
	}
	// De oneshot-unit die achtergebleven storingen terugdraait, komt erbij.
	if b, err := os.ReadFile(filepath.Join(e.root, "etc/systemd/system/cf-agent-fault-clear.service")); err != nil ||
		!strings.Contains(string(b), "ExecStart=/usr/local/bin/cf-agent fault-clear") || !strings.Contains(string(b), "Before=network-pre.target cf-agent.service") {
		t.Fatalf("fault-clear-unit: %v %s", err, b)
	}
	if _, err := os.Stat(filepath.Join(e.root, "etc/systemd/system/cf-agent.service")); err == nil {
		t.Fatal("upgrade hoort de unit niet te herschrijven")
	}

	// Zonder aanmelding valt er niets bij te werken.
	if err := os.Remove(filepath.Join(e.root, "etc/clusterforge/agent.json")); err != nil {
		t.Fatal(err)
	}
	if out, code := e.run("--server", e.srv, "--upgrade"); code != 1 || !strings.Contains(out, "installeer hem met --token") {
		t.Fatalf("zonder aanmelding: %d %s", code, out)
	}
}

func TestInstallRefusesHTTP(t *testing.T) {
	e := newInstallEnv(t)
	cmd := exec.Command("sh", e.script, "--server", e.srv, "--upgrade")
	cmd.Env = append(os.Environ(), "PATH="+e.path, "CF_INSTALL_ROOT="+e.root)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 || !strings.Contains(string(out), "--allow-http") {
		t.Fatalf("http zonder --allow-http: %v %s", err, out)
	}
}

func TestInstallFreeBSD(t *testing.T) {
	e := newInstallEnv(t)
	services := filepath.Join(e.root, "service.log")
	fake := strings.SplitN(e.path, string(os.PathListSeparator), 2)[0]
	write(t, filepath.Join(fake, "service"), "#!/bin/sh\necho \"$*\" >>"+services+"\n", 0o755)
	bin := "#!/bin/sh\nif [ \"$1\" = enroll ]; then echo \"enroll $CF_ENROLL_TOKEN $*\" >>" + services + "; fi\necho 0.3.0\n"
	s := sha256.Sum256([]byte(bin))
	write(t, filepath.Join(e.downloads, "cf-agent-freebsd-amd64"), bin, 0o644)
	write(t, filepath.Join(e.downloads, "cf-agent-freebsd-amd64.sha256"), hex.EncodeToString(s[:])+"  cf-agent-freebsd-amd64\n", 0o644)
	write(t, filepath.Join(e.root, "usr/local/opnsense/version/core"), "{}", 0o644)
	run := func(args ...string) (string, int) {
		cmd := exec.Command("sh", append([]string{e.script, "--allow-http"}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+e.path, "CF_INSTALL_ROOT="+e.root, "CF_INSTALL_OS=FreeBSD", "CF_INSTALL_ARCH=amd64")
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), ee.ExitCode()
		}
		return string(out), 0
	}
	out, code := run("--server", e.srv, "--token", "cfe_abc")
	if code != 0 || !strings.Contains(out, "cf-agent draait en is aangemeld") {
		t.Fatalf("installeren: %d %s", code, out)
	}
	rc, err := os.ReadFile(filepath.Join(e.root, "usr/local/etc/rc.d/cf_agent"))
	if err != nil || !strings.Contains(string(rc), "rcvar=cf_agent_enable") || !strings.Contains(string(rc), "/usr/local/bin/cf-agent run") {
		t.Fatalf("rc.d: %v %s", err, rc)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "etc/rc.conf.d/cf_agent")); string(b) != "cf_agent_enable=\"YES\"\n" {
		t.Fatalf("rc.conf.d: %q", b)
	}
	if b, _ := os.ReadFile(services); !strings.Contains(string(b), "enroll cfe_abc enroll -server "+e.srv+" -allow-http") || !strings.HasSuffix(string(b), "cf_agent restart\n") {
		t.Fatalf("service/enroll: %q", b)
	}
	if _, err := os.Stat(filepath.Join(e.root, "etc/systemd")); err == nil {
		t.Fatal("systemd-units op FreeBSD")
	}
	out, code = run("--server", e.srv, "--upgrade")
	if code != 0 || !strings.Contains(out, "bijgewerkt van 0.3.0 naar 0.3.0") {
		t.Fatalf("bijwerken: %d %s", code, out)
	}
	if out, code := run("--server", e.srv, "--no-enroll"); code != 2 {
		t.Fatalf("--no-enroll: %d %s", code, out)
	}
}

func TestInstallTrueNAS(t *testing.T) {
	e := newInstallEnv(t)
	log := filepath.Join(e.root, "enroll.log")
	e.serve("#!/bin/sh\nif [ \"$1\" = enroll ]; then echo \"enroll $CF_ENROLL_TOKEN\" >>"+log+"; fi\necho 0.4.0\n", "")
	run := func(args ...string) (string, int) {
		cmd := exec.Command("sh", append([]string{e.script, "--allow-http"}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+e.path, "CF_INSTALL_ROOT="+e.root, "CF_INSTALL_TRUENAS=1")
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), ee.ExitCode()
		}
		return string(out), 0
	}
	out, code := run("--server", e.srv, "--token", "cfe_nas")
	if code != 0 || !strings.Contains(out, "start vanzelf na een herstart of update") {
		t.Fatalf("installeren: %d %s", code, out)
	}
	home := filepath.Join(e.root, "data/clusterforge")
	if b, err := os.ReadFile(filepath.Join(home, "etc/agent.json")); err != nil || string(b) != `{"node_id":"n1"}` {
		t.Fatalf("aanmelding niet meegenomen: %v %s", err, b)
	}
	if l, err := os.Readlink(filepath.Join(e.root, "etc/clusterforge")); err != nil || l != "/data/clusterforge/etc" {
		t.Fatalf("koppeling /etc/clusterforge: %q %v", l, err)
	}
	unit, err := os.ReadFile(filepath.Join(e.root, "etc/systemd/system/cf-agent.service"))
	if err != nil || !strings.Contains(string(unit), "ExecStart=/var/lib/clusterforge/bin/cf-agent run") {
		t.Fatalf("unit: %v %s", err, unit)
	}
	if fi, err := os.Stat(filepath.Join(home, "boot.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("boot.sh: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(e.root, "var/lib/clusterforge/bin/cf-agent")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("uitvoerbare kopie: %v", err)
	}
	// /data is noexec: daar mag het script niets rechtstreeks uitvoeren.
	if err := os.Chmod(filepath.Join(home, "bin/cf-agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, "boot.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); string(b) != "enroll cfe_nas\n" {
		t.Fatalf("aanmelden: %q", b)
	}
	out, code = run("--server", e.srv, "--upgrade")
	if code != 0 || !strings.Contains(out, "bijgewerkt naar 0.4.0") {
		t.Fatalf("bijwerken: %d %s", code, out)
	}
	if out, code := run("--server", e.srv, "--no-enroll"); code != 2 {
		t.Fatalf("--no-enroll: %d %s", code, out)
	}
}
