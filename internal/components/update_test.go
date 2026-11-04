package components

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De volgorde van een rollende wijziging: wat in order staat eerst, de
// rest erachter op de gewone volgorde.
func TestOrdered(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	spec := Spec{Nodes: []Node{
		{NodeID: ids[0], Hostname: "nc1", Role: RoleData, Index: 1},
		{NodeID: ids[1], Hostname: "nc2", Role: RoleData, Index: 2},
		{NodeID: ids[2], Hostname: "nc3", Role: RoleData, Index: 3},
		{NodeID: ids[3], Hostname: "arb", Role: RoleArbiter, Index: 1},
	}}
	names := func(ns []Node) string {
		var out []string
		for _, n := range ns {
			out = append(out, n.Hostname)
		}
		return strings.Join(out, " ")
	}
	if got := names(spec.ordered(nil)); got != "nc1 nc2 nc3 arb" {
		t.Errorf("zonder volgorde: %s", got)
	}
	if got := names(spec.ordered([]uuid.UUID{ids[1], ids[2], ids[0]})); got != "nc2 nc3 nc1 arb" {
		t.Errorf("nc1 als laatste: %s", got)
	}
}

func TestUpdatable(t *testing.T) {
	pkgs := []string{"mariadb-server", "mariadb-client-core", "galera-4", "libmariadb3", "mysql-common", "openssl", "postgresql-17", "libpq5", "patroni", "python3-etcd", "postgresql-common"}
	if got := updatable(protocol.DBMariaDB, RoleData, pkgs); !slices.Equal(got, []string{"mariadb-server", "mariadb-client-core", "galera-4", "libmariadb3", "mysql-common"}) {
		t.Errorf("MariaDB: %v", got)
	}
	if got := updatable(protocol.DBMariaDB, RoleArbiter, pkgs); !slices.Equal(got, []string{"galera-4"}) {
		t.Errorf("garbd: %v", got)
	}
	if got := updatable(protocol.DBPostgreSQL, RoleData, pkgs); !slices.Equal(got, []string{"postgresql-17", "libpq5", "patroni", "python3-etcd", "postgresql-common"}) {
		t.Errorf("PostgreSQL: %v", got)
	}
	if got := updatable(protocol.DBPostgreSQL, RoleArbiter, pkgs); got != nil {
		t.Errorf("scheidsrechter van Patroni: %v", got)
	}
}

// De stappen van bijwerken hebben elk een merkteken van de taak, en de
// patronen staan tussen enkele aanhalingstekens, zodat de shell ze niet
// uitvouwt.
func TestUpdateSteps(t *testing.T) {
	id := uuid.MustParse("6f1c2b8e-0000-4000-8000-000000000001")
	m := "/var/lib/clusterforge/db-update-" + id.String()
	check, found, install, restart := updateSteps(id, updatePackages[protocol.DBMariaDB][RoleData], "mariadb")
	if check.Command.Creates != m+".checked" || install.Command.Creates != m+".done" || found.Command.Unless != "test ! -e "+m+".found" {
		t.Errorf("merktekens: %+v %+v %+v", check.Command, install.Command, found.Command)
	}
	if !strings.Contains(check.Command.Run, `dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' 'mariadb-*' 'libmariadb*' 'galera-*' 'mysql-common'`) ||
		!strings.Contains(check.Command.Run, "! -name 'db-update-"+id.String()+"*' -delete") {
		t.Errorf("check: %s", check.Command.Run)
	}
	if !strings.Contains(install.Command.Run, "date +%s > "+m+".t0") || !strings.Contains(install.Command.Run, "cp "+m+".t0 "+m+".restart") ||
		!strings.Contains(install.Command.Run, galeraExtra("apt-get -q -o DPkg::Lock::Timeout=300")+`; if [ -n "$extra" ]; then`) ||
		!strings.Contains(install.Command.Run, `--force-confold $pkgs "$extra"; else`) ||
		!strings.Contains(install.Command.Run, "--force-confold $pkgs; fi && "+galeraScripts("apt-get -q -o DPkg::Lock::Timeout=300")+" && dpkg-query") ||
		strings.Contains(install.Command.Run, "%!") {
		t.Errorf("install: %s", install.Command.Run)
	}
	if restart.Command.Run != "systemctl restart --no-block mariadb && rm -f "+m+".restart" ||
		!strings.HasPrefix(restart.Command.Unless, "test ! -e "+m+".restart || ") || !strings.Contains(restart.Command.Unless, "+%s") {
		t.Errorf("restart: %+v", restart.Command)
	}
	_, _, install, restart = updateSteps(id, updatePackages[protocol.DBPostgreSQL][RoleData], "patroni")
	if restart.Command.Run != "systemctl restart patroni && rm -f "+m+".restart" || strings.Contains(install.Command.Run, "galera") ||
		!strings.Contains(install.Command.Run, "install -y --only-upgrade --no-remove -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold $pkgs && dpkg-query") {
		t.Errorf("patroni: %s\n%s", restart.Command.Run, install.Command.Run)
	}
}

// TestGaleraScripts draait galeraScripts in sh, met dpkg-query, apt-get en
// grep als scripts die hun aanroep opschrijven.
func TestGaleraScripts(t *testing.T) {
	run := func(t *testing.T, galera4 string, recovery bool, simExit int) (log, stdout, stderr string, code int) {
		t.Helper()
		dir := t.TempDir()
		logFile := filepath.Join(dir, "log")
		tools := map[string]string{
			"dpkg-query": fmt.Sprintf("case \"$*\" in *galera-4*) printf '%%s' '%s';; *mariadb-server*) printf '1:12.3.2+maria~deb13';; esac\n", galera4),
			"apt-get":    fmt.Sprintf("echo \"apt-get $*\" >> '%s'\ncase \"$*\" in *' -s '*) exit %d;; esac\n", logFile, simExit),
			"grep":       "exec /usr/bin/grep \"$@\"\n",
		}
		if recovery {
			tools["galera_recovery"] = "exit 0\n"
		}
		for name, body := range tools {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("/bin/sh", "-c", galeraScripts("apt-get -q")+" && echo klaar")
		cmd.Env = []string{"PATH=" + dir}
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			code = ee.ExitCode()
		case err != nil:
			t.Fatal(err)
		}
		b, _ := os.ReadFile(logFile)
		return string(b), out.String(), errb.String(), code
	}
	pin := "mariadb-server-galera=1:12.3.2+maria~deb13"
	for _, tc := range []struct {
		name, galera4 string
		recovery      bool
	}{{"zonder Galera", "un ", false}, {"galera-4 nooit geïnstalleerd", "", false}, {"met galera_recovery", "ii ", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if log, out, _, code := run(t, tc.galera4, tc.recovery, 0); code != 0 || log != "" || out != "klaar\n" {
				t.Fatalf("code %d, log %q, uitvoer %q", code, log, out)
			}
		})
	}
	t.Run("installeren", func(t *testing.T) {
		log, out, _, code := run(t, "ii ", false, 0)
		if code != 0 || out != "nieuw: mariadb-server-galera (sinds MariaDB 12.3.2 apart)\nklaar\n" ||
			log != "apt-get -q -s -o Debug::NoLocking=1 install --no-remove "+pin+"\n"+
				"apt-get -q install -y --no-install-recommends --no-remove -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "+pin+"\n" {
			t.Fatalf("code %d, uitvoer %q, log %q", code, out, log)
		}
	})
	t.Run("niet in de pakketbron", func(t *testing.T) {
		log, out, stderr, code := run(t, "ii ", false, 100)
		if code != 1 || out != "" || !strings.HasPrefix(stderr, "mariadb-server-galera staat niet in de pakketbron") ||
			log != "apt-get -q -s -o Debug::NoLocking=1 install --no-remove "+pin+"\n" {
			t.Fatalf("code %d, uitvoer %q, stderr %q, log %q", code, out, stderr, log)
		}
	})
}

// TestGaleraExtra draait galeraExtra in sh: mariadb-server-galera gaat mee
// als Galera er staat, het pakket nog niet, en apt het in de versie van de
// nieuwe mariadb-server kan installeren.
func TestGaleraExtra(t *testing.T) {
	for _, tc := range []struct {
		name, galera4, pkg, cand string
		sim                      int
		want                     string
	}{
		{"nodig", "ii ", "un ", "1:12.3.3+maria~deb13", 0, "mariadb-server-galera=1:12.3.3+maria~deb13"},
		{"zonder Galera", "un ", "un ", "1:12.3.3+maria~deb13", 0, ""},
		{"al geïnstalleerd", "ii ", "ii ", "1:12.3.3+maria~deb13", 0, ""},
		{"niet in de pakketbron", "ii ", "", "1:11.8.3-0+deb13u1", 100, ""},
		{"geen kandidaat", "ii ", "", "(none)", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logFile := filepath.Join(dir, "log")
			tools := map[string]string{
				"dpkg-query": fmt.Sprintf("case \"$*\" in *galera-4*) printf '%%s' '%s';; *mariadb-server-galera*) printf '%%s' '%s';; esac\n", tc.galera4, tc.pkg),
				"apt-cache":  fmt.Sprintf("printf 'mariadb-server:\\n  Installed: 1:12.3.1+maria~deb13\\n  Candidate: %s\\n'\n", tc.cand),
				"apt-get":    fmt.Sprintf("echo \"apt-get $*\" >> '%s'\nexit %d\n", logFile, tc.sim),
				"grep":       "exec /usr/bin/grep \"$@\"\n",
				"awk":        "exec /usr/bin/awk \"$@\"\n",
			}
			for name, body := range tools {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("/bin/sh", "-c", galeraExtra("apt-get -q")+` && printf '%s' "$extra"`)
			cmd.Env = []string{"PATH=" + dir}
			out, err := cmd.Output()
			if err != nil || string(out) != tc.want {
				t.Fatalf("%v, extra %q", err, out)
			}
			b, _ := os.ReadFile(logFile)
			if tc.want != "" && string(b) != "apt-get -q -s -o Debug::NoLocking=1 install --no-remove "+tc.want+"\n" {
				t.Errorf("log %q", b)
			}
		})
	}
}
