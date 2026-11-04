package components

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/galera"
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
	// garbd draait niet als root; een logbestand dat hij niet mag openen,
	// laat hem meteen stoppen.
	if regexp.MustCompile(`(?m)^\s*(export\s+)?LOG_FILE=`).MatchString(garb) {
		t.Errorf("garb met een logbestand:\n%s", garb)
	}
	if steps[0].Package == nil || steps[0].Package.Names[0] != "galera-arbitrator-4" || steps[len(steps)-1].Service.Name != "garb" {
		t.Errorf("stappen van de scheidsrechter: %+v", steps)
	}
	if got := spec.order(); got[0].Hostname != "nc1" || got[1].Hostname != "nc2" || got[2].Role != RoleArbiter {
		t.Errorf("volgorde: %+v", got)
	}
}

// TestBootstrap: alleen de eerste node van een nieuw cluster begint het, en
// een node die al Primary is, krijgt alleen het merkteken. Met again komen
// er twee stappen bij die het cluster ook met het merkteken beginnen, maar
// niet op een node met databases met tabellen.
func TestBootstrap(t *testing.T) {
	tpl, spec, states := galeraSpec(t, false)
	steps := render(t, tpl, spec, states, 0)
	out, err := bootstrap(steps, "nextcloud", false, false)
	if err != nil {
		t.Fatal(err)
	}
	i := slicesIndex(out, func(s templates.Step) bool { return s.Command != nil && s.Command.Creates == Marker("nextcloud") })
	if out[i].Command.Run != "systemctl stop mariadb && galera_new_cluster && touch /var/lib/clusterforge/galera-nextcloud" || len(out) != len(steps) {
		t.Errorf("beginnen: %q, %d stappen", out[i].Command.Run, len(out))
	}
	if steps[i].Command.Run == out[i].Command.Run {
		t.Error("bootstrap veranderde de stappen van de aanroeper")
	}
	out, _ = bootstrap(steps, "nextcloud", true, false)
	if out[i].Command.Run != "touch /var/lib/clusterforge/galera-nextcloud" {
		t.Errorf("al Primary: %q", out[i].Command.Run)
	}
	// Al Primary: niets opnieuw beginnen, ook niet met again.
	if out, _ = bootstrap(steps, "nextcloud", true, true); len(out) != len(steps) {
		t.Errorf("al Primary met again: %d stappen", len(out))
	}
	if _, err := bootstrap(steps, "ander", false, false); err == nil {
		t.Error("een ander merkteken gaf geen fout")
	}

	out, _ = bootstrap(steps, "nextcloud", false, true)
	if len(out) != len(steps)+2 || out[i].Command.Creates != Marker("nextcloud") {
		t.Fatalf("again: %+v", out)
	}
	data, begin := out[i+1], out[i+2]
	if data.Title != dataTitle || data.Command.Creates != "" || !strings.HasPrefix(data.Command.Unless, protocol.DBCheckCommand+" || ") ||
		!strings.Contains(data.Command.Run, "exit 3") || !strings.Contains(data.Command.Run, "/var/lib/mysql/*/") {
		t.Errorf("nakijken: %+v %+v", data, data.Command)
	}
	if begin.Title != "het Galera-cluster opnieuw beginnen" || begin.Command.Creates != "" || begin.Command.Unless != protocol.DBCheckCommand ||
		begin.Command.Run != "("+galera.BootstrapAny+") && touch /var/lib/clusterforge/galera-nextcloud" {
		t.Errorf("opnieuw beginnen: %+v %+v", begin, begin.Command)
	}
	if out[i+3].Service == nil || out[i+3].Service.Name != "mariadb" {
		t.Errorf("na het beginnen: %+v", out[i+3])
	}
}

// TestUserDatabases speelt het script na dat de databases met tabellen in
// de datamap noemt: die van MariaDB zelf en lege databases tellen niet.
func TestUserDatabases(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("geen sh")
	}
	dir := t.TempDir()
	script := strings.ReplaceAll(userDatabases, galera.DataDir, dir)
	// De status telt niet: het script staat altijd in $(...).
	run := func() string {
		out, _ := exec.Command("sh", "-c", script).Output()
		return string(out)
	}
	if got := run(); got != "" {
		t.Errorf("lege datamap: %q", got)
	}
	for _, f := range []string{"mysql/user.MAI", "performance_schema/db.opt", "sys/sys_config.ibd", "#innodb_redo/#ib_redo1", "test/db.opt", "leeg/.keep"} {
		writeTestFile(t, filepath.Join(dir, f))
	}
	_ = os.Remove(filepath.Join(dir, "leeg/.keep"))
	writeTestFile(t, filepath.Join(dir, "grastate.dat"))
	if got := run(); got != "" {
		t.Errorf("alleen MariaDB zelf: %q", got)
	}
	writeTestFile(t, filepath.Join(dir, "zabbix_proef/history.ibd"))
	writeTestFile(t, filepath.Join(dir, "zabbix_proef/db.opt"))
	writeTestFile(t, filepath.Join(dir, "nextcloud/oc_users.frm"))
	if got := run(); got != "nextcloud zabbix_proef " {
		t.Errorf("met data: %q", got)
	}
}

func writeTestFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOnlyHolder: de eerste node begint het cluster alleen opnieuw als
// geen andere datanode data van het cluster kan hebben.
func TestOnlyHolder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		peers []peer
		ok    bool
		why   string
	}{
		{"geen andere datanodes", nil, true, ""},
		{"nergens MariaDB", []peer{{host: "nc2"}, {host: "nc3"}}, true, ""},
		{"nog nooit in een cluster", []peer{{host: "nc2", server: true, fresh: true}, {host: "nc3"}}, true, ""},
		{"draait", []peer{{host: "nc2"}, {host: "nc3", server: true, running: true}}, false, "op nc3 draait MariaDB"},
		{"zat in een cluster", []peer{{host: "nc2", server: true}}, false, "nc2 zat al in een Galera-cluster en kan nieuwere data hebben"},
		{"antwoordt niet", []peer{{host: "nc2", err: errors.New("de agent is offline")}}, false, "nc2 zegt niet of daar data staat (de agent is offline)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := onlyHolder(tc.peers)
			if ok != tc.ok || why != tc.why {
				t.Errorf("%v %q, verwacht %v %q", ok, why, tc.ok, tc.why)
			}
		})
	}
}

// TestPeerOf: een andere datanode telt alleen als zonder data als zijn
// MariaDB zeker stilstaat en hij zeker nog nooit in een cluster zat.
func TestPeerOf(t *testing.T) {
	const clusterUUID = "5c3a1e0e-0000-11f0-9a3b-7e1c2d3f4a5b"
	stopped := &protocol.DBServer{Active: "failed"}
	for _, tc := range []struct {
		name   string
		srv    *protocol.DBServer
		err    error
		pos    *protocol.DBPositionResult
		posErr error
		ok     bool
		why    string
	}{
		{"geen MariaDB", nil, nil, nil, nil, true, ""},
		{"volgens InnoDB nooit in een cluster", stopped, nil, &protocol.DBPositionResult{UUID: galera.NoUUID, Seqno: -1, From: protocol.DBFromRecovery, Via: "galera_recovery"}, nil, true, ""},
		// Alleen grastate.dat zegt het niet zeker: een teruggezette
		// back-up heeft er geen, en Galera zet er nullen in bij een
		// volledige kopie.
		{"geen grastate.dat, InnoDB niet gelezen", stopped, nil, &protocol.DBPositionResult{Seqno: -1, Error: galera.NoGrastate}, nil, false,
			"nc2 zegt niet of daar data staat (" + galera.NoGrastate + ")"},
		{"uuid van nullen in grastate.dat, recovery mislukt", stopped, nil, &protocol.DBPositionResult{UUID: galera.NoUUID, Seqno: -1, From: protocol.DBFromGrastate, Error: "galera_recovery: exit status 1"}, nil, false,
			"nc2 zegt niet of daar data staat (galera_recovery: exit status 1)"},
		{"teruggezette back-up", stopped, nil, &protocol.DBPositionResult{UUID: clusterUUID, Seqno: 9000, From: protocol.DBFromRecovery}, nil, false,
			"nc2 zat al in een Galera-cluster en kan nieuwere data hebben"},
		{"zat in een cluster, recovery mislukt", stopped, nil, &protocol.DBPositionResult{UUID: clusterUUID, Seqno: -1, From: protocol.DBFromGrastate, Error: "galera_recovery: exit status 1"}, nil, false,
			"nc2 zat al in een Galera-cluster en kan nieuwere data hebben"},
		{"grastate.dat leeg", stopped, nil, &protocol.DBPositionResult{Error: "grastate.dat heeft geen uuid"}, nil, false,
			"nc2 zegt niet of daar data staat (grastate.dat heeft geen uuid)"},
		{"grastate.dat niet te lezen", stopped, nil, &protocol.DBPositionResult{Error: "open /var/lib/mysql/grastate.dat: permission denied"}, nil, false,
			"nc2 zegt niet of daar data staat (open /var/lib/mysql/grastate.dat: permission denied)"},
		{"positie zonder antwoord", stopped, nil, nil, errors.New("de agent antwoordde niet op tijd"), false, "nc2 zegt niet of daar data staat (de agent antwoordde niet op tijd)"},
		{"draait intussen", stopped, nil, &protocol.DBPositionResult{Running: true, Error: "status: geen antwoord"}, nil, false, "op nc2 draait MariaDB"},
		{"start nog (volledige kopie)", &protocol.DBServer{Active: "activating", Error: "geen socket"}, nil, nil, nil, false, "op nc2 draait MariaDB"},
		{"unit draait, agent kan er niet in", &protocol.DBServer{Active: "active", Error: "Access denied"}, nil, nil, nil, false, "op nc2 draait MariaDB"},
		{"status zonder antwoord", nil, errors.New("de agent is offline"), nil, nil, false, "nc2 zegt niet of daar data staat (de agent is offline)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			p := peerOf("nc2", tc.srv, tc.err, func() (*protocol.DBPositionResult, error) {
				asked = true
				if tc.pos == nil && tc.posErr == nil {
					t.Fatal("positie gevraagd")
				}
				return tc.pos, tc.posErr
			})
			if ok, why := onlyHolder([]peer{p}); ok != tc.ok || why != tc.why {
				t.Errorf("%v %q, verwacht %v %q (%+v, positie gevraagd: %v)", ok, why, tc.ok, tc.why, p, asked)
			}
		})
	}
}

// TestOutside: de eerste node begint het cluster alleen opnieuw als hij
// zeker niet meer in een Primary component zit; een donor doet nog mee.
func TestOutside(t *testing.T) {
	for _, tc := range []struct {
		name string
		srv  *protocol.DBServer
		err  error
		ok   bool
		why  string
	}{
		{"geen MariaDB", nil, nil, true, ""},
		{"gestopt na een fout", &protocol.DBServer{Active: "failed"}, nil, true, ""},
		{"gestopt", &protocol.DBServer{Active: "inactive"}, nil, true, ""},
		{"draait zonder Galera", &protocol.DBServer{Active: "active", Running: true}, nil, true, ""},
		{"status van Galera niet te lezen", &protocol.DBServer{Active: "active", Running: true, Error: "status: timeout"}, nil, false,
			"ZabbixHa1 zegt niet of MariaDB in een Primary component zit (status: timeout)"},
		{"non-Primary", &protocol.DBServer{Active: "active", Running: true, Galera: &protocol.DBGalera{ClusterName: "zabbix", ClusterStatus: "non-Primary", State: "Initialized"}}, nil, true, ""},
		{"donor", &protocol.DBServer{Active: "active", Running: true, Galera: &protocol.DBGalera{ClusterName: "zabbix", ClusterStatus: "Primary", State: "Donor/Desynced"}}, nil, false,
			"MariaDB op ZabbixHa1 zit nog in een Primary component (Donor/Desynced)"},
		{"start nog", &protocol.DBServer{Active: "activating"}, nil, false, "MariaDB op ZabbixHa1 is activating"},
		{"zonder antwoord", nil, errors.New("de agent is offline"), false, "ZabbixHa1 zegt niet hoe MariaDB ervoor staat (de agent is offline)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ok, why := outside(tc.srv, tc.err, "ZabbixHa1"); ok != tc.ok || why != tc.why {
				t.Errorf("%v %q, verwacht %v %q", ok, why, tc.ok, tc.why)
			}
		})
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
	s.checkMemory(&pn, &n, r, &protocol.Facts{MemoryBytes: 4 << 30}, 1<<30, map[uuid.UUID]float64{n.NodeID: 80}, engines[protocol.DBMariaDB], add)
	// (3.2G + 1.29G) / 0.85 = 5.28G, afgerond op 512 MiB is 5632 MiB.
	if len(got) != 1 || got[0].Command != "pct set 186 --memory 5632" || pn.FreeAfterPct == nil || *pn.FreeAfterPct > -12 {
		t.Fatalf("geheugen: %+v, over %v", got, pn.FreeAfterPct)
	}
	got = nil
	s.checkMemory(&pn, &n, r, &protocol.Facts{MemoryBytes: 16 << 30}, 1<<30, map[uuid.UUID]float64{n.NodeID: 30}, engines[protocol.DBMariaDB], add)
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

// TestVIPSteps: het bestand met cf_db, en de vrrp_instance met het VIP uit
// de facts.
func TestVIPSteps(t *testing.T) {
	steps := vipSteps("nextcloud", "10.0.20.185")
	if len(steps) != 2 || steps[0].Directory == nil || steps[0].Directory.Path != "/etc/keepalived/conf.d" || steps[1].File == nil ||
		steps[1].File.Path != protocol.DBTrackFile || steps[1].File.Mode != "0644" {
		t.Fatalf("stappen: %+v", steps)
	}
	conf := steps[1].File.Content
	for _, want := range []string{"cluster nextcloud", "vrrp_script cf_db {", `script "/usr/local/bin/cf-agent check db"`, "fall 2", "user root"} {
		if !strings.Contains(conf, want) {
			t.Errorf("%q ontbreekt:\n%s", want, conf)
		}
	}
	f := &protocol.Facts{Keepalived: &protocol.Keepalived{Active: "active", Instances: []protocol.VRRPInstance{
		{Name: "VI_WEB", VIPs: []string{"10.0.20.100"}}, {Name: "VI_1", VIPs: []string{"10.0.20.185"}},
	}}}
	if inst, ok := hasInstance(f, "10.0.20.185"); !ok || inst != "VI_1" {
		t.Errorf("VI_1: %q %v", inst, ok)
	}
	if _, ok := hasInstance(f, "10.0.20.180"); ok {
		t.Error(".180 staat er niet in")
	}
	if _, ok := hasInstance(&protocol.Facts{}, "10.0.20.185"); ok {
		t.Error("zonder keepalived")
	}
	if got := vipTitles("nextcloud", "10.0.20.185", ""); got[2] != "keepalived.conf: track_script cf_db in de vrrp_instance met 10.0.20.185" {
		t.Errorf("titels: %v", got)
	}
}

// Een agent zonder protocol.DBPGSince kent het veld Engine niet en zou een
// verzoek voor PostgreSQL op MariaDB uitvoeren; ClusterForge stuurt hem er
// geen.
func TestPGAgent(t *testing.T) {
	for want, p := range map[string]pgNode{
		"protocol 9": {node: Node{Hostname: "nc1"}, protocol: protocol.DBPGSince - 1},
		"protocol 0": {node: Node{Hostname: "nc2"}},
		"":           {node: Node{Hostname: "nc3"}, protocol: protocol.DBPGSince},
	} {
		err := pgAgent(p)
		if want == "" {
			if err != nil {
				t.Errorf("%s: %v", p.node.Hostname, err)
			}
			continue
		}
		var conflict *ConflictError
		if !errors.As(err, &conflict) || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "--upgrade") {
			t.Errorf("%s: %v", p.node.Hostname, err)
		}
	}
}
