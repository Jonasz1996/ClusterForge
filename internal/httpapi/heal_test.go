package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

	// Galera zoals de test het naspeelt: wie meedoet, en op welke node het
	// cluster begon.
	mu      sync.Mutex
	members []int
	began   []string
}

const (
	healUUID    = "5c3a1e0e-0000-11f0-9a3b-7e1c2d3f4a5b"
	healGalera  = "/etc/mysql/mariadb.conf.d/60-galera.cnf"
	healNewAddr = "gcomm://192.168.10.181,192.168.10.182,192.168.10.183"
)

// shell speelt de commando's van een herstel na op node i: een hangend
// proces stoppen, MariaDB starten zonder te wachten en het cluster
// beginnen. Een node sluit alleen aan als zijn config de nieuwe adressen
// heeft en er een Primary component is; anders faalt de unit, zoals Galera.
func (d *healDB) shell(i int) func(cmd string) (bool, []byte, error) {
	return func(cmd string) (bool, []byte, error) {
		h, root := d.hosts[i], d.roots[i]
		fail := errors.New("exit status 1")
		switch {
		case strings.HasPrefix(cmd, "! { kill -0 "):
			if _, err := os.Stat(filepath.Join(root, "proc/169")); err == nil {
				return true, nil, fail
			}
			return true, nil, nil
		case strings.HasPrefix(cmd, "systemctl kill --signal=TERM mariadb.service"):
			_ = os.RemoveAll(filepath.Join(root, "proc/169"))
			return true, nil, nil
		case cmd == "systemctl is-active --quiet mariadb":
			if u, _ := h.Unit("mariadb"); u.Active {
				return true, nil, nil
			}
			return true, nil, fail
		case cmd == "systemctl unset-environment _WSREP_NEW_CLUSTER; systemctl reset-failed mariadb 2>/dev/null; systemctl start --no-block mariadb":
			cfg, _ := os.ReadFile(filepath.Join(root, healGalera))
			d.mu.Lock()
			alive := len(d.members) > 0
			d.mu.Unlock()
			if !alive || !strings.Contains(string(cfg), healNewAddr) {
				d.setUnit(i, agenttest.Unit{Failed: true})
				return true, nil, nil
			}
			d.join(i, false)
			return true, nil, nil
		case strings.Contains(cmd, "galera_new_cluster"):
			if h.MariaDB.NoGaleraScripts {
				return true, []byte(galera.NoNewCluster + "\n"), fail
			}
			d.join(i, true)
			return true, nil, nil
		}
		return false, nil, nil
	}
}

// after laat een gewone stop van MariaDB de node uit het cluster halen en
// zijn positie in grastate.dat schrijven.
func (d *healDB) after(i int) func(call string, err error) {
	return func(call string, err error) {
		if err == nil && call == "systemctl stop mariadb" {
			d.leave(i)
		}
	}
}

func (d *healDB) setUnit(i int, u agenttest.Unit) {
	old, _ := d.hosts[i].Unit("mariadb")
	u.Enabled = old.Enabled
	d.hosts[i].SetUnit("mariadb", u)
}

func (d *healDB) join(i int, first bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if first {
		d.members = nil
		d.began = append(d.began, []string{"db1", "db2", "db3"}[i])
	}
	if !slices.Contains(d.members, i) {
		d.members = append(d.members, i)
	}
	d.setUnit(i, agenttest.Unit{Active: true})
	d.update()
}

func (d *healDB) leave(i int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.members = slices.DeleteFunc(d.members, func(m int) bool { return m == i })
	h := d.hosts[i]
	var seqno int64 = -1
	h.Locked(func() {
		if h.MariaDB.Galera != nil {
			seqno = h.MariaDB.Galera.Seqno
		}
		h.MariaDB.Down, h.MariaDB.Galera = true, nil
	})
	writeGrastate(h, seqno, false)
	d.update()
}

// update geeft elk lid de toestand van het cluster; d.mu is vast.
func (d *healDB) update() {
	for _, m := range d.members {
		h := d.hosts[m]
		h.Locked(func() {
			h.MariaDB.Down = false
			h.MariaDB.Galera = &agenttest.Galera{ClusterName: "db", UUID: healUUID, Status: "Primary", Size: len(d.members), State: 4, Comment: "Synced", Ready: true, Seqno: 42}
		})
	}
}

func newHealDB(t *testing.T, e *testEnv, c *client, setup ...func(i int, h *agenttest.Host)) *healDB {
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
			// db1 crashte (de positie zit alleen nog in InnoDB) en heeft de
			// nieuwste data; db2 stopte gewoon.
			h.MariaDB = &agenttest.MariaDB{Version: "12.2.2-MariaDB", MaxConnections: 200, Down: true, Recovered: healUUID + ":42"}
			switch i {
			case 0:
				h.SetUnit("mariadb", agenttest.Unit{Failed: true})
				write("proc/169/comm", "mariadbd\n")
				write("proc/169/stat", "169 (mariadbd) S 1 169 169 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 7 0 100 0 0\n")
				write("proc/169/cgroup", "0::/system.slice/mariadb.service\n")
				h.SetJournal("mariadb.service", "WSREP: (abc251de-a157, 'tcp://0.0.0.0:4567') connection to peer 00000000-0000 with addr tcp://192.168.0.191:4567 timed out, no messages seen in PT3S\n")
				writeGrastate(h, -1, false)
			case 1:
				h.SetUnit("mariadb", agenttest.Unit{Failed: true})
				writeGrastate(h, 41, false)
			case 2:
				h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
				h.MariaDB.Down, h.MariaDB.Uptime = false, 3600
				h.MariaDB.Galera = &agenttest.Galera{ClusterName: "db", UUID: healUUID, Status: "non-Primary", Size: 1, State: 0, Comment: "Initialized", Seqno: 40}
			}
			for _, f := range setup {
				f(i, h)
			}
			d.hosts = append(d.hosts, h)
			d.roots = append(d.roots, root)
			h.Shell = d.shell(i)
			h.SetAfter(d.after(i))
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

// TestHealRun is de acceptatie van mijlpaal 2 van fase 7: Nu herstellen op
// Jonas' storing zet de Galera-adressen goed (met een kopie van de config),
// stopt het hangende mariadbd op db1, leest de posities, begint het cluster
// op db1 dat de nieuwste data heeft, en laat db2 en db3 aansluiten tot alle
// drie Synced zijn. Alleen een beheerder mag het, met het plan dat hij zag.
func TestHealRun(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	d := newHealDB(t, e, c)
	r := d.heal(t, e, c, func(r healView) bool { return strings.Contains(r.codes(), "db1+db2+db3") })
	if r.Plan == nil || r.Plan.Blocked != "" {
		t.Fatalf("plan: %+v", r.Plan)
	}
	hash := r.Plan.Hash
	run := func(c *client, hash, confirm string, out any) int {
		t.Helper()
		body := map[string]any{"key": r.Key, "hash": hash}
		if confirm != "" {
			body["confirm"] = confirm
		}
		return c.do("POST", "/api/v1/heal/run", body, out)
	}

	// Een kijker mag niet, een ander plan ook niet.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := run(v, hash, "", nil); s != http.StatusForbidden {
		t.Fatalf("kijker: %d", s)
	}
	var apiErr struct {
		Code string `json:"code"`
	}
	if s := run(c, "een-ander-plan", "", &apiErr); s != http.StatusConflict || apiErr.Code != "plan_stale" {
		t.Fatalf("ander plan: %d %+v", s, apiErr)
	}
	if s := c.do("POST", "/api/v1/heal/run", map[string]any{"key": "mariadb/" + d.ids[0], "hash": hash}, nil); s != http.StatusNotFound {
		t.Fatalf("onbekende database: %d", s)
	}
	// Op prod vraagt het tweestapsverificatie.
	if s := c.do("PATCH", "/api/v1/clusters/"+d.cluster.ID, map[string]any{"environment": "prod"}, nil); s != http.StatusOK {
		t.Fatalf("naar prod: %d", s)
	}
	if s := run(c, hash, "database-cluster", &apiErr); s != http.StatusForbidden || apiErr.Code != "totp_required" {
		t.Fatalf("prod: %d %+v", s, apiErr)
	}
	if s := c.do("PATCH", "/api/v1/clusters/"+d.cluster.ID, map[string]any{"environment": "lab"}, nil); s != http.StatusOK {
		t.Fatalf("naar lab: %d", s)
	}

	var j job
	if s := run(c, hash, "", &j); s != http.StatusAccepted || j.Title != "Zelfherstel: MariaDB van database cluster" {
		t.Fatalf("herstellen: %d %+v", s, j)
	}
	j = waitJobLong(t, c, j.ID)
	var steps []string
	logs := map[string]string{}
	for _, st := range j.Steps {
		steps = append(steps, st.Name)
		logs[st.Name] = strings.Join(st.Log, "\n")
	}
	if j.Status != "succeeded" {
		t.Fatalf("taak: %s %s %v\n%v", j.Status, j.Error, steps, logs)
	}
	want := []string{"db1: Galera-adressen bijwerken", "db2: Galera-adressen bijwerken", "db3: Galera-adressen bijwerken", "db1: hangend proces stoppen",
		"Posities lezen", "db1: het cluster beginnen", "db2: aansluiten", "db3: aansluiten", "Controleren"}
	if !slices.Equal(steps, want) {
		t.Fatalf("stappen:\n%q\nverwacht:\n%q", steps, want)
	}
	if l := logs["Posities lezen"]; !strings.Contains(l, "db3: MariaDB stoppen om de positie te lezen") || !strings.Contains(l, "db1: seqno 42 uit recovery") ||
		!strings.Contains(l, "db2: seqno 41 uit grastate") || !strings.Contains(l, "db1 heeft de nieuwste data (seqno 42) en begint het cluster") {
		t.Errorf("posities:\n%s", l)
	}
	if l := logs["Controleren"]; !strings.Contains(l, "3 nodes Synced in één cluster") {
		t.Errorf("controle:\n%s", l)
	}
	d.mu.Lock()
	began := slices.Clone(d.began)
	d.mu.Unlock()
	if !slices.Equal(began, []string{"db1"}) {
		t.Fatalf("het cluster begon op %v", began)
	}
	for i, root := range d.roots {
		b, _ := os.ReadFile(filepath.Join(root, healGalera))
		if !strings.Contains(string(b), "wsrep_cluster_address = "+healNewAddr) || !strings.Contains(string(b), "wsrep_node_address = 192.168.10.18"+string(rune('1'+i))) {
			t.Errorf("db%d config:\n%s", i+1, b)
		}
		copies, _ := filepath.Glob(filepath.Join(root, healGalera+".cf-zelfherstel-*"))
		if len(copies) != 1 {
			t.Errorf("db%d kopieën: %v", i+1, copies)
		} else if b, _ := os.ReadFile(copies[0]); !strings.Contains(string(b), "192.168.0.190") {
			t.Errorf("db%d kopie:\n%s", i+1, b)
		}
	}
	if _, err := os.Stat(filepath.Join(d.roots[0], "proc/169")); err == nil {
		t.Error("mariadbd 169 draait nog op db1")
	}

	// Daarna is er niets meer mis, en toont de kaart het gelukte herstel.
	var last struct {
		LastRun *struct {
			JobID  string `json:"job_id"`
			Status string `json:"status"`
		} `json:"last_run"`
	}
	r = d.heal(t, e, c, func(r healView) bool { return !strings.Contains(r.codes(), "galera") })
	if r.codes() != "" || r.Plan != nil {
		t.Fatalf("na het herstel: %s %+v", r.codes(), r.Plan)
	}
	var l list[json.RawMessage]
	c.do("GET", "/api/v1/heal", nil, &l)
	for _, raw := range l.Items {
		var k struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal(raw, &k)
		if k.Key == r.Key {
			_ = json.Unmarshal(raw, &last)
		}
	}
	if last.LastRun == nil || last.LastRun.JobID != j.ID || last.LastRun.Status != "succeeded" {
		t.Fatalf("laatste herstel: %+v", last.LastRun)
	}

	// In het logboek, bij het cluster.
	rows, err := e.pool.Query(ctx, "SELECT action, subject_type, subject_id, job_ref FROM events WHERE action LIKE 'cluster.heal_%' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var evs []string
	for rows.Next() {
		var a, st, sid string
		var jr *string
		if err := rows.Scan(&a, &st, &sid, &jr); err != nil {
			t.Fatal(err)
		}
		if st != "cluster" || sid != d.cluster.ID || jr == nil || *jr != j.ID {
			t.Errorf("%s: %s %s %v", a, st, sid, jr)
		}
		evs = append(evs, a)
	}
	rows.Close()
	if !slices.Equal(evs, []string{"cluster.heal_started", "cluster.heal_done"}) {
		t.Fatalf("events: %v", evs)
	}
	var audit list[struct {
		Action  string `json:"action"`
		Summary string `json:"summary"`
	}]
	c.do("GET", "/api/v1/audit?cluster="+d.cluster.ID, nil, &audit)
	var sums []string
	for _, a := range audit.Items {
		if strings.HasPrefix(a.Action, "cluster.heal_") {
			sums = append(sums, a.Summary)
		}
	}
	if !slices.Contains(sums, "Zelfherstel van MariaDB van database cluster gelukt") ||
		!slices.ContainsFunc(sums, func(s string) bool {
			return strings.HasPrefix(s, "Zelfherstel gestart voor MariaDB van database cluster: db1, db2")
		}) {
		t.Errorf("logboek: %q", sums)
	}

	// Er is niets meer te herstellen.
	if s := run(c, hash, "", &apiErr); s != http.StatusConflict {
		t.Fatalf("niets te doen: %d", s)
	}

	// Een nieuwe stroomstoring: alles valt uit. Terwijl Jonas het plan
	// bekijkt, begint hij het cluster zelf op db3. Het herstel begint dan
	// geen tweede cluster, maar laat db1 en db2 bij db3 aansluiten.
	d.mu.Lock()
	d.members = nil
	d.mu.Unlock()
	for i, h := range d.hosts {
		h.Locked(func() { h.MariaDB.Down, h.MariaDB.Galera = true, nil })
		d.setUnit(i, agenttest.Unit{Failed: true})
		writeGrastate(h, -1, false)
	}
	r = d.heal(t, e, c, func(r healView) bool { return strings.Contains(r.codes(), "galera_no_primary") })
	if r.Plan == nil || r.Plan.Action != "galera" || !r.Plan.Auto || r.Plan.Blocked != "" {
		t.Fatalf("plan na de storing: %s %+v", r.codes(), r.Plan)
	}
	d.join(2, true)
	if s := run(c, r.Plan.Hash, "", &j); s != http.StatusAccepted {
		t.Fatalf("tweede herstel: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	steps, logs = nil, map[string]string{}
	for _, st := range j.Steps {
		steps = append(steps, st.Name)
		logs[st.Name] = strings.Join(st.Log, "\n")
	}
	if j.Status != "succeeded" || !slices.Equal(steps, []string{"Posities lezen", "db1: aansluiten", "db2: aansluiten", "db3: aansluiten", "Controleren"}) {
		t.Fatalf("tweede herstel: %s %s %q\n%v", j.Status, j.Error, steps, logs)
	}
	if l := logs["Posities lezen"] + logs["db3: aansluiten"]; !strings.Contains(l, "db3 zit in een Primary component; de andere nodes sluiten daar aan") ||
		!strings.Contains(l, "db3 doet al mee") {
		t.Errorf("posities:\n%s", l)
	}
	d.mu.Lock()
	began = slices.Clone(d.began)
	d.mu.Unlock()
	if !slices.Equal(began, []string{"db1", "db3"}) {
		t.Fatalf("het cluster begon op %v", began)
	}
}

// TestHealRunNoGaleraScripts: zoals DatabaseClusterNode1 op 19 oktober mist
// db1 galera_recovery en galera_new_cluster (MariaDB 12.3.2 zonder
// mariadb-server-galera). Zelfherstel meldt dat, leest de positie van db1
// met mariadbd --wsrep-recover en begint het cluster op db2, die de
// nieuwste data heeft; db1 sluit aan.
func TestHealRunNoGaleraScripts(t *testing.T) {
	e, c := adminClient(t)
	d := newHealDB(t, e, c, func(i int, h *agenttest.Host) {
		if i == 0 {
			h.MariaDB.Version, h.MariaDB.NoGaleraScripts, h.MariaDB.Recovered = "12.3.2-MariaDB", true, healUUID+":39"
		}
	})
	r := d.heal(t, e, c, func(r healView) bool {
		return strings.Contains(r.codes(), "galera_package@db1") && strings.Contains(r.codes(), "galera_no_primary@db1+db2+db3")
	})
	for _, p := range r.Problems {
		if p.Code == "galera_package" && (p.Title != "Op db1 ontbreekt het pakket mariadb-server-galera" || !strings.Contains(p.Why, "mariadbd --wsrep-recover")) {
			t.Errorf("probleem: %+v", p)
		}
	}
	if r.Plan == nil || r.Plan.Blocked != "" || r.Plan.Auto {
		t.Fatalf("plan: %+v", r.Plan)
	}
	var j job
	if s := c.do("POST", "/api/v1/heal/run", map[string]any{"key": r.Key, "hash": r.Plan.Hash}, &j); s != http.StatusAccepted {
		t.Fatalf("herstellen: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	logs := map[string]string{}
	for _, st := range j.Steps {
		logs[st.Name] = strings.Join(st.Log, "\n")
	}
	if j.Status != "succeeded" {
		t.Fatalf("taak: %s %s\n%v", j.Status, j.Error, logs)
	}
	if l := logs["Posities lezen"]; !strings.Contains(l, "db1: seqno 39 uit recovery met mariadbd --wsrep-recover (galera_recovery ontbreekt)") ||
		!strings.Contains(l, "db2: seqno 41 uit grastate") || !strings.Contains(l, "db2 heeft de nieuwste data (seqno 41) en begint het cluster") {
		t.Errorf("posities:\n%s", l)
	}
	if !slices.Contains(d.hosts[0].Calls(), "mariadbd --user=mysql --wsrep-recover --disable-log-error") {
		t.Errorf("db1 zonder mariadbd --wsrep-recover: %v", d.hosts[0].Calls())
	}
	d.mu.Lock()
	began := slices.Clone(d.began)
	d.mu.Unlock()
	if !slices.Equal(began, []string{"db2"}) {
		t.Fatalf("het cluster begon op %v", began)
	}
}

// TestHealRunSingle: een losse MariaDB zonder cluster die stilstaat, start
// Nu herstellen weer, met de events bij de node.
func TestHealRunSingle(t *testing.T) {
	// Een losse MariaDB, en een MySQL (mysql.service) zonder Galera.
	for _, tc := range []struct{ host, unit, version, title string }{
		{"mysql1", "mariadb", "11.8.3-MariaDB-0+deb13u1", "MariaDB staat stil op mysql1"},
		{"web1", "mysql", "8.0.43-0ubuntu0.24.04.2", "MySQL staat stil op web1"},
	} {
		t.Run(tc.unit, func(t *testing.T) { healRunSingle(t, tc.host, tc.unit, tc.version, tc.title) })
	}
}

func healRunSingle(t *testing.T, host, unit, version, title string) {
	e, c := adminClient(t)
	ctx := context.Background()
	var n node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": host}, &n); s != http.StatusCreated {
		t.Fatalf("node: %d", s)
	}
	a := &addrs{}
	a.set("10.0.0.50")
	var h *agenttest.Host
	startAgentWith(t, e, c, n.ID, strings.Repeat("c", 32), a, func(ag *agent.Agent, root string) {
		h = agenttest.NewHost(root, "10.0.0.50")
		ag.Exec, ag.Root, ag.Collector.Run = h.Exec, root, h.Exec
		h.MariaDB = &agenttest.MariaDB{Version: version, MaxConnections: 151, Down: true}
		h.SetUnit(unit, agenttest.Unit{Enabled: true, Failed: true})
		start := "systemctl reset-failed " + unit + " 2>/dev/null; systemctl start --no-block " + unit
		if unit == "mariadb" {
			start = "systemctl unset-environment _WSREP_NEW_CLUSTER; " + start
		}
		h.Shell = func(cmd string) (bool, []byte, error) {
			switch cmd {
			case "systemctl is-active --quiet " + unit:
				if u, _ := h.Unit(unit); u.Active {
					return true, nil, nil
				}
				return true, nil, errors.New("exit status 3")
			case start:
				h.SetUnit(unit, agenttest.Unit{Enabled: true, Active: true})
				h.Locked(func() { h.MariaDB.Down, h.MariaDB.Uptime = false, 1 })
				return true, nil, nil
			}
			return false, nil, nil
		}
	})
	key := "mariadb/node/" + n.ID
	var r healView
	eventually(t, "Zelfherstel ziet "+host, func() bool {
		if err := e.dbs.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.heal.InspectAll(ctx); err != nil {
			t.Fatal(err)
		}
		var l list[healView]
		c.do("GET", "/api/v1/heal", nil, &l)
		for _, x := range l.Items {
			if x.Key == key && x.codes() == "unit_stopped@"+host {
				r = x
				return true
			}
		}
		return false
	})
	if r.Problems[0].Title != title {
		t.Errorf("probleem: %+v", r.Problems[0])
	}
	if r.Plan == nil || r.Plan.Action != "restart" || !r.Plan.Auto {
		t.Fatalf("plan: %+v", r.Plan)
	}
	var j job
	if s := c.do("POST", "/api/v1/heal/run", map[string]any{"key": key, "hash": r.Plan.Hash}, &j); s != http.StatusAccepted {
		t.Fatalf("herstellen: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || len(j.Steps) != 1 || j.Steps[0].Name != host+": "+unit+" starten" || j.NodeID == nil || *j.NodeID != n.ID {
		t.Fatalf("taak: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	if l := strings.Join(j.Steps[0].Log, "\n"); !strings.Contains(l, unit+" draait") {
		t.Errorf("log:\n%s", l)
	}
	var evs []string
	rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE '%.heal_%' AND subject_type = 'node' AND subject_id = $1 ORDER BY id", n.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		evs = append(evs, a)
	}
	rows.Close()
	if !slices.Equal(evs, []string{"node.heal_started", "node.heal_done"}) {
		t.Fatalf("events: %v", evs)
	}
}

// crash laat alle drie de nodes tegelijk uitvallen: MariaDB draait nergens
// en de positie zit alleen nog in InnoDB.
func (d *healDB) crash() {
	d.mu.Lock()
	d.members = nil
	d.mu.Unlock()
	for i, h := range d.hosts {
		h.Locked(func() { h.MariaDB.Down, h.MariaDB.Galera = true, nil })
		d.setUnit(i, agenttest.Unit{Failed: true})
		writeGrastate(h, -1, false)
	}
}

// TestHealAuto is de acceptatie van mijlpaal 3 van fase 7: Vanzelf
// herstellen staat uit tot een beheerder het per cluster aanzet, kan niet
// aan op prod, wacht tot een probleem lang genoeg bestaat, begint het
// cluster hoogstens eens per uur opnieuw, wacht na een mislukte poging en
// pauzeert na drie mislukte pogingen op rij.
func TestHealAuto(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	d := newHealDB(t, e, c)
	// De adressen kloppen al en niets hangt: het plan mag vanzelf.
	for i, root := range d.roots {
		writeFile(t, filepath.Join(root, healGalera), "[galera]\nwsrep_on = ON\nwsrep_cluster_name = db\nwsrep_cluster_address = "+healNewAddr+
			"\nwsrep_node_address = 192.168.10.18"+string(rune('1'+i))+"\n", 0o644)
		d.hosts[i].SetJournal("mariadb.service", "")
	}
	_ = os.RemoveAll(filepath.Join(d.roots[0], "proc/169"))
	type autoView struct {
		Auto *struct {
			Enabled      bool    `json:"enabled"`
			PausedAt     *string `json:"paused_at"`
			PausedReason string  `json:"paused_reason"`
			Allowed      bool    `json:"allowed"`
			Due          bool    `json:"due"`
			Note         string  `json:"note"`
		} `json:"auto"`
	}
	auto := func() autoView {
		t.Helper()
		var l list[json.RawMessage]
		c.do("GET", "/api/v1/heal", nil, &l)
		for _, raw := range l.Items {
			var k struct {
				Key string `json:"key"`
			}
			_ = json.Unmarshal(raw, &k)
			if k.Key == "mariadb/"+d.cluster.ID {
				var v autoView
				_ = json.Unmarshal(raw, &v)
				if v.Auto == nil {
					t.Fatal("geen auto bij een cluster")
				}
				return v
			}
		}
		t.Fatal("geen rapport")
		return autoView{}
	}
	autoJobs := func() []job {
		t.Helper()
		rows, err := e.pool.Query(ctx, "SELECT id FROM jobs WHERE kind = 'heal.run' AND params->>'auto' = 'true' ORDER BY created_at")
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		var out []job
		for _, id := range ids {
			var j job
			c.do("GET", "/api/v1/jobs/"+id, nil, &j)
			out = append(out, j)
		}
		return out
	}
	autoRun := func() {
		t.Helper()
		if err := e.heal.InspectAll(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.heal.AutoRun(ctx); err != nil {
			t.Fatalf("vanzelf: %v", err)
		}
	}
	put := func(c *client, on bool, out any) int {
		t.Helper()
		return c.do("PUT", "/api/v1/clusters/"+d.cluster.ID+"/heal-auto", map[string]any{"enabled": on}, out)
	}

	r := d.heal(t, e, c, func(r healView) bool { return strings.Contains(r.codes(), "galera_no_primary@db1+db2+db3") })
	if r.Plan == nil || !r.Plan.Auto || strings.Contains(r.codes(), "wrong_address") || strings.Contains(r.codes(), "hung") {
		t.Fatalf("plan: %s %+v", r.codes(), r.Plan)
	}
	// Standaard uit: niets gebeurt vanzelf.
	if a := auto().Auto; a.Enabled || !a.Allowed || a.Due || !strings.Contains(a.Note, "Vanzelf herstellen staat uit") {
		t.Fatalf("standaard: %+v", a)
	}
	autoRun()
	if j := autoJobs(); len(j) != 0 {
		t.Fatalf("vanzelf terwijl het uit staat: %+v", j)
	}

	// Een kijker mag het niet aanzetten, en op prod kan het niet.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := put(v, true, nil); s != http.StatusForbidden {
		t.Fatalf("kijker: %d", s)
	}
	c.do("PATCH", "/api/v1/clusters/"+d.cluster.ID, map[string]any{"environment": "prod"}, nil)
	if s := put(c, true, nil); s != http.StatusConflict {
		t.Fatalf("prod: %d", s)
	}
	if a := auto().Auto; a.Allowed || a.Due || !strings.Contains(a.Note, "Op prod") {
		t.Fatalf("prod: %+v", a)
	}
	c.do("PATCH", "/api/v1/clusters/"+d.cluster.ID, map[string]any{"environment": "lab"}, nil)

	// Aan, maar het probleem moet eerst lang genoeg bestaan.
	e.heal.AutoAfter = time.Hour
	var set struct {
		Enabled bool `json:"enabled"`
	}
	if s := put(c, true, &set); s != http.StatusOK || !set.Enabled {
		t.Fatalf("aanzetten: %d %+v", s, set)
	}
	autoRun()
	if j := autoJobs(); len(j) != 0 {
		t.Fatalf("vanzelf te vroeg: %+v", j)
	}
	if a := auto().Auto; !a.Enabled || a.Due || !strings.Contains(a.Note, "wacht tot het probleem 60 min bestaat") {
		t.Fatalf("wachten: %+v", a)
	}
	e.heal.AutoAfter = 0
	if a := auto().Auto; !a.Due {
		t.Fatalf("nu: %+v", a)
	}
	autoRun()
	jobs := autoJobs()
	if len(jobs) != 1 || jobs[0].Title != "Zelfherstel (vanzelf): MariaDB van database cluster" || jobs[0].RequestedBy != nil {
		t.Fatalf("vanzelf: %+v", jobs)
	}
	if j := waitJobLong(t, c, jobs[0].ID); j.Status != "succeeded" {
		t.Fatalf("vanzelf herstel: %s %s", j.Status, j.Error)
	}
	d.mu.Lock()
	began := slices.Clone(d.began)
	d.mu.Unlock()
	if !slices.Equal(began, []string{"db1"}) {
		t.Fatalf("het cluster begon op %v", began)
	}

	// Weer alles uit binnen het uur: het cluster begint niet nog eens
	// vanzelf.
	d.crash()
	d.heal(t, e, c, func(r healView) bool { return strings.Contains(r.codes(), "galera_no_primary@db1+db2+db3") })
	autoRun()
	if j := autoJobs(); len(j) != 1 {
		t.Fatalf("tweede keer beginnen: %d taken", len(j))
	}
	if a := auto().Auto; a.Due || !strings.Contains(a.Note, "begon het afgelopen uur al vanzelf opnieuw") {
		t.Fatalf("eens per uur: %+v", a)
	}

	// Na een mislukte poging wacht het; na drie op rij pauzeert het.
	fail := func() {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (kind, title, status, params, cluster_id, error, started_at, finished_at)
			VALUES ('heal.run', 'Zelfherstel (vanzelf): MariaDB van database cluster', 'failed', $1, $2, 'db2 sloot niet aan', now(), now())`,
			`{"auto": true, "key": "mariadb/`+d.cluster.ID+`"}`, d.cluster.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Zonder bootstrap-probleem: db3 draait weer als Primary, db1 en db2
	// staan stil.
	d.join(2, true)
	d.heal(t, e, c, func(r healView) bool {
		return !strings.Contains(r.codes(), "galera_no_primary") && strings.Contains(r.codes(), "unit_stopped@db1") && strings.Contains(r.codes(), "unit_stopped@db2")
	})
	fail()
	autoRun()
	if a := auto().Auto; a.Due || !strings.Contains(a.Note, "De vorige poging mislukte") {
		t.Fatalf("na een mislukte poging: %+v", a)
	}
	fail()
	fail()
	autoRun()
	a := auto().Auto
	if a.PausedAt == nil || !strings.Contains(a.PausedReason, "3 pogingen op rij mislukten") || a.Due {
		t.Fatalf("pauze: %+v", a)
	}
	if j := autoJobs(); len(j) != 4 {
		t.Fatalf("taken na de pauze: %d", len(j))
	}
	// Opnieuw aanzetten heft de pauze op en begint de telling opnieuw.
	if s := put(c, true, nil); s != http.StatusOK {
		t.Fatalf("opnieuw aan: %d", s)
	}
	if a := auto().Auto; a.PausedAt != nil || !a.Due {
		t.Fatalf("opnieuw aan: %+v", a)
	}
	autoRun()
	jobs = autoJobs()
	if len(jobs) != 5 {
		t.Fatalf("na opnieuw aanzetten: %d taken", len(jobs))
	}
	if j := waitJobLong(t, c, jobs[4].ID); j.Status != "succeeded" {
		t.Fatalf("na opnieuw aanzetten: %s %s", j.Status, j.Error)
	}

	var evs []string
	rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE 'cluster.heal_auto_%' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		evs = append(evs, a)
	}
	rows.Close()
	if !slices.Equal(evs, []string{"cluster.heal_auto_changed", "cluster.heal_auto_paused", "cluster.heal_auto_changed"}) {
		t.Fatalf("events: %v", evs)
	}
}
