package agent

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestPGMigrateRealPostgres verhuist een database binnen de echte
// PostgreSQL van CF_TEST_DATABASE_URL: de oude en de nieuwe server zijn
// dezelfde, met een andere naam voor de nieuwe database. pg_dump leest als
// de tijdelijke gebruiker over TCP, met het wachtwoord uit de passfile, en
// de import loopt als de beheerder.
func TestPGMigrateRealPostgres(t *testing.T) {
	r := newRealPG(t)
	a, sql := r.a, r.sql
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	db := "cf_t_" + hex.EncodeToString(suffix[:4])
	owner, ro, grp, member := db+"_app", db+"_ro", db+"_grp", db+"_lid"
	trial, moved, tmp := db+"_proef", db+"_nieuw", "cf_verhuizing_"+hex.EncodeToString(suffix)
	cleanup := func() {
		for _, q := range []string{
			`DROP DATABASE IF EXISTS "` + db + `" WITH (FORCE)`, `DROP DATABASE IF EXISTS "` + trial + `" WITH (FORCE)`,
			`DROP DATABASE IF EXISTS "` + moved + `" WITH (FORCE)`,
			`DROP ROLE IF EXISTS "` + tmp + `"`, `DROP ROLE IF EXISTS "` + ro + `"`, `DROP ROLE IF EXISTS "` + member + `"`, `DROP ROLE IF EXISTS "` + owner + `"`, `DROP ROLE IF EXISTS "` + grp + `"`,
		} {
			_, _ = r.psqlAs(r.admin, "postgres", q)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	const password, roPassword, tmpPassword = "Hn5pR8wK2mT9xV4cB7qL", "Lq2wE8rT5yU1iO9pA3sD", "Vb7nM3kJ9hG5fD1sA8qW"
	sql("postgres",
		`SET password_encryption = 'scram-sha-256'`,
		`CREATE ROLE "`+grp+`" NOLOGIN`,
		`CREATE ROLE "`+owner+`" LOGIN PASSWORD '`+password+`' IN ROLE "`+grp+`"`,
		`CREATE ROLE "`+ro+`" LOGIN PASSWORD '`+roPassword+`'`,
		`CREATE ROLE "`+member+`" LOGIN PASSWORD '`+roPassword+`' IN ROLE "`+grp+`"`,
		`CREATE DATABASE "`+db+`" OWNER "`+owner+`"`,
		`GRANT CONNECT, TEMPORARY ON DATABASE "`+db+`" TO "`+ro+`"`,
	)
	env := func(user, pw string) []string {
		return append(slices.Clone(r.base), "PGHOST="+r.host, "PGPORT="+r.port, "PGUSER="+user, "PGPASSWORD="+pw)
	}
	app := env(owner, password)
	if out, err := r.psqlAs(app, db,
		"CREATE TABLE files (id serial PRIMARY KEY, name text, data bytea)",
		"CREATE TABLE log (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, file int REFERENCES files, at timestamptz)",
		`CREATE SCHEMA "Data"`,
		`CREATE TABLE "Data"."Mixed ""Case""" (a int)`,
		"CREATE VIEW names AS SELECT name FROM files",
		"CREATE FUNCTION stamp() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN NEW.at := coalesce(NEW.at, now()); RETURN NEW; END$$",
		"CREATE TRIGGER log_at BEFORE INSERT ON log FOR EACH ROW EXECUTE FUNCTION stamp()",
		`INSERT INTO files (name, data) VALUES ('één.txt', '\x00ff0a0d27'), (E'tab\there', NULL), ('quote''s', '\x5c')`,
		"INSERT INTO log (file, at) SELECT id, '2026-10-12 02:00:00+00' FROM files",
		`INSERT INTO "Data"."Mixed ""Case""" SELECT generate_series(1, 25)`,
		`GRANT SELECT ON files TO "`+ro+`"`,
		`GRANT SELECT ON log TO "`+grp+`"`,
	); err != nil {
		t.Fatalf("als %s: %v: %s", owner, err, out)
	}
	extension := true
	if out, err := r.psqlAs(app, db, "CREATE EXTENSION IF NOT EXISTS pgcrypto"); err != nil {
		t.Logf("geen pgcrypto: %s", out)
		extension = false
	}
	checksum := func(d string) string {
		return sql(d, "SELECT string_agg(id || ':' || name || ':' || coalesce(encode(data, 'hex'), '-'), '|' ORDER BY id) FROM files",
			"SELECT count(*) FROM log", `SELECT sum(a) FROM "Data"."Mixed ""Case"""`, "SELECT count(*) FROM names")
	}
	want := checksum(db)
	req := func(action string) protocol.DBMigrateRequest {
		return protocol.DBMigrateRequest{Action: action, Database: db, User: tmp, Password: tmpPassword, Host: r.host, Port: protocol.DBPGPort}
	}

	info := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: db}).Info
	names := []string{}
	for _, tb := range info.Tables {
		names = append(names, tb.Name)
	}
	if !info.Exists || info.Owner != owner || info.OwnerSuper || info.Charset != "UTF8" || info.Views != 1 || info.Routines != 1 || info.Triggers != 1 ||
		len(info.Unreadable) != 0 || !slices.Equal(names, []string{`Data.Mixed "Case"`, "files", "log"}) || info.SizeBytes == 0 {
		t.Fatalf("inspect: %+v", info)
	}
	if extension && !slices.Equal(info.Extensions, []string{"pgcrypto"}) {
		t.Errorf("extensies: %v", info.Extensions)
	}
	if p := pgMigrate(t, a, req(protocol.DBMigrateProbe)).Probe; !p.Reached || p.Code != 0 || !slices.Contains(p.Extensions, "plpgsql") {
		t.Fatalf("probe: %+v", p)
	}

	pgMigrate(t, a, req(protocol.DBMigrateGrant))
	if got := sql("postgres", "SELECT rolcanlogin AND NOT rolsuper AND rolvaliduntil BETWEEN now() + interval '11 hours' AND now() + interval '13 hours' "+
		"AND pg_has_role(oid, '"+owner+"', 'USAGE') FROM pg_roles WHERE rolname = '"+tmp+"'"); got != "t" {
		t.Fatalf("tijdelijke gebruiker: %q", got)
	}
	if out, err := r.psqlAs(env(tmp, tmpPassword), db, "INSERT INTO files (name) VALUES ('mag niet')"); err == nil || !strings.Contains(out, "read-only transaction") {
		t.Errorf("de tijdelijke gebruiker schrijft: %v: %s", err, out)
	}

	// De proef: zonder eigenaars en rechten.
	pull := req(protocol.DBMigratePull)
	pull.Target, pull.Charset, pull.Total, pull.Trial = trial, info.Charset, info.SizeBytes, true
	pgMigrate(t, a, pull)
	run := pgWaitPull(t, a, trial)
	if run.Error != "" || len(run.Tables) != 3 || run.Views != 1 || run.Routines != 1 || run.Triggers != 1 || run.Bytes == 0 {
		t.Fatalf("proef: %+v", run)
	}
	for _, c := range run.Tables {
		if c.Source != c.Target || c.Source == 0 {
			t.Errorf("tabel %+v", c)
		}
	}
	if got := checksum(trial); got != want {
		t.Fatalf("proef: %s, wil %s", got, want)
	}
	if got := sql(trial, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND pg_get_userbyid(c.relowner) = '"+owner+"'"); got != "0" {
		t.Errorf("in de proef is iets van %s: %s", owner, got)
	}

	// De gebruikers, met hun verifier.
	accounts := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateAccounts, Database: db, Secret: true}).Accounts
	byName := map[string]protocol.DBMigrateAccount{}
	for _, acc := range accounts {
		byName[acc.User] = acc
	}
	if o := byName[owner]; o.Plugin != dbsql.PGLoginSCRAM || !strings.HasPrefix(o.Hash, "SCRAM-SHA-256$") || !slices.Contains(o.Privileges, "OWNER") ||
		!slices.Equal(o.MemberOf, []string{grp}) {
		t.Errorf("eigenaar: %+v", o)
	}
	if x := byName[ro]; x.Plugin != dbsql.PGLoginSCRAM || !slices.Equal(x.Privileges, []string{"CONNECT", "TEMPORARY", "OBJECTS"}) {
		t.Errorf("%s: %+v", ro, x)
	}
	if m := byName[member]; m.Plugin != dbsql.PGLoginSCRAM || len(m.Privileges) != 0 || !slices.Equal(m.MemberOf, []string{grp}) {
		t.Errorf("%s: %+v", member, m)
	}
	if g := byName[grp]; g.Plugin != dbsql.PGLoginNone || !slices.Equal(g.Privileges, []string{"OBJECTS"}) {
		t.Errorf("%s: %+v", grp, g)
	}
	if _, ok := byName[tmp]; ok {
		t.Errorf("de tijdelijke gebruiker staat bij de gebruikers: %+v", accounts)
	}
	var adopt []protocol.DBMigrateAccount
	for _, acc := range accounts {
		if dbsql.CheckPGAccount(acc) == nil {
			adopt = append(adopt, acc)
		}
	}
	// Dezelfde server: de rollen zijn er al en houden hun wachtwoord.
	if created := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateAdopt, Database: db, Accounts: adopt}).Accounts; len(created) != 0 {
		t.Errorf("nieuw gemaakt: %+v", created)
	}
	if out, err := r.psqlAs(env(ro, roPassword), db, "SELECT count(*) FROM files"); err != nil || out != "3" {
		t.Fatalf("%s na adopt: %v: %s", ro, err, out)
	}

	// De echte verhuizing, met eigenaars en rechten.
	pull.Target, pull.Trial, pull.Owner = moved, false, owner
	for _, acc := range adopt {
		acc.Hash = ""
		pull.Accounts = append(pull.Accounts, acc)
	}
	pgMigrate(t, a, pull)
	if run := pgWaitPull(t, a, moved); run.Error != "" || len(run.Tables) != 3 {
		t.Fatalf("verhuizing: %+v", run)
	}
	if got := checksum(moved); got != want {
		t.Fatalf("verhuisd: %s, wil %s", got, want)
	}
	if got := sql("postgres", "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = '"+moved+"'"); got != owner {
		t.Errorf("eigenaar van %s: %s", moved, got)
	}
	if out, err := r.psqlAs(env(ro, roPassword), moved, "SELECT count(*) FROM files"); err != nil || out != "3" {
		t.Errorf("%s leest de nieuwe database niet: %v: %s", ro, err, out)
	}
	if out, err := r.psqlAs(env(member, roPassword), moved, "SELECT count(*) FROM log"); err != nil || out != "3" {
		t.Errorf("%s leest de nieuwe database niet via %s: %v: %s", member, grp, err, out)
	}
	if got := sql("postgres", "SELECT datacl::text FROM pg_database WHERE datname = '"+moved+"'"); !strings.Contains(got, ro+"=Tc/") {
		t.Errorf("de rechten op %s: %s", moved, got)
	}
	if out, err := r.psqlAs(app, moved, "INSERT INTO log (file) VALUES (1)", "SELECT count(*) FROM log WHERE at IS NOT NULL"); err != nil || out != "4" {
		t.Errorf("de app in de nieuwe database: %v: %s", err, out)
	}

	// Het slot op de oude database, voor nieuwe verbindingen.
	pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateLock, Database: db})
	if out, err := r.psqlAs(app, db, "INSERT INTO files (name) VALUES ('na het slot')"); err == nil || !strings.Contains(out, "read-only transaction") {
		t.Errorf("schrijven na het slot: %v: %s", err, out)
	}
	pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateUnlock, Database: db})
	if out, err := r.psqlAs(app, db, "INSERT INTO files (name) VALUES ('weer open')"); err != nil {
		t.Errorf("schrijven zonder slot: %v: %s", err, out)
	}

	pgMigrate(t, a, req(protocol.DBMigrateRevoke))
	if got := sql("postgres", "SELECT count(*) FROM pg_roles WHERE rolname = '"+tmp+"'"); got != "0" {
		t.Errorf("de tijdelijke gebruiker staat er nog: %s", got)
	}
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Errorf("in TMPDIR: %v", left)
	}
}
