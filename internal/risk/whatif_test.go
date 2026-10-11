package risk

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/topo"
)

const gib = int64(1) << 30

// lab is de wereld van de wat-als: drie hosts, lokale storage en gedeelde
// ceph, en vier clusters.
//
//   - web-prod: web01 en web02, allebei op pve1 op local-lvm, met een VIP.
//   - db-prod: db01 op pve1 en db02 op pve2, op ceph; db01 onder HA.
//   - dns-prod: alleen dns01 op pve2, met net0 op vmbr0 en net1 op vmbr1.
//   - app-test: app01 op pve1 onder HA, maar te groot voor een andere host.
//
// Daarnaast VM 900 zonder node en ClusterForge zelf als VM 950 op pve1,
// onder HA op ceph.
type lab struct {
	conn uuid.UUID
	ids  map[string]uuid.UUID
	m    topo.Model
	w    deps.World
}

func (l *lab) id(name string) uuid.UUID {
	if _, ok := l.ids[name]; !ok {
		l.ids[name] = uuid.New()
	}
	return l.ids[name]
}

func (l *lab) ptr(name string) *uuid.UUID {
	id := l.id(name)
	return &id
}

func newLab() *lab {
	l := &lab{conn: uuid.New(), ids: map[string]uuid.UUID{}}
	l.m.Connections = []topo.Connection{{ID: l.conn, Name: "lab", Read: true}}
	l.m.Hosts = []topo.Host{
		{Conn: l.conn, Name: "pve1", Online: true, MaxMem: 64 * gib, Mem: 40 * gib},
		{Conn: l.conn, Name: "pve2", Online: true, MaxMem: 64 * gib, Mem: 20 * gib},
		{Conn: l.conn, Name: "pve3", Online: true, MaxMem: 16 * gib, Mem: 15 * gib},
	}
	for _, h := range []string{"pve1", "pve2", "pve3"} {
		l.m.Storages = append(l.m.Storages,
			topo.Storage{Conn: l.conn, Name: "local-lvm", Host: h},
			topo.Storage{Conn: l.conn, Name: "ceph", Host: h, Shared: true})
	}
	l.cluster("web-prod", "prod", deps.VIP{Address: "10.0.20.10", Owner: l.id("web01")})
	l.cluster("db-prod", "prod")
	l.cluster("dns-prod", "prod")
	l.cluster("app-test", "test")
	l.guest(101, "web01", "web-prod", "pve1", "local-lvm", "", 2*gib, "vmbr0")
	l.guest(102, "web02", "web-prod", "pve1", "local-lvm", "", 2*gib, "vmbr0")
	l.guest(201, "db01", "db-prod", "pve1", "ceph", "started", 8*gib, "vmbr0")
	l.guest(202, "db02", "db-prod", "pve2", "ceph", "started", 8*gib, "vmbr0")
	l.guest(301, "dns01", "dns-prod", "pve2", "local-lvm", "", gib, "vmbr0", "vmbr1")
	l.guest(401, "app01", "app-test", "pve1", "ceph", "started", 64*gib, "vmbr0")
	l.guest(900, "oud-ding", "", "pve1", "local-lvm", "", gib, "vmbr0")
	l.guest(950, "clusterforge", "", "pve1", "ceph", "started", 2*gib, "vmbr0")
	l.m.Guests = append(l.m.Guests, topo.Guest{Conn: l.conn, VMID: 9000, Type: "qemu", Name: "debian-13", Host: "pve1", Status: "stopped", Template: true})
	l.m.Self = topo.Self{Source: "auto", Conn: l.conn, VMID: 950, MatchedOn: "MAC-adres bc:24:11:00:09:50"}
	l.units("web01", "nginx", "keepalived")
	l.units("web02", "nginx", "keepalived")
	l.units("db01", "mariadb")
	l.units("db02", "mariadb")
	l.units("dns01", "named")
	l.units("app01", "app")
	l.svc("web-prod", "nginx", "web", "nginx")
	l.svc("web-prod", "keepalived", "vip", "keepalived")
	l.svc("db-prod", "mariadb", "database", "mariadb")
	l.svc("dns-prod", "bind9", "dns", "named")
	l.svc("app-test", "app", "app", "app")
	return l
}

func (l *lab) cluster(name, env string, vips ...deps.VIP) {
	l.w.Clusters = append(l.w.Clusters, deps.Cluster{ID: l.id(name), Name: name, Slug: name, Environment: env, Type: "keepalived", Status: status.Healthy, VIPs: vips})
}

func (l *lab) guest(vmid int, name, cluster, host, storage, ha string, mem int64, bridges ...string) {
	g := topo.Guest{Conn: l.conn, VMID: vmid, Type: "qemu", Name: name, Host: host, Status: "running", HAState: ha, MaxMem: mem}
	cfg := &topo.Config{Disks: []proxmox.Disk{{Key: "scsi0", Storage: storage, SizeBytes: 32 * gib}}}
	for i, b := range bridges {
		cfg.NICs = append(cfg.NICs, proxmox.NIC{Key: fmt.Sprintf("net%d", i), Bridge: b, MAC: fmt.Sprintf("bc:24:11:00:%02x:%02x", vmid/100, vmid%100)})
	}
	g.Config = cfg
	if cluster != "" {
		g.NodeID, g.Hostname = l.ptr(name), name
		l.w.Nodes = append(l.w.Nodes, deps.Node{ID: l.id(name), Hostname: name, ClusterID: l.ptr(cluster), Active: true, Fresh: true,
			Units: map[string]string{}, Status: status.Healthy})
	}
	l.m.Guests = append(l.m.Guests, g)
}

func (l *lab) units(node string, units ...string) {
	for i := range l.w.Nodes {
		if l.w.Nodes[i].Hostname == node {
			for _, u := range units {
				l.w.Nodes[i].Units[u] = "active"
			}
		}
	}
}

func (l *lab) svc(cluster, name, kind, unit string) {
	l.w.Services = append(l.w.Services, deps.Svc{ID: l.id(cluster + "/" + name), ClusterID: l.ptr(cluster), Name: name, Kind: kind, Unit: unit,
		Source: deps.SourceManual, State: deps.StateConfirmed})
}

func (l *lab) world() WhatIfWorld {
	return WhatIfWorld{Topo: l.m, Graph: deps.Build(l.w), Candidates: map[uuid.UUID]map[string][]uuid.UUID{
		l.id("web-prod"): {"10.0.20.10": {l.id("web01"), l.id("web02")}},
	}}
}

func (l *lab) run(t *testing.T, keys ...string) WhatIf {
	t.Helper()
	w := l.world()
	comps, err := w.Resolve(keys)
	if err != nil {
		t.Fatal(err)
	}
	return w.Run(comps, false)
}

func guests(r WhatIf) string {
	var out []string
	for _, g := range r.Guests {
		s := fmt.Sprintf("%d %s: %s", g.VMID, g.Outcome, g.Reason)
		if g.RestartHost != "" {
			s += " → " + g.RestartHost
		}
		if g.Unknown {
			s += " (onbekend)"
		}
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

func clusters(r WhatIf) string {
	var out []string
	for _, c := range r.Clusters {
		s := fmt.Sprintf("%s %s→%s", c.Name, c.Before.Status, c.During.Status)
		if c.AfterHA != nil {
			s += "→" + c.AfterHA.Status
		}
		out = append(out, s+": "+c.Final().Reason)
	}
	return strings.Join(out, "\n")
}

func services(r WhatIf) string {
	var out []string
	for _, s := range r.Services {
		line := s.Group + "/" + s.Name + " " + s.Impact + ": " + s.Reason
		if s.Recovers {
			line += " (komt terug)"
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (l *lab) host(name string) string { return hostKey(l.conn, name) }

func TestWhatIfHost(t *testing.T) {
	l := newLab()
	r := l.run(t, l.host("pve1"))

	// app01 is het grootst en komt eerst aan de beurt, maar past nergens;
	// db01 en ClusterForge gaan naar pve2, waar het meeste vrij is.
	want := strings.Join([]string{
		"101 down: draait op pve1",
		"102 down: draait op pve1",
		"401 down: draait op pve1; HA kan hem niet herstarten: geen andere host heeft genoeg vrij geheugen",
		"900 down: draait op pve1 (onbekend)",
		"201 restarted: draait op pve1 → pve2",
		"950 restarted: draait op pve1 → pve2 (onbekend)",
	}, "\n")
	if got := guests(r); got != want {
		t.Fatalf("gasten:\n%s\nwil\n%s", got, want)
	}
	var nodes []string
	for _, n := range r.Nodes {
		nodes = append(nodes, n.Hostname+" "+n.Outcome)
	}
	if got := strings.Join(nodes, ", "); got != "app01 down, db01 restarted, web01 down, web02 down" {
		t.Fatalf("nodes: %s", got)
	}
	if r.Guests[0].Key != topo.GuestKey(l.conn, 101) {
		t.Fatalf("sleutel van de gast: %s", r.Guests[0].Key)
	}
	want = strings.Join([]string{
		"web-prod healthy→down: alle nodes zijn down",
		"db-prod healthy→degraded→healthy: ",
		"app-test healthy→down: alle nodes zijn down",
	}, "\n")
	if got := clusters(r); got != want {
		t.Fatalf("clusters:\n%s\nwil\n%s", got, want)
	}
	if r.Clusters[1].During.Reason != "db01: valt uit: draait op pve1" {
		t.Fatalf("db-prod eerst: %+v", r.Clusters[1].During)
	}
	want = strings.Join([]string{
		"web-prod/keepalived down: geen andere node kan VIP 10.0.20.10 overnemen",
		"web-prod/nginx down: nginx draait op geen andere node",
		"db-prod/mariadb degraded: draait nog op db02 (komt terug)",
		"app-test/app down: app draait op geen andere node",
	}, "\n")
	if got := services(r); got != want {
		t.Fatalf("diensten:\n%s\nwil\n%s", got, want)
	}
	if r.Self == nil || r.Self.Outcome != "restarted" || r.Self.RestartHost != "pve2" || r.Self.Name != "clusterforge (950)" {
		t.Fatalf("ClusterForge zelf: %+v", r.Self)
	}
	if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "HA-groepen") {
		t.Fatalf("opmerkingen: %v", r.Notes)
	}

	// Zonder vrij geheugen op pve2 en pve3 herstart HA niets.
	l.m.Hosts[1].Mem, l.m.Hosts[2].Mem = 63*gib, 16*gib
	r = l.run(t, l.host("pve1"))
	if !strings.Contains(guests(r), "201 down: draait op pve1; HA kan hem niet herstarten: geen andere host heeft genoeg vrij geheugen") {
		t.Fatalf("vol:\n%s", guests(r))
	}
	if r.Self == nil || r.Self.Outcome != "down" {
		t.Fatalf("ClusterForge zelf, vol: %+v", r.Self)
	}
}

func TestWhatIfVIPMoves(t *testing.T) {
	l := newLab()
	// web02 naar pve2: valt pve1 uit, dan neemt web02 het VIP over.
	for i := range l.m.Guests {
		if l.m.Guests[i].VMID == 102 {
			l.m.Guests[i].Host = "pve2"
		}
	}
	r := l.run(t, l.host("pve1"))
	if !strings.HasPrefix(clusters(r), "web-prod healthy→degraded: web01: valt uit: draait op pve1") {
		t.Fatalf("clusters:\n%s", clusters(r))
	}
	if !strings.Contains(services(r), "web-prod/keepalived degraded: VIP 10.0.20.10 verhuist naar web02") {
		t.Fatalf("diensten:\n%s", services(r))
	}
	// Zonder keepalived-gegevens kan elke andere actieve node het overnemen.
	w := l.world()
	w.Candidates = nil
	comps, _ := w.Resolve([]string{l.host("pve1")})
	if got := clusters(w.Run(comps, false)); !strings.HasPrefix(got, "web-prod healthy→degraded") {
		t.Fatalf("zonder kandidaten:\n%s", got)
	}
	// Staat het VIP alleen in de configuratie van web01, dan is het weg.
	w.Candidates = map[uuid.UUID]map[string][]uuid.UUID{l.id("web-prod"): {"10.0.20.10": {l.id("web01")}}}
	if got := clusters(w.Run(comps, false)); !strings.HasPrefix(got, "web-prod healthy→down: VIP 10.0.20.10 heeft geen eigenaar") {
		t.Fatalf("alleen web01:\n%s", got)
	}
}

func TestWhatIfStorageAndBridge(t *testing.T) {
	l := newLab()
	key := func(kind, rest string) string { return kind + ":" + l.conn.String() + ":" + rest }

	r := l.run(t, key("storage", "local-lvm@pve1"))
	want := strings.Join([]string{
		"101 down: schijf scsi0 staat op local-lvm",
		"102 down: schijf scsi0 staat op local-lvm",
		"900 down: schijf scsi0 staat op local-lvm (onbekend)",
	}, "\n")
	if got := guests(r); got != want {
		t.Fatalf("local-lvm op pve1:\n%s", got)
	}
	// Gedeelde storage valt overal uit, en dan helpt HA niet.
	r = l.run(t, key("storage", "ceph"))
	for _, vm := range []string{"201 down", "202 down", "401 down", "950 down"} {
		if !strings.Contains(guests(r), vm+": schijf scsi0 staat op ceph") {
			t.Fatalf("ceph, %s:\n%s", vm, guests(r))
		}
	}
	if !strings.Contains(clusters(r), "db-prod healthy→down") || r.Self == nil || r.Self.Outcome != "down" {
		t.Fatalf("ceph:\n%s\n%+v", clusters(r), r.Self)
	}
	// net1 valt weg: dns01 draait door. net0 valt weg: dns01 is weg.
	r = l.run(t, key("bridge", "pve2:vmbr1"))
	if got := guests(r); got != "301 partial: net1 hangt aan vmbr1" || len(r.Clusters) != 0 {
		t.Fatalf("vmbr1:\n%s\n%s", got, clusters(r))
	}
	r = l.run(t, key("bridge", "pve2:vmbr0"))
	if got := guests(r); got != "202 down: net0 hangt aan vmbr0\n301 down: net0 hangt aan vmbr0" {
		t.Fatalf("vmbr0:\n%s", got)
	}
	// Een VM waarvan de configuratie nog niet gelezen is, staat erbij.
	for i := range l.m.Guests {
		if l.m.Guests[i].VMID == 101 {
			l.m.Guests[i].Config = nil
		}
	}
	r = l.run(t, key("storage", "local-lvm@pve1"))
	if len(r.Notes) != 1 || r.Notes[0] != "Van web01 (101) is de configuratie nog niet gelezen: waar de schijven en netwerkkaarten zitten en of HA hem herstart, is onbekend." {
		t.Fatalf("niet gelezen: %v", r.Notes)
	}
	if _, err := l.world().Resolve([]string{"host:onbekend"}); err == nil || err.Error() != "onbekend onderdeel: host:onbekend" {
		t.Fatalf("onbekend onderdeel: %v", err)
	}
}

func TestWhatIfNodesAndClusters(t *testing.T) {
	l := newLab()
	r := l.run(t, "node:"+l.id("db01").String(), "node:"+l.id("db02").String())
	if got := clusters(r); got != "db-prod healthy→down: alle nodes zijn down" {
		t.Fatalf("db01 en db02:\n%s", got)
	}
	if got := services(r); got != "db-prod/mariadb down: mariadb draait op geen andere node" {
		t.Fatalf("db01 en db02, diensten:\n%s", got)
	}
	if len(r.Guests) != 0 {
		t.Fatalf("een node raakt geen VM: %+v", r.Guests)
	}
	r = l.run(t, "cluster:"+l.id("dns-prod").String())
	if got := clusters(r); got != "dns-prod healthy→down: alle nodes zijn down" {
		t.Fatalf("dns-prod:\n%s", got)
	}
	// De toestand van nu telt: is web02 al down, dan maakt web01 alleen al
	// web-prod down.
	l.w.Nodes[1].Status, l.w.Nodes[1].Reason, l.w.Nodes[1].Fresh = status.Down, "geen heartbeat meer", false
	r = l.run(t, "node:"+l.id("web01").String())
	if got := clusters(r); got != "web-prod degraded→down: alle nodes zijn down" {
		t.Fatalf("web01 met web02 al down:\n%s", got)
	}
}

func TestSPOFs(t *testing.T) {
	l := newLab()
	// web02 is down, maar de SPOF-lijst rekent alsof alles gezond is.
	l.w.Nodes[1].Status, l.w.Nodes[1].Fresh = status.Down, false
	spofs := l.world().SPOFs()
	var got []string
	for _, sp := range spofs {
		var cs, ss []string
		for _, c := range sp.Clusters {
			cs = append(cs, c.Name)
		}
		for _, s := range sp.Services {
			ss = append(ss, s.Name)
		}
		got = append(got, fmt.Sprintf("%s %s: %d down, prod %v %v", sp.Component.Kind, sp.Component.Name, sp.Down, cs, ss))
	}
	want := strings.Join([]string{
		"host pve1: 5 down, prod [web-prod] [keepalived nginx]",
		"bridge vmbr0 op pve1: 5 down, prod [web-prod] [keepalived nginx]",
		"storage ceph (gedeeld): 4 down, prod [db-prod] [mariadb]",
		"storage local-lvm op pve1: 3 down, prod [web-prod] [keepalived nginx]",
		"host pve2: 2 down, prod [dns-prod] [bind9]",
		"storage local-lvm op pve2: 2 down, prod [dns-prod] [bind9]",
		"bridge vmbr0 op pve2: 2 down, prod [dns-prod] [bind9]",
		"node dns01: 2 down, prod [dns-prod] [bind9]",
	}, "\n")
	if strings.Join(got, "\n") != want {
		t.Fatalf("SPOF's:\n%s\nwil\n%s", strings.Join(got, "\n"), want)
	}

	// De bevindingen per cluster: pve1 staat al als same_host bij web-prod,
	// en dns01 als single_node bij dns-prod.
	by := SPOFsByCluster(spofs)
	web := healthy()
	web.ID, web.Name = l.id("web-prod"), "web-prod"
	web.Nodes[1].Host = "pve1"
	web.Nodes[0].Host = "pve1"
	web.SPOFs = by[web.ID]
	var texts []string
	for _, f := range findings(Score(web, nil, now)) {
		if f.Card == Redundancy {
			texts = append(texts, f.Check+": "+f.Text)
		}
	}
	slices.Sort(texts)
	want = "redundancy.same_host: web-01 en web-02 draaien allebei op pve1: valt pve1 uit, dan is web-prod down"
	if strings.Join(texts, "\n") != want {
		t.Fatalf("web-prod:\n%s", strings.Join(texts, "\n"))
	}

	dns := healthy()
	dns.ID, dns.Name = l.id("dns-prod"), "dns-prod"
	dns.Nodes = dns.Nodes[:1]
	dns.Nodes[0].Host = "pve2"
	dns.VIPs, dns.FailoverTests = nil, nil
	dns.SPOFs = by[dns.ID]
	texts = nil
	for _, f := range findings(Score(dns, nil, now)) {
		if f.Check == "redundancy.spof" {
			texts = append(texts, f.Text)
		}
	}
	// De host geeft de bevinding; zijn lokale storage en bridge zeggen
	// hetzelfde.
	if strings.Join(texts, "\n") != "Valt Proxmox-host pve2 uit, dan is dns-prod down" {
		t.Fatalf("dns-prod:\n%s", strings.Join(texts, "\n"))
	}
	f := find(t, Score(dns, nil, now), "redundancy.spof")
	if f.Severity != High || f.Subject.Type != "component" || f.Subject.Name != "pve2" ||
		f.Link != "/risico?tab=wat-als&onderdeel="+url.QueryEscape("host:"+l.conn.String()+":pve2") {
		t.Fatalf("SPOF-bevinding: %+v", f)
	}
	// Een acceptatie hoort bij het onderdeel en blijft gelijk tussen twee
	// berekeningen.
	again := SPOFsByCluster(l.world().SPOFs())[dns.ID]
	if !slices.ContainsFunc(again, func(in SPOFInput) bool { return in.ID == f.Subject.ID }) {
		t.Fatalf("id van het onderdeel verandert: %+v %v", again, f.Subject.ID)
	}
	// Met de nodes over twee hosts verspreid blijft alleen de gedeelde
	// storage over, als die prod down maakt.
	dns.SPOFs = []SPOFInput{{Key: "storage:x:ceph", Kind: KindStorage, Name: "ceph (gedeeld)", ID: uuid.New(), ClusterDown: true}}
	if f := find(t, Score(dns, nil, now), "redundancy.spof"); f.Text != "Valt storage ceph uit, dan is dns-prod down" {
		t.Fatalf("ceph: %s", f.Text)
	}
	// Op test of lab geeft een SPOF geen bevinding.
	dns.Env = "test"
	if fs := findings(Score(dns, nil, now)); strings.Contains(fmt.Sprint(fs), "redundancy.spof") {
		t.Fatalf("test-cluster: %+v", fs)
	}
}

func TestSPOFServicesOnly(t *testing.T) {
	l := newLab()
	// bind9 draait op twee DNS-nodes, maar named alleen op dns01: dan gaat
	// de dienst down en het cluster niet.
	l.guest(302, "dns02", "dns-prod", "pve3", "ceph", "", gib, "vmbr0")
	spofs := l.world().SPOFs()
	var sp *SPOF
	for i := range spofs {
		if spofs[i].Component.Kind == KindNode && spofs[i].Component.Name == "dns01" {
			sp = &spofs[i]
		}
	}
	if sp == nil || len(sp.Clusters) != 0 || len(sp.Services) != 1 || sp.Services[0].Name != "bind9" {
		t.Fatalf("dns01: %+v", sp)
	}
	in := SPOFsByCluster(spofs)[l.id("dns-prod")]
	dns := healthy()
	dns.ID, dns.Name, dns.SPOFs = l.id("dns-prod"), "dns-prod", in
	var texts []string
	for _, f := range findings(Score(dns, nil, now)) {
		if f.Check == "redundancy.spof" {
			texts = append(texts, f.Text)
		}
	}
	if !strings.Contains(strings.Join(texts, "\n"), "Valt node dns01 uit, dan is de dienst bind9 down") {
		t.Fatalf("dns-prod:\n%s", strings.Join(texts, "\n"))
	}
}

func TestSelfPlatform(t *testing.T) {
	id := uuid.New()
	text := func(in *SelfInput) string {
		var out []string
		for _, f := range PlatformCard(PlatformInput{Self: in}, nil, now).Findings {
			out = append(out, string(f.Severity)+" "+f.Check+": "+f.Text)
		}
		return strings.Join(out, "\n")
	}
	if got := text(&SelfInput{}); got != "low platform.self_unknown: Niet bekend op welke VM ClusterForge draait, dus de wat-als ziet niet wanneer hij zelf uitvalt" {
		t.Fatalf("onbekend: %s", got)
	}
	if got := text(&SelfInput{Found: true, ID: id, Name: "clusterforge (950)", BackupsRead: true}); got != "high platform.self_backup: De VM van ClusterForge, clusterforge (950), heeft geen back-up" {
		t.Fatalf("geen back-up: %s", got)
	}
	if got := text(&SelfInput{Found: true, ID: id, Name: "clusterforge (950)", BackupsRead: true, Latest: ago(40 * time.Hour)}); got != "medium platform.self_backup: De nieuwste back-up van de VM van ClusterForge, clusterforge (950), is 40 u oud; het beleid is 30 uur" {
		t.Fatalf("oud: %s", got)
	}
	for _, in := range []*SelfInput{
		{Found: true, ID: id, Name: "clusterforge (950)", BackupsRead: true, Latest: ago(2 * time.Hour)},
		{Found: true, ID: id, Name: "clusterforge (950)"},
		nil,
	} {
		if got := text(in); got != "" {
			t.Fatalf("%+v: %s", in, got)
		}
	}
}
