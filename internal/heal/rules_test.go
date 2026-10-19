package heal

import (
	"slices"
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

func TestDiagnoseGaleraPackage(t *testing.T) {
	// Alles gestopt na een herstart; Node1 ging naar MariaDB 12.3.2 zonder
	// mariadb-server-galera.
	f := jonas()
	for i, id := range f.ids {
		n := f.in.Nodes[id]
		n.Inspect.Galera.ClusterAddress = "gcomm://192.168.10.181,192.168.10.182,192.168.10.183"
		n.Inspect.Galera.NodeAddress = ""
		n.Inspect.Galera.PeerTimeouts = nil
		n.Inspect.Active, n.Inspect.Processes, n.Inspect.Enabled = "inactive", nil, "enabled"
		if i == 0 {
			n.Inspect.Galera.MissingTools = []string{"galera_recovery"}
		}
		f.in.Group.Servers[i].Running, f.in.Group.Servers[i].Galera = false, nil
		f.in.Nodes[id] = n
	}
	r := Diagnose(f.in)
	want := "unit_stopped@DatabaseClusterNode1 galera_package@DatabaseClusterNode1 unit_stopped@DatabaseClusterNode2 unit_stopped@DatabaseClusterNode3 " +
		"galera_no_primary@DatabaseClusterNode1+DatabaseClusterNode2+DatabaseClusterNode3"
	if got := codes(r); got != want {
		t.Errorf("problemen:\n%s\nverwacht:\n%s", got, want)
	}
	for _, p := range r.Problems {
		if p.Code == CodeGaleraTools && (p.Level != LevelWarning || p.Title != "Op DatabaseClusterNode1 ontbreekt het pakket mariadb-server-galera" ||
			!strings.HasPrefix(p.Why, "galera_recovery staat niet op DatabaseClusterNode1. Sinds MariaDB 12.3.2") || !strings.Contains(p.Why, "apt install mariadb-server-galera")) {
			t.Errorf("probleem: %+v", p)
		}
	}
	// Het plan kan, maar niet vanzelf: beginnen of een volledige kopie
	// lukt op Node1 niet.
	p := r.Plan
	if p == nil || p.Action != ActionGalera || p.Blocked != "" || p.Auto ||
		p.AutoWhy != "Op DatabaseClusterNode1 ontbreekt mariadb-server-galera; daar lukt het beginnen of een volledige kopie niet. Installeer het eerst, of bevestig het plan." {
		t.Fatalf("plan: %+v", p)
	}
	if !strings.Contains(strings.Join(p.Steps, " "), "mariadbd --wsrep-recover waar dat script ontbreekt") {
		t.Errorf("stappen: %q", p.Steps)
	}

	// Zonder Galera telt het niet.
	f.in.Group.Kind = databases.KindSingle
	for _, id := range f.ids {
		f.in.Nodes[id].Inspect.Galera.On = false
	}
	if r := Diagnose(f.in); strings.Contains(codes(r), "galera_package") {
		t.Errorf("zonder Galera: %s", codes(r))
	}
}

func TestDiagnoseSST(t *testing.T) {
	// Node1 haalt al een uur een volledige kopie bij Node2, die Primary is;
	// Node3 staat stil, dus Galera meldt dat het die niet bereikt.
	f := jonas()
	for i, id := range f.ids {
		n := f.in.Nodes[id]
		n.Inspect.Galera.ClusterAddress = "gcomm://192.168.10.181,192.168.10.182,192.168.10.183"
		n.Inspect.Galera.NodeAddress = ""
		n.Inspect.Galera.PeerTimeouts = []string{"192.168.10.183"}
		n.Inspect.Enabled = "enabled"
		switch i {
		case 0:
			n.Inspect.Active, n.Inspect.StateSeconds, n.Inspect.Processes = "activating", 3600, []protocol.HealProcess{{PID: 900, Name: "mariadbd", Unit: "mariadb"}}
			f.in.Group.Servers[i].Running, f.in.Group.Servers[i].Galera = false, nil
		case 1:
			n.Inspect.Active = "active"
			f.in.Group.Servers[i].Running, f.in.Group.Servers[i].Active = true, "active"
			f.in.Group.Servers[i].Galera = &protocol.DBGalera{ClusterName: "db", ClusterStatus: "Primary", State: "Donor/Desynced", ClusterSize: 2}
		case 2:
			n.Inspect.Active, n.Inspect.Processes = "inactive", nil
			f.in.Group.Servers[i].Running, f.in.Group.Servers[i].Galera = false, nil
		}
		f.in.Nodes[id] = n
	}
	// Zonder SST-script lijkt Node1 te hangen.
	if r := Diagnose(f.in); !strings.Contains(codes(r), "process_hung@DatabaseClusterNode1") {
		t.Fatalf("zonder SST: %s", codes(r))
	}
	f.in.Nodes[f.ids[0]].Inspect.Galera.SST = "wsrep_sst_rsync"
	r := Diagnose(f.in)
	if strings.Contains(codes(r), "DatabaseClusterNode1") {
		t.Errorf("Node1 haalt een kopie, toch: %s", codes(r))
	}
	// Node3 start pas als de kopie klaar is.
	if p := r.Plan; p == nil || p.Auto || p.Blocked != "DatabaseClusterNode1 haalt nu een volledige kopie (SST); herstel de rest als die Synced is, zodat de donor niet twee kopieën tegelijk geeft" ||
		slices.ContainsFunc(p.Nodes, func(n PlanNode) bool { return n.Hostname == "DatabaseClusterNode1" && (n.Kill || n.Start) }) {
		t.Errorf("plan: %+v", p)
	}
}

func TestDiagnoseNewClusterFlag(t *testing.T) {
	f := jonas()
	// Node3 draait; een afgebroken galera_new_cluster liet de vlag staan.
	f.in.Nodes[f.ids[2]].Inspect.Galera.NewClusterFlag = true
	r := Diagnose(f.in)
	if !strings.Contains(codes(r), "galera_new_cluster_flag@DatabaseClusterNode3") {
		t.Fatalf("problemen: %s", codes(r))
	}
	for _, p := range r.Problems {
		if p.Code == CodeNewCluster && (p.Level != LevelWarning || p.Title != "Een volgende start van MariaDB op DatabaseClusterNode3 begint een nieuw cluster" ||
			!strings.Contains(p.Why, "systemctl unset-environment _WSREP_NEW_CLUSTER")) {
			t.Errorf("probleem: %+v", p)
		}
	}
	// Zolang MariaDB start, hoort de vlag bij een galera_new_cluster die nog
	// loopt.
	f.in.Nodes[f.ids[2]].Inspect.Active = "activating"
	if r := Diagnose(f.in); strings.Contains(codes(r), "galera_new_cluster_flag") {
		t.Errorf("tijdens het starten: %s", codes(r))
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

func TestDiagnoseKnownNode(t *testing.T) {
	// De config klopt, maar ClusterForge meldt de MariaDB van Node3 nog
	// niet: zijn adres is niet verouderd en het plan haalt hem er niet uit.
	f := jonas()
	cur := "gcomm://192.168.10.181,192.168.10.182,192.168.10.183"
	for _, id := range f.ids {
		n := f.in.Nodes[id]
		n.Inspect.Galera.ClusterAddress, n.Inspect.Galera.NodeAddress, n.Inspect.Galera.PeerTimeouts = cur, "", nil
		n.Inspect.Processes = nil
		f.in.Nodes[id] = n
	}
	delete(f.in.Nodes, f.ids[2])
	f.in.Group.Servers = f.in.Group.Servers[:2]
	f.in.Known = map[string]string{"192.168.10.181": f.hosts[0], "192.168.10.182": f.hosts[1], "192.168.10.183": f.hosts[2]}
	if r := Diagnose(f.in); strings.Contains(codes(r), CodeWrongAddress) {
		t.Errorf("bekende node als verouderd gezien: %s", codes(r))
	}
	// Mist er wel een adres, dan blijft Node3 in het nieuwe adres staan.
	for _, id := range f.ids[:2] {
		f.in.Nodes[id].Inspect.Galera.ClusterAddress = "gcomm://192.168.0.190,192.168.10.183"
	}
	r := Diagnose(f.in)
	if r.Plan == nil || len(r.Plan.Nodes) == 0 || r.Plan.Nodes[0].ClusterAddress != cur {
		t.Fatalf("plan: %s %+v", codes(r), r.Plan)
	}
}

// patroni is een PostgreSQL-cluster met Patroni na een nieuwe
// netwerkindeling: de config noemt etcd en de nodes nog op 192.168.0.x,
// pg1 staat stil.
func patroni() *fixture {
	f := &fixture{hosts: []string{"pg1", "pg2", "pg3"}}
	cid := uuid.New()
	g := databases.Group{ClusterID: &cid, ClusterName: "pg", ClusterSlug: "pg", ClusterEnv: "lab", Engine: protocol.DBPostgreSQL, Kind: databases.KindPatroni, Health: databases.HealthDown}
	f.in = Input{Nodes: map[uuid.UUID]Node{}}
	const file = "/etc/patroni/config.yml"
	for i, h := range f.hosts {
		id := uuid.New()
		f.ids = append(f.ids, id)
		ip := "192.168.10.19" + string(rune('1'+i))
		old := "192.168.0.19" + string(rune('1'+i))
		srv := databases.Server{NodeID: id, Hostname: h, Unit: "patroni", Active: "active"}
		insp := &protocol.HealEngine{Engine: protocol.DBPostgreSQL, Unit: "patroni", Active: "active", Enabled: "enabled", DataDir: "/var/lib/postgresql/16/main",
			Patroni: &protocol.HealPatroni{File: file, Scope: "pg", Name: h, DCS: "etcd3", Etcd: "active"}}
		for j := range 3 {
			ep := "192.168.0.19" + string(rune('1'+j)) + ":2379"
			insp.Patroni.Settings = append(insp.Patroni.Settings, protocol.HealSetting{Key: "etcd3.hosts", Value: ep, File: file, Line: 10 + j})
			insp.Patroni.Endpoints = append(insp.Patroni.Endpoints, protocol.HealEndpoint{Address: ep, Error: "geen antwoord binnen 2 s"})
		}
		insp.Patroni.Settings = append(insp.Patroni.Settings,
			protocol.HealSetting{Key: "restapi.connect_address", Value: old + ":8008", File: file, Line: 6},
			protocol.HealSetting{Key: "postgresql.connect_address", Value: old + ":5432", File: file, Line: 16})
		if i == 0 {
			srv.Active, insp.Active, insp.Result, insp.StateSeconds = "failed", "failed", "exit-code", 600
		}
		g.Servers = append(g.Servers, srv)
		f.in.Nodes[id] = Node{ID: id, Hostname: h, Online: true, PrimaryIP: ip, Addresses: []string{ip}, Inspect: insp}
	}
	f.in.Group = g
	return f
}

// fix zet de adressen in de config van node i goed; reach zegt welke
// etcd-adressen antwoorden.
func (f *fixture) fix(i int, reach ...bool) {
	pt := f.in.Nodes[f.ids[i]].Inspect.Patroni
	for j := range pt.Settings {
		pt.Settings[j].Value = strings.Replace(pt.Settings[j].Value, "192.168.0.", "192.168.10.", 1)
	}
	for j := range pt.Endpoints {
		pt.Endpoints[j].Address = strings.Replace(pt.Endpoints[j].Address, "192.168.0.", "192.168.10.", 1)
		if j < len(reach) && reach[j] {
			pt.Endpoints[j].OK, pt.Endpoints[j].Error = true, ""
		}
	}
}

func TestDiagnosePatroni(t *testing.T) {
	f := patroni()
	r := Diagnose(f.in)
	want := "unit_stopped@pg1 patroni_dcs@pg1 patroni_wrong_address@pg1 patroni_dcs@pg2 patroni_wrong_address@pg2 patroni_dcs@pg3 patroni_wrong_address@pg3"
	if got := codes(r); got != want {
		t.Errorf("problemen:\n%s\nverwacht:\n%s", got, want)
	}
	for _, p := range r.Problems {
		switch {
		case p.Code == CodePatroniDCS && (p.Level != LevelDown || !strings.Contains(p.Why, "192.168.0.192:2379: geen antwoord binnen 2 s, en geen node heeft dat adres (/etc/patroni/config.yml regel 11)")):
			t.Errorf("etcd: %+v", p)
		case p.Code == CodePatroniAddr && p.Nodes[0] == "pg2" && !strings.Contains(p.Why, "De config zegt restapi.connect_address: 192.168.0.192:8008 (/etc/patroni/config.yml regel 6) en "+
			"postgresql.connect_address: 192.168.0.192:5432 (/etc/patroni/config.yml regel 16), maar pg2 heeft nu 192.168.10.192."):
			t.Errorf("adres: %+v", p)
		}
	}
	if p := r.Plan; p == nil || p.Action != ActionRestart || p.Auto || !strings.Contains(p.Blocked, "Patroni op pg1 bereikt etcd niet") {
		t.Fatalf("plan: %+v", r.Plan)
	}

	// De adressen kloppen weer en etcd op pg3 antwoordt niet: pg1 starten
	// mag vanzelf, met een waarschuwing per node.
	for i := range 3 {
		f.fix(i, true, true, false)
	}
	r = Diagnose(f.in)
	if got := codes(r); got != "unit_stopped@pg1 patroni_dcs@pg1 patroni_dcs@pg2 patroni_dcs@pg3" {
		t.Errorf("problemen: %s", got)
	}
	if r.Problems[1].Level != LevelWarning || strings.Contains(r.Problems[1].Why, "geen node heeft") {
		t.Errorf("waarschuwing: %+v", r.Problems[1])
	}
	if p := r.Plan; p == nil || !p.Auto || p.Blocked != "" || len(p.Nodes) != 1 || p.Nodes[0].Hostname != "pg1" || p.Nodes[0].Unit != "patroni" {
		t.Fatalf("plan: %+v", r.Plan)
	}

	// Alleen pg1 heeft nog een oud eigen adres: starten mag, maar niet
	// vanzelf.
	f = patroni()
	for i := range 3 {
		f.fix(i, true, true, true)
	}
	f.in.Nodes[f.ids[0]].Inspect.Patroni.Settings[4].Value = "192.168.0.191:5432"
	r = Diagnose(f.in)
	if got := codes(r); got != "unit_stopped@pg1 patroni_wrong_address@pg1" {
		t.Errorf("problemen: %s", got)
	}
	if p := r.Plan; p == nil || p.Auto || p.Blocked != "" || !strings.Contains(p.AutoWhy, "Patroni op pg1") {
		t.Fatalf("plan: %+v", r.Plan)
	}
}

func TestDiagnoseMySQL(t *testing.T) {
	// Een losse MySQL waarvan systemd de unit op failed zette terwijl
	// mysqld nog draait.
	id := uuid.New()
	in := Input{
		Group: databases.Group{Engine: protocol.DBMariaDB, Kind: databases.KindSingle, Health: databases.HealthDown,
			Servers: []databases.Server{{NodeID: id, Hostname: "web1", Unit: "mysql", Active: "failed", Version: "8.0.43"}}},
		Nodes: map[uuid.UUID]Node{id: {ID: id, Hostname: "web1", Addresses: []string{"10.0.0.7"},
			Inspect: &protocol.HealEngine{Engine: protocol.DBMariaDB, Unit: "mysql", Active: "failed", Result: "timeout", Enabled: "enabled", StateSeconds: 900,
				Processes: []protocol.HealProcess{{PID: 2210, Name: "mysqld", Unit: "mysql"}}, DataDir: "/var/lib/mysql", DataFree: 20 << 30, DataTotal: 40 << 30}}},
	}
	r := Diagnose(in)
	if codes(r) != "process_hung@web1" || !strings.HasPrefix(r.Problems[0].Title, "MySQL op web1") {
		t.Errorf("problemen: %s %+v", codes(r), r.Problems)
	}
	p := r.Plan
	if p == nil || p.Action != ActionRestart || !p.Auto || len(p.Nodes) != 1 || !p.Nodes[0].Kill || p.Nodes[0].Unit != "mysql" || p.Nodes[0].KillPIDs[0] != 2210 {
		t.Fatalf("plan: %+v", p)
	}
	if !strings.Contains(p.Steps[0], "MySQL een voor een starten") {
		t.Errorf("stappen: %q", p.Steps)
	}

	// Galera op MySQL zonder Primary: niet zelf opnieuw beginnen.
	f := jonas()
	for i, id := range f.ids {
		n := f.in.Nodes[id]
		n.Inspect.Unit, n.Inspect.Galera.ClusterAddress, n.Inspect.Galera.NodeAddress, n.Inspect.Galera.PeerTimeouts = "mysql", "gcomm://192.168.10.181,192.168.10.182,192.168.10.183", "", nil
		n.Inspect.Processes = nil
		f.in.Group.Servers[i].Unit, f.in.Group.Servers[i].Version = "mysql", "8.0.41-32.1"
		f.in.Nodes[id] = n
	}
	r = Diagnose(f.in)
	if p := r.Plan; p == nil || p.Auto || !strings.Contains(p.Blocked, "kan ClusterForge alleen met MariaDB") {
		t.Fatalf("Galera op MySQL: %s %+v", codes(r), r.Plan)
	}
}

func TestDiagnosePatroniAddresses(t *testing.T) {
	// 127.0.0.1 en IPv6 zonder haken (zoals Patroni ze leest) kloppen.
	f := patroni()
	for i := range 3 {
		f.fix(i, true, true, true)
	}
	n := f.in.Nodes[f.ids[1]]
	n.Addresses = []string{"192.168.10.192", "fd00::12"}
	n.Inspect.Patroni.Settings[3].Value, n.Inspect.Patroni.Settings[4].Value = "127.0.0.1:8008", "fd00::12:5432"
	f.in.Nodes[f.ids[1]] = n
	if got := codes(Diagnose(f.in)); got != "unit_stopped@pg1" {
		t.Errorf("problemen: %s", got)
	}

	// Kan de agent de config niet lezen, dan zegt Zelfherstel dat.
	n.Inspect.Patroni = &protocol.HealPatroni{Error: "geen configbestand van Patroni gevonden"}
	f.in.Nodes[f.ids[1]] = n
	r := Diagnose(f.in)
	if got := codes(r); got != "unit_stopped@pg1 patroni_config@pg2" || !strings.HasPrefix(r.Problems[1].Why, "Geen configbestand van Patroni gevonden.") {
		t.Errorf("problemen: %s %+v", got, r.Problems)
	}

	// Bij een onderdeel van ClusterForge wijst de tekst naar Toepassen.
	f = patroni()
	f.in.Managed = &Managed{ComponentID: uuid.New(), Title: "PostgreSQL met Patroni"}
	r = Diagnose(f.in)
	for _, p := range r.Problems {
		if strings.HasPrefix(p.Code, "patroni_") && (!strings.Contains(p.Why, "Toepassen van het onderdeel PostgreSQL met Patroni") || strings.Contains(p.Why, "niet zelf")) {
			t.Errorf("onderdeel: %+v", p)
		}
	}
	if r.Plan == nil || r.Plan.Managed == nil {
		t.Errorf("plan: %+v", r.Plan)
	}
}
