package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
)

type joinPlanView struct {
	Engine     string `json:"engine"`
	Revision   int    `json:"revision"`
	Candidates []struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
	} `json:"candidates"`
	Node *struct {
		Hostname string   `json:"hostname"`
		Index    int      `json:"index"`
		Address  string   `json:"address"`
		Steps    []string `json:"steps"`
	} `json:"node"`
	Members []struct {
		Hostname string   `json:"hostname"`
		Healthy  bool     `json:"healthy"`
		Writer   bool     `json:"writer"`
		Changes  []string `json:"changes"`
	} `json:"members"`
	VIP      string             `json:"vip"`
	VIPJoins bool               `json:"vip_joins"`
	Votes    int                `json:"votes"`
	Effects  []string           `json:"effects"`
	Findings []componentFinding `json:"findings"`
	Blocked  bool               `json:"blocked"`
	Hash     string             `json:"hash"`
}

// changes zijn de wijzigingen per node die er al was, als "nc1: a; b".
func (p joinPlanView) changes() map[string]string {
	out := map[string]string{}
	for _, m := range p.Members {
		out[m.Hostname] = strings.Join(m.Changes, "; ")
	}
	return out
}

// callsSince zijn de commando's op h na de eerste n.
func callsSince(h *agenttest.Host, n int) []string { return h.Calls()[n:] }

// TestDatabaseJoinGalera is de acceptatie van Node toevoegen bij MariaDB
// Galera (mijlpaal 11 van fase 5): nc4, een node van het cluster zonder
// database, krijgt MariaDB en sluit aan; nc1 tot nc3 krijgen hem in hun
// configuratie, maar herstarten niet.
func TestDatabaseJoinGalera(t *testing.T) {
	lab := newGaleraLab(t, "nc4")
	e, c, g := lab.e, lab.c, lab.g
	url := lab.base + "/" + lab.cid
	plan := func(node string) joinPlanView {
		t.Helper()
		in := map[string]any{}
		if node != "" {
			in["node_id"] = lab.ids[node]
		}
		var p joinPlanView
		if s := c.do("POST", url+"/join/plan", in, &p); s != http.StatusOK {
			t.Fatalf("plan: %d", s)
		}
		return p
	}

	p := plan("")
	if p.Engine != "mariadb" || len(p.Candidates) != 1 || p.Candidates[0].Hostname != "nc4" || p.Node != nil || !p.Blocked || codesOf(p.Findings) != "blocked:choose" {
		t.Fatalf("plan zonder node: %+v", p)
	}
	var ae apiErr
	if s := c.do("POST", url+"/join/plan", map[string]any{"node_id": lab.ids["nc2"]}, &ae); s != http.StatusBadRequest || !strings.Contains(ae.Message, "nc2 hoort al bij de database") {
		t.Fatalf("nc2: %d %+v", s, ae)
	}

	// Zolang nc3 niet meedoet, kan het niet.
	g.leave("nc3", g.hosts["nc3"])
	if p = plan("nc4"); !p.Blocked || !strings.Contains(codesOf(p.Findings), "blocked:not_healthy") {
		t.Fatalf("met nc3 weg: %+v", p.Findings)
	}
	g.join("nc3", g.hosts["nc3"], false)

	p = plan("nc4")
	if p.Blocked || p.Node == nil || p.Node.Hostname != "nc4" || p.Node.Index != 4 || p.Node.Address != "10.0.20.189" || p.Votes != 4 || codesOf(p.Findings) != "warning:even" {
		t.Fatalf("plan: %+v", p)
	}
	if !slices.Contains(p.Node.Steps, "bij het Galera-cluster aansluiten") || slices.ContainsFunc(p.Node.Steps, func(s string) bool { return strings.Contains(s, "beginnen") }) {
		t.Errorf("stappen van nc4: %v", p.Node.Steps)
	}
	for _, name := range []string{"nc1", "nc2", "nc3"} {
		if got := p.changes()[name]; got != "bestand /etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf" {
			t.Errorf("%s: %q", name, got)
		}
	}
	if eff := strings.Join(p.Effects, "\n"); !strings.Contains(eff, "nc4 krijgt MariaDB en haalt alle data bij een node die meedoet") ||
		!strings.Contains(eff, "nc1, nc2 en nc3 krijgen nc4 in hun configuratie voor de volgende start; MariaDB herstart daar niet") {
		t.Errorf("gevolgen:\n%s", eff)
	}

	in := map[string]any{"node_id": lab.ids["nc4"], "hash": "oud"}
	if s := c.do("POST", url+"/join", in, &ae); s != http.StatusConflict || ae.Code != "plan_stale" {
		t.Fatalf("oude hash: %d %+v", s, ae)
	}
	before := map[string]int{}
	for name, h := range g.hosts {
		before[name] = len(h.Calls())
	}
	in["hash"] = p.Hash
	var j job
	if s := c.do("POST", url+"/join", in, &j); s != http.StatusAccepted || j.Title != "Node toevoegen: nc4 aan MariaDB Galera op Nextcloud" {
		t.Fatalf("toevoegen: %d %+v", s, j)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), []string{"nc1: MariaDB", "nc2: MariaDB", "nc3: MariaDB", "nc4: MariaDB", "Controleren"}) {
		t.Fatalf("taak: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}

	// nc1 tot nc3 kennen nc4 nu, zonder herstart; nc4 kent ze allemaal.
	for _, name := range []string{"nc1", "nc2", "nc3", "nc4"} {
		h := g.hosts[name]
		b, _ := os.ReadFile(filepath.Join(h.Root, "etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf"))
		if !strings.Contains(string(b), "wsrep_cluster_address = gcomm://10.0.20.186,10.0.20.187,10.0.20.188,10.0.20.189") {
			t.Errorf("%s: %s", name, b)
		}
		if name != "nc4" && slices.ContainsFunc(callsSince(h, before[name]), func(c string) bool { return strings.Contains(c, "restart mariadb") }) {
			t.Errorf("%s herstartte: %v", name, callsSince(h, before[name]))
		}
	}
	if log := stepLogOf(j, "nc2: MariaDB"); !strings.Contains(log, "nc2 krijgt de nieuwe node in zijn configuratie voor de volgende start; MariaDB herstart niet") {
		t.Errorf("nc2:\n%s", log)
	}
	if log := stepLogOf(j, "nc4: MariaDB"); !strings.Contains(log, "nc4: Synced, Primary, 4 leden") {
		t.Errorf("nc4:\n%s", log)
	}
	if log := stepLogOf(j, "Controleren"); !strings.Contains(log, "alle 4 datanodes Synced in één cluster met 4 leden") {
		t.Errorf("controleren:\n%s", log)
	}
	var comps list[componentView]
	c.do("GET", lab.base, nil, &comps)
	comp := comps.Items[0]
	if comp.Revision != 2 || comp.AppliedRevision == nil || *comp.AppliedRevision != 2 || len(comp.Nodes) != 4 || comp.Nodes[3].Hostname != "nc4" || comp.Nodes[3].Index != 4 {
		t.Errorf("onderdeel: %+v", comp)
	}
	if ev := clusterEvents(t, e, lab.cluster.ID, "cluster.database_node_added"); len(ev) != 1 || !strings.Contains(ev[0], `"node": "nc4"`) || !strings.Contains(ev[0], `"revision": 2`) {
		t.Errorf("events: %v", ev)
	}
	if p = plan(""); len(p.Candidates) != 0 || codesOf(p.Findings) != "blocked:no_candidates" {
		t.Errorf("daarna: %+v", p)
	}
}

// etcdShell speelt etcdctl na op de nodes met PostgreSQL: wie lid is, en of
// etcd op een node meedoet.
type etcdShell struct {
	mu      sync.Mutex
	peers   []string
	addedOn []string
}

var (
	errFailed  = errors.New("exit status 1")
	etcdAdd    = regexp.MustCompile(`member add (\S+) --peer-urls=(\S+)$`)
	etcdMember = regexp.MustCompile(`member list \| grep -qF '(\S+)'$`)
)

func (s *etcdShell) shell(name string, h *agenttest.Host) func(cmd string) (bool, []byte, error) {
	return func(cmd string) (bool, []byte, error) {
		if !strings.Contains(cmd, "etcdctl") {
			return false, nil, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if m := etcdAdd.FindStringSubmatch(cmd); m != nil {
			s.peers = append(s.peers, m[2])
			s.addedOn = append(s.addedOn, name)
			return true, []byte("Member added to cluster\n"), nil
		}
		if m := etcdMember.FindStringSubmatch(cmd); m != nil {
			if slices.Contains(s.peers, m[1]) {
				return true, nil, nil
			}
			return true, nil, errFailed
		}
		if strings.Contains(cmd, "endpoint health") {
			if u, _ := h.Unit("etcd"); u.Active && slices.Contains(s.peers, "http://"+h.Address+":2380") {
				return true, nil, nil
			}
			return true, nil, errFailed
		}
		return true, []byte("etcdctl: onbekend in de test\n"), errFailed
	}
}

// TestDatabaseJoinPatroni is de acceptatie van Node toevoegen bij
// PostgreSQL met Patroni (mijlpaal 11 van fase 5): nc4 wordt eerst lid van
// etcd via nc1, sluit dan aan bij de leden die er zijn, en wordt een
// replica. Patroni op nc1 tot nc3 krijgt nc4 in pg_hba en herlaadt, zonder
// herstart; het eigen VIP krijgt nc4 als peer.
func TestDatabaseJoinPatroni(t *testing.T) {
	lab := newPatroniHosts(t, "nc4")
	e, c, g, nc := lab.e, lab.c, lab.g, lab.nc
	cid := lab.deploy(t)
	url := "/api/v1/clusters/" + nc.ID + "/components/" + cid
	eventually(t, "het VIP op nc1", func() bool { return slices.Contains(g.hosts["nc1"].Addresses(), "10.0.20.190") })
	// De taak zet het VIP als laatste op de node die het volgens de server
	// heeft; die moet dat dus eerst weten.
	eventually(t, "de server ziet het VIP op nc1", func() bool {
		var has bool
		_ = e.pool.QueryRow(context.Background(), "SELECT '10.0.20.190' = ANY(addresses) FROM node_status WHERE node_id = $1", lab.ids["nc1"]).Scan(&has)
		return has
	})
	etcd := &etcdShell{peers: []string{"http://10.0.20.186:2380", "http://10.0.20.187:2380", "http://10.0.20.188:2380"}}
	for name, h := range g.hosts {
		h.Locked(func() { h.Shell = etcd.shell(name, h) })
	}

	var p joinPlanView
	if s := c.do("POST", url+"/join/plan", map[string]any{"node_id": lab.ids["nc4"]}, &p); s != http.StatusOK {
		t.Fatalf("plan: %d", s)
	}
	if p.Blocked || p.Engine != "postgresql" || p.Node == nil || p.Votes != 4 || !p.VIPJoins || p.VIP != "10.0.20.190" || codesOf(p.Findings) != "warning:even" {
		t.Fatalf("plan: %+v", p)
	}
	ch := p.changes()
	if !strings.HasPrefix(ch["nc1"], "etcd: nc4 als lid toevoegen; ") || strings.Contains(ch["nc2"], "etcd: nc4") {
		t.Errorf("lid van etcd via nc1: %v", ch)
	}
	for _, name := range []string{"nc1", "nc2", "nc3"} {
		for _, want := range []string{"bestand /etc/etcd/clusterforge.yml", "bestand /etc/patroni/clusterforge.yml", "nc4 als peer", "keepalived herladen"} {
			if !strings.Contains(ch[name], want) {
				t.Errorf("%s zonder %q: %s", name, want, ch[name])
			}
		}
	}
	if !slices.Contains(p.Node.Steps, "pakket keepalived") || !slices.Contains(p.Node.Steps, "etcd starten") {
		t.Errorf("stappen van nc4: %v", p.Node.Steps)
	}
	if eff := strings.Join(p.Effects, "\n"); !strings.Contains(eff, "ClusterForge maakt nc4 lid van etcd via nc1") || !strings.Contains(eff, "nc4 wordt een replica") {
		t.Errorf("gevolgen:\n%s", eff)
	}

	var j job
	if s := c.do("POST", url+"/join", map[string]any{"node_id": lab.ids["nc4"], "hash": p.Hash}, &j); s != http.StatusAccepted ||
		j.Title != "Node toevoegen: nc4 aan PostgreSQL met Patroni op Nextcloud" {
		t.Fatalf("toevoegen: %d %+v", s, j)
	}
	j = waitJobLong(t, c, j.ID)
	want := []string{"nc1: etcd", "nc2: etcd", "nc3: etcd", "nc4: etcd", "nc1: PostgreSQL", "nc2: PostgreSQL", "nc3: PostgreSQL", "nc4: PostgreSQL",
		"nc2: VIP 10.0.20.190", "nc3: VIP 10.0.20.190", "nc4: VIP 10.0.20.190", "nc1: VIP 10.0.20.190", "Controleren"}
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), want) {
		t.Fatalf("taak: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if !slices.Equal(etcd.addedOn, []string{"nc1"}) {
		t.Errorf("lid van etcd via %v", etcd.addedOn)
	}
	if log := stepLogOf(j, "nc4: etcd"); !strings.Contains(log, "nc4 is lid van etcd, toegevoegd via nc1") || !strings.Contains(log, "etcd doet mee op nc4") {
		t.Errorf("nc4 etcd:\n%s", log)
	}
	read := func(name, path string) string {
		b, _ := os.ReadFile(filepath.Join(g.hosts[name].Root, path))
		return string(b)
	}
	if conf := read("nc4", "etc/etcd/clusterforge.yml"); !strings.Contains(conf, "initial-cluster-state: existing") || !strings.Contains(conf, ",nc4=http://10.0.20.189:2380") {
		t.Errorf("etcd van nc4:\n%s", conf)
	}
	if conf := read("nc1", "etc/etcd/clusterforge.yml"); !strings.Contains(conf, "initial-cluster-state: new") || !strings.Contains(conf, ",nc4=http://10.0.20.189:2380") {
		t.Errorf("etcd van nc1:\n%s", conf)
	}
	if conf := read("nc1", "etc/patroni/clusterforge.yml"); !strings.Contains(conf, "host replication cf_replicatie 10.0.20.189/32 scram-sha-256") {
		t.Errorf("patroni van nc1:\n%s", conf)
	}
	if conf := read("nc1", "etc/keepalived/conf.d/clusterforge-postgresql.conf"); !strings.Contains(conf, "        10.0.20.189\n") {
		t.Errorf("keepalived van nc1:\n%s", conf)
	}
	g.mu.Lock()
	restarts, primary := slices.Clone(g.restarts), g.primary
	g.mu.Unlock()
	if len(restarts) != 0 || primary != "nc1" {
		t.Errorf("herstarts %v, primary %s", restarts, primary)
	}
	var role string
	g.hosts["nc4"].Locked(func() { role = g.hosts["nc4"].Patroni.Role })
	if role != "replica" {
		t.Errorf("nc4 is %s", role)
	}
	if !slices.Contains(g.hosts["nc1"].Addresses(), "10.0.20.190") {
		t.Errorf("het VIP staat niet meer op nc1")
	}
	if ev := clusterEvents(t, e, nc.ID, "cluster.database_node_added"); len(ev) != 1 || !strings.Contains(ev[0], `"node": "nc4"`) {
		t.Errorf("events: %v", ev)
	}
}
