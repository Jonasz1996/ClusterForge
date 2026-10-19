package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// healHost maakt een node zoals DatabaseClusterNode1 op 18 oktober: de unit
// mariadb op failed, maar mariadbd (pid 169) draait nog en zoekt de andere
// nodes op hun oude adressen.
func healHost(t *testing.T, primary bool) (*Agent, string) {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("/etc/mysql/my.cnf", "[client-server]\n!includedir /etc/mysql/conf.d/\n!includedir /etc/mysql/mariadb.conf.d/\n")
	write("/etc/mysql/conf.d/mysql.cnf", "[mysql]\n")
	write("/etc/mysql/mariadb.conf.d/50-server.cnf", "[mysqld]\ndatadir = /var/lib/mysql\nbind-address = 0.0.0.0\n")
	write("/etc/mysql/mariadb.conf.d/60-galera.cnf", "[galera]\nwsrep_on = ON\nwsrep_provider = /usr/lib/galera/libgalera_smm.so\n"+
		"wsrep_cluster_name = \"db\"\nwsrep-cluster-address = gcomm://192.168.0.190,192.168.0.191,192.168.0.192 # oud\n"+
		"wsrep_node_address = 192.168.0.190\n")
	write("/var/lib/mysql/grastate.dat", "seqno: -1\n")
	write("/proc/uptime", "100000.00 1.00\n")
	// pid 169: mariadbd, sinds 99000 s na de boot.
	write("/proc/169/comm", "mariadbd\n")
	write("/proc/169/stat", "169 (mariadbd) S 1 169 169 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 7 0 9900000 0 0\n")
	write("/proc/169/cgroup", "0::/system.slice/mariadb.service\n")
	write("/proc/170/comm", "bash\n")
	a := &Agent{Root: root, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return time.Date(2026, 10, 18, 18, 0, 0, 0, time.UTC) }}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return []byte("Id=mariadb.service\nLoadState=loaded\nActiveState=failed\nSubState=failed\nResult=resources\nUnitFileState=disabled\nMainPID=0\n" +
				"StateChangeTimestampMonotonic=1000000000\n\nId=mariadb.service\nLoadState=loaded\nActiveState=failed\n\n" +
				"Id=postgresql.service\nLoadState=not-found\n\n"), nil
		case "my_print_defaults":
			// Zoals MariaDB: de bestanden op naam, de laatste waarde geldt.
			var out strings.Builder
			for _, f := range []string{"50-server.cnf", "60-galera.cnf"} {
				b, _ := os.ReadFile(filepath.Join(root, "/etc/mysql/mariadb.conf.d", f))
				for l := range strings.SplitSeq(string(b), "\n") {
					if k, v, ok := strings.Cut(l, "="); ok {
						fmt.Fprintf(&out, "--%s=%s\n", strings.TrimSpace(k), strings.TrimSpace(strings.Split(v, " #")[0]))
					}
				}
			}
			return []byte(out.String()), nil
		case "journalctl":
			return []byte("WSREP: (abc251de-a157, 'tcp://0.0.0.0:4567') connection to peer 00000000-0000 with addr tcp://192.168.0.191:4567 timed out, no messages seen in PT3S\n" +
				"WSREP: (abc251de-a157, 'tcp://0.0.0.0:4567') connection to peer 00000000-0000 with addr tcp://192.168.0.192:4567 timed out, no messages seen in PT3S\n" +
				"WSREP: Failed to establish connection: Operation aborted.\n"), nil
		case "mariadb":
			if primary {
				return []byte("wsrep_cluster_status\tPrimary\n"), nil
			}
			return []byte("ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"), errors.New("exit status 1")
		case "sh":
			// MariaDB 12.3.2 zonder mariadb-server-galera.
			if len(args) == 2 && args[1] == "command -v galera_recovery" {
				return nil, errors.New("exit status 127")
			}
		}
		return nil, fmt.Errorf("onbekend commando %s", name)
	}
	return a, root
}

func TestHealInspect(t *testing.T) {
	a, _ := healHost(t, false)
	res := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect})
	if res.Error != "" || res.Heal == nil || len(res.Heal.Engines) != 1 {
		t.Fatalf("antwoord: %+v", res)
	}
	e := res.Heal.Engines[0]
	if e.Engine != protocol.DBMariaDB || e.Unit != "mariadb" || e.Active != "failed" || e.Result != "resources" || e.Enabled != "disabled" || e.MainPID != 0 {
		t.Errorf("unit: %+v", e)
	}
	if e.StateSeconds != 99000 {
		t.Errorf("toestand sinds %d s, verwacht 99000", e.StateSeconds)
	}
	if len(e.Processes) != 1 || e.Processes[0].PID != 169 || e.Processes[0].Unit != "mariadb" || e.Processes[0].AgeSeconds != 1000 {
		t.Errorf("processen: %+v", e.Processes)
	}
	if e.DataDir != "/var/lib/mysql" || e.DataTotal == 0 {
		t.Errorf("datamap: %s %d/%d", e.DataDir, e.DataFree, e.DataTotal)
	}
	g := e.Galera
	if g == nil || !g.On || g.ClusterName != "db" || g.ClusterAddress != "gcomm://192.168.0.190,192.168.0.191,192.168.0.192" || g.NodeAddress != "192.168.0.190" {
		t.Fatalf("galera: %+v", g)
	}
	if strings.Join(g.PeerTimeouts, ",") != "192.168.0.191,192.168.0.192" {
		t.Errorf("peers: %v", g.PeerTimeouts)
	}
	if strings.Join(g.MissingTools, ",") != "galera_recovery" {
		t.Errorf("ontbrekende scripts: %v", g.MissingTools)
	}
	var keys []string
	for _, s := range g.Settings {
		keys = append(keys, fmt.Sprintf("%s@%s:%d", s.Key, filepath.Base(s.File), s.Line))
	}
	if strings.Join(keys, " ") != "wsrep_cluster_name@60-galera.cnf:4 wsrep_cluster_address@60-galera.cnf:5 wsrep_node_address@60-galera.cnf:6" {
		t.Errorf("regels: %v", keys)
	}
}

func TestHealInspectGaleraScripts(t *testing.T) {
	a, _ := healHost(t, false)
	exec := a.Exec
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "sh" && len(args) == 2 && args[1] == "command -v galera_recovery" {
			return []byte("/usr/bin/galera_recovery\n"), nil
		}
		return exec(ctx, name, args...)
	}
	res := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect})
	if res.Error != "" || res.Heal == nil || len(res.Heal.Engines) != 1 || res.Heal.Engines[0].Galera == nil {
		t.Fatalf("antwoord: %+v", res)
	}
	if m := res.Heal.Engines[0].Galera.MissingTools; m != nil {
		t.Errorf("galera_recovery staat er, toch ontbreekt %v", m)
	}
	if res.Heal.Engines[0].Galera.NewClusterFlag {
		t.Error("vlag van galera_new_cluster zonder dat hij staat")
	}
}

func TestHealInspectSST(t *testing.T) {
	a, root := healHost(t, false)
	if res := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect}); res.Heal.Engines[0].Galera.SST != "" {
		t.Fatalf("SST zonder script: %q", res.Heal.Engines[0].Galera.SST)
	}
	// Een script van een andere unit telt niet; dat van mariadb wel. De
	// scripts beginnen met #!/usr/bin/env bash, dus het proces heet bash.
	for pid, cg := range map[string]string{"171": "0::/user.slice/user-0.slice/session-1.scope\n", "172": "0::/system.slice/mariadb.service\n"} {
		writeFile(t, root, "/proc/"+pid+"/comm", "bash\n")
		writeFile(t, root, "/proc/"+pid+"/cmdline", "bash\x00/usr/bin/wsrep_sst_rsync\x00--role\x00joiner\x00--address\x00192.168.10.191\x00")
		writeFile(t, root, "/proc/"+pid+"/cgroup", cg)
	}
	// Een gewone bash in mariadb.service is geen SST.
	writeFile(t, root, "/proc/173/comm", "bash\n")
	writeFile(t, root, "/proc/173/cmdline", "bash\x00-c\x00wsrep_sst_rsync\x00")
	writeFile(t, root, "/proc/173/cgroup", "0::/system.slice/mariadb.service\n")
	res := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect})
	if g := res.Heal.Engines[0].Galera; g.SST != "wsrep_sst_rsync" {
		t.Fatalf("SST: %q", g.SST)
	}
	if err := os.Remove(filepath.Join(root, "/proc/172/cgroup")); err != nil {
		t.Fatal(err)
	}
	if g := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect}).Heal.Engines[0].Galera; g.SST != "" {
		t.Fatalf("SST buiten de unit: %q", g.SST)
	}
}

func TestSSTScript(t *testing.T) {
	for cmd, want := range map[string]string{
		"bash\x00/usr/bin/wsrep_sst_rsync\x00--role\x00joiner\x00": "wsrep_sst_rsync",
		"/bin/bash\x00-ue\x00/usr/bin/wsrep_sst_mariabackup\x00":   "",
		"/usr/bin/wsrep_sst_mariabackup\x00--role\x00donor\x00":    "wsrep_sst_mariabackup",
		"bash\x00-c\x00wsrep_sst_rsync\x00":                        "",
		"/usr/sbin/mariadbd\x00--wsrep_start_position=x:1\x00":     "",
		"": "",
	} {
		if got := sstScript(cmd); got != want {
			t.Errorf("%q: %q, verwacht %q", cmd, got, want)
		}
	}
}

func TestHealInspectNewClusterFlag(t *testing.T) {
	for env, want := range map[string]bool{
		"LANG=C.UTF-8\n_WSREP_NEW_CLUSTER=--wsrep-new-cluster\n": true,
		// Zoals galera_new_cluster hem achterlaat als het klaar is.
		"_WSREP_NEW_CLUSTER=\n":   false,
		"_WSREP_NEW_CLUSTER=''\n": false,
		"LANG=C.UTF-8\n":          false,
	} {
		a, _ := healHost(t, false)
		exec := a.Exec
		a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "systemctl" && len(args) == 1 && args[0] == "show-environment" {
				return []byte(env), nil
			}
			return exec(ctx, name, args...)
		}
		res := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect})
		if res.Heal == nil || len(res.Heal.Engines) != 1 || res.Heal.Engines[0].Galera == nil || res.Heal.Engines[0].Galera.NewClusterFlag != want {
			t.Errorf("%q: %+v", env, res)
		}
	}
	// Nieuwere versies van galera_new_cluster zetten hem in een bestand dat
	// de unit leest.
	for _, tc := range []struct {
		file, content string
		want          bool
	}{
		{"/run/mariadb-wsrep-new-cluster", "_WSREP_NEW_CLUSTER=--wsrep-new-cluster\n", true},
		{"/run/mysqld/wsrep-new-cluster", "_WSREP_NEW_CLUSTER='--wsrep-new-cluster'\n", true},
		{"/run/mariadb/wsrep-new-cluster", "--wsrep-new-cluster\n", true},
		{"/run/mariadb-wsrep-new-cluster", "", false},
	} {
		a, root := healHost(t, false)
		writeFile(t, root, tc.file, tc.content)
		res := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect})
		if res.Heal == nil || len(res.Heal.Engines) != 1 || res.Heal.Engines[0].Galera == nil || res.Heal.Engines[0].Galera.NewClusterFlag != tc.want {
			t.Errorf("%s %q: %+v", tc.file, tc.content, res)
		}
	}
}

func TestHealSetGaleraAddress(t *testing.T) {
	a, root := healHost(t, false)
	ctx := context.Background()
	for _, bad := range []protocol.DBHealRequest{
		{ClusterAddress: "192.168.10.181"},
		{ClusterAddress: "gcomm://a b"},
		{NodeAddress: "geen-ip"},
		{},
	} {
		bad.Action = protocol.HealSetGaleraAddress
		if res := a.dbHeal(ctx, &bad); res.Error == "" {
			t.Errorf("%+v werd aanvaard", bad)
		}
	}
	res := a.dbHeal(ctx, &protocol.DBHealRequest{
		Action: protocol.HealSetGaleraAddress, ClusterAddress: "gcomm://192.168.10.181,192.168.10.182,192.168.10.183", NodeAddress: "192.168.10.181",
	})
	if res.Error != "" || len(res.Heal.Changed) != 1 {
		t.Fatalf("antwoord: %+v", res)
	}
	c := res.Heal.Changed[0]
	if c.File != "/etc/mysql/mariadb.conf.d/60-galera.cnf" || !strings.HasPrefix(c.Backup, c.File+protocol.HealBackupSuffix+"20261018T180000Z") {
		t.Errorf("wijziging: %+v", c)
	}
	b, _ := os.ReadFile(filepath.Join(root, c.File))
	if !strings.Contains(string(b), "wsrep_cluster_address = gcomm://192.168.10.181,192.168.10.182,192.168.10.183\n") ||
		!strings.Contains(string(b), "wsrep_node_address = 192.168.10.181\n") || strings.Contains(string(b), "192.168.0.") {
		t.Errorf("bestand:\n%s", b)
	}
	old, _ := os.ReadFile(filepath.Join(root, c.Backup))
	if !strings.Contains(string(old), "192.168.0.191") {
		t.Errorf("kopie:\n%s", old)
	}
	if g := res.Heal.Engines[0].Galera; g.ClusterAddress != "gcomm://192.168.10.181,192.168.10.182,192.168.10.183" || g.NodeAddress != "192.168.10.181" {
		t.Errorf("na het wijzigen: %+v", g)
	}

}

func TestHealSetGaleraAddressAdds(t *testing.T) {
	a, root := healHost(t, false)
	f := filepath.Join(root, "/etc/mysql/mariadb.conf.d/60-galera.cnf")
	if err := os.WriteFile(f, []byte("[galera]\nwsrep_on = ON\nwsrep_cluster_address = gcomm://192.168.0.190\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealSetGaleraAddress, NodeAddress: "192.168.10.181"})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	b, _ := os.ReadFile(f)
	if string(b) != "[galera]\nwsrep_on = ON\nwsrep_cluster_address = gcomm://192.168.0.190\nwsrep_node_address = 192.168.10.181\n" {
		t.Errorf("bestand:\n%q", b)
	}
}
