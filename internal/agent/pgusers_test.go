package agent

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// pgHost is nc1 van Nextcloud met MariaDB Galera en PostgreSQL met Patroni
// naast elkaar; recovery maakt PostgreSQL een replica.
func pgHost(t *testing.T, address string, recovery bool) *agenttest.Host {
	t.Helper()
	h := agenttest.NewHost(t.TempDir(), address)
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	h.SetUnit("patroni", agenttest.Unit{Enabled: true, Active: true})
	h.MariaDB = syncedMaria()
	h.MariaDB.Databases = []agenttest.Database{{Name: "nextcloud", Size: 1 << 20, Tables: 5}}
	h.Postgres = &agenttest.Postgres{Version: "17.6 (Debian 17.6-1.pgdg13+1)", Recovery: recovery, MaxConnections: 100, Roles: agenttest.PatroniRoles()}
	return h
}

func pgAgent(h *agenttest.Host) *Agent {
	return &Agent{
		Exec: h.Exec, ExecStdin: h.ExecStdin, Stream: h.Stream, DumpDir: filepath.Join(h.Root, "clusterforge-db"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func pgRequest(t *testing.T, a *Agent, req protocol.DBRequest) protocol.DBResponse {
	t.Helper()
	req.Engine = protocol.DBPostgreSQL
	return dbRequest(t, a, req)
}

func TestScramVerifier(t *testing.T) {
	v, err := scramVerifier(dbTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	r := agenttest.PGRole{Verifier: v}
	if !r.LogsIn(dbTestPassword, time.Now()) || r.LogsIn(dbTestPassword+"x", time.Now()) {
		t.Fatalf("verifier %s past niet bij het wachtwoord", v)
	}
	if w, _ := scramVerifier(dbTestPassword); w == v || !strings.HasPrefix(v, "SCRAM-SHA-256$4096:") {
		t.Errorf("verifiers %s en %s", v, w)
	}
}

func TestPGEnsureAndUsers(t *testing.T) {
	h := pgHost(t, "10.0.20.186", false)
	a := pgAgent(h)

	res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: "nextcloud", User: "nextcloud", Password: dbTestPassword, Grant: true,
	}})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	// Het wachtwoord ging nergens heen, ook niet via stdin: alleen de
	// verifier.
	for _, c := range append(h.Calls(), h.Stdins()...) {
		if strings.Contains(c, dbTestPassword) {
			t.Errorf("wachtwoord in %q", c)
		}
	}
	if !slices.Contains(h.Calls(), "runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -d postgres") {
		t.Errorf("aanroepen: %q", h.Calls())
	}
	var role agenttest.PGRole
	var owner string
	var mariaDBs []agenttest.Database
	h.Locked(func() {
		role = h.Postgres.Roles[slices.IndexFunc(h.Postgres.Roles, func(r agenttest.PGRole) bool { return r.Name == "nextcloud" })]
		owner = h.Postgres.Databases[0].Owner
		mariaDBs = slices.Clone(h.MariaDB.Databases)
		h.Postgres.Roles = append(h.Postgres.Roles, agenttest.PGRole{Name: "rapport"})
		h.Postgres.Databases[0].ACL = map[string][]string{"rapport": {"CONNECT"}}
	})
	if !role.LogsIn(dbTestPassword, time.Now()) || role.Super || owner != "nextcloud" {
		t.Fatalf("gebruiker %+v, eigenaar %q", role, owner)
	}
	// MariaDB op dezelfde node bleef zoals ze was.
	if len(mariaDBs) != 1 || mariaDBs[0].Tables != 5 || len(h.MariaDB.Users) != len(agenttest.DebianUsers()) {
		t.Errorf("MariaDB: %+v", mariaDBs)
	}

	// Opnieuw is niets nieuws, en een ander wachtwoord gaat erin.
	if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: "nextcloud", User: "nextcloud", Password: strings.Repeat("a", 20), Grant: true,
	}}); res.Error != "" {
		t.Fatal(res.Error)
	}

	res = pgRequest(t, a, protocol.DBRequest{Op: protocol.DBUsers})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	names := []string{}
	for _, u := range res.Users {
		names = append(names, u.Name)
	}
	if want := []string{"cf_replicatie", "cf_rewind", "nextcloud", "postgres", "rapport"}; !slices.Equal(names, want) {
		t.Fatalf("gebruikers: %v", names)
	}
	byName := func(n string) protocol.DBUser { return res.Users[slices.Index(names, n)] }
	if u := byName("nextcloud"); u.Host != "" || len(u.Global) != 0 || len(u.Databases) != 1 || u.Databases[0].Database != "nextcloud" ||
		!slices.Equal(u.Databases[0].Privileges, []string{"OWNER"}) {
		t.Errorf("nextcloud: %+v", u)
	}
	if u := byName("rapport"); len(u.Databases) != 1 || !slices.Equal(u.Databases[0].Privileges, []string{"CONNECT"}) {
		t.Errorf("rapport: %+v", u)
	}
	if u := byName("postgres"); !slices.Equal(u.Global, []string{"SUPERUSER", "CREATEDB", "CREATEROLE", "REPLICATION"}) {
		t.Errorf("postgres: %+v", u)
	}

	// Weghalen met bevestiging; wat van PostgreSQL of Patroni is niet.
	for name, c := range map[string]struct {
		drop protocol.DBDropRequest
		want string
	}{
		"postgres":      {protocol.DBDropRequest{Database: "postgres", Confirm: "postgres"}, "van PostgreSQL zelf"},
		"template1":     {protocol.DBDropRequest{Database: "template1", Confirm: "template1"}, "van PostgreSQL zelf"},
		"cf_replicatie": {protocol.DBDropRequest{User: "cf_replicatie", Confirm: "cf_replicatie"}, "PostgreSQL of Patroni zelf"},
		"pg_monitor":    {protocol.DBDropRequest{User: "pg_monitor", Confirm: "pg_monitor"}, "pg_ ervoor"},
		"bevestiging":   {protocol.DBDropRequest{Database: "nextcloud", Confirm: "next"}, "bevestiging"},
		"eigenaar":      {protocol.DBDropRequest{User: "nextcloud", Confirm: "nextcloud"}, "some objects depend on it"},
	} {
		if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &c.drop}); !strings.Contains(res.Error, c.want) {
			t.Errorf("%s: %q", name, res.Error)
		}
	}
	for _, d := range []protocol.DBDropRequest{{Database: "nextcloud", Confirm: "nextcloud"}, {User: "nextcloud", Confirm: "nextcloud"}} {
		if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &d}); res.Error != "" {
			t.Fatal(res.Error)
		}
	}
	h.Locked(func() {
		if len(h.Postgres.Databases) != 0 || slices.ContainsFunc(h.Postgres.Roles, func(r agenttest.PGRole) bool { return r.Name == "nextcloud" }) {
			t.Errorf("na weghalen: %+v", h.Postgres)
		}
	})

	// Een onbekende database, en een node zonder PostgreSQL.
	if res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBUsers, Engine: "oracle"}); !strings.Contains(res.Error, "onbekende database oracle") {
		t.Errorf("oracle: %q", res.Error)
	}
	maria := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	maria.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	maria.MariaDB = syncedMaria()
	if res := pgRequest(t, pgAgent(maria), protocol.DBRequest{Op: protocol.DBUsers}); !strings.Contains(res.Error, "geen PostgreSQL") {
		t.Errorf("zonder PostgreSQL: %q", res.Error)
	}
}

func TestPGEnsureReplicaAndTempRole(t *testing.T) {
	replica := pgHost(t, "10.0.20.187", true)
	if res := pgRequest(t, pgAgent(replica), protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: "nextcloud", User: "nextcloud", Password: dbTestPassword, Grant: true,
	}}); !strings.Contains(res.Error, "read-only transaction") {
		t.Errorf("op een replica: %q", res.Error)
	}

	h := pgHost(t, "10.0.20.186", false)
	a := pgAgent(h)
	expired := "cf_herstel_00000000000000aa"
	h.Postgres.Roles = append(h.Postgres.Roles, agenttest.PGRole{Name: expired, Super: true, ValidUntil: time.Now().Add(-time.Minute)})
	tmp := "cf_herstel_0123456789abcdef"
	for name, c := range map[string]struct {
		ensure protocol.DBEnsureRequest
		want   string
	}{
		"naam":     {protocol.DBEnsureRequest{User: "beheer", Password: dbTestPassword, Restore: true}, "tijdelijke beheerder"},
		"database": {protocol.DBEnsureRequest{User: tmp, Database: "x", Password: dbTestPassword, Restore: true}, "tijdelijke beheerder"},
		"gewoon":   {protocol.DBEnsureRequest{User: tmp, Password: dbTestPassword}, ""},
	} {
		res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &c.ensure})
		if c.want == "" {
			// Zonder Restore is een naam met cf_herstel_ een gewone
			// gebruiker; die kan weer weg met bevestiging.
			if res.Error != "" {
				t.Errorf("%s: %q", name, res.Error)
			}
			continue
		}
		if !strings.Contains(res.Error, c.want) {
			t.Errorf("%s: %q", name, res.Error)
		}
	}
	if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		User: tmp, Password: dbTestPassword, Restore: true,
	}}); res.Error != "" {
		t.Fatal(res.Error)
	}
	h.Locked(func() {
		i := slices.IndexFunc(h.Postgres.Roles, func(r agenttest.PGRole) bool { return r.Name == tmp })
		if i < 0 {
			t.Fatal("geen tijdelijke beheerder")
		}
		r := h.Postgres.Roles[i]
		if !r.Super || !r.LogsIn(dbTestPassword, time.Now()) || r.ValidUntil.Before(time.Now().Add(11*time.Hour)) || r.ValidUntil.After(time.Now().Add(13*time.Hour)) {
			t.Errorf("tijdelijke beheerder: %+v", r)
		}
		if slices.ContainsFunc(h.Postgres.Roles, func(r agenttest.PGRole) bool { return r.Name == expired }) {
			t.Error("de verlopen beheerder staat er nog")
		}
	})
	if in := h.Stdins(); !strings.HasPrefix(in[len(in)-1], dbsql.PGDropExpired) {
		t.Errorf("stdin: %q", in[len(in)-1])
	}
	// Een tijdelijke beheerder gaat weg zonder bevestiging.
	if res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{User: tmp}}); res.Error != "" {
		t.Fatal(res.Error)
	}
}
