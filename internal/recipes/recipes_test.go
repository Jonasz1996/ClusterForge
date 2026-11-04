package recipes

import (
	"encoding/json"
	"strings"
	"testing"
)

// Het Zabbix-voorbeeld uit docs/design/fase9-recepten.md.
const zabbix = `RECIPE zabbix 8.0
FROM debian:13
NEEDS database mariadb           # zonder databasecluster kan je dit niet uitrollen

SECRET db_password               # ClusterForge maakt het, bewaart het versleuteld

# a. Repository van Zabbix
REPO_DEB https://repo.zabbix.com/zabbix/8.0/release/debian/pool/main/z/zabbix-release/zabbix-release_latest_8.0+debian13_all.deb

# b. Pakketten
APT zabbix-server-mysql zabbix-frontend-php zabbix-apache-conf zabbix-sql-scripts zabbix-agent

# c. Database, gebruiker en schema (op het databasecluster van dit cluster)
DATABASE zabbix CHARSET utf8mb4 COLLATE utf8mb4_bin
DBUSER zabbix PASSWORD {{ secret.db_password }} GRANT ALL ON zabbix
IMPORT zabbix FROM /usr/share/zabbix/sql-scripts/mysql/server.sql.gz \
    TRUST_FUNCTIONS UNLESS "SELECT 1 FROM config"

# d. Configuratie
SET /etc/zabbix/zabbix_server.conf DBHost={{ db.host }}
SET /etc/zabbix/zabbix_server.conf DBPassword={{ secret.db_password }} MODE 0640 GROUP zabbix

# e. Diensten
SERVICE zabbix-server zabbix-agent apache2 ENABLE START

HEALTH service zabbix-server
HEALTH http http://localhost/zabbix/ 200
`

func mustParse(t *testing.T, src string) *Recipe {
	t.Helper()
	r, diags := Parse(src)
	if r == nil {
		t.Fatalf("fouten: %v", diags)
	}
	for _, d := range diags {
		t.Logf("waarschuwing: %v", d)
	}
	return r
}

func errs(t *testing.T, src string) []Diag {
	t.Helper()
	r, diags := Parse(src)
	if r != nil {
		t.Fatalf("verwachtte een fout, kreeg %+v", r)
	}
	return diags
}

func hasDiag(diags []Diag, line int, part string) bool {
	for _, d := range diags {
		if d.Line == line && strings.Contains(d.Message, part) {
			return true
		}
	}
	return false
}

func TestZabbix(t *testing.T) {
	r := mustParse(t, zabbix)
	if r.Name != "zabbix" || r.Version != "8.0" || r.From != "debian:13" {
		t.Fatalf("kop: %+v", r)
	}
	if r.NeedsDatabase() != "mariadb" || r.NeedsVIP() {
		t.Fatalf("needs: %+v", r.Needs)
	}
	if len(r.Params) != 1 || r.Params[0].Type != "secret" || r.Params[0].Length != 32 {
		t.Fatalf("params: %+v", r.Params)
	}
	if len(r.Roles) != 1 || r.Roles[0].Name != "" {
		t.Fatalf("rollen: %+v", r.Roles)
	}
	steps := r.Roles[0].Steps
	var kinds []string
	for _, s := range steps {
		kinds = append(kinds, s.Kind)
	}
	want := "repo_deb package db_database db_user db_import ini_set ini_set service service service"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("soorten:\n got %s\nwant %s", got, want)
	}
	// Database-instructies lopen één keer; de rest op elke node.
	for _, s := range steps {
		if s.Once != strings.HasPrefix(s.Kind, "db_") {
			t.Errorf("%s (regel %d): once=%v", s.Kind, s.Line, s.Once)
		}
	}
	// Het wachtwoord staat er alleen als verwijzing.
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "secret.") {
		t.Errorf("onvertaalde verwijzing: %s", b)
	}
	user := steps[3]
	if !user.Secret || user.Spec["db_user"].(map[string]any)["password"] != "{{ .params.db_password }}" {
		t.Errorf("db_user: %+v", user)
	}
	imp := steps[4].Spec["db_import"].(map[string]any)
	if imp["trust_functions"] != true || imp["unless"] != "SELECT 1 FROM config" || imp["path"] != "/usr/share/zabbix/sql-scripts/mysql/server.sql.gz" {
		t.Errorf("import: %+v", imp)
	}
	host := steps[5].Spec["ini_set"].(map[string]any)
	if host["value"] != "{{ .db.host }}" || host["mode"] != nil {
		t.Errorf("DBHost: %+v", host)
	}
	pw := steps[6]
	spec := pw.Spec["ini_set"].(map[string]any)
	if !pw.Secret || spec["value"] != "{{ .params.db_password }}" || spec["mode"] != "0640" || spec["group"] != "zabbix" {
		t.Errorf("DBPassword: %+v", pw)
	}
	// Beide SET-regels laten de drie diensten herstarten.
	n := spec["notify"].([]string)
	if strings.Join(n, ",") != "service:zabbix-server:restart,service:zabbix-agent:restart,service:apache2:restart" {
		t.Errorf("notify: %v", n)
	}
	svc := steps[7].Spec["service"].(map[string]any)
	if svc["name"] != "zabbix-server" || svc["enabled"] != true || svc["state"] != "started" {
		t.Errorf("service: %+v", svc)
	}
	if h := r.Roles[0].Health; len(h) != 2 || h[0].Kind != "service" || h[1].Target != "http://localhost/zabbix/" || h[1].Expect != 200 {
		t.Errorf("health: %+v", h)
	}
}

func TestSecretsNeverInText(t *testing.T) {
	base := "RECIPE app 1.0\nFROM debian:13\nNEEDS database mariadb\nSECRET pw\n"
	d := errs(t, base+"DBUSER app PASSWORD hunter2\n")
	if !hasDiag(d, 5, "secret") {
		t.Errorf("DBUSER met wachtwoord: %v", d)
	}
	d = errs(t, base+"DBUSER app PASSWORD x{{ secret.pw }}\n")
	if !hasDiag(d, 5, "nooit het wachtwoord zelf") {
		t.Errorf("DBUSER met voorvoegsel: %v", d)
	}
	d = errs(t, base+"SET /etc/app.conf DBPassword=hunter2\n")
	if !hasDiag(d, 5, "wachtwoord") {
		t.Errorf("SET met wachtwoord: %v", d)
	}
	d = errs(t, base+"SET /etc/app.conf password={{ param.pw }}\n")
	if !hasDiag(d, 5, "is een SECRET") {
		t.Errorf("secret als param: %v", d)
	}
	// Een bestand met een geheim krijgt 0600.
	r := mustParse(t, base+"FILE /etc/app.env <<EOF\nPW={{ secret.pw }}\nEOF\n")
	f := r.Roles[0].Steps[0]
	if !f.Secret || f.Spec["file"].(map[string]any)["mode"] != "0600" || f.Spec["file"].(map[string]any)["content"] != "PW={{ .params.pw }}\n" {
		t.Errorf("file: %+v", f)
	}
	_, w := Parse(base + "FILE /etc/app.env MODE 0644 <<EOF\nPW={{ secret.pw }}\nEOF\n")
	if !hasDiag(w, 5, "iedereen") {
		t.Errorf("leesbaar geheim zonder waarschuwing: %v", w)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		src  string
		line int
		part string
	}{
		{"FROM debian:13\n", 1, "RECIPE"},
		{"RECIPE app 1.0\nAPT nginx\n", 1, "FROM ontbreekt"},
		{"RECIPE app 1.0\nFROM debian:13\napt nginx\n", 3, "hoofdletters"},
		{"RECIPE app 1.0\nFROM centos:7\nAPT nginx\n", 2, "FROM kent"},
		{"RECIPE app 1.0\nFROM debian:13\nRUN apt-get update\n", 3, "CREATES"},
		{"RECIPE app 1.0\nFROM debian:13\nDATABASE app\n", 3, "NEEDS database"},
		{"RECIPE app 1.0\nFROM debian:13\nSET /etc/x a={{ db.host }}\n", 3, "NEEDS database"},
		{"RECIPE app 1.0\nFROM debian:13\nSET /etc/x a={{ secret.nope }}\n", 3, "staat niet eerder"},
		{"RECIPE app 1.0\nFROM debian:13\nSET /etc/x a={{ whoami }}\n", 3, "onbekende waarde"},
		{"RECIPE app 1.0\nFROM debian:13\nFILE /etc/x <<EOF\nabc\n", 3, "sluit nergens af"},
		{"RECIPE app 1.0\nFROM debian:13\nFILE /etc/x\n", 3, "inhoud"},
		{"RECIPE app 1.0\nFROM debian:13\nAPT nginx\nROLE web\n", 4, "voor de eerste stap"},
		{"RECIPE app 1.0\nFROM debian:13\nSERVICE nginx\n", 3, "ENABLE"},
		{"RECIPE app 1.0\nFROM debian:13\nSERVICE nginx START STOP\n", 3, "tegenstrijdig"},
		{"RECIPE app 1.0\nFROM debian:13\nONCE APT nginx\n", 3, "ONCE kan alleen"},
		{"RECIPE app 1.0\nFROM debian:13\nREPO_DEB https://x/y.txt\n", 3, ".deb"},
		{"RECIPE app 1.0\nFROM debian:13\nIMPORT app FROM /x.csv\n", 3, "NEEDS database"},
		{"RECIPE app 1.0\nFROM debian:13\nFROBNICATE\n", 3, "onbekende instructie"},
		{"RECIPE App 1.0\nFROM debian:13\nAPT nginx\n", 1, "kleine letters"},
		{"", 1, "leeg"},
	}
	for _, c := range cases {
		d := errs(t, c.src)
		if !hasDiag(d, c.line, c.part) {
			t.Errorf("%q: wilde regel %d met %q, kreeg %v", c.src, c.line, c.part, d)
		}
	}
}

func TestRunAndOnce(t *testing.T) {
	r := mustParse(t, `RECIPE app 1.0
FROM debian:13
NEEDS vip
RUN CREATES /opt/app/bin/app curl -fsSL "https://example.org/app.tgz" | tar -xz -C /opt/app
ONCE RUN UNLESS "test -f /var/lib/app/init" TITLE "app initialiseren" /opt/app/bin/app init --vip {{ vip }}
FILE /etc/app.conf MODE 0644 <<EOF
listen = {{ node.ip }}  # geen commentaar in een heredoc
EOF
SERVICE app ENABLE START RELOAD
SERVICE app2 ENABLE START NORESTART
`)
	s := r.Roles[0].Steps
	c := s[0].Spec["command"].(map[string]any)
	if c["run"] != `curl -fsSL "https://example.org/app.tgz" | tar -xz -C /opt/app` || c["creates"] != "/opt/app/bin/app" {
		t.Errorf("run: %+v", c)
	}
	o := s[1]
	oc := o.Spec["command"].(map[string]any)
	if !o.Once || oc["run"] != "/opt/app/bin/app init --vip {{ .params.vip }}" || oc["unless"] != "test -f /var/lib/app/init" || o.Title != "app initialiseren" {
		t.Errorf("once run: %+v", o)
	}
	// Een stap op één node laat de dienst niet herstarten.
	if oc["notify"] != nil {
		t.Errorf("once met notify: %+v", oc)
	}
	f := s[2].Spec["file"].(map[string]any)
	if f["content"] != "listen = {{ .node.address }}  # geen commentaar in een heredoc\n" {
		t.Errorf("file: %q", f["content"])
	}
	if n := f["notify"].([]string); len(n) != 1 || n[0] != "service:app:reload" {
		t.Errorf("notify: %v", n)
	}
	if c["notify"].([]string)[0] != "service:app:reload" {
		t.Errorf("run notify: %v", c["notify"])
	}
}

func TestRolesAndRestartWindow(t *testing.T) {
	r := mustParse(t, `RECIPE app 2.1.0
FROM debian:13
ROLE web
FILE /etc/a <<EOF
a
EOF
SERVICE nginx ENABLE START
FILE /etc/b <<EOF
b
EOF
SERVICE nginx START
ROLE worker
FILE /etc/c <<EOF
c
EOF
SERVICE worker ENABLE START
HEALTH port 8080
`)
	if len(r.Roles) != 2 || r.Roles[0].Name != "web" || r.Roles[1].Name != "worker" {
		t.Fatalf("rollen: %+v", r.Roles)
	}
	web := r.Roles[0].Steps
	// /etc/a laat nginx één keer herstarten (bij de eerste SERVICE), niet
	// opnieuw bij de tweede.
	if n := web[0].Spec["file"].(map[string]any)["notify"].([]string); len(n) != 1 {
		t.Errorf("/etc/a notify: %v", n)
	}
	if n := web[2].Spec["file"].(map[string]any)["notify"].([]string); len(n) != 1 || n[0] != "service:nginx:restart" {
		t.Errorf("/etc/b notify: %v", n)
	}
	if n, _ := r.Roles[1].Steps[0].Spec["file"].(map[string]any)["notify"].([]string); len(n) != 1 || n[0] != "service:worker:restart" {
		t.Errorf("/etc/c notify: %v", n)
	}
	if h := r.Roles[1].Health; len(h) != 1 || h[0].Target != "8080" {
		t.Errorf("health: %+v", h)
	}
}

func TestRepo(t *testing.T) {
	r := mustParse(t, "RECIPE pg 17\nFROM debian:13\nREPO pgdg https://apt.postgresql.org/pub/repos/apt trixie-pgdg main KEY https://www.postgresql.org/media/keys/ACCC4CF8.asc\nAPT postgresql-17\n")
	repo := r.Roles[0].Steps[0].Spec["repo"].(map[string]any)
	if repo["suite"] != "trixie-pgdg" || repo["key"] != "https://www.postgresql.org/media/keys/ACCC4CF8.asc" || len(repo["components"].([]string)) != 1 {
		t.Errorf("repo: %+v", repo)
	}
}
