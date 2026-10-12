package agent

import (
	"context"
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
		if p == nil || p.Seqno != 1234 || p.UUID != posUUID || p.From != protocol.DBFromRecovery || p.Error != "" || p.Active != "failed" {
			t.Fatalf("%+v", p)
		}
		if !recovered(h) {
			t.Fatal("geen galera_recovery")
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
