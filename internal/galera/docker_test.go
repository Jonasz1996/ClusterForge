package galera

// Een test met drie echte MariaDB-containers in een Galera-cluster. Hij
// laat zien wat de rest van ClusterForge aanneemt: welke node na een
// volledige uitval de nieuwste data heeft, dat beginnen op die node geen
// transactie kwijtraakt, en dat Galera na een stroomuitval van alle nodes
// zelf terugkomt. Hij draait alleen met CF_TEST_GALERA_DOCKER=1 en docker,
// zoals in de CI.
//
// Een container start mariadbd zoals de unit van Debian dat doet: heeft
// grastate.dat geen seqno, dan haalt galera_recovery eerst de positie uit
// InnoDB en krijgt mariadbd die als --wsrep_start_position.
//
// TestRealGaleraJoin voegt een vierde node toe zoals Node toevoegen: alleen
// de nieuwe node kent alle vier de adressen, de andere draaien door met hun
// oude configuratie, en de nieuwe haalt alle data met mariabackup terwijl
// er geschreven wordt.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
)

const galeraImage = "mariadb:11.8"

type dockerGalera struct {
	t      *testing.T
	prefix string
	conf   string
	names  []string
	// confs is de configuratie van een node die een eigen heeft.
	confs map[int]string
}

func newDockerGalera(t *testing.T) *dockerGalera {
	if os.Getenv("CF_TEST_GALERA_DOCKER") == "" {
		t.Skip("CF_TEST_GALERA_DOCKER niet gezet")
	}
	g := &dockerGalera{t: t, prefix: fmt.Sprintf("cfgalera%d", os.Getpid())}
	for i := 1; i <= 3; i++ {
		g.names = append(g.names, fmt.Sprintf("%s-%d", g.prefix, i))
	}
	g.conf = g.writeConf("galera.cnf")
	g.cleanup()
	t.Cleanup(g.cleanup)
	g.docker("network", "create", g.prefix)
	return g
}

// writeConf schrijft de instellingen van 99-clusterforge-galera.cnf met de
// nodes die er nu zijn.
func (g *dockerGalera) writeConf(name string) string {
	g.t.Helper()
	p := filepath.Join(g.t.TempDir(), name)
	conf := "[mysqld]\nbinlog_format = ROW\ndefault_storage_engine = InnoDB\ninnodb_autoinc_lock_mode = 2\nbind-address = 0.0.0.0\n\n" +
		"[galera]\nwsrep_on = ON\nwsrep_provider = /usr/lib/galera/libgalera_smm.so\nwsrep_cluster_name = cftest\n" +
		"wsrep_cluster_address = gcomm://" + strings.Join(g.names, ",") + "\nwsrep_sst_method = mariabackup\nwsrep_sst_auth = mysql:\n"
	if err := os.WriteFile(p, []byte(conf), 0o644); err != nil {
		g.t.Fatal(err)
	}
	return p
}

// confOf is de configuratie van node i.
func (g *dockerGalera) confOf(i int) string {
	if c, ok := g.confs[i]; ok {
		return c
	}
	return g.conf
}

func (g *dockerGalera) cleanup() {
	_ = exec.Command("docker", append([]string{"rm", "-f"}, g.names...)...).Run()
	_ = exec.Command("docker", append([]string{"volume", "rm", "-f"}, g.names...)...).Run()
	_ = exec.Command("docker", "network", "rm", g.prefix).Run()
}

func (g *dockerGalera) docker(args ...string) string {
	g.t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		g.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// onVolume draait een commando in een tijdelijke container op de datamap
// van node i, terwijl de node zelf stilstaat.
func (g *dockerGalera) onVolume(i int, cmd ...string) string {
	g.t.Helper()
	return g.docker(append([]string{"run", "--rm", "-v", g.names[i] + ":/var/lib/mysql", "-v", g.confOf(i) + ":/etc/mysql/conf.d/galera.cnf:ro", galeraImage}, cmd...)...)
}

// position leest de positie van een node die stilstaat, zoals de agent:
// grastate.dat, en zonder seqno galera_recovery.
func (g *dockerGalera) position(i int) (Position, string) {
	g.t.Helper()
	p, err := ParseGrastate(g.onVolume(i, "cat", GrastateFile))
	if err != nil {
		g.t.Fatalf("%s: %v", g.names[i], err)
	}
	if p.Seqno >= 0 {
		return p, "grastate"
	}
	r, err := ParseRecovered(g.onVolume(i, "galera_recovery"))
	if err != nil {
		g.t.Fatalf("%s: %v", g.names[i], err)
	}
	r.SafeToBootstrap = p.SafeToBootstrap
	return r, "recovery"
}

// start start node i zoals systemd: met de positie uit galera_recovery als
// grastate.dat er geen heeft, en bij newCluster als galera_new_cluster.
func (g *dockerGalera) start(i int, newCluster bool) {
	g.t.Helper()
	name := g.names[i]
	_ = exec.Command("docker", "rm", "-f", name).Run()
	args := []string{"run", "-d", "--name", name, "--hostname", name, "--network", g.prefix, "-v", name + ":/var/lib/mysql",
		"-v", g.confOf(i) + ":/etc/mysql/conf.d/galera.cnf:ro", "-e", "MARIADB_ALLOW_EMPTY_ROOT_PASSWORD=1", galeraImage,
		"--wsrep_node_name=" + name, "--wsrep_node_address=" + name}
	if out, err := exec.Command("docker", "run", "--rm", "-v", name+":/var/lib/mysql", galeraImage, "cat", GrastateFile).Output(); err == nil {
		if p, err := ParseGrastate(string(out)); err == nil && p.Seqno < 0 {
			if r, err := ParseRecovered(g.onVolume(i, "galera_recovery")); err == nil {
				args = append(args, "--wsrep_start_position="+r.UUID+":"+strconv.FormatInt(r.Seqno, 10))
			}
		}
	}
	if newCluster {
		args = append(args, "--wsrep-new-cluster")
	}
	g.docker(args...)
}

func (g *dockerGalera) sql(i int, query string) string {
	g.t.Helper()
	return strings.TrimSpace(g.docker("exec", g.names[i], "mariadb", "-N", "-B", "-e", query))
}

func (g *dockerGalera) status(i int) map[string]string {
	out, err := exec.Command("docker", "exec", g.names[i], "mariadb", "-N", "-B", "-e",
		"SHOW GLOBAL STATUS WHERE Variable_name IN ('wsrep_local_state_comment', 'wsrep_cluster_size', 'wsrep_cluster_status', "+
			"'wsrep_cluster_state_uuid', 'wsrep_last_committed')").Output()
	if err != nil {
		return nil
	}
	return keyValues(string(out))
}

func keyValues(s string) map[string]string {
	m := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		if k, v, ok := strings.Cut(line, "\t"); ok {
			m[k] = v
		}
	}
	return m
}

// synced wacht tot de nodes Synced zijn in een Primary component van size.
func (g *dockerGalera) synced(size int, nodes ...int) {
	g.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for _, i := range nodes {
		for {
			st := g.status(i)
			if st["wsrep_local_state_comment"] == "Synced" && st["wsrep_cluster_status"] == "Primary" && st["wsrep_cluster_size"] == strconv.Itoa(size) {
				break
			}
			if time.Now().After(deadline) {
				logs, _ := exec.Command("docker", "logs", "--tail", "30", g.names[i]).CombinedOutput()
				g.t.Fatalf("%s niet Synced in een cluster van %d: %v\n%s", g.names[i], size, st, logs)
			}
			time.Sleep(2 * time.Second)
		}
	}
}

func (g *dockerGalera) stop(i int) { g.t.Helper(); g.docker("stop", "-t", "60", g.names[i]) }
func (g *dockerGalera) kill(i int) { g.t.Helper(); g.docker("kill", g.names[i]) }

// up begint een nieuw cluster van drie nodes met een tabel.
func (g *dockerGalera) up() {
	g.t.Helper()
	g.start(0, true)
	g.synced(1, 0)
	// De SST-gebruiker, zoals de stap van het onderdeel hem maakt.
	g.sql(0, "CREATE USER IF NOT EXISTS 'mysql'@'localhost' IDENTIFIED VIA unix_socket; "+
		"GRANT RELOAD, PROCESS, LOCK TABLES, BINLOG MONITOR ON *.* TO 'mysql'@'localhost'; "+
		"CREATE DATABASE cf; CREATE TABLE cf.t (id int AUTO_INCREMENT PRIMARY KEY, v text); INSERT INTO cf.t (v) VALUES ('a'), ('b')")
	g.start(1, false)
	g.start(2, false)
	g.synced(3, 0, 1, 2)
}

// choose leest de posities van de drie nodes en kiest er een.
func (g *dockerGalera) choose() (Candidate, []Candidate) {
	g.t.Helper()
	var cands []Candidate
	for i, name := range g.names {
		p, from := g.position(i)
		g.t.Logf("%s: %s (%s, safe_to_bootstrap %v)", name, p, from, p.SafeToBootstrap)
		cands = append(cands, Candidate{Name: name, Index: i + 1, Position: p})
	}
	best, err := Choose(cands)
	if err != nil {
		g.t.Fatal(err)
	}
	return best, cands
}

// restart begint het cluster op node best zoals de taak van ClusterForge
// en start daarna de andere, en kijkt of alle rijen er zijn.
func (g *dockerGalera) restart(best Candidate, rows int) {
	g.t.Helper()
	b := best.Index - 1
	g.onVolume(b, "sh", "-c", SafeToBootstrap)
	g.start(b, true)
	g.synced(1, b)
	for i := range g.names {
		if i != b {
			g.start(i, false)
		}
	}
	g.synced(3, 0, 1, 2)
	for i := range g.names {
		if got := g.sql(i, "SELECT count(*) FROM cf.t"); got != strconv.Itoa(rows) {
			g.t.Fatalf("%s heeft %s rijen, verwacht %d", g.names[i], got, rows)
		}
	}
}

func TestRealGaleraRestart(t *testing.T) {
	g := newDockerGalera(t)
	g.up()

	t.Run("gewone stop", func(t *testing.T) {
		g.t = t
		// nc1 en nc2 stoppen; nc3 schrijft daarna nog en stopt als laatste.
		g.stop(0)
		g.stop(1)
		g.synced(1, 2)
		g.sql(2, "INSERT INTO cf.t (v) VALUES ('c')")
		g.stop(2)
		best, cands := g.choose()
		if best.Name != g.names[2] || !best.Position.SafeToBootstrap || best.Position.Seqno <= cands[0].Position.Seqno {
			t.Fatalf("gekozen %+v uit %+v", best, cands)
		}
		g.restart(best, 3)
	})

	t.Run("crash", func(t *testing.T) {
		g.t = t
		// nc1 valt uit, nc2 schrijft nog, en dan vallen nc2 en nc3 tegelijk
		// uit: geen enkele node weet zijn seqno.
		g.kill(0)
		g.synced(2, 1)
		g.sql(1, "INSERT INTO cf.t (v) VALUES ('d'), ('e')")
		g.kill(1)
		g.kill(2)
		for i := range g.names {
			if p, _ := ParseGrastate(g.onVolume(i, "cat", GrastateFile)); p.Seqno != -1 || p.SafeToBootstrap {
				t.Fatalf("%s na een crash: %+v", g.names[i], p)
			}
		}
		best, cands := g.choose()
		// nc2 en nc3 hebben dezelfde data; het laagste volgnummer begint.
		if best.Name != g.names[1] || cands[0].Position.Seqno >= best.Position.Seqno || cands[2].Position.Seqno != best.Position.Seqno {
			t.Fatalf("gekozen %+v uit %+v", best, cands)
		}
		g.restart(best, 5)
		// De geschiedenis loopt door: dezelfde uuid, en de seqno van een
		// draaiende node is minstens die van de gekozen node.
		st := g.status(0)
		if st["wsrep_cluster_state_uuid"] != best.Position.UUID {
			t.Fatalf("uuid na het herstarten: %v, verwacht %s", st, best.Position.UUID)
		}
		g.sql(0, "INSERT INTO cf.t (v) VALUES ('f')")
		if n, _ := strconv.ParseInt(g.status(2)["wsrep_last_committed"], 10, 64); n <= best.Position.Seqno {
			t.Fatalf("wsrep_last_committed %d na een nieuwe rij, gekozen seqno %d", n, best.Position.Seqno)
		}
	})

	t.Run("stroomuitval", func(t *testing.T) {
		g.t = t
		// Alle drie vallen tegelijk uit en starten weer: Galera herstelt de
		// laatste Primary component zelf (pc.recovery), zonder ClusterForge.
		for i := range g.names {
			g.kill(i)
		}
		for i := range g.names {
			g.start(i, false)
		}
		g.synced(3, 0, 1, 2)
		if got := g.sql(1, "SELECT count(*) FROM cf.t"); got != "6" {
			t.Fatalf("%s rijen na de stroomuitval", got)
		}
	})

	t.Run("gewone stop zonder ClusterForge", func(t *testing.T) {
		g.t = t
		// Na een gewone stop van alle nodes begint Galera niet vanzelf: de
		// nodes geven het op en ClusterForge moet het cluster beginnen.
		for i := range g.names {
			g.stop(i)
		}
		for i := range g.names {
			g.start(i, false)
		}
		deadline := time.Now().Add(3 * time.Minute)
		for {
			running := 0
			for _, name := range g.names {
				if strings.TrimSpace(g.docker("inspect", "-f", "{{.State.Running}}", name)) == "true" {
					running++
				}
			}
			if running == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("nog %d nodes draaien zonder cluster", running)
			}
			time.Sleep(2 * time.Second)
		}
		best, _ := g.choose()
		g.restart(best, 6)
	})
}

func TestRealGaleraJoin(t *testing.T) {
	g := newDockerGalera(t)
	g.up()
	uptime := func(i int) int {
		n, _ := strconv.Atoi(g.sql(i, "SELECT variable_value FROM information_schema.global_status WHERE variable_name = 'UPTIME'"))
		return n
	}
	before := uptime(0)

	// Er wordt geschreven terwijl de nieuwe node aansluit.
	stop := make(chan struct{})
	done := make(chan int)
	first := g.names[0]
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				done <- n
				return
			default:
			}
			if exec.Command("docker", "exec", first, "mariadb", "-e", "INSERT INTO cf.t (v) VALUES ('tijdens')").Run() == nil {
				n++
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Alleen de nieuwe node kent alle vier de adressen.
	g.names = append(g.names, fmt.Sprintf("%s-%d", g.prefix, 4))
	g.confs = map[int]string{3: g.writeConf("galera4.cnf")}
	start := time.Now()
	g.start(3, false)
	g.synced(4, 0, 1, 2, 3)
	t.Logf("de vierde node Synced na %s", time.Since(start).Round(100*time.Millisecond))
	close(stop)
	written := <-done
	if written == 0 {
		t.Fatal("er werd niets geschreven tijdens het aansluiten")
	}
	want := strconv.Itoa(2 + written)
	for i := range g.names {
		g.synced(4, i)
		if got := g.sql(i, "SELECT count(*) FROM cf.t"); got != want {
			t.Errorf("%s heeft %s rijen, verwacht %s", g.names[i], got, want)
		}
	}
	// De nodes die er al waren, herstartten niet.
	if after := uptime(0); after < before {
		t.Errorf("%s herstartte: uptime %d, eerder %d", g.names[0], after, before)
	}
	// Ook na een herstart van een oude node met zijn oude configuratie doet
	// hij weer mee: hij vindt het cluster via de nodes die hij kent.
	g.stop(1)
	g.synced(3, 0, 3)
	g.start(1, false)
	g.synced(4, 0, 1, 2, 3)

	// De query's van de agent voor de grafieken en de positie werken op
	// een echte Galera-node.
	m := keyValues(g.sql(0, dbsql.MariaMetrics))
	for _, k := range []string{"threads_connected", "questions", "innodb_buffer_pool_bytes_data", "wsrep_local_recv_queue",
		"wsrep_flow_control_paused_ns", "max_connections", "innodb_buffer_pool_size"} {
		if _, err := strconv.ParseFloat(m[k], 64); err != nil {
			t.Errorf("%s ontbreekt in de metingen: %v", k, m)
		}
	}
	if m["wsrep_local_state"] != "4" || m["wsrep_cluster_size"] != "4" {
		t.Errorf("metingen: %v", m)
	}
	if p := keyValues(g.sql(0, dbsql.MariaPosition)); p["Uptime"] == "" || p["wsrep_last_committed"] == "" {
		t.Errorf("positie: %v", p)
	}
}
