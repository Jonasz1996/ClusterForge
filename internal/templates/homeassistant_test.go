package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// haContext is een cluster van drie Home Assistant-nodes.
func haContext(t *testing.T, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("home-assistant")
	if !ok {
		t.Fatal("geen template home-assistant")
	}
	in := map[string]any{"vip": "192.0.2.50", "vrid": 54, "auth_pass": "geheim12", "db_password": "Wachtwoord-123"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Huis", Slug: "huis", Environment: "prod"}}
	for i := 1; i <= 3; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("ha-%02d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.0.2.%d", 50+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

func TestRenderHomeAssistant(t *testing.T) {
	tpl, c := haContext(t, nil)
	if c.Params["nodes"] != 3 || c.Params["port"] != 8123 || c.Params["ha_image"] != "ghcr.io/home-assistant/home-assistant:stable" {
		t.Fatalf("standaarden: %+v", c.Params)
	}
	files, services := npmFiles(t, tpl, c)
	for _, s := range services {
		if s == "cf-homeassistant" {
			t.Fatalf("cf-homeassistant mag geen service-stap hebben: %v", services)
		}
	}
	for path, want := range map[string][]string{
		"/opt/cf-homeassistant/compose.yaml": {"image: ghcr.io/home-assistant/home-assistant:stable", `restart: "no"`, "network_mode: host",
			"/opt/cf-homeassistant/config:/config", "/etc/cf-homeassistant/recorder.yaml:/config/cf-recorder.yaml:ro"},
		"/etc/cf-homeassistant/recorder.yaml": {"db_url: mysql://homeassistant:Wachtwoord-123@192.0.2.50:3306/homeassistant?charset=utf8mb4", "purge_keep_days: 10"},
		"/etc/cf-homeassistant/http.yaml":     {"server_port: 8123"},
		"/etc/cf-homeassistant/db.cnf":        {"host=192.0.2.50", "user=homeassistant", "password=Wachtwoord-123"},
		"/etc/keepalived/keepalived.conf": {"virtual_router_id 54", "192.0.2.50/24 dev eth0", "nopreempt",
			"unicast_peer {\n        192.0.2.52\n        192.0.2.53\n    }", `script "/usr/local/lib/clusterforge/ha-ctl.sh check"`},
		"/etc/systemd/system/cf-homeassistant.service": {"ExecStart=/usr/local/lib/clusterforge/ha-ctl.sh up", "BindsTo=keepalived.service"},
		"/usr/local/lib/clusterforge/ha-ctl.sh":        {"VIP='192.0.2.50'", "INDEX='1'", "BETTER=''", "cf-agent check db mariadb"},
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
	if strings.Contains(files["/etc/cf-homeassistant/http.yaml"], "trusted_proxies") {
		t.Error("trusted_proxies zonder proxy")
	}
	if strings.Contains(files["/etc/systemd/system/cf-homeassistant.service"], "WantedBy") {
		t.Error("cf-homeassistant mag niet enabled worden")
	}

	// Achter Nginx Proxy Manager.
	_, c2 := haContext(t, map[string]any{"trusted_proxies": "192.168.102.244 192.168.102.0/24"})
	files2, _ := npmFiles(t, tpl, c2)
	if got := files2["/etc/cf-homeassistant/http.yaml"]; !strings.Contains(got, "use_x_forwarded_for: true\ntrusted_proxies:\n  - 192.168.102.244\n  - 192.168.102.0/24") {
		t.Errorf("http.yaml met proxy's:\n%s", got)
	}
	if _, err := tpl.Validate(map[string]any{"vip": "192.0.2.50", "trusted_proxies": "npm.thuis"}); err == nil {
		t.Error("trusted_proxies met een naam aanvaard")
	}
	if _, err := tpl.Validate(map[string]any{"vip": "192.0.2.50", "nodes": 4}); err == nil {
		t.Error("vier nodes aanvaard")
	}

	// Node 3 kent de betere nodes.
	c.Node = &c.Nodes[2]
	files3, _ := npmFiles(t, tpl, c)
	if !strings.Contains(files3["/usr/local/lib/clusterforge/ha-ctl.sh"], "BETTER='192.0.2.51 192.0.2.52 '") {
		t.Error("BETTER op node 3")
	}

	// Database: Galera met de database en gebruiker homeassistant.
	if tpl.Database == nil || tpl.Database.Engine != "mariadb" || tpl.Database.Role != "node" {
		t.Fatalf("database: %+v", tpl.Database)
	}
	steps, err := tpl.RenderDBSteps(c)
	if err != nil || len(steps) != 2 || steps[0].Database != "homeassistant" || steps[1].User != "homeassistant" ||
		steps[1].Password != "Wachtwoord-123" || steps[1].GrantOn != "homeassistant" {
		t.Errorf("database-instructies: %+v %v", steps, err)
	}
	if host, err := tpl.DBHost(c); err != nil || host != "192.0.2.50" {
		t.Errorf("databasehost: %q %v", host, err)
	}

	syncs, err := tpl.RenderSyncs(c)
	if err != nil || len(syncs) != 1 {
		t.Fatalf("syncs: %+v %v", syncs, err)
	}
	if s := syncs[0]; s.Name != "Home Assistant-configuratie" || s.Schedule != "*/5 * * * *" || s.Source != "/var/lib/cf-homeassistant/outgoing/" ||
		s.Target != "/var/lib/cf-homeassistant/incoming/" || s.Before != "/usr/local/lib/clusterforge/ha-ctl.sh snapshot" || !s.Delete {
		t.Errorf("sync: %+v", s)
	}

	if _, err := exec.LookPath("shellcheck"); err == nil {
		p := filepath.Join(t.TempDir(), "ha-ctl.sh")
		if err := os.WriteFile(p, []byte(files["/usr/local/lib/clusterforge/ha-ctl.sh"]), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
			t.Errorf("shellcheck:\n%s", out)
		}
	}
}

// haNode is een nagespeelde Home Assistant-node: ha-ctl.sh met eigen mappen
// en nep-commando's.
type haNode struct {
	t                 *testing.T
	script, bin, base string
	state, etc        string
	vip, active, db   bool
}

func newHANode(t *testing.T, script, name string) *haNode {
	t.Helper()
	dir := t.TempDir()
	n := &haNode{t: t, script: filepath.Join(dir, "ha-ctl.sh"), bin: filepath.Join(dir, "bin"), base: filepath.Join(dir, "opt"),
		state: filepath.Join(dir, "state"), etc: filepath.Join(dir, "etc")}
	for _, d := range []string{n.bin, n.base + "/config", n.state + "/incoming", n.etc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stubs := map[string]string{
		"ip":     `[ "$1" = neigh ] && exit 0; [ -f "$CF_TEST_DIR/vip" ] && echo "2: eth0    inet 192.0.2.50/24 scope global secondary eth0"; exit 0`,
		"arping": `exit 0`,
		"ping":   `exit 0`,
		"systemctl": `case "$1 $2" in "is-active --quiet") [ "$3" = cf-homeassistant ] && { [ -f "$CF_TEST_DIR/active" ]; exit $?; }; exit 0;;
"is-failed --quiet") exit 1;; esac; echo "systemctl $*" >> "$CF_TEST_DIR/log"; exit 0`,
		"docker": `echo "docker $*" >> "$CF_TEST_DIR/log"
case "$1" in
ps) [ -f "$CF_TEST_DIR/foreign" ] && cat "$CF_TEST_DIR/foreign";;
inspect) case "$3" in *Mounts*) cat "$CF_TEST_DIR/mounts";; esac;;
stop) rm -f "$CF_TEST_DIR/foreign";;
esac
exit 0`,
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

func (n *haNode) run(args ...string) (string, error) {
	n.t.Helper()
	dir := filepath.Dir(n.script)
	for f, on := range map[string]bool{"vip": n.vip, "active": n.active, "db": n.db} {
		p := filepath.Join(dir, f)
		if on {
			_ = os.WriteFile(p, nil, 0o644)
		} else {
			_ = os.Remove(p)
		}
	}
	cmd := exec.Command("/bin/sh", append([]string{n.script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+n.bin+":"+os.Getenv("PATH"), "CF_TEST_DIR="+dir,
		"CF_HA_BASE="+n.base, "CF_HA_STATE="+n.state, "CF_HA_ETC="+n.etc, "CF_HA_HASH="+filepath.Join(dir, "hash"),
		"CF_HA_DB_READY="+filepath.Join(dir, "db"), "CF_HA_NO_WAIT=1", "CF_HA_GALERA="+filepath.Join(dir, "galera.cnf"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *haNode) sql(db, q string) string {
	n.t.Helper()
	out, err := exec.Command("sqlite3", n.base+"/config/"+db, q).CombinedOutput()
	if err != nil {
		n.t.Fatalf("sqlite3 %s: %v %s", q, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestHomeAssistantFailover speelt de kopie van /config en een failover na
// met echte sqlite3 en rsync.
func TestHomeAssistantFailover(t *testing.T) {
	for _, tool := range []string{"sqlite3", "rsync", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " staat niet op deze machine")
		}
	}
	tpl, c := haContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	a := newHANode(t, files["/usr/local/lib/clusterforge/ha-ctl.sh"], "ha-01")
	c.Node = &c.Nodes[1]
	files2, _ := npmFiles(t, tpl, c)
	b := newHANode(t, files2["/usr/local/lib/clusterforge/ha-ctl.sh"], "ha-02")

	// Een nieuw cluster: node 1 start leeg en maakt configuration.yaml, maar
	// pas als de database er is.
	a.vip = true
	if out, err := a.run("up"); err == nil || !strings.Contains(out, "wachten op MariaDB Galera") {
		t.Fatalf("up zonder database: %v %s", err, out)
	}
	a.db = true
	if out, err := a.run("up"); err != nil {
		t.Fatalf("up: %v %s", err, out)
	}
	conf, _ := os.ReadFile(a.base + "/config/configuration.yaml")
	for _, w := range []string{"default_config:", "recorder: !include cf-recorder.yaml", "http: !include cf-http.yaml", "automation: !include automations.yaml"} {
		if !strings.Contains(string(conf), w) {
			t.Errorf("configuration.yaml mist %q:\n%s", w, conf)
		}
	}
	if log, _ := os.ReadFile(filepath.Dir(a.script) + "/log"); !strings.Contains(string(log), "up --no-build") {
		t.Errorf("docker: %s", log)
	}

	// Home Assistant draait en schrijft.
	a.active = true
	a.sql("zigbee.db", "PRAGMA journal_mode=WAL; CREATE TABLE devices (ieee TEXT); INSERT INTO devices VALUES ('00:11:22');")
	write(t, map[string]string{
		a.base + "/config/.storage/core.config_entries": `{"data":{"entries":[]}}`,
		a.base + "/config/.storage/auth":                `{"users":["jonas"]}`,
		a.base + "/config/home-assistant.log":           "veel regels",
		a.base + "/config/home-assistant_v2.db":         "oude recorder",
		a.base + "/config/backups/groot.tar":            "back-up",
		a.base + "/config/cf-recorder.yaml":             "", // het mountpunt
	})
	if out, err := a.run("snapshot"); err != nil {
		t.Fatalf("snapshot: %v %s", err, out)
	}
	outDir := a.state + "/outgoing/config"
	for _, p := range []string{".storage/auth", "configuration.yaml", "zigbee.db", "automations.yaml"} {
		if _, err := os.Stat(filepath.Join(outDir, p)); err != nil {
			t.Errorf("kopie mist %s", p)
		}
	}
	for _, p := range []string{"home-assistant.log", "home-assistant_v2.db", "backups", "zigbee.db-wal", "cf-recorder.yaml"} {
		if _, err := os.Stat(filepath.Join(outDir, p)); err == nil {
			t.Errorf("kopie heeft %s", p)
		}
	}

	// Node 2 zonder kopie start niet leeg.
	b.vip, b.db = true, true
	if out, err := b.run("up"); err == nil || !strings.Contains(out, "niet leeg") {
		t.Fatalf("lege node 2: %v %s", err, out)
	}
	if out, err := exec.Command("rsync", "-a", "--checksum", "--delete", a.state+"/outgoing/", b.state+"/incoming/").CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v %s", err, out)
	}
	write(t, map[string]string{b.base + "/config/backups/eigen.tar": "blijft", b.base + "/config/zigbee.db-wal": "rommel"})
	out, err := b.run("up")
	if err != nil || !strings.Contains(out, "Kopie van ha-01") {
		t.Fatalf("failover: %v %s", err, out)
	}
	if got := b.sql("zigbee.db", "SELECT ieee FROM devices;"); got != "00:11:22" {
		t.Errorf("zigbee.db na failover: %q", got)
	}
	if d, _ := os.ReadFile(b.base + "/config/.storage/auth"); string(d) != `{"users":["jonas"]}` {
		t.Errorf(".storage na failover: %q", d)
	}
	if d, _ := os.ReadFile(b.base + "/config/backups/eigen.tar"); string(d) != "blijft" {
		t.Error("de eigen back-ups van node 2 zijn weg")
	}
	if _, err := os.Stat(b.base + "/config/zigbee.db-wal"); err == nil {
		t.Error("de oude -wal bleef staan")
	}

	// Een halve kopie komt er niet in.
	write(t, map[string]string{b.state + "/stamp": "0", b.state + "/incoming/config/.storage/auth": "half"})
	if out, err := b.run("up"); err != nil || !strings.Contains(out, "onvolledig") {
		t.Fatalf("halve kopie: %v %s", err, out)
	}

	// Een bestaande configuratie met een eigen recorder: niet aanraken, wel
	// waarschuwen; http komt erbij.
	write(t, map[string]string{b.base + "/config/configuration.yaml": "default_config:\nrecorder:\n  purge_keep_days: 5\n"})
	out, _ = b.run("up")
	if !strings.Contains(out, "eigen recorder") {
		t.Errorf("geen waarschuwing over recorder: %s", out)
	}
	conf, _ = os.ReadFile(b.base + "/config/configuration.yaml")
	if strings.Contains(string(conf), "cf-recorder") || !strings.Contains(string(conf), "http: !include cf-http.yaml") {
		t.Errorf("configuration.yaml: %s", conf)
	}

	// Overnemen van een Home Assistant in Docker op deze node.
	o := newHANode(t, files["/usr/local/lib/clusterforge/ha-ctl.sh"], "ha-oud")
	dir := filepath.Dir(o.script)
	old := t.TempDir()
	write(t, map[string]string{
		dir + "/foreign":                       "abc123 ghcr.io/home-assistant/home-assistant:stable homeassistant\n",
		dir + "/mounts":                        "/config " + old + "\n",
		old + "/configuration.yaml":            "default_config:\nautomation: !include automations.yaml\n",
		old + "/.storage/core.entity_registry": "{}",
		old + "/home-assistant_v2.db":          "geschiedenis",
	})
	o.vip, o.db = true, true
	if out, err := o.run("up"); err == nil || !strings.Contains(out, "andere Home Assistant") {
		t.Errorf("up naast de oude: %v %s", err, out)
	}
	out, err = o.run("overnemen")
	if err != nil || !strings.Contains(out, "home-assistant_v2.db") {
		t.Fatalf("overnemen: %v %s", err, out)
	}
	conf, _ = os.ReadFile(o.base + "/config/configuration.yaml")
	if !strings.Contains(string(conf), "recorder: !include cf-recorder.yaml") {
		t.Errorf("overgenomen configuration.yaml: %s", conf)
	}
	if _, err := os.Stat(o.base + "/config/.storage/core.entity_registry"); err != nil {
		t.Error(".storage niet overgenomen")
	}
	if log, _ := os.ReadFile(dir + "/log"); !strings.Contains(string(log), "docker update --restart=no abc123") || !strings.Contains(string(log), "systemctl start cf-homeassistant") {
		t.Errorf("overnemen: %s", log)
	}

	// check: met Galera telt de database (hier niet bereikbaar: cf-agent
	// ontbreekt), zonder Galera niet.
	if out, err := a.run("check"); err != nil {
		t.Errorf("check zonder Galera: %v %s", err, out)
	}
	write(t, map[string]string{filepath.Dir(a.script) + "/galera.cnf": ""})
	if _, err := a.run("check"); err == nil {
		t.Error("check met Galera zonder cf-agent gaf geen fout")
	}
}
