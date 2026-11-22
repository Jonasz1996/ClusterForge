package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// patroniHost is een PostgreSQL-node met Patroni na een nieuwe
// netwerkindeling: de config noemt etcd en zichzelf nog op de oude
// adressen, alleen etcd op 192.168.10.181 antwoordt.
func patroniHost(t *testing.T, cfg string) *Agent {
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
	write("/etc/patroni/pg1.yml", cfg)
	write("/proc/uptime", "5000.00 1.00\n")
	a := &Agent{Root: root, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" || len(args) < 2 {
			return nil, fmt.Errorf("onbekend commando %s", name)
		}
		if strings.Contains(args[1], "ExecStart") {
			return []byte("Id=patroni.service\nLoadState=loaded\nActiveState=active\n" +
				"ExecStart={ path=/usr/bin/patroni ; argv[]=/usr/bin/patroni ${PATRONI_CONFIG} ; ignore_errors=no ; start_time=[n/a] ; pid=0 }\n" +
				"Environment=LANG=C PATRONI_CONFIG=/etc/patroni/pg1.yml\n\n" +
				"Id=etcd.service\nLoadState=loaded\nActiveState=failed\nExecStart=\nEnvironment=\n"), nil
		}
		return []byte("Id=patroni.service\nLoadState=loaded\nActiveState=active\nSubState=running\nResult=success\nUnitFileState=enabled\nMainPID=812\n" +
			"StateChangeTimestampMonotonic=4000000000\n\nId=postgresql.service\nLoadState=loaded\nActiveState=active\n\n"), nil
	}
	a.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		switch addr {
		case "192.168.10.181:2379":
			c, s := net.Pipe()
			_ = s.Close()
			return c, nil
		case "192.168.0.182:2379":
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		return nil, fmt.Errorf("dial tcp %s: %w", addr, context.DeadlineExceeded)
	}
	return a
}

const patroniYAML = `scope: pgcluster
name: pg1

restapi:
  listen: 0.0.0.0:8008
  connect_address: 192.168.0.181:8008

etcd3:
  hosts:
    - 192.168.10.181:2379
    - 192.168.0.182:2379
    - 192.168.0.183

postgresql:
  listen: "*:5432"
  connect_address: 192.168.0.181:5432
  data_dir: /var/lib/postgresql/16/main
`

func TestHealInspectPatroni(t *testing.T) {
	a := patroniHost(t, patroniYAML)
	res := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect})
	if res.Error != "" || res.Heal == nil || len(res.Heal.Engines) != 1 {
		t.Fatalf("antwoord: %+v", res)
	}
	e := res.Heal.Engines[0]
	if e.Engine != protocol.DBPostgreSQL || e.Unit != "patroni" || e.DataDir != "/var/lib/postgresql/16/main" {
		t.Errorf("engine: %+v", e)
	}
	p := e.Patroni
	if p == nil || p.Error != "" || p.File != "/etc/patroni/pg1.yml" || p.Scope != "pgcluster" || p.Name != "pg1" || p.DCS != "etcd3" || p.Etcd != "failed" {
		t.Fatalf("patroni: %+v", p)
	}
	var lines []string
	for _, s := range p.Settings {
		lines = append(lines, fmt.Sprintf("%s=%s@%d", s.Key, s.Value, s.Line))
	}
	if got := strings.Join(lines, " "); got != "etcd3.hosts=192.168.10.181:2379@10 etcd3.hosts=192.168.0.182:2379@11 etcd3.hosts=192.168.0.183:2379@12 "+
		"restapi.connect_address=192.168.0.181:8008@6 postgresql.connect_address=192.168.0.181:5432@16" {
		t.Errorf("regels: %s", got)
	}
	var eps []string
	for _, ep := range p.Endpoints {
		eps = append(eps, fmt.Sprintf("%s:%v:%s", ep.Address, ep.OK, ep.Error))
	}
	if got := strings.Join(eps, " | "); got != "192.168.10.181:2379:true: | 192.168.0.182:2379:false:verbinding geweigerd; daar luistert niets | "+
		"192.168.0.183:2379:false:geen antwoord binnen 2 s" {
		t.Errorf("etcd: %s", got)
	}
}

func TestHealInspectPatroniForms(t *testing.T) {
	for _, tc := range []struct{ cfg, want string }{
		{"etcd:\n  host: 10.0.0.1:2379\n", "etcd.host=10.0.0.1:2379@2"},
		{"etcd3:\n  hosts: 10.0.0.1:2379,10.0.0.2\n", "etcd3.hosts=10.0.0.1:2379@2 etcd3.hosts=10.0.0.2:2379@2"},
		{"etcd3:\n  url: https://[fd00::1]:2379\n", "etcd3.url=[fd00::1]:2379@2"},
		{"etcd3:\n  proxy: http://etcd.lan\n  hosts: 10.0.0.1\n", "etcd3.proxy=etcd.lan:2379@2"},
		// Zoals Patroni: IPv6 zonder haken met de poort na de laatste
		// dubbele punt, port als standaard, en hosts gaat voor host.
		{"etcd3:\n  port: 2380\n  host: 127.0.0.1\n  hosts:\n  - fd00::1:2379\n  - 10.0.0.2\n", "etcd3.hosts=[fd00::1]:2379@5 etcd3.hosts=10.0.0.2:2380@6"},
		// Een sleutel die twee keer staat: de laatste geldt.
		{"etcd3:\n  hosts: 10.0.0.1\n  hosts: 10.0.0.9\n", "etcd3.hosts=10.0.0.9:2379@3"},
		{"consul:\n  host: 10.0.0.1:8500\n", ""},
	} {
		a := patroniHost(t, tc.cfg)
		e := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect}).Heal.Engines[0]
		var lines []string
		for _, s := range e.Patroni.Settings {
			lines = append(lines, fmt.Sprintf("%s=%s@%d", s.Key, s.Value, s.Line))
		}
		if got := strings.Join(lines, " "); got != tc.want {
			t.Errorf("%q: %s, verwacht %s", tc.cfg, got, tc.want)
		}
	}
	a := patroniHost(t, "etcd3:\n  srv: etcd.lan\n")
	if e := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect}).Heal.Engines[0]; !strings.Contains(e.Patroni.Error, "DNS") {
		t.Errorf("srv: %+v", e.Patroni)
	}
}

func TestHealInspectPatroniDir(t *testing.T) {
	// Een map als config, met een schuine streep erachter in ExecStart: de
	// bestanden op naam, een later bestand vervangt een waarde.
	a := patroniHost(t, "")
	for f, s := range map[string]string{
		"/etc/patroni/conf.d/00-base.yml":  "scope: pg\netcd3:\n  hosts: 10.0.0.1:2379\nrestapi:\n  connect_address: 10.0.0.5:8008\n",
		"/etc/patroni/conf.d/10-local.yml": "name: pg1\netcd3:\n  hosts: 10.0.0.2:2379\n",
		"/etc/patroni/conf.d/README":       "geen yaml\n",
	} {
		p := filepath.Join(a.Root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exec := a.Exec
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 1 && strings.Contains(args[1], "ExecStart") {
			return []byte("Id=patroni.service\nLoadState=loaded\nActiveState=active\nExecStart={ path=/usr/bin/patroni ; argv[]=/usr/bin/patroni /etc/patroni/conf.d/ ; ignore_errors=no }\n\n" +
				"Id=etcd.service\nLoadState=not-found\nActiveState=inactive\n"), nil
		}
		return exec(ctx, name, args...)
	}
	p := a.dbHeal(context.Background(), &protocol.DBHealRequest{Action: protocol.HealInspect}).Heal.Engines[0].Patroni
	var lines []string
	for _, s := range p.Settings {
		lines = append(lines, fmt.Sprintf("%s=%s@%s:%d", s.Key, s.Value, filepath.Base(s.File), s.Line))
	}
	if p.File != "/etc/patroni/conf.d" || p.Scope != "pg" || p.Name != "pg1" || p.Etcd != "" ||
		strings.Join(lines, " ") != "etcd3.hosts=10.0.0.2:2379@10-local.yml:3 restapi.connect_address=10.0.0.5:8008@00-base.yml:5" {
		t.Errorf("patroni: %+v %v", p, lines)
	}
}
