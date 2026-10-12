package components

// Een test met echte etcd, Patroni en PostgreSQL op drie adressen van
// loopback (127.0.0.21 tot 127.0.0.23), met de configuratie die het
// onderdeel postgresql-patroni rendert. Hij laat zien wat de rest van
// ClusterForge aanneemt: de eerste node maakt het cluster, de andere worden
// replica, één replica is synchroon, een harde uitval van de primary geeft
// binnen de tijd een nieuwe zonder bevestigde rij te verliezen, de oude
// primary komt terug als replica, en na een uitval van alles komt het
// cluster vanzelf terug. cf-agent check db postgresql slaagt alleen op de
// primary.
//
// Hij draait alleen met CF_TEST_PATRONI (het pad van patroni), etcd in
// PATH of in CF_TEST_ETCD, en PostgreSQL in CF_TEST_PG_BIN (standaard de
// nieuwste in /usr/lib/postgresql), zoals in de CI. Wat voor één machine
// anders moet dan op drie nodes, past de test aan: elk adres een eigen
// poort en socket, en eigen mappen.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type realNode struct {
	name, ip, dir string
	etcd, patroni *exec.Cmd
}

type realPatroni struct {
	t       *testing.T
	patroni string
	etcd    string
	pgBin   string
	nodes   []*realNode
	// asPostgres: de test draait als root, en PostgreSQL weigert dat.
	asPostgres bool
}

func newRealPatroni(t *testing.T) *realPatroni {
	bin := os.Getenv("CF_TEST_PATRONI")
	if bin == "" {
		t.Skip("CF_TEST_PATRONI niet gezet")
	}
	r := &realPatroni{t: t, patroni: bin, etcd: os.Getenv("CF_TEST_ETCD"), pgBin: os.Getenv("CF_TEST_PG_BIN")}
	if r.etcd == "" {
		p, err := exec.LookPath("etcd")
		if err != nil {
			t.Fatal("etcd niet gevonden; zet CF_TEST_ETCD")
		}
		r.etcd = p
	}
	if r.pgBin == "" {
		dirs, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
		slices.SortFunc(dirs, func(a, b string) int { return pgMajor(a) - pgMajor(b) })
		if len(dirs) == 0 {
			t.Fatal("PostgreSQL niet gevonden; zet CF_TEST_PG_BIN")
		}
		r.pgBin = dirs[len(dirs)-1]
	}
	r.asPostgres = os.Geteuid() == 0
	// Als root draait PostgreSQL als postgres, dus de mappen moeten voor
	// hem te openen zijn; de tijdelijke map van de test is dat niet altijd.
	base := os.Getenv("CF_TEST_PATRONI_DIR")
	if base == "" {
		base = t.TempDir()
	}
	base, err := os.MkdirTemp(base, "patroni-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("CF_TEST_PATRONI_KEEP") != "" {
			t.Logf("logs en data staan nog in %s", base)
			return
		}
		_ = os.RemoveAll(base)
	})
	for i := 1; i <= 3; i++ {
		n := &realNode{name: fmt.Sprintf("pg%d", i), ip: fmt.Sprintf("127.0.0.%d", 20+i), dir: filepath.Join(base, fmt.Sprintf("pg%d", i))}
		for _, d := range []string{"etcd", "sock", "data"} {
			if err := os.MkdirAll(filepath.Join(n.dir, d), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		r.nodes = append(r.nodes, n)
	}
	if r.asPostgres {
		u, err := user.Lookup("postgres")
		if err != nil {
			t.Fatal("als root draait de test PostgreSQL als postgres, maar die gebruiker bestaat niet")
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		_ = os.Chmod(base, 0o755)
		_ = filepath.Walk(base, func(p string, _ os.FileInfo, _ error) error { return os.Chown(p, uid, gid) })
	}
	t.Cleanup(r.stopAll)
	return r
}

func pgMajor(dir string) int {
	n, _ := strconv.Atoi(filepath.Base(filepath.Dir(dir)))
	return n
}

// render geeft de configuratie van etcd en Patroni die het onderdeel voor
// een node maakt, aangepast aan één machine.
func (r *realPatroni) render(n *realNode) (etcdConf, patroniConf []byte, env []string) {
	t := r.t
	tpl, ok := registry.Latest("postgresql-patroni")
	if !ok {
		t.Fatal("postgresql-patroni ontbreekt")
	}
	params, err := tpl.Values(map[string]any{"shared_buffers": "128M", "max_connections": 50},
		map[string]string{"replication_password": "repl4Test", "rewind_password": "rewind4Test", "rest_password": "rest4Test"})
	if err != nil {
		t.Fatal(err)
	}
	// Een loopbackadres keurt de parameter af; hier is het VIP er een.
	params["vip"] = "127.0.0.20"
	c := templates.Context{Params: params, Cluster: templates.ClusterInfo{Name: "PG-test", Slug: "pgtest", Environment: "test"}}
	for i, x := range r.nodes {
		c.Nodes = append(c.Nodes, templates.NodeInfo{Hostname: x.name, Role: RoleData, Index: i + 1, Address: x.ip, Prefix: 8, Interface: "lo", OSVersion: "13"})
	}
	c.Node = &c.Nodes[slices.Index(r.nodes, n)]
	steps, err := tpl.Steps(RoleData, c)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, s := range steps {
		if s.File != nil {
			files[s.File.Path] = s.File.Content
		}
	}

	var e map[string]any
	if err := yaml.Unmarshal([]byte(files["/etc/etcd/clusterforge.yml"]), &e); err != nil {
		t.Fatalf("etcd.yml: %v\n%s", err, files["/etc/etcd/clusterforge.yml"])
	}
	// 127.0.0.1:2379 kan maar één etcd tegelijk hebben.
	e["data-dir"] = filepath.Join(n.dir, "etcd", "data")
	e["listen-client-urls"] = "http://" + n.ip + ":2379"
	etcdConf, _ = yaml.Marshal(e)

	var p map[string]any
	if err := yaml.Unmarshal([]byte(files["/etc/patroni/clusterforge.yml"]), &p); err != nil {
		t.Fatalf("patroni.yml: %v\n%s", err, files["/etc/patroni/clusterforge.yml"])
	}
	p["restapi"].(map[string]any)["listen"] = n.ip + ":8008"
	pg := p["postgresql"].(map[string]any)
	if !strings.HasSuffix(fmt.Sprint(pg["bin_dir"]), "/17/bin") || !strings.HasSuffix(fmt.Sprint(pg["data_dir"]), "/17/clusterforge") {
		t.Errorf("Debian 13 heeft PostgreSQL 17: %v %v", pg["bin_dir"], pg["data_dir"])
	}
	pg["listen"] = n.ip + ":5432"
	pg["data_dir"] = filepath.Join(n.dir, "data", "pg")
	pg["bin_dir"] = r.pgBin
	pg["pgpass"] = filepath.Join(n.dir, "pgpass")
	pg["parameters"].(map[string]any)["unix_socket_directories"] = filepath.Join(n.dir, "sock")
	// Op loopback komt elke verbinding van 127.0.0.1.
	hba := slices.Clone(pg["pg_hba"].([]any))
	if !slices.Contains(hba, any("host all all 127.0.0.20/32 scram-sha-256")) || !slices.Contains(hba, any("host replication cf_replicatie 127.0.0.22/32 scram-sha-256")) {
		t.Errorf("pg_hba zonder VIP of nodes: %v", hba)
	}
	if !r.asPostgres {
		// Zonder root draait PostgreSQL als de gebruiker van de test, zoals
		// runner in de CI; peer laat die via een map binnen als postgres.
		i := slices.Index(hba, any("local all all peer"))
		u, err := user.Current()
		if i < 0 || err != nil {
			t.Fatalf("pg_hba zonder peer via de socket: %v %v", hba, err)
		}
		hba[i] = "local all all peer map=test"
		pg["pg_ident"] = []any{"test " + u.Username + " postgres", "test " + u.Username + " " + u.Username}
	}
	pg["pg_hba"] = append(hba, "host replication cf_replicatie 127.0.0.1/32 scram-sha-256")
	patroniConf, _ = yaml.Marshal(p)
	// De wachtwoorden: systemd geeft ze als omgeving aan Patroni.
	for line := range strings.SplitSeq(files["/etc/patroni/clusterforge.env"], "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			env = append(env, line)
		}
	}
	if len(env) != 3 || strings.Contains(files["/etc/patroni/clusterforge.yml"], "repl4Test") {
		t.Fatalf("de wachtwoorden horen in clusterforge.env: %v", env)
	}
	return etcdConf, patroniConf, env
}

func (r *realPatroni) command(n *realNode, log string, name string, args ...string) *exec.Cmd {
	if r.asPostgres {
		args = append([]string{"-u", "postgres", "--", name}, args...)
		name = "runuser"
	}
	cmd := exec.Command(name, args...)
	// Als postgres mag Patroni niet in de map van de test.
	cmd.Dir = n.dir
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func (r *realPatroni) startEtcd(n *realNode) {
	conf, _, _ := r.render(n)
	p := filepath.Join(n.dir, "etcd.yml")
	if err := os.WriteFile(p, conf, 0o644); err != nil {
		r.t.Fatal(err)
	}
	n.etcd = r.command(n, filepath.Join(n.dir, "etcd.log"), r.etcd, "--config-file", p)
	if err := n.etcd.Start(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *realPatroni) startPatroni(n *realNode) {
	_, conf, env := r.render(n)
	p := filepath.Join(n.dir, "patroni.yml")
	if err := os.WriteFile(p, conf, 0o600); err != nil {
		r.t.Fatal(err)
	}
	if r.asPostgres {
		_ = exec.Command("chown", "postgres:postgres", p).Run()
	}
	// Zoals op een node: de agent leest het wachtwoord van de REST-API uit
	// /etc/patroni/clusterforge.env, hier onder de map van de node.
	envFile := filepath.Join(n.dir, "etc", "patroni", "clusterforge.env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte(strings.Join(env, "\n")+"\n"), 0o600); err != nil {
		r.t.Fatal(err)
	}
	n.patroni = r.command(n, filepath.Join(n.dir, "patroni.log"), "env", append(env, r.patroni, p)...)
	if err := n.patroni.Start(); err != nil {
		r.t.Fatal(err)
	}
}

// kill laat een node hard uitvallen: Patroni, PostgreSQL en met all ook
// etcd, alles met SIGKILL.
func (r *realPatroni) kill(n *realNode, all bool) {
	if n.patroni != nil && n.patroni.Process != nil {
		_ = syscall.Kill(-n.patroni.Process.Pid, syscall.SIGKILL)
		_ = n.patroni.Wait()
		n.patroni = nil
	}
	if b, err := os.ReadFile(filepath.Join(n.dir, "data", "pg", "postmaster.pid")); err == nil {
		// Het nummer kan intussen van een ander proces zijn; alleen een
		// postgres van deze node gaat eraan.
		pid, err := strconv.Atoi(strings.SplitN(string(b), "\n", 2)[0])
		if cmd, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && strings.Contains(string(cmd), n.dir) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	// De backends van PostgreSQL houden anders de poort nog even vast.
	_ = exec.Command("pkill", "-9", "-f", filepath.Join(n.dir, "data", "pg")).Run()
	if all && n.etcd != nil && n.etcd.Process != nil {
		_ = syscall.Kill(-n.etcd.Process.Pid, syscall.SIGKILL)
		_ = n.etcd.Wait()
		n.etcd = nil
	}
}

func (r *realPatroni) stopAll() {
	for _, n := range r.nodes {
		r.kill(n, true)
	}
	if r.t.Failed() {
		for _, n := range r.nodes {
			for _, f := range []string{"etcd.log", "patroni.log"} {
				b, _ := os.ReadFile(filepath.Join(n.dir, f))
				if len(b) > 6000 {
					b = append(append(b[:3000:3000], "\n…\n"...), b[len(b)-3000:]...)
				}
				r.t.Logf("%s %s:\n%s", n.name, f, b)
			}
		}
	}
}

func (r *realPatroni) agent(n *realNode) *agent.Agent {
	return &agent.Agent{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PatroniURL: "http://" + n.ip + ":8008", Root: n.dir}
}

// state vraagt /patroni van een node; leeg als hij niet antwoordt.
func (r *realPatroni) state(n *realNode) *protocol.DBPatroni {
	res := r.agent(n).PatroniStatus(context.Background())
	if res.Error != "" {
		return nil
	}
	return res
}

func (r *realPatroni) wait(what string, timeout time.Duration, ok func() bool) time.Duration {
	start := time.Now()
	for !ok() {
		if time.Since(start) > timeout {
			r.t.Fatalf("niet binnen %s: %s", timeout, what)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return time.Since(start)
}

// primary is de node die /primary met 200 beantwoordt, of nil.
func (r *realPatroni) primary() *realNode {
	for _, n := range r.nodes {
		resp, err := http.Get("http://" + n.ip + ":8008/primary") //nolint:noctx // test
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return n
		}
	}
	return nil
}

// psql voert sql uit op een node via zijn socket, als postgres.
func (r *realPatroni) psql(n *realNode, query string) (string, error) {
	args := []string{"-XAtq", "-h", filepath.Join(n.dir, "sock"), "-p", "5432", "-U", "postgres", "-d", "postgres", "-c", query}
	name := filepath.Join(r.pgBin, "psql")
	if r.asPostgres {
		args = append([]string{"-u", "postgres", "--", name}, args...)
		name = "runuser"
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestRealPatroni(t *testing.T) {
	r := newRealPatroni(t)
	pg1, pg2, pg3 := r.nodes[0], r.nodes[1], r.nodes[2]
	ctx := context.Background()

	// Zoals de uitrol: eerst etcd op alle nodes, dan Patroni node voor node.
	for _, n := range r.nodes {
		r.startEtcd(n)
	}
	r.startPatroni(pg1)
	r.wait("pg1 primary", 2*time.Minute, func() bool { return r.primary() == pg1 })
	if err := r.agent(pg1).CheckDB(ctx, protocol.DBPostgreSQL); err != nil {
		t.Fatalf("check db op de primary: %v", err)
	}
	r.startPatroni(pg2)
	r.startPatroni(pg3)
	for _, n := range []*realNode{pg2, pg3} {
		r.wait(n.name+" replica die bijhoudt", 2*time.Minute, func() bool {
			s := r.state(n)
			return s != nil && s.Role == "replica" && s.State == "running" && s.Replication == "streaming"
		})
		if err := r.agent(n).CheckDB(ctx, protocol.DBPostgreSQL); err == nil || !strings.Contains(err.Error(), "replica") {
			t.Errorf("check db op %s: %v", n.name, err)
		}
	}
	r.wait("een synchrone replica", time.Minute, func() bool {
		a, b := r.state(pg2), r.state(pg3)
		return a != nil && b != nil && a.Sync != b.Sync
	})
	if s := r.state(pg1); s.Scope != "pgtest" || s.Role != "primary" || s.Timeline != 1 {
		t.Errorf("pg1: %+v", s)
	}

	// Bevestigde rijen op de primary.
	if out, err := r.psql(pg1, "CREATE TABLE proef (id int PRIMARY KEY); INSERT INTO proef SELECT generate_series(1, 1000)"); err != nil {
		t.Fatalf("schrijven: %v %s", err, out)
	}

	// Harde uitval van de primary: binnen de ttl van 20 s een nieuwe, met
	// alle rijen. keepalived zet het VIP er daarna binnen een paar seconden
	// op, dus samen blijft het onder een halve minuut.
	r.kill(pg1, false)
	var next *realNode
	took := r.wait("een nieuwe primary", time.Minute, func() bool {
		next = r.primary()
		return next != nil && next != pg1
	})
	t.Logf("nieuwe primary %s na %s", next.name, took.Round(100*time.Millisecond))
	if took > 25*time.Second {
		t.Errorf("de nieuwe primary kwam pas na %s", took)
	}
	if out, err := r.psql(next, "SELECT count(*) FROM proef"); err != nil || out != "1000" {
		t.Errorf("rijen na de failover: %v %q", err, out)
	}

	// De oude primary komt terug als replica, met pg_rewind.
	r.startPatroni(pg1)
	r.wait("pg1 weer replica", 2*time.Minute, func() bool {
		s := r.state(pg1)
		return s != nil && s.Role == "replica" && s.State == "running" && s.Replication == "streaming"
	})
	if out, err := r.psql(next, "INSERT INTO proef VALUES (1001)"); err != nil {
		t.Fatalf("schrijven na de failover: %v %s", err, out)
	}

	// Alles tegelijk weg, ook etcd, zoals een stroomuitval: zodra de
	// meerderheid van etcd er is, kiest Patroni zelf een primary.
	for _, n := range r.nodes {
		r.kill(n, true)
	}
	for _, n := range r.nodes {
		r.startEtcd(n)
		r.startPatroni(n)
	}
	// Vlak na de start zegt Patroni soms al primary terwijl PostgreSQL nog
	// herstelt; keepalived wacht daarom op twee goede checks na elkaar.
	start := time.Now()
	for rows := ""; rows != "1001"; time.Sleep(250 * time.Millisecond) {
		if time.Since(start) > 3*time.Minute {
			t.Fatalf("na de uitval van alles geen primary met 1001 rijen; laatst %q", rows)
		}
		if next = r.primary(); next != nil {
			if out, err := r.psql(next, "SELECT count(*) FROM proef"); err == nil {
				rows = out
			}
		}
	}
	t.Logf("%s primary met alle rijen na de uitval van alles, na %s", next.name, time.Since(start).Round(100*time.Millisecond))
	for _, n := range r.nodes {
		if n == next {
			continue
		}
		r.wait(n.name+" weer replica na de uitval", 2*time.Minute, func() bool {
			s := r.state(n)
			return s != nil && s.Role == "replica" && s.State == "running"
		})
	}
	r.manage(next)
}

// op vraagt de agent op n iets aan Patroni, zoals de server dat doet.
func (r *realPatroni) op(n *realNode, req protocol.DBPatroniRequest) (*protocol.DBPatroniResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), protocol.DBPatroniTimeout)
	defer cancel()
	res := r.agent(n).PatroniOp(ctx, &req)
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return res.Patroni, nil
}

// replicaOK wacht tot n een replica is die bijhoudt.
func (r *realPatroni) replicaOK(n *realNode, what string) {
	r.wait(n.name+" replica die bijhoudt "+what, 3*time.Minute, func() bool {
		s := r.state(n)
		return s != nil && s.Role == "replica" && s.State == "running" && s.Replication == "streaming"
	})
}

// manage is het beheer uit mijlpaal 11 tegen echte Patroni: overschakelen,
// een replica opnieuw opbouwen, max_connections in etcd en herstarten waar
// dat nodig is. primary is de primary bij het begin.
func (r *realPatroni) manage(primary *realNode) {
	t := r.t
	var replicas []*realNode
	for _, n := range r.nodes {
		if n != primary {
			replicas = append(replicas, n)
			r.replicaOK(n, "voor het beheer")
		}
	}

	// Overschakelen: Patroni antwoordt als de nieuwe primary er is; de oude
	// wordt replica zonder dat er een bevestigde rij verloren gaat. In
	// synchronous_mode kan alleen de synchrone replica primary worden.
	var to, other *realNode
	r.wait("een synchrone replica voor de switchover", time.Minute, func() bool {
		for i, n := range replicas {
			if s := r.state(n); s != nil && s.Sync {
				to, other = n, replicas[1-i]
				return true
			}
		}
		return false
	})
	if _, err := r.op(to, protocol.DBPatroniRequest{Action: protocol.DBPatroniSwitchover, Leader: primary.name, Candidate: other.name}); err == nil ||
		!strings.Contains(err.Error(), "sync_standby") {
		t.Errorf("switchover naar de replica die niet synchroon is: %v", err)
	}
	res, err := r.op(other, protocol.DBPatroniRequest{Action: protocol.DBPatroniSwitchover, Leader: primary.name, Candidate: to.name})
	if err != nil || !res.Changed {
		t.Fatalf("switchover van %s naar %s: %v %+v", primary.name, to.name, err, res)
	}
	t.Logf("switchover: %s", res.Message)
	r.wait(to.name+" primary na de switchover", time.Minute, func() bool { return r.primary() == to })
	r.replicaOK(primary, "na de switchover")
	if out, err := r.psql(to, "INSERT INTO proef VALUES (1002); SELECT count(*) FROM proef"); err != nil || out != "1002" {
		t.Fatalf("schrijven na de switchover: %v %q", err, out)
	}
	if _, err := r.op(to, protocol.DBPatroniRequest{Action: protocol.DBPatroniSwitchover, Leader: primary.name, Candidate: to.name}); err == nil {
		t.Errorf("een switchover van een node die geen primary meer is, slaagde")
	}

	// De oude primary opnieuw opbouwen: de data weg en alles opnieuw van de
	// primary. Op de primary weigert Patroni het.
	if _, err := r.op(to, protocol.DBPatroniRequest{Action: protocol.DBPatroniReinit}); err == nil {
		t.Errorf("reinitialize op de primary slaagde")
	}
	if res, err := r.op(primary, protocol.DBPatroniRequest{Action: protocol.DBPatroniReinit}); err != nil || !res.Changed {
		t.Fatalf("reinitialize op %s: %v %+v", primary.name, err, res)
	}
	r.wait(primary.name+" opnieuw opgebouwd met alle rijen", 3*time.Minute, func() bool {
		s := r.state(primary)
		if s == nil || s.Role != "replica" || s.State != "running" || s.Replication != "streaming" {
			return false
		}
		out, err := r.psql(primary, "SELECT count(*) FROM proef")
		return err == nil && out == "1002"
	})

	// max_connections in etcd: daarna wil elke node een herstart.
	res, err = r.op(primary, protocol.DBPatroniRequest{Action: protocol.DBPatroniConfig, Parameters: map[string]string{"max_connections": "60"}})
	if err != nil || !res.Changed || res.Parameters["max_connections"] != "60" {
		t.Fatalf("config: %v %+v", err, res)
	}
	// Een andere node ziet het pas na zijn volgende ronde in etcd.
	r.wait(to.name+" leest max_connections 60", 30*time.Second, func() bool {
		res, err := r.op(to, protocol.DBPatroniRequest{Action: protocol.DBPatroniConfig})
		return err == nil && !res.Changed && res.Parameters["max_connections"] == "60"
	})
	if res, err := r.op(to, protocol.DBPatroniRequest{Action: protocol.DBPatroniConfig, Parameters: map[string]string{"max_connections": "60"}}); err != nil || res.Changed {
		t.Errorf("config opnieuw: %v %+v", err, res)
	}
	if _, err := r.op(to, protocol.DBPatroniRequest{Action: protocol.DBPatroniConfig, Parameters: map[string]string{"shared_buffers": "1GB"}}); err == nil {
		t.Errorf("config met shared_buffers slaagde")
	}
	for _, n := range r.nodes {
		r.wait(n.name+" wil een herstart", time.Minute, func() bool {
			s := r.state(n)
			return s != nil && s.PendingRestart
		})
	}
	// Herstarten: de replica's eerst, de primary als laatste.
	for _, n := range append(slices.DeleteFunc(slices.Clone(r.nodes), func(x *realNode) bool { return x == to }), to) {
		res, err := r.op(n, protocol.DBPatroniRequest{Action: protocol.DBPatroniRestart, Pending: true})
		if err != nil || !res.Changed {
			t.Fatalf("herstart van %s: %v %+v", n.name, err, res)
		}
		r.wait(n.name+" draait met max_connections 60", 2*time.Minute, func() bool {
			s := r.state(n)
			out, err := r.psql(n, "SHOW max_connections")
			return s != nil && s.State == "running" && !s.PendingRestart && err == nil && out == "60"
		})
		if res, err := r.op(n, protocol.DBPatroniRequest{Action: protocol.DBPatroniRestart, Pending: true}); err != nil || res.Changed {
			t.Errorf("tweede herstart van %s: %v %+v", n.name, err, res)
		}
	}
	if r.primary() != to {
		t.Errorf("de primary veranderde bij het herstarten: %v", r.primary())
	}
	if s := r.state(primary); s == nil || s.LagBytes == nil {
		t.Errorf("geen lag van replica %s: %+v", primary.name, s)
	}

	// Met een verkeerd wachtwoord weigert Patroni.
	envFile := filepath.Join(primary.dir, "etc", "patroni", "clusterforge.env")
	good, _ := os.ReadFile(envFile)
	_ = os.WriteFile(envFile, []byte("PATRONI_RESTAPI_PASSWORD=fout\n"), 0o600)
	if _, err := r.op(primary, protocol.DBPatroniRequest{Action: protocol.DBPatroniReinit}); err == nil || !strings.Contains(err.Error(), "weigert het wachtwoord") {
		t.Errorf("verkeerd wachtwoord: %v", err)
	}
	_ = os.WriteFile(envFile, good, 0o600)
}
