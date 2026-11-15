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

// akContext is een cluster van drie Authentik-nodes.
func akContext(t *testing.T, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("authentik")
	if !ok {
		t.Fatal("geen template authentik")
	}
	in := map[string]any{"vip": "192.0.2.50", "db_vip": "192.0.2.49", "vrid": 61, "auth_pass": "geheim12",
		"db_password": "Wachtwoord-123", "secret_key": "sleutel-van-de-template-1234567890"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Authentik", Slug: "authentik", Environment: "prod"}}
	for i := 1; i <= 3; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("ak-%02d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.0.2.%d", 50+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

func TestRenderAuthentik(t *testing.T) {
	tpl, c := akContext(t, nil)
	if c.Params["nodes"] != 3 || c.Params["version"] != "2026.8.5" || c.Params["sync_minutes"] != 1 {
		t.Fatalf("standaarden: %+v", c.Params)
	}
	files, services := npmFiles(t, tpl, c)
	for path, want := range map[string][]string{
		"/opt/cf-authentik/compose.yaml": {"image: ghcr.io/goauthentik/server:2026.8.5", `restart: "no"`, "command: worker",
			"/run/cf-authentik/key.env", "/opt/cf-authentik/files/media:/media", "/opt/cf-authentik/files/certs:/certs", `"9000:9000"`},
		"/etc/cf-authentik/authentik.env": {"AUTHENTIK_POSTGRESQL__HOST=192.0.2.49", "AUTHENTIK_POSTGRESQL__USER=authentik",
			"AUTHENTIK_POSTGRESQL__PASSWORD=Wachtwoord-123", "AUTHENTIK_POSTGRESQL__CONN_HEALTH_CHECKS=true"},
		"/etc/keepalived/keepalived.conf": {"virtual_router_id 61", "192.0.2.50/24 dev eth0", "weight 50", "priority 150",
			"unicast_peer {\n        192.0.2.52\n        192.0.2.53\n    }", "include /etc/keepalived/conf.d/*.conf"},
		"/etc/systemd/system/cf-authentik.service":     {"ExecStart=/usr/local/lib/clusterforge/authentik-ctl.sh up", "Restart=always", "WantedBy=multi-user.target"},
		"/usr/local/lib/clusterforge/authentik-ctl.sh": {"VIP='192.0.2.50'", "INDEX='1'", "VERSION='2026.8.5'", "PARAM_KEY='sleutel-van-de-template-1234567890'"},
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
	// De geheime sleutel staat niet in de compose of de env van de template.
	for _, p := range []string{"/opt/cf-authentik/compose.yaml", "/etc/cf-authentik/authentik.env"} {
		if strings.Contains(files[p], "sleutel-van-de-template") {
			t.Errorf("%s bevat de geheime sleutel", p)
		}
	}
	// nopreempt zou een ongezonde node het VIP laten houden.
	if strings.Contains(files["/etc/keepalived/keepalived.conf"], "nopreempt") {
		t.Error("nopreempt")
	}
	for _, s := range services {
		if s == "cf-authentik" {
			t.Errorf("cf-authentik hoort geen service-stap te hebben (start zou falen zolang de database er niet is): %v", services)
		}
	}
	if _, err := tpl.Validate(map[string]any{"vip": "192.0.2.50", "db_vip": "192.0.2.49", "nodes": 2}); err == nil {
		t.Error("twee nodes aanvaard")
	}
	if _, err := tpl.Validate(map[string]any{"vip": "192.0.2.50", "db_vip": "192.0.2.49", "version": "latest"}); err == nil {
		t.Error("versie latest aanvaard")
	}

	if tpl.Database == nil || tpl.Database.Engine != "postgresql" || tpl.Database.Role != "node" {
		t.Fatalf("database: %+v", tpl.Database)
	}
	if v, err := tpl.DBVIP(c); err != nil || v != "192.0.2.49" {
		t.Errorf("VIP van de database: %q %v", v, err)
	}
	steps, err := tpl.RenderDBSteps(c)
	if err != nil || len(steps) != 1 || steps[0].User != "authentik" || steps[0].GrantOn != "authentik" || steps[0].Password != "Wachtwoord-123" {
		t.Errorf("database-instructies: %+v %v", steps, err)
	}
	syncs, err := tpl.RenderSyncs(c)
	if err != nil || len(syncs) != 1 {
		t.Fatalf("syncs: %+v %v", syncs, err)
	}
	if s := syncs[0]; s.Name != "Authentik-media" || s.Schedule != "* * * * *" || s.Source != "/opt/cf-authentik/files/" || s.Target != s.Source || s.Delete {
		t.Errorf("sync: %+v", s)
	}

	c.Node = &c.Nodes[2]
	files3, _ := npmFiles(t, tpl, c)
	if !strings.Contains(files3["/etc/keepalived/keepalived.conf"], "priority 130") || !strings.Contains(files3["/usr/local/lib/clusterforge/authentik-ctl.sh"], "INDEX='3'") {
		t.Error("node 3")
	}

	if _, err := exec.LookPath("shellcheck"); err == nil {
		p := filepath.Join(t.TempDir(), "authentik-ctl.sh")
		if err := os.WriteFile(p, []byte(files["/usr/local/lib/clusterforge/authentik-ctl.sh"]), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
			t.Errorf("shellcheck:\n%s", out)
		}
	}
}

// akNode is een nagespeelde Authentik-node: authentik-ctl.sh met eigen
// mappen, een nep-docker en de echte psql tegen een testdatabase.
type akNode struct {
	t                        *testing.T
	dir, script, bin         string
	base, state, etc, runDir string
}

func newAKNode(t *testing.T, script, name, dbEnv string) *akNode {
	t.Helper()
	dir := t.TempDir()
	n := &akNode{t: t, dir: dir, script: filepath.Join(dir, "authentik-ctl.sh"), bin: filepath.Join(dir, "bin"),
		base: filepath.Join(dir, "opt"), state: filepath.Join(dir, "state"), etc: filepath.Join(dir, "etc"), runDir: filepath.Join(dir, "run")}
	for _, d := range []string{n.bin, n.base + "/files/media", n.base + "/files/data", n.base + "/files/templates", n.base + "/files/certs", n.state, n.etc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// docker: containers staan als bestanden in $CF_TEST_DIR/c/<id>/; exec
	// draait psql of pg_dump echt, tegen de bron in src.
	docker := `d=$CF_TEST_DIR/c
echo "docker $*" >> "$CF_TEST_DIR/log"
case "$1" in
ps)
  case "$2" in
  -aq) p=${4#label=com.docker.compose.project=}; for c in "$d"/*; do [ "$(cat "$c/project")" = "$p" ] && basename "$c"; done ;;
  *) for c in "$d"/*; do [ -f "$c/running" ] && echo "$(basename "$c") $(cat "$c/image") $(cat "$c/project")"; done ;;
  esac ;;
inspect)
  c=$d/$4
  case "$3" in
  *working_dir*) cat "$c/workdir" ;;
  *config_files*) cat "$c/configs" ;;
  *compose.service*) cat "$c/service" ;;
  *Labels*) cat "$c/project" ;;
  *NetworkMode*) cat "$c/netmode" 2>/dev/null ;;
  *Config.Cmd*) cat "$c/cmd" ;;
  *Config.Image*) cat "$c/image" ;;
  *State.Running*) [ -f "$c/running" ] && echo true || echo false ;;
  *Config.Env*) cat "$c/env" ;;
  *Mounts*) cat "$c/mounts" 2>/dev/null ;;
  esac ;;
stop) shift; for c in "$@"; do rm -f "$d/$c/running"; done ;;
start) shift; for c in "$@"; do touch "$d/$c/running"; done ;;
exec) shift 2; exec "$@" ;;
esac
exit 0`
	stubs := map[string]string{
		"docker":    docker,
		"ip":        `exit 0`,
		"curl":      `exit 1`,
		"logger":    `exit 0`,
		"systemctl": `echo "systemctl $*" >> "$CF_TEST_DIR/log"; exit 0`,
		"hostname":  `echo ` + name,
	}
	for cmd, body := range stubs {
		write(t, map[string]string{filepath.Join(n.bin, cmd): "#!/bin/sh\n" + body + "\n"})
		if err := os.Chmod(filepath.Join(n.bin, cmd), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, map[string]string{n.script: script, n.etc + "/authentik.env": dbEnv, n.base + "/compose.yaml": "services: {}\n"})
	return n
}

// container zet een nep-container van een oude Authentik op de node.
func (n *akNode) container(id string, files map[string]string) {
	n.t.Helper()
	m := map[string]string{}
	for k, v := range files {
		m[filepath.Join(n.dir, "c", id, k)] = v
	}
	write(n.t, m)
}

func (n *akNode) run(env []string, args ...string) (string, error) {
	n.t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{n.script}, args...)...)
	cmd.Env = append(append(os.Environ(), "PATH="+n.bin+":"+os.Getenv("PATH"), "CF_TEST_DIR="+n.dir,
		"CF_AK_BASE="+n.base, "CF_AK_STATE="+n.state, "CF_AK_ETC="+n.etc, "CF_AK_RUN="+n.runDir), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *akNode) log() string {
	b, _ := os.ReadFile(filepath.Join(n.dir, "log"))
	return string(b)
}

// TestAuthentikOvernemen neemt een oude Authentik over in een echte
// PostgreSQL: een bron (de PostgreSQL-container van de oude Authentik) en
// een lege database van het cluster met een eigen eigenaar.
func TestAuthentikOvernemen(t *testing.T) {
	url := os.Getenv("CF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CF_TEST_DATABASE_URL ontbreekt")
	}
	for _, tool := range []string{"psql", "pg_dump", "rsync", "sha256sum", "gzip"} {
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
	for _, q := range []string{akn("DROP DATABASE IF EXISTS cf_ak_bron"), akn("DROP DATABASE IF EXISTS cf_ak_doel"), akn("DROP ROLE IF EXISTS cf_ak_eigenaar")} {
		exe(admin, q)
	}
	exe(admin, akn("CREATE ROLE cf_ak_eigenaar LOGIN PASSWORD 'Wachtwoord123'"))
	exe(admin, akn("CREATE DATABASE cf_ak_bron"))
	exe(admin, akn("CREATE DATABASE cf_ak_doel OWNER cf_ak_eigenaar"))
	t.Cleanup(func() {
		for _, q := range []string{akn("DROP DATABASE IF EXISTS cf_ak_bron WITH (FORCE)"), akn("DROP DATABASE IF EXISTS cf_ak_doel WITH (FORCE)"), akn("DROP ROLE IF EXISTS cf_ak_eigenaar")} {
			_, _ = admin.Exec(context.Background(), q)
		}
	})
	srcCfg := cfg.Copy()
	srcCfg.Database = akn("cf_ak_bron")
	src, err := pgx.ConnectConfig(ctx, srcCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close(ctx) }()
	exe(src, `CREATE TABLE authentik_core_user (id serial PRIMARY KEY, username text UNIQUE NOT NULL);
CREATE TABLE authentik_core_provider (id serial PRIMARY KEY, name text NOT NULL);
CREATE TABLE authentik_providers_proxy_proxyprovider (provider_ptr_id int PRIMARY KEY REFERENCES authentik_core_provider(id), external_host text);
CREATE TABLE authentik_core_application (id serial PRIMARY KEY, slug text UNIQUE, provider_id int REFERENCES authentik_core_provider(id));
INSERT INTO authentik_core_user (username) SELECT 'gebruiker' || g FROM generate_series(1, 3) g;
INSERT INTO authentik_core_provider (name) SELECT 'proxy ' || g FROM generate_series(1, 100) g;
INSERT INTO authentik_providers_proxy_proxyprovider SELECT id, 'https://app' || id || '.thuis' FROM authentik_core_provider;
INSERT INTO authentik_core_application (slug, provider_id) SELECT 'app' || id, id FROM authentik_core_provider;`)

	host := cfg.Host
	if host == "" || strings.HasPrefix(host, "/") {
		host = "127.0.0.1"
	}
	dbEnv := fmt.Sprintf(akn("AUTHENTIK_POSTGRESQL__HOST=%s\nAUTHENTIK_POSTGRESQL__PORT=%d\nAUTHENTIK_POSTGRESQL__NAME=cf_ak_doel\nAUTHENTIK_POSTGRESQL__USER=cf_ak_eigenaar\nAUTHENTIK_POSTGRESQL__PASSWORD=Wachtwoord123\n"), host, cfg.Port)
	// De bron: wat docker exec in de oude PostgreSQL-container draait.
	srcEnv := []string{"PGHOST=" + host, fmt.Sprintf("PGPORT=%d", cfg.Port), "PGPASSWORD=" + cfg.Password}

	tpl, c := akContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	a := newAKNode(t, files["/usr/local/lib/clusterforge/authentik-ctl.sh"], "ak-01", dbEnv)
	c.Node = &c.Nodes[1]
	files2, _ := npmFiles(t, tpl, c)
	b := newAKNode(t, files2["/usr/local/lib/clusterforge/authentik-ctl.sh"], "ak-02", dbEnv)

	// De oude Authentik op node 1: server, worker, PostgreSQL en Redis.
	oldMedia := t.TempDir()
	oldCerts := t.TempDir()
	write(t, map[string]string{oldMedia + "/application-icons/grafana.png": "png", oldCerts + "/thuis.pem": "cert"})
	oldEnv := fmt.Sprintf(akn("AUTHENTIK_SECRET_KEY=oude+sleutel/met=base64tekens\nAUTHENTIK_POSTGRESQL__NAME=cf_ak_bron\nAUTHENTIK_POSTGRESQL__USER=%s\nAUTHENTIK_POSTGRESQL__PORT=%d\nPATH=/usr/bin\n"), cfg.User, cfg.Port)
	oldDir := t.TempDir()
	a.container("srv1", map[string]string{"project": "authentik", "image": "ghcr.io/goauthentik/server:2026.5.2", "cmd": "server",
		"env": oldEnv, "mounts": "/media " + oldMedia + "\n/templates " + oldMedia + "/geen\n", "running": ""})
	a.container("wrk1", map[string]string{"project": "authentik", "image": "ghcr.io/goauthentik/server:2026.5.2", "cmd": "worker",
		"env": oldEnv, "mounts": "/certs " + oldCerts + "\n/var/run/docker.sock /var/run/docker.sock\n", "running": ""})
	a.container("pg1", map[string]string{"project": "authentik", "image": "docker.io/library/postgres:16-alpine", "cmd": "postgres", "running": "",
		"netmode": "host", "service": "postgresql", "workdir": oldDir, "configs": oldDir + "/docker-compose.yml"})
	for id, svc := range map[string]string{"srv1": "server", "wrk1": "worker"} {
		a.container(id, map[string]string{"service": svc})
	}
	a.container("rd1", map[string]string{"project": "authentik", "image": "docker.io/library/redis:alpine", "cmd": "redis-server", "running": ""})
	// Een outpost is geen oude Authentik.
	a.container("out1", map[string]string{"project": "outposts", "image": "ghcr.io/goauthentik/proxy:2026.5.2", "cmd": "", "running": ""})

	// Node 1 met de oude Authentik: niet starten, wel gezond voor keepalived
	// als de oude antwoordt. Node 2 met een kloon van de oude: nooit het VIP.
	if out, err := a.run(nil, "up"); err == nil || !strings.Contains(out, "buiten het cluster") {
		t.Fatalf("up naast de oude: %v %s", err, out)
	}
	b.container("srv2", map[string]string{"project": "authentik", "image": "ghcr.io/goauthentik/server:2026.5.2", "cmd": "server", "env": oldEnv, "running": ""})
	write(t, map[string]string{b.bin + "/curl": "#!/bin/sh\nexit 0\n", a.bin + "/curl": "#!/bin/sh\nexit 0\n"})
	if _, err := b.run(nil, "check"); err == nil {
		t.Error("check op node 2 met een oude Authentik gaf het VIP")
	}
	if _, err := a.run(nil, "check"); err != nil {
		t.Error("check op node 1 met een gezonde oude Authentik")
	}

	// Tijdens de uitrol: de oude PostgreSQL (network_mode: host) gaat van
	// 5432 naar 5433, zodat Patroni de poort krijgt.
	if _, err := a.run(nil, "voorbereid"); err == nil {
		t.Error("voorbereid met de oude PostgreSQL op 5432")
	}
	if out, err := a.run([]string{"CF_AK_SLEEP=0"}, "voorbereiden"); err != nil || !strings.Contains(out, "antwoordt weer") {
		t.Fatalf("voorbereiden: %v %s", err, out)
	}
	ov, _ := os.ReadFile(oldDir + "/docker-compose.cf-poort.yml")
	for _, w := range []string{"  postgresql:\n    command: [\"postgres\", \"-p\", \"5433\"]", "pg_isready -p 5433 -d $${POSTGRES_DB}",
		"  server:\n    environment:\n      AUTHENTIK_POSTGRESQL__PORT: \"5433\"", "  worker:\n"} {
		if !strings.Contains(string(ov), w) {
			t.Errorf("override mist %q:\n%s", w, ov)
		}
	}
	if !strings.Contains(a.log(), "docker compose -p authentik --project-directory "+oldDir+" -f "+oldDir+"/docker-compose.yml -f "+oldDir+"/docker-compose.cf-poort.yml up -d") {
		t.Errorf("compose: %s", a.log())
	}
	a.container("pg1", map[string]string{"cmd": "postgres -p 5433"})
	if out, err := a.run(nil, "voorbereid"); err != nil {
		t.Errorf("voorbereid na de verhuizing: %v %s", err, out)
	}

	// Andere versie: weigeren met de juiste raad.
	if out, err := a.run(srcEnv, "overnemen"); err == nil || !strings.Contains(out, "Versie van Authentik: 2026.5.2") {
		t.Fatalf("overnemen met een andere versie: %v %s", err, out)
	}
	if !fileExists(filepath.Join(a.dir, "c/srv1/running")) {
		t.Fatal("de oude Authentik werd gestopt bij een geweigerde overname")
	}

	// Het terugzetten faalt (een extensie die alleen een superuser mag
	// maken): de database van het cluster blijft leeg, de oude draait weer.
	var fdw bool
	if err := src.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'file_fdw')").Scan(&fdw); err != nil {
		t.Fatal(err)
	}
	if fdw {
		exe(src, "CREATE EXTENSION file_fdw")
		out, err := a.run(srcEnv, "overnemen", "--andere-versie")
		if err == nil || !strings.Contains(out, "Terugdraaien") || !strings.Contains(out, "niets veranderd") {
			t.Fatalf("mislukte overname: %v %s", err, out)
		}
		if !fileExists(filepath.Join(a.dir, "c/srv1/running")) || !fileExists(filepath.Join(a.dir, "c/wrk1/running")) {
			t.Error("de oude Authentik draait niet meer na een mislukte overname")
		}
		if fileExists(a.base + "/files/.cf/secret_key") {
			t.Error("sleutel na een mislukte overname")
		}
		// De volgende overname kijkt na dat de database weer leeg is.
		var meta *string
		if err := admin.QueryRow(ctx, akn("SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = 'cf_ak_doel'")).Scan(&meta); err != nil || meta != nil {
			t.Errorf("markering na een mislukte overname: %v %v", meta, err)
		}
		exe(src, "DROP EXTENSION file_fdw")
	}

	out, err := a.run(srcEnv, "overnemen", "--andere-versie")
	if err != nil {
		t.Fatalf("overnemen: %v %s", err, out)
	}
	for _, w := range []string{"100 -> 100", "     3 -> 3", "Overgenomen."} {
		if !strings.Contains(out, w) {
			t.Errorf("uitvoer mist %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "authentik_outposts_outpost") {
		t.Error("een tabel die er niet was, werd geteld")
	}
	if k, _ := os.ReadFile(a.base + "/files/.cf/secret_key"); string(k) != "oude+sleutel/met=base64tekens" {
		t.Errorf("sleutel: %q", k)
	}
	if d, _ := os.ReadFile(a.base + "/files/media/application-icons/grafana.png"); string(d) != "png" {
		t.Error("media niet overgenomen")
	}
	if d, _ := os.ReadFile(a.base + "/files/certs/thuis.pem"); string(d) != "cert" {
		t.Error("certificaten niet overgenomen")
	}
	for _, id := range []string{"srv1", "wrk1", "pg1", "rd1"} {
		if fileExists(filepath.Join(a.dir, "c", id, "running")) || !strings.Contains(a.log(), "docker update --restart=no "+id) {
			t.Errorf("%s draait nog of start nog vanzelf", id)
		}
	}
	if !fileExists(filepath.Join(a.dir, "c/out1/running")) {
		t.Error("de outpost werd gestopt")
	}
	if !strings.Contains(a.log(), "systemctl restart cf-authentik") {
		t.Errorf("cf-authentik niet gestart: %s", a.log())
	}
	if m, _ := filepath.Glob(a.state + "/overname/authentik-*.sql.gz"); len(m) != 1 {
		t.Errorf("dump: %v", m)
	}
	dstCfg := cfg.Copy()
	dstCfg.Database = akn("cf_ak_doel")
	dst, err := pgx.ConnectConfig(ctx, dstCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close(ctx) }()
	var proxies int
	var owner string
	if err := dst.QueryRow(ctx, "SELECT count(*), (SELECT tableowner FROM pg_tables WHERE tablename = 'authentik_core_user') FROM authentik_providers_proxy_proxyprovider").Scan(&proxies, &owner); err != nil || proxies != 100 || owner != akn("cf_ak_eigenaar") {
		t.Errorf("doel: %d proxy's, eigenaar %s, %v", proxies, owner, err)
	}

	// Een tweede overname zou de database overschrijven.
	a.container("srv1", map[string]string{"running": ""})
	a.container("pg1", map[string]string{"running": ""})
	if out, err := a.run(srcEnv, "overnemen", "--andere-versie"); err == nil || !strings.Contains(out, "al in gebruik") {
		t.Errorf("tweede overname: %v %s", err, out)
	}
	_ = os.Remove(filepath.Join(a.dir, "c/srv1/running"))

	// Node 2 zonder de sleutel wacht; zijn oude Authentik draait nog.
	if out, err := b.run(nil, "up"); err == nil || !strings.Contains(out, "niet die van het cluster") {
		t.Fatalf("up zonder sleutel: %v %s", err, out)
	}
	// De sync brengt de sleutel: node 2 stopt zijn oude Authentik en start.
	if out, err := exec.Command("rsync", "-a", a.base+"/files/", b.base+"/files/").CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v %s", err, out)
	}
	if out, err := b.run(nil, "up"); err != nil {
		t.Fatalf("up na de sync: %v %s", err, out)
	}
	if !strings.Contains(b.log(), "up --no-build") || !strings.Contains(b.log(), "docker update --restart=no srv2") {
		t.Errorf("node 2: %s", b.log())
	}
	if k, _ := os.ReadFile(b.runDir + "/key.env"); string(k) != "AUTHENTIK_SECRET_KEY=oude+sleutel/met=base64tekens\n" {
		t.Errorf("key.env: %q", k)
	}
	if out, err := b.run(nil, "voorbereiden"); err != nil || !strings.Contains(out, "Geen oude Authentik") {
		t.Errorf("voorbereiden na de start: %v %s", err, out)
	}
	if out, err := b.run(nil, "status"); err != nil || !strings.Contains(out, "overgenomen") || !strings.Contains(out, "ready=") {
		t.Errorf("status: %v %s", err, out)
	}
}

// TestAuthentikNieuwCluster: zonder oude Authentik begint node 1 met de lege
// database en volgen de andere nodes met de sleutel van de template.
func TestAuthentikNieuwCluster(t *testing.T) {
	url := os.Getenv("CF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CF_TEST_DATABASE_URL ontbreekt")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql staat niet op deze machine")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	cfg := admin.Config()
	for _, q := range []string{akn("DROP DATABASE IF EXISTS cf_ak_nieuw"), akn("DROP ROLE IF EXISTS cf_ak_nieuw"), akn("CREATE ROLE cf_ak_nieuw LOGIN PASSWORD 'Wachtwoord123'"), akn("CREATE DATABASE cf_ak_nieuw OWNER cf_ak_nieuw")} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), akn("DROP DATABASE IF EXISTS cf_ak_nieuw WITH (FORCE)"))
		_, _ = admin.Exec(context.Background(), akn("DROP ROLE IF EXISTS cf_ak_nieuw"))
	})
	host := cfg.Host
	if host == "" || strings.HasPrefix(host, "/") {
		host = "127.0.0.1"
	}
	dbEnv := fmt.Sprintf(akn("AUTHENTIK_POSTGRESQL__HOST=%s\nAUTHENTIK_POSTGRESQL__PORT=%d\nAUTHENTIK_POSTGRESQL__NAME=cf_ak_nieuw\nAUTHENTIK_POSTGRESQL__USER=cf_ak_nieuw\nAUTHENTIK_POSTGRESQL__PASSWORD=Wachtwoord123\n"), host, cfg.Port)
	tpl, c := akContext(t, nil)
	c.Node = &c.Nodes[1]
	files2, _ := npmFiles(t, tpl, c)
	b := newAKNode(t, files2["/usr/local/lib/clusterforge/authentik-ctl.sh"], "ak-02", dbEnv)
	if out, err := b.run(nil, "up"); err == nil || !strings.Contains(out, "nog niet in gebruik") {
		t.Fatalf("node 2 begint een lege database: %v %s", err, out)
	}
	c.Node = &c.Nodes[0]
	files, _ := npmFiles(t, tpl, c)
	a := newAKNode(t, files["/usr/local/lib/clusterforge/authentik-ctl.sh"], "ak-01", dbEnv)
	if out, err := a.run(nil, "up"); err != nil || !strings.Contains(out, "Nieuw cluster") {
		t.Fatalf("node 1: %v %s", err, out)
	}
	if out, err := b.run(nil, "up"); err != nil {
		t.Fatalf("node 2 na node 1: %v %s", err, out)
	}
	if k, _ := os.ReadFile(b.runDir + "/key.env"); string(k) != "AUTHENTIK_SECRET_KEY=sleutel-van-de-template-1234567890\n" {
		t.Errorf("key.env: %q", k)
	}
	// Een database met tabellen maar zonder markering (zelf teruggezet):
	// vrijgeven.
	if _, err := admin.Exec(ctx, akn("COMMENT ON DATABASE cf_ak_nieuw IS NULL")); err != nil {
		t.Fatal(err)
	}
	if out, err := a.run(nil, "vrijgeven"); err == nil || !strings.Contains(out, "leeg") {
		t.Errorf("vrijgeven van een lege database: %v %s", err, out)
	}
}

// akPrefix maakt namen van databases en rollen uniek per testproces: rollen
// gelden voor de hele server, ook voor een andere testrun ernaast.
var akPrefix = fmt.Sprintf("cf_ak%d_", os.Getpid())

func akn(s string) string { return strings.ReplaceAll(s, "cf_ak_", akPrefix) }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
