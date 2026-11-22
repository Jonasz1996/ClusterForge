package galera

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const clusterUUID = "5c3a1e0e-a8f1-11f0-9a3b-7e1c2d3f4a5b"

func TestParseGrastate(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     Position
		err      bool
	}{
		{"na een gewone stop", "# GALERA saved state\nversion: 2.1\nuuid:    " + clusterUUID + "\nseqno:   1234\nsafe_to_bootstrap: 1\n",
			Position{UUID: clusterUUID, Seqno: 1234, SafeToBootstrap: true}, false},
		{"na een crash", "# GALERA saved state\nversion: 2.1\nuuid:    " + clusterUUID + "\nseqno:   -1\nsafe_to_bootstrap: 0\n",
			Position{UUID: clusterUUID, Seqno: -1}, false},
		{"nieuwe node", "# GALERA saved state\nversion: 2.1\nuuid:    " + NoUUID + "\nseqno:   -1\nsafe_to_bootstrap: 0\n",
			Position{UUID: NoUUID, Seqno: -1}, false},
		{"leeg", "", Position{Seqno: -1}, true},
		{"kapotte seqno", "uuid: " + clusterUUID + "\nseqno: x\n", Position{UUID: clusterUUID, Seqno: -1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseGrastate(tc.in)
			if (err != nil) != tc.err || got != tc.want {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	if p, _ := ParseGrastate("uuid: " + clusterUUID + "\nseqno: -1\n"); p.Known() || p.String() != "onbekend" {
		t.Fatalf("seqno -1 is onbekend: %+v", p)
	}
	if p, _ := ParseGrastate("uuid: " + NoUUID + "\nseqno: 0\n"); p.Known() {
		t.Fatalf("de lege uuid is onbekend: %+v", p)
	}
}

func TestParseRecovered(t *testing.T) {
	// Zoals galera_recovery van MariaDB 11.8 het schrijft, en mysqld
	// --wsrep-recover in zijn log, met een GTID erachter.
	for _, in := range []string{
		"--wsrep_start_position=" + clusterUUID + ":1234\n",
		"2026-10-12 12:01:02 0 [Note] WSREP: Recovered position: " + clusterUUID + ":1234\n",
		"[Note] WSREP: Recovered position " + strings.ToUpper(clusterUUID) + ":1234,0-1-1234\n",
		"[Note] WSREP: Recovered position: " + clusterUUID + ":7\n--wsrep_start_position=" + clusterUUID + ":1234\n",
	} {
		p, err := ParseRecovered(in)
		if err != nil || p != (Position{UUID: clusterUUID, Seqno: 1234}) {
			t.Errorf("%q: %+v %v", in, p, err)
		}
	}
	if _, err := ParseRecovered("WSREP: Failed to recover position\n"); err == nil {
		t.Error("zonder positie: geen fout")
	}
}

func TestChoose(t *testing.T) {
	pos := func(seqno int64, safe bool) Position {
		return Position{UUID: clusterUUID, Seqno: seqno, SafeToBootstrap: safe}
	}
	for _, tc := range []struct {
		name  string
		cands []Candidate
		want  string
		err   string
	}{
		{"hoogste seqno", []Candidate{{"nc1", 1, pos(9, false)}, {"nc2", 2, pos(11, false)}, {"nc3", 3, pos(10, false)}}, "nc2", ""},
		{"gelijk: laatst gestopt", []Candidate{{"nc1", 1, pos(11, false)}, {"nc2", 2, pos(11, false)}, {"nc3", 3, pos(11, true)}}, "nc3", ""},
		{"gelijk: laagste volgnummer", []Candidate{{"nc3", 3, pos(11, false)}, {"nc2", 2, pos(11, false)}}, "nc2", ""},
		{"safe maar ouder", []Candidate{{"nc1", 1, pos(12, false)}, {"nc2", 2, pos(11, true)}}, "nc1", ""},
		{"onbekend", []Candidate{{"nc1", 1, pos(12, false)}, {"nc2", 2, pos(-1, false)}}, "", "de positie van nc2 is onbekend"},
		{"andere geschiedenis", []Candidate{{"nc1", 1, pos(12, false)}, {"nc2", 2, Position{UUID: "11111111-a8f1-11f0-9a3b-7e1c2d3f4a5b", Seqno: 3}}}, "",
			"nc1 en nc2 hebben een andere geschiedenis"},
		{"niemand", nil, "", "er is geen node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Choose(tc.cands)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("fout %v, verwacht %q", err, tc.err)
				}
				return
			}
			if err != nil || got.Name != tc.want {
				t.Fatalf("%+v %v, verwacht %s", got, err, tc.want)
			}
		})
	}
}

// TestBootstrap draait Bootstrap in een echte shell, met systemctl, sed en
// galera_new_cluster als scripts die alleen opschrijven dat ze draaiden.
func TestBootstrap(t *testing.T) {
	script := Bootstrap
	run := func(t *testing.T, tools map[string]int) (log, stderr string, code int) {
		t.Helper()
		dir := t.TempDir()
		logFile := filepath.Join(dir, "log")
		for name, exit := range tools {
			script := fmt.Sprintf("#!/bin/sh\necho \"${0##*/} $*\" >> '%s'\nexit %d\n", logFile, exit)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env = []string{"PATH=" + dir}
		var errb strings.Builder
		cmd.Stderr = &errb
		err := cmd.Run()
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			code = ee.ExitCode()
		case err != nil:
			t.Fatal(err)
		}
		b, _ := os.ReadFile(logFile)
		return string(b), errb.String(), code
	}
	const (
		stop  = "systemctl stop mariadb\n"
		sed   = "sed -i s/^safe_to_bootstrap:.*/safe_to_bootstrap: 1/ " + GrastateFile + "\n"
		unset = "systemctl unset-environment _WSREP_NEW_CLUSTER\nrm -f /run/mariadb-wsrep-new-cluster /run/mysqld/wsrep-new-cluster /run/mariadb/wsrep-new-cluster\n"
	)
	t.Run("zonder galera_new_cluster", func(t *testing.T) {
		log, stderr, code := run(t, map[string]int{"systemctl": 0, "sed": 0, "rm": 0})
		if code != 1 || log != "" || stderr != NoNewCluster+"\n" {
			t.Fatalf("code %d, stderr %q, log %q", code, stderr, log)
		}
	})
	t.Run("gelukt", func(t *testing.T) {
		log, _, code := run(t, map[string]int{"systemctl": 0, "sed": 0, "rm": 0, "galera_new_cluster": 0})
		if code != 0 || log != stop+sed+"galera_new_cluster \n"+unset {
			t.Fatalf("code %d, log %q", code, log)
		}
	})
	t.Run("galera_new_cluster mislukt", func(t *testing.T) {
		// De vlag gaat ook dan weg, en de fout blijft.
		log, _, code := run(t, map[string]int{"systemctl": 0, "sed": 0, "rm": 0, "galera_new_cluster": 3})
		if code != 3 || log != stop+sed+"galera_new_cluster \n"+unset {
			t.Fatalf("code %d, log %q", code, log)
		}
	})
	t.Run("sed mislukt", func(t *testing.T) {
		log, _, code := run(t, map[string]int{"systemctl": 0, "sed": 2, "rm": 0, "galera_new_cluster": 0})
		if code != 2 || log != stop+sed+unset {
			t.Fatalf("code %d, log %q", code, log)
		}
	})
	// BootstrapAny op een node zonder grastate.dat: niets veilig te
	// verklaren, galera_new_cluster begint een nieuwe geschiedenis.
	t.Run("BootstrapAny zonder grastate.dat", func(t *testing.T) {
		if _, err := os.Stat(GrastateFile); err == nil {
			t.Skip(GrastateFile + " staat op deze machine")
		}
		script = BootstrapAny
		defer func() { script = Bootstrap }()
		log, _, code := run(t, map[string]int{"systemctl": 0, "sed": 2, "rm": 0, "galera_new_cluster": 0})
		if code != 0 || log != stop+"galera_new_cluster \n"+unset {
			t.Fatalf("code %d, log %q", code, log)
		}
	})
	// Met grastate.dat moet de sed lukken, net als bij Bootstrap.
	t.Run("BootstrapAny met grastate.dat", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "grastate.dat")
		if err := os.WriteFile(f, []byte("safe_to_bootstrap: 0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		script = strings.ReplaceAll(BootstrapAny, GrastateFile, f)
		defer func() { script = Bootstrap }()
		sedF := strings.ReplaceAll(sed, GrastateFile, f)
		log, _, code := run(t, map[string]int{"systemctl": 0, "sed": 2, "rm": 0, "galera_new_cluster": 0})
		if code != 2 || log != stop+sedF+unset {
			t.Fatalf("sed mislukt: code %d, log %q", code, log)
		}
		log, _, code = run(t, map[string]int{"systemctl": 0, "sed": 0, "rm": 0, "galera_new_cluster": 0})
		if code != 0 || log != stop+sedF+"galera_new_cluster \n"+unset {
			t.Fatalf("gelukt: code %d, log %q", code, log)
		}
	})
	if !strings.Contains(BootstrapAny, SafeToBootstrap) || strings.Replace(BootstrapAny, "{ [ ! -e "+GrastateFile+" ] || "+SafeToBootstrap+"; }", SafeToBootstrap, 1) != Bootstrap {
		t.Errorf("BootstrapAny wijkt meer af van Bootstrap dan de controle op %s:\n%s", GrastateFile, BootstrapAny)
	}
}

func TestViaNote(t *testing.T) {
	for via, want := range map[string]string{
		"":                         "",
		"galera_recovery":          "",
		"mariadbd --wsrep-recover": " met mariadbd --wsrep-recover (galera_recovery ontbreekt)",
	} {
		if got := ViaNote(via); got != want {
			t.Errorf("%q: %q", via, got)
		}
	}
}

func TestPositionError(t *testing.T) {
	// Zoals een agent van voor de terugval het op 19 oktober zei.
	old := `galera_recovery: het programma om de database te lezen ontbreekt: exec: "galera_recovery": executable file not found in $PATH`
	if got := PositionError(old); !strings.HasPrefix(got, old+". Op deze node ontbreekt het pakket mariadb-server-galera") || !strings.Contains(got, "Agents bijwerken") {
		t.Errorf("oude agent: %q", got)
	}
	if got := PositionError("geen seqno"); got != "geen seqno" {
		t.Errorf("andere fout: %q", got)
	}
}

func TestNewClusterGone(t *testing.T) {
	for env, gone := range map[string]bool{
		"LANG=C.UTF-8\n_WSREP_NEW_CLUSTER=--wsrep-new-cluster\n": false,
		"_WSREP_NEW_CLUSTER=\n":                                  true,
		"LANG=C.UTF-8\n":                                         true,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\nprintf '"+strings.ReplaceAll(env, "\n", `\n`)+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("/bin/sh", "-c", NewClusterGone)
		cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
		if err := cmd.Run(); (err == nil) != gone {
			t.Errorf("%q: %v", env, err)
		}
	}
}
