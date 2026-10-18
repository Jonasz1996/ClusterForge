package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type healView struct {
	Key         string `json:"key"`
	ClusterName string `json:"cluster_name"`
	Engine      string `json:"engine"`
	Problems    []struct {
		Code  string   `json:"code"`
		Level string   `json:"level"`
		Title string   `json:"title"`
		Why   string   `json:"why"`
		Nodes []string `json:"nodes"`
	} `json:"problems"`
	Plan *struct {
		Action  string   `json:"action"`
		Steps   []string `json:"steps"`
		Auto    bool     `json:"auto"`
		AutoWhy string   `json:"auto_why"`
		Blocked string   `json:"blocked"`
		Hash    string   `json:"hash"`
		Nodes   []struct {
			Hostname          string `json:"hostname"`
			Start             bool   `json:"start"`
			Kill              bool   `json:"kill"`
			KillPids          []int  `json:"kill_pids"`
			SetAddress        bool   `json:"set_address"`
			OldClusterAddress string `json:"old_cluster_address"`
			ClusterAddress    string `json:"cluster_address"`
			NodeAddress       string `json:"node_address"`
			File              string `json:"file"`
		} `json:"nodes"`
	} `json:"plan"`
}

func (h healView) codes() string {
	var c []string
	for _, p := range h.Problems {
		c = append(c, p.Code+"@"+strings.Join(p.Nodes, "+"))
	}
	return strings.Join(c, " ")
}

// healDB zet het databasecluster van Jonas van 18 oktober neer: drie
// Galera-nodes in lab met een config die nog naar de oude adressen
// 192.168.0.190-192 wijst. db1 hangt (unit failed, mariadbd draait nog),
// db2 staat stil, db3 draait zonder meerderheid.
type healDB struct {
	cluster cluster
	ids     []string
	hosts   []*agenttest.Host
	roots   []string
}

func newHealDB(t *testing.T, e *testEnv, c *client) *healDB {
	t.Helper()
	d := &healDB{}
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "database-cluster", "name": "database cluster", "type": "generic", "environment": "lab"}, &d.cluster); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	for i, name := range []string{"db1", "db2", "db3"} {
		ip := "192.168.10.18" + string(rune('1'+i))
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": d.cluster.ID}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		d.ids = append(d.ids, n.ID)
		a := &addrs{}
		a.set(ip)
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('d'+i)), 32), a, func(ag *agent.Agent, root string) {
			h := agenttest.NewHost(root, ip)
			ag.Exec, ag.Root, ag.Collector.Run = h.Exec, root, h.Exec
			write := func(p, s string) {
				full := filepath.Join(root, p)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("etc/mysql/my.cnf", "[client-server]\n!includedir /etc/mysql/conf.d/\n!includedir /etc/mysql/mariadb.conf.d/\n")
			write("etc/mysql/mariadb.conf.d/60-galera.cnf", "[galera]\nwsrep_on = ON\nwsrep_cluster_name = db\n"+
				"wsrep_cluster_address = gcomm://192.168.0.190,192.168.0.191,192.168.0.192\nwsrep_node_address = 192.168.0.19"+string(rune('0'+i))+"\n")
			galera := &agenttest.Galera{ClusterName: "db", UUID: "5c3a1e0e-0000-11f0-9a3b-7e1c2d3f4a5b", Status: "non-Primary", Size: 1, State: 0, Comment: "Initialized"}
			switch i {
			case 0:
				h.SetUnit("mariadb", agenttest.Unit{Failed: true})
				write("proc/169/comm", "mariadbd\n")
				write("proc/169/stat", "169 (mariadbd) S 1 169 169 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 7 0 100 0 0\n")
				write("proc/169/cgroup", "0::/system.slice/mariadb.service\n")
				h.SetJournal("mariadb.service", "WSREP: (abc251de-a157, 'tcp://0.0.0.0:4567') connection to peer 00000000-0000 with addr tcp://192.168.0.191:4567 timed out, no messages seen in PT3S\n")
			case 1:
				h.SetUnit("mariadb", agenttest.Unit{Failed: true})
			case 2:
				h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
				h.MariaDB = &agenttest.MariaDB{Version: "12.2.2-MariaDB", MaxConnections: 200, Uptime: 3600, Galera: galera}
			}
			d.hosts = append(d.hosts, h)
			d.roots = append(d.roots, root)
		})
	}
	return d
}

// heal leest de databases, laat Zelfherstel kijken en geeft het rapport van
// de MariaDB van het cluster, zodra ready het goedvindt.
func (d *healDB) heal(t *testing.T, e *testEnv, c *client, ready func(healView) bool) healView {
	t.Helper()
	ctx := context.Background()
	var out healView
	eventually(t, "Zelfherstel ziet het cluster", func() bool {
		if err := e.dbs.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.heal.InspectAll(ctx); err != nil {
			t.Fatal(err)
		}
		var raw json.RawMessage
		if s := c.do("GET", "/api/v1/heal", nil, &raw); s != http.StatusOK {
			t.Fatalf("heal: %d", s)
		}
		var l list[healView]
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatal(err)
		}
		for _, r := range l.Items {
			if r.Key == "mariadb/"+d.cluster.ID && !strings.Contains(r.codes(), "no_inspect") && ready(r) {
				out = r
				return true
			}
		}
		return false
	})
	return out
}

// TestHeal is de acceptatie van mijlpaal 1 van fase 7: bij Jonas' storing
// zegt Zelfherstel dat Galera de andere nodes op oude adressen zoekt, dat
// MariaDB op db1 hangt en dat er geen Primary component is, met een plan dat
// niet vanzelf mag omdat het de adressen wijzigt.
func TestHeal(t *testing.T) {
	e, c := adminClient(t)
	d := newHealDB(t, e, c)
	r := d.heal(t, e, c, func(r healView) bool { return strings.Contains(r.codes(), "db1+db2+db3") })
	want := "process_hung@db1 unit_disabled@db1 unit_stopped@db2 unit_disabled@db2 " +
		"galera_wrong_address@db1 galera_wrong_address@db2 galera_wrong_address@db3 galera_no_primary@db1+db2+db3"
	if r.codes() != want {
		t.Fatalf("problemen:\n%s\nverwacht:\n%s", r.codes(), want)
	}
	for _, p := range r.Problems {
		if p.Code == "galera_wrong_address" && p.Nodes[0] == "db1" &&
			(!strings.Contains(p.Why, "192.168.0.190, 192.168.0.191, 192.168.0.192") || !strings.Contains(p.Why, "192.168.10.181, 192.168.10.182, 192.168.10.183") ||
				!strings.Contains(p.Why, "probeert sinds de laatste start 192.168.0.191")) {
			t.Errorf("waarom: %s", p.Why)
		}
		if p.Code == "process_hung" && !strings.Contains(p.Why, "pid 169") {
			t.Errorf("hangend: %s", p.Why)
		}
	}
	p := r.Plan
	if p == nil || p.Action != "galera" || p.Auto || p.Blocked != "" || len(p.Nodes) != 3 || len(p.Steps) != 5 {
		t.Fatalf("plan: %+v", p)
	}
	for i, n := range p.Nodes {
		if !n.SetAddress || n.ClusterAddress != "gcomm://192.168.10.181,192.168.10.182,192.168.10.183" || n.NodeAddress != "192.168.10.18"+string(rune('1'+i)) ||
			n.File != "/etc/mysql/mariadb.conf.d/60-galera.cnf" || n.Kill != (i == 0) {
			t.Errorf("node %d: %+v", i, n)
		}
	}

	// Een kijker ziet het ook.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := v.do("GET", "/api/v1/heal", nil, nil); s != http.StatusOK {
		t.Fatalf("kijker: %d", s)
	}
}
