package templates

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// hpSecret is de sleutel (secret.key) van de oude homepage in de tests; hij
// mag nooit in de uitvoer van een opdracht staan.
const hpSecret = "OudeSleutel-van-de-homepage-0123456789abcdefghij="

// hpContext is een cluster van drie homepage-nodes.
func hpContext(t *testing.T, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("homepage")
	if !ok {
		t.Fatal("geen template homepage")
	}
	in := map[string]any{"vip": "192.0.2.157", "db_vip": "192.0.2.158", "vrid": 66, "auth_pass": "geheim12",
		"db_password": "Wacht~woord.123"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "homepage", Slug: "homepage", Environment: "prod"}}
	for i, a := range []int{156, 151, 152} {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("hp-%02d", i+1), Role: "node", Index: i + 1,
			Address: fmt.Sprintf("192.0.2.%d", a), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

const (
	hpCtl  = "/usr/local/lib/clusterforge/homepage-ctl.sh"
	hpBase = "/etc/cf-homepage/base.env"
)

func TestRenderHomepage(t *testing.T) {
	tpl, c := hpContext(t, nil)
	if c.Params["nodes"] != 3 || c.Params["version"] != "main" || tpl.Failback {
		t.Fatalf("standaarden: %+v failback %v", c.Params, tpl.Failback)
	}
	files, services := npmFiles(t, tpl, c)
	for path, want := range map[string][]string{
		hpBase: {"POSTGRES_HOST=192.0.2.158", "POSTGRES_PORT=5432", "POSTGRES_USER=homepage", "POSTGRES_PASSWORD=Wacht~woord.123",
			"POSTGRES_DB=homepage"},
		"/etc/keepalived/keepalived.conf": {"virtual_router_id 66", "192.0.2.157/24 dev eth0", "nopreempt", "priority 150", "chk_homepage",
			"unicast_peer {\n        192.0.2.151\n        192.0.2.152\n    }", "include /etc/keepalived/conf.d/*.conf",
			`notify_master "/usr/local/lib/clusterforge/homepage-ctl.sh master"`, `notify_backup "/usr/local/lib/clusterforge/homepage-ctl.sh vip-weg"`},
		"/etc/systemd/system/cf-homepage-api.service": {"ExecStartPre=+/usr/local/lib/clusterforge/homepage-ctl.sh voorstart",
			"ExecStartPre=/usr/local/lib/clusterforge/homepage-ctl.sh migreren", "EnvironmentFile=-/etc/cf-homepage/homepage.env",
			"Restart=always", "User=homepage", "uvicorn app.main:app --host 127.0.0.1 --port 8000"},
		"/etc/systemd/system/cf-homepage-worker.service": {"ExecCondition=+/usr/local/lib/clusterforge/homepage-ctl.sh worker-mag",
			"ExecStart=/usr/local/lib/clusterforge/homepage-ctl.sh worker", "AmbientCapabilities=CAP_NET_RAW", "RuntimeDirectory=cf-homepage-worker"},
		"/etc/systemd/system/cf-homepage-syslog.service": {"ExecCondition=+/usr/local/lib/clusterforge/homepage-ctl.sh syslog-mag",
			"python -m app.syslog.server", "CAP_NET_BIND_SERVICE"},
		"/etc/systemd/system/cf-homepage-backup.service": {"User=postgres", "homepage-ctl.sh backup-mag", "deploy/homepage-backup.sh",
			"EnvironmentFile=/etc/cf-homepage/homepage.env"},
		"/etc/systemd/system/cf-homepage-backup.timer": {"OnCalendar=*-*-* 03:30:00"},
		"/etc/systemd/system/cf-homepage-volg.timer":   {"OnUnitActiveSec=30"},
		"/etc/systemd/system/cf-homepage-volg.service": {"homepage-ctl.sh volg"},
		hpCtl: {"VIP='192.0.2.157'", "DB_VIP='192.0.2.158'", "INDEX='1'", "ME='hp-01'", "VERSION='main'", "PEERS='192.0.2.151 192.0.2.152 '"},
		"/etc/postgresql-common/createcluster.d/clusterforge.conf": {"create_main_cluster = false"},
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
	// De worker is nooit enabled: keepalived en cf-homepage-volg starten hem.
	if strings.Contains(files["/etc/systemd/system/cf-homepage-worker.service"], "[Install]") {
		t.Error("cf-homepage-worker heeft een [Install]")
	}
	for p, body := range files {
		if p != hpBase && strings.Contains(body, "Wacht~woord.123") {
			t.Errorf("%s bevat een geheim", p)
		}
	}
	for _, s := range services {
		if strings.HasPrefix(s, "cf-homepage") {
			t.Errorf("%s hoort geen service-stap te hebben: %v", s, services)
		}
	}

	for _, bad := range []map[string]any{{"nodes": 2}, {"version": "main; rm -rf /"}} {
		in := map[string]any{"vip": "192.0.2.157", "db_vip": "192.0.2.158"}
		for k, v := range bad {
			in[k] = v
		}
		if _, err := tpl.Validate(in); err == nil {
			t.Errorf("aanvaard: %v", bad)
		}
	}

	if tpl.Database == nil || tpl.Database.Engine != "postgresql" || tpl.Database.Role != "node" ||
		tpl.Database.Settings["shared_preload_libraries"] != "timescaledb" {
		t.Fatalf("database: %+v", tpl.Database)
	}
	steps, err := tpl.RenderDBSteps(c)
	if err != nil || len(steps) != 1 || steps[0].User != "homepage" || steps[0].GrantOn != "homepage" || steps[0].Password != "Wacht~woord.123" {
		t.Errorf("database-instructies: %+v %v", steps, err)
	}
	syncs, err := tpl.RenderSyncs(c)
	if err != nil || len(syncs) != 2 {
		t.Fatalf("syncs: %+v %v", syncs, err)
	}
	if s := syncs[0]; s.Source != "/opt/cf-homepage/files/" || s.Delete {
		t.Errorf("sync instellingen: %+v", s)
	}
	if s := syncs[1]; s.Source != "/var/backups/homepage/" || s.Delete {
		t.Errorf("sync back-ups: %+v", s)
	}

	c.Node = &c.Nodes[2]
	files3, _ := npmFiles(t, tpl, c)
	if !strings.Contains(files3["/etc/keepalived/keepalived.conf"], "priority 130") || !strings.Contains(files3[hpCtl], "INDEX='3'") {
		t.Error("node 3")
	}

	if _, err := exec.LookPath("shellcheck"); err == nil {
		p := filepath.Join(t.TempDir(), "homepage-ctl.sh")
		if err := os.WriteFile(p, []byte(files[hpCtl]), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
			t.Errorf("shellcheck:\n%s", out)
		}
	}
}

var hpPrefix = fmt.Sprintf("cf_hp%d_", os.Getpid())

func hpn(s string) string { return strings.ReplaceAll(s, "cf_hp_", hpPrefix) }

// hpNode is node 1 met de oude homepage erop, nagespeeld: homepage-ctl.sh
// met eigen mappen, gestubde systemctl/curl en de echte
// psql/pg_dump/pg_restore tegen een testdatabase.
type hpNode struct {
	t   *testing.T
	dir string
	env []string
}

func newHPNode(t *testing.T, ctl, baseEnv, oudEnv, commit, oudDB string, port uint16) *hpNode {
	t.Helper()
	dir := t.TempDir()
	n := &hpNode{t: t, dir: dir}
	bin := filepath.Join(dir, "bin")
	stubs := map[string]string{
		"ip":       `exit 0`,
		"hostname": `echo jbogaerthomepage`,
		// healthz: 200 met $CF_TEST_DIR/gezond, anders niets (de api draait niet).
		"curl": `case "$*" in
*http_code*) if [ -f "$CF_TEST_DIR/gezond" ]; then printf 200; else printf 000; fi ;;
*) [ -f "$CF_TEST_DIR/gezond" ] ;;
esac`,
		"nginx": `exit 0`,
		// Na voorbereiden: alleen het hernoemde cluster van de oude homepage.
		"pg_lsclusters": `echo "17 homepageoud 5433 online postgres /var/lib/postgresql/17/homepageoud /var/log/postgresql/x.log"`,
		"pg_ctlcluster": `echo "pg_ctlcluster $*" >> "$CF_TEST_DIR/log"`,
		// De oude homepage is "actief" zolang $CF_TEST_DIR/oud-actief bestaat.
		"systemctl": `echo "systemctl $*" >> "$CF_TEST_DIR/log"
case "$*" in
"is-active --quiet homepage-api homepage-worker homepage-syslog"|"is-enabled --quiet homepage-api") [ -f "$CF_TEST_DIR/oud-actief" ] ;;
"disable --now homepage-api homepage-worker homepage-syslog homepage-backup.timer homepage-backuptest.timer") rm -f "$CF_TEST_DIR/oud-actief" ;;
"enable --now homepage-api homepage-worker homepage-syslog homepage-backup.timer homepage-backuptest.timer") touch "$CF_TEST_DIR/oud-actief" ;;
"is-active --quiet nginx") exit 0 ;;
is-active*) exit 3 ;;
*) exit 0 ;;
esac`,
	}
	for cmd, body := range stubs {
		write(t, map[string]string{filepath.Join(bin, cmd): "#!/bin/sh\n" + body + "\n"})
		if err := os.Chmod(filepath.Join(bin, cmd), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	code := filepath.Join(dir, "code")
	write(t, map[string]string{
		filepath.Join(dir, "homepage-ctl.sh"):                       ctl,
		filepath.Join(dir, "etc", "base.env"):                       baseEnv,
		filepath.Join(dir, "oud", "homepage.env"):                   oudEnv,
		filepath.Join(dir, "oud", "secret.key"):                     hpSecret + "\n",
		filepath.Join(dir, "oud", "setup-token"):                    "setup-123\n",
		filepath.Join(dir, "oud", "versie"):                         "versie=v1.4.0\ncommit=" + commit + "\nkanaal=stabiel\n",
		filepath.Join(dir, "oud", "homepage-api.service"):           "[Service]\n",
		filepath.Join(dir, "user_clusters"):                         "# cf-homepage: standaard Patroni op 5432, niet homepageoud\n* * 17 /var/run/postgresql:5432 *\n",
		filepath.Join(dir, "state", "gebouwd"):                      "main " + commit + "\n",
		filepath.Join(code, "backend/alembic/versions/0025_uur.py"): "revision = '0025'\ndown_revision = '0024'\n",
		filepath.Join(code, "deploy/nginx.conf"): "server {\n    listen 80 default_server;\n    listen [::]:80 default_server;\n" +
			"    set_real_ip_from __NPM_IP__;\n    root /opt/homepage/frontend/dist;\n    location /api/ { proxy_pass http://127.0.0.1:8000; }\n}\n",
		filepath.Join(code, "deploy/nginx-map.conf"):     "map $realip_remote_addr $homepage_from_npm {\n    \"~^__NPM_IP_RE__$\" 1;\n}\ngeo $x $y {\n    __LAN_NETS__\n}\n\"~^..\\|lan\\|\" __LAN_DICHT__;\n",
		filepath.Join(code, "deploy/nginx-headers.conf"): "add_header X-Frame-Options DENY;\n",
		filepath.Join(code, ".venv/bin/python"):          "#!/bin/sh\necho \"worker $$\" >> \"$CF_TEST_DIR/worker.log\"\nexec sleep 300\n",
		filepath.Join(code, ".venv/bin/alembic"):         "#!/bin/sh\necho \"alembic $*\"\n[ ! -f \"$CF_TEST_DIR/alembic-faalt\" ]\n",
		filepath.Join(dir, "oud-actief"):                 "",
	})
	for _, p := range []string{".venv/bin/python", ".venv/bin/alembic"} {
		if err := os.Chmod(filepath.Join(code, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	n.env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "CF_TEST_DIR=" + dir, "CF_HP_TEST=1",
		"CF_HP_BASE=" + filepath.Join(dir, "opt"), "CF_HP_STATE=" + filepath.Join(dir, "state"), "CF_HP_ETC=" + filepath.Join(dir, "etc"),
		"CF_HP_CODE=" + code, "CF_HP_BACKUPS=" + filepath.Join(dir, "backups"), "CF_HP_OUD_BACKUPS=" + filepath.Join(dir, "oud", "backups"),
		"CF_HP_RUN=" + filepath.Join(dir, "run"), "CF_HP_NGINX=" + filepath.Join(dir, "nginx"),
		"CF_HP_OUD_ETC=" + filepath.Join(dir, "oud"), "CF_HP_OUD_UNIT=" + filepath.Join(dir, "oud", "homepage-api.service"),
		"CF_HP_OUD_DB=" + oudDB, "CF_HP_OUD_PORT_NEW=" + fmt.Sprint(port), "CF_HP_SU_PORT=" + fmt.Sprint(port),
		"CF_HP_USER_CLUSTERS=" + filepath.Join(dir, "user_clusters"), "CF_HP_SLEEP=0", "CF_HP_WAIT=2", "CF_HP_WAIT_CHECKS=1", "CF_HP_MD5_MAX=100000"}
	return n
}

func (n *hpNode) cmd(extra []string, args ...string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(n.dir, "homepage-ctl.sh")}, args...)...)
	cmd.Env = append(append(os.Environ(), n.env...), extra...)
	return cmd
}

func (n *hpNode) run(args ...string) (string, error) {
	n.t.Helper()
	out, err := n.cmd(nil, args...).CombinedOutput()
	if strings.Contains(string(out), hpSecret) || strings.Contains(string(out), "Wacht~woord") {
		n.t.Errorf("een geheim staat in de uitvoer van %v", args)
	}
	return string(out), err
}

func (n *hpNode) exists(p string) bool { _, err := os.Stat(filepath.Join(n.dir, p)); return err == nil }

func (n *hpNode) read(p string) string {
	b, _ := os.ReadFile(filepath.Join(n.dir, p))
	return string(b)
}

// TestHomepageOvernemen neemt een homepage over in een echte PostgreSQL:
// eerst een overname die faalt (de api van het cluster komt niet op) en dus
// terugdraait, dan een die lukt, dan het slot van de checks, dan de terugweg.
func TestHomepageOvernemen(t *testing.T) {
	url := os.Getenv("CF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CF_TEST_DATABASE_URL ontbreekt")
	}
	for _, tool := range []string{"psql", "pg_dump", "pg_restore", "sha256sum", "python3", "mkfifo"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " staat niet op deze machine")
		}
	}
	if out, err := exec.Command("psql", "--version").Output(); err != nil || strings.Contains(string(out), " 15.") || strings.Contains(string(out), " 14.") {
		t.Skip("psql 16 of nieuwer nodig (SHELL_ERROR)")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	cfg := admin.Config()
	exe := func(conn *pgx.Conn, q string) {
		t.Helper()
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	drop := []string{hpn("DROP DATABASE IF EXISTS cf_hp_oud WITH (FORCE)"), hpn("DROP DATABASE IF EXISTS cf_hp_doel WITH (FORCE)"), hpn("DROP ROLE IF EXISTS cf_hp_eigenaar")}
	for _, q := range drop {
		exe(admin, q)
	}
	exe(admin, hpn("CREATE ROLE cf_hp_eigenaar LOGIN PASSWORD 'Wacht~woord.123'"))
	exe(admin, hpn("CREATE DATABASE cf_hp_oud"))
	exe(admin, hpn("CREATE DATABASE cf_hp_doel OWNER cf_hp_eigenaar"))
	t.Cleanup(func() {
		for _, q := range drop {
			_, _ = admin.Exec(context.Background(), q)
		}
	})

	// De oude database: een stukje van het schema van de homepage, van de
	// gebruiker van de homepage.
	oudCfg := cfg.Copy()
	oudCfg.Database = hpn("cf_hp_oud")
	oud, err := pgx.ConnectConfig(ctx, oudCfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		hpn("GRANT ALL ON SCHEMA public TO cf_hp_eigenaar"),
		hpn("SET ROLE cf_hp_eigenaar"),
		"CREATE TABLE alembic_version (version_num varchar(32) PRIMARY KEY)",
		"INSERT INTO alembic_version VALUES ('0025')",
		"CREATE TABLE services (id serial PRIMARY KEY, name text, \"check\" jsonb, created_at timestamptz)",
		"INSERT INTO services (name, \"check\", created_at) SELECT 'svc-' || i, jsonb_build_object('type', 'ping', 'interval', 60), now() - i * interval '1 hour' FROM generate_series(1, 40) i",
		"CREATE TABLE check_results (ts timestamptz, service_id int, ok bool, ms double precision)",
		"INSERT INTO check_results SELECT now() - i * interval '1 minute', i % 40 + 1, i % 7 <> 0, random() * 100 FROM generate_series(1, 3000) i",
		"CREATE TABLE secrets (id int PRIMARY KEY, blob bytea)",
		"INSERT INTO secrets SELECT i, decode(md5(i::text), 'hex') FROM generate_series(1, 20) i",
		"CREATE TABLE leeg (id int)",
	} {
		exe(oud, q)
	}
	_ = oud.Close(ctx)

	host, port := cfg.Host, cfg.Port
	t.Setenv("PGHOST", host)
	t.Setenv("PGUSER", cfg.User)
	if cfg.Password != "" {
		t.Setenv("PGPASSWORD", cfg.Password)
	}
	baseEnv := fmt.Sprintf("POSTGRES_HOST=%s\nPOSTGRES_PORT=%d\nPOSTGRES_USER=%s\nPOSTGRES_PASSWORD=Wacht~woord.123\nPOSTGRES_DB=%s\n",
		host, port, hpn("cf_hp_eigenaar"), hpn("cf_hp_doel"))
	oudEnv := fmt.Sprintf("HOMEPAGE_DATABASE_URL=postgresql+asyncpg://homepage@/homepage?host=/var/run/postgresql&port=%d\n"+
		"HOMEPAGE_SECRET_KEY_FILE=/etc/homepage/secret.key\nHOMEPAGE_COOKIE_SECURE=auto\nHOMEPAGE_LAN_OPEN=0\nHOMEPAGE_NPM_IP=192.0.2.10\n"+
		"HOMEPAGE_LAN_NETS=2a02:1810:abcd:1200::/56\nHOMEPAGE_DB_POOL_WORKER=60\nHOMEPAGE_SYSLOG_TARGET=192.0.2.156\nHOMEPAGE_SITE_NAME=thuis\n# commentaar\n", port)

	tpl, c := hpContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	commit := "0123456789abcdef0123456789abcdef01234567"
	n := newHPNode(t, files[hpCtl], baseEnv, oudEnv, commit, hpn("cf_hp_oud"), port)
	doelCfg := cfg.Copy()
	doelCfg.Database = hpn("cf_hp_doel")
	doelCount := func(q string) (int, error) {
		conn, err := pgx.ConnectConfig(ctx, doelCfg)
		if err != nil {
			return 0, err
		}
		defer func() { _ = conn.Close(ctx) }()
		var n int
		err = conn.QueryRow(ctx, q).Scan(&n)
		return n, err
	}

	if out, err := n.run("voorbereid"); err != nil {
		t.Fatalf("voorbereid: %v %s", err, out)
	}
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "oude homepage draait nog") {
		t.Fatalf("voorstart vóór de overname: %v\n%s", err, out)
	}
	for _, cmd := range []string{"worker-mag", "syslog-mag"} {
		if out, err := n.run(cmd); err == nil {
			t.Fatalf("%s vóór de overname: %s", cmd, out)
		}
	}
	// Vóór de overname blijft de nginx van de oude homepage.
	if out, err := n.run("nginx-ok"); err != nil {
		t.Fatalf("nginx-ok vóór de overname: %v %s", err, out)
	}

	// 1. De api van het cluster komt niet op: alles terugdraaien.
	out, err := n.run("overnemen")
	if err == nil || !strings.Contains(out, "antwoordt niet") || !strings.Contains(out, "oude homepage draait weer") {
		t.Fatalf("overname zonder api: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Error("de oude homepage is na het terugdraaien niet gestart")
	}
	for _, f := range []string{"opt/files/.cf/homepage.env", "opt/files/.cf/secret.key"} {
		if n.exists(f) {
			t.Errorf("%s bleef staan na het terugdraaien", f)
		}
	}
	if tables, err := doelCount("SELECT count(*) FROM pg_tables WHERE schemaname = 'public'"); err != nil || tables != 0 {
		t.Fatalf("database van het cluster na het terugdraaien: %d tabellen %v", tables, err)
	}
	if m, _ := filepath.Glob(filepath.Join(n.dir, "oud", "backups", "cf-overname-*.dump")); len(m) == 0 {
		t.Errorf("back-up van de oude database: %v", m)
	}

	// 2. Een andere commit zonder --andere-versie: weigeren vóór de stop.
	write(t, map[string]string{filepath.Join(n.dir, "state", "gebouwd"): "main 0000000000000000000000000000000000000000\n"})
	if out, err := n.run("overnemen"); err == nil || !strings.Contains(out, "Zet Versie van de homepage op "+commit) {
		t.Fatalf("andere commit: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Fatal("de oude homepage werd gestopt bij een geweigerde overname")
	}
	write(t, map[string]string{filepath.Join(n.dir, "state", "gebouwd"): "main " + commit + "\n"})

	// 3. Geslaagde overname.
	write(t, map[string]string{filepath.Join(n.dir, "gezond"): ""})
	out, err = n.run("overnemen")
	if err != nil || !strings.Contains(out, "Overgenomen") {
		t.Fatalf("overname: %v\n%s", err, out)
	}
	for _, w := range []string{"services", "aantal+md5 ja", "check_results", "aantal ja", "TimescaleDB geen", "Oude homepage stoppen",
		"http://192.0.2.157 poort 80", "checks draaien op één node"} {
		if !strings.Contains(out, w) {
			t.Errorf("uitvoer mist %q:\n%s", w, out)
		}
	}
	if n.exists("oud-actief") {
		t.Error("de oude homepage draait nog na de overname")
	}
	for tb, want := range map[string]int{"services": 40, "check_results": 3000, "secrets": 20, "alembic_version": 1} {
		if got, err := doelCount("SELECT count(*) FROM " + tb); err != nil || got != want {
			t.Errorf("%s: %d %v", tb, got, err)
		}
	}
	// Van de gebruiker van de homepage, zodat de api erin kan schrijven.
	if got, err := doelCount(hpn("SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tableowner <> 'cf_hp_eigenaar'")); err != nil || got != 0 {
		t.Errorf("tabellen van een andere eigenaar: %d %v", got, err)
	}
	conn, err := pgx.ConnectConfig(ctx, doelCfg)
	if err != nil {
		t.Fatal(err)
	}
	var meta string
	if err := conn.QueryRow(ctx, "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = current_database()").Scan(&meta); err != nil ||
		!strings.Contains(meta, "cf-homepage key=") || !strings.Contains(meta, "commit="+commit) {
		t.Errorf("markering: %q %v", meta, err)
	}
	_ = conn.Close(ctx)
	for f, mode := range map[string]os.FileMode{"opt/files/.cf/homepage.env": 0o600, "opt/files/.cf/secret.key": 0o600} {
		st, err := os.Stat(filepath.Join(n.dir, f))
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", f, st, err)
		}
	}
	cfEnv := n.read("opt/files/.cf/homepage.env")
	for _, bad := range []string{"HOMEPAGE_DATABASE_URL", "HOMEPAGE_SECRET_KEY_FILE", "HOMEPAGE_DB_POOL", "HOMEPAGE_SYSLOG_TARGET", "commentaar"} {
		if strings.Contains(cfEnv, bad) {
			t.Errorf("overgenomen instellingen bevatten %s:\n%s", bad, cfEnv)
		}
	}
	if !strings.Contains(cfEnv, "HOMEPAGE_NPM_IP=192.0.2.10") || !strings.Contains(cfEnv, "HOMEPAGE_SITE_NAME=thuis") {
		t.Errorf("overgenomen instellingen:\n%s", cfEnv)
	}
	if n.read("opt/files/.cf/secret.key") != hpSecret+"\n" || n.exists("opt/files/.cf/timescaledb") {
		t.Error("secret.key niet overgenomen, of een TimescaleDB-versie zonder TimescaleDB")
	}

	// Een tweede overname weigert.
	write(t, map[string]string{filepath.Join(n.dir, "oud-actief"): ""})
	if out, err := n.run("overnemen"); err == nil || !strings.Contains(out, "al in gebruik") {
		t.Fatalf("tweede overname: %v\n%s", err, out)
	}
	_ = os.Remove(filepath.Join(n.dir, "oud-actief"))

	// voorstart: de instellingen van de oude node, met de database van het
	// cluster, de sleutel en nginx; worker en syslog mogen nu.
	if out, err := n.run("voorstart"); err != nil {
		t.Fatalf("voorstart: %v\n%s", err, out)
	}
	appEnv := n.read("etc/homepage.env")
	for _, w := range []string{
		fmt.Sprintf("HOMEPAGE_DATABASE_URL=postgresql+asyncpg://%s:Wacht~woord.123@%s:%d/%s", hpn("cf_hp_eigenaar"), host, port, hpn("cf_hp_doel")),
		"HOMEPAGE_SECRET_KEY_FILE=" + filepath.Join(n.dir, "etc", "secret.key"), "HOMEPAGE_SYSLOG_TARGET=192.0.2.157",
		"HOMEPAGE_DB_POOL_WORKER=40", "HOMEPAGE_SITE_NAME=thuis", "PGPASSWORD=Wacht~woord.123", "PGDATABASE=" + hpn("cf_hp_doel"),
	} {
		if !strings.Contains(appEnv, w) {
			t.Errorf("homepage.env mist %q:\n%s", w, appEnv)
		}
	}
	if strings.Contains(appEnv, "DB_POOL_WORKER=60") || strings.Contains(appEnv, "192.0.2.156") {
		t.Errorf("homepage.env neemt de verbindingen of het syslog-doel van de oude node over:\n%s", appEnv)
	}
	if n.read("etc/secret.key") != hpSecret+"\n" {
		t.Error("secret.key staat niet in /etc/cf-homepage")
	}
	site, hpMap := n.read("nginx/sites-available/homepage"), n.read("nginx/conf.d/homepage-map.conf")
	if !strings.Contains(site, "set_real_ip_from 192.0.2.10;") || !strings.Contains(hpMap, `"~^192\.0\.2\.10$" 1;`) ||
		!strings.Contains(hpMap, "2a02:1810:abcd:1200::/56 lan;") || !strings.Contains(hpMap, `"~^..\|lan\|" 1;`) {
		t.Errorf("nginx:\n%s\n%s", site, hpMap)
	}
	if !strings.Contains(n.read("etc/nginx"), "lan=dicht") || !strings.Contains(n.read("etc/nginx"), "npm=192.0.2.10") {
		t.Errorf("staat van nginx: %s", n.read("etc/nginx"))
	}
	if out, err := n.run("nginx-ok"); err != nil {
		t.Errorf("nginx-ok na voorstart: %v %s", err, out)
	}
	for _, cmd := range []string{"worker-mag", "syslog-mag", "unchanged"} {
		if out, err := n.run(cmd); err != nil {
			t.Errorf("%s na de overname: %v %s", cmd, err, out)
		}
	}
	if out, err := n.run("master"); err != nil {
		t.Errorf("master: %v %s", err, out)
	}
	if lg := n.read("log"); !strings.Contains(lg, "systemctl start --no-block cf-homepage-worker") {
		t.Errorf("master startte de worker niet:\n%s", lg)
	}

	// Migraties onder het slot, met de omgeving van cf-homepage-api.
	appVars := strings.Split(strings.TrimSpace(appEnv), "\n")
	if out, err := n.cmd(appVars, "migreren").CombinedOutput(); err != nil || !strings.Contains(string(out), "alembic upgrade head") {
		t.Errorf("migreren: %v\n%s", err, out)
	}
	write(t, map[string]string{filepath.Join(n.dir, "alembic-faalt"): ""})
	if out, err := n.cmd(appVars, "migreren").CombinedOutput(); err == nil || !strings.Contains(string(out), "alembic upgrade head mislukte") {
		t.Errorf("migreren met een fout: %v\n%s", err, out)
	}

	// Het slot van de checks: de tweede worker wacht tot de eerste stopt.
	startWorker := func(name string) (*exec.Cmd, *bytes.Buffer) {
		t.Helper()
		var buf bytes.Buffer
		cmd := n.cmd(append(appVars, "RUNTIME_DIRECTORY="+filepath.Join(n.dir, "run-"+name)), "worker")
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
		return cmd, &buf
	}
	waitFor := func(buf *bytes.Buffer, s string) bool {
		for range 100 {
			if strings.Contains(buf.String(), s) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	a, aOut := startWorker("a")
	if !waitFor(aOut, "Slot genomen") {
		t.Fatalf("worker a kreeg het slot niet:\n%s", aOut)
	}
	out, _ = n.run("status")
	if !strings.Contains(out, "checks:      draaien op hp-01") {
		t.Errorf("status toont niet wie de checks doet:\n%s", out)
	}
	_, bOut := startWorker("b")
	if !waitFor(bOut, "Wacht op het slot") {
		t.Fatalf("worker b:\n%s", bOut)
	}
	time.Sleep(1500 * time.Millisecond)
	if strings.Contains(bOut.String(), "Slot genomen") {
		t.Fatalf("twee workers met het slot:\n%s\n%s", aOut, bOut)
	}
	_ = a.Process.Signal(syscall.SIGTERM)
	if err := a.Wait(); err != nil {
		t.Logf("worker a stopte: %v", err)
	}
	if !waitFor(bOut, "Slot genomen") {
		t.Fatalf("worker b kreeg het slot niet na de stop van a:\n%s", bOut)
	}
	if strings.Count(n.read("worker.log"), "worker ") != 2 {
		t.Errorf("worker.log:\n%s", n.read("worker.log"))
	}

	// Een node met een andere sleutel start niet.
	write(t, map[string]string{filepath.Join(n.dir, "opt", "files", ".cf", "secret.key"): "een-andere-sleutel\n"})
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "niet die van het cluster") {
		t.Fatalf("andere sleutel: %v\n%s", err, out)
	}
	write(t, map[string]string{filepath.Join(n.dir, "opt", "files", ".cf", "secret.key"): hpSecret + "\n"})

	// Terugweg: dump van het cluster, markering weg, oude homepage en haar
	// nginx weer aan.
	out, err = n.run("terug")
	if err != nil || !strings.Contains(out, "Terug op de oude homepage") {
		t.Fatalf("terug: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Error("de oude homepage draait niet na terug")
	}
	if m, _ := filepath.Glob(filepath.Join(n.dir, "state", "overname", "cluster-*.dump")); len(m) != 1 {
		t.Errorf("dump van het cluster: %v", m)
	}
	_ = os.Remove(filepath.Join(n.dir, "oud-actief"))
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "nog niet overgenomen") {
		t.Fatalf("voorstart na terug: %v\n%s", err, out)
	}
}
