package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestPGRealPostgres doet een gebruiker met database, een dump en het
// terugzetten met een tijdelijke beheerder tegen de echte PostgreSQL van
// CF_TEST_DATABASE_URL, met psql en pg_dump van de machine. psql en
// pg_dump als postgres via de socket worden de beheerder uit de URL; psql
// met een conninfo krijgt niets uit de omgeving, zodat het wachtwoord uit
// de passfile moet komen.
func TestPGRealPostgres(t *testing.T) {
	raw := os.Getenv("CF_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("CF_TEST_DATABASE_URL niet gezet")
	}
	for _, p := range []string{"psql", "pg_dump"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skipf("%s ontbreekt", p)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	if port != strconv.Itoa(protocol.DBPGPort) {
		t.Skipf("PostgreSQL op poort %s; de agent verbindt met %d", port, protocol.DBPGPort)
	}
	t.Setenv("TMPDIR", t.TempDir())
	pw, _ := u.User.Password()
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LC_ALL=C"}
	if m := u.Query().Get("sslmode"); m != "" {
		base = append(base, "PGSSLMODE="+m)
	}
	admin := append(slices.Clone(base), "PGHOST="+u.Hostname(), "PGPORT="+port, "PGUSER="+u.User.Username(), "PGPASSWORD="+pw, "PGDATABASE=postgres")
	command := func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
		env := base
		if name == "runuser" {
			if len(args) < 4 || !slices.Equal(args[:3], []string{"-u", "postgres", "--"}) {
				return nil, fmt.Errorf("runuser %s", strings.Join(args, " "))
			}
			name, args, env = args[3], args[4:], admin
		}
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = env
		return cmd, nil
	}
	a := &Agent{DumpDir: filepath.Join(t.TempDir(), "clusterforge-db"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" {
			return []byte("Id=patroni.service\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\n\n"), nil
		}
		cmd, err := command(ctx, name, args)
		if err != nil {
			return nil, err
		}
		out, err := cmd.Output()
		if ee, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%w: %s", err, ee.Stderr)
		}
		return out, err
	}
	a.ExecStdin = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		cmd, err := command(ctx, name, args)
		if err != nil {
			return nil, err
		}
		cmd.Stdin = bytes.NewReader(stdin)
		return cmd.CombinedOutput()
	}
	a.Stream = func(ctx context.Context, in io.Reader, out io.Writer, name string, args ...string) error {
		cmd, err := command(ctx, name, args)
		if err != nil {
			return err
		}
		cmd.Stdin, cmd.Stdout = in, out
		var stderr tailWriter
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %s", name, stderr.errorLine())
		}
		return nil
	}

	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	db := "cf_t_" + hex.EncodeToString(suffix[:4])
	user, target, tmp := db+"_app", db+"_herstel", "cf_herstel_"+hex.EncodeToString(suffix)
	// psqlAs voert opdrachten uit als de beheerder, of met env als iemand
	// anders, en geeft de uitvoer zonder witruimte eromheen.
	psqlAs := func(env []string, database string, sql ...string) (string, error) {
		args := []string{"-XAtq", "-v", "ON_ERROR_STOP=1", "-d", database}
		for _, s := range sql {
			args = append(args, "-c", s)
		}
		cmd := exec.Command("psql", args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	sql := func(database string, q ...string) string {
		t.Helper()
		out, err := psqlAs(admin, database, q...)
		if err != nil {
			t.Fatalf("%s: %v: %s", q, err, out)
		}
		return out
	}
	cleanup := func() {
		for _, q := range []string{
			`DROP DATABASE IF EXISTS "` + db + `" WITH (FORCE)`, `DROP DATABASE IF EXISTS "` + target + `" WITH (FORCE)`,
			`DROP ROLE IF EXISTS "` + user + `"`, `DROP ROLE IF EXISTS "` + tmp + `"`,
		} {
			_, _ = psqlAs(admin, "postgres", q)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// De gebruiker en zijn database, en daarin wat een app maakt.
	const password = "Hn5pR8wK2mT9xV4cB7qL"
	if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: db, User: user, Password: password, Grant: true,
	}}); res.Error != "" {
		t.Fatal(res.Error)
	}
	app := append(slices.Clone(base), "PGHOST="+u.Hostname(), "PGPORT="+port, "PGUSER="+user, "PGPASSWORD="+password)
	if out, err := psqlAs(app, db,
		"CREATE TABLE files (id serial PRIMARY KEY, name text, data bytea)",
		"CREATE TABLE log (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, file int REFERENCES files, at timestamptz)",
		"CREATE VIEW names AS SELECT name FROM files",
		`INSERT INTO files (name, data) VALUES ('één.txt', '\x00ff0a0d27'), (E'tab\there', NULL), ('quote''s', '\x5c')`,
		"INSERT INTO log (file, at) SELECT id, '2026-10-12 02:00:00+00' FROM files",
	); err != nil {
		t.Fatalf("als %s met het wachtwoord: %v: %s", user, err, out)
	}
	// Een extensie maakt de beheerder bij het terugzetten zelf; daarna
	// hoort ze bij de eigenaar van de database.
	extension := true
	if out, err := psqlAs(app, db, "CREATE EXTENSION IF NOT EXISTS pgcrypto"); err != nil {
		t.Logf("geen pgcrypto: %s", out)
		extension = false
	}
	checksum := func(d string) string {
		return sql(d, "SELECT string_agg(id || ':' || name || ':' || coalesce(encode(data, 'hex'), '-'), '|' ORDER BY id) FROM files",
			"SELECT count(*) FROM log", "SELECT count(*) FROM names")
	}
	want := checksum(db)

	res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBUsers})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	i := slices.IndexFunc(res.Users, func(x protocol.DBUser) bool { return x.Name == user })
	if i < 0 || len(res.Users[i].Global) != 0 || len(res.Users[i].Databases) != 1 || res.Users[i].Databases[0].Database != db ||
		!slices.Equal(res.Users[i].Databases[0].Privileges, []string{"OWNER"}) {
		t.Fatalf("gebruikers: %+v", res.Users)
	}

	file := pgDumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: db}).Started
	l := listDumps(t, a)
	if len(l.Dumps) != 1 {
		t.Fatalf("dumps: %+v", l.Dumps)
	}
	if d := l.Dumps[0]; d.Error != "" || !d.Complete || d.Tables != 2 || d.TablesExpected != 2 || d.SizeBytes == 0 || d.Engine != protocol.DBPostgreSQL {
		t.Fatalf("dump: %+v", d)
	}

	const tmpPassword = "Zt3kW9nP5rX2mQ8vC4bH"
	if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		User: tmp, Password: tmpPassword, Restore: true,
	}}); res.Error != "" {
		t.Fatal(res.Error)
	}
	if got := sql("postgres", "SELECT rolsuper AND rolvaliduntil BETWEEN now() + interval '11 hours' AND now() + interval '13 hours' FROM pg_roles WHERE rolname = '"+tmp+"'"); got != "t" {
		t.Fatalf("tijdelijke beheerder: %q", got)
	}
	restore := protocol.DBDumpRequest{
		Action: protocol.DBDumpRestore, Database: db, File: file, Target: target, Primary: "127.0.0.1", User: tmp, Password: tmpPassword,
	}
	wrong := restore
	wrong.Password = strings.Repeat("x", 20)
	if e := pgDumpErr(t, a, wrong); !strings.Contains(e, "password authentication failed") {
		t.Errorf("verkeerd wachtwoord: %s", e)
	}
	pgDumpReq(t, a, restore)
	if l := listDumps(t, a); len(l.Restores) != 1 || l.Restores[0].Error != "" {
		t.Fatalf("teruggezet: %+v", l.Restores)
	}
	if got := checksum(target); got != want {
		t.Fatalf("teruggezet: %s, wil %s", got, want)
	}
	// Alles in de nieuwe database is van de gebruiker, niets van de
	// tijdelijke beheerder, die daarom weg kan.
	if got := sql("postgres", "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = '"+target+"'"); got != user {
		t.Errorf("eigenaar van %s: %s", target, got)
	}
	if got := sql(target, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND pg_get_userbyid(c.relowner) <> '"+user+"'"); got != "0" {
		t.Errorf("objecten van een ander: %s", got)
	}
	if extension {
		if got := sql(target, "SELECT pg_get_userbyid(extowner) FROM pg_extension WHERE extname = 'pgcrypto'"); got != user {
			t.Errorf("eigenaar van pgcrypto: %s", got)
		}
	}
	if e := pgDumpErr(t, a, restore); !strings.Contains(e, "bestaat al") {
		t.Errorf("bestaat al: %s", e)
	}
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Errorf("in TMPDIR: %v", left)
	}
	if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{User: tmp}}); res.Error != "" {
		t.Fatalf("tijdelijke beheerder weghalen: %s", res.Error)
	}

	// Weghalen: eerst de databases, dan de gebruiker.
	for _, d := range []protocol.DBDropRequest{{Database: target, Confirm: target}, {Database: db, Confirm: db}, {User: user, Confirm: user}} {
		if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &d}); res.Error != "" {
			t.Fatalf("weghalen %+v: %s", d, res.Error)
		}
	}
	if got := sql("postgres", "SELECT count(*) FROM pg_roles WHERE rolname IN ('"+user+"', '"+tmp+"')"); got != "0" {
		t.Errorf("gebruikers over: %s", got)
	}
}
