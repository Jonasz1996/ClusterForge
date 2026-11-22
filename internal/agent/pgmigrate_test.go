package agent

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestPGMigrateSQL(t *testing.T) {
	v, err := scramVerifier(dbTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbsql.PGMigrateGrant("nextcloud", "cf_verhuizing_ab12", v)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SELECT format('DROP ROLE %I', rolname) FROM pg_roles WHERE rolname ~ '^cf_verhuizing_[a-z0-9]{1,40}$' AND rolvaliduntil < now()\\gexec\n",
		"SELECT 'CREATE ROLE \"cf_verhuizing_ab12\"' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cf_verhuizing_ab12')\\gexec\n",
		"NOBYPASSRLS INHERIT PASSWORD %L VALID UNTIL %L', '" + v + "', now() + interval '12 hours')\\gexec\n",
		"ALTER ROLE \"cf_verhuizing_ab12\" SET default_transaction_read_only = on;\n",
		"SELECT format('GRANT %I TO \"cf_verhuizing_ab12\"', r.rolname) FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba " +
			"WHERE d.datname = 'nextcloud' AND NOT r.rolsuper\\gexec\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("grant mist %q in\n%s", want, got)
		}
	}
	for _, bad := range []struct{ db, user, verifier string }{
		{"nextcloud", "nextcloud", v},
		{"nextcloud", "cf_verhuizing_x'", v},
		{"nextcloud", "cf_verhuizing_ab", "geheim"},
		{"postgres", "cf_verhuizing_ab", v},
		{"Next", "cf_verhuizing_ab", v},
	} {
		if _, err := dbsql.PGMigrateGrant(bad.db, bad.user, bad.verifier); err == nil {
			t.Errorf("geen fout voor %+v", bad)
		}
	}
	if q, _ := dbsql.PGMigrateRevoke("cf_verhuizing_ab12"); !strings.HasSuffix(q, "DROP ROLE IF EXISTS \"cf_verhuizing_ab12\";\n") {
		t.Errorf("revoke: %s", q)
	}
	if _, err := dbsql.PGMigrateRevoke("nextcloud"); err == nil {
		t.Error("revoke van een andere rol")
	}

	adopt := func(a protocol.DBMigrateAccount) string {
		t.Helper()
		q, err := dbsql.PGMigrateAdopt(a)
		if err != nil {
			return "fout: " + err.Error()
		}
		return q
	}
	create := "SELECT 'CREATE ROLE \"nextcloud\"' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nextcloud')\\gexec\n"
	for name, c := range map[string]struct {
		acc  protocol.DBMigrateAccount
		want string
	}{
		"scram":        {protocol.DBMigrateAccount{User: "nextcloud", Plugin: "scram-sha-256", Hash: v}, create + "ALTER ROLE \"nextcloud\" WITH LOGIN PASSWORD '" + v + "';\n"},
		"groep":        {protocol.DBMigrateAccount{User: "nextcloud", Plugin: "nologin"}, create},
		"geen wachtw.": {protocol.DBMigrateAccount{User: "nextcloud", Plugin: "none"}, create + "ALTER ROLE \"nextcloud\" WITH LOGIN;\n"},
		"md5":          {protocol.DBMigrateAccount{User: "nextcloud", Plugin: "md5", Hash: "md5abc"}, "fout: het wachtwoord van nextcloud staat als md5-hash"},
		"superuser":    {protocol.DBMigrateAccount{User: "nextcloud", Plugin: "nologin", Super: true}, "fout: nextcloud is superuser"},
		"postgres":     {protocol.DBMigrateAccount{User: "postgres", Plugin: "scram-sha-256", Hash: v}, "fout: postgres is een rol van PostgreSQL"},
		"hoofdletter":  {protocol.DBMigrateAccount{User: "NextCloud", Plugin: "nologin"}, "fout: \"NextCloud\": ClusterForge neemt alleen"},
		"tijdelijk":    {protocol.DBMigrateAccount{User: "cf_verhuizing_ab12", Plugin: "nologin"}, "fout: cf_verhuizing_ab12 is een tijdelijke"},
		"verifier":     {protocol.DBMigrateAccount{User: "nextcloud", Plugin: "scram-sha-256", Hash: "SCRAM-SHA-256$4096:x'"}, "fout: nextcloud heeft geen geldige"},
		"zonder":       {protocol.DBMigrateAccount{User: "nextcloud", Plugin: "scram-sha-256"}, "fout: nextcloud zonder verifier"},
		"onbekend":     {protocol.DBMigrateAccount{User: "nextcloud", Plugin: "other"}, "fout: nextcloud logt in op een manier"},
	} {
		if got := adopt(c.acc); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: %s", name, got)
		}
	}
	members := dbsql.PGMigrateMembers([]protocol.DBMigrateAccount{
		{User: "nextcloud", Plugin: "scram-sha-256", MemberOf: []string{"nc_grp", "pg_read_all_data", "nextcloud", "Raar"}},
		{User: "postgres", Plugin: "scram-sha-256", MemberOf: []string{"nc_grp"}},
	})
	if members != "SELECT format('GRANT %I TO \"nextcloud\"', g.rolname) FROM pg_roles g WHERE g.rolname = 'nc_grp' AND NOT pg_has_role('nextcloud', g.oid, 'MEMBER')\\gexec\n" {
		t.Errorf("lidmaatschappen: %s", members)
	}

	if q, _ := dbsql.PGMigrateCreate("nextcloud", "nextcloud", "UTF8"); q != "CREATE DATABASE \"nextcloud\" OWNER \"nextcloud\" ENCODING 'UTF8' TEMPLATE template0;\n" {
		t.Errorf("maken: %s", q)
	}
	if q, _ := dbsql.PGMigrateCreate("wiki_proef", "", "LATIN1"); q != "CREATE DATABASE \"wiki_proef\" ENCODING 'LATIN1' TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C';\n" {
		t.Errorf("maken latin1: %s", q)
	}
	for _, bad := range [][3]string{{"wiki", "", "UTF8'; DROP"}, {"wiki", "postgres", "UTF8"}, {"template1", "", "UTF8"}} {
		if _, err := dbsql.PGMigrateCreate(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("maken zonder fout: %v", bad)
		}
	}
	if q, _ := dbsql.PGMigrateLock("nextcloud", true); q != "ALTER DATABASE \"nextcloud\" SET default_transaction_read_only = on;\n" {
		t.Errorf("slot: %s", q)
	}
	if q, _ := dbsql.PGMigrateLock("nextcloud", false); q != "ALTER DATABASE \"nextcloud\" RESET default_transaction_read_only;\n" {
		t.Errorf("van slot: %s", q)
	}

	tables := make([]dbsql.PGTable, 0, 150)
	for i := range 150 {
		tables = append(tables, dbsql.PGTable{Schema: "public", Name: "t" + string(rune('a'+i%26))})
	}
	tables[3] = dbsql.PGTable{Schema: "s 2", Name: `we"ird`}
	qs, err := dbsql.PGCountRows(tables)
	if err != nil || len(qs) != 2 || strings.Count(qs[0], "count(*)") != 100 || strings.Count(qs[1], "count(*)") != 50 {
		t.Fatalf("tellen: %d %v", len(qs), err)
	}
	if !strings.Contains(qs[0], `(SELECT count(*) FROM "s 2"."we""ird")`) {
		t.Errorf("naam met aanhalingsteken: %s", qs[0][:200])
	}
	if tables[3].String() != `s 2.we"ird` || tables[0].String() != "ta" {
		t.Errorf("namen: %s %s", tables[3], tables[0])
	}
}

// pgMigrateHosts zijn db1 met de gedeelde PostgreSQL van Jonas (bereikbaar
// op zijn VIP .180) en nc1, de primary van het nieuwe cluster van
// Nextcloud.
func pgMigrateHosts(t *testing.T) (db1, nc1 *agenttest.Host, a, b *Agent) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	db1, nc1 = pgHost(t, "10.0.20.181", false), pgHost(t, "10.0.20.186", false)
	db1.Postgres.Version = "15.8 (Debian 15.8-0+deb12u1)"
	ncVerifier, _ := scramVerifier("Nextcloud0123456789ab")
	roVerifier, _ := scramVerifier("Alleenlezen012345678")
	db1.Postgres.Roles = append(db1.Postgres.Roles,
		agenttest.PGRole{Name: "nextcloud", Verifier: ncVerifier, MemberOf: []string{"nc_grp"}},
		agenttest.PGRole{Name: "nc_ro", Verifier: roVerifier},
		agenttest.PGRole{Name: "nc_grp", NoLogin: true},
		agenttest.PGRole{Name: "nc_lid", Verifier: roVerifier, MemberOf: []string{"nc_grp"}},
		agenttest.PGRole{Name: "wiki", Verifier: "md5" + strings.Repeat("a", 32)},
	)
	db1.Postgres.Databases = []agenttest.Database{
		{
			Name: "nextcloud", Owner: "nextcloud", Size: 3 << 20, Tables: 3, Rows: 42, Views: 1, Routines: 2, Triggers: 1, NoPK: []string{"t2"},
			Extensions: []string{"pgcrypto"}, ACL: map[string][]string{"nc_ro": {"CONNECT"}}, Grantees: []string{"nc_grp", "nc_ro"}, Settings: 1,
		},
		{Name: "beheer", Owner: "postgres", Size: 1 << 20, Tables: 1},
		{Name: "wiki", Owner: "wiki", Size: 1 << 20, Tables: 2, Unreadable: []string{"public.geheim"}},
	}
	hosts := map[string]*agenttest.Host{"10.0.20.180:5432": db1, "10.0.20.181:5432": db1, "10.0.20.186:5432": nc1}
	for _, h := range []*agenttest.Host{db1, nc1} {
		h.Network = func(addr string) *agenttest.Host { return hosts[addr] }
	}
	agent := func(h *agenttest.Host) *Agent {
		a := pgAgent(h)
		a.Dial = h.Dial
		return a
	}
	return db1, nc1, agent(db1), agent(nc1)
}

func pgMigrate(t *testing.T, a *Agent, req protocol.DBMigrateRequest) *protocol.DBMigrateResult {
	t.Helper()
	res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBMigrate, Migrate: &req})
	if res.Error != "" {
		t.Fatalf("%s: %s", req.Action, res.Error)
	}
	return res.Migrate
}

func pgMigrateErr(t *testing.T, a *Agent, req protocol.DBMigrateRequest) string {
	t.Helper()
	res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBMigrate, Migrate: &req})
	if res.Error == "" {
		t.Fatalf("%s %s: geen fout", req.Action, req.Database)
	}
	return res.Error
}

// pgWaitPull wacht tot de import van target klaar is.
func pgWaitPull(t *testing.T, a *Agent, target string) protocol.DBMigrateRun {
	t.Helper()
	for range 2000 {
		r := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateStatus, Target: target}).Run
		if r == nil {
			t.Fatalf("geen import van %s", target)
		}
		if !r.Finished.IsZero() {
			return *r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("de import van %s is niet klaar", target)
	return protocol.DBMigrateRun{}
}

func pgDatabase(h *agenttest.Host, name string) (d agenttest.Database, ok bool) {
	h.Locked(func() {
		if i := slices.IndexFunc(h.Postgres.Databases, func(x agenttest.Database) bool { return x.Name == name }); i >= 0 {
			d, ok = h.Postgres.Databases[i], true
		}
	})
	return d, ok
}

func pgRole(h *agenttest.Host, name string) (r agenttest.PGRole, ok bool) {
	h.Locked(func() {
		if i := slices.IndexFunc(h.Postgres.Roles, func(x agenttest.PGRole) bool { return x.Name == name }); i >= 0 {
			r, ok = h.Postgres.Roles[i], true
		}
	})
	return r, ok
}

func TestPGMigrate(t *testing.T) {
	db1, nc1, a, b := pgMigrateHosts(t)

	info := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: "nextcloud"}).Info
	if !info.Exists || info.Owner != "nextcloud" || info.OwnerSuper || info.Charset != "UTF8" || len(info.Tables) != 3 || info.Tables[1].PrimaryKey ||
		info.Views != 1 || info.Routines != 2 || info.Triggers != 1 || !slices.Equal(info.Extensions, []string{"pgcrypto"}) || info.Settings != 1 ||
		info.SizeBytes != 3<<20 || !strings.HasPrefix(info.Version, "15.8") {
		t.Fatalf("inspect: %+v", info)
	}
	if info := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: "fotos"}).Info; info.Exists {
		t.Errorf("fotos bestaat niet: %+v", info)
	}
	if info := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: "beheer"}).Info; !info.OwnerSuper {
		t.Errorf("beheer is van postgres: %+v", info)
	}

	// Bereikbaar, en pg_hba.conf laat nc1 binnen: de server vraagt een
	// wachtwoord.
	probe := protocol.DBMigrateRequest{Action: protocol.DBMigrateProbe, Host: "10.0.20.180", Port: 5432, Database: "nextcloud"}
	if p := pgMigrate(t, b, probe).Probe; !p.Reached || p.Code != 0 || p.TLS || !slices.Contains(p.Extensions, "pgcrypto") {
		t.Fatalf("probe: %+v", p)
	}
	db1.Locked(func() { db1.Postgres.HBADeny, db1.Postgres.TLS = []string{"10.0.20.186"}, true })
	if p := pgMigrate(t, b, probe).Probe; !p.Reached || !p.TLS || p.Code != protocol.DBMigrateNoEntry || p.Seen != "10.0.20.186" ||
		!strings.HasPrefix(p.Error, "FATAL: no pg_hba.conf entry for host") {
		t.Fatalf("probe met pg_hba: %+v", p)
	}
	db1.Locked(func() { db1.Postgres.HBADeny = nil })
	probe.Host = "10.0.20.99"
	if p := pgMigrate(t, b, probe).Probe; p.Reached || !strings.Contains(p.Error, "refused") {
		t.Fatalf("probe weg: %+v", p)
	}
	probe.Host = "10.0.20.180; rm"
	if e := pgMigrateErr(t, b, probe); !strings.Contains(e, "geen adres") {
		t.Errorf("adres: %s", e)
	}

	// De tijdelijke gebruiker: lid van de eigenaar, alleen lezen, 12 uur.
	const user, password = "cf_verhuizing_ab12cd34", "Verhuis0123456789abc"
	pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateGrant, Database: "nextcloud", User: user, Password: password})
	tmp, ok := pgRole(db1, user)
	if !ok || !slices.Equal(tmp.MemberOf, []string{"nextcloud"}) || !tmp.ReadOnly || tmp.ValidUntil.IsZero() || !tmp.LogsIn(password, tmp.ValidUntil.Add(-1)) {
		t.Fatalf("tijdelijke gebruiker: %+v", tmp)
	}
	for _, s := range append(db1.Calls(), db1.Stdins()...) {
		if strings.Contains(s, password) {
			t.Errorf("het wachtwoord staat in %q", s)
		}
	}
	for name, c := range map[string]struct {
		req  protocol.DBMigrateRequest
		want string
	}{
		"superuser":  {protocol.DBMigrateRequest{Database: "beheer", User: user, Password: password}, "is superuser"},
		"onleesbaar": {protocol.DBMigrateRequest{Database: "wiki", User: user, Password: password}, "mag niet alles lezen: public.geheim"},
		"weg":        {protocol.DBMigrateRequest{Database: "fotos", User: user, Password: password}, "bestaat niet"},
		"andere rol": {protocol.DBMigrateRequest{Database: "nextcloud", User: "nextcloud", Password: password}, "geen tijdelijke gebruiker"},
		"wachtwoord": {protocol.DBMigrateRequest{Database: "nextcloud", User: user, Password: "kort"}, "16 tot 128"},
		"systeem":    {protocol.DBMigrateRequest{Database: "postgres", User: user, Password: password}, "van PostgreSQL zelf"},
	} {
		c.req.Action = protocol.DBMigrateGrant
		if e := pgMigrateErr(t, a, c.req); !strings.Contains(e, c.want) {
			t.Errorf("grant %s: %s", name, e)
		}
	}

	// De proef: zonder eigenaars en rechten, alles van postgres.
	pull := protocol.DBMigrateRequest{
		Action: protocol.DBMigratePull, Database: "nextcloud", Target: "nextcloud_proef", User: user, Password: password,
		Host: "10.0.20.180", Port: 5432, Charset: "UTF8", Total: info.SizeBytes, Trial: true,
	}
	pgMigrate(t, b, pull)
	r := pgWaitPull(t, b, "nextcloud_proef")
	if r.Error != "" || len(r.Tables) != 3 || r.Views != 1 || r.Routines != 2 || r.Triggers != 1 || r.Imported.IsZero() || r.Bytes == 0 || !r.TLS {
		t.Fatalf("import: %+v", r)
	}
	for _, c := range r.Tables {
		if c.Source != 42 || c.Target != 42 || strings.Contains(c.Name, "public") {
			t.Errorf("tabel %+v", c)
		}
	}
	if d, ok := pgDatabase(nc1, "nextcloud_proef"); !ok || d.Owner != "" || d.Charset != "UTF8" || !slices.Equal(d.Extensions, []string{"pgcrypto"}) {
		t.Errorf("de proef op nc1: %+v", d)
	}
	for _, s := range append(nc1.Calls(), nc1.Stdins()...) {
		if strings.Contains(s, password) {
			t.Errorf("het wachtwoord staat in %q", s)
		}
	}
	if !slices.ContainsFunc(nc1.Calls(), func(c string) bool {
		return strings.HasPrefix(c, "pg_dump -d host=10.0.20.180 port=5432 dbname=nextcloud user="+user+" passfile=") &&
			strings.HasSuffix(c, " --no-password --lock-wait-timeout=5min --no-owner --no-privileges")
	}) {
		t.Errorf("aanroepen: %q", nc1.Calls())
	}
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Errorf("in TMPDIR: %v", left)
	}

	// Een verhuizing met eigenaar kan pas als de rollen er zijn.
	move := pull
	move.Target, move.Trial, move.Owner = "nextcloud", false, "nextcloud"
	if e := pgMigrateErr(t, b, move); !strings.Contains(e, `role "nextcloud" does not exist`) {
		t.Errorf("zonder rollen: %s", e)
	}
	accounts := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateAccounts, Database: "nextcloud", Secret: true}).Accounts
	if len(accounts) != 4 {
		t.Fatalf("gebruikers: %+v", accounts)
	}
	byName := map[string]protocol.DBMigrateAccount{}
	for _, acc := range accounts {
		byName[acc.User] = acc
	}
	if nc := byName["nextcloud"]; nc.Plugin != "scram-sha-256" || nc.Hash == "" || !slices.Equal(nc.Privileges, []string{"OWNER"}) ||
		!slices.Equal(nc.MemberOf, []string{"nc_grp"}) || nc.Super {
		t.Errorf("nextcloud: %+v", nc)
	}
	if ro := byName["nc_ro"]; ro.Plugin != "scram-sha-256" || !slices.Equal(ro.Privileges, []string{"CONNECT", "OBJECTS"}) {
		t.Errorf("nc_ro: %+v", ro)
	}
	if g := byName["nc_grp"]; g.Plugin != "nologin" || g.Hash != "" || !slices.Equal(g.Privileges, []string{"OBJECTS"}) {
		t.Errorf("nc_grp: %+v", g)
	}
	// Lid van nc_grp, en zo met rechten in de database.
	if l := byName["nc_lid"]; l.Plugin != "scram-sha-256" || len(l.Privileges) != 0 || !slices.Equal(l.MemberOf, []string{"nc_grp"}) {
		t.Errorf("nc_lid: %+v", l)
	}
	if plain := pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateAccounts, Database: "nextcloud"}).Accounts; plain[0].Hash != "" {
		t.Errorf("hash zonder secret: %+v", plain[0])
	}

	nc1.Locked(func() {
		nc1.Postgres.Roles = append(nc1.Postgres.Roles, agenttest.PGRole{Name: "nc_ro", NoLogin: true})
	})
	created := pgMigrate(t, b, protocol.DBMigrateRequest{Action: protocol.DBMigrateAdopt, Database: "nextcloud", Accounts: accounts}).Accounts
	var names []string
	for _, c := range created {
		names = append(names, c.User)
		if c.Hash != "" {
			t.Errorf("een verifier in het antwoord: %+v", c)
		}
	}
	if !slices.Equal(names, []string{"nc_grp", "nc_lid", "nextcloud"}) {
		t.Errorf("nieuw gemaakt: %v", names)
	}
	if nc, _ := pgRole(nc1, "nextcloud"); !nc.LogsIn("Nextcloud0123456789ab", nc.ValidUntil) || nc.NoLogin || !slices.Equal(nc.MemberOf, []string{"nc_grp"}) {
		t.Errorf("nextcloud op nc1: %+v", nc)
	}
	if g, _ := pgRole(nc1, "nc_grp"); !g.NoLogin {
		t.Errorf("nc_grp op nc1: %+v", g)
	}
	for _, s := range nc1.Stdins() {
		if strings.Contains(s, byName["nextcloud"].Hash) && !strings.HasPrefix(s, "SELECT 'CREATE ROLE \"nextcloud\"'") {
			t.Errorf("de verifier van nextcloud staat bij een andere opdracht: %q", s)
		}
	}
	for _, acc := range accounts {
		acc.Hash = ""
		move.Accounts = append(move.Accounts, acc)
	}
	pgMigrate(t, b, move)
	if r := pgWaitPull(t, b, "nextcloud"); r.Error != "" || len(r.Tables) != 3 {
		t.Fatalf("verhuizing: %+v", r)
	}
	// Met de rechten op de database zelf, die niet in de dump staan.
	if d, _ := pgDatabase(nc1, "nextcloud"); d.Owner != "nextcloud" || d.Tables != 3 || !slices.Equal(d.ACL["nc_ro"], []string{"CONNECT"}) || len(d.ACL) != 1 {
		t.Errorf("nextcloud op nc1: %+v", d)
	}
	if d, _ := pgDatabase(nc1, "nextcloud_proef"); len(d.ACL) != 0 {
		t.Errorf("rechten in de proef: %+v", d.ACL)
	}

	// Een extensie die er op de nieuwe node niet is: de import mislukt en de
	// halve database gaat weg.
	nc1.Locked(func() { nc1.Postgres.Available = []string{"plpgsql"} })
	pull.Target = "nextcloud_proef2"
	pgMigrate(t, b, pull)
	if r := pgWaitPull(t, b, pull.Target); !strings.Contains(r.Error, `extension "pgcrypto" is not available`) {
		t.Errorf("extensie: %+v", r)
	}
	if _, ok := pgDatabase(nc1, pull.Target); ok {
		t.Error("nextcloud_proef2 staat er nog")
	}
	nc1.Locked(func() { nc1.Postgres.Available = nil })

	// Een verkeerd wachtwoord: de fout van pg_dump, zonder het wachtwoord.
	pull.Target, pull.Password = "nextcloud_proef3", "Verkeerd0123456789xyz"
	pgMigrate(t, b, pull)
	if r := pgWaitPull(t, b, pull.Target); !strings.Contains(r.Error, `password authentication failed for user "`+user+`"`) ||
		strings.Contains(r.Error, pull.Password) {
		t.Errorf("verkeerd wachtwoord: %+v", r)
	}

	// Een dump zonder slotregel is niet compleet.
	db1.Locked(func() { db1.Postgres.DumpIncomplete = true })
	pull.Target, pull.Password = "nextcloud_proef4", password
	pgMigrate(t, b, pull)
	if r := pgWaitPull(t, b, pull.Target); !strings.Contains(r.Error, "niet compleet") {
		t.Errorf("niet compleet: %+v", r)
	}
	db1.Locked(func() { db1.Postgres.DumpIncomplete = false })

	for name, c := range map[string]struct {
		req  func(*protocol.DBMigrateRequest)
		want string
	}{
		"bestaat al":         {func(r *protocol.DBMigrateRequest) { r.Target = "nextcloud_proef" }, "bestaat al"},
		"proef met eigenaar": {func(r *protocol.DBMigrateRequest) { r.Owner = "nextcloud" }, "een proef heeft geen eigenaar"},
		"encoding":           {func(r *protocol.DBMigrateRequest) { r.Charset = "UTF8'" }, "geen encoding"},
		"onbereikbaar":       {func(r *protocol.DBMigrateRequest) { r.Host = "10.0.20.99" }, "refused"},
	} {
		req := pull
		req.Target = "nextcloud_x"
		c.req(&req)
		if e := pgMigrateErr(t, b, req); !strings.Contains(e, c.want) {
			t.Errorf("pull %s: %s", name, e)
		}
	}

	// Op een replica kan niets geschreven worden.
	nc1.Locked(func() { nc1.Postgres.Recovery = true })
	pull.Target = "nextcloud_x"
	if e := pgMigrateErr(t, b, pull); !strings.Contains(e, "read-only transaction") {
		t.Errorf("replica: %s", e)
	}
	nc1.Locked(func() { nc1.Postgres.Recovery = false })

	// Het slot op de oude database, en eraf.
	pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateLock, Database: "nextcloud"})
	if d, _ := pgDatabase(db1, "nextcloud"); !d.ReadOnly {
		t.Error("niet op slot")
	}
	pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateUnlock, Database: "nextcloud"})
	if d, _ := pgDatabase(db1, "nextcloud"); d.ReadOnly {
		t.Error("nog op slot")
	}

	// md5 en superusers gaan niet mee.
	if e := pgMigrateErr(t, b, protocol.DBMigrateRequest{Action: protocol.DBMigrateAdopt, Database: "wiki", Accounts: []protocol.DBMigrateAccount{
		{User: "wiki", Plugin: "md5", Hash: "md5" + strings.Repeat("a", 32)},
	}}); !strings.Contains(e, "md5") {
		t.Errorf("md5: %s", e)
	}

	pgMigrate(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateRevoke, User: user})
	if _, ok := pgRole(db1, user); ok {
		t.Error("de tijdelijke gebruiker staat er nog")
	}
	if e := pgMigrateErr(t, a, protocol.DBMigrateRequest{Action: protocol.DBMigrateRevoke, User: "nextcloud"}); !strings.Contains(e, "geen tijdelijke gebruiker") {
		t.Errorf("andere rol weghalen: %s", e)
	}
}
