package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const probeRun = "0f3c9a52-7d41-4e1b-9c3a-5b2e8f6d1a07"

func TestProbeSQL(t *testing.T) {
	for _, tc := range []struct{ engine, want string }{
		{protocol.DBMariaDB, "INSERT INTO `clusterforge_probe`.`writes` (run, seq) VALUES ('" + probeRun + "', 7)"},
		{protocol.DBPostgreSQL, "INSERT INTO writes (run, seq) VALUES ('" + probeRun + "', 7)"},
	} {
		got, err := dbsql.ProbeInsert(tc.engine, probeRun, 7)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q %v", tc.engine, got, err)
		}
	}
	for _, bad := range []string{"", "x", probeRun + "'", strings.ToUpper(probeRun), "0f3c9a52-7d41-4e1b-9c3a-5b2e8f6d1a0"} {
		if _, err := dbsql.ProbeInsert(protocol.DBMariaDB, bad, 1); err == nil {
			t.Errorf("run %q geaccepteerd", bad)
		}
		if _, err := dbsql.ProbeDelete(protocol.DBPostgreSQL, bad); err == nil {
			t.Errorf("run %q geaccepteerd bij delete", bad)
		}
	}
	if q := dbsql.ProbeCreate(protocol.DBMariaDB); !strings.Contains(q, "PRIMARY KEY (run, seq)") || !strings.Contains(q, "ENGINE=InnoDB") {
		t.Errorf("create: %s", q)
	}
}

// galeraProbe zijn drie Galera-nodes met één tabel voor de schrijftest, en
// het VIP .185 op de node in vip. De agent op nc2 schrijft.
type galeraProbe struct {
	hosts map[string]*agenttest.Host
	table *agenttest.ProbeTable
	mu    sync.Mutex
	vip   string
	a     *Agent
}

func newGaleraProbe(t *testing.T) *galeraProbe {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	g := &galeraProbe{hosts: map[string]*agenttest.Host{}, table: agenttest.NewProbeTable(), vip: "10.0.20.186"}
	for _, addr := range []string{"10.0.20.186", "10.0.20.187", "10.0.20.188"} {
		h := agenttest.NewHost(t.TempDir(), addr)
		h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
		h.MariaDB = syncedMaria()
		h.MariaDB.Probe = g.table
		for _, from := range []string{"10.0.20.185", "10.0.20.186", "10.0.20.187", "10.0.20.188"} {
			h.MariaDB.Users = append(h.MariaDB.Users, agenttest.MariaUser{User: protocol.DBProbeUser, Host: from, Password: dbTestPassword,
				Grants: map[string][]string{`clusterforge\_probe`: {"ALL PRIVILEGES"}}})
		}
		g.hosts[addr] = h
	}
	for _, h := range g.hosts {
		h.Network = func(addr string) *agenttest.Host {
			host, _, _ := strings.Cut(addr, ":")
			if host == "10.0.20.185" {
				g.mu.Lock()
				host = g.vip
				g.mu.Unlock()
			}
			return g.hosts[host]
		}
	}
	nc2 := g.hosts["10.0.20.187"]
	g.a = &Agent{Exec: nc2.Exec, ExecStdin: nc2.ExecStdin, Dial: nc2.Dial, ProbeInterval: 10 * time.Millisecond, Log: dbAgent(nc2).Log}
	return g
}

func (g *galeraProbe) moveVIP(addr string) {
	g.mu.Lock()
	g.vip = addr
	g.mu.Unlock()
}

func probeOp(t *testing.T, a *Agent, engine string, req protocol.DBProbeRequest) *protocol.DBProbeResult {
	t.Helper()
	res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBWriteProbe, Engine: engine, Probe: &req})
	if res.Error != "" {
		t.Fatalf("%s: %s", req.Action, res.Error)
	}
	return res.Probe
}

func probeErr(t *testing.T, a *Agent, engine string, req protocol.DBProbeRequest) string {
	t.Helper()
	res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBWriteProbe, Engine: engine, Probe: &req})
	if res.Error == "" {
		t.Fatalf("%s: geen fout", req.Action)
	}
	return res.Error
}

// waitAttempts wacht tot er na from een poging is die ok is (of niet).
func waitAttempts(t *testing.T, a *Agent, engine string, from int, ok bool) *protocol.DBProbeResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := probeOp(t, a, engine, protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: probeRun, From: from})
		if slices.ContainsFunc(r.Attempts, func(x protocol.DBProbeAttempt) bool { return x.OK == ok }) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("geen poging met ok=%v na %d", ok, from)
	return nil
}

func TestWriteProbeGalera(t *testing.T) {
	g := newGaleraProbe(t)
	start := protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: probeRun, Host: "10.0.20.185", Port: 3306, Password: dbTestPassword, MaxSeconds: 60}
	r := probeOp(t, g.a, "", start)
	if !r.Running || r.Run != probeRun {
		t.Fatalf("start: %+v", r)
	}
	// Het wachtwoord staat nooit in een argument.
	for _, h := range g.hosts {
		for _, c := range h.Calls() {
			if strings.Contains(c, dbTestPassword) {
				t.Errorf("wachtwoord in een argument: %s", c)
			}
		}
	}
	// Een tweede start geeft dezelfde test.
	if again := probeOp(t, g.a, "", start); again.Run != probeRun || !again.Running {
		t.Errorf("tweede start: %+v", again)
	}
	r = waitAttempts(t, g.a, "", 0, true)

	// nc1 valt weg: schrijven via het VIP lukt niet, tot nc3 het VIP heeft.
	nc1 := g.hosts["10.0.20.186"]
	nc1.Locked(func() { nc1.MariaDB.Galera.State, nc1.MariaDB.Galera.Comment = 1, "Joining" })
	failed := waitAttempts(t, g.a, "", r.Total, false)
	var fail protocol.DBProbeAttempt
	for _, x := range failed.Attempts {
		if !x.OK {
			fail = x
			break
		}
	}
	if !strings.Contains(fail.Error, "WSREP has not yet prepared node") || strings.HasPrefix(fail.Error, "exit status") {
		t.Errorf("fout: %+v", fail)
	}
	g.moveVIP("10.0.20.188")
	back := waitAttempts(t, g.a, "", failed.Total, true)

	// Een bevestigde rij gaat verloren.
	var lost int64
	for _, x := range back.Attempts {
		if x.OK {
			lost = x.Seq
		}
	}
	g.table.Lose(probeRun, lost)
	fin := probeOp(t, g.a, "", protocol.DBProbeRequest{Action: protocol.DBProbeFinish, Run: probeRun})
	if fin.Running || !fin.Checked || fin.Missing != 1 || !slices.Equal(fin.MissingSeqs, []int64{lost}) || fin.CheckError != "" {
		t.Fatalf("finish: %+v", fin)
	}
	ok, bad := 0, 0
	for _, x := range fin.Attempts {
		if x.OK {
			ok++
		} else {
			bad++
		}
	}
	if fin.Confirmed != ok || bad == 0 || fin.Total != len(fin.Attempts) {
		t.Errorf("tellingen: %d bevestigd, %d ok, %d mislukt, %d pogingen", fin.Confirmed, ok, bad, fin.Total)
	}
	// De rijen van de run zijn weg, en de map met het wachtwoord ook.
	if rows := g.table.Rows(probeRun); len(rows) != 0 {
		t.Errorf("rijen over: %v", rows)
	}
	if entries, _ := os.ReadDir(os.Getenv("TMPDIR")); len(entries) != 0 {
		t.Errorf("tijdelijke bestanden over: %v", entries)
	}
	// Opnieuw vragen geeft hetzelfde, vanaf From.
	again := probeOp(t, g.a, "", protocol.DBProbeRequest{Action: protocol.DBProbeFinish, Run: probeRun, From: 2})
	if again.Missing != 1 || again.Confirmed != fin.Confirmed || len(again.Attempts) != fin.Total-2 || again.Attempts[0].Seq != 3 {
		t.Errorf("tweede finish: %+v", again)
	}
}

// TestWriteProbeVerify: de node die schreef, herstart. De nieuwe agent kent
// de schrijftest niet meer; met de volgnummers van de server kijkt hij na
// welke rijen er nog zijn, en haalt hij de oude map met het wachtwoord weg.
func TestWriteProbeVerify(t *testing.T) {
	g := newGaleraProbe(t)
	probeOp(t, g.a, "", protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: probeRun, Host: "10.0.20.185", Port: 3306,
		Password: dbTestPassword, MaxSeconds: 60})
	r := waitAttempts(t, g.a, "", 0, true)
	var seqs []int64
	for _, x := range r.Attempts {
		if x.OK {
			seqs = append(seqs, x.Seq)
		}
	}
	// De herstart: schrijven stopt, de map met het wachtwoord blijft.
	st := g.a.probes()
	st.mu.Lock()
	old := st.runs[probeRun]
	st.mu.Unlock()
	old.stop()
	stale := time.Now().Add(-2 * probeStaleDir)
	if err := os.Chtimes(old.dir, stale, stale); err != nil {
		t.Fatal(err)
	}
	nc2 := g.hosts["10.0.20.187"]
	after := &Agent{Exec: nc2.Exec, ExecStdin: nc2.ExecStdin, Dial: nc2.Dial, Log: g.a.Log}
	g.table.Lose(probeRun, seqs[0])
	verify := protocol.DBProbeRequest{Action: protocol.DBProbeVerify, Run: probeRun, Host: "10.0.20.185", Port: 3306, Password: dbTestPassword}
	for _, tc := range []struct {
		name string
		seqs []int64
		want string
	}{
		{"nul", []int64{0}, "geen volgnummer"},
		{"te veel", make([]int64, protocol.DBProbeVerifyMax+1), "hoogstens"},
	} {
		req := verify
		req.Seqs = tc.seqs
		if msg := probeErr(t, after, "", req); !strings.Contains(msg, tc.want) {
			t.Errorf("%s: %s", tc.name, msg)
		}
	}
	verify.Seqs = seqs
	res := probeOp(t, after, "", verify)
	if !res.Checked || res.Confirmed != len(seqs) || res.Missing != 1 || !slices.Equal(res.MissingSeqs, seqs[:1]) || res.CheckError != "" {
		t.Fatalf("verify: %+v", res)
	}
	// Rijen die er zijn maar die de server niet als bevestigd kende, tellen
	// apart.
	if want := g.table.Written(probeRun) - len(seqs); res.Extra != want {
		t.Errorf("extra: %d, verwacht %d", res.Extra, want)
	}
	if rows := g.table.Rows(probeRun); len(rows) != 0 {
		t.Errorf("rijen over: %v", rows)
	}
	if entries, _ := os.ReadDir(os.Getenv("TMPDIR")); len(entries) != 0 {
		t.Errorf("tijdelijke bestanden over: %v", entries)
	}
	for _, c := range nc2.Calls() {
		if strings.Contains(c, dbTestPassword) {
			t.Errorf("wachtwoord in een argument: %s", c)
		}
	}
	// Een node die het VIP niet bereikt, kan niet nakijken.
	g.moveVIP("10.0.20.199")
	if msg := probeErr(t, after, "", verify); !strings.Contains(msg, "antwoordt niet") {
		t.Errorf("zonder VIP: %s", msg)
	}
}

func TestWriteProbeErrors(t *testing.T) {
	g := newGaleraProbe(t)
	ok := protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: probeRun, Host: "10.0.20.185", Port: 3306, Password: dbTestPassword}
	for _, tc := range []struct {
		name string
		edit func(r *protocol.DBProbeRequest)
		want string
	}{
		{"run", func(r *protocol.DBProbeRequest) { r.Run = "nextcloud" }, "geen id van een run"},
		{"adres", func(r *protocol.DBProbeRequest) { r.Host = "vip.example" }, "geen IP-adres"},
		{"poort", func(r *protocol.DBProbeRequest) { r.Port = 0 }, "geen poort"},
		{"wachtwoord", func(r *protocol.DBProbeRequest) { r.Password = "kort'" }, "16 tot 128"},
		{"duur", func(r *protocol.DBProbeRequest) { r.MaxSeconds = protocol.DBProbeMaxSeconds + 1 }, "hoogstens"},
		{"actie", func(r *protocol.DBProbeRequest) { r.Action = "drop" }, "onbekende actie"},
		{"lezen zonder test", func(r *protocol.DBProbeRequest) { r.Action = protocol.DBProbeRead }, "geen schrijftest"},
		{"niemand op het VIP", func(r *protocol.DBProbeRequest) { r.Host = "10.0.20.199" }, "antwoordt niet"},
		{"verkeerd wachtwoord", func(r *protocol.DBProbeRequest) { r.Password = strings.Repeat("x", 20) }, "Access denied"},
	} {
		req := ok
		tc.edit(&req)
		if msg := probeErr(t, g.a, "", req); !strings.Contains(msg, tc.want) || strings.Contains(msg, dbTestPassword) {
			t.Errorf("%s: %s", tc.name, msg)
		}
	}
	// Zonder de database clusterforge_probe begint de test niet.
	for _, h := range g.hosts {
		h.Locked(func() { h.MariaDB.Probe = nil })
	}
	if msg := probeErr(t, g.a, "", ok); !strings.Contains(msg, "de tabel clusterforge_probe.writes maken via 10.0.20.185") {
		t.Errorf("zonder database: %s", msg)
	}
	// Stoppen zonder test kan altijd.
	if r := probeOp(t, g.a, "", protocol.DBProbeRequest{Action: protocol.DBProbeStop, Run: probeRun}); r.Running {
		t.Errorf("stop: %+v", r)
	}
}

// TestWriteProbeLimit: na MaxSeconds stopt de agent vanzelf, en finish
// telt daarna nog.
func TestWriteProbeLimit(t *testing.T) {
	g := newGaleraProbe(t)
	probeOp(t, g.a, "", protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: probeRun, Host: "10.0.20.185", Port: 3306,
		Password: dbTestPassword, MaxSeconds: 1})
	deadline := time.Now().Add(5 * time.Second)
	var r *protocol.DBProbeResult
	for time.Now().Before(deadline) {
		if r = probeOp(t, g.a, "", protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: probeRun}); !r.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r.Running || r.Stopped != "na 1 s vanzelf gestopt" || r.Total == 0 {
		t.Fatalf("na de limiet: %+v", r)
	}
	if fin := probeOp(t, g.a, "", protocol.DBProbeRequest{Action: protocol.DBProbeFinish, Run: probeRun}); fin.Missing != 0 || fin.Confirmed != r.Total {
		t.Errorf("finish: %+v", fin)
	}
}

func TestWriteProbePostgres(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	verifier, err := scramVerifier(dbTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	table := agenttest.NewProbeTable()
	pg1, pg2 := pgHost(t, "10.0.20.191", false), pgHost(t, "10.0.20.192", true)
	for _, h := range []*agenttest.Host{pg1, pg2} {
		h.Postgres.Probe = table
		h.Postgres.Roles = append(h.Postgres.Roles, agenttest.PGRole{Name: protocol.DBProbeUser, Verifier: verifier})
		h.Postgres.Databases = append(h.Postgres.Databases, agenttest.Database{Name: protocol.DBProbeDatabase, Owner: protocol.DBProbeUser})
	}
	var mu sync.Mutex
	primary := pg1
	hosts := map[string]*agenttest.Host{"10.0.20.191:5432": pg1, "10.0.20.192:5432": pg2}
	for _, h := range []*agenttest.Host{pg1, pg2} {
		h.Network = func(addr string) *agenttest.Host {
			if addr == "10.0.20.190:5432" {
				mu.Lock()
				defer mu.Unlock()
				return primary
			}
			return hosts[addr]
		}
	}
	a := pgAgent(pg2)
	a.ProbeInterval = 10 * time.Millisecond
	probeOp(t, a, protocol.DBPostgreSQL, protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: probeRun, Host: "10.0.20.190", Port: 5432,
		Password: dbTestPassword, MaxSeconds: 60})
	r := waitAttempts(t, a, protocol.DBPostgreSQL, 0, true)

	// pg1 stopt; het VIP staat even op een replica, daarna is pg2 de
	// primary.
	pg1.Locked(func() { pg1.Postgres.Down = true })
	mu.Lock()
	primary = pg2
	mu.Unlock()
	failed := waitAttempts(t, a, protocol.DBPostgreSQL, r.Total, false)
	var fail protocol.DBProbeAttempt
	for _, x := range failed.Attempts {
		if !x.OK {
			fail = x
			break
		}
	}
	if !strings.Contains(fail.Error, "read-only transaction") {
		t.Errorf("fout: %+v", fail)
	}
	pg2.Locked(func() { pg2.Postgres.Recovery = false })
	waitAttempts(t, a, protocol.DBPostgreSQL, failed.Total, true)
	fin := probeOp(t, a, protocol.DBPostgreSQL, protocol.DBProbeRequest{Action: protocol.DBProbeFinish, Run: probeRun})
	if !fin.Checked || fin.Missing != 0 || fin.Confirmed == 0 || fin.CheckError != "" {
		t.Fatalf("finish: %+v", fin)
	}
	if rows := table.Rows(probeRun); len(rows) != 0 {
		t.Errorf("rijen over: %v", rows)
	}
	for _, c := range pg2.Calls() {
		if strings.Contains(c, dbTestPassword) {
			t.Errorf("wachtwoord in een argument: %s", c)
		}
	}
}

// TestWriteProbeRealMariaDB schrijft echt via TCP: de agent naast de
// MariaDB van CF_TEST_MARIADB_SOURCE schrijft in die van CF_TEST_MARIADB,
// met TLS als die het kan. Zonder het recht INSERT mislukt schrijven, met
// de fout van MariaDB.
func TestWriteProbeRealMariaDB(t *testing.T) {
	dst := os.Getenv("CF_TEST_MARIADB")
	if dst == "" {
		t.Skip("CF_TEST_MARIADB niet gezet")
	}
	src := os.Getenv("CF_TEST_MARIADB_SOURCE")
	if src == "" {
		src = dst
	}
	t.Setenv("TMPDIR", t.TempDir())
	sql := func(query string) {
		t.Helper()
		if out, err := exec.Command("docker", "exec", dst, "mariadb", "-e", query).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", query, err, out)
		}
	}
	srcIP, dstIP := containerIP(t, src), containerIP(t, dst)
	account := "'" + protocol.DBProbeUser + "'@'" + srcIP + "'"
	cleanup := func() {
		sql("DROP DATABASE IF EXISTS " + protocol.DBProbeDatabase + "; DROP USER IF EXISTS " + account)
	}
	cleanup()
	t.Cleanup(cleanup)
	sql("CREATE DATABASE " + protocol.DBProbeDatabase + "; CREATE USER " + account + " IDENTIFIED BY '" + dbTestPassword + "'; " +
		"GRANT ALL PRIVILEGES ON `clusterforge\\_probe`.* TO " + account)

	a := dockerAgent(t, src)
	a.ProbeInterval = 50 * time.Millisecond
	run := probeRunID()
	probeOp(t, a, "", protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: run, Host: dstIP, Port: 3306, Password: dbTestPassword, MaxSeconds: 60})
	read := func(from int, ok bool) *protocol.DBProbeResult {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			r := probeOp(t, a, "", protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: run, From: from})
			if slices.ContainsFunc(r.Attempts, func(x protocol.DBProbeAttempt) bool { return x.OK == ok }) {
				return r
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("geen poging met ok=%v", ok)
		return nil
	}
	r := read(0, true)
	sql("REVOKE INSERT ON `clusterforge\\_probe`.* FROM " + account)
	failed := read(r.Total, false)
	sql("GRANT INSERT ON `clusterforge\\_probe`.* TO " + account)
	read(failed.Total, true)
	fin := probeOp(t, a, "", protocol.DBProbeRequest{Action: protocol.DBProbeFinish, Run: run})
	if !fin.Checked || fin.Missing != 0 || fin.Confirmed == 0 || fin.CheckError != "" {
		t.Fatalf("finish: %+v", fin)
	}
	var denied string
	for _, x := range fin.Attempts {
		if !x.OK {
			denied = x.Error
			break
		}
	}
	if !strings.Contains(denied, "INSERT command denied") || strings.HasPrefix(denied, "exit status") {
		t.Errorf("fout zonder INSERT: %q", denied)
	}
	count := func() string {
		t.Helper()
		out, err := exec.Command("docker", "exec", dst, "mariadb", "-N", "-e", "SELECT COUNT(*) FROM clusterforge_probe.writes").Output()
		if err != nil {
			t.Fatalf("tellen: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	if n := count(); n != "0" {
		t.Errorf("rijen over: %s", n)
	}

	// Nakijken zoals na een herstart, met de volgnummers van de server.
	run = probeRunID()
	probeOp(t, a, "", protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: run, Host: dstIP, Port: 3306, Password: dbTestPassword, MaxSeconds: 60})
	var seqs []int64
	for _, x := range read(0, true).Attempts {
		if x.OK {
			seqs = append(seqs, x.Seq)
		}
	}
	res := probeOp(t, a, "", protocol.DBProbeRequest{Action: protocol.DBProbeVerify, Run: run, Host: dstIP, Port: 3306, Password: dbTestPassword,
		Seqs: append(seqs, 999999)})
	if !res.Checked || res.Missing != 1 || !slices.Equal(res.MissingSeqs, []int64{999999}) || res.CheckError != "" {
		t.Fatalf("verify: %+v", res)
	}
	if n := count(); n != "0" {
		t.Errorf("rijen over na verify: %s", n)
	}
}

// TestWriteProbeRealPostgres schrijft met psql via TCP in de PostgreSQL
// van CF_TEST_DATABASE_URL, met het wachtwoord uit een passfile.
func TestWriteProbeRealPostgres(t *testing.T) {
	r := newRealPG(t)
	cleanup := func() {
		_, _ = r.psqlAs(r.admin, "postgres", `DROP DATABASE IF EXISTS `+protocol.DBProbeDatabase+` WITH (FORCE)`)
		_, _ = r.psqlAs(r.admin, "postgres", `DROP ROLE IF EXISTS `+protocol.DBProbeUser)
	}
	cleanup()
	t.Cleanup(cleanup)
	if res := pgRequest(t, r.a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: protocol.DBProbeDatabase, User: protocol.DBProbeUser, Password: dbTestPassword, Grant: true,
	}}); res.Error != "" {
		t.Fatal(res.Error)
	}
	r.a.ProbeInterval = 50 * time.Millisecond
	run := probeRunID()
	probeOp(t, r.a, protocol.DBPostgreSQL, protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: run, Host: "127.0.0.1", Port: protocol.DBPGPort,
		Password: dbTestPassword, MaxSeconds: 60})
	deadline := time.Now().Add(30 * time.Second)
	for {
		res := probeOp(t, r.a, protocol.DBPostgreSQL, protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: run})
		if res.Total >= 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("te weinig pogingen: %+v", res)
		}
		time.Sleep(50 * time.Millisecond)
	}
	fin := probeOp(t, r.a, protocol.DBPostgreSQL, protocol.DBProbeRequest{Action: protocol.DBProbeFinish, Run: run})
	if !fin.Checked || fin.Missing != 0 || fin.Confirmed < 5 || fin.Confirmed != fin.Total || fin.CheckError != "" {
		t.Fatalf("finish: %+v", fin)
	}
	if n := r.sql(protocol.DBProbeDatabase, "SELECT count(*) FROM writes"); n != "0" {
		t.Errorf("rijen over: %s", n)
	}

	// Nakijken zoals na een herstart: een tweede run, met de volgnummers
	// die de server kent.
	run = probeRunID()
	probeOp(t, r.a, protocol.DBPostgreSQL, protocol.DBProbeRequest{Action: protocol.DBProbeStart, Run: run, Host: "127.0.0.1", Port: protocol.DBPGPort,
		Password: dbTestPassword, MaxSeconds: 60})
	var seqs []int64
	for len(seqs) < 3 {
		if time.Now().After(deadline.Add(30 * time.Second)) {
			t.Fatalf("te weinig bevestigde rijen: %v", seqs)
		}
		time.Sleep(50 * time.Millisecond)
		seqs = seqs[:0]
		for _, x := range probeOp(t, r.a, protocol.DBPostgreSQL, protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: run}).Attempts {
			if x.OK {
				seqs = append(seqs, x.Seq)
			}
		}
	}
	r.sql(protocol.DBProbeDatabase, fmt.Sprintf("DELETE FROM writes WHERE run = '%s' AND seq = %d", run, seqs[1]))
	res := probeOp(t, r.a, protocol.DBPostgreSQL, protocol.DBProbeRequest{Action: protocol.DBProbeVerify, Run: run, Host: "127.0.0.1",
		Port: protocol.DBPGPort, Password: dbTestPassword, Seqs: seqs})
	if !res.Checked || res.Missing != 1 || !slices.Equal(res.MissingSeqs, seqs[1:2]) || res.Confirmed != len(seqs) || res.CheckError != "" {
		t.Fatalf("verify: %+v", res)
	}
	if n := r.sql(protocol.DBProbeDatabase, "SELECT count(*) FROM writes"); n != "0" {
		t.Errorf("rijen over na verify: %s", n)
	}
}

func probeRunID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:]
}
