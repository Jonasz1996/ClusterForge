package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestDumpScan(t *testing.T) {
	maria := "-- MariaDB dump\n--\n-- Table structure for table `a`\n--\nCREATE TABLE `a`;\n" +
		"-- Temporary table structure for view `v`\n" + strings.Repeat("x", 100) + "\n" +
		"-- Table structure for table `b`\n\n-- Table structure for table `c`\n" + strings.Repeat("y", 3000) +
		"\n-- Dump completed on 2026-10-12  2:00:00\n"
	// Zoals pg_dump 17.6 en later: na de slotregel nog \unrestrict met een
	// sleutel van 64 tekens.
	pg := "--\n-- PostgreSQL database dump\n--\n\n\\restrict " + strings.Repeat("k", 64) + "\n\n" +
		"--\n-- Name: a; Type: TABLE; Schema: public; Owner: nextcloud\n--\n\nCREATE TABLE public.a (id integer);\n" +
		"--\n-- Name: a_id_seq; Type: SEQUENCE; Schema: public; Owner: nextcloud\n--\n" +
		"--\n-- Data for Name: a; Type: TABLE DATA; Schema: public; Owner: nextcloud\n--\n\nCOPY public.a (id) FROM stdin;\n1\n\\.\n" +
		"--\n-- Name: " + strings.Repeat("b", 63) + "; Type: TABLE; Schema: " + strings.Repeat("s", 63) + "; Owner: " + strings.Repeat("o", 63) + "\n" +
		"--\n-- Name: p; Type: TABLE; Schema: public; Owner: nextcloud\n" + strings.Repeat("z", 3000) +
		"\n--\n-- PostgreSQL database dump complete\n--\n\n\\unrestrict " + strings.Repeat("k", 64) + "\n\n"
	for _, c := range []struct {
		engine, dump, end string
	}{
		{protocol.DBMariaDB, maria, "-- Dump completed on 2026-10-12  2:00:00\n"},
		{protocol.DBPostgreSQL, pg, "-- PostgreSQL database dump complete\n--\n\n\\unrestrict " + strings.Repeat("k", 64) + "\n\n"},
	} {
		for _, size := range []int{1, 2, 7, 31, 32, 64, 1000, 5000} {
			s := newDumpScan(c.engine, nil)
			for p := c.dump; p != ""; {
				n := min(size, len(p))
				if _, err := s.Write([]byte(p[:n])); err != nil {
					t.Fatal(err)
				}
				p = p[n:]
			}
			if s.tables != 3 || !s.complete() || s.n != int64(len(c.dump)) {
				t.Errorf("%s in stukken van %d: %d tabellen, compleet %v, %d bytes", c.engine, size, s.tables, s.complete(), s.n)
			}
		}
		s := newDumpScan(c.engine, nil)
		_, _ = s.Write([]byte(strings.TrimSuffix(c.dump, c.end)))
		if s.complete() {
			t.Errorf("%s: een dump zonder slotregel heet compleet", c.engine)
		}
	}
	// De slotregel van de andere engine telt niet.
	if s := newDumpScan(protocol.DBPostgreSQL, nil); func() bool { _, _ = s.Write([]byte(maria)); return s.complete() || s.tables != 0 }() {
		t.Error("een dump van MariaDB telt als dump van PostgreSQL")
	}
}

// dumpAgent is een agent met een nagespeelde MariaDB met nextcloud, en een
// klok die de test zet.
func dumpAgent(t *testing.T) (*Agent, *agenttest.Host, *time.Time) {
	t.Helper()
	h := agenttest.NewHost(t.TempDir(), "10.0.20.187")
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	h.MariaDB = syncedMaria()
	h.MariaDB.Databases = []agenttest.Database{{Name: "nextcloud", Size: 2 << 20, Tables: 3}}
	now := time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
	a := &Agent{
		Exec: h.Exec, ExecStdin: h.ExecStdin, Stream: h.Stream, DumpDir: filepath.Join(t.TempDir(), "clusterforge-db"),
		Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return a, h, &now
}

func dumpReq(t *testing.T, a *Agent, req protocol.DBDumpRequest) protocol.DBDumpResult {
	t.Helper()
	res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDump, Dump: &req})
	if res.Error != "" {
		t.Fatalf("%s: %s", req.Action, res.Error)
	}
	if err := a.waitDump(t.Context()); err != nil {
		t.Fatal(err)
	}
	return *res.Dump
}

func dumpErr(t *testing.T, a *Agent, req protocol.DBDumpRequest) string {
	t.Helper()
	res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDump, Dump: &req})
	if res.Error == "" {
		t.Fatalf("%s %s: geen fout", req.Action, req.Database)
	}
	return res.Error
}

func listDumps(t *testing.T, a *Agent) protocol.DBDumpResult {
	t.Helper()
	return dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpList})
}

func TestDBDump(t *testing.T) {
	a, h, now := dumpAgent(t)
	if l := listDumps(t, a); len(l.Dumps) != 0 || l.Running != nil {
		t.Fatalf("zonder dumps: %+v", l)
	}
	r := dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"})
	if r.Started != "nextcloud-20261012-020000.sql.zst" {
		t.Fatalf("begonnen: %q", r.Started)
	}
	l := listDumps(t, a)
	if len(l.Dumps) != 1 || l.Running != nil {
		t.Fatalf("na de dump: %+v", l)
	}
	d := l.Dumps[0]
	if d.Error != "" || !d.Complete || d.Tables != 3 || d.TablesExpected != 3 || d.SizeBytes == 0 || d.RawBytes <= d.SizeBytes ||
		!d.Started.Equal(*now) || d.Database != "nextcloud" {
		t.Fatalf("dump: %+v", d)
	}
	if !slices.Contains(h.Calls(), "mariadb-dump --single-transaction --quick --routines --events --triggers --hex-blob --default-character-set=utf8mb4 nextcloud") {
		t.Errorf("mariadb-dump: %v", h.Calls())
	}
	// Alleen root mag erbij.
	for path, mode := range map[string]fs.FileMode{
		a.DumpDir: 0o700 | fs.ModeDir, filepath.Join(a.DumpDir, "nextcloud"): 0o700 | fs.ModeDir,
		filepath.Join(a.DumpDir, "nextcloud", d.File): 0o600, filepath.Join(a.DumpDir, "nextcloud", d.File+".json"): 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != mode {
			t.Errorf("%s: %v, wil %v", path, fi.Mode(), mode)
		}
	}
	// Het is zstd met de SQL erin.
	f, _ := os.Open(filepath.Join(a.DumpDir, "nextcloud", d.File))
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := io.ReadAll(zr)
	zr.Close()
	_ = f.Close()
	if err != nil || int64(len(sql)) != d.RawBytes || !strings.Contains(string(sql), "-- Table structure for table `t3`") {
		t.Fatalf("inhoud: %v, %d bytes", err, len(sql))
	}

	// Geen systeemdatabase, geen database die er niet is, één tegelijk.
	if e := dumpErr(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "mysql"}); !strings.Contains(e, "van MariaDB zelf") {
		t.Errorf("mysql: %s", e)
	}
	if e := dumpErr(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "wiki"}); !strings.Contains(e, "bestaat niet") {
		t.Errorf("wiki: %s", e)
	}
	if e := dumpErr(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}); !strings.Contains(e, "net al een dump") {
		t.Errorf("zelfde seconde: %s", e)
	}
	st := a.dumps()
	if err := st.begin(&protocol.DBDumpRun{Action: protocol.DBDumpStart, Database: "nextcloud"}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if e := dumpErr(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}); !strings.Contains(e, "maakt al een dump van nextcloud") {
		t.Errorf("tegelijk: %s", e)
	}
	st.end(func() {})

	// Een mislukte dump laat geen bestand achter, wel de fout.
	h.Fail("mariadb-dump", "mariadb-dump: Got error: 2013: \"Lost connection to server during query\" when dumping table `t2` at row: 0")
	r = dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"})
	l = listDumps(t, a)
	if len(l.Dumps) != 2 || l.Dumps[1].File != r.Started || !strings.Contains(l.Dumps[1].Error, "Lost connection") {
		t.Fatalf("mislukt: %+v", l.Dumps)
	}
	for _, ext := range []string{"", ".partial"} {
		if _, err := os.Stat(filepath.Join(a.DumpDir, "nextcloud", r.Started+ext)); !os.IsNotExist(err) {
			t.Errorf("%s%s staat er nog: %v", r.Started, ext, err)
		}
	}

	// Zonder slotregel is een dump niet compleet.
	h.Locked(func() { h.MariaDB.DumpIncomplete = true })
	*now = now.Add(time.Minute)
	r = dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"})
	l = listDumps(t, a)
	if d := l.Dumps[2]; d.File != r.Started || d.Complete || d.Error != "" {
		t.Fatalf("niet compleet: %+v", d)
	}
}

func TestDBDumpPrune(t *testing.T) {
	a, h, now := dumpAgent(t)
	h.MariaDB.Databases = append(h.MariaDB.Databases, agenttest.Database{Name: "wiki", Size: 1 << 20, Tables: 2})
	start := *now
	var files []string
	for day := range 10 {
		*now = start.AddDate(0, 0, day)
		files = append(files, dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}).Started)
	}
	*now = start
	wiki := dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "wiki"}).Started
	// Een halve dump van een agent die halverwege stopte.
	partial := filepath.Join(a.DumpDir, "nextcloud", "nextcloud-20261001-020000.sql.zst.partial")
	if err := os.WriteFile(partial, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(partial, old, old)

	*now = start.AddDate(0, 0, 10)
	if e := dumpErr(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpPrune, KeepDays: 0}); !strings.Contains(e, "minstens één dag") {
		t.Errorf("0 dagen: %s", e)
	}
	r := dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpPrune, Database: "nextcloud", KeepDays: 7, Keep: []string{files[1]}})
	// Weg: dag 0 en 2; dag 1 staat in Keep, dag 3 is precies zeven dagen
	// oud, en wiki is een andere database.
	if want := []string{files[0], files[2]}; !slices.Equal(r.Removed, want) {
		t.Fatalf("weg: %v, wil %v", r.Removed, want)
	}
	for _, f := range r.Dumps {
		if f.File == files[0] || f.File == files[2] {
			t.Errorf("%s staat nog in de lijst", f.File)
		}
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("halve dump staat er nog: %v", err)
	}

	// De nieuwste complete dump blijft, ook als hij te oud is.
	*now = start.AddDate(0, 1, 0)
	r = dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpPrune, KeepDays: 7})
	var left []string
	for _, f := range r.Dumps {
		left = append(left, f.File)
	}
	if want := []string{wiki, files[9]}; !slices.Equal(left, want) {
		t.Fatalf("over: %v, wil %v", left, want)
	}
}

func TestDBDumpRestore(t *testing.T) {
	a, h, now := dumpAgent(t)
	file := dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}).Started
	restore := func(target string) protocol.DBDumpRequest {
		return protocol.DBDumpRequest{Action: protocol.DBDumpRestore, Database: "nextcloud", File: file, Target: target}
	}
	dumpReq(t, a, restore("nextcloud_herstel_20261012"))
	l := listDumps(t, a)
	if len(l.Restores) != 1 || l.Restores[0].Error != "" || l.Restores[0].Finished.IsZero() || l.Restores[0].Bytes != l.Restores[0].Total {
		t.Fatalf("teruggezet: %+v", l.Restores)
	}
	var got agenttest.Database
	h.Locked(func() {
		for _, d := range h.MariaDB.Databases {
			if d.Name == "nextcloud_herstel_20261012" {
				got = d
			}
		}
	})
	if got.Tables != 3 {
		t.Fatalf("nieuwe database: %+v", got)
	}

	for target, want := range map[string]string{
		"nextcloud_herstel_20261012": "bestaat al",
		"nextcloud":                  "bestaat al",
		"mysql":                      "van MariaDB zelf",
		"Nextcloud":                  "kleine letters",
	} {
		if e := dumpErr(t, a, restore(target)); !strings.Contains(e, want) {
			t.Errorf("%s: %s", target, e)
		}
	}
	for name, req := range map[string]protocol.DBDumpRequest{
		"andere database": {Action: protocol.DBDumpRestore, Database: "wiki", File: file, Target: "x"},
		"pad":             {Action: protocol.DBDumpRestore, Database: "nextcloud", File: "../" + file, Target: "x"},
	} {
		if e := dumpErr(t, a, req); !strings.Contains(e, "geen dump van") {
			t.Errorf("%s: %s", name, e)
		}
	}

	// Mislukt het terugzetten, dan is de nieuwe database weer weg.
	h.Fail("mariadb --batch --skip-print-query-on-error nextcloud_kapot", "ERROR 1064 (42000) at line 12: You have an error in your SQL syntax")
	dumpReq(t, a, restore("nextcloud_kapot"))
	l = listDumps(t, a)
	if len(l.Restores) != 2 || !strings.Contains(l.Restores[1].Error, "ERROR 1064") {
		t.Fatalf("mislukt: %+v", l.Restores)
	}
	h.Locked(func() {
		for _, d := range h.MariaDB.Databases {
			if d.Name == "nextcloud_kapot" {
				t.Error("nextcloud_kapot staat er nog")
			}
		}
	})

	// Een dump die niet compleet is, komt niet terug.
	h.Locked(func() { h.MariaDB.DumpIncomplete = true })
	*now = now.Add(time.Minute)
	file = dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: "nextcloud"}).Started
	if e := dumpErr(t, a, restore("nextcloud_half")); !strings.Contains(e, "niet compleet") {
		t.Errorf("niet compleet: %s", e)
	}
}

// TestDBDumpRealMariaDB maakt een dump van een echte MariaDB in de container
// CF_TEST_MARIADB, met een view, een trigger, binaire en Unicode-data, en
// zet hem terug in een nieuwe database.
func TestDBDumpRealMariaDB(t *testing.T) {
	container := os.Getenv("CF_TEST_MARIADB")
	if container == "" {
		t.Skip("CF_TEST_MARIADB niet gezet")
	}
	const db, target = "cf_agent_m7", "cf_agent_m7_herstel"
	sql := func(query string) string {
		t.Helper()
		out, err := exec.Command("docker", "exec", container, "mariadb", "-N", "-B", "-e", query).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", query, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	cleanup := func() { sql("DROP DATABASE IF EXISTS " + db + "; DROP DATABASE IF EXISTS " + target) }
	cleanup()
	t.Cleanup(cleanup)
	sql("CREATE DATABASE " + db + " CHARACTER SET utf8mb4; USE " + db + ";" +
		"CREATE TABLE files (id int PRIMARY KEY, name varchar(100), data varbinary(64));" +
		"CREATE TABLE log (id int AUTO_INCREMENT PRIMARY KEY, file int, at datetime);" +
		"CREATE VIEW names AS SELECT name FROM files;" +
		"CREATE TRIGGER files_log AFTER INSERT ON files FOR EACH ROW INSERT INTO log (file, at) VALUES (NEW.id, '2026-10-12 02:00:00');" +
		"INSERT INTO files VALUES (1, 'één.txt', 0x00FF0A0D27), (2, 'tab\there', NULL), (3, 'quote''s', 0x5C);")
	checksum := func(d string) string {
		return sql("SELECT GROUP_CONCAT(id, ':', name, ':', COALESCE(HEX(data), '-') ORDER BY id SEPARATOR '|') FROM "+d+".files") +
			" / " + sql("SELECT COUNT(*) FROM "+d+".log") + " / " + sql("SELECT COUNT(*) FROM "+d+".names")
	}
	want := checksum(db)

	a := &Agent{DumpDir: filepath.Join(t.TempDir(), "clusterforge-db"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return []byte("Id=mariadb.service\nLoadState=loaded\nActiveState=active\n\n"), nil
		case "mariadb":
			out, err := exec.CommandContext(ctx, "docker", append([]string{"exec", container, "mariadb"}, args...)...).Output()
			if ee, ok := err.(*exec.ExitError); ok {
				err = fmt.Errorf("%w: %s", err, ee.Stderr)
			}
			return out, err
		}
		return nil, fmt.Errorf("onbekend commando %s", name)
	}
	a.ExecStdin = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", container, name}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		return cmd.CombinedOutput()
	}
	a.Stream = func(ctx context.Context, in io.Reader, out io.Writer, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", container, name}, args...)...)
		cmd.Stdin, cmd.Stdout = in, out
		var stderr tailWriter
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %s", name, stderr.errorLine())
		}
		return nil
	}

	file := dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: db}).Started
	l := listDumps(t, a)
	if len(l.Dumps) != 1 {
		t.Fatalf("dumps: %+v", l.Dumps)
	}
	if d := l.Dumps[0]; d.Error != "" || !d.Complete || d.Tables != 2 || d.TablesExpected != 2 || d.SizeBytes == 0 {
		t.Fatalf("dump: %+v", d)
	}
	dumpReq(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpRestore, Database: db, File: file, Target: target})
	l = listDumps(t, a)
	if len(l.Restores) != 1 || l.Restores[0].Error != "" {
		t.Fatalf("teruggezet: %+v", l.Restores)
	}
	if got := checksum(target); got != want {
		t.Fatalf("teruggezet: %s, wil %s", got, want)
	}
	if got := sql("SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = '" + target + "'"); got != "1" {
		t.Errorf("triggers: %s", got)
	}

	if e := dumpErr(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpRestore, Database: db, File: file, Target: target}); !strings.Contains(e, "bestaat al") {
		t.Errorf("bestaat al: %s", e)
	}
	sql("DROP DATABASE " + db)
	if e := dumpErr(t, a, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: db}); !strings.Contains(e, "bestaat niet") {
		t.Errorf("weg: %s", e)
	}
}
