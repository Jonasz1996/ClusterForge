package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestParseGreeting(t *testing.T) {
	handshake := func(version string, caps uint16) []byte {
		b := []byte{10}
		b = append(b, version...)
		b = append(b, 0)
		b = append(b, 1, 0, 0, 0)            // thread-id
		b = append(b, []byte("abcdefgh")...) // scramble
		b = append(b, 0)                     // opvulling
		b = binary.LittleEndian.AppendUint16(b, caps)
		return append(b, 0x2d, 2, 0)
	}
	for _, tc := range []struct {
		name string
		body []byte
		want protocol.DBMigrateReach
	}{
		{"11.8 met TLS", handshake("11.8.3-MariaDB-0+deb13u1", 0xfffe), protocol.DBMigrateReach{Reached: true, Version: "11.8.3-MariaDB-0+deb13u1", TLS: true}},
		{"10.11 zonder TLS", handshake("5.5.5-10.11.6-MariaDB-0+deb12u1", 0xf7fe), protocol.DBMigrateReach{Reached: true, Version: "10.11.6-MariaDB-0+deb12u1"}},
		{"adres niet toegelaten", append([]byte{0xff, 0x6a, 0x04}, "Host '10.0.20.186' is not allowed to connect to this MariaDB server"...),
			protocol.DBMigrateReach{Reached: true, Code: 1130, Error: "Host '10.0.20.186' is not allowed to connect to this MariaDB server"}},
		{"fout met sqlstate", append([]byte{0xff, 0x10, 0x04, '#', 'H', 'Y', '0', '0', '0'}, "Too many connections"...),
			protocol.DBMigrateReach{Reached: true, Code: 1040, Error: "Too many connections"}},
		{"geen MariaDB", []byte("SSH-2.0-OpenSSH_9.2p1"), protocol.DBMigrateReach{Error: "x:3306 is geen MariaDB of MySQL"}},
	} {
		if got := parseGreeting("x:3306", tc.body); got != tc.want {
			t.Errorf("%s: %+v, wil %+v", tc.name, got, tc.want)
		}
	}
}

func TestMariaAtLeast(t *testing.T) {
	for v, want := range map[string]bool{
		"11.8.3-MariaDB-0+deb13u1":  true,
		"11.3.0-MariaDB":            true,
		"11.2.9-MariaDB":            false,
		"10.11.6-MariaDB-0+deb12u1": false,
		"12.0.1-MariaDB":            true,
		"8.0.36":                    false,
	} {
		if got := mariaAtLeast(v, 11, 3); got != want {
			t.Errorf("%s: %v", v, got)
		}
	}
}

func TestMigrateSQL(t *testing.T) {
	got, err := dbsql.MigrateGrant("next_cloud", "cf_verhuizing_ab12", []string{"10.0.20.186"}, "Abcdefgh12345678",
		dbsql.MigrateRights{Routines: "proc", Triggers: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE OR REPLACE USER 'cf_verhuizing_ab12'@'10.0.20.186' IDENTIFIED BY 'Abcdefgh12345678';\n" +
		"GRANT SELECT, SHOW VIEW, TRIGGER ON `next\\_cloud`.* TO 'cf_verhuizing_ab12'@'10.0.20.186';\n" +
		"GRANT SELECT ON `mysql`.`proc` TO 'cf_verhuizing_ab12'@'10.0.20.186';\n"
	if got != want {
		t.Errorf("grant:\n%s\nwil\n%s", got, want)
	}
	got, _ = dbsql.MigrateGrant("wiki", "cf_verhuizing_ab12", []string{"10.0.20.186"}, "Abcdefgh12345678", dbsql.MigrateRights{Routines: "db", Events: true})
	if !strings.Contains(got, "GRANT SELECT, SHOW VIEW, EVENT, SHOW CREATE ROUTINE ON `wiki`.*") || strings.Contains(got, "mysql") {
		t.Errorf("grant 11.3: %s", got)
	}
	for _, bad := range []struct{ db, user, host, pw string }{
		{"wiki", "nextcloud", "10.0.20.186", "Abcdefgh12345678"},
		{"wiki", "cf_verhuizing_x'", "10.0.20.186", "Abcdefgh12345678"},
		{"wiki", "cf_verhuizing_ab", "10.0.20.186'", "Abcdefgh12345678"},
		{"wiki", "cf_verhuizing_ab", "10.0.20.186", "kort"},
		{"mysql", "cf_verhuizing_ab", "10.0.20.186", "Abcdefgh12345678"},
	} {
		if _, err := dbsql.MigrateGrant(bad.db, bad.user, []string{bad.host}, bad.pw, dbsql.MigrateRights{}); err == nil {
			t.Errorf("geen fout voor %+v", bad)
		}
	}
	tables := make([]string, 0, 150)
	for i := range 150 {
		tables = append(tables, fmt.Sprintf("t%d", i))
	}
	tables[3] = "raar`naam"
	qs, err := dbsql.CountRows("wiki", tables)
	if err != nil || len(qs) != 2 || strings.Count(qs[0], "COUNT(*)") != 100 || strings.Count(qs[1], "COUNT(*)") != 50 {
		t.Fatalf("tellen: %d %v", len(qs), err)
	}
	if !strings.Contains(qs[0], "(SELECT COUNT(*) FROM `wiki`.`raar``naam`)") {
		t.Errorf("naam met backtick: %s", qs[0][:200])
	}
	if q, _ := dbsql.CreateDatabaseAs("wiki_proef", "utf8mb4", "utf8mb4_general_ci"); q != "CREATE DATABASE `wiki_proef` CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci;\n" {
		t.Errorf("maken: %s", q)
	}
	if _, err := dbsql.CreateDatabaseAs("wiki_proef", "utf8mb4; DROP", ""); err == nil {
		t.Error("tekenset met ;")
	}
}

// dockerAgent is een agent waarvan de MariaDB in een container draait. Een
// optiebestand voor een andere server zet hij om in opties, want het
// bestand staat niet in de container.
func dockerAgent(t *testing.T, container string) *Agent {
	t.Helper()
	translate := func(args []string) []string {
		if len(args) == 0 || !strings.HasPrefix(args[0], "--defaults-extra-file=") {
			return args
		}
		f, err := os.Open(strings.TrimPrefix(args[0], "--defaults-extra-file="))
		if err != nil {
			t.Errorf("optiebestand: %v", err)
			return args[1:]
		}
		defer func() { _ = f.Close() }()
		var opts []string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), "="); ok {
				opts = append(opts, "--"+k+"="+v)
			}
		}
		return append(opts, args[1:]...)
	}
	a := &Agent{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return []byte("Id=mariadb.service\nLoadState=loaded\nActiveState=active\n\n"), nil
		case "mariadb":
			out, err := exec.CommandContext(ctx, "docker", append([]string{"exec", container, "mariadb"}, translate(args)...)...).Output()
			if ee, ok := err.(*exec.ExitError); ok {
				err = fmt.Errorf("%w: %s", err, ee.Stderr)
			}
			return out, err
		}
		return nil, fmt.Errorf("onbekend commando %s", name)
	}
	a.ExecStdin = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", container, name}, translate(args)...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		return cmd.CombinedOutput()
	}
	a.Stream = func(ctx context.Context, in io.Reader, out io.Writer, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", container, name}, translate(args)...)...)
		cmd.Stdin, cmd.Stdout = in, out
		var stderr tailWriter
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %s", name, stderr.errorLine())
		}
		return nil
	}
	return a
}

func containerIP(t *testing.T, container string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", container).Output()
	if err != nil {
		t.Fatal(err)
	}
	ip, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return ip
}

func migrate(t *testing.T, a *Agent, req protocol.DBMigrateRequest) *protocol.DBMigrateResult {
	t.Helper()
	res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBMigrate, Migrate: &req})
	if res.Error != "" {
		t.Fatalf("%s: %s", req.Action, res.Error)
	}
	return res.Migrate
}

func migrateErr(t *testing.T, a *Agent, req protocol.DBMigrateRequest) string {
	t.Helper()
	res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBMigrate, Migrate: &req})
	if res.Error == "" {
		t.Fatalf("%s %s: geen fout", req.Action, req.Database)
	}
	return res.Error
}

// waitPull wacht tot de import van target klaar is.
func waitPull(t *testing.T, a *Agent, target string) protocol.DBMigrateRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		r := migrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateStatus, Target: target}).Run
		if r == nil {
			t.Fatalf("geen import van %s", target)
		}
		if !r.Finished.IsZero() {
			return *r
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("de import van %s is niet klaar", target)
	return protocol.DBMigrateRun{}
}

// TestDBMigrateRealMariaDB verhuist een database van de MariaDB in
// CF_TEST_MARIADB_SOURCE (standaard dezelfde als CF_TEST_MARIADB) naar die
// in CF_TEST_MARIADB, met een tijdelijke gebruiker die alleen vanaf het
// adres van de nieuwe server mag komen. In de CI is de oude een MariaDB
// 10.11 en de nieuwe 11.8, zoals Debian 12 en 13.
func TestDBMigrateRealMariaDB(t *testing.T) {
	dst := os.Getenv("CF_TEST_MARIADB")
	if dst == "" {
		t.Skip("CF_TEST_MARIADB niet gezet")
	}
	src := os.Getenv("CF_TEST_MARIADB_SOURCE")
	if src == "" {
		src = dst
	}
	const db, target, user, password = "cf_agent_m8", "cf_agent_m8_proef", "cf_verhuizing_m8test", "Verhuis0123456789abc"
	sql := func(container, query string) string {
		t.Helper()
		out, err := exec.Command("docker", "exec", container, "mariadb", "-N", "-B", "-e", query).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", query, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	srcIP, dstIP := containerIP(t, src), containerIP(t, dst)
	cleanup := func() {
		sql(src, "DROP DATABASE IF EXISTS "+db+"; DROP USER IF EXISTS '"+user+"'@'"+dstIP+"'")
		sql(dst, "DROP DATABASE IF EXISTS "+target+"; DROP DATABASE IF EXISTS "+target+"2")
	}
	cleanup()
	t.Cleanup(cleanup)
	sql(src, "CREATE DATABASE "+db+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; USE "+db+";"+
		"CREATE TABLE files (id int PRIMARY KEY, name varchar(100), data varbinary(64));"+
		"CREATE TABLE log (file int, at datetime);"+
		"CREATE TABLE cache (k varchar(40) PRIMARY KEY, v text) ENGINE=MyISAM;"+
		"CREATE VIEW names AS SELECT name FROM files;"+
		"CREATE TRIGGER files_log AFTER INSERT ON files FOR EACH ROW INSERT INTO log (file, at) VALUES (NEW.id, '2026-10-12 02:00:00');"+
		"CREATE PROCEDURE opruimen() DELETE FROM log WHERE at < '2000-01-01';"+
		"CREATE EVENT nachtelijk ON SCHEDULE EVERY 1 DAY DISABLE DO DELETE FROM cache;"+
		"INSERT INTO files VALUES (1, 'één.txt', 0x00FF0A0D27), (2, 'tab\there', NULL), (3, 'quote''s', 0x5C);"+
		"INSERT INTO cache VALUES ('a', 'x'), ('b', 'y');")
	checksum := func(container, d string) string {
		return sql(container, "SELECT GROUP_CONCAT(id, ':', name, ':', COALESCE(HEX(data), '-') ORDER BY id SEPARATOR '|') FROM "+d+".files") +
			" / " + sql(container, "SELECT COUNT(*) FROM "+d+".log") + " / " + sql(container, "SELECT COUNT(*) FROM "+d+".names")
	}
	want := checksum(src, db)

	old, nw := dockerAgent(t, src), dockerAgent(t, dst)
	info := migrate(t, old, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: db}).Info
	if !info.Exists || info.Charset != "utf8mb4" || info.Collation != "utf8mb4_unicode_ci" || len(info.Tables) != 3 ||
		info.Views != 1 || info.Routines != 1 || info.Triggers != 1 || info.Events != 1 {
		t.Fatalf("inspect: %+v", info)
	}
	byName := map[string]protocol.DBMigrateTable{}
	for _, tb := range info.Tables {
		byName[tb.Name] = tb
	}
	if !byName["files"].PrimaryKey || byName["log"].PrimaryKey || byName["cache"].Engine != "MyISAM" || byName["files"].Engine != "InnoDB" {
		t.Errorf("tabellen: %+v", info.Tables)
	}
	if i := migrate(t, old, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: "bestaat_niet"}).Info; i.Exists || i.Version == "" {
		t.Errorf("bestaat niet: %+v", i)
	}

	p := migrate(t, nw, protocol.DBMigrateRequest{Action: protocol.DBMigrateProbe, Host: srcIP, Port: 3306}).Probe
	if !p.Reached || !strings.Contains(p.Version, "MariaDB") || strings.HasPrefix(p.Version, "5.5.5") {
		t.Fatalf("probe: %+v", p)
	}
	if p := migrate(t, nw, protocol.DBMigrateRequest{Action: protocol.DBMigrateProbe, Host: srcIP, Port: 3307}).Probe; p.Reached || p.Error == "" {
		t.Errorf("verkeerde poort: %+v", p)
	}

	pull := protocol.DBMigrateRequest{
		Action: protocol.DBMigratePull, Database: db, Target: target, User: user, Password: password,
		Host: srcIP, Port: 3306, Charset: info.Charset, Collation: info.Collation, Total: info.SizeBytes,
	}
	// Zonder de tijdelijke gebruiker komt de nieuwe server er niet in, en
	// de halve database gaat weer weg.
	migrate(t, nw, pull)
	if r := waitPull(t, nw, target); !strings.Contains(r.Error, "Access denied") || strings.Contains(r.Error, password) {
		t.Fatalf("zonder gebruiker: %+v", r)
	}
	if got := sql(dst, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '"+target+"'"); got != "0" {
		t.Fatalf("%s staat er nog", target)
	}

	migrate(t, old, protocol.DBMigrateRequest{Action: protocol.DBMigrateGrant, Database: db, User: user, Hosts: []string{dstIP}, Password: password})
	grants := sql(src, "SHOW GRANTS FOR '"+user+"'@'"+dstIP+"'")
	if !strings.Contains(grants, "SELECT, SHOW VIEW") || strings.Contains(grants, "INSERT") || strings.Contains(grants, "ALL PRIVILEGES") {
		t.Errorf("rechten: %s", grants)
	}
	migrate(t, nw, pull)
	r := waitPull(t, nw, target)
	if r.Error != "" || r.Imported.IsZero() || r.Bytes == 0 {
		t.Fatalf("import: %+v", r)
	}
	if r.Views != 1 || r.Routines != 1 || r.Triggers != 1 || r.Events != 1 || len(r.Tables) != 3 {
		t.Errorf("objecten: %+v", r)
	}
	for _, c := range r.Tables {
		if c.Source != c.Target || c.Source == 0 {
			t.Errorf("tabel %s: %d oud, %d nieuw", c.Name, c.Source, c.Target)
		}
	}
	if got := checksum(dst, target); got != want {
		t.Fatalf("na de import: %s, wil %s", got, want)
	}
	if got := sql(dst, "SELECT DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '"+target+"'"); got != "utf8mb4_unicode_ci" {
		t.Errorf("collatie: %s", got)
	}
	if e := migrateErr(t, nw, pull); !strings.Contains(e, "bestaat al") {
		t.Errorf("nog eens: %s", e)
	}

	migrate(t, old, protocol.DBMigrateRequest{Action: protocol.DBMigrateRevoke, User: user, Hosts: []string{dstIP}})
	if got := sql(src, "SELECT COUNT(*) FROM mysql.user WHERE User = '"+user+"'"); got != "0" {
		t.Errorf("de tijdelijke gebruiker staat er nog: %s", got)
	}
	pull.Target = target + "2"
	migrate(t, nw, pull)
	if r := waitPull(t, nw, pull.Target); !strings.Contains(r.Error, "Access denied") {
		t.Errorf("na revoke: %+v", r)
	}
}

// TestDBMigrate verhuist tussen twee nagespeelde machines: db1 met de oude
// MariaDB en nc1 die de dump ophaalt.
func TestDBMigrate(t *testing.T) {
	old := agenttest.NewHost(t.TempDir(), "10.0.20.181")
	old.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	old.MariaDB = syncedMaria()
	old.MariaDB.Version = "10.11.6-MariaDB-0+deb12u1"
	old.MariaDB.Databases = []agenttest.Database{{
		Name: "nextcloud", Size: 2 << 20, Tables: 3, Rows: 42, NoPK: []string{"t2"}, Engines: map[string]string{"t3": "MyISAM"}, Triggers: 1,
	}}
	nw := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	nw.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	nw.MariaDB = syncedMaria()
	network := func(addr string) *agenttest.Host {
		if addr == "10.0.20.180:3306" {
			return old
		}
		return nil
	}
	old.Network, nw.Network = network, network
	agent := func(h *agenttest.Host) *Agent {
		return &Agent{Exec: h.Exec, ExecStdin: h.ExecStdin, Stream: h.Stream, Dial: h.Dial, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	}
	a, b := agent(old), agent(nw)

	info := migrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: "nextcloud"}).Info
	if !info.Exists || len(info.Tables) != 3 || info.Tables[1].PrimaryKey || info.Tables[2].Engine != "MyISAM" || info.Triggers != 1 || info.SizeBytes == 0 {
		t.Fatalf("inspect: %+v", info)
	}
	if p := migrate(t, b, protocol.DBMigrateRequest{Action: protocol.DBMigrateProbe, Host: "10.0.20.180", Port: 3306}).Probe; !p.Reached || p.Version != old.MariaDB.Version || p.TLS {
		t.Fatalf("probe: %+v", p)
	}
	if p := migrate(t, b, protocol.DBMigrateRequest{Action: protocol.DBMigrateProbe, Host: "10.0.20.99", Port: 3306}).Probe; p.Reached || !strings.Contains(p.Error, "refused") {
		t.Fatalf("probe weg: %+v", p)
	}
	if e := migrateErr(t, b, protocol.DBMigrateRequest{Action: protocol.DBMigrateProbe, Host: "10.0.20.180; rm", Port: 3306}); !strings.Contains(e, "geen adres") {
		t.Errorf("adres: %s", e)
	}

	const user, password = "cf_verhuizing_ab12cd34", "Verhuis0123456789abc"
	migrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateGrant, Database: "nextcloud", User: user, Hosts: []string{"10.0.20.186"}, Password: password})
	var grants []string
	old.Locked(func() {
		for _, u := range old.MariaDB.Users {
			if u.User == user {
				grants = u.Grants[`nextcloud`]
			}
		}
	})
	if strings.Join(grants, ",") != "SELECT,SHOW VIEW,TRIGGER" {
		t.Errorf("rechten: %v", grants)
	}
	if calls := strings.Join(old.Calls(), "\n"); strings.Contains(calls, password) {
		t.Errorf("het wachtwoord staat in een argument:\n%s", calls)
	}

	pull := protocol.DBMigrateRequest{
		Action: protocol.DBMigratePull, Database: "nextcloud", Target: "nextcloud_proef", User: user, Password: password,
		Host: "10.0.20.180", Port: 3306, Charset: "utf8mb4", Collation: "utf8mb4_unicode_ci", Total: info.SizeBytes,
	}
	migrate(t, b, pull)
	r := waitPull(t, b, "nextcloud_proef")
	if r.Error != "" || len(r.Tables) != 3 || r.Triggers != 1 || r.Imported.IsZero() || r.Bytes == 0 {
		t.Fatalf("import: %+v", r)
	}
	for _, c := range r.Tables {
		if c.Source != 42 || c.Target != 42 {
			t.Errorf("tabel %+v", c)
		}
	}
	if calls := strings.Join(nw.Calls(), "\n"); strings.Contains(calls, password) {
		t.Errorf("het wachtwoord staat in een argument:\n%s", calls)
	}

	// Een verkeerd wachtwoord: de import mislukt zonder het wachtwoord in
	// de fout, en de halve database gaat weg.
	pull.Target, pull.Password = "nextcloud_proef2", "Verkeerd0123456789xyz"
	migrate(t, b, pull)
	if r := waitPull(t, b, pull.Target); !strings.Contains(r.Error, "Access denied for user 'cf_verhuizing_ab12cd34'@'10.0.20.186'") ||
		strings.Contains(r.Error, pull.Password) {
		t.Errorf("verkeerd wachtwoord: %+v", r)
	}
	nw.Locked(func() {
		if slices.ContainsFunc(nw.MariaDB.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud_proef2" }) {
			t.Error("nextcloud_proef2 staat er nog")
		}
	})

	// Een dump zonder slotregel is niet compleet.
	old.Locked(func() { old.MariaDB.DumpIncomplete = true })
	pull.Target, pull.Password = "nextcloud_proef3", password
	migrate(t, b, pull)
	if r := waitPull(t, b, pull.Target); !strings.Contains(r.Error, "niet compleet") {
		t.Errorf("niet compleet: %+v", r)
	}
	old.Locked(func() { old.MariaDB.DumpIncomplete = false })

	if e := migrateErr(t, b, protocol.DBMigrateRequest{Action: protocol.DBMigratePull, Database: "nextcloud", Target: "nextcloud_proef",
		User: user, Password: password, Host: "10.0.20.180", Port: 3306}); !strings.Contains(e, "bestaat al") {
		t.Errorf("bestaat al: %s", e)
	}
	migrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateRevoke, User: user, Hosts: []string{"10.0.20.186"}})
	old.Locked(func() {
		if slices.ContainsFunc(old.MariaDB.Users, func(u agenttest.MariaUser) bool { return u.User == user }) {
			t.Error("de tijdelijke gebruiker staat er nog")
		}
	})
	if e := migrateErr(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateRevoke, User: "nextcloud", Hosts: []string{"10.0.20.186"}}); !strings.Contains(e, "geen tijdelijke gebruiker") {
		t.Errorf("andere gebruiker weghalen: %s", e)
	}
}
