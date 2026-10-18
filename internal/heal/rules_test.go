package heal

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fixture is het databasecluster van Jonas op 18 oktober: Node1 en Node2
// zetten de unit op failed maar mariadbd blijft aansluiten op de oude
// adressen, Node3 draait zonder meerderheid.
type fixture struct {
	in    Input
	ids   []uuid.UUID
	hosts []string
}

func jonas() *fixture {
	f := &fixture{hosts: []string{"DatabaseClusterNode1", "DatabaseClusterNode2", "DatabaseClusterNode3"}}
	cid := uuid.New()
	g := databases.Group{ClusterID: &cid, ClusterName: "database cluster", ClusterSlug: "database-cluster", ClusterEnv: "lab", Engine: protocol.DBMariaDB, Kind: databases.KindGalera, Health: databases.HealthDown}
	f.in = Input{Nodes: map[uuid.UUID]Node{}, VIPs: []string{"192.168.10.180", "192.168.10.185"}}
	old := "gcomm://192.168.0.190,192.168.0.191,192.168.0.192"
	for i, h := range f.hosts {
		id := uuid.New()
		f.ids = append(f.ids, id)
		ip := "192.168.10.18" + string(rune('1'+i))
		srv := databases.Server{NodeID: id, Hostname: h, Unit: "mariadb", Active: "failed"}
		insp := &protocol.HealEngine{
			Engine: protocol.DBMariaDB, Unit: "mariadb", Active: "failed", Result: "resources", Enabled: "disabled", StateSeconds: 30 * 3600,
			Processes: []protocol.HealProcess{{PID: 169, Name: "mariadbd", Unit: "mariadb", AgeSeconds: 30 * 3600}},
			DataDir:   "/var/lib/mysql", DataFree: 100 << 30, DataTotal: 200 << 30,
			Galera: &protocol.HealGalera{On: true, ClusterName: "db", ClusterAddress: old, NodeAddress: "192.168.0.19" + string(rune('0'+i)),
				Settings:     []protocol.HealSetting{{Key: "wsrep_cluster_address", Value: old, File: "/etc/mysql/mariadb.conf.d/60-galera.cnf", Line: 5}},
				PeerTimeouts: []string{"192.168.0.191", "192.168.0.192"}},
		}
		addrs := []string{ip}
		if i == 2 {
			srv.Running, srv.Active = true, "active"
			srv.Galera = &protocol.DBGalera{ClusterName: "db", ClusterStatus: "non-Primary", State: "Initialized", ClusterSize: 1}
			insp.Active, insp.Result, insp.Processes[0].PID = "active", "success", 4242
			addrs = append(addrs, "192.168.10.180")
		}
		g.Servers = append(g.Servers, srv)
		f.in.Nodes[id] = Node{ID: id, Hostname: h, Protocol: protocol.Version, Online: true, PrimaryIP: "192.168.0.19" + string(rune('0'+i)), Addresses: addrs, Inspect: insp}
	}
	f.in.Group = g
	return f
}

func codes(r Report) string {
	var c []string
	for _, p := range r.Problems {
		c = append(c, p.Code+"@"+strings.Join(p.Nodes, "+"))
	}
	return strings.Join(c, " ")
}

func TestDiagnoseJonas(t *testing.T) {
	f := jonas()
	r := Diagnose(f.in)
	want := "process_hung@DatabaseClusterNode1 unit_disabled@DatabaseClusterNode1 process_hung@DatabaseClusterNode2 unit_disabled@DatabaseClusterNode2 " +
		"galera_wrong_address@DatabaseClusterNode1 galera_wrong_address@DatabaseClusterNode2 galera_wrong_address@DatabaseClusterNode3 " +
		"galera_no_primary@DatabaseClusterNode1+DatabaseClusterNode2+DatabaseClusterNode3"
	if got := codes(r); got != want {
		t.Errorf("problemen:\n%s\nverwacht:\n%s", got, want)
	}
	for _, p := range r.Problems {
		if p.Code == CodeWrongAddress && !strings.Contains(p.Why, "192.168.0.190, 192.168.0.191, 192.168.0.192") ||
			p.Code == CodeWrongAddress && !strings.Contains(p.Why, "De nodes hebben nu 192.168.10.181, 192.168.10.182, 192.168.10.183") {
			t.Errorf("waarom: %s", p.Why)
		}
		if p.Code == CodeProcessHung && !strings.Contains(p.Why, "pid 169") {
			t.Errorf("hangend: %s", p.Why)
		}
	}
	p := r.Plan
	if p == nil || p.Action != ActionGalera || p.Blocked != "" || p.Auto || !strings.Contains(p.AutoWhy, "nooit vanzelf") || len(p.Nodes) != 3 {
		t.Fatalf("plan: %+v", p)
	}
	for i, n := range p.Nodes {
		if !n.SetAddress || n.ClusterAddress != "gcomm://192.168.10.181,192.168.10.182,192.168.10.183" || n.NodeAddress != "192.168.10.18"+string(rune('1'+i)) ||
			!n.Start || n.Kill != (i < 2) || n.File != "/etc/mysql/mariadb.conf.d/60-galera.cnf" {
			t.Errorf("node %d: %+v", i, n)
		}
	}
	if len(p.Steps) != 5 || !strings.Contains(p.Steps[1], "DatabaseClusterNode1 (pid 169)") {
		t.Errorf("stappen: %q", p.Steps)
	}
	if again := Diagnose(jonas().in); again.Plan.Hash == p.Hash {
		t.Error("een ander cluster heeft dezelfde hash")
	}
}

func TestDiagnoseRestartAll(t *testing.T) {
	// Na een gewone herstart van alle nodes met de goede adressen: alles
	// gestopt, niets hangt.
	f := jonas()
	for i, id := range f.ids {
		n := f.in.Nodes[id]
		n.Inspect.Galera.ClusterAddress = "gcomm://192.168.10.181,192.168.10.182,192.168.10.183"
		n.Inspect.Galera.NodeAddress = ""
		n.Inspect.Galera.PeerTimeouts = nil
		n.Inspect.Active, n.Inspect.Processes = "inactive", nil
		f.in.Group.Servers[i].Running, f.in.Group.Servers[i].Galera = false, nil
		f.in.Nodes[id] = n
	}
	f.in.Group.Kind = databases.KindSingle
	r := Diagnose(f.in)
	if !strings.Contains(codes(r), "galera_no_primary") || strings.Contains(codes(r), "wrong_address") {
		t.Errorf("problemen: %s", codes(r))
	}
	if p := r.Plan; p == nil || p.Action != ActionGalera || !p.Auto || p.Blocked != "" {
		t.Errorf("plan: %+v", r.Plan)
	}

	// Antwoordt een node niet, dan kan beginnen niet.
	n := f.in.Nodes[f.ids[1]]
	n.Inspect, n.InspectError = nil, "de agent is niet verbonden"
	f.in.Nodes[f.ids[1]] = n
	r = Diagnose(f.in)
	if p := r.Plan; p == nil || p.Auto || !strings.Contains(p.Blocked, "DatabaseClusterNode2") || !strings.Contains(codes(r), "no_quorum") {
		t.Errorf("plan zonder Node2: %+v %s", r.Plan, codes(r))
	}
}

func TestDiagnoseJoinPrimary(t *testing.T) {
	// Node3 is Primary, Node1 staat stil: alleen starten, vanzelf mag.
	f := jonas()
	for i, id := range f.ids {
		n := f.in.Nodes[id]
		n.Inspect.Galera.ClusterAddress = "gcomm://192.168.10.181,192.168.10.182,192.168.10.183"
		n.Inspect.Galera.NodeAddress, n.Inspect.Galera.PeerTimeouts = "", nil
		if i == 0 {
			n.Inspect.Processes = nil
		} else {
			n.Inspect.Active = "active"
			f.in.Group.Servers[i].Running = true
			f.in.Group.Servers[i].Galera = &protocol.DBGalera{ClusterStatus: "Primary", State: "Synced", StateCode: 4, ClusterSize: 2}
		}
		f.in.Nodes[id] = n
	}
	r := Diagnose(f.in)
	if codes(r) != "unit_stopped@DatabaseClusterNode1 unit_disabled@DatabaseClusterNode1" {
		t.Errorf("problemen: %s", codes(r))
	}
	p := r.Plan
	if p == nil || p.Action != ActionGalera || !p.Auto || len(p.Steps) != 1 || !strings.Contains(p.Steps[0], "aansluiten bij DatabaseClusterNode2 en DatabaseClusterNode3") {
		t.Fatalf("plan: %+v", p)
	}

	// Een volle schijf: melden, en niet vanzelf.
	n := f.in.Nodes[f.ids[0]]
	n.Inspect.DataFree = 200 << 20
	f.in.Nodes[f.ids[0]] = n
	r = Diagnose(f.in)
	if !strings.Contains(codes(r), "disk_full@DatabaseClusterNode1") || r.Plan.Auto {
		t.Errorf("schijf vol: %s %+v", codes(r), r.Plan)
	}

	// Een proces buiten systemd: niet uitvoeren.
	n.Inspect.DataFree = 100 << 30
	n.Inspect.Processes = []protocol.HealProcess{{PID: 7, Name: "mariadbd"}}
	f.in.Nodes[f.ids[0]] = n
	r = Diagnose(f.in)
	if !strings.Contains(codes(r), "process_outside_unit") || r.Plan.Blocked == "" {
		t.Errorf("buiten systemd: %s %+v", codes(r), r.Plan)
	}
}

func TestDiagnoseHealthyAndManaged(t *testing.T) {
	f := jonas()
	for i, id := range f.ids {
		n := f.in.Nodes[id]
		n.Inspect.Galera.ClusterAddress = "gcomm://192.168.10.181,192.168.10.182,192.168.10.183"
		n.Inspect.Galera.NodeAddress, n.Inspect.Galera.PeerTimeouts = "", nil
		n.Inspect.Active = "active"
		f.in.Group.Servers[i].Running = true
		f.in.Group.Servers[i].Galera = &protocol.DBGalera{ClusterStatus: "Primary", State: "Synced", StateCode: 4, ClusterSize: 3}
		f.in.Nodes[id] = n
	}
	f.in.Group.Health = databases.HealthOK
	if r := Diagnose(f.in); len(r.Problems) != 0 || r.Plan != nil {
		t.Errorf("gezond: %s %+v", codes(r), r.Plan)
	}

	m := jonas()
	m.in.Managed = &Managed{ComponentID: uuid.New(), Title: "MariaDB Galera"}
	r := Diagnose(m.in)
	if r.Plan == nil || r.Plan.Managed == nil || r.Plan.Action != "" || !strings.Contains(r.Plan.Steps[0], "Opnieuw starten") {
		t.Errorf("onderdeel: %+v", r.Plan)
	}
}

func TestDiagnoseSingle(t *testing.T) {
	// Een losse PostgreSQL die stilstaat.
	id := uuid.New()
	in := Input{
		Group: databases.Group{Engine: protocol.DBPostgreSQL, Kind: databases.KindSingle, Health: databases.HealthDown,
			Servers: []databases.Server{{NodeID: id, Hostname: "pg1", Unit: "postgresql@17-main", Active: "failed"}}},
		Nodes: map[uuid.UUID]Node{id: {ID: id, Hostname: "pg1", Addresses: []string{"10.0.0.5"},
			Inspect: &protocol.HealEngine{Engine: protocol.DBPostgreSQL, Unit: "postgresql@17-main", Active: "failed", Result: "exit-code", Enabled: "enabled"}}},
	}
	r := Diagnose(in)
	if codes(r) != "unit_stopped@pg1" || r.Plan == nil || r.Plan.Action != ActionRestart || !r.Plan.Auto {
		t.Errorf("%s %+v", codes(r), r.Plan)
	}
}
