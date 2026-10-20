package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/galera"
)

// zabbixHeal is het zabbix-cluster van Jonas op 20 oktober: MariaDB op
// ZabbixHa1 staat stil, ZabbixHa2 heeft nog geen MariaDB, en Zabbixproxy is
// de scheidsrechter (garb) met zijn eigen MariaDB zonder Galera, voor de
// Zabbix-proxy. Zelfherstel zag die toen als Galera-node: het zette
// 192.168.10.198 in wsrep_cluster_address van ZabbixHa1 en stopte hem om
// zijn positie te lezen.
type zabbixHeal struct {
	cluster cluster
	ids     map[string]string
	hosts   map[string]*agenttest.Host
	roots   map[string]string
	// proxyFails: MariaDB op Zabbixproxy start niet.
	proxyFails bool
	// hold houdt galera_new_cluster op ZabbixHa1 tegen tot hij dicht gaat;
	// beginning gaat dicht zodra het begint.
	hold, beginning chan struct{}
	once            sync.Once
}

const zabbixGalera = "/etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf"

// newZabbixHeal zet het cluster neer. component: de uitrol van het
// onderdeel MariaDB (niet af) noemt Zabbixproxy de scheidsrechter.
// proxyRunning: de MariaDB van de proxy draait.
func newZabbixHeal(t *testing.T, e *testEnv, c *client, component, proxyRunning bool) *zabbixHeal {
	t.Helper()
	z := &zabbixHeal{ids: map[string]string{}, hosts: map[string]*agenttest.Host{}, roots: map[string]string{}}
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "zabbix", "name": "zabbix", "type": "generic", "environment": "lab"}, &z.cluster); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	for i, name := range []string{"ZabbixHa1", "ZabbixHa2", "Zabbixproxy"} {
		ip := map[string]string{"ZabbixHa1": "192.168.10.134", "ZabbixHa2": "192.168.10.135", "Zabbixproxy": "192.168.10.198"}[name]
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": z.cluster.ID}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		z.ids[name] = n.ID
		a := &addrs{}
		a.set(ip)
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), a, func(ag *agent.Agent, root string) {
			h := agenttest.NewHost(root, ip)
			ag.Exec, ag.Root, ag.Collector.Run = h.Exec, root, h.Exec
			writeFile(t, filepath.Join(root, "etc/mysql/my.cnf"), "[client-server]\n!includedir /etc/mysql/conf.d/\n!includedir /etc/mysql/mariadb.conf.d/\n", 0o644)
			switch name {
			case "ZabbixHa1":
				writeFile(t, filepath.Join(root, zabbixGalera), "[galera]\nwsrep_on = ON\nwsrep_cluster_name = zabbix\n"+
					"wsrep_cluster_address = gcomm://192.168.10.134,192.168.10.135\nwsrep_node_address = 192.168.10.134\n", 0o644)
				h.MariaDB = &agenttest.MariaDB{Version: "11.8.3-MariaDB", MaxConnections: 151, Down: true}
				h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Failed: true})
				writeGrastate(h, 12, false)
				h.Shell = func(cmd string) (bool, []byte, error) {
					switch {
					case cmd == "systemctl is-active --quiet mariadb":
						if u, _ := h.Unit("mariadb"); u.Active {
							return true, nil, nil
						}
						return true, nil, errors.New("exit status 3")
					case strings.Contains(cmd, "galera_new_cluster"):
						if z.hold != nil {
							z.once.Do(func() { close(z.beginning) })
							<-z.hold
						}
						h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
						h.Locked(func() {
							h.MariaDB.Down, h.MariaDB.Uptime = false, 1
							h.MariaDB.Galera = &agenttest.Galera{ClusterName: "zabbix", UUID: galeraUUID, Status: "Primary", Size: 1, State: 4, Comment: "Synced", Ready: true, Seqno: 12}
						})
						return true, nil, nil
					}
					return false, nil, nil
				}
			case "Zabbixproxy":
				h.MariaDB = &agenttest.MariaDB{Version: "11.8.3-MariaDB", MaxConnections: 151, Down: !proxyRunning, Uptime: 3600,
					Databases: []agenttest.Database{{Name: "zabbix_proxy", Size: 64 << 20, Tables: 180}}}
				h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: proxyRunning})
				h.SetUnit("garb", agenttest.Unit{Enabled: true, Active: true})
				start := galera.ClearNewCluster + "; systemctl reset-failed mariadb 2>/dev/null; systemctl start --no-block mariadb"
				h.Shell = func(cmd string) (bool, []byte, error) {
					switch cmd {
					case "systemctl is-active --quiet mariadb":
						if u, _ := h.Unit("mariadb"); u.Active {
							return true, nil, nil
						}
						return true, nil, errors.New("exit status 3")
					case start:
						if z.proxyFails {
							h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Failed: true})
							return true, nil, errors.New("Job for mariadb.service failed")
						}
						h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
						h.Locked(func() { h.MariaDB.Down, h.MariaDB.Uptime = false, 1 })
						return true, nil, nil
					}
					return false, nil, nil
				}
				h.SetAfter(func(call string, err error) {
					if err == nil && call == "systemctl stop mariadb" {
						h.Locked(func() { h.MariaDB.Down = true })
					}
				})
			}
			z.hosts[name], z.roots[name] = h, root
		})
	}
	if component {
		spec, _ := json.Marshal(map[string]any{"nodes": []map[string]any{
			{"node_id": z.ids["ZabbixHa1"], "hostname": "ZabbixHa1", "role": "data", "index": 1},
			{"node_id": z.ids["ZabbixHa2"], "hostname": "ZabbixHa2", "role": "data", "index": 2},
			{"node_id": z.ids["Zabbixproxy"], "hostname": "Zabbixproxy", "role": "arbiter", "index": 1},
		}})
		if _, err := e.pool.Exec(context.Background(), `INSERT INTO cluster_components (cluster_id, engine, spec) VALUES ($1, 'mariadb', $2)`, z.cluster.ID, spec); err != nil {
			t.Fatal(err)
		}
	}
	return z
}

// reports laat Zelfherstel kijken tot ready de rapporten per sleutel
// goedvindt.
func (z *zabbixHeal) reports(t *testing.T, e *testEnv, c *client, ready func(map[string]healView) bool) map[string]healView {
	t.Helper()
	ctx := context.Background()
	var out map[string]healView
	eventually(t, "Zelfherstel ziet zabbix", func() bool {
		if err := e.dbs.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.heal.InspectAll(ctx); err != nil {
			t.Fatal(err)
		}
		var l list[healView]
		if s := c.do("GET", "/api/v1/heal", nil, &l); s != http.StatusOK {
			t.Fatalf("heal: %d", s)
		}
		out = map[string]healView{}
		for _, r := range l.Items {
			if strings.Contains(r.codes(), "no_inspect") {
				return false
			}
			out[r.Key] = r
		}
		return ready(out)
	})
	return out
}

// galeraCall: een aanroep die alleen het herstel van een Galera-node doet
// (stoppen, positie lezen, beginnen, een proces afschieten).
func galeraCall(call string) bool {
	for _, s := range []string{"systemctl stop", "galera_recovery", "--wsrep-recover", "galera_new_cluster", "kill "} {
		if strings.Contains(call, s) {
			return true
		}
	}
	return false
}

func stepNames(j job) []string {
	var out []string
	for _, st := range j.Steps {
		out = append(out, st.Name)
	}
	return out
}

// TestHealZabbixArbiter: met het onderdeel is de MariaDB van Zabbixproxy
// een losse server in het cluster. Zelfherstel begint het cluster alleen op
// ZabbixHa1 en raakt de proxy niet aan; de proxy heeft zijn eigen herstel,
// dat hem gewoon start.
func TestHealZabbixArbiter(t *testing.T) {
	e, c := adminClient(t)
	z := newZabbixHeal(t, e, c, true, false)
	clusterKey, proxyKey := "mariadb/"+z.cluster.ID, "mariadb/node/"+z.ids["Zabbixproxy"]
	reps := z.reports(t, e, c, func(m map[string]healView) bool {
		return strings.Contains(m[clusterKey].codes(), "galera_no_primary") && m[proxyKey].codes() == "unit_stopped@Zabbixproxy"
	})
	r := reps[clusterKey]
	if r.codes() != "unit_stopped@ZabbixHa1 component_unfinished@ZabbixHa1 galera_no_primary@ZabbixHa1" {
		t.Fatalf("cluster: %s", r.codes())
	}
	if r.Plan == nil || r.Plan.Blocked != "" || len(r.Plan.Nodes) != 1 || r.Plan.Nodes[0].Hostname != "ZabbixHa1" || r.Plan.Nodes[0].SetAddress {
		t.Fatalf("plan van het cluster: %+v", r.Plan)
	}
	p := reps[proxyKey]
	if p.ClusterName != "zabbix" || p.Hostname != "Zabbixproxy" || p.NodeID == nil || *p.NodeID != z.ids["Zabbixproxy"] ||
		p.Plan == nil || p.Plan.Action != "restart" || !p.Plan.Auto || len(p.Plan.Nodes) != 1 {
		t.Fatalf("proxy: %+v %+v", p, p.Plan)
	}

	j, logs := (&healDB{}).healRun(t, c, r)
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Posities lezen", "ZabbixHa1: het cluster beginnen", "Controleren"}) {
		t.Fatalf("taak: %s %s %v\n%v", j.Status, j.Error, stepNames(j), logs)
	}
	if l := logs["Posities lezen"]; !strings.Contains(l, "ZabbixHa1: seqno 12 uit grastate") || strings.Contains(l, "Zabbixproxy") {
		t.Errorf("posities:\n%s", l)
	}
	for _, call := range z.hosts["Zabbixproxy"].Calls() {
		if galeraCall(call) || strings.Contains(call, "systemctl start") {
			t.Errorf("Zabbixproxy aangeraakt: %s", call)
		}
	}
	if cfg, _ := os.ReadFile(filepath.Join(z.roots["ZabbixHa1"], zabbixGalera)); !strings.Contains(string(cfg), "gcomm://192.168.10.134,192.168.10.135\n") {
		t.Errorf("config van ZabbixHa1:\n%s", cfg)
	}

	// Het herstel van de proxy start zijn MariaDB als gewone server.
	j, logs = (&healDB{}).healRun(t, c, p)
	if j.Status != "succeeded" || j.Title != "Zelfherstel: MariaDB op Zabbixproxy" || !slices.Equal(stepNames(j), []string{"Zabbixproxy: mariadb starten"}) {
		t.Fatalf("proxy: %s %s %s %v\n%v", j.Status, j.Error, j.Title, stepNames(j), logs)
	}
	if u, _ := z.hosts["Zabbixproxy"].Unit("mariadb"); !u.Active {
		t.Errorf("MariaDB op Zabbixproxy: %+v", u)
	}

	// De pagina Databases toont de proxy als losse server in zabbix.
	var groups list[struct {
		Key         string  `json:"key"`
		ClusterID   *string `json:"cluster_id"`
		NodeID      *string `json:"node_id"`
		ClusterName string  `json:"cluster_name"`
		Servers     []struct {
			Hostname string `json:"hostname"`
		} `json:"servers"`
	}]
	if s := c.do("GET", "/api/v1/databases", nil, &groups); s != http.StatusOK {
		t.Fatalf("databases: %d", s)
	}
	var got []string
	for _, g := range groups.Items {
		node := ""
		if g.NodeID != nil {
			node = *g.NodeID
		}
		var hosts []string
		for _, s := range g.Servers {
			hosts = append(hosts, s.Hostname)
		}
		got = append(got, g.Key+" "+node+" "+g.ClusterName+" "+strings.Join(hosts, ","))
	}
	want := []string{clusterKey + "  zabbix ZabbixHa1", proxyKey + " " + z.ids["Zabbixproxy"] + " zabbix Zabbixproxy"}
	if !slices.Equal(got, want) {
		t.Errorf("databases:\n%q\nwil\n%q", got, want)
	}

	// Verhuizen van de proxy naar de database van zijn eigen cluster biedt
	// ClusterForge niet aan: de apps delen vaak een gebruiker (zabbix).
	var comp string
	if err := e.pool.QueryRow(context.Background(), `UPDATE cluster_components SET applied_revision = 1 WHERE cluster_id = $1 RETURNING id::text`, z.cluster.ID).Scan(&comp); err != nil {
		t.Fatal(err)
	}
	if err := e.dbs.SyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	var mp migrationPlanView
	if s := c.do("GET", "/api/v1/databases/migration?source="+url.QueryEscape(proxyKey)+"&database=zabbix_proxy", nil, &mp); s != http.StatusOK {
		t.Fatalf("verhuizen: %d", s)
	}
	if len(mp.Targets) != 0 || mp.Source.Name != "Zabbixproxy" {
		t.Errorf("verhuizen: %+v %+v", mp.Source, mp.Targets)
	}
	mp = migrationPlanView{}
	if s := c.do("GET", "/api/v1/databases/migration?source="+url.QueryEscape(proxyKey)+"&database=zabbix_proxy&component="+comp, nil, &mp); s != http.StatusOK {
		t.Fatalf("verhuizen naar zabbix: %d", s)
	}
	if len(mp.Findings) != 1 || mp.Findings[0].Code != "same" || !strings.HasPrefix(mp.Findings[0].Message, "zabbix_proxy staat op Zabbixproxy, een losse server in dit cluster") {
		t.Errorf("verhuizen naar zabbix: %+v", mp.Findings)
	}
}

// TestHealLooseNextToGalera: zonder onderdeel weet ClusterForge niet dat
// Zabbixproxy de scheidsrechter is, en zit zijn MariaDB in de groep van het
// cluster. Zelfherstel ziet aan de config dat die geen Galera-node is: het
// start hem als gewone server, begint het cluster alleen op ZabbixHa1 en
// stopt de proxy niet.
func TestHealLooseNextToGalera(t *testing.T) {
	e, c := adminClient(t)
	z := newZabbixHeal(t, e, c, false, false)
	key := "mariadb/" + z.cluster.ID
	reps := z.reports(t, e, c, func(m map[string]healView) bool {
		return strings.Contains(m[key].codes(), "unit_stopped@Zabbixproxy") && strings.Contains(m[key].codes(), "galera_no_primary")
	})
	r := reps[key]
	if r.codes() != "unit_stopped@ZabbixHa1 unit_stopped@Zabbixproxy galera_no_primary@ZabbixHa1" {
		t.Fatalf("problemen: %s", r.codes())
	}
	if r.Plan == nil || r.Plan.Blocked != "" || len(r.Plan.Nodes) != 2 || r.Plan.Nodes[0].SetAddress || r.Plan.Nodes[1].Hostname != "Zabbixproxy" ||
		!strings.HasPrefix(r.Plan.Steps[0], "Op Zabbixproxy MariaDB starten en wachten tot hij antwoordt.") {
		t.Fatalf("plan: %+v", r.Plan)
	}
	j, logs := (&healDB{}).healRun(t, c, r)
	want := []string{"Zabbixproxy: mariadb starten", "Posities lezen", "ZabbixHa1: het cluster beginnen", "Controleren"}
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), want) {
		t.Fatalf("taak: %s %s %v\n%v", j.Status, j.Error, stepNames(j), logs)
	}
	if l := logs["Posities lezen"]; strings.Contains(l, "Zabbixproxy") {
		t.Errorf("posities:\n%s", l)
	}
	if l := logs["Controleren"]; !strings.Contains(l, "1 nodes Synced in één cluster") {
		t.Errorf("controle:\n%s", l)
	}
	for _, call := range z.hosts["Zabbixproxy"].Calls() {
		if galeraCall(call) {
			t.Errorf("Zabbixproxy als Galera-node behandeld: %s", call)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(z.roots["ZabbixHa1"], zabbixGalera+".cf-zelfherstel-*")); len(matches) > 0 {
		t.Errorf("config van ZabbixHa1 gewijzigd: %v", matches)
	}

	// Draait het cluster en staat alleen Zabbixproxy stil, dan start de
	// taak alleen hem en raakt ze het cluster niet aan.
	proxy := z.hosts["Zabbixproxy"]
	proxy.SetUnit("mariadb", agenttest.Unit{Enabled: true, Failed: true})
	proxy.Locked(func() { proxy.MariaDB.Down = true })
	reps = z.reports(t, e, c, func(m map[string]healView) bool {
		return m[key].codes() == "unit_stopped@Zabbixproxy" && m[key].Plan != nil
	})
	calls := len(z.hosts["ZabbixHa1"].Calls())
	j, logs = (&healDB{}).healRun(t, c, reps[key])
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Zabbixproxy: mariadb starten"}) {
		t.Fatalf("alleen de proxy: %s %s %v\n%v", j.Status, j.Error, stepNames(j), logs)
	}
	for _, call := range z.hosts["ZabbixHa1"].Calls()[calls:] {
		if galeraCall(call) {
			t.Errorf("ZabbixHa1 aangeraakt: %s", call)
		}
	}
}

// TestHealLooseFails: start de MariaDB van Zabbixproxy niet, dan begint de
// taak het cluster toch op ZabbixHa1 en mislukt ze met de fout van de proxy.
func TestHealLooseFails(t *testing.T) {
	e, c := adminClient(t)
	z := newZabbixHeal(t, e, c, false, false)
	z.proxyFails = true
	key := "mariadb/" + z.cluster.ID
	reps := z.reports(t, e, c, func(m map[string]healView) bool {
		return strings.Contains(m[key].codes(), "unit_stopped@Zabbixproxy") && strings.Contains(m[key].codes(), "galera_no_primary")
	})
	j, logs := (&healDB{}).healRun(t, c, reps[key])
	want := []string{"Zabbixproxy: mariadb starten", "Posities lezen", "ZabbixHa1: het cluster beginnen", "Controleren"}
	if j.Status != "failed" || !slices.Equal(stepNames(j), want) || !strings.HasPrefix(j.Error, "Zabbixproxy: ") {
		t.Fatalf("taak: %s %s %v\n%v", j.Status, j.Error, stepNames(j), logs)
	}
	if u, _ := z.hosts["ZabbixHa1"].Unit("mariadb"); !u.Active {
		t.Errorf("ZabbixHa1 niet begonnen: %+v", u)
	}
}

// TestHealLooseListedGarb: staat 192.168.10.198 in wsrep_cluster_address van
// ZabbixHa1, dan is dat adres van garb als garb op Zabbixproxy draait of er
// bij een boot start; na een stroomonderbreking blijft garb gestopt tot er
// een Primary is. Een gestopte garb die niet bij een boot start, zegt niets.
func TestHealLooseListedGarb(t *testing.T) {
	e, c := adminClient(t)
	z := newZabbixHeal(t, e, c, false, true)
	writeFile(t, filepath.Join(z.roots["ZabbixHa1"], zabbixGalera), "[galera]\nwsrep_on = ON\nwsrep_cluster_name = zabbix\n"+
		"wsrep_cluster_address = gcomm://192.168.10.134,192.168.10.198,192.168.10.135\nwsrep_node_address = 192.168.10.134\n", 0o644)
	key := "mariadb/" + z.cluster.ID
	z.hosts["Zabbixproxy"].SetUnit("garb", agenttest.Unit{Enabled: true, Failed: true})
	reps := z.reports(t, e, c, func(m map[string]healView) bool {
		return m[key].Plan != nil && strings.Contains(m[key].codes(), "galera_no_primary")
	})
	if r := reps[key]; r.Plan.Blocked != "" || len(r.Plan.Nodes) != 1 || r.Plan.Nodes[0].Hostname != "ZabbixHa1" || r.Plan.Nodes[0].SetAddress {
		t.Errorf("garb gestopt, enabled: %+v", r.Plan)
	}

	// Niet enabled: dan kan Zabbixproxy een Galera-node zijn die zijn config
	// kwijt is.
	if _, err := e.pool.Exec(context.Background(), `UPDATE node_facts SET facts = jsonb_set(facts, '{services}',
		(SELECT jsonb_agg(CASE WHEN x ->> 'name' = 'garb' THEN x || '{"enabled": "disabled"}' ELSE x END) FROM jsonb_array_elements(facts -> 'services') x))
		WHERE node_id = $1`, z.ids["Zabbixproxy"]); err != nil {
		t.Fatal(err)
	}
	reps = z.reports(t, e, c, func(m map[string]healView) bool {
		return m[key].Plan != nil && strings.HasPrefix(m[key].Plan.Blocked, "Zabbixproxy staat in wsrep_cluster_address van een andere node")
	})
	if r := reps[key]; r.Plan.Auto {
		t.Errorf("garb gestopt, disabled: %+v", r.Plan)
	}

	// Draait garb, dan is het adres weer van garb.
	z.hosts["Zabbixproxy"].SetUnit("garb", agenttest.Unit{Active: true})
	reps = z.reports(t, e, c, func(m map[string]healView) bool { return m[key].Plan != nil && m[key].Plan.Blocked == "" })
	if r := reps[key]; len(r.Plan.Nodes) != 1 || r.Plan.Nodes[0].Hostname != "ZabbixHa1" {
		t.Errorf("garb draait: %+v", r.Plan)
	}
}

// TestHealResumeBegin: stopt de server terwijl ZabbixHa1 het cluster begint,
// dan gaat de hervatte taak verder met dezelfde keuze uit Posities lezen, en
// niet met een gewone start op de plaats van het beginnen.
func TestHealResumeBegin(t *testing.T) {
	e, c := adminClient(t)
	z := newZabbixHeal(t, e, c, false, true)
	z.hold, z.beginning = make(chan struct{}), make(chan struct{})
	key := "mariadb/" + z.cluster.ID
	reps := z.reports(t, e, c, func(m map[string]healView) bool {
		return m[key].Plan != nil && strings.Contains(m[key].codes(), "galera_no_primary")
	})
	var j job
	if s := c.do("POST", "/api/v1/heal/run", map[string]any{"key": key, "hash": reps[key].Plan.Hash}, &j); s != http.StatusAccepted {
		t.Fatalf("herstellen: %d", s)
	}
	select {
	case <-z.beginning:
	case <-time.After(30 * time.Second):
		t.Fatal("ZabbixHa1 begint het cluster niet")
	}
	e.restartRunner()
	close(z.hold)
	j = waitJobLong(t, c, j.ID)
	if want := []string{"Posities lezen", "ZabbixHa1: het cluster beginnen", "Controleren"}; j.Status != "succeeded" || !slices.Equal(stepNames(j), want) {
		t.Fatalf("taak: %s %s %v", j.Status, j.Error, stepNames(j))
	}
}
