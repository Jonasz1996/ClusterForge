package components

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// galeraSpec is Nextcloud met nc1 tot nc3 als datanodes, of met nc1 en
// nc2 en een scheidsrechter.
func galeraSpec(t *testing.T, arbiter bool) (*templates.Template, Spec, map[uuid.UUID]deploy.NodeState) {
	t.Helper()
	tpl, ok := registry.Latest("mariadb-galera")
	if !ok {
		t.Fatal("mariadb-galera ontbreekt")
	}
	spec := Spec{
		Template: deploy.SpecTemplate{Name: tpl.Name, Version: tpl.Version},
		Cluster:  templates.ClusterInfo{Name: "Nextcloud", Slug: "nextcloud", Environment: "prod"},
		Params:   map[string]any{"buffer_pool": "1G", "max_connections": 300}, Secrets: tpl.Secrets(),
	}
	states := map[uuid.UUID]deploy.NodeState{}
	for i, h := range []string{"nc1", "nc2", "nc3"} {
		id := uuid.New()
		addr := "10.0.20." + []string{"186", "187", "188"}[i]
		role, index := RoleData, i+1
		if arbiter && i == 2 {
			role, index = RoleArbiter, 1
		}
		spec.Nodes = append(spec.Nodes, Node{NodeID: id, Hostname: h, Role: role, Index: index})
		states[id] = deploy.NodeState{PrimaryIP: addr, Facts: &protocol.Facts{
			Interfaces: []protocol.Interface{{Name: "eth0", Addresses: []string{addr + "/24"}}},
		}}
	}
	return tpl, spec, states
}

func render(t *testing.T, tpl *templates.Template, spec Spec, states map[uuid.UUID]deploy.NodeState, i int) []templates.Step {
	t.Helper()
	steps, err := deploy.RenderNode(tpl, spec.deploySpec(), map[string]string{}, states, spec.Nodes[i].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func file(steps []templates.Step, path string) string {
	for _, s := range steps {
		if s.File != nil && s.File.Path == path {
			return s.File.Content
		}
	}
	return ""
}

// TestGaleraConfig controleert de gerenderde configuratie van een node: de
// adressen van de datanodes, de naam van het cluster, SST met de eigen
// gebruiker en de instellingen uit het plan.
func TestGaleraConfig(t *testing.T) {
	tpl, spec, states := galeraSpec(t, false)
	steps := render(t, tpl, spec, states, 1)
	cnf := file(steps, "/etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf")
	for _, want := range []string{
		"wsrep_cluster_name = nextcloud\n",
		"wsrep_cluster_address = gcomm://10.0.20.186,10.0.20.187,10.0.20.188\n",
		"wsrep_node_address = 10.0.20.187\n",
		"wsrep_node_name = nc2\n",
		"wsrep_sst_method = mariabackup\n",
		"wsrep_sst_auth = mysql:\n",
		"binlog_format = ROW\n",
		"innodb_autoinc_lock_mode = 2\n",
		"innodb_buffer_pool_size = 1G\n",
		"max_connections = 300\n",
		"bind-address = 0.0.0.0\n",
	} {
		if !strings.Contains(cnf, want) {
			t.Errorf("99-clusterforge-galera.cnf mist %q:\n%s", want, cnf)
		}
	}
	for _, s := range steps {
		if s.File != nil && s.File.Path == "/etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf" && (s.File.Mode != "0644" || len(s.Notify) > 0) {
			// Een herstart na een nieuwe configuratie zou de eerste node uit
			// het cluster halen dat hij net begon; de taak herstart zelf.
			t.Errorf("99-clusterforge-galera.cnf: modus %s, notify %v", s.File.Mode, s.Notify)
		}
	}
	if len(tpl.Secrets()) > 0 {
		// SST gaat via de socket; er hoort geen wachtwoord op de node.
		t.Errorf("geheimen: %v", tpl.Secrets())
	}
	join := steps[slicesIndex(steps, func(s templates.Step) bool { return s.Command != nil && s.Command.Creates == Marker("nextcloud") })]
	if join.Command.Run != "systemctl restart mariadb && touch /var/lib/clusterforge/galera-nextcloud" {
		t.Errorf("aansluiten: %q", join.Command.Run)
	}
	sst := steps[len(steps)-1].Command
	if sst == nil || !strings.Contains(sst.Run, "IDENTIFIED VIA unix_socket") || !strings.Contains(sst.Unless, "SHOW GRANTS FOR 'mysql'@'localhost'") {
		t.Errorf("SST-gebruiker: %+v", sst)
	}
}

// TestArbiter: met twee datanodes en een scheidsrechter kent het cluster
// alleen de datanodes, en garbd wijst ernaar.
func TestArbiter(t *testing.T) {
	tpl, spec, states := galeraSpec(t, true)
	cnf := file(render(t, tpl, spec, states, 0), "/etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf")
	if !strings.Contains(cnf, "gcomm://10.0.20.186,10.0.20.187\n") {
		t.Errorf("datanodes:\n%s", cnf)
	}
	steps := render(t, tpl, spec, states, 2)
	garb := file(steps, "/etc/default/garb")
	if !strings.Contains(garb, `GALERA_NODES="10.0.20.186:4567 10.0.20.187:4567"`) || !strings.Contains(garb, `GALERA_GROUP="nextcloud"`) {
		t.Errorf("garb:\n%s", garb)
	}
	if steps[0].Package == nil || steps[0].Package.Names[0] != "galera-arbitrator-4" || steps[len(steps)-1].Service.Name != "garb" {
		t.Errorf("stappen van de scheidsrechter: %+v", steps)
	}
	if got := spec.order(); got[0].Hostname != "nc1" || got[1].Hostname != "nc2" || got[2].Role != RoleArbiter {
		t.Errorf("volgorde: %+v", got)
	}
}

// TestBootstrap: alleen de eerste node van een nieuw cluster begint het, en
// een node die al Primary is, krijgt alleen het merkteken.
func TestBootstrap(t *testing.T) {
	tpl, spec, states := galeraSpec(t, false)
	steps := render(t, tpl, spec, states, 0)
	out, err := bootstrap(steps, "nextcloud", false)
	if err != nil {
		t.Fatal(err)
	}
	i := slicesIndex(out, func(s templates.Step) bool { return s.Command != nil && s.Command.Creates == Marker("nextcloud") })
	if out[i].Command.Run != "systemctl stop mariadb && galera_new_cluster && touch /var/lib/clusterforge/galera-nextcloud" {
		t.Errorf("beginnen: %q", out[i].Command.Run)
	}
	if steps[i].Command.Run == out[i].Command.Run {
		t.Error("bootstrap veranderde de stappen van de aanroeper")
	}
	out, _ = bootstrap(steps, "nextcloud", true)
	if out[i].Command.Run != "touch /var/lib/clusterforge/galera-nextcloud" {
		t.Errorf("al Primary: %q", out[i].Command.Run)
	}
	if _, err := bootstrap(steps, "ander", false); err == nil {
		t.Error("een ander merkteken gaf geen fout")
	}
}

// TestServices: MariaDB komt in de graaf met een soort die deps kent.
func TestServices(t *testing.T) {
	for _, tpl := range registry.All() {
		svcs, err := tpl.RenderServices(map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range svcs {
			if !deps.KnownKind(s.Kind) {
				t.Errorf("%s: dienst %s met onbekende soort %s", tpl.Name, s.Name, s.Kind)
			}
		}
	}
	if Templates[protocol.DBMariaDB] != "mariadb-galera" {
		t.Error("geen onderdeel voor MariaDB")
	}
}

// TestHostsAndMemory: twee van de drie stemmen op één Proxmox-host is een
// waarschuwing, en te weinig geheugen geeft het commando voor de CT.
func TestHostsAndMemory(t *testing.T) {
	_, spec, _ := galeraSpec(t, false)
	host := func(name string) *string { return &name }
	byID := map[uuid.UUID]store.ListComponentCandidatesRow{
		spec.Nodes[0].NodeID: {PveNode: host("pve1"), ProxmoxName: host("thuis")},
		spec.Nodes[1].NodeID: {PveNode: host("pve1"), ProxmoxName: host("thuis")},
		spec.Nodes[2].NodeID: {PveNode: host("pve2"), ProxmoxName: host("thuis")},
	}
	var got []Finding
	add := func(level, code string, n *Node, msg, command string) {
		got = append(got, Finding{Level: level, Code: code, Message: msg, Command: command})
	}
	checkHosts(spec, byID, add)
	if len(got) != 1 || got[0].Code != "same_host" || !strings.HasPrefix(got[0].Message, "nc1 en nc2 staan op dezelfde Proxmox-host pve1") {
		t.Fatalf("hosts: %+v", got)
	}
	byID[spec.Nodes[1].NodeID] = store.ListComponentCandidatesRow{PveNode: host("pve3"), ProxmoxName: host("thuis")}
	got = nil
	checkHosts(spec, byID, add)
	if len(got) != 0 {
		t.Fatalf("drie hosts: %+v", got)
	}

	// 4 GiB, waarvan de app 80% gebruikt; de database vraagt 1G + 300M.
	s := &Service{}
	n := spec.Nodes[0]
	vmid := int32(186)
	r := store.ListComponentCandidatesRow{PveType: host("lxc"), PveNode: host("pve1"), PveVmid: &vmid}
	pn := PlanNode{}
	got = nil
	s.checkMemory(&pn, &n, r, &protocol.Facts{MemoryBytes: 4 << 30}, 1<<30, map[uuid.UUID]float64{n.NodeID: 80}, add)
	// (3.2G + 1.29G) / 0.85 = 5.28G, afgerond op 512 MiB is 5632 MiB.
	if len(got) != 1 || got[0].Command != "pct set 186 --memory 5632" || pn.FreeAfterPct == nil || *pn.FreeAfterPct > -12 {
		t.Fatalf("geheugen: %+v, over %v", got, pn.FreeAfterPct)
	}
	got = nil
	s.checkMemory(&pn, &n, r, &protocol.Facts{MemoryBytes: 16 << 30}, 1<<30, map[uuid.UUID]float64{n.NodeID: 30}, add)
	if len(got) != 0 {
		t.Fatalf("genoeg geheugen: %+v", got)
	}

	if !under("/var/lib/mysql", "/") || !under("/var/lib/mysql", "/var") || under("/var/lib/mysql", "/var/lib/my") {
		t.Error("under")
	}
}

func slicesIndex[T any](s []T, f func(T) bool) int {
	for i, x := range s {
		if f(x) {
			return i
		}
	}
	return -1
}
