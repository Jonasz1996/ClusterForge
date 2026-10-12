package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// pgDumpReq en pgDumpErr zijn dumpReq en dumpErr voor PostgreSQL.
func pgDumpReq(t *testing.T, a *Agent, req protocol.DBDumpRequest) protocol.DBDumpResult {
	t.Helper()
	res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBDump, Dump: &req})
	if res.Error != "" {
		t.Fatalf("%s: %s", req.Action, res.Error)
	}
	if err := a.waitDump(t.Context()); err != nil {
		t.Fatal(err)
	}
	return *res.Dump
}

func pgDumpErr(t *testing.T, a *Agent, req protocol.DBDumpRequest) string {
	t.Helper()
	res := pgRequest(t, a, protocol.DBRequest{Op: protocol.DBDump, Dump: &req})
	if res.Error == "" {
		t.Fatalf("%s %s: geen fout", req.Action, req.Database)
	}
	return res.Error
}

// pgDumpHosts zijn nc1 (de primary) en nc2 (een replica) op één netwerk,
// met in PostgreSQL de database nextcloud van de gebruiker nextcloud, en
// in MariaDB ook een database nextcloud.
func pgDumpHosts(t *testing.T) (nc1, nc2 *agenttest.Host, now *time.Time) {
	t.Helper()
	// De agent zet zijn passfile in TMPDIR; de test kijkt dat hij weg is.
	t.Setenv("TMPDIR", t.TempDir())
	nc1, nc2 = pgHost(t, "10.0.20.186", false), pgHost(t, "10.0.20.187", true)
	hosts := map[string]*agenttest.Host{"10.0.20.186:5432": nc1, "10.0.20.187:5432": nc2}
	for _, h := range []*agenttest.Host{nc1, nc2} {
		h.Network = func(addr string) *agenttest.Host { return hosts[addr] }
		h.Postgres.Roles = append(h.Postgres.Roles, agenttest.PGRole{Name: "nextcloud"})
		h.Postgres.Databases = []agenttest.Database{{Name: "nextcloud", Owner: "nextcloud", Size: 3 << 20, Tables: 4, Rows: 9, Views: 1}}
	}
	t0 := time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
	return nc1, nc2, &t0
}

func clockAgent(h *agenttest.Host, now *time.Time) *Agent {
	a := pgAgent(h)
	a.Now = func() time.Time { return *now }
	return a
}

func TestPGDump(t *testing.T) {
	_, nc2, now := pgDumpHosts(t)
	a := clockAgent(nc2, now)
	r := pgDumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"})
	if r.Started != "nextcloud-20261012-020000.pgsql.zst" {
		t.Fatalf("begonnen: %q", r.Started)
	}
	if !slices.Contains(nc2.Calls(), "runuser -u postgres -- pg_dump --lock-wait-timeout=5min nextcloud") {
		t.Errorf("pg_dump: %v", nc2.Calls())
	}
	// Een dump van MariaDB in dezelfde seconde staat ernaast.
	m := dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"})
	if m.Started != "nextcloud-20261012-020000.sql.zst" {
		t.Fatalf("MariaDB: %q", m.Started)
	}
	l := listDumps(t, a)
	if len(l.Dumps) != 2 {
		t.Fatalf("dumps: %+v", l.Dumps)
	}
	for _, d := range l.Dumps {
		want := map[string]int{protocol.DBMariaDB: 5, protocol.DBPostgreSQL: 4}[d.Engine]
		if d.Engine != protocol.DBDumpEngine(d.File) || d.Error != "" || !d.Complete || d.Tables != want || d.TablesExpected != want || d.SizeBytes == 0 {
			t.Errorf("dump: %+v", d)
		}
	}
	fi, err := os.Stat(filepath.Join(a.DumpDir, "nextcloud", r.Started))
	if err != nil || fi.Mode() != 0o600 {
		t.Fatalf("bestand: %v %v", fi, err)
	}

	for db, want := range map[string]string{"postgres": "van PostgreSQL zelf", "template1": "van PostgreSQL zelf", "wiki": "bestaat niet"} {
		if e := pgDumpErr(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: db}); !strings.Contains(e, want) {
			t.Errorf("%s: %s", db, e)
		}
	}

	// Zonder slotregel is een dump niet compleet; een mislukte laat geen
	// bestand achter.
	nc2.Locked(func() { nc2.Postgres.DumpIncomplete = true })
	*now = now.Add(time.Minute)
	r = pgDumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"})
	nc2.Fail("runuser -u postgres -- pg_dump", "pg_dump: error: query failed: ERROR:  canceling statement due to lock timeout")
	*now = now.Add(time.Minute)
	f := pgDumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"})
	l = listDumps(t, a)
	for _, d := range l.Dumps {
		switch d.File {
		case r.Started:
			if d.Complete || d.Error != "" {
				t.Errorf("niet compleet: %+v", d)
			}
		case f.Started:
			if !strings.Contains(d.Error, "lock timeout") {
				t.Errorf("mislukt: %+v", d)
			}
			if _, err := os.Stat(filepath.Join(a.DumpDir, "nextcloud", d.File)); !os.IsNotExist(err) {
				t.Errorf("mislukte dump staat er: %v", err)
			}
		}
	}
}

func TestPGDumpPrune(t *testing.T) {
	_, nc2, now := pgDumpHosts(t)
	a := clockAgent(nc2, now)
	start := *now
	var pg []string
	for day := range 5 {
		*now = start.AddDate(0, 0, day)
		pg = append(pg, pgDumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}).Started)
	}
	*now = start
	maria := dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}).Started

	// Opruimen voor PostgreSQL laat de dumps van MariaDB staan, en de
	// nieuwste complete van PostgreSQL.
	*now = start.AddDate(0, 1, 0)
	r := pgDumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpPrune, KeepDays: 7})
	if !slices.Equal(r.Removed, pg[:4]) {
		t.Fatalf("weg: %v, wil %v", r.Removed, pg[:4])
	}
	var left []string
	for _, f := range r.Dumps {
		left = append(left, f.File)
	}
	if want := []string{maria, pg[4]}; !slices.Equal(left, want) {
		t.Fatalf("over: %v, wil %v", left, want)
	}
	// En omgekeerd.
	r = dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpPrune, KeepDays: 7, Keep: []string{}})
	if !slices.Equal(r.Removed, []string{maria}) || len(r.Dumps) != 1 || r.Dumps[0].File != pg[4] {
		t.Fatalf("MariaDB opgeruimd: %+v", r)
	}
}

func TestPGDumpRestore(t *testing.T) {
	nc1, nc2, now := pgDumpHosts(t)
	primary, replica := clockAgent(nc1, now), clockAgent(nc2, now)
	file := pgDumpReq(t, replica, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}).Started
	maria := dumpReq(t, replica, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}).Started

	// De server maakt een tijdelijke beheerder op de primary.
	const tmp, password = "cf_herstel_0123456789abcdef", "Kq4mZ8rT2vLp9Xw3Nb7s"
	if res := pgRequest(t, primary, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		User: tmp, Password: password, Restore: true,
	}}); res.Error != "" {
		t.Fatal(res.Error)
	}
	restore := func(target string) protocol.DBDumpRequest {
		return protocol.DBDumpRequest{
			Action: protocol.DBDumpRestore, Database: "nextcloud", File: file, Target: target,
			Primary: "10.0.20.186", User: tmp, Password: password,
		}
	}
	pgDumpReq(t, replica, restore("nextcloud_herstel_20261012"))
	l := listDumps(t, replica)
	if len(l.Restores) != 1 || l.Restores[0].Error != "" || l.Restores[0].Bytes != l.Restores[0].Total {
		t.Fatalf("teruggezet: %+v", l.Restores)
	}
	var got agenttest.Database
	nc1.Locked(func() {
		got = nc1.Postgres.Databases[slices.IndexFunc(nc1.Postgres.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud_herstel_20261012" })]
	})
	if got.Tables != 4 || got.Rows != 9 || got.Views != 1 || got.Owner != "nextcloud" {
		t.Fatalf("op de primary: %+v", got)
	}
	// Het wachtwoord stond alleen in de passfile, en die is weg.
	for _, c := range nc2.Calls() {
		if strings.Contains(c, password) {
			t.Errorf("wachtwoord in %q", c)
		}
	}
	if !slices.ContainsFunc(nc2.Calls(), func(c string) bool {
		return strings.HasPrefix(c, "psql -X -q -v ON_ERROR_STOP=1 -d host=10.0.20.186 port=5432 dbname=nextcloud_herstel_20261012 user="+tmp+" passfile=")
	}) {
		t.Errorf("aanroepen: %q", nc2.Calls())
	}
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Errorf("in TMPDIR: %v", left)
	}

	// Een dump op de primary zelf gaat ook over het netwerk.
	self := pgDumpReq(t, primary, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}).Started
	req := restore("nextcloud_zelf")
	req.File = self
	pgDumpReq(t, primary, req)
	if l := listDumps(t, primary); len(l.Restores) != 1 || l.Restores[0].Error != "" {
		t.Fatalf("op de primary zelf: %+v", l.Restores)
	}

	for name, c := range map[string]struct {
		req  func(*protocol.DBDumpRequest)
		want string
	}{
		"bestaat al":   {func(r *protocol.DBDumpRequest) { r.Target = "nextcloud" }, "bestaat al"},
		"systeem":      {func(r *protocol.DBDumpRequest) { r.Target = "template1" }, "van PostgreSQL zelf"},
		"MariaDB-dump": {func(r *protocol.DBDumpRequest) { r.File = maria }, "geen dump van"},
		"hostnaam":     {func(r *protocol.DBDumpRequest) { r.Primary = "nc1" }, "geen IP-adres"},
		"beheerder":    {func(r *protocol.DBDumpRequest) { r.User = "postgres" }, "geen tijdelijke beheerder"},
		"wachtwoord":   {func(r *protocol.DBDumpRequest) { r.Password = strings.Repeat("x", 20) }, "password authentication failed"},
		"replica":      {func(r *protocol.DBDumpRequest) { r.Primary = "10.0.20.187" }, "password authentication failed"},
	} {
		req := restore("nextcloud_x")
		c.req(&req)
		if e := pgDumpErr(t, replica, req); !strings.Contains(e, c.want) {
			t.Errorf("%s: %s", name, e)
		}
	}

	// Mislukt het terugzetten, dan is de nieuwe database op de primary weer
	// weg, en de passfile ook.
	replica.Log.Info("terugzetten dat mislukt")
	nc2.Fail("psql -X -q -v ON_ERROR_STOP=1 -d host=10.0.20.186 port=5432 dbname=nextcloud_kapot", `psql:<stdin>:57: ERROR:  role "rapport" does not exist`)
	pgDumpReq(t, replica, restore("nextcloud_kapot"))
	l = listDumps(t, replica)
	if len(l.Restores) != 2 || !strings.Contains(l.Restores[1].Error, `role "rapport" does not exist`) {
		t.Fatalf("mislukt: %+v", l.Restores)
	}
	nc1.Locked(func() {
		if slices.ContainsFunc(nc1.Postgres.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud_kapot" }) {
			t.Error("nextcloud_kapot staat er nog")
		}
	})
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Errorf("in TMPDIR: %v", left)
	}

	// Een verlopen beheerder komt niet meer binnen.
	nc1.Locked(func() {
		i := slices.IndexFunc(nc1.Postgres.Roles, func(r agenttest.PGRole) bool { return r.Name == tmp })
		nc1.Postgres.Roles[i].ValidUntil = time.Now().Add(-time.Second)
	})
	if e := pgDumpErr(t, replica, restore("nextcloud_laat")); !strings.Contains(e, "password authentication failed") {
		t.Errorf("verlopen: %s", e)
	}
}
