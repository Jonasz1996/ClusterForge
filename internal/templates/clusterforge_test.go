package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cfContext is het eigen cluster van ClusterForge met drie nodes.
func cfContext(t *testing.T, input map[string]any, node int) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("clusterforge")
	if !ok {
		t.Fatal("geen template clusterforge")
	}
	in := map[string]any{"vip": "192.0.2.60", "db_vip": "192.0.2.61", "vrid": 70, "auth_pass": "geheim12", "db_password": "dbWachtwoord123"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "ClusterForge", Slug: "clusterforge", Environment: "prod"}}
	for i := 1; i <= 3; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("cf-%02d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.0.2.%d", 61+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[node-1]
	return tpl, c
}

func TestRenderClusterForge(t *testing.T) {
	tpl, c := cfContext(t, nil, 1)
	if c.Params["nodes"] != 3 || c.Params["scanner"] != true || c.Params["vm_retention"] != "90d" || c.Params["sync_minutes"] != 5 {
		t.Fatalf("standaarden: %+v", c.Params)
	}
	files, services := npmFiles(t, tpl, c)
	for _, s := range services {
		if s == "cf-clusterforge" {
			t.Fatalf("cf-clusterforge mag geen service-stap krijgen: %v", services)
		}
	}
	for path, want := range map[string][]string{
		"/etc/cf-clusterforge/base.env": {"CF_DATABASE_URL=postgres://clusterforge:dbWachtwoord123@192.0.2.61:5432/clusterforge?sslmode=disable",
			"CF_NATS_ADVERTISE=192.0.2.60", "CF_VICTORIAMETRICS_URL=http://127.0.0.1:8428",
			"CF_VICTORIAMETRICS_MIRRORS=http://192.0.2.63:8428,http://192.0.2.64:8428", "CF_SELF_CLUSTER=clusterforge",
			"CF_MASTER_KEY_FILE=/etc/clusterforge/master.key", "CF_HTTP_BIND=0.0.0.0"},
		"/opt/cf-clusterforge/compose.yaml": {"network_mode: host", "env_file: /etc/cf-clusterforge/server.env", `restart: "no"`,
			"/etc/cf-clusterforge/master.key:/etc/clusterforge/master.key:ro", "image: ${CF_IMAGE}-scanner", "cf-clusterforge-server"},
		"/opt/cf-clusterforge/vm.yaml":                   {"-retentionPeriod=90d", "-httpListenAddr=:8428", "/var/lib/cf-clusterforge/vm:/victoria-metrics-data"},
		"/etc/keepalived/keepalived.conf":                {"virtual_router_id 70", "192.0.2.60/24 dev eth0", "nopreempt", `script "/usr/local/lib/clusterforge/clusterforge-ctl.sh check"`},
		"/etc/systemd/system/cf-clusterforge.service":    {"ExecStart=/usr/local/lib/clusterforge/clusterforge-ctl.sh up", "BindsTo=keepalived.service"},
		"/etc/systemd/system/cf-clusterforge-vm.service": {"WantedBy=multi-user.target", "vm.yaml"},
		"/usr/local/lib/clusterforge/clusterforge-ctl.sh": {"VIP='192.0.2.60'", "INDEX='1'", "SCANNER='1'", "PEERS='192.0.2.63 192.0.2.64 '",
			"{{.State.Running}}", "goose_db_version"},
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
	if strings.Contains(files["/etc/systemd/system/cf-clusterforge.service"], "WantedBy") {
		t.Error("cf-clusterforge mag niet enabled worden")
	}
	_, c2 := cfContext(t, map[string]any{"scanner": false}, 2)
	files2, _ := npmFiles(t, tpl, c2)
	if strings.Contains(files2["/opt/cf-clusterforge/compose.yaml"], "\n  scanner:") {
		t.Error("scanner zonder param")
	}
	if !strings.Contains(files2["/etc/cf-clusterforge/base.env"], "CF_VICTORIAMETRICS_MIRRORS=http://192.0.2.62:8428,http://192.0.2.64:8428") {
		t.Errorf("spiegels op node 2:\n%s", files2["/etc/cf-clusterforge/base.env"])
	}
	syncs, err := tpl.RenderSyncs(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(syncs) != 1 || syncs[0].Source != "/var/lib/cf-clusterforge/outgoing/" || syncs[0].Before != "/usr/local/lib/clusterforge/clusterforge-ctl.sh snapshot" ||
		syncs[0].Schedule != "*/5 * * * *" {
		t.Errorf("syncs: %+v", syncs)
	}
	if tpl.Database == nil || tpl.Database.Engine != "postgresql" {
		t.Error("geen PostgreSQL")
	}

	script := files["/usr/local/lib/clusterforge/clusterforge-ctl.sh"]
	p := filepath.Join(t.TempDir(), "clusterforge-ctl.sh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("sh -n:\n%s", out)
	}
	if _, err := exec.LookPath("shellcheck"); err == nil {
		if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
			t.Errorf("shellcheck:\n%s", out)
		}
	}
}

// cfNode is een nagespeelde node: clusterforge-ctl.sh met eigen mappen en
// nep-commando's.
type cfNode struct {
	t                  *testing.T
	script, dir        string
	etc, state, backup string
}

func newCFNode(t *testing.T, node int) *cfNode {
	t.Helper()
	tpl, c := cfContext(t, nil, node)
	files, _ := npmFiles(t, tpl, c)
	dir := t.TempDir()
	n := &cfNode{t: t, script: filepath.Join(dir, "clusterforge-ctl.sh"), dir: dir,
		etc: filepath.Join(dir, "etc"), state: filepath.Join(dir, "state"), backup: filepath.Join(dir, "backups")}
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{bin, n.etc, n.state + "/incoming", filepath.Join(dir, "opt")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(n.etc, "base.env"), []byte(files["/etc/cf-clusterforge/base.env"]), 0o600); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{
		"ip":        `[ "$1" = neigh ] && exit 0; [ -f "$CF_TEST_DIR/vip" ] && echo "2: eth0    inet 192.0.2.60/24 scope global secondary eth0"; exit 0`,
		"arping":    `exit 0`,
		"ping":      `exit 0`,
		"curl":      `[ -f "$CF_TEST_DIR/healthy" ]`,
		"logger":    `echo "logger $*" >> "$CF_TEST_DIR/log"`,
		"chown":     `exit 0`,
		"systemctl": `case "$1 $2" in "is-active --quiet") [ "$3" = cf-clusterforge ] && { [ -f "$CF_TEST_DIR/active" ]; exit $?; }; exit 0;; "is-failed --quiet") exit 1;; esac; echo "systemctl $*" >> "$CF_TEST_DIR/log"; exit 0`,
		"docker":    `echo "docker $*" >> "$CF_TEST_DIR/log"; exit 0`,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Het pad in het script vervangen: de nep-commando's eerst.
	s := strings.Replace(files["/usr/local/lib/clusterforge/clusterforge-ctl.sh"], "PATH=/usr/local/sbin", "PATH="+bin+":/usr/local/sbin", 1)
	if err := os.WriteFile(n.script, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
	return n
}

func (n *cfNode) run(args ...string) (string, error) {
	cmd := exec.Command(n.script, args...)
	cmd.Env = append(os.Environ(), "CF_TEST_DIR="+n.dir, "CF_CF_BASE="+filepath.Join(n.dir, "opt"), "CF_CF_ETC="+n.etc,
		"CF_CF_STATE="+n.state, "CF_CF_BACKUPS="+n.backup, "CF_CF_HASH="+filepath.Join(n.dir, "hash"), "CF_CF_UPDATING="+filepath.Join(n.dir, "updating"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *cfNode) touch(name string) {
	if err := os.WriteFile(filepath.Join(n.dir, name), nil, 0o644); err != nil {
		n.t.Fatal(err)
	}
}

func TestClusterForgeCtl(t *testing.T) {
	n1, n2 := newCFNode(t, 1), newCFNode(t, 2)
	// Vóór de verhuizing: node 1 mag het VIP houden, node 2 niet.
	if out, err := n1.run("check"); err != nil {
		t.Fatalf("check node 1 zonder data: %v\n%s", err, out)
	}
	if _, err := n2.run("check"); err == nil {
		t.Fatal("node 2 zonder hoofdsleutel mag het VIP niet nemen")
	}
	out, _ := n1.run("rol")
	if !strings.Contains(out, "index=1") || !strings.Contains(out, "vip=0") || !strings.Contains(out, "verhuisd=0") {
		t.Fatalf("rol: %s", out)
	}
	// Na de verhuizing op node 1: hoofdsleutel, image en instellingen.
	n1.touch("vip")
	n1.touch("active")
	must(t, os.WriteFile(filepath.Join(n1.etc, "master.key"), []byte("c2xldXRlbA==\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(n1.etc, "image.env"), []byte("CF_IMAGE=ghcr.io/jonasz1996/clusterforge-server:abc1234\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(n1.etc, "overgenomen.env"), []byte("CF_SECURE_COOKIES=false\nCF_NATS_ADVERTISE=192.0.2.153\nCF_TEST_WINDOW=zo 03:00-05:00\n"), 0o600))
	if out, err := n1.run("snapshot"); err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	if out, err := n1.run("restart-if-changed"); err != nil {
		t.Fatalf("restart-if-changed: %v\n%s", err, out)
	}
	env, err := os.ReadFile(filepath.Join(n1.etc, "server.env"))
	must(t, err)
	// Het VIP van het cluster wint van het oude adres; de rest gaat mee.
	if !strings.Contains(string(env), "CF_SECURE_COOKIES=false") || !strings.Contains(string(env), "CF_NATS_ADVERTISE=192.0.2.60") ||
		strings.Contains(string(env), "192.0.2.153") || !strings.Contains(string(env), "CF_TEST_WINDOW=zo 03:00-05:00") {
		t.Fatalf("server.env:\n%s", env)
	}
	// De sync brengt de kopie naar node 2; die zet ze bij de start op zijn plaats.
	cp := exec.Command("cp", "-a", filepath.Join(n1.state, "outgoing")+"/.", filepath.Join(n2.state, "incoming"))
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	if out, err := n2.run("check"); err != nil {
		t.Fatalf("node 2 met een kopie: %v\n%s", err, out)
	}
	n2.touch("vip")
	out, err = n2.run("up")
	if err != nil {
		t.Fatalf("up op node 2: %v\n%s", err, out)
	}
	key, _ := os.ReadFile(filepath.Join(n2.etc, "master.key"))
	img, _ := os.ReadFile(filepath.Join(n2.etc, "image.env"))
	if strings.TrimSpace(string(key)) != "c2xldXRlbA==" || !strings.Contains(string(img), ":abc1234") {
		t.Fatalf("node 2 kreeg de kopie niet: %q %q\n%s", key, img, out)
	}
	log, _ := os.ReadFile(filepath.Join(n2.dir, "log"))
	if !strings.Contains(string(log), "compose -p cf-clusterforge") || !strings.Contains(string(log), "up --no-build") {
		t.Fatalf("up startte compose niet:\n%s", log)
	}
	// Een nieuw image zetten op een node zonder server.
	n3 := newCFNode(t, 3)
	cp = exec.Command("cp", "-a", filepath.Join(n1.state, "outgoing")+"/.", filepath.Join(n3.state, "incoming"))
	must(t, cp.Run())
	if out, err := n3.run("set-image", "ghcr.io/jonasz1996/clusterforge-server:def5678"); err != nil {
		t.Fatalf("set-image: %v\n%s", err, out)
	}
	img, _ = os.ReadFile(filepath.Join(n3.etc, "image.env"))
	if !strings.Contains(string(img), ":def5678") {
		t.Fatalf("image.env: %s", img)
	}
	if _, err := n3.run("set-image", "geen image"); err == nil {
		t.Fatal("ongeldig image aanvaard")
	}
	// Een oudere kopie overschrijft het nieuwe image niet.
	if _, err := n3.run("up"); err != nil {
		t.Fatal(err)
	}
	img, _ = os.ReadFile(filepath.Join(n3.etc, "image.env"))
	if !strings.Contains(string(img), ":def5678") {
		t.Fatalf("de oudere kopie zette het image terug: %s", img)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
