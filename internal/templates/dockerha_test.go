package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dockerHAContext is een cluster van n Docker-hosts, met of zonder VIP.
func dockerHAContext(t *testing.T, n int, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("docker-ha")
	if !ok {
		t.Fatal("geen template docker-ha")
	}
	in := map[string]any{"nodes": n, "auth_pass": "geheim12"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Docker", Slug: "docker-dubbel", Environment: "prod"}}
	for i := 1; i <= n; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("dockerdubbel%d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.0.2.%d", 165+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

func TestRenderDockerHA(t *testing.T) {
	tpl, c := dockerHAContext(t, 2, map[string]any{"vip": "192.0.2.165", "vrid": 84})
	if c.Params["grace_seconds"] != 180 {
		t.Fatalf("standaarden: %+v", c.Params)
	}
	files, services := npmFiles(t, tpl, c)
	if len(services) != 0 {
		t.Errorf("services: %v (keepalived start via docker-ha.sh, Docker blijft zoals het is)", services)
	}
	for path, want := range map[string][]string{
		"/usr/local/lib/clusterforge/docker-ha/keepalived.conf": {"virtual_router_id 84", "192.0.2.165/24 dev eth0", "nopreempt",
			"unicast_peer {\n        192.0.2.167\n    }", `script "/usr/local/lib/clusterforge/docker-ha.sh check"`,
			`notify_master "/usr/local/lib/clusterforge/docker-ha.sh master"`, "auth_pass geheim12", "priority 150"},
		"/usr/local/lib/clusterforge/docker-ha.sh": {"VIP='192.0.2.165'", "GRACE='180'", "{{.State}}|{{.Status}}"},
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
	if _, ok := files["/etc/keepalived/keepalived.conf"]; ok {
		t.Error("keepalived.conf hoort eerst in de map van docker-ha")
	}

	// Zonder VIP: één losse host, geen keepalived.
	tpl, c = dockerHAContext(t, 1, nil)
	files, _ = npmFiles(t, tpl, c)
	if k := files["/usr/local/lib/clusterforge/docker-ha/keepalived.conf"]; strings.Contains(k, "vrrp_instance") {
		t.Errorf("keepalived zonder VIP:\n%s", k)
	}
	if !strings.Contains(files["/usr/local/lib/clusterforge/docker-ha.sh"], "VIP=''") {
		t.Error("VIP hoort leeg")
	}
	if _, err := tpl.Validate(map[string]any{"nodes": 6}); err == nil {
		t.Error("6 nodes aanvaard")
	}
}

// dhaNode is een nagespeelde Docker-host: docker-ha.sh met nep-commando's.
type dhaNode struct {
	t           *testing.T
	dir, script string
}

func newDHANode(t *testing.T, script string) *dhaNode {
	t.Helper()
	dir := t.TempDir()
	n := &dhaNode{t: t, dir: dir, script: filepath.Join(dir, "docker-ha.sh")}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{
		"ip":     `[ -f "$CF_TEST_DIR/vip" ] && echo "2: eth0    inet 192.0.2.165/24 scope global secondary eth0"; exit 0`,
		"logger": `echo "logger $*" >> "$CF_TEST_DIR/log"`,
		"setsid": `"$@"`,
		"docker": `echo "docker $*" >> "$CF_TEST_DIR/log"
case "$1" in
info) [ ! -f "$CF_TEST_DIR/nodocker" ];;
ps) p=$(echo "$*" | sed -n 's/.*project=\([a-z0-9_-]*\).*/\1/p'); cat "$CF_TEST_DIR/ps-$p" 2>/dev/null;;
esac`,
		"crontab": `if [ "$1" = - ]; then cat > "$CF_TEST_DIR/crontab.new" && mv "$CF_TEST_DIR/crontab.new" "$CF_TEST_DIR/crontab"; else cat "$CF_TEST_DIR/crontab" 2>/dev/null; fi`,
	}
	for cmd, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, cmd), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(n.script, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return n
}

func (n *dhaNode) set(name, content string) {
	n.t.Helper()
	if err := os.WriteFile(filepath.Join(n.dir, name), []byte(content), 0o644); err != nil {
		n.t.Fatal(err)
	}
}

func (n *dhaNode) rm(name string) { _ = os.Remove(filepath.Join(n.dir, name)) }

func (n *dhaNode) run(args ...string) (string, error) {
	n.t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{n.script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(n.dir, "bin")+":"+os.Getenv("PATH"), "CF_TEST_DIR="+n.dir,
		"CF_DHA_LIST="+filepath.Join(n.dir, "stacks.list"), "CF_DHA_STATE="+filepath.Join(n.dir, "state"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *dhaNode) log() string {
	data, _ := os.ReadFile(filepath.Join(n.dir, "log"))
	_ = os.Remove(filepath.Join(n.dir, "log"))
	return string(data)
}

func TestDockerHAScript(t *testing.T) {
	tpl, c := dockerHAContext(t, 2, map[string]any{"vip": "192.0.2.165", "vrid": 84})
	files, _ := npmFiles(t, tpl, c)
	script := files["/usr/local/lib/clusterforge/docker-ha.sh"]
	if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v %s", err, out)
	}
	n := newDHANode(t, script)
	n.set("stacks.list", "# kop\nwordpress\t/docker/wordpress\tdocker-compose.yml\tvip\t1\n"+
		"plex\t/docker/plex\tcompose.yaml,override.yaml\talle\t0\n"+
		"oud\t/docker/oud\tcompose.yaml\tuit\t1\n")

	// Zonder VIP telt alleen Docker.
	if _, err := n.run("check"); err != nil {
		t.Fatalf("check zonder VIP: %v", err)
	}
	n.set("nodocker", "")
	if _, err := n.run("check"); err == nil {
		t.Fatal("check zonder Docker slaagt")
	}
	n.rm("nodocker")

	// Met het VIP: wordpress (kritiek) moet gezond draaien, na de opstarttijd.
	n.set("vip", "")
	n.set("ps-wordpress", "running|Up 2 hours (healthy)\nexited|Exited (0) 2 hours ago\n")
	if out, err := n.run("check"); err != nil {
		t.Fatalf("check gezond: %v %s", err, out)
	}
	n.set("ps-wordpress", "running|Up 2 hours (unhealthy)\n")
	if _, err := n.run("check"); err == nil {
		t.Fatal("unhealthy kritiek aanvaard")
	}
	n.set("ps-wordpress", "paused|Up 2 hours (Paused)\n")
	if _, err := n.run("check"); err != nil {
		t.Fatal("gepauzeerd (sync) hoort ok")
	}
	n.set("ps-wordpress", "restarting|Restarting (1) 3 seconds ago\n")
	if _, err := n.run("check"); err == nil {
		t.Fatal("restarting aanvaard")
	}
	// Net master: de opstarttijd geldt.
	if err := os.MkdirAll(filepath.Join(n.dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	n.set("state/master", strconv.FormatInt(time.Now().Unix(), 10))
	if _, err := n.run("check"); err != nil {
		t.Fatal("opstarttijd niet gerespecteerd")
	}
	n.set("state/master", strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10))
	if _, err := n.run("check"); err == nil {
		t.Fatal("na de opstarttijd aanvaard")
	}
	n.rm("ps-wordpress")
	if _, err := n.run("check"); err == nil {
		t.Fatal("ontbrekende kritieke stack aanvaard")
	}

	// master start alleen de stacks met plaatsing vip, backup stopt ze.
	n.log()
	if out, err := n.run("master"); err != nil {
		t.Fatalf("master: %v %s", err, out)
	}
	time.Sleep(300 * time.Millisecond)
	l := n.log()
	if !strings.Contains(l, "docker compose -p wordpress --project-directory /docker/wordpress -f /docker/wordpress/docker-compose.yml up -d --remove-orphans") ||
		strings.Contains(l, "-p plex") || strings.Contains(l, "-p oud") {
		t.Fatalf("master:\n%s", l)
	}
	n.rm("vip")
	if _, err := n.run("backup"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if l := n.log(); !strings.Contains(l, "-p wordpress --project-directory /docker/wordpress -f /docker/wordpress/docker-compose.yml stop") {
		t.Fatalf("backup:\n%s", l)
	}
	if _, err := os.Stat(filepath.Join(n.dir, "state/master")); err == nil {
		t.Error("master-stempel blijft na backup")
	}

	if out, _ := n.run("status"); !strings.Contains(out, "plex (alle): niet gezond") || !strings.Contains(out, "wordpress (vip, kritiek)") {
		t.Errorf("status:\n%s", out)
	}

	// De oude sync in crontab gaat in commentaar, de rest blijft.
	n.set("crontab", "*/5 * * * * /root/sync_docker.sh >> /var/log/sync_docker.log 2>&1\n0 3 * * * /root/backup.sh\n")
	if _, err := n.run("old-sync-ok"); err == nil {
		t.Fatal("old-sync-ok met een oude sync")
	}
	if out, err := n.run("old-sync-off"); err != nil {
		t.Fatalf("old-sync-off: %v %s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(n.dir, "crontab"))
	if !strings.Contains(string(data), "#cf-docker-ha: vervangen door de syncs van ClusterForge# */5 * * * * /root/sync_docker.sh") ||
		!strings.Contains(string(data), "\n0 3 * * * /root/backup.sh") {
		t.Fatalf("crontab:\n%s", data)
	}
	if _, err := n.run("old-sync-ok"); err != nil {
		t.Fatal("old-sync-ok na uitzetten")
	}
}

// TestDockerInstallKeepsExisting: de stap Docker van docker en docker-ha
// installeert nooit Docker op een node die al Docker heeft. docker.io naast
// Docker CE zou Docker CE verwijderen (en alle containers stoppen).
func TestDockerInstallKeepsExisting(t *testing.T) {
	for _, name := range []string{"docker", "docker-ha"} {
		tpl, _ := Latest(name)
		in := map[string]any{}
		if name == "docker-ha" {
			in["auth_pass"] = "geheim12"
		}
		params, err := tpl.Validate(in)
		if err != nil {
			t.Fatal(err)
		}
		c := Context{Params: params, Cluster: ClusterInfo{Name: "D", Slug: "d", Environment: "lab"},
			Nodes: []NodeInfo{{Hostname: "d1", Role: tpl.Roles[0].Name, Index: 1, Address: "192.0.2.10", Prefix: 24, Interface: "eth0"}}}
		c.Node = &c.Nodes[0]
		steps, err := tpl.Steps(c.Node.Role, c)
		if err != nil {
			t.Fatal(err)
		}
		var run, unless string
		for _, s := range steps {
			if s.Command != nil && strings.Contains(s.Command.Run, "apt-get install -y docker.io") {
				run, unless = s.Command.Run, s.Command.Unless
			}
		}
		if run == "" {
			t.Fatalf("%s: geen stap Docker", name)
		}
		for _, tc := range []struct {
			what                         string
			ce, dockerd, compose, active bool
			skip                         bool
			apt, marker                  string
		}{
			{what: "Docker CE draait", ce: true, dockerd: true, compose: true, active: true, skip: true},
			{what: "Docker CE gestopt", ce: true, dockerd: true, compose: true},
			{what: "Docker CE zonder compose", ce: true, dockerd: true, active: true, apt: "install -y docker-compose-plugin"},
			{what: "docker.io zonder compose", dockerd: true, active: true, apt: "install -y docker-compose"},
			{what: "geen Docker", apt: "install -y docker.io docker-compose", marker: "ja"},
		} {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, tool := range []string{"grep", "mkdir", "touch"} {
				p, err := exec.LookPath(tool)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
			}
			stub := func(cmd, body string) {
				if err := os.WriteFile(filepath.Join(bin, cmd), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			b := func(v bool) string {
				if v {
					return "exit 0"
				}
				return "exit 1"
			}
			stub("dpkg-query", map[bool]string{true: "printf 'install ok installed'", false: "exit 1"}[tc.ce])
			if tc.dockerd {
				stub("dockerd", "exit 0")
			}
			stub("docker", b(tc.compose))
			stub("apt-get", `echo "$*" >> "`+dir+`/apt"`)
			stub("systemctl", `case "$1" in is-active) `+b(tc.active)+`;; esac; echo "$*" >> "`+dir+`/systemctl"`)
			sh := func(script string) error {
				script = strings.ReplaceAll(script, "/etc/clusterforge", dir+"/etc")
				cmd := exec.Command("/bin/sh", "-c", script)
				cmd.Env = []string{"PATH=" + bin}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Logf("%s %s: %s", name, tc.what, out)
				}
				return err
			}
			if skipped := sh(unless) == nil; skipped != tc.skip {
				t.Fatalf("%s, %s: unless %v", name, tc.what, skipped)
			}
			if tc.skip {
				continue
			}
			if err := sh(run); err != nil {
				t.Fatalf("%s, %s: %v", name, tc.what, err)
			}
			apt, _ := os.ReadFile(filepath.Join(dir, "apt"))
			if strings.TrimSpace(string(apt)) != tc.apt {
				t.Fatalf("%s, %s: apt-get %q, wil %q", name, tc.what, apt, tc.apt)
			}
			_, err := os.Stat(filepath.Join(dir, "etc/docker-door-clusterforge"))
			if (err == nil) != (tc.marker != "") {
				t.Fatalf("%s, %s: merkteken %v", name, tc.what, err)
			}
		}
	}
}
