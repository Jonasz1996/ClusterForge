package templates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// ctSecret is de sleutel van de oude cssthema in de tests; hij mag nooit in
// de uitvoer van een opdracht staan.
const ctSecret = "OudeSleutel-van-cssthema-0123456789abcdefghijklmnop"

// ctContext is een cluster van drie cssthema-nodes.
func ctContext(t *testing.T, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("cssthema")
	if !ok {
		t.Fatal("geen template cssthema")
	}
	in := map[string]any{"vip": "192.0.2.160", "db_vip": "192.0.2.161", "vrid": 64, "auth_pass": "geheim12",
		"db_password": "Wachtwoord-123", "secret_key": "sleutel-van-de-template-1234567890-abcdefghijk"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "cssthema", Slug: "cssthema", Environment: "prod"}}
	for i, a := range []int{154, 155, 156} {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("ct-%02d", i+1), Role: "node", Index: i + 1,
			Address: fmt.Sprintf("192.0.2.%d", a), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

const (
	ctCtl  = "/usr/local/lib/clusterforge/cssthema-ctl.sh"
	ctBase = "/etc/cf-cssthema/base.env"
)

func TestRenderCssthema(t *testing.T) {
	tpl, c := ctContext(t, nil)
	if c.Params["nodes"] != 3 || c.Params["version"] != "main" || c.Params["nieuw"] != false || tpl.Failback {
		t.Fatalf("standaarden: %+v failback %v", c.Params, tpl.Failback)
	}
	files, services := npmFiles(t, tpl, c)
	for path, want := range map[string][]string{
		ctBase: {"POSTGRES_HOST=192.0.2.161", "POSTGRES_PORT=5432", "POSTGRES_USER=cssthema", "POSTGRES_PASSWORD=Wachtwoord-123",
			"POSTGRES_DB=cssthema", "REDIS_URL=redis://localhost:6379/0"},
		"/etc/keepalived/keepalived.conf": {"virtual_router_id 64", "192.0.2.160/24 dev eth0", "nopreempt", "priority 150", "chk_cssthema",
			"unicast_peer {\n        192.0.2.155\n        192.0.2.156\n    }", "include /etc/keepalived/conf.d/*.conf",
			`notify_master "/usr/local/lib/clusterforge/cssthema-ctl.sh master"`, `notify_fault "/usr/local/lib/clusterforge/cssthema-ctl.sh vip-weg"`},
		"/etc/systemd/system/cf-cssthema-api.service": {"ExecStartPre=+/usr/local/lib/clusterforge/cssthema-ctl.sh voorstart",
			"alembic upgrade head", "EnvironmentFile=-/etc/cf-cssthema/cssthema.env", "Restart=always", "User=cssthema",
			"uvicorn cssthema.main:app --host 127.0.0.1 --port 8000"},
		"/etc/systemd/system/cf-cssthema-worker.service": {"ExecCondition=+/usr/local/lib/clusterforge/cssthema-ctl.sh worker-mag",
			"arq cssthema.worker.WorkerSettings"},
		"/etc/systemd/system/cf-cssthema-backup.timer":   {"OnCalendar=*-*-* 03:25"},
		"/etc/systemd/system/cf-cssthema-backup.service": {"cssthema-ctl.sh backup"},
		ctCtl: {"VIP='192.0.2.160'", "DB_VIP='192.0.2.161'", "INDEX='1'", "VERSION='main'", "NIEUW='false'", "PEERS='192.0.2.155 192.0.2.156 '"},
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
	// De worker is nooit enabled: keepalived start hem op de VIP-node.
	if strings.Contains(files["/etc/systemd/system/cf-cssthema-worker.service"], "[Install]") {
		t.Error("cf-cssthema-worker heeft een [Install]")
	}
	// Bij een overname staat geen sleutel van de template in base.env, en
	// nooit een geheim in een script of de keepalived-config.
	if strings.Contains(files[ctBase], "SECRET_KEY") {
		t.Errorf("base.env bij een overname bevat een sleutel:\n%s", files[ctBase])
	}
	for p, body := range files {
		if p != ctBase && (strings.Contains(body, "sleutel-van-de-template") || strings.Contains(body, "Wachtwoord-123")) {
			t.Errorf("%s bevat een geheim", p)
		}
	}
	for _, s := range services {
		if strings.HasPrefix(s, "cf-cssthema") {
			t.Errorf("%s hoort geen service-stap te hebben: %v", s, services)
		}
	}

	// Een nieuw cluster: sleutel en hostnamen in base.env.
	tplN, cN := ctContext(t, map[string]any{"nieuw": true, "dashboard_host": "cssthema.example.be",
		"public_base_url": "https://css.example.be", "trusted_proxies": "192.0.2.10"})
	filesN, _ := npmFiles(t, tplN, cN)
	for _, w := range []string{"SECRET_KEY=sleutel-van-de-template", "DASHBOARD_HOST=cssthema.example.be",
		"PUBLIC_BASE_URL=https://css.example.be", "TRUSTED_PROXIES=192.0.2.10"} {
		if !strings.Contains(filesN[ctBase], w) {
			t.Errorf("nieuw cluster: base.env mist %q:\n%s", w, filesN[ctBase])
		}
	}

	for _, bad := range []map[string]any{{"nodes": 2}, {"version": "main; rm -rf /"}, {"public_base_url": "javascript:alert(1)"},
		{"trusted_proxies": "1.2.3.4; rm"}, {"dashboard_host": "Host met spatie"}} {
		in := map[string]any{"vip": "192.0.2.160", "db_vip": "192.0.2.161"}
		for k, v := range bad {
			in[k] = v
		}
		if _, err := tpl.Validate(in); err == nil {
			t.Errorf("aanvaard: %v", bad)
		}
	}

	if tpl.Database == nil || tpl.Database.Engine != "postgresql" || tpl.Database.Role != "node" {
		t.Fatalf("database: %+v", tpl.Database)
	}
	steps, err := tpl.RenderDBSteps(c)
	if err != nil || len(steps) != 1 || steps[0].User != "cssthema" || steps[0].GrantOn != "cssthema" || steps[0].Password != "Wachtwoord-123" {
		t.Errorf("database-instructies: %+v %v", steps, err)
	}
	syncs, err := tpl.RenderSyncs(c)
	if err != nil || len(syncs) != 2 {
		t.Fatalf("syncs: %+v %v", syncs, err)
	}
	if s := syncs[0]; s.Source != "/opt/cf-cssthema/files/" || s.Delete {
		t.Errorf("sync instellingen: %+v", s)
	}
	if s := syncs[1]; s.Source != "/var/lib/cssthema/" || s.Delete || len(s.Exclude) != 2 {
		t.Errorf("sync bestanden: %+v", s)
	}

	c.Node = &c.Nodes[2]
	files3, _ := npmFiles(t, tpl, c)
	if !strings.Contains(files3["/etc/keepalived/keepalived.conf"], "priority 130") || !strings.Contains(files3[ctCtl], "INDEX='3'") {
		t.Error("node 3")
	}

	if _, err := exec.LookPath("shellcheck"); err == nil {
		p := filepath.Join(t.TempDir(), "cssthema-ctl.sh")
		if err := os.WriteFile(p, []byte(files[ctCtl]), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
			t.Errorf("shellcheck:\n%s", out)
		}
	}
}

var ctPrefix = fmt.Sprintf("cf_ct%d_", os.Getpid())

func ctn(s string) string { return strings.ReplaceAll(s, "cf_ct_", ctPrefix) }

// ctNode is node 1 met de oude cssthema erop, nagespeeld: cssthema-ctl.sh
// met eigen mappen, gestubde systemctl/curl en de echte
// psql/pg_dump/pg_restore tegen een testdatabase.
type ctNode struct {
	t   *testing.T
	dir string
	env []string
}

func newCTNode(t *testing.T, ctl, baseEnv, oudEnv, commit, oudDB string, oudPort uint16) *ctNode {
	t.Helper()
	dir := t.TempDir()
	n := &ctNode{t: t, dir: dir}
	bin := filepath.Join(dir, "bin")
	stubs := map[string]string{
		"ip":        `exit 0`,
		"hostname":  `echo jbogaertcssthema`,
		"curl":      `[ -f "$CF_TEST_DIR/gezond" ]`,
		"nginx":     `exit 0`,
		"redis-cli": `echo "redis-cli $*" >> "$CF_TEST_DIR/log"`,
		// Na voorbereiden: alleen het hernoemde cluster van de oude cssthema.
		"pg_lsclusters": `echo "17 cssthemaoud 5433 online postgres /var/lib/postgresql/17/cssthemaoud /var/log/postgresql/x.log"`,
		"pg_ctlcluster": `echo "pg_ctlcluster $*" >> "$CF_TEST_DIR/log"`,
		// De oude cssthema is "actief" zolang $CF_TEST_DIR/oud-actief bestaat.
		"systemctl": `echo "systemctl $*" >> "$CF_TEST_DIR/log"
case "$*" in
"is-active --quiet cssthema-api cssthema-worker"|"is-enabled --quiet cssthema-api") [ -f "$CF_TEST_DIR/oud-actief" ] ;;
"disable --now cssthema-api cssthema-worker") rm -f "$CF_TEST_DIR/oud-actief" ;;
"enable --now cssthema-api cssthema-worker") touch "$CF_TEST_DIR/oud-actief" ;;
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
		filepath.Join(dir, "cssthema-ctl.sh"):                                  ctl,
		filepath.Join(dir, "etc", "base.env"):                                  baseEnv,
		filepath.Join(dir, "oud", "cssthema.env"):                              oudEnv,
		filepath.Join(dir, "oud", "cssthema-api.service"):                      "[Service]\n",
		filepath.Join(dir, "user_clusters"):                                    "# cf-cssthema: standaard Patroni op 5432, niet cssthemaoud\n* * 17 /var/run/postgresql:5432 *\n",
		filepath.Join(dir, "state", "gebouwd"):                                 "main " + commit + "\n",
		filepath.Join(dir, "data", "geinstalleerde-commit"):                    commit + "\n",
		filepath.Join(code, "backend/migrations/versions/0007_host_checks.py"): "revision: str = \"0007\"\n",
		filepath.Join(dir, "runtime", "venv/bin/uvicorn"):                      "",
		filepath.Join(dir, "oud-actief"):                                       "",
	})
	if err := os.Chmod(filepath.Join(dir, "runtime", "venv/bin/uvicorn"), 0o755); err != nil {
		t.Fatal(err)
	}
	n.env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "CF_TEST_DIR=" + dir, "CF_CT_TEST=1",
		"CF_CT_BASE=" + filepath.Join(dir, "opt"), "CF_CT_STATE=" + filepath.Join(dir, "state"), "CF_CT_ETC=" + filepath.Join(dir, "etc"),
		"CF_CT_DATA=" + filepath.Join(dir, "data"), "CF_CT_CODE=" + code, "CF_CT_RUNTIME=" + filepath.Join(dir, "runtime"),
		"CF_CT_BACKUPS=" + filepath.Join(dir, "backups"), "CF_CT_OUD_BACKUPS=" + filepath.Join(dir, "oud", "backups"),
		"CF_CT_RUN=" + filepath.Join(dir, "run"), "CF_CT_NGINX=" + filepath.Join(dir, "nginx"),
		"CF_CT_OUD_ENV=" + filepath.Join(dir, "oud", "cssthema.env"), "CF_CT_OUD_UNIT=" + filepath.Join(dir, "oud", "cssthema-api.service"),
		"CF_CT_OUD_DB=" + oudDB, "CF_CT_OUD_PORT_NEW=" + fmt.Sprint(oudPort), "CF_CT_USER_CLUSTERS=" + filepath.Join(dir, "user_clusters"),
		"CF_CT_SLEEP=0", "CF_CT_WAIT=2", "CF_CT_MD5_MAX=100000"}
	return n
}

func (n *ctNode) run(args ...string) (string, error) {
	n.t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(n.dir, "cssthema-ctl.sh")}, args...)...)
	cmd.Env = append(os.Environ(), n.env...)
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), ctSecret) {
		n.t.Errorf("de sleutel staat in de uitvoer van %v", args)
	}
	return string(out), err
}

func (n *ctNode) exists(p string) bool { _, err := os.Stat(filepath.Join(n.dir, p)); return err == nil }

// TestCssthemaOvernemen neemt een cssthema over in een echte PostgreSQL:
// eerst een overname die faalt (cssthema van het cluster wordt niet gezond)
// en dus terugdraait, dan een die lukt, dan de terugweg.
func TestCssthemaOvernemen(t *testing.T) {
	url := os.Getenv("CF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CF_TEST_DATABASE_URL ontbreekt")
	}
	for _, tool := range []string{"psql", "pg_dump", "pg_restore", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " staat niet op deze machine")
		}
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
	drop := []string{ctn("DROP DATABASE IF EXISTS cf_ct_oud WITH (FORCE)"), ctn("DROP DATABASE IF EXISTS cf_ct_doel WITH (FORCE)"), ctn("DROP ROLE IF EXISTS cf_ct_eigenaar")}
	for _, q := range drop {
		exe(admin, q)
	}
	exe(admin, ctn("CREATE ROLE cf_ct_eigenaar LOGIN PASSWORD 'Wachtwoord-123'"))
	exe(admin, ctn("CREATE DATABASE cf_ct_oud"))
	exe(admin, ctn("CREATE DATABASE cf_ct_doel OWNER cf_ct_eigenaar"))
	t.Cleanup(func() {
		for _, q := range drop {
			_, _ = admin.Exec(context.Background(), q)
		}
	})

	// De oude database: een stukje van het schema van cssthema, met pg_trgm
	// (zoals migratie 0001), bytea, tijden, json en een grote tabel.
	oudCfg := cfg.Copy()
	oudCfg.Database = ctn("cf_ct_oud")
	oud, err := pgx.ConnectConfig(ctx, oudCfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"CREATE TABLE alembic_version (version_num varchar(32) PRIMARY KEY)",
		"INSERT INTO alembic_version VALUES ('0007')",
		"CREATE TABLE themes (id uuid PRIMARY KEY, slug text, css text, meta jsonb, created_at timestamptz)",
		"INSERT INTO themes SELECT gen_random_uuid(), 'thema-' || i, ':root{--c:#' || i || '}', jsonb_build_object('n', i), now() - i * interval '1 hour' FROM generate_series(1, 40) i",
		"CREATE INDEX themes_slug_trgm ON themes USING gin (slug gin_trgm_ops)",
		"CREATE TABLE assets (sha256 bytea PRIMARY KEY, storage_key text, size bigint)",
		"INSERT INTO assets SELECT decode(md5(i::text) || md5(i::text), 'hex'), 'assets/' || i, i * 1000 FROM generate_series(1, 30) i",
		"CREATE TABLE snapshots (id int PRIMARY KEY, data bytea)",
		"INSERT INTO snapshots SELECT i, (SELECT decode(string_agg(md5(random()::text || i || g), ''), 'hex') FROM generate_series(1, 200) g) FROM generate_series(1, 300) i",
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
	baseEnv := fmt.Sprintf("POSTGRES_HOST=%s\nPOSTGRES_PORT=%d\nPOSTGRES_USER=%s\nPOSTGRES_PASSWORD=Wachtwoord-123\nPOSTGRES_DB=%s\nREDIS_URL=redis://localhost:6379/0\n",
		host, port, ctn("cf_ct_eigenaar"), ctn("cf_ct_doel"))
	oudEnv := fmt.Sprintf("ENVIRONMENT=production\nPUBLIC_BASE_URL=https://css.example.be\nDASHBOARD_HOST=cssthema.example.be\n"+
		"TRUSTED_PROXIES=192.0.2.10\nPOSTGRES_HOST=localhost\nPOSTGRES_PORT=%d\nPOSTGRES_USER=cssthema\nPOSTGRES_PASSWORD=oud-wachtwoord\n"+
		"POSTGRES_DB=cssthema\nREDIS_URL=redis://localhost:6379/0\nSECRET_KEY=%s\nENCRYPTION_KEY=\nBACKUP_DIR=/mnt/back-up\n# commentaar\n", port, ctSecret)

	tpl, c := ctContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	commit := "0123456789abcdef0123456789abcdef01234567"
	n := newCTNode(t, files[ctCtl], baseEnv, oudEnv, commit, ctn("cf_ct_oud"), port)
	doelCfg := cfg.Copy()
	doelCfg.Database = ctn("cf_ct_doel")
	doel, err := pgx.ConnectConfig(ctx, doelCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = doel.Close(ctx) }()

	if out, err := n.run("voorbereid"); err != nil {
		t.Fatalf("voorbereid: %v %s", err, out)
	}
	// Vóór de overname: de api van het cluster start niet (de oude draait).
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "oude cssthema draait nog") {
		t.Fatalf("voorstart vóór de overname: %v\n%s", err, out)
	}
	if out, err := n.run("worker-mag"); err == nil {
		t.Fatalf("worker-mag vóór de overname: %s", out)
	}

	// 1. cssthema van het cluster wordt niet gezond: alles terugdraaien.
	out, err := n.run("overnemen")
	if err == nil || !strings.Contains(out, "antwoordt niet gezond") || !strings.Contains(out, "oude cssthema draait weer") {
		t.Fatalf("overname zonder gezonde cssthema: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Error("de oude cssthema is na het terugdraaien niet gestart")
	}
	if n.exists("opt/files/.cf/cssthema.env") {
		t.Error("overgenomen instellingen bleven staan na het terugdraaien")
	}
	var tables int
	if err := doel.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("database van het cluster na het terugdraaien: %d tabellen %v", tables, err)
	}
	if m, _ := filepath.Glob(filepath.Join(n.dir, "oud", "backups", "cf-overname-*.dump")); len(m) == 0 {
		t.Errorf("back-up van de oude database: %v", m)
	}

	// 2. Een andere commit zonder --andere-versie: weigeren vóór de stop.
	write(t, map[string]string{filepath.Join(n.dir, "state", "gebouwd"): "main 0000000000000000000000000000000000000000\n"})
	if out, err := n.run("overnemen"); err == nil || !strings.Contains(out, "Zet Versie van cssthema op "+commit) {
		t.Fatalf("andere commit: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Fatal("de oude cssthema werd gestopt bij een geweigerde overname")
	}
	write(t, map[string]string{filepath.Join(n.dir, "state", "gebouwd"): "main " + commit + "\n"})

	// 3. Geslaagde overname.
	write(t, map[string]string{filepath.Join(n.dir, "gezond"): ""})
	out, err = n.run("overnemen")
	if err != nil || !strings.Contains(out, "Overgenomen") {
		t.Fatalf("overname: %v\n%s", err, out)
	}
	for _, w := range []string{"themes", "aantal+md5 ja", "snapshots", "aantal ja", "Oude cssthema stoppen", "192.0.2.160 poort 80"} {
		if !strings.Contains(out, w) {
			t.Errorf("uitvoer mist %q:\n%s", w, out)
		}
	}
	if n.exists("oud-actief") {
		t.Error("de oude cssthema draait nog na de overname")
	}
	for tb, want := range map[string]int{"themes": 40, "assets": 30, "snapshots": 300, "alembic_version": 1} {
		var got int
		if err := doel.QueryRow(ctx, "SELECT count(*) FROM "+tb).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %d %v", tb, got, err)
		}
	}
	var meta string
	if err := doel.QueryRow(ctx, "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = current_database()").Scan(&meta); err != nil ||
		!strings.Contains(meta, "cf-cssthema key=") || !strings.Contains(meta, "commit="+commit) {
		t.Errorf("markering: %q %v", meta, err)
	}
	cfEnv := filepath.Join(n.dir, "opt", "files", ".cf", "cssthema.env")
	st, err := os.Stat(cfEnv)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v", cfEnv, st, err)
	}
	b, _ := os.ReadFile(cfEnv)
	for _, bad := range []string{"POSTGRES_", "REDIS_URL", "BACKUP_DIR", "commentaar"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("overgenomen instellingen bevatten %s:\n%s", bad, b)
		}
	}
	if !strings.Contains(string(b), "SECRET_KEY="+ctSecret) || !strings.Contains(string(b), "DASHBOARD_HOST=cssthema.example.be") {
		t.Errorf("overgenomen instellingen:\n%s", b)
	}

	// Een tweede overname weigert.
	write(t, map[string]string{filepath.Join(n.dir, "oud-actief"): ""})
	if out, err := n.run("overnemen"); err == nil || !strings.Contains(out, "al in gebruik") {
		t.Fatalf("tweede overname: %v\n%s", err, out)
	}
	_ = os.Remove(filepath.Join(n.dir, "oud-actief"))

	// voorstart: de instellingen van de oude node, met de database van het
	// cluster; de worker mag op de VIP-node.
	if out, err := n.run("voorstart"); err != nil {
		t.Fatalf("voorstart: %v\n%s", err, out)
	}
	b, _ = os.ReadFile(filepath.Join(n.dir, "etc", "cssthema.env"))
	if !strings.Contains(string(b), "SECRET_KEY="+ctSecret) || !strings.Contains(string(b), "POSTGRES_HOST="+host) ||
		strings.Contains(string(b), "oud-wachtwoord") || strings.Contains(string(b), "BACKUP_DIR") {
		t.Errorf("cssthema.env:\n%s", b)
	}
	if out, err := n.run("worker-mag"); err != nil {
		t.Errorf("worker-mag na de overname: %v %s", err, out)
	}
	if out, err := n.run("master"); err != nil {
		t.Errorf("master: %v %s", err, out)
	}
	if lg, _ := os.ReadFile(filepath.Join(n.dir, "log")); !strings.Contains(string(lg), "redis-cli -n 0 FLUSHDB") ||
		!strings.Contains(string(lg), "systemctl start --no-block cf-cssthema-worker") {
		t.Errorf("master leegde Redis niet of startte de worker niet:\n%s", lg)
	}
	// Een node met een andere sleutel start niet.
	write(t, map[string]string{cfEnv: strings.ReplaceAll(string(b), ctSecret, "een-andere-sleutel-0123456789-abcdefghij")})
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "niet die van het cluster") {
		t.Fatalf("andere sleutel: %v\n%s", err, out)
	}
	write(t, map[string]string{cfEnv: string(b)})

	// Nachtelijke back-up: database, bestanden en instellingen.
	write(t, map[string]string{filepath.Join(n.dir, "data", "css-files", "eigen.css"): "body{}\n",
		filepath.Join(n.dir, "data", "storage", "assets", "ab", "abcd"): "x"})
	if out, err := n.run("backup"); err != nil || !strings.Contains(out, "back-up klaar") {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	for _, f := range []string{"database.dump", "css-files.tar.gz", "storage.tar.gz", "cssthema.env", "commit"} {
		if m, _ := filepath.Glob(filepath.Join(n.dir, "backups", "cssthema-2*", f)); len(m) != 1 {
			t.Errorf("back-up mist %s", f)
		}
	}

	// Terugweg: dump van het cluster, markering weg, oude cssthema aan.
	out, err = n.run("terug")
	if err != nil || !strings.Contains(out, "Terug op de oude cssthema") {
		t.Fatalf("terug: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Error("de oude cssthema draait niet na terug")
	}
	if m, _ := filepath.Glob(filepath.Join(n.dir, "state", "overname", "cluster-*.dump")); len(m) != 1 {
		t.Errorf("dump van het cluster: %v", m)
	}
	_ = os.Remove(filepath.Join(n.dir, "oud-actief"))
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "nog niet overgenomen") {
		t.Fatalf("voorstart na terug: %v\n%s", err, out)
	}
}
