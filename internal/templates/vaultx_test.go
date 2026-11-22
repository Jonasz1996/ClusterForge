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

// vxSecret is de sleutel van de oude node in de tests; hij mag nooit in de
// uitvoer van een opdracht staan.
const vxSecret = "OudeSleutel-van-de-kluis-0123456789abcdefghij"

// vxContext is een cluster van drie VaultX-nodes.
func vxContext(t *testing.T, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("vaultx")
	if !ok {
		t.Fatal("geen template vaultx")
	}
	in := map[string]any{"vip": "192.0.2.60", "db_vip": "192.0.2.59", "vrid": 62, "auth_pass": "geheim12",
		"db_password": "Wachtwoord-123", "secret_key": "sleutel-van-de-template-1234567890-abcdefghijk"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "VaultX", Slug: "vaultx", Environment: "prod"}}
	for i := 1; i <= 3; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("vx-%02d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.0.2.%d", 60+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

const (
	vxCtl  = "/usr/local/lib/clusterforge/vaultx-ctl.sh"
	vxBron = "/usr/local/lib/clusterforge/vaultx-bron.sh"
	vxBase = "/etc/cf-vaultx/base.env"
)

func TestRenderVaultX(t *testing.T) {
	tpl, c := vxContext(t, nil)
	if c.Params["nodes"] != 3 || c.Params["version"] != "main" || c.Params["nieuw"] != false {
		t.Fatalf("standaarden: %+v", c.Params)
	}
	files, services := npmFiles(t, tpl, c)
	for path, want := range map[string][]string{
		vxBase: {"VAULTX_DATABASE_URL=postgresql+psycopg://vaultx:Wachtwoord-123@192.0.2.59:5432/vaultx"},
		"/etc/keepalived/keepalived.conf": {"virtual_router_id 62", "192.0.2.60/24 dev eth0", "weight 50", "priority 150", "chk_vaultx",
			"unicast_peer {\n        192.0.2.62\n        192.0.2.63\n    }", "include /etc/keepalived/conf.d/*.conf"},
		"/etc/systemd/system/cf-vaultx.service": {"ExecStartPre=+/usr/local/lib/clusterforge/vaultx-ctl.sh voorstart",
			"vaultx-migreer.py", "EnvironmentFile=-/etc/vaultx/vaultx.env", "Restart=always", "User=vaultx", "ProtectSystem=strict"},
		"/etc/systemd/system/cf-vaultx-backup.timer":   {"OnCalendar=*-*-* 03:15"},
		"/etc/systemd/system/cf-vaultx-backup.service": {"vaultx-ctl.sh backup"},
		vxCtl:  {"VIP='192.0.2.60'", "DB_VIP='192.0.2.59'", "INDEX='1'", "VERSION='main'", "NIEUW='false'", "ADDRESS='192.0.2.61'", "PEERS='192.0.2.62 192.0.2.63 '"},
		vxBron: {"cf-vaultx-bron", "SSH_ORIGINAL_COMMAND"},
		"/usr/local/lib/clusterforge/vaultx-migreer.py": {"pg_advisory_lock", "alembic"},
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
	// Bij een overname staat geen sleutel van de template in base.env, en
	// nooit een geheim in een script of de keepalived-config.
	if strings.Contains(files[vxBase], "SECRET_KEY") || strings.Contains(files[vxBase], "OIDC") {
		t.Errorf("base.env bij een overname bevat een sleutel of OIDC:\n%s", files[vxBase])
	}
	for p, body := range files {
		if p != vxBase && (strings.Contains(body, "sleutel-van-de-template") || strings.Contains(body, "Wachtwoord-123")) {
			t.Errorf("%s bevat een geheim", p)
		}
	}
	if strings.Contains(files["/etc/keepalived/keepalived.conf"], "nopreempt") {
		t.Error("nopreempt")
	}
	for _, s := range services {
		if s == "cf-vaultx" {
			t.Errorf("cf-vaultx hoort geen service-stap te hebben: %v", services)
		}
	}

	// Een nieuw cluster: sleutel en OIDC in base.env.
	tplN, cN := vxContext(t, map[string]any{"nieuw": true, "public_url": "https://vaultx.example.be",
		"oidc_issuer": "https://auth.example.be/application/o/vaultx/", "oidc_client_id": "abc123"})
	filesN, _ := npmFiles(t, tplN, cN)
	for _, w := range []string{"VAULTX_SECRET_KEY=sleutel-van-de-template", "VAULTX_PUBLIC_URL=https://vaultx.example.be",
		"VAULTX_OIDC_ISSUER=https://auth.example.be/application/o/vaultx/", "VAULTX_OIDC_CLIENT_ID=abc123", "VAULTX_OIDC_CLIENT_SECRET="} {
		if !strings.Contains(filesN[vxBase], w) {
			t.Errorf("nieuw cluster: base.env mist %q:\n%s", w, filesN[vxBase])
		}
	}
	if !strings.Contains(filesN[vxCtl], "NIEUW='true'") {
		t.Error("NIEUW='true'")
	}

	for _, bad := range []map[string]any{{"nodes": 2}, {"version": "main; rm -rf /"}, {"public_url": "javascript:alert(1)"}} {
		in := map[string]any{"vip": "192.0.2.60", "db_vip": "192.0.2.59"}
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
	if v, err := tpl.DBVIP(c); err != nil || v != "192.0.2.59" {
		t.Errorf("VIP van de database: %q %v", v, err)
	}
	steps, err := tpl.RenderDBSteps(c)
	if err != nil || len(steps) != 1 || steps[0].User != "vaultx" || steps[0].GrantOn != "vaultx" || steps[0].Password != "Wachtwoord-123" {
		t.Errorf("database-instructies: %+v %v", steps, err)
	}
	syncs, err := tpl.RenderSyncs(c)
	if err != nil || len(syncs) != 1 {
		t.Fatalf("syncs: %+v %v", syncs, err)
	}
	if s := syncs[0]; s.Name != "VaultX-instellingen" || s.Source != "/opt/cf-vaultx/files/" || s.Target != s.Source || s.Delete {
		t.Errorf("sync: %+v", s)
	}

	c.Node = &c.Nodes[2]
	files3, _ := npmFiles(t, tpl, c)
	if !strings.Contains(files3["/etc/keepalived/keepalived.conf"], "priority 130") || !strings.Contains(files3[vxCtl], "INDEX='3'") {
		t.Error("node 3")
	}

	if _, err := exec.LookPath("shellcheck"); err == nil {
		for _, name := range []string{vxCtl, vxBron} {
			p := filepath.Join(t.TempDir(), filepath.Base(name))
			if err := os.WriteFile(p, []byte(files[name]), 0o755); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
				t.Errorf("shellcheck %s:\n%s", name, out)
			}
		}
	}
}

// vxPrefix maakt namen van databases en rollen uniek per testproces.
var vxPrefix = fmt.Sprintf("cf_vx%d_", os.Getpid())

func vxn(s string) string { return strings.ReplaceAll(s, "cf_vx_", vxPrefix) }

// vxNode is een nagespeelde clusternode met vaultx-ctl.sh, eigen mappen en
// de echte psql/pg_dump/pg_restore tegen een testdatabase; de oude node is
// cf-vaultx-bron, rechtstreeks aangeroepen in plaats van via SSH.
type vxNode struct {
	t   *testing.T
	dir string
	env []string
}

func newVXNode(t *testing.T, ctl, bron, baseEnv, oudEnv, commit string, pgArgs, oudDB string) *vxNode {
	t.Helper()
	dir := t.TempDir()
	n := &vxNode{t: t, dir: dir}
	bin := filepath.Join(dir, "bin")
	stubs := map[string]string{
		"ip":       `exit 0`,
		"logger":   `exit 0`,
		"hostname": `echo vx-test`,
		"curl":     `[ -f "$CF_TEST_DIR/gezond" ]`,
		"nginx":    `exit 0`,
		// Na voorbereiden: alleen het hernoemde cluster van de oude VaultX.
		"pg_lsclusters": `echo "17 vaultxoud 5433 online postgres /var/lib/postgresql/17/vaultxoud /var/log/postgresql/x.log"`,
		// systemctl van de clusternode en van de oude node; de oude VaultX
		// is "actief" zolang $CF_TEST_DIR/oud-actief bestaat.
		"systemctl": `echo "systemctl $*" >> "$CF_TEST_DIR/log"
case "$*" in
*"is-active --quiet vaultx"|*"is-active vaultx") [ -f "$CF_TEST_DIR/oud-actief" ] ;;
*"is-enabled --quiet vaultx") [ -f "$CF_TEST_DIR/oud-actief" ] ;;
"disable --now vaultx"|"stop vaultx") rm -f "$CF_TEST_DIR/oud-actief" ;;
"enable --now vaultx"|"start vaultx") touch "$CF_TEST_DIR/oud-actief" ;;
*) exit 0 ;;
esac`,
		// De SSH-verbinding: de opdracht gaat als SSH_ORIGINAL_COMMAND naar
		// cf-vaultx-bron, zoals de forced command doet.
		"bron": `SSH_ORIGINAL_COMMAND="$*" exec /bin/sh "$CF_TEST_DIR/cf-vaultx-bron"`,
	}
	for cmd, body := range stubs {
		write(t, map[string]string{filepath.Join(bin, cmd): "#!/bin/sh\n" + body + "\n"})
		if err := os.Chmod(filepath.Join(bin, cmd), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	code := filepath.Join(dir, "code")
	write(t, map[string]string{
		filepath.Join(dir, "vaultx-ctl.sh"):                              ctl,
		filepath.Join(dir, "cf-vaultx-bron"):                             bron,
		filepath.Join(dir, "etc", "base.env"):                            baseEnv,
		filepath.Join(dir, "oud", "vaultx.env"):                          oudEnv,
		filepath.Join(dir, "oud", "nginx-realip.conf"):                   "set_real_ip_from 192.0.2.244;\n",
		filepath.Join(dir, "state", "gebouwd"):                           "main " + commit + "\n",
		filepath.Join(code, "backend/alembic/versions/0013_webauthn.py"): "revision: str = '0013'\n",
		filepath.Join(code, "venv/bin/uvicorn"):                          "",
		filepath.Join(dir, "oud-actief"):                                 "",
	})
	if err := os.Chmod(filepath.Join(code, "venv/bin/uvicorn"), 0o755); err != nil {
		t.Fatal(err)
	}
	n.env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "CF_TEST_DIR=" + dir, "CF_VX_TEST=1",
		"CF_VX_BASE=" + filepath.Join(dir, "opt"), "CF_VX_STATE=" + filepath.Join(dir, "state"), "CF_VX_ETC=" + filepath.Join(dir, "etc"),
		"CF_VX_VXETC=" + filepath.Join(dir, "vxetc"), "CF_VX_CODE=" + code, "CF_VX_BACKUPS=" + filepath.Join(dir, "backups"),
		"CF_VX_RUN=" + filepath.Join(dir, "run"), "CF_VX_BRON=" + filepath.Join(bin, "bron"), "CF_VX_SLEEP=0", "CF_VX_WAIT=2",
		"CF_VXB_ENV=" + filepath.Join(dir, "oud", "vaultx.env"), "CF_VXB_REALIP=" + filepath.Join(dir, "oud", "nginx-realip.conf"),
		"CF_VXB_CODE=" + filepath.Join(dir, "oudcode"), "CF_VXB_DB=" + oudDB, "CF_VXB_BACKUPS=" + filepath.Join(dir, "oud", "backups"),
		"CF_VXB_STATE=" + filepath.Join(dir, "oud", "state"), "CF_VXB_AUTH=" + filepath.Join(dir, "oud", "authorized_keys"),
		"CF_VXB_PSQL=psql " + pgArgs, "CF_VXB_PGDUMP=pg_dump " + pgArgs, "CF_VXB_MD5_MAX=100000"}
	return n
}

func (n *vxNode) run(args ...string) (string, error) {
	n.t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(n.dir, "vaultx-ctl.sh")}, args...)...)
	cmd.Env = append(os.Environ(), n.env...)
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), vxSecret) {
		n.t.Errorf("de sleutel staat in de uitvoer van %v", args)
	}
	return string(out), err
}

func (n *vxNode) exists(p string) bool { _, err := os.Stat(filepath.Join(n.dir, p)); return err == nil }

// TestVaultXOvernemen neemt een kluis over in een echte PostgreSQL: eerst
// een overname die faalt (VaultX van het cluster wordt niet gezond) en dus
// terugdraait, dan een die lukt, dan de terugweg.
func TestVaultXOvernemen(t *testing.T) {
	url := os.Getenv("CF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CF_TEST_DATABASE_URL ontbreekt")
	}
	for _, tool := range []string{"psql", "pg_dump", "pg_restore", "sha256sum", "git"} {
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
	for _, q := range []string{vxn("DROP DATABASE IF EXISTS cf_vx_oud"), vxn("DROP DATABASE IF EXISTS cf_vx_doel"), vxn("DROP ROLE IF EXISTS cf_vx_eigenaar")} {
		exe(admin, q)
	}
	exe(admin, vxn("CREATE ROLE cf_vx_eigenaar LOGIN PASSWORD 'Wachtwoord-123'"))
	exe(admin, vxn("CREATE DATABASE cf_vx_oud"))
	exe(admin, vxn("CREATE DATABASE cf_vx_doel OWNER cf_vx_eigenaar"))
	t.Cleanup(func() {
		for _, q := range []string{vxn("DROP DATABASE IF EXISTS cf_vx_oud WITH (FORCE)"), vxn("DROP DATABASE IF EXISTS cf_vx_doel WITH (FORCE)"), vxn("DROP ROLE IF EXISTS cf_vx_eigenaar")} {
			_, _ = admin.Exec(context.Background(), q)
		}
	})

	// De oude kluis: een stukje van het schema van VaultX, met bytea,
	// tijden, json en een grote tabel (alleen geteld, geen md5).
	oudCfg := cfg.Copy()
	oudCfg.Database = vxn("cf_vx_oud")
	oud, err := pgx.ConnectConfig(ctx, oudCfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE alembic_version (version_num varchar(32) PRIMARY KEY)",
		"INSERT INTO alembic_version VALUES ('0013')",
		"CREATE TABLE users (id uuid PRIMARY KEY, email text, created_at timestamptz, failed_logins int)",
		"INSERT INTO users SELECT gen_random_uuid(), 'u' || i || '@example.be', now() - i * interval '1 day', i % 3 FROM generate_series(1, 25) i",
		"CREATE TABLE vault_ciphers (id uuid PRIMARY KEY, user_id uuid, data jsonb, revision timestamptz, score double precision)",
		"INSERT INTO vault_ciphers SELECT gen_random_uuid(), (SELECT id FROM users LIMIT 1), jsonb_build_object('name', '2.encrypted|' || i), now(), i / 7.0 FROM generate_series(1, 200) i",
		"CREATE TABLE vault_attachments (id uuid PRIMARY KEY, data bytea)",
		"INSERT INTO vault_attachments SELECT gen_random_uuid(), (SELECT decode(string_agg(md5(random()::text || i || g), ''), 'hex') FROM generate_series(1, 200) g) FROM generate_series(1, 300) i",
		"CREATE TABLE leeg (id int)",
	} {
		exe(oud, q)
	}
	_ = oud.Close(ctx)

	host, port := cfg.Host, cfg.Port
	pgArgs := fmt.Sprintf("-h %s -p %d -U %s", host, port, cfg.User)
	if cfg.Password != "" {
		t.Setenv("PGPASSWORD", cfg.Password)
	}
	baseEnv := fmt.Sprintf("VAULTX_DATABASE_URL=postgresql+psycopg://%s:Wachtwoord-123@%s:%d/%s\n", vxn("cf_vx_eigenaar"), host, port, vxn("cf_vx_doel"))
	oudEnv := "VAULTX_DATABASE_URL=postgresql+psycopg://vaultx:oud@127.0.0.1:5432/vaultx\nVAULTX_PUBLIC_URL=https://vaultx.example.be\n" +
		"VAULTX_SECRET_KEY=" + vxSecret + "\nVAULTX_OIDC_CLIENT_SECRET=client-geheim\n# commentaar\nANDERS=x\n"

	tpl, c := vxContext(t, nil)
	files, _ := npmFiles(t, tpl, c)

	// De oude code: een git-repo met één commit.
	n0 := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", n0}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	commitOut, _ := exec.Command("git", "-C", n0, "rev-parse", "HEAD").Output()
	commit := strings.TrimSpace(string(commitOut))

	n := newVXNode(t, files[vxCtl], files[vxBron], baseEnv, oudEnv, commit, pgArgs, vxn("cf_vx_oud"))
	if err := os.Rename(n0, filepath.Join(n.dir, "oudcode")); err != nil {
		t.Fatal(err)
	}

	// bron-koppelen: een sleutel en één regel met het script en de sleutel,
	// alleen vanaf deze node en het VIP.
	write(t, map[string]string{filepath.Join(n.dir, "vaultx-bron.sh"): files[vxBron]})
	if _, err := exec.LookPath("ssh-keygen"); err == nil {
		out, err := n.run("bron-koppelen", "192.0.2.157")
		if err != nil || !strings.Contains(out, `/usr/local/sbin/cf-vaultx-bron sleutel 'restrict,from="192.0.2.61,192.0.2.60",command="/usr/local/sbin/cf-vaultx-bron" ssh-ed25519 `) {
			t.Fatalf("bron-koppelen: %v\n%s", err, out)
		}
		b64 := strings.SplitN(strings.SplitN(out, "printf %s '", 2)[1], "'", 2)[0]
		dec, err := exec.Command("sh", "-c", "printf %s '"+b64+"' | base64 -d | gunzip").Output()
		if err != nil || string(dec) != files[vxBron] {
			t.Errorf("het meegegeven script is niet vaultx-bron.sh: %v", err)
		}
		if st, err := os.Stat(filepath.Join(n.dir, "state", "bron_ed25519")); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("sleutel: %v %v", st, err)
		}
	}
	if out, err := n.run("bron-koppelen", "geen-ip"); err == nil {
		t.Errorf("bron-koppelen zonder IP: %s", out)
	}

	// Vóór de overname: VaultX start nergens.
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "nog niet overgenomen") {
		t.Fatalf("voorstart vóór de overname: %v\n%s", err, out)
	}

	// 0. De oude VaultX draait op deze node zelf (lokaal): een mislukte
	// overname zet ook zijn eigen vaultx.env terug, die voorstart intussen
	// kon overschrijven.
	oudUnit := filepath.Join(n.dir, "vaultx.service")
	oudLokaal := "VAULTX_DATABASE_URL=postgresql+psycopg://vaultx:oud@127.0.0.1:5433/vaultx\nVAULTX_SECRET_KEY=" + vxSecret + "\n"
	write(t, map[string]string{oudUnit: "[Service]\n", filepath.Join(n.dir, "state", "oud", "vaultx.env"): oudLokaal,
		filepath.Join(n.dir, "vxetc", "vaultx.env"): "door voorstart overschreven\n"})
	uc := filepath.Join(n.dir, "user_clusters")
	n.env = append(n.env, "CF_VX_OUD_UNIT="+oudUnit, "CF_VX_USER_CLUSTERS="+uc)
	if out, err := n.run("overnemen"); err == nil || !strings.Contains(out, "oude VaultX draait weer") {
		t.Fatalf("lokale overname zonder gezonde VaultX: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(n.dir, "vxetc", "vaultx.env")); string(b) != oudLokaal {
		t.Errorf("vaultx.env van de oude VaultX niet teruggezet:\n%s", b)
	}
	// Klaargezet door een oudere versie (zonder user_clusters): voorbereid
	// faalt, voorbereiden zet alleen de regel voor psql.
	if out, err := n.run("voorbereid"); err == nil {
		t.Errorf("voorbereid zonder user_clusters: %s", out)
	}
	if out, err := n.run("voorbereiden"); err != nil || !strings.Contains(out, "5432 (Patroni)") {
		t.Errorf("voorbereiden na een oudere versie: %v %s", err, out)
	}
	if b, _ := os.ReadFile(uc); !strings.Contains(string(b), "* * 17 /var/run/postgresql:5432 *") {
		t.Errorf("user_clusters:\n%s", b)
	}
	if out, err := n.run("voorbereid"); err != nil {
		t.Errorf("voorbereid met vaultxoud: %v %s", err, out)
	}
	if err := os.Remove(oudUnit); err != nil {
		t.Fatal(err)
	}

	// 1. VaultX van het cluster wordt niet gezond: alles terugdraaien.
	out, err := n.run("overnemen", "192.0.2.157")
	if err == nil || !strings.Contains(out, "antwoordt niet gezond") || !strings.Contains(out, "oude VaultX draait weer") {
		t.Fatalf("overname zonder gezonde VaultX: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Error("de oude VaultX is na het terugdraaien niet gestart")
	}
	if n.exists("opt/files/.cf/vaultx.env") {
		t.Error("overgenomen instellingen bleven staan na het terugdraaien")
	}
	doelCfg := cfg.Copy()
	doelCfg.Database = vxn("cf_vx_doel")
	doel, err := pgx.ConnectConfig(ctx, doelCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = doel.Close(ctx) }()
	var tables int
	if err := doel.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("database van het cluster na het terugdraaien: %d tabellen %v", tables, err)
	}
	// De back-up op de oude node is er wel.
	if m, _ := filepath.Glob(filepath.Join(n.dir, "oud", "backups", "cf-overname-*.dump")); len(m) == 0 {
		t.Errorf("back-up op de oude node: %v", m)
	}

	// 2. Een andere commit zonder --andere-versie: weigeren vóór de stop.
	write(t, map[string]string{filepath.Join(n.dir, "state", "gebouwd"): "main 0000000000000000000000000000000000000000\n"})
	if out, err := n.run("overnemen", "192.0.2.157"); err == nil || !strings.Contains(out, "Zet Versie van VaultX op "+commit) {
		t.Fatalf("andere commit: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Fatal("de oude VaultX werd gestopt bij een geweigerde overname")
	}
	write(t, map[string]string{filepath.Join(n.dir, "state", "gebouwd"): "main " + commit + "\n"})

	// 3. Geslaagde overname.
	write(t, map[string]string{filepath.Join(n.dir, "gezond"): ""})
	out, err = n.run("overnemen", "192.0.2.157")
	if err != nil || !strings.Contains(out, "Overgenomen") {
		t.Fatalf("overname: %v\n%s", err, out)
	}
	for _, w := range []string{"vault_ciphers", "aantal+md5 ja", "vault_attachments", "aantal ja", "sha256 gelijk", "Oude VaultX stoppen"} {
		if !strings.Contains(out, w) {
			t.Errorf("uitvoer mist %q:\n%s", w, out)
		}
	}
	if n.exists("oud-actief") {
		t.Error("de oude VaultX draait nog na de overname")
	}
	for tb, want := range map[string]int{"users": 25, "vault_ciphers": 200, "vault_attachments": 300, "alembic_version": 1} {
		var got int
		if err := doel.QueryRow(ctx, "SELECT count(*) FROM "+tb).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %d %v", tb, got, err)
		}
	}
	var meta string
	if err := doel.QueryRow(ctx, "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = current_database()").Scan(&meta); err != nil ||
		!strings.Contains(meta, "cf-vaultx key=") || !strings.Contains(meta, "commit="+commit) {
		t.Errorf("markering: %q %v", meta, err)
	}
	cfEnv := filepath.Join(n.dir, "opt", "files", ".cf", "vaultx.env")
	st, err := os.Stat(cfEnv)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v", cfEnv, st, err)
	}
	b, _ := os.ReadFile(cfEnv)
	if !strings.Contains(string(b), "VAULTX_SECRET_KEY="+vxSecret) || strings.Contains(string(b), "ANDERS") || strings.Contains(string(b), "vaultx:oud@") {
		t.Errorf("overgenomen instellingen:\n%s", b)
	}
	// Geen kopie van de instellingen in de werkmap van de overname.
	if m, _ := filepath.Glob(filepath.Join(n.dir, "state", "overname", "*", "vaultx.env")); len(m) != 0 {
		t.Errorf("instellingen bleven in de werkmap: %v", m)
	}

	// Een tweede overname weigert.
	write(t, map[string]string{filepath.Join(n.dir, "oud-actief"): ""})
	if out, err := n.run("overnemen", "192.0.2.157"); err == nil || !strings.Contains(out, "al in gebruik") {
		t.Fatalf("tweede overname: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Fatal("een geweigerde tweede overname stopte de oude VaultX")
	}
	_ = os.Remove(filepath.Join(n.dir, "oud-actief"))

	// voorstart: de instellingen van de oude node, met de database van het
	// cluster, en geen sleutel van de template.
	if out, err := n.run("voorstart"); err != nil {
		t.Fatalf("voorstart: %v\n%s", err, out)
	}
	b, _ = os.ReadFile(filepath.Join(n.dir, "vxetc", "vaultx.env"))
	if !strings.Contains(string(b), "VAULTX_SECRET_KEY="+vxSecret) || !strings.Contains(string(b), "VAULTX_PUBLIC_URL=https://vaultx.example.be") ||
		!strings.Contains(string(b), "@"+host) || strings.Contains(string(b), "vaultx:oud@") {
		t.Errorf("vaultx.env:\n%s", b)
	}
	// Een node met een andere sleutel start niet.
	write(t, map[string]string{cfEnv: strings.ReplaceAll(string(b), vxSecret, "een-andere-sleutel-0123456789-abcdefghij")})
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "niet die van het cluster") {
		t.Fatalf("andere sleutel: %v\n%s", err, out)
	}
	write(t, map[string]string{cfEnv: string(b)})

	// Nachtelijke back-up.
	if out, err := n.run("backup"); err != nil || !strings.Contains(out, "back-up klaar") {
		t.Fatalf("backup: %v\n%s", err, out)
	}

	// Terugweg: dump van het cluster, markering weg, oude VaultX aan.
	out, err = n.run("terug", "192.0.2.157")
	if err != nil || !strings.Contains(out, "Terug op 192.0.2.157") {
		t.Fatalf("terug: %v\n%s", err, out)
	}
	if !n.exists("oud-actief") {
		t.Error("de oude VaultX draait niet na terug")
	}
	if m, _ := filepath.Glob(filepath.Join(n.dir, "state", "overname", "cluster-*.dump")); len(m) != 1 {
		t.Errorf("dump van het cluster: %v", m)
	}
	if out, err := n.run("voorstart"); err == nil || !strings.Contains(out, "nog niet overgenomen") {
		t.Fatalf("voorstart na terug: %v\n%s", err, out)
	}
}

// TestVaultXBronSleutel: cf-vaultx-bron zet alleen een sleutelregel van
// bron-koppelen, doet via SSH alleen vaste opdrachten, en ruimt op.
func TestVaultXBronSleutel(t *testing.T) {
	tpl, c := vxContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	dir := t.TempDir()
	bron := filepath.Join(dir, "cf-vaultx-bron")
	auth := filepath.Join(dir, "ssh", "authorized_keys")
	write(t, map[string]string{bron: files[vxBron], auth: "ssh-ed25519 AAAAbestaand jonas@pc\n"})
	run := func(sshCmd string, args ...string) (string, error) {
		cmd := exec.Command("/bin/sh", append([]string{bron}, args...)...)
		cmd.Env = append(os.Environ(), "CF_VXB_AUTH="+auth, "SSH_ORIGINAL_COMMAND="+sshCmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	line := `restrict,from="192.0.2.61,192.0.2.60",command="/usr/local/sbin/cf-vaultx-bron" ssh-ed25519 AAAAnieuw cf-vaultx-bron`
	if out, err := run("", "sleutel", "ssh-ed25519 AAAAvrij iemand"); err == nil {
		t.Fatalf("een vrije sleutel aanvaard: %s", out)
	}
	if out, err := run("sleutel", "sleutel", line); err == nil {
		t.Fatalf("sleutel via SSH aanvaard: %s", out)
	}
	for i := 0; i < 2; i++ {
		if out, err := run("", "sleutel", line); err != nil {
			t.Fatalf("sleutel: %v %s", err, out)
		}
	}
	b, _ := os.ReadFile(auth)
	if strings.Count(string(b), "cf-vaultx-bron\n") != 1 || !strings.Contains(string(b), "jonas@pc") {
		t.Errorf("authorized_keys:\n%s", b)
	}
	if out, err := run("rm -rf /"); err == nil || !strings.Contains(out, "onbekende opdracht") {
		t.Errorf("vrije opdracht via SSH: %v %s", err, out)
	}
	if out, err := run("opruimen"); err != nil {
		t.Fatalf("opruimen: %v %s", err, out)
	}
	b, _ = os.ReadFile(auth)
	if strings.Contains(string(b), "cf-vaultx-bron") || !strings.Contains(string(b), "jonas@pc") {
		t.Errorf("na opruimen:\n%s", b)
	}
}
