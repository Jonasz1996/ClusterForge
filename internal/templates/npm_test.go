package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// npmContext is een cluster van twee NPM-nodes.
func npmContext(t *testing.T, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("nginx-proxy-manager")
	if !ok {
		t.Fatal("geen template nginx-proxy-manager")
	}
	in := map[string]any{"vip": "192.0.2.50", "vrid": 52, "auth_pass": "geheim12"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Proxy", Slug: "proxy", Environment: "prod"}}
	for i := 1; i <= 2; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("npm-%02d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.0.2.%d", 50+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

func npmFiles(t *testing.T, tpl *Template, c Context) (map[string]string, []string) {
	t.Helper()
	steps, err := tpl.Steps("node", c)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	var services []string
	for _, s := range steps {
		if s.File != nil {
			files[s.File.Path] = s.File.Content
		}
		if s.Service != nil {
			services = append(services, s.Service.Name)
		}
	}
	return files, services
}

func TestRenderNPM(t *testing.T) {
	tpl, c := npmContext(t, nil)
	if c.Params["sync_minutes"] != 5 || c.Params["npm_image"] != "jc21/nginx-proxy-manager:latest" || c.Params["nodes"] != 2 {
		t.Fatalf("standaarden: %+v", c.Params)
	}
	files, services := npmFiles(t, tpl, c)
	// cf-npm krijgt nooit een service-stap: alleen keepalived start het.
	for _, s := range services {
		if s == "cf-npm" {
			t.Fatalf("services: %v", services)
		}
	}
	for path, want := range map[string][]string{
		"/opt/cf-npm/compose.yaml": {"image: jc21/nginx-proxy-manager:latest", `restart: "no"`, `"81:81"`, "/opt/cf-npm/data:/data",
			"/opt/cf-npm/letsencrypt:/etc/letsencrypt", `TZ: "Europe/Brussels"`},
		"/etc/keepalived/keepalived.conf": {"virtual_router_id 52", "192.0.2.50/24 dev eth0", "nopreempt", "unicast_peer {\n        192.0.2.52\n    }",
			`notify_master "/usr/local/lib/clusterforge/npm-ctl.sh master"`, `script "/usr/local/lib/clusterforge/npm-ctl.sh check"`},
		"/etc/systemd/system/cf-npm.service":     {"ExecStart=/usr/local/lib/clusterforge/npm-ctl.sh up", "Restart=on-failure"},
		"/usr/local/lib/clusterforge/npm-ctl.sh": {"VIP='192.0.2.50'", "\".backup '", "{{.State.Running}}"},
	} {
		got, ok := files[path]
		if !ok {
			t.Errorf("%s ontbreekt", path)
			continue
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s mist %q:\n%s", path, w, got)
			}
		}
	}
	if strings.Contains(files["/etc/systemd/system/cf-npm.service"], "WantedBy") {
		t.Error("cf-npm mag niet enabled worden")
	}

	syncs, err := tpl.RenderSyncs(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(syncs) != 1 {
		t.Fatalf("syncs: %+v", syncs)
	}
	s := syncs[0]
	if s.Name != "NPM-configuratie" || s.Schedule != "*/5 * * * *" || s.Source != "/var/lib/cf-npm/outgoing/" ||
		s.Target != "/var/lib/cf-npm/incoming/" || s.Before != "/usr/local/lib/clusterforge/npm-ctl.sh snapshot" || !s.Delete || !s.Alert {
		t.Errorf("sync: %+v", s)
	}
	_, c = npmContext(t, map[string]any{"sync_minutes": 60})
	if syncs, _ := tpl.RenderSyncs(c); syncs[0].Schedule != "0 * * * *" {
		t.Errorf("elk uur: %q", syncs[0].Schedule)
	}
	if _, err := tpl.Validate(map[string]any{"vip": "192.0.2.50", "sync_minutes": 7}); err == nil {
		t.Error("sync_minutes 7 aanvaard")
	}

	if _, err := exec.LookPath("shellcheck"); err == nil {
		p := filepath.Join(t.TempDir(), "npm-ctl.sh")
		if err := os.WriteFile(p, []byte(files["/usr/local/lib/clusterforge/npm-ctl.sh"]), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
			t.Errorf("shellcheck:\n%s", out)
		}
	}
}

// npmNode is een nagespeelde NPM-node: npm-ctl.sh met eigen mappen en
// nep-commando's voor ip, systemctl, docker en hostname.
type npmNode struct {
	t                 *testing.T
	script, bin, base string
	state             string
	vip, active       bool
}

func newNPMNode(t *testing.T, script, name string) *npmNode {
	t.Helper()
	dir := t.TempDir()
	n := &npmNode{t: t, script: filepath.Join(dir, "npm-ctl.sh"), bin: filepath.Join(dir, "bin"), base: filepath.Join(dir, "opt"), state: filepath.Join(dir, "state")}
	for _, d := range []string{n.bin, n.base + "/data", n.base + "/letsencrypt", n.state + "/incoming"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stubs := map[string]string{
		"ip": `[ -f "$CF_TEST_DIR/vip" ] && echo "2: eth0    inet 192.0.2.50/24 scope global secondary eth0"; exit 0`,
		"systemctl": `case "$1 $2" in "is-active --quiet") [ "$3" = cf-npm ] && { [ -f "$CF_TEST_DIR/active" ]; exit $?; }; exit 0;;
"is-failed --quiet") exit 1;; esac; echo "systemctl $*" >> "$CF_TEST_DIR/log"; exit 0`,
		"docker":   `echo "docker $*" >> "$CF_TEST_DIR/log"; exit 0`,
		"hostname": `echo ` + name,
	}
	for cmd, body := range stubs {
		if err := os.WriteFile(filepath.Join(n.bin, cmd), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(n.script, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return n
}

func (n *npmNode) run(args ...string) (string, error) {
	n.t.Helper()
	dir := filepath.Dir(n.script)
	for f, on := range map[string]bool{"vip": n.vip, "active": n.active} {
		p := filepath.Join(dir, f)
		if on {
			_ = os.WriteFile(p, nil, 0o644)
		} else {
			_ = os.Remove(p)
		}
	}
	cmd := exec.Command("/bin/sh", append([]string{n.script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+n.bin+":"+os.Getenv("PATH"), "CF_TEST_DIR="+dir,
		"CF_NPM_BASE="+n.base, "CF_NPM_STATE="+n.state, "CF_NPM_HASH="+filepath.Join(dir, "hash"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *npmNode) sql(q string) string {
	n.t.Helper()
	out, err := exec.Command("sqlite3", n.base+"/data/database.sqlite", q).CombinedOutput()
	if err != nil {
		n.t.Fatalf("sqlite3 %s: %v %s", q, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, files map[string]string) {
	t.Helper()
	for p, data := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNPMSnapshotFailover speelt de kopie en een failover na met echte
// sqlite3 en rsync: de VIP-node maakt een kopie, de sync brengt ze naar de
// andere node, en die zet ze vóór de start op haar plaats; een halve of
// oudere kopie komt er niet in.
func TestNPMSnapshotFailover(t *testing.T) {
	for _, tool := range []string{"sqlite3", "rsync", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " staat niet op deze machine")
		}
	}
	tpl, c := npmContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	script := files["/usr/local/lib/clusterforge/npm-ctl.sh"]

	a, b := newNPMNode(t, script, "npm-01"), newNPMNode(t, script, "npm-02")
	a.sql("PRAGMA journal_mode=WAL; CREATE TABLE proxy_host (id INTEGER PRIMARY KEY, domain TEXT); INSERT INTO proxy_host (domain) VALUES ('cloud.example.org');")
	write(t, map[string]string{
		a.base + "/data/keys.json":                      `{"key":"geheim"}`,
		a.base + "/data/nginx/proxy_host/1.conf":        "server { server_name cloud.example.org; }",
		a.base + "/data/logs/proxy-host-1_access.log":   "veel regels",
		a.base + "/letsencrypt/archive/npm-1/cert1.pem": "CERT",
	})
	if err := os.MkdirAll(a.base+"/letsencrypt/live/npm-1", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../archive/npm-1/cert1.pem", a.base+"/letsencrypt/live/npm-1/cert.pem"); err != nil {
		t.Fatal(err)
	}

	// Zonder VIP of zonder draaiende NPM: geen kopie.
	if out, err := a.run("snapshot"); err == nil || !strings.Contains(out, "VIP") {
		t.Fatalf("zonder VIP: %v %s", err, out)
	}
	a.vip = true
	if out, err := a.run("snapshot"); err == nil || !strings.Contains(out, "draait hier niet") {
		t.Fatalf("zonder NPM: %v %s", err, out)
	}
	a.active = true
	out, err := a.run("snapshot")
	if err != nil {
		t.Fatalf("snapshot: %v %s", err, out)
	}
	outDir := a.state + "/outgoing"
	for _, p := range []string{"MANIFEST", "SHA256SUMS", "data/database.sqlite", "data/keys.json", "data/nginx/proxy_host/1.conf", "letsencrypt/live/npm-1/cert.pem"} {
		if _, err := os.Stat(filepath.Join(outDir, p)); err != nil {
			t.Errorf("kopie mist %s", p)
		}
	}
	for _, p := range []string{"data/logs", "data/database.sqlite-wal", "data/database.sqlite-shm"} {
		if _, err := os.Stat(filepath.Join(outDir, p)); err == nil {
			t.Errorf("kopie heeft %s", p)
		}
	}
	if fi, err := os.Stat(outDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("rechten van outgoing: %v %v", fi.Mode(), err)
	}

	// De sync (rsync --delete naar incoming, zoals cf-agent sync-receive;
	// --checksum omdat de kopieën in de test binnen dezelfde seconde vallen).
	syncTo := func() {
		t.Helper()
		if out, err := exec.Command("rsync", "-a", "--checksum", "--delete", outDir+"/", b.state+"/incoming/").CombinedOutput(); err != nil {
			t.Fatalf("rsync: %v %s", err, out)
		}
	}
	syncTo()

	// Node b had een oude database met een -wal van een vorige keer.
	b.sql("CREATE TABLE proxy_host (id INTEGER PRIMARY KEY, domain TEXT); INSERT INTO proxy_host (domain) VALUES ('oud.example.org');")
	write(t, map[string]string{b.base + "/data/database.sqlite-wal": "rommel", b.base + "/data/logs/eigen.log": "blijft"})

	// Failover: b krijgt het VIP en start NPM.
	b.vip = true
	out, err = b.run("up")
	if err != nil || !strings.Contains(out, "Kopie van npm-01") {
		t.Fatalf("up: %v %s", err, out)
	}
	if got := b.sql("SELECT group_concat(domain) FROM proxy_host;"); got != "cloud.example.org" {
		t.Errorf("database na failover: %q", got)
	}
	if _, err := os.Stat(b.base + "/data/database.sqlite-wal"); err == nil {
		t.Error("de oude -wal bleef staan")
	}
	if l, err := os.Readlink(b.base + "/letsencrypt/live/npm-1/cert.pem"); err != nil || l != "../../archive/npm-1/cert1.pem" {
		t.Errorf("certificaat: %q %v", l, err)
	}
	if d, _ := os.ReadFile(b.base + "/data/logs/eigen.log"); string(d) != "blijft" {
		t.Error("de eigen logs van b zijn weg")
	}
	log, _ := os.ReadFile(filepath.Join(filepath.Dir(b.script), "log"))
	if !strings.Contains(string(log), "rm -sf") || !strings.Contains(string(log), "up --no-build") {
		t.Errorf("docker: %s", log)
	}

	// Dezelfde kopie nog eens: niets te doen.
	b.sql("INSERT INTO proxy_host (domain) VALUES ('nieuw-op-b.example.org');")
	if out, err := b.run("up"); err != nil || !strings.Contains(out, "niets overgezet") {
		t.Fatalf("tweede up: %v %s", err, out)
	}
	if got := b.sql("SELECT count(*) FROM proxy_host;"); got != "2" {
		t.Errorf("tweede up overschreef de database van b: %s", got)
	}

	// Een halve kopie (een ander bestand dan in SHA256SUMS) komt er niet in.
	a.sql("INSERT INTO proxy_host (domain) VALUES ('later.example.org');")
	if out, err := a.run("snapshot"); err != nil {
		t.Fatalf("tweede snapshot: %v %s", err, out)
	}
	syncTo()
	// De test loopt binnen één seconde; b doet alsof zijn data ouder is.
	write(t, map[string]string{b.state + "/stamp": "0", b.state + "/incoming/data/keys.json": "half"})
	if out, err := b.run("up"); err != nil || !strings.Contains(out, "onvolledig") {
		t.Fatalf("halve kopie: %v %s", err, out)
	}
	if got := b.sql("SELECT count(*) FROM proxy_host;"); got != "2" {
		t.Errorf("halve kopie toch gebruikt: %s", got)
	}
	syncTo()
	if out, err := b.run("up"); err != nil || !strings.Contains(out, "Kopie van npm-01") {
		t.Fatalf("hele kopie: %v %s", err, out)
	}
	if got := b.sql("SELECT group_concat(domain) FROM proxy_host;"); got != "cloud.example.org,later.example.org" {
		t.Errorf("na de hele kopie: %q", got)
	}

	// status vertelt wat er staat.
	if out, err := b.run("status"); err != nil || !strings.Contains(out, "ontvangen: kopie van npm-01") {
		t.Errorf("status: %v %s", err, out)
	}
}
