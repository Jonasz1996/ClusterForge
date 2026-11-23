package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rustdeskContext is een cluster van twee RustDesk-nodes.
func rustdeskContext(t *testing.T, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("rustdesk")
	if !ok {
		t.Fatal("geen template rustdesk")
	}
	in := map[string]any{"vip": "192.0.2.50", "vrid": 61, "auth_pass": "geheim12", "public_host": "rustdesk.example.org",
		"admin_password": "admin-wachtwoord-1", "jwt_key": "jwtsleutel123"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "RustDesk", Slug: "rustdesk", Environment: "prod"}}
	for i := 1; i <= 2; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("rustdesk-%02d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.0.2.%d", 50+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

func TestRenderRustDesk(t *testing.T) {
	tpl, c := rustdeskContext(t, map[string]any{"api_url": "https://rd-api.example.org", "trusted_proxies": "192.0.2.244"})
	if c.Params["nodes"] != 2 || c.Params["sync_minutes"] != 1 || c.Params["server_image"] != "rustdesk/rustdesk-server:latest" ||
		c.Params["api_image"] != "lejianwen/rustdesk-api:latest" || c.Params["always_relay"] != false {
		t.Fatalf("standaarden: %+v", c.Params)
	}
	files, services := npmFiles(t, tpl, c)
	for _, s := range services {
		if s == "cf-rustdesk" {
			t.Fatalf("cf-rustdesk mag geen service-stap krijgen: %v", services)
		}
	}
	for path, want := range map[string][]string{
		"/opt/cf-rustdesk/compose.yaml": {"image: rustdesk/rustdesk-server:latest", "image: lejianwen/rustdesk-api:latest", `restart: "no"`,
			"network_mode: host", `command: ["hbbs", "-r", "rustdesk.example.org:21117"]`, `command: ["hbbr"]`, `ALWAYS_USE_RELAY: "N"`,
			"/opt/cf-rustdesk/data:/root", "/opt/cf-rustdesk/api:/app/data", "/opt/cf-rustdesk/data:/app/keys:ro",
			`RUSTDESK_API_RUSTDESK_ID_SERVER: "rustdesk.example.org:21116"`, `RUSTDESK_API_RUSTDESK_RELAY_SERVER: "rustdesk.example.org:21117"`,
			`RUSTDESK_API_RUSTDESK_API_SERVER: "https://rd-api.example.org"`, `RUSTDESK_API_GIN_TRUST_PROXY: "192.0.2.244"`,
			"RUSTDESK_API_RUSTDESK_KEY_FILE: /app/keys/id_ed25519.pub", "env_file: /etc/cf-rustdesk/api.env"},
		"/etc/keepalived/keepalived.conf": {"virtual_router_id 61", "192.0.2.50/24 dev eth0", "nopreempt", "unicast_peer {\n        192.0.2.52\n    }",
			`notify_master "/usr/local/lib/clusterforge/rustdesk-ctl.sh master"`, `script "/usr/local/lib/clusterforge/rustdesk-ctl.sh check"`},
		"/etc/systemd/system/cf-rustdesk.service":     {"ExecStart=/usr/local/lib/clusterforge/rustdesk-ctl.sh up", "BindsTo=keepalived.service"},
		"/usr/local/lib/clusterforge/rustdesk-ctl.sh": {"VIP='192.0.2.50'", "INDEX='1'", "{{.State.Running}}", "reset-admin-pwd"},
		"/etc/cf-rustdesk/api.env":                    {"RUSTDESK_API_JWT_KEY=jwtsleutel123"},
		"/etc/cf-rustdesk/admin.pw":                   {"admin-wachtwoord-1"},
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
	if strings.Contains(files["/etc/systemd/system/cf-rustdesk.service"], "WantedBy") {
		t.Error("cf-rustdesk mag niet enabled worden")
	}

	// Zonder api_url: de API op het VIP; altijd relay aan.
	_, c2 := rustdeskContext(t, map[string]any{"always_relay": true})
	files2, _ := npmFiles(t, tpl, c2)
	for _, w := range []string{`RUSTDESK_API_RUSTDESK_API_SERVER: "http://192.0.2.50:21114"`, `ALWAYS_USE_RELAY: "Y"`, `RUSTDESK_API_GIN_TRUST_PROXY: ""`} {
		if !strings.Contains(files2["/opt/cf-rustdesk/compose.yaml"], w) {
			t.Errorf("compose mist %q:\n%s", w, files2["/opt/cf-rustdesk/compose.yaml"])
		}
	}
	if _, err := tpl.Validate(map[string]any{"vip": "192.0.2.50", "public_host": "rustdesk.example.org:21116"}); err == nil {
		t.Error("publiek adres met poort aanvaard")
	}

	syncs, err := tpl.RenderSyncs(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(syncs) != 1 || syncs[0].Source != "/var/lib/cf-rustdesk/outgoing/" || syncs[0].Target != "/var/lib/cf-rustdesk/incoming/" ||
		syncs[0].Before != "/usr/local/lib/clusterforge/rustdesk-ctl.sh snapshot" || !syncs[0].Delete || syncs[0].Schedule != "* * * * *" {
		t.Errorf("syncs: %+v", syncs)
	}

	if _, err := exec.LookPath("shellcheck"); err == nil {
		p := filepath.Join(t.TempDir(), "rustdesk-ctl.sh")
		if err := os.WriteFile(p, []byte(files["/usr/local/lib/clusterforge/rustdesk-ctl.sh"]), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
			t.Errorf("shellcheck:\n%s", out)
		}
	}
}

// rdNode is een nagespeelde RustDesk-node: rustdesk-ctl.sh met eigen mappen
// en nep-commando's voor ip, systemctl, docker, ss en curl.
type rdNode struct {
	t                    *testing.T
	script, dir, bin     string
	base, state, conf    string
	vip, active, healthy bool
}

func newRDNode(t *testing.T, script, name string) *rdNode {
	t.Helper()
	dir := t.TempDir()
	n := &rdNode{t: t, script: filepath.Join(dir, "rustdesk-ctl.sh"), dir: dir, bin: filepath.Join(dir, "bin"),
		base: filepath.Join(dir, "opt"), state: filepath.Join(dir, "state"), conf: filepath.Join(dir, "conf")}
	for _, d := range []string{n.bin, n.base + "/data", n.base + "/api", n.state + "/incoming", n.conf} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stubs := map[string]string{
		"ip":     `[ "$1" = neigh ] && exit 0; [ -f "$CF_TEST_DIR/vip" ] && echo "2: eth0    inet 192.0.2.50/24 scope global secondary eth0"; exit 0`,
		"arping": `exit 0`,
		"ping":   `exit 0`,
		"curl":   `[ -f "$CF_TEST_DIR/healthy" ]`,
		"ss":     `[ -f "$CF_TEST_DIR/healthy" ] && echo "LISTEN 0 128 0.0.0.0:2111x"; exit 0`,
		"logger": `echo "logger $*" >> "$CF_TEST_DIR/log"`,
		"systemctl": `case "$1 $2" in "is-active --quiet") [ "$3" = cf-rustdesk ] && { [ -f "$CF_TEST_DIR/active" ]; exit $?; }; exit 0;;
"is-failed --quiet") exit 1;; esac; echo "systemctl $*" >> "$CF_TEST_DIR/log"; exit 0`,
		// foreign: regels "id image project" van de oude containers;
		// foreign-running: idem, alleen de draaiende.
		"docker": `echo "docker $*" >> "$CF_TEST_DIR/log"
case "$1" in
ps) if [ "$2" = -a ]; then cat "$CF_TEST_DIR/foreign" 2>/dev/null; else cat "$CF_TEST_DIR/foreign-running" 2>/dev/null; fi;;
inspect) case "$3" in
  *Mounts*) cat "$CF_TEST_DIR/mounts" 2>/dev/null;;
  *Running*) [ -f "$CF_TEST_DIR/healthy" ] && echo true;;
  *RestartPolicy*) cat "$CF_TEST_DIR/policy" 2>/dev/null;;
  esac;;
update) echo no > "$CF_TEST_DIR/policy";;
stop) rm -f "$CF_TEST_DIR/foreign-running";;
exec) [ -f "$CF_TEST_DIR/exec-faalt" ] && exit 1;;
esac
exit 0`,
		"hostname": `echo ` + name,
		// nft: -f bewaart de regels in nft.rules, delete wist ze.
		"nft": `echo "nft $*" >> "$CF_TEST_DIR/log"
case "$1" in
-f) cat > "$CF_TEST_DIR/nft.rules";;
delete) rm -f "$CF_TEST_DIR/nft.rules";;
list) [ -f "$CF_TEST_DIR/nft.rules" ];;
esac`,
	}
	for cmd, body := range stubs {
		if err := os.WriteFile(filepath.Join(n.bin, cmd), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(n.script, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, map[string]string{n.conf + "/admin.pw": "admin-wachtwoord-1\n"})
	return n
}

func (n *rdNode) run(args ...string) (string, error) {
	n.t.Helper()
	for f, on := range map[string]bool{"vip": n.vip, "active": n.active, "healthy": n.healthy} {
		p := filepath.Join(n.dir, f)
		if on {
			_ = os.WriteFile(p, nil, 0o644)
		} else {
			_ = os.Remove(p)
		}
	}
	cmd := exec.Command("/bin/sh", append([]string{n.script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+n.bin+":"+os.Getenv("PATH"), "CF_TEST_DIR="+n.dir,
		"CF_RD_BASE="+n.base, "CF_RD_STATE="+n.state, "CF_RD_CONF="+n.conf, "CF_RD_HASH="+filepath.Join(n.dir, "hash"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *rdNode) log() string {
	b, _ := os.ReadFile(filepath.Join(n.dir, "log"))
	return string(b)
}

func sqlite(t *testing.T, db, q string) string {
	t.Helper()
	out, err := exec.Command("sqlite3", db, q).CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite3 %s: %v %s", q, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestRustDeskTakeoverFailover speelt de overname van de oude containers op
// node 1, de kopie, de sync en een failover naar node 2 na: node 2 krijgt
// dezelfde sleutel en de data van de API; zonder kopie start node 2 nooit
// met een nieuwe sleutel.
func TestRustDeskTakeoverFailover(t *testing.T) {
	for _, tool := range []string{"sqlite3", "rsync", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " staat niet op deze machine")
		}
	}
	tpl, c := rustdeskContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	script1 := files["/usr/local/lib/clusterforge/rustdesk-ctl.sh"]
	c.Node = &c.Nodes[1]
	files, _ = npmFiles(t, tpl, c)
	script2 := files["/usr/local/lib/clusterforge/rustdesk-ctl.sh"]
	if !strings.Contains(script2, "INDEX='2'") || !strings.Contains(script2, "BETTER='192.0.2.51 '") {
		t.Fatalf("script node 2:\n%s", script2)
	}

	a, b := newRDNode(t, script1, "rustdesk"), newRDNode(t, script2, "rustdesk-failover")

	// De oude hbbs/hbbr op node 1 met hun map /root/rustdesk/data.
	old := filepath.Join(t.TempDir(), "oud")
	write(t, map[string]string{old + "/id_ed25519": "PRIVE", old + "/id_ed25519.pub": "PUBLIEKESLEUTEL="})
	sqlite(t, old+"/db_v2.sqlite3", "PRAGMA journal_mode=WAL; CREATE TABLE peer (id TEXT); INSERT INTO peer VALUES ('123456789');")
	write(t, map[string]string{
		a.dir + "/foreign":         "aaa rustdesk/rustdesk-server:latest rustdesk\nbbb rustdesk/rustdesk-server:latest rustdesk\n",
		a.dir + "/foreign-running": "aaa rustdesk/rustdesk-server:latest rustdesk\nbbb rustdesk/rustdesk-server:latest rustdesk\n",
		a.dir + "/mounts":          "/root=" + old + " ",
		a.dir + "/policy":          "unless-stopped",
	})

	// De oude containers draaien nog: niet starten; de uitrol stopt ze.
	if out, err := a.run("geen-oude"); err == nil {
		t.Fatalf("geen-oude met draaiende oude containers: %s", out)
	}
	if out, err := a.run("up"); err == nil || !strings.Contains(out, "oude RustDesk-containers") {
		t.Fatalf("up naast oude containers: %v %s", err, out)
	}
	if out, err := a.run("oude-stoppen"); err != nil {
		t.Fatalf("oude-stoppen: %v %s", err, out)
	}
	if !strings.Contains(a.log(), "docker update --restart=no aaa") || !strings.Contains(a.log(), "docker stop aaa bbb") {
		t.Fatalf("log:\n%s", a.log())
	}
	if out, err := a.run("geen-oude"); err != nil {
		t.Fatalf("geen-oude na stoppen: %s", out)
	}

	// Node 1 start: neemt sleutel en database van de oude containers over.
	a.vip = true
	out, err := a.run("up")
	if err != nil || !strings.Contains(out, "PUBLIEKESLEUTEL=") {
		t.Fatalf("up node 1: %v %s", err, out)
	}
	if got, _ := os.ReadFile(a.base + "/data/id_ed25519"); string(got) != "PRIVE" {
		t.Fatalf("sleutel: %q", got)
	}
	if got := sqlite(t, a.base+"/data/db_v2.sqlite3", "SELECT id FROM peer"); got != "123456789" {
		t.Fatalf("peer: %q", got)
	}
	a.active, a.healthy = true, true
	sqlite(t, a.base+"/api/rustdeskapi.db", "CREATE TABLE users (name TEXT); INSERT INTO users VALUES ('admin'),('jonas');")

	// Het wachtwoord van admin komt er na de opstarttijd één keer in.
	if err := os.WriteFile(a.state+"/started", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := a.run("check"); err != nil {
		t.Fatalf("check: %v %s", err, out)
	}
	if !strings.Contains(a.log(), "docker exec -w /app cf-rustdesk-api ./apimain reset-admin-pwd admin-wachtwoord-1") {
		t.Fatalf("wachtwoord niet gezet:\n%s", a.log())
	}
	before := strings.Count(a.log(), "reset-admin-pwd")
	if _, err := a.run("check"); err != nil || strings.Count(a.log(), "reset-admin-pwd") != before {
		t.Fatal("wachtwoord bij elke check opnieuw gezet")
	}

	// Node 2 zonder kopie: mag het VIP niet nemen en start niet.
	if out, err := b.run("check"); err == nil {
		t.Fatalf("check node 2 zonder sleutel: %s", out)
	}
	b.vip = true
	if out, err := b.run("up"); err == nil || !strings.Contains(out, "geen sleutel") {
		t.Fatalf("up node 2 zonder kopie: %v %s", err, out)
	}
	b.vip = false

	// Kopie op node 1 en de sync naar node 2.
	out, err = a.run("snapshot")
	if err != nil {
		t.Fatalf("snapshot: %v %s", err, out)
	}
	for _, p := range []string{"MANIFEST", "SHA256SUMS", "data/id_ed25519", "data/id_ed25519.pub", "data/db_v2.sqlite3", "api/rustdeskapi.db", "api/.cf-admin-pw"} {
		if _, err := os.Stat(filepath.Join(a.state, "outgoing", p)); err != nil {
			t.Errorf("kopie mist %s", p)
		}
	}
	if out, err := exec.Command("rsync", "-a", "--checksum", "--delete", a.state+"/outgoing/", b.state+"/incoming/").CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v %s", err, out)
	}
	if out, err := b.run("check"); err != nil {
		t.Fatalf("check node 2 met kopie: %v %s", err, out)
	}

	// Failover: node 2 start met dezelfde sleutel en de data van de API.
	b.vip = true
	if out, err := b.run("up"); err != nil {
		t.Fatalf("up node 2: %v %s", err, out)
	}
	if got, _ := os.ReadFile(b.base + "/data/id_ed25519.pub"); string(got) != "PUBLIEKESLEUTEL=" {
		t.Fatalf("sleutel node 2: %q", got)
	}
	if got := sqlite(t, b.base+"/api/rustdeskapi.db", "SELECT count(*) FROM users"); got != "2" {
		t.Fatalf("users node 2: %q", got)
	}
	if got := sqlite(t, b.base+"/data/db_v2.sqlite3", "SELECT id FROM peer"); got != "123456789" {
		t.Fatalf("peer node 2: %q", got)
	}
	// De stempel van het wachtwoord reisde mee: node 2 zet het niet opnieuw.
	b.active, b.healthy = true, true
	if err := os.WriteFile(b.state+"/started", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.run("check"); err != nil || strings.Contains(b.log(), "reset-admin-pwd") {
		t.Fatalf("node 2 zette het wachtwoord opnieuw:\n%s", b.log())
	}
	// Dezelfde kopie nog eens: niet ouder dan wat er staat, niets overgezet.
	if out, err := b.run("up"); err != nil || !strings.Contains(out, "niets overgezet") {
		t.Fatalf("tweede up: %v %s", err, out)
	}
	if out, err := b.run("status"); err != nil || !strings.Contains(out, "sleutel:   PUBLIEKESLEUTEL=") {
		t.Fatalf("status: %v %s", err, out)
	}
}

// TestRustDeskUDPFromVIP: op de VIP-node antwoordt UDP 21116 vanaf het VIP
// (nft zet het bronadres), op een backup-node staat die regel er niet.
func TestRustDeskUDPFromVIP(t *testing.T) {
	tpl, c := rustdeskContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	a := newRDNode(t, files["/usr/local/lib/clusterforge/rustdesk-ctl.sh"], "rustdesk")
	a.vip = true
	if out, err := a.run("master"); err != nil {
		t.Fatalf("master: %v %s", err, out)
	}
	rules, err := os.ReadFile(filepath.Join(a.dir, "nft.rules"))
	if err != nil {
		t.Fatalf("geen nft-regels na master:\n%s", a.log())
	}
	for _, want := range []string{"ip daddr 192.0.2.50 udp dport 21116 notrack", "udp sport 21116 ip saddr != 192.0.2.50 notrack ip saddr set 192.0.2.50"} {
		if !strings.Contains(string(rules), want) {
			t.Fatalf("nft-regels zonder %q:\n%s", want, rules)
		}
	}
	a.vip = false
	if out, err := a.run("backup"); err != nil {
		t.Fatalf("backup: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(a.dir, "nft.rules")); err == nil {
		t.Fatal("nft-regels staan er nog na backup")
	}
	ch, err := tpl.RenderChecks(c)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range ch {
		if x.UDP != nil && x.UDP.Addr == "192.0.2.50:21116" && x.UDP.Probe == "rustdesk" {
			found = true
		}
	}
	if !found {
		t.Fatalf("geen udp-controle op het VIP: %+v", ch)
	}
}
