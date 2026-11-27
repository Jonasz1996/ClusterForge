package upkeep

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func full() Policy {
	p := DefaultPolicy
	p.Enabled, p.Tmp, p.DockerLogsMB, p.DBOptimize = true, true, 200, true
	return p
}

func TestScriptSyntax(t *testing.T) {
	for _, mode := range []string{ModeRun, ModePreview, ModeUrgent} {
		for _, env := range []NodeEnv{{}, {GaleraExpect: 3, Degraded: true}} {
			s := Script(full(), mode, env)
			for _, ph := range []string{"@MODE@", "@DEGRADED@", "@GALERA@", "@JOURNALMB@", "@OLDDAYS@", "@TMPDAYS@", "@IMGHOURS@",
				"@IMGDAYS@", "@LOGSMB@", "@MINMB@", "@MINPCT@", "@ANALYZE@", "@OPTIMIZE@", "@VACUUM@", "@REINDEX@"} {
				if strings.Contains(s, ph) {
					t.Fatalf("%s blijft staan in %s", ph, mode)
				}
			}
			f := filepath.Join(t.TempDir(), "s.sh")
			if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
				t.Fatalf("%s: sh -n: %v\n%s", mode, err, out)
			}
		}
	}
	if s := Script(full(), ModeUrgent, NodeEnv{}); strings.Contains(s, "MariaDB") || strings.Contains(s, "VACUUM") {
		t.Fatal("een spoedbeurt raakt geen databases")
	}
	s := Script(full(), ModeRun, NodeEnv{GaleraExpect: 3})
	if !strings.Contains(s, "OPTIMIZE NO_WRITE_TO_BINLOG TABLE \\`$s\\`.\\`$t\\`") || !strings.Contains(s, "wsrep_OSU_method='RSU'") ||
		!strings.Contains(s, "GALERA_EXPECT=3") || !strings.Contains(s, "MODE=run") {
		t.Fatal("OPTIMIZE zonder RSU, quotes of grootte")
	}
	if s := Script(Policy{Journal: true, JournalMB: 500}, ModeRun, NodeEnv{}); strings.Contains(s, "docker") || strings.Contains(s, "psql") {
		t.Fatal("alleen wat aan staat hoort in het script")
	}
}

// TestScriptRuns draait het voorbeeld echt: in de testcontainer zonder
// databases moet het netjes eindigen met de gebruikte ruimte.
func TestScriptRuns(t *testing.T) {
	if _, err := exec.LookPath("df"); err != nil {
		t.Skip("geen df")
	}
	p := full()
	p.DBAnalyze, p.DBOptimize = false, false
	out, err := exec.Command("sh", "-c", Script(p, ModePreview, NodeEnv{})).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := Parse(strings.Split(string(out), "\n"), true)
	if r.UsedBefore == 0 || r.UsedAfter == 0 || len(r.Items) == 0 {
		t.Fatalf("resultaat: %+v\n%s", r, out)
	}
	for _, it := range r.Items {
		if it.Status != "plan" && it.Status != "skip" {
			t.Fatalf("voorbeeld deed iets: %+v", it)
		}
	}
}

func TestParse(t *testing.T) {
	lines := []string{
		"CFUPKEEP\tused\tbefore\t10000",
		"CFUPKEEP\tsize\tjournal\t3000",
		"CFUPKEEP\titem\tjournal\tok\t",
		"CFUPKEEP\tdsize\tdocker\t1.5GB (40%)",
		"CFUPKEEP\titem\tdocker\tfail\tdocker image prune gaf een fout",
		"CFUPKEEP\tdb\tmariadb\tgalera\tanalyze\t12\tstatistieken van 12 tabellen bijgewerkt",
		"CFUPKEEP\ttable\tmariadb\tapp.log\t300000000\t900000000",
		"gewone regel",
		"CFUPKEEP\tused\tafter\t7000",
	}
	r := Parse(lines, false)
	if r.Freed != 3000 || len(r.Items) != 2 || r.Items[1].Bytes != 1_500_000_000 || r.OK() {
		t.Fatalf("%+v", r)
	}
	if len(r.DB) != 1 || r.DB[0].Count != 12 || len(r.Tables) != 1 || r.Tables[0].Free != 300000000 {
		t.Fatalf("%+v", r)
	}
	p := Parse([]string{lines[1], lines[3]}, true)
	if p.Freed != 1_500_003_000 || p.Items[0].Status != "plan" {
		t.Fatalf("voorbeeld: %+v", p)
	}
}

func TestOrder(t *testing.T) {
	in := []PlanNode{
		{NodeID: uuid.New(), Hostname: "db3", VIPHolder: true, DBRole: protocol.DBRoleReplica},
		{NodeID: uuid.New(), Hostname: "db1", DBRole: protocol.DBRolePrimary},
		{NodeID: uuid.New(), Hostname: "db2", DBRole: protocol.DBRoleReplica},
		{NodeID: uuid.New(), Hostname: "Alpha"},
	}
	got := Order(in)
	var names []string
	for _, n := range got {
		names = append(names, n.Hostname)
	}
	if strings.Join(names, ",") != "Alpha,db2,db1,db3" || got[3].Order != 4 || !strings.Contains(got[3].Why, "VIP") {
		t.Fatalf("%v %+v", names, got)
	}
}

func TestNormalize(t *testing.T) {
	p, err := Policy{Enabled: true, Journal: true}.Normalize()
	if err != nil || p.JournalMB != 500 || p.Window != DefaultPolicy.Window {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := (Policy{Enabled: true}).Normalize(); err == nil {
		t.Fatal("niets gekozen moet een fout geven")
	}
	if _, err := (Policy{Journal: true, Window: "ooit"}).Normalize(); err == nil {
		t.Fatal("slecht venster")
	}
	if _, err := (Policy{Journal: true, DockerLogsMB: 5}).Normalize(); err == nil {
		t.Fatal("te kleine logdrempel")
	}
}

// TestScriptGalera speelt een Galera-node na met een nep-mariadb: ANALYZE
// en OPTIMIZE lokaal, OPTIMIZE met RSU, en niets bij een te klein cluster.
func TestScriptGalera(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("pgrep", `[ "$2" = mariadbd ] && exit 0; exit 1`)
	write("mariadb", `q="$4"
case "$q" in
  "SELECT 1") echo 1 ;;
  *wsrep_cluster_size*) printf 'wsrep_cluster_size\t'"${SIZE:-3}"'\n' ;;
  *wsrep_local_state*) printf 'wsrep_local_state\t4\n' ;;
  *wsrep_cluster_status*) printf 'wsrep_cluster_status\tPrimary\n' ;;
  *data_free*) printf 'app\tlog\t300000000\t900000000\n' ;;
  *information_schema.tables*) printf 'app\tusers\napp\tlog\n' ;;
  *) echo "$q" >> "`+calls+`" ;;
esac
`)
	p := Policy{DBAnalyze: true, DBOptimize: true, OptimizeMinMB: 100, OptimizeMinPct: 20}
	run := func(size string, env NodeEnv) Result {
		_ = os.Remove(calls)
		cmd := exec.Command("sh", "-c", Script(p, ModeRun, env))
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SIZE="+size)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return Parse(strings.Split(string(out), "\n"), false)
	}
	r := run("3", NodeEnv{GaleraExpect: 3})
	got, _ := os.ReadFile(calls)
	want := "ANALYZE NO_WRITE_TO_BINLOG TABLE `app`.`users`\nANALYZE NO_WRITE_TO_BINLOG TABLE `app`.`log`\n" +
		"SET SESSION wsrep_OSU_method='RSU'; OPTIMIZE NO_WRITE_TO_BINLOG TABLE `app`.`log`\n"
	if string(got) != want {
		t.Fatalf("aanroepen:\n%s\nwil:\n%s", got, want)
	}
	if len(r.DB) != 2 || r.DB[0].Role != "galera" || r.DB[0].Count != 2 || r.DB[1].Action != "optimize" || len(r.Tables) != 1 || !r.OK() {
		t.Fatalf("%+v", r)
	}
	// Een node weg: Galera is niet volledig, de database blijft ongemoeid.
	r = run("2", NodeEnv{GaleraExpect: 3})
	if got, _ := os.ReadFile(calls); len(got) != 0 || len(r.DB) != 1 || r.DB[0].Action != "skip" {
		t.Fatalf("te klein: %s %+v", got, r)
	}
	// Een andere node offline: ANALYZE wel, OPTIMIZE niet.
	r = run("3", NodeEnv{GaleraExpect: 3, Degraded: true})
	if got, _ := os.ReadFile(calls); strings.Contains(string(got), "OPTIMIZE") || r.DB[1].Action != "skip" {
		t.Fatalf("degraded: %s %+v", got, r)
	}
}

// TestScriptPostgres: VACUUM alleen op de primary; een replica slaat over.
func TestScriptPostgres(t *testing.T) {
	if _, err := exec.LookPath("id"); err != nil {
		t.Skip("geen id")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock")
	calls := filepath.Join(dir, "calls")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("pgrep", `[ "$2" = postgres ] && exit 0; exit 1`)
	write("id", `exit 0`)
	write("psql", `exit 0`)
	// runuser -u postgres -- psql ... -c "<sql>": de sql is het laatste argument.
	write("runuser", `for a; do q="$a"; done
case "$q" in
  "SELECT 1") echo 1 ;;
  *pg_is_in_recovery*) echo "${REC:-f}" ;;
  *pg_database*) printf 'app\npostgres\n' ;;
  *) echo "$q" >> "`+calls+`" ;;
esac
`)
	// Het script zoekt de socket in vaste mappen; in de test via een
	// symbolische link is dat niet na te spelen, dus vervangen we het pad.
	p := Policy{DBAnalyze: true}
	run := func(rec string) Result {
		_ = os.Remove(calls)
		s := strings.Replace(Script(p, ModeRun, NodeEnv{}), "/var/run/postgresql /run/postgresql /tmp", sock, 1)
		if err := os.MkdirAll(sock, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", `python3 -c "import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])" `+sock+`/.s.PGSQL.5433 2>/dev/null; `+s)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "REC="+rec)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return Parse(strings.Split(string(out), "\n"), false)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("geen python3 voor de socket")
	}
	r := run("f")
	got, _ := os.ReadFile(calls)
	if string(got) != "VACUUM (ANALYZE)\nVACUUM (ANALYZE)\n" || len(r.DB) != 1 || r.DB[0].Role != "primary" || r.DB[0].Count != 2 {
		t.Fatalf("primary: %q %+v", got, r)
	}
	r = run("t")
	if got, _ := os.ReadFile(calls); len(got) != 0 || len(r.DB) != 1 || r.DB[0].Role != "replica" {
		t.Fatalf("replica: %q %+v", got, r)
	}
}
