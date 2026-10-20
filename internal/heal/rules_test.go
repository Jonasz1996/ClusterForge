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
	// Het plan kan, maar niet vanzelf: beginnen of aansluiten lukt op
	// Node1 niet, en de taak slaat hem over.
	p := r.Plan
	if p == nil || p.Action != ActionGalera || p.Blocked != "" || p.Auto ||
		p.AutoWhy != "Op DatabaseClusterNode1 ontbreekt mariadb-server-galera; die node kan het cluster niet beginnen en niet aansluiten. Installeer het eerst, of bevestig het plan." {
		t.Fatalf("plan: %+v", p)
	}
	if steps := strings.Join(p.Steps, " "); !strings.Contains(steps, "mariadbd --wsrep-recover waar dat script ontbreekt") ||
		!strings.Contains(steps, "Op DatabaseClusterNode1 ontbreekt mariadb-server-galera: de taak start MariaDB daar niet") {
		t.Errorf("stappen: %q", p.Steps)
	}
	for i, n := range p.Nodes {
		if n.NoGaleraTools != (i == 0) {
			t.Errorf("node %s: NoGaleraTools %v", n.Hostname, n.NoGaleraTools)
		}
	}

	// Draaien Node2 en Node3 in Primary, dan zou Node1 aansluiten; zonder
	// het pakket lukt dat niet, en meer is er niet te doen.
	for i, id := range f.ids[1:] {
		n := f.in.Nodes[id]
		n.Inspect.Active = "active"
		f.in.Nodes[id] = n
		f.in.Group.Servers[i+1].Running = true
		f.in.Group.Servers[i+1].Galera = &protocol.DBGalera{ClusterStatus: "Primary", State: "Synced", StateCode: 4, ClusterSize: 2}
	}
	r = Diagnose(f.in)
	if p := r.Plan; p == nil || p.Auto || p.Blocked != "Op DatabaseClusterNode1 ontbreekt mariadb-server-galera; zonder dat pakket sluit de node niet aan. Installeer het eerst, in de versie van mariadb-server" ||
		len(p.Steps) != 0 {
		t.Fatalf("aansluiten: %s %+v", codes(r), r.Plan)
	}
	// Staat ook Node2 stil, mét het pakket, dan start de taak alleen Node2.
	n := f.in.Nodes[f.ids[1]]
	n.Inspect.Active = "inactive"
	f.in.Nodes[f.ids[1]] = n
	f.in.Group.Servers[1].Running, f.in.Group.Servers[1].Galera = false, nil
	r = Diagnose(f.in)
	if p := r.Plan; p == nil || p.Auto || p.Blocked != "" || len(p.Steps) != 2 ||
		!strings.HasPrefix(p.Steps[0], "Op DatabaseClusterNode2 MariaDB een voor een starten") ||
		!strings.HasPrefix(p.Steps[1], "Op DatabaseClusterNode1 ontbreekt mariadb-server-galera: de taak start MariaDB daar niet") ||
		!strings.HasPrefix(p.AutoWhy, "Op DatabaseClusterNode1 ontbreekt mariadb-server-galera; zonder dat pakket sluit die node niet aan") {
		t.Fatalf("met Node2: %s %+v", codes(r), r.Plan)
	}
	n.Inspect.Active = "active"
	f.in.Nodes[f.ids[1]] = n
	f.in.Group.Servers[1].Running = true
	f.in.Group.Servers[1].Galera = &protocol.DBGalera{ClusterStatus: "Primary", State: "Synced", StateCode: 4, ClusterSize: 2}
	// Mist een draaiende node het pakket, dan kan hij geen donor zijn: niet
	// vanzelf.
	n = f.in.Nodes[f.ids[1]]
	n.Inspect.Galera.MissingTools = []string{"galera_recovery"}
	f.in.Nodes[f.ids[1]] = n
	n = f.in.Nodes[f.ids[0]]
	n.Inspect.Galera.MissingTools = nil
	f.in.Nodes[f.ids[0]] = n
	if r := Diagnose(f.in); r.Plan == nil || r.Plan.Auto || !strings.Contains(r.Plan.AutoWhy, "als donor") || !strings.Contains(codes(r), "galera_package@DatabaseClusterNode2") {
		t.Fatalf("draaiende node zonder pakket: %s %+v", codes(r), r.Plan)
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

func TestDiagnoseGaleraPackageEverywhere(t *testing.T) {
	// Alle nodes naar 12.3 zonder mariadb-server-galera, en alles gestopt:
	// geen node kan beginnen, dus geen knop.
	f := jonas()
	for i, id := range f.ids {
		n := f.in.Nodes[id]
		n.Inspect.Galera.ClusterAddress = "gcomm://192.168.10.181,192.168.10.182,192.168.10.183"
		n.Inspect.Galera.NodeAddress, n.Inspect.Galera.PeerTimeouts = "", nil
		n.Inspect.Active, n.Inspect.Processes = "inactive", nil
		n.Inspect.Galera.MissingTools = []string{"galera_recovery"}
		f.in.Group.Servers[i].Running, f.in.Group.Servers[i].Galera = false, nil
		f.in.Nodes[id] = n
	}
	r := Diagnose(f.in)
	if p := r.Plan; p == nil || p.Auto || !strings.HasPrefix(p.Blocked, "Op geen enkele node staat mariadb-server-galera") {
		t.Fatalf("plan: %s %+v", codes(r), r.Plan)
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
		if p.Code == CodeNewCluster && (p.Level != LevelWarning || p.Title != "Een volgende start van MariaDB op DatabaseClusterNode3 probeert een nieuw cluster te beginnen" ||
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

// zabbix is het zabbix-cluster van Jonas op 19 oktober: de uitrol van het
// onderdeel kwam op ZabbixHa1 niet verder, zabbix2 heeft nog geen MariaDB,
// dus de groep is alleen ZabbixHa1, die als enige draait.
func zabbix() Input {
	id, cid := uuid.New(), uuid.New()
	return Input{
		Group: databases.Group{ClusterID: &cid, ClusterName: "zabbix", ClusterSlug: "zabbix", ClusterEnv: "test", Engine: protocol.DBMariaDB, Kind: databases.KindGalera, Health: databases.HealthOK,
			Servers: []databases.Server{{NodeID: id, Hostname: "ZabbixHa1", Unit: "mariadb", Active: "active", Running: true,
				Galera: &protocol.DBGalera{ClusterName: "zabbix", ClusterStatus: "Primary", State: "Synced", StateCode: 4, ClusterSize: 1}}}},
		Nodes: map[uuid.UUID]Node{id: {ID: id, Hostname: "ZabbixHa1", Protocol: protocol.Version, Online: true, Addresses: []string{"192.168.10.134"},
			Inspect: &protocol.HealEngine{Engine: protocol.DBMariaDB, Unit: "mariadb", Active: "active", Enabled: "enabled", DataDir: "/var/lib/mysql", DataFree: 100 << 30, DataTotal: 200 << 30,
				Galera: &protocol.HealGalera{On: true, ClusterName: "zabbix", ClusterAddress: "gcomm://192.168.10.134,192.168.10.135"}}}},
		Known:      map[string]string{"192.168.10.134": "ZabbixHa1", "192.168.10.135": "zabbix2"},
		Unfinished: &Unfinished{ComponentID: uuid.New(), Error: "zabbix2: MariaDB: pakketten: apt-get update: E: The repository 'https://repo.zabbix.com/zabbix/8.0/stable/debian trixie Release' does not have a Release file."},
	}
}

func TestDiagnoseUnfinished(t *testing.T) {
	// Draait ZabbixHa1, dan meldt Zelfherstel alleen dat de uitrol niet af
	// is, met de fout van de taak en wat de uitrol afmaakt.
	in := zabbix()
	r := Diagnose(in)
	if codes(r) != "component_unfinished@ZabbixHa1" || r.Plan != nil {
		t.Fatalf("draait: %s %+v", codes(r), r.Plan)
	}
	p := r.Problems[0]
	if p.Level != LevelWarning || p.Title != "De uitrol van MariaDB op zabbix is niet af" || !strings.Contains(p.Why, "stopte: zabbix2: MariaDB: pakketten: apt-get update") ||
		!strings.Contains(p.Why, "Kies Opnieuw toepassen") || !strings.Contains(p.Why, "begint de eerste node het opnieuw") {
		t.Errorf("probleem: %+v", p)
	}

	// Staat hij stil, dan is er een plan, maar niet vanzelf: de uitrol
	// beslist.
	srv := &in.Group.Servers[0]
	srv.Running, srv.Active, srv.Galera = false, "failed", nil
	n := in.Nodes[srv.NodeID]
	n.Inspect.Active, n.Inspect.Result = "failed", "exit-code"
	in.Nodes[srv.NodeID] = n
	in.Group.Kind, in.Group.Health = databases.KindSingle, databases.HealthDown
	r = Diagnose(in)
	if codes(r) != "unit_stopped@ZabbixHa1 component_unfinished@ZabbixHa1 galera_no_primary@ZabbixHa1" {
		t.Fatalf("stil: %s", codes(r))
	}
	if pl := r.Plan; pl == nil || pl.Action != ActionGalera || pl.Auto || pl.Blocked != "" || !strings.HasPrefix(pl.AutoWhy, "De uitrol van het onderdeel van ClusterForge is niet af") {
		t.Errorf("plan: %+v", r.Plan)
	}

	// Loopt de uitrol nog, dan is er niets te melden, en nog steeds niets
	// vanzelf.
	in.Unfinished.Running = true
	r = Diagnose(in)
	if strings.Contains(codes(r), "component_unfinished") || r.Plan == nil || r.Plan.Auto {
		t.Errorf("uitrol loopt: %s %+v", codes(r), r.Plan)
	}
}

// zabbixProxy is zabbix() met Zabbixproxy erbij: de scheidsrechter van het
// cluster, met zijn eigen MariaDB zonder Galera-config voor de Zabbix-proxy.
// Op 20 oktober zag Zelfherstel die als Galera-node: het zette zijn adres in
// wsrep_cluster_address van ZabbixHa1 en stopte hem om de positie te lezen.
func zabbixProxy() Input {
	in := zabbix()
	id := uuid.New()
	in.Group.Servers = append(in.Group.Servers, databases.Server{NodeID: id, Hostname: "Zabbixproxy", Unit: "mariadb", Active: "active", Running: true, Version: "11.8.3"})
	in.Nodes[id] = Node{ID: id, Hostname: "Zabbixproxy", Protocol: protocol.Version, Online: true, Addresses: []string{"192.168.10.198"},
		Inspect: &protocol.HealEngine{Engine: protocol.DBMariaDB, Unit: "mariadb", Active: "active", Enabled: "enabled", DataDir: "/var/lib/mysql",
			DataFree: 10 << 30, DataTotal: 20 << 30, Processes: []protocol.HealProcess{{PID: 812, Name: "mariadbd", Unit: "mariadb", AgeSeconds: 3600}}}}
	in.Known["192.168.10.198"] = "Zabbixproxy"
	return in
}

// stop zet de server van host stil, zoals systemd na een fout.
func stop(in *Input, host string) {
	i := slices.IndexFunc(in.Group.Servers, func(s databases.Server) bool { return s.Hostname == host })
	srv := &in.Group.Servers[i]
	srv.Running, srv.Active, srv.Galera = false, "failed", nil
	n := in.Nodes[srv.NodeID]
	n.Inspect.Active, n.Inspect.Result, n.Inspect.Processes = "failed", "exit-code", nil
	in.Nodes[srv.NodeID] = n
}

func nodeOf(in Input, host string) Node {
	i := slices.IndexFunc(in.Group.Servers, func(s databases.Server) bool { return s.Hostname == host })
	return in.Nodes[in.Group.Servers[i].NodeID]
}

func TestDiagnoseLooseServer(t *testing.T) {
	// Draait alles, dan is er niets te doen: Zabbixproxy hoort niet bij het
	// cluster en ontbreekt dus niet in wsrep_cluster_address.
	in := zabbixProxy()
	if r := Diagnose(in); codes(r) != "component_unfinished@ZabbixHa1" || r.Plan != nil {
		t.Fatalf("draait: %s %+v", codes(r), r.Plan)
	}

	// Staat ZabbixHa1 stil, dan begint het plan het cluster alleen op
	// ZabbixHa1; Zabbixproxy blijft draaien.
	stop(&in, "ZabbixHa1")
	in.Group.Kind, in.Group.Health = databases.KindSingle, databases.HealthWarning
	r := Diagnose(in)
	if codes(r) != "unit_stopped@ZabbixHa1 component_unfinished@ZabbixHa1 galera_no_primary@ZabbixHa1" {
		t.Fatalf("ZabbixHa1 stil: %s", codes(r))
	}
	pl := r.Plan
	if pl == nil || pl.Action != ActionGalera || pl.Blocked != "" || len(pl.Nodes) != 1 || pl.Nodes[0].Hostname != "ZabbixHa1" || pl.Nodes[0].SetAddress {
		t.Fatalf("plan: %+v", pl)
	}
	if slices.ContainsFunc(pl.Steps, func(s string) bool { return strings.Contains(s, "Zabbixproxy") }) {
		t.Errorf("stappen noemen Zabbixproxy: %q", pl.Steps)
	}

	// Staat Zabbixproxy ook stil, dan start de taak hem als gewone server,
	// voor het cluster.
	stop(&in, "Zabbixproxy")
	r = Diagnose(in)
	if !strings.Contains(codes(r), "unit_stopped@Zabbixproxy") || strings.Contains(codes(r), "galera_no_primary@ZabbixHa1+Zabbixproxy") {
		t.Fatalf("beide stil: %s", codes(r))
	}
	pl = r.Plan
	if pl == nil || len(pl.Nodes) != 2 || pl.Nodes[0].Hostname != "ZabbixHa1" || pl.Nodes[0].Loose ||
		pl.Nodes[1].Hostname != "Zabbixproxy" || !pl.Nodes[1].Loose || !pl.Nodes[1].Start || pl.Nodes[1].Unit != "mariadb" {
		t.Fatalf("plan: %+v", pl)
	}
	if first := pl.Steps[0]; !strings.HasPrefix(first, "Op Zabbixproxy MariaDB starten en wachten tot hij antwoordt.") || !strings.Contains(first, "zonder Galera-config") ||
		slices.ContainsFunc(pl.Steps[1:], func(s string) bool { return strings.Contains(s, "Zabbixproxy") }) {
		t.Errorf("stappen: %q", pl.Steps)
	}
	in.Unfinished = nil
	if pl := Diagnose(in).Plan; pl == nil || !pl.Auto {
		t.Errorf("vanzelf: %+v", pl)
	}
	// Een bijna volle schijf op Zabbixproxy: dan niet vanzelf.
	nodeOf(in, "Zabbixproxy").Inspect.DataFree = 100 << 20
	if pl := Diagnose(in).Plan; pl == nil || pl.Auto || pl.AutoWhy != "Een node heeft te weinig vrije ruimte voor een volledige kopie." {
		t.Errorf("volle schijf: %+v", pl)
	}
	nodeOf(in, "Zabbixproxy").Inspect.DataFree = 10 << 30

	// Draait ZabbixHa1 weer, dan start het plan alleen Zabbixproxy; vanzelf
	// mag dat zodra de uitrol af is.
	in = zabbixProxy()
	stop(&in, "Zabbixproxy")
	r = Diagnose(in)
	pl = r.Plan
	if pl == nil || pl.Action != ActionGalera || pl.Auto || len(pl.Nodes) != 2 || !pl.Nodes[0].Primary || !pl.Nodes[1].Loose || len(pl.Steps) != 1 {
		t.Fatalf("alleen Zabbixproxy: %s %+v", codes(r), pl)
	}
	in.Unfinished = nil
	if pl := Diagnose(in).Plan; pl == nil || !pl.Auto || pl.AutoWhy != "Alleen starten van wat stilstaat; er gaat geen data verloren." {
		t.Errorf("vanzelf: %+v", pl)
	}

	// Een proces buiten systemd op Zabbixproxy houdt het cluster niet
	// tegen; Zabbixproxy start de taak dan niet.
	in = zabbixProxy()
	stop(&in, "ZabbixHa1")
	stop(&in, "Zabbixproxy")
	nodeOf(in, "Zabbixproxy").Inspect.Processes = []protocol.HealProcess{{PID: 900, Name: "mariadbd"}}
	r = Diagnose(in)
	if pl := r.Plan; !strings.Contains(codes(r), "process_outside_unit@Zabbixproxy") || pl == nil || pl.Blocked != "" || len(pl.Nodes) != 1 {
		t.Errorf("buiten systemd: %s %+v", codes(r), r.Plan)
	}
}

func TestDiagnoseLooseListed(t *testing.T) {
	// Na de mislukte taak van 20 oktober staat 192.168.10.198 in
	// wsrep_cluster_address van ZabbixHa1. Zonder garb op Zabbixproxy kan
	// die een Galera-node zijn die zijn config kwijt is: dan geen plan.
	in := zabbixProxy()
	stop(&in, "ZabbixHa1")
	nodeOf(in, "ZabbixHa1").Inspect.Galera.ClusterAddress = "gcomm://192.168.10.134,192.168.10.198,192.168.10.135"
	r := Diagnose(in)
	if pl := r.Plan; pl == nil || !strings.HasPrefix(pl.Blocked, "Zabbixproxy staat in wsrep_cluster_address van een andere node") || pl.Auto {
		t.Fatalf("zonder garb: %s %+v", codes(r), r.Plan)
	}

	// Met garb, de scheidsrechter, op Zabbixproxy is dat adres dat van garb:
	// het plan begint het cluster op ZabbixHa1 en laat het adres staan.
	n := nodeOf(in, "Zabbixproxy")
	n.Garb = true
	in.Nodes[n.ID] = n
	r = Diagnose(in)
	if pl := r.Plan; pl == nil || pl.Blocked != "" || len(pl.Nodes) != 1 || pl.Nodes[0].Hostname != "ZabbixHa1" || pl.Nodes[0].SetAddress || strings.Contains(codes(r), CodeWrongAddress) {
		t.Fatalf("met garb: %s %+v", codes(r), r.Plan)
	}

	// Kan de agent de config van Zabbixproxy niet lezen, dan weet
	// Zelfherstel niet wat hij is, ook niet met garb op de node.
	for _, garb := range []bool{false, true} {
		in = zabbixProxy()
		stop(&in, "ZabbixHa1")
		n := nodeOf(in, "Zabbixproxy")
		n.Garb, n.Inspect.Error = garb, "my_print_defaults: exit status 1"
		in.Nodes[n.ID] = n
		r = Diagnose(in)
		if pl := r.Plan; pl == nil || !strings.HasPrefix(pl.Blocked, "Zelfherstel kan de config van MariaDB op Zabbixproxy niet lezen") || pl.Auto {
			t.Errorf("onleesbaar, garb %v: %s %+v", garb, codes(r), r.Plan)
		}
	}

	// my_print_defaults van MySQL kent --mysqld niet: een node met
	// mysql.service telt dan als Galera-node, zoals vroeger.
	in = zabbixProxy()
	stop(&in, "ZabbixHa1")
	n = nodeOf(in, "Zabbixproxy")
	n.Inspect.Unit, n.Inspect.Error = "mysql", "my_print_defaults: exit status 1"
	in.Nodes[n.ID] = n
	r = Diagnose(in)
	if pl := r.Plan; pl == nil || strings.Contains(pl.Blocked, "niet lezen") || len(pl.Nodes) != 2 || pl.Nodes[1].Loose {
		t.Errorf("MySQL: %s %+v", codes(r), r.Plan)
	}
	// Naast garb is zo'n MySQL de eigen database van de scheidsrechter.
	n.Garb = true
	in.Nodes[n.ID] = n
	r = Diagnose(in)
	if pl := r.Plan; pl == nil || pl.Blocked != "" || len(pl.Nodes) != 1 || pl.Nodes[0].Hostname != "ZabbixHa1" {
		t.Errorf("MySQL naast garb: %s %+v", codes(r), r.Plan)
	}
}

func TestDiagnoseLooseGaleraOnArbiter(t *testing.T) {
	// Een losse groep op de scheidsrechter van het onderdeel met een
	// Galera-config: Zelfherstel begint daar geen tweede cluster.
	in := zabbixProxy()
	stop(&in, "Zabbixproxy")
	srv := in.Group.Servers[slices.IndexFunc(in.Group.Servers, func(s databases.Server) bool { return s.Hostname == "Zabbixproxy" })]
	n := in.Nodes[srv.NodeID]
	n.Inspect.Galera = &protocol.HealGalera{On: true, ClusterName: "zabbix", ClusterAddress: "gcomm://192.168.10.134,192.168.10.135,192.168.10.198", NodeAddress: "192.168.10.198"}
	cluster := in.Group.ClusterID
	in = Input{Group: databases.Group{ClusterID: cluster, NodeID: &srv.NodeID, ClusterName: "zabbix", Engine: protocol.DBMariaDB, Kind: databases.KindSingle,
		Health: databases.HealthDown, Servers: []databases.Server{srv}}, Nodes: map[uuid.UUID]Node{srv.NodeID: n}, Known: map[string]string{}}
	r := Diagnose(in)
	if pl := r.Plan; pl == nil || pl.Action != ActionGalera || pl.Auto || !strings.HasPrefix(pl.Blocked, "Op Zabbixproxy staat de scheidsrechter van het onderdeel") {
		t.Fatalf("plan: %s %+v", codes(r), r.Plan)
	}

	// Zonder Galera-config is het een gewone server: starten.
	n.Inspect.Galera = nil
	r = Diagnose(in)
	if pl := r.Plan; pl == nil || pl.Action != ActionRestart || pl.Blocked != "" || len(pl.Nodes) != 1 || !pl.Nodes[0].Start {
		t.Fatalf("zonder config: %s %+v", codes(r), r.Plan)
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
