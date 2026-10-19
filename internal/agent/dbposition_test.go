package agent

import (
	"context"
	"errors"
	osexec "os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/galera"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const posUUID = "5c3a1e0e-0000-11f0-9a3b-7e1c2d3f4a5b"

func grastate(seqno string, safe bool) string {
	s := "0"
	if safe {
		s = "1"
	}
	return "# GALERA saved state\nversion: 2.1\nuuid:    " + posUUID + "\nseqno:   " + seqno + "\nsafe_to_bootstrap: " + s + "\n"
}

func TestRecoverFailure(t *testing.T) {
	for _, c := range []struct {
		out  string
		err  error
		want string
	}{
		{"[Note] a\n[ERROR] b\n[ERROR] c\n[Note] d\n", nil, "[ERROR] b"},
		{"[Note] a\n[Note] d\n\n", nil, "[Note] d"},
		{"", errExit1Test, "exit status 1"},
		{"", nil, "lege uitvoer"},
		{strings.Repeat("x", 400), nil, strings.Repeat("x", 300) + "…"},
	} {
		if got := recoverFailure([]byte(c.out), c.err); got != c.want {
			t.Errorf("%q: %q, verwacht %q", c.out, got, c.want)
		}
	}
}

var errExit1Test = errors.New("exit status 1")

func TestDBPosition(t *testing.T) {
	ctx := context.Background()
	stopped := func(t *testing.T, unit agenttest.Unit, state string) (*Agent, *agenttest.Host) {
		root := t.TempDir()
		h := agenttest.NewHost(root, "10.0.20.186")
		h.SetUnit("mariadb", unit)
		h.MariaDB = &agenttest.MariaDB{Version: "11.8.3-MariaDB-0+deb13u1", Down: true, Recovered: posUUID + ":1234"}
		if state != "" {
			writeFile(t, root, galera.GrastateFile, state)
		}
		a := dbAgent(h)
		a.Root = root
		return a, h
	}
	recovered := func(h *agenttest.Host) bool { return slices.Contains(h.Calls(), "galera_recovery") }

	t.Run("draait", func(t *testing.T) {
		h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
		h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
		h.MariaDB = &agenttest.MariaDB{Version: "11.8.3-MariaDB-0+deb13u1", Galera: &agenttest.Galera{
			ClusterName: "nextcloud", UUID: posUUID, Status: "Non-Primary", Size: 1, State: 0, Comment: "Initialized", Seqno: 1300,
		}}
		res := dbAgent(h).dbPosition(ctx, &protocol.DBPositionRequest{Recover: true})
		p := res.Position
		if res.Error != "" || p == nil || !p.Running || p.From != protocol.DBFromRunning || p.UUID != posUUID || p.Seqno != 1300 ||
			p.Galera == nil || p.Galera.ClusterName != "nextcloud" || p.Galera.ClusterStatus != "Non-Primary" || p.Unit != "mariadb" || p.Active != "active" {
			t.Fatalf("%+v %+v", res, p)
		}
		if recovered(h) {
			t.Fatal("galera_recovery terwijl MariaDB draait")
		}
	})
	t.Run("zonder Galera", func(t *testing.T) {
		h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
		h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
		h.MariaDB = &agenttest.MariaDB{Version: "11.8.3-MariaDB-0+deb13u1"}
		res := dbAgent(h).dbPosition(ctx, nil)
		if p := res.Position; p == nil || !p.Running || p.Error != "MariaDB draait zonder Galera" || p.Seqno != -1 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("gewone stop", func(t *testing.T) {
		a, h := stopped(t, agenttest.Unit{Enabled: true}, grastate("1250", true))
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Running || p.From != protocol.DBFromGrastate || p.Seqno != 1250 || !p.SafeToBootstrap || p.UUID != posUUID || p.Error != "" || p.Active != "inactive" {
			t.Fatalf("%+v", p)
		}
		if recovered(h) {
			t.Fatal("galera_recovery terwijl grastate.dat de positie heeft")
		}
	})
	t.Run("crash zonder recover", func(t *testing.T) {
		a, h := stopped(t, agenttest.Unit{Enabled: true, Failed: true}, grastate("-1", false))
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{}).Position
		if p == nil || p.Seqno != -1 || p.UUID != posUUID || p.From != protocol.DBFromGrastate || !strings.Contains(p.Error, "alleen nog in InnoDB") {
			t.Fatalf("%+v", p)
		}
		if recovered(h) {
			t.Fatal("galera_recovery zonder Recover")
		}
	})
	t.Run("crash", func(t *testing.T) {
		a, h := stopped(t, agenttest.Unit{Enabled: true, Failed: true}, grastate("-1", false))
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != 1234 || p.UUID != posUUID || p.From != protocol.DBFromRecovery || p.Via != "galera_recovery" || p.Error != "" || p.Active != "failed" {
			t.Fatalf("%+v", p)
		}
		if !recovered(h) {
			t.Fatal("geen galera_recovery")
		}
		if slices.ContainsFunc(h.Calls(), func(c string) bool { return strings.HasPrefix(c, "mariadbd") }) {
			t.Fatal("mariadbd --wsrep-recover terwijl galera_recovery er is")
		}
	})
	t.Run("start nog", func(t *testing.T) {
		a, h := stopped(t, agenttest.Unit{Enabled: true, Activating: true}, grastate("-1", false))
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != -1 || p.Active != "activating" || !strings.Contains(p.Error, "MariaDB is activating") {
			t.Fatalf("%+v", p)
		}
		if recovered(h) {
			t.Fatal("galera_recovery terwijl MariaDB start")
		}
	})
	t.Run("recovery mislukt", func(t *testing.T) {
		a, h := stopped(t, agenttest.Unit{Enabled: true}, grastate("-1", false))
		h.MariaDB.Recovered = ""
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != -1 || !strings.HasPrefix(p.Error, "galera_recovery: ") || !strings.Contains(p.Error, "InnoDB has no position") {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("crash zonder galera_recovery", func(t *testing.T) {
		// MariaDB 12.3.2 zonder mariadb-server-galera, zoals
		// DatabaseClusterNode1 op 19 oktober.
		a, h := stopped(t, agenttest.Unit{Enabled: true, Failed: true}, grastate("-1", false))
		h.MariaDB.NoGaleraScripts = true
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != 1234 || p.UUID != posUUID || p.From != protocol.DBFromRecovery || p.Via != "mariadbd --wsrep-recover" || p.Error != "" {
			t.Fatalf("%+v", p)
		}
		if !slices.Contains(h.Calls(), "mariadbd --user=mysql --wsrep-recover --disable-log-error") {
			t.Fatalf("geen mariadbd --wsrep-recover: %v", h.Calls())
		}
	})
	t.Run("crash zonder galera_recovery, gebruiker uit de config", func(t *testing.T) {
		a, h := stopped(t, agenttest.Unit{Enabled: true}, grastate("-1", false))
		h.MariaDB.NoGaleraScripts = true
		writeFile(t, a.Root, "/etc/mysql/mariadb.conf.d/50-server.cnf", "[mysqld]\nuser = mariadb\n")
		if p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position; p == nil || p.Seqno != 1234 {
			t.Fatalf("%+v", p)
		}
		if !slices.Contains(h.Calls(), "mariadbd --user=mariadb --wsrep-recover --disable-log-error") {
			t.Fatalf("niet als de gebruiker uit de config: %v", h.Calls())
		}
	})
	t.Run("zonder galera_recovery geen positie", func(t *testing.T) {
		a, h := stopped(t, agenttest.Unit{Enabled: true}, grastate("-1", false))
		h.MariaDB.NoGaleraScripts, h.MariaDB.Recovered = true, ""
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != -1 || p.Via != "mariadbd --wsrep-recover" ||
			p.Error != "galera_recovery ontbreekt op deze node (sinds MariaDB 12.3.2 zit het in het pakket mariadb-server-galera), en mariadbd --wsrep-recover gaf geen positie: "+
				"2026-10-19 18:20:01 0 [Note] Server socket created on IP: '0.0.0.0'." {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("zonder galera_recovery mislukt", func(t *testing.T) {
		// systemd zegt failed, maar een mariadbd buiten de unit houdt de
		// datamap nog vast.
		a, h := stopped(t, agenttest.Unit{Enabled: true, Failed: true}, grastate("-1", false))
		h.MariaDB.NoGaleraScripts = true
		h.Fail("mariadbd", "2026-10-19 18:20:01 0 [Note] Starting MariaDB 12.3.2-MariaDB-deb13 source revision as process 4242\n"+
			"2026-10-19 18:20:01 0 [ERROR] mariadbd: Can't lock aria control file '/var/lib/mysql/aria_log_control' for exclusive use, error: 11. Will retry for 30 seconds\n"+
			"2026-10-19 18:20:31 0 [ERROR] Aborting\n")
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != -1 || p.Error != "galera_recovery ontbreekt op deze node (sinds MariaDB 12.3.2 zit het in het pakket mariadb-server-galera), "+
			"en mariadbd --wsrep-recover lukte niet: 2026-10-19 18:20:01 0 [ERROR] mariadbd: Can't lock aria control file "+
			"'/var/lib/mysql/aria_log_control' for exclusive use, error: 11. Will retry for 30 seconds" {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("zonder galera_recovery te lang", func(t *testing.T) {
		// De tijdslimiet loopt af terwijl InnoDB nog herstelt; exec doodt
		// mariadbd.
		a, h := stopped(t, agenttest.Unit{Enabled: true}, grastate("-1", false))
		h.MariaDB.NoGaleraScripts = true
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		exec := a.Exec
		a.Exec = func(c context.Context, name string, args ...string) ([]byte, error) {
			if name == "mariadbd" {
				cancel()
				return []byte("2026-10-19 18:20:01 0 [Note] InnoDB: To recover: 183456 pages\n"), errors.New("signal: killed")
			}
			return exec(c, name, args...)
		}
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != -1 || p.Via != "mariadbd --wsrep-recover" ||
			!strings.Contains(p.Error, "en de agent brak mariadbd --wsrep-recover af na de tijdslimiet (10m0s); InnoDB was nog bezig met herstellen") ||
			!strings.HasSuffix(p.Error, "Laatste regel: 2026-10-19 18:20:01 0 [Note] InnoDB: To recover: 183456 pages") {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("zonder galera_recovery en mariadbd", func(t *testing.T) {
		a, h := stopped(t, agenttest.Unit{Enabled: true}, grastate("-1", false))
		h.MariaDB.NoGaleraScripts = true
		exec := a.Exec
		a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "mariadbd" || name == "mysqld" {
				return nil, &osexec.Error{Name: name, Err: osexec.ErrNotFound}
			}
			return exec(ctx, name, args...)
		}
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != -1 || p.Via != "" || !strings.HasSuffix(p.Error, ", en mariadbd ook") {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("galera_recovery mislukt anders", func(t *testing.T) {
		// Een andere fout dan "ontbreekt": geen terugval, want het script
		// deed al wat mariadbd zou doen.
		a, h := stopped(t, agenttest.Unit{Enabled: true}, grastate("-1", false))
		h.Fail("galera_recovery", "WSREP: Failed to start mysqld for wsrep recovery: '[ERROR] InnoDB: Plugin initialization aborted'\n")
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != -1 || p.Via != "galera_recovery" || !strings.HasPrefix(p.Error, "galera_recovery: exit status 1: WSREP: Failed to start mysqld") {
			t.Fatalf("%+v", p)
		}
		if slices.ContainsFunc(h.Calls(), func(c string) bool { return strings.HasPrefix(c, "mariadbd") }) {
			t.Fatal("mariadbd na een andere fout van galera_recovery")
		}
	})
	t.Run("nooit in een cluster", func(t *testing.T) {
		a, _ := stopped(t, agenttest.Unit{Enabled: true}, "")
		p := a.dbPosition(ctx, &protocol.DBPositionRequest{Recover: true}).Position
		if p == nil || p.Seqno != -1 || !strings.Contains(p.Error, "nog nooit in een Galera-cluster") {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("geen MariaDB", func(t *testing.T) {
		h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
		res := dbAgent(h).dbPosition(ctx, nil)
		if res.Error != "op deze node staat geen MariaDB" || res.Position != nil {
			t.Fatalf("%+v", res)
		}
	})
}
