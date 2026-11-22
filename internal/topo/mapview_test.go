package topo

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const gib = 1 << 30

type mapLab struct {
	m    Model
	inv  Inventory
	conn uuid.UUID
	ids  map[string]uuid.UUID
	repl *strings.Replacer
}

// newMapLab: pve1 en pve2 met local-lvm en ceph; web01 en web02 van het
// prod-cluster Web in VLAN 20 met keepalived; een container zonder node,
// ClusterForge zelf op pve1 en een node zonder agent in het testcluster DB.
func newMapLab() *mapLab {
	l := &mapLab{conn: uuid.New(), ids: map[string]uuid.UUID{}}
	for _, n := range []string{"web01", "web02", "db01", "Web", "DB", "vip"} {
		l.ids[n] = uuid.New()
	}
	c := l.conn
	id := func(n string) *uuid.UUID { v := l.ids[n]; return &v }
	l.m = Model{
		Connections: []Connection{{ID: c, Name: "Thuislab", Read: true}},
		Hosts:       []Host{{Conn: c, Name: "pve1", Online: true, MaxMem: 64 * gib, Mem: 20 * gib}, {Conn: c, Name: "pve2", Online: true, MaxMem: 64 * gib}},
		Storages: []Storage{
			{Conn: c, Name: "local-lvm", Host: "pve1", Type: "lvmthin", Active: true},
			{Conn: c, Name: "local-lvm", Host: "pve2", Type: "lvmthin", Active: true},
			{Conn: c, Name: "ceph", Host: "pve1", Type: "rbd", Shared: true, Active: true},
			{Conn: c, Name: "ceph", Host: "pve2", Type: "rbd", Shared: true},
		},
		Guests: []Guest{
			{Conn: c, VMID: 101, Type: "qemu", Name: "web01", Host: "pve1", Status: "running", NodeID: id("web01"), Hostname: "web01",
				Config: &Config{Disks: []proxmox.Disk{{Key: "scsi0", Storage: "local-lvm", SizeBytes: 32 * gib}},
					NICs: []proxmox.NIC{{Key: "net0", Bridge: "vmbr0", Tag: 20, MAC: "bc:24:11:00:01:01"}}}},
			{Conn: c, VMID: 102, Type: "qemu", Name: "web02", Host: "pve2", Status: "running", NodeID: id("web02"), Hostname: "web02", HAState: "started",
				Config: &Config{Disks: []proxmox.Disk{{Key: "scsi0", Storage: "ceph"}, {Key: "scsi1", Storage: "ceph"}},
					NICs: []proxmox.NIC{{Key: "net0", Bridge: "vmbr0", Tag: 20, MAC: "bc:24:11:00:01:02"}}}},
			{Conn: c, VMID: 200, Type: "lxc", Name: "dns01", Host: "pve2", Status: "running"},
			{Conn: c, VMID: 950, Type: "lxc", Name: "clusterforge", Host: "pve1", Status: "running",
				Config: &Config{Disks: []proxmox.Disk{{Key: "rootfs", Storage: "local-lvm"}, {Key: "mp0", Path: "/srv/cf"}}}},
			{Conn: c, VMID: 9000, Type: "qemu", Name: "debian-13", Host: "pve1", Status: "stopped", Template: true},
			{Conn: c, VMID: 9101, Type: "qemu", Name: "cf-verify-101", Host: "pve2", Status: "running", Pool: proxmox.SandboxPool},
		},
		HostIfaces: []HostIface{
			{Conn: c, Host: "pve1", HostIface: proxmox.HostIface{Iface: "bond0", Type: "bond", Active: true, Ports: []string{"eno1", "eno2"}}},
			{Conn: c, Host: "pve1", HostIface: proxmox.HostIface{Iface: "vmbr0", Type: "bridge", Active: true, Ports: []string{"bond0"},
				CIDR: "192.168.1.11/24", Gateway: "192.168.1.1", VLANAware: true}},
		},
		NetworkRead: map[HostRef]time.Time{{c, "pve1"}: time.Now()},
		Self:        Self{Source: "auto", Conn: c, VMID: 950},
	}
	ka := func(peer string) *protocol.Keepalived {
		return &protocol.Keepalived{Active: "active", VIPs: []string{"10.0.20.100"},
			Instances: []protocol.VRRPInstance{{Name: "VI_1", Interface: "eth0", VRID: 51, Peers: []string{peer}, VIPs: []string{"10.0.20.100"}}}}
	}
	l.inv = Inventory{
		Nodes: []InvNode{
			{ID: l.ids["web01"], Hostname: "web01", ClusterID: id("Web"), Cluster: "Web", Env: "prod", Status: "healthy", Conn: c, VMID: 101, HasAgent: true,
				Interfaces: []protocol.Interface{{Name: "lo", Addresses: []string{"127.0.0.1/8"}},
					{Name: "eth0", MAC: "BC:24:11:00:01:01", Up: true, Kind: "physical", Addresses: []string{"10.0.20.11/24", "fe80::1/64"}}},
				Gateway: "10.0.20.1", Keepalived: ka("10.0.20.12")},
			{ID: l.ids["web02"], Hostname: "web02", ClusterID: id("Web"), Cluster: "Web", Env: "prod", Status: "degraded", Reason: "nginx draait niet", Conn: c, VMID: 102, HasAgent: true,
				Interfaces: []protocol.Interface{{Name: "eth0", MAC: "bc:24:11:00:01:02", Up: true, Kind: "physical", Addresses: []string{"10.0.20.12/24"}},
					{Name: "docker0", Kind: "bridge", Addresses: []string{"172.17.0.1/16"}}},
				Gateway: "10.0.20.1", Keepalived: ka("10.0.20.11")},
			{ID: l.ids["db01"], Hostname: "db01", ClusterID: id("DB"), Cluster: "DB", Env: "test", Status: "unknown", PrimaryIP: "10.0.30.5"},
		},
		Clusters: []InvCluster{{ID: l.ids["Web"], Name: "Web", Env: "prod", Status: "healthy"}, {ID: l.ids["DB"], Name: "DB", Env: "test", Status: "unknown"}},
		VIPs:     []InvVIP{{ID: l.ids["vip"], ClusterID: l.ids["Web"], Address: "10.0.20.100", Interface: "eth0", VRID: 51, Owner: id("web01")}},
	}
	var pairs []string
	for n, v := range l.ids {
		pairs = append(pairs, v.String(), n)
	}
	pairs = append(pairs, c.String(), "C")
	l.repl = strings.NewReplacer(pairs...)
	return l
}

func (l *mapLab) items(m Map) string {
	var out []string
	for _, it := range m.Items {
		s := l.repl.Replace(it.ID) + " " + it.Status
		if it.Parent != "" {
			s += " in " + l.repl.Replace(it.Parent)
		}
		if it.Detail != "" {
			s += " (" + it.Detail + ")"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}

func (l *mapLab) edges(m Map) string {
	var out []string
	for _, e := range m.Edges {
		s := l.repl.Replace(e.From) + " -" + e.Kind + "-> " + l.repl.Replace(e.To)
		if e.Label != "" {
			s += " [" + e.Label + "]"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}

func TestPhysical(t *testing.T) {
	l := newMapLab()
	m := Physical(l.m, l.inv, MapFilter{})
	want := strings.Join([]string{
		"guest:C:101 healthy in host:C:pve1 (VM 101 · Web)",
		"guest:C:102 degraded in host:C:pve2 (VM 102 · Web)",
		"guest:C:200 healthy in host:C:pve2 (container 200 · geen node)",
		"guest:C:950 healthy in host:C:pve1 (container 950 · geen node · ClusterForge)",
		"host:C:pve1 healthy (Thuislab · 20,0 GB van 64,0 GB)",
		"host:C:pve2 healthy (Thuislab · 0,0 GB van 64,0 GB)",
		"storage:C:ceph healthy (gedeeld · rbd)",
		"storage:C:local-lvm@pve1 healthy in host:C:pve1 (lvmthin)",
		"storage:C:local-lvm@pve2 healthy in host:C:pve2 (lvmthin)",
	}, "\n")
	if got := l.items(m); got != want {
		t.Fatalf("knopen:\n%s\nwil\n%s", got, want)
	}
	want = strings.Join([]string{
		"guest:C:101 -disk-> storage:C:local-lvm@pve1",
		"guest:C:102 -disk-> storage:C:ceph",
		"guest:C:950 -disk-> storage:C:local-lvm@pve1",
	}, "\n")
	if got := l.edges(m); got != want {
		t.Fatalf("lijnen:\n%s\nwil\n%s", got, want)
	}
	if len(m.Notes) != 1 || m.Notes[0] != "Van dns01 (200) is de configuratie nog niet gelezen; de schijven en netwerkkaarten ontbreken nog." {
		t.Fatalf("opmerkingen: %q", m.Notes)
	}
	var web01 MapItem
	for _, it := range m.Items {
		if it.ID == GuestKey(l.conn, 101) {
			web01 = it
		}
	}
	facts := map[string]string{}
	for _, f := range web01.Facts {
		facts[f.Label] = f.Value
	}
	if web01.Link != "/nodes/detail?id="+l.ids["web01"].String() || facts["Schijf scsi0"] != "local-lvm (32,0 GB)" ||
		facts["Netwerkkaart net0"] != "aan vmbr0, VLAN 20, bc:24:11:00:01:01" || web01.Self {
		t.Fatalf("web01: %+v", web01)
	}

	// Alleen het cluster Web: geen container zonder node, geen lokale
	// storage van pve2.
	web := l.ids["Web"]
	m = Physical(l.m, l.inv, MapFilter{ClusterID: &web})
	want = strings.Join([]string{
		"guest:C:101 healthy in host:C:pve1 (VM 101 · Web)",
		"guest:C:102 degraded in host:C:pve2 (VM 102 · Web)",
		"host:C:pve1 healthy (Thuislab · 20,0 GB van 64,0 GB)",
		"host:C:pve2 healthy (Thuislab · 0,0 GB van 64,0 GB)",
		"storage:C:ceph healthy (gedeeld · rbd)",
		"storage:C:local-lvm@pve1 healthy in host:C:pve1 (lvmthin)",
	}, "\n")
	if got := l.items(m); got != want {
		t.Fatalf("alleen Web:\n%s\nwil\n%s", got, want)
	}
	if got := Physical(l.m, l.inv, MapFilter{Environment: "lab"}); len(got.Items) != 0 {
		t.Fatalf("lab: %s", l.items(got))
	}
}

func TestNetwork(t *testing.T) {
	l := newMapLab()
	m := Network(l.m, l.inv, MapFilter{})
	want := strings.Join([]string{
		"bridge:C:pve1:vmbr0 healthy in host:C:pve1 (bond0 (eno1, eno2))",
		"bridge:C:pve2:vmbr0 unknown in host:C:pve2",
		"gateway:10.0.20.1 unknown (gateway)",
		"gateway:192.168.1.1 unknown (gateway)",
		"host:C:pve1 healthy (Thuislab)",
		"host:C:pve2 healthy (Thuislab)",
		"node:db01 unknown (10.0.30.5)",
		"node:web01 healthy (10.0.20.11)",
		"node:web02 degraded (10.0.20.12)",
		"subnet:10.0.20.0/24 unknown (VLAN 20 · 2 nodes)",
		"subnet:192.168.1.0/24 unknown (geen nodes)",
		"vip:vip healthy (VIP van Web)",
	}, "\n")
	if got := l.items(m); got != want {
		t.Fatalf("knopen:\n%s\nwil\n%s", got, want)
	}
	want = strings.Join([]string{
		"bridge:C:pve1:vmbr0 -uplink-> subnet:10.0.20.0/24 [VLAN 20]",
		"bridge:C:pve1:vmbr0 -uplink-> subnet:192.168.1.0/24",
		"bridge:C:pve2:vmbr0 -uplink-> subnet:10.0.20.0/24 [VLAN 20]",
		"gateway:10.0.20.1 -gateway-> subnet:10.0.20.0/24",
		"gateway:192.168.1.1 -gateway-> subnet:192.168.1.0/24",
		"node:web01 -vrrp-> node:web02 [VRRP 51]",
		"subnet:10.0.20.0/24 -member-> node:web01",
		"subnet:10.0.20.0/24 -member-> node:web02",
		"vip:vip -owner-> node:web01 [eigenaar]",
	}, "\n")
	// De VRRP-lijn loopt van de kleinste uuid naar de grootste.
	got := strings.Replace(l.edges(m), "node:web02 -vrrp-> node:web01", "node:web01 -vrrp-> node:web02", 1)
	if got != want {
		t.Fatalf("lijnen:\n%s\nwil\n%s", got, want)
	}
	if strings.Join(m.Notes, "\n") != "Van db01 is geen adres met subnet bekend: er is geen agent, of hij meldde nog geen facts. Ze staan los op de kaart.\n"+
		"Het netwerk van pve2 is nog niet gelezen; bridges komen dan alleen uit de netwerkkaarten van de VM's." {
		t.Fatalf("opmerkingen: %q", m.Notes)
	}

	// Alleen prod: db01 en het subnet van de host vallen weg.
	m = Network(l.m, l.inv, MapFilter{Environment: "prod"})
	got = l.items(m)
	if strings.Contains(got, "db01") || strings.Contains(got, "192.168.1") || !strings.Contains(got, "vip:vip") {
		t.Fatalf("alleen prod:\n%s", got)
	}

	// Multicast: geen peers, dezelfde VRID in hetzelfde cluster.
	for i := range l.inv.Nodes[:2] {
		l.inv.Nodes[i].Keepalived.Instances[0].Peers = nil
	}
	if e := l.edges(Network(l.m, l.inv, MapFilter{})); strings.Count(e, "-vrrp->") != 1 {
		t.Fatalf("multicast:\n%s", e)
	}
	// Zonder eigenaar hangt het VIP aan zijn subnet.
	l.inv.VIPs[0].Owner = nil
	if e := l.edges(Network(l.m, l.inv, MapFilter{})); !strings.Contains(e, "vip:vip -member-> subnet:10.0.20.0/24") {
		t.Fatalf("zonder eigenaar:\n%s", e)
	}
}

func TestBridgesFromHostNetwork(t *testing.T) {
	l := newMapLab()
	var got []string
	for _, b := range l.m.Bridges() {
		got = append(got, b.Host+":"+b.Name)
	}
	// vmbr0 op pve1 uit het netwerk van de host, vmbr0 op pve2 uit de
	// netwerkkaart van web02.
	if strings.Join(got, ",") != "pve1:vmbr0,pve2:vmbr0" {
		t.Fatalf("bridges: %v", got)
	}
}
