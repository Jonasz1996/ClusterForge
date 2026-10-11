package topo

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Map is één laag van de kaart: knopen en lijnen, met hun status. De
// kaart rekent niets uit en leest alleen; de webinterface tekent hem.
type Map struct {
	Layer string    `json:"layer"`
	Items []MapItem `json:"items"`
	Edges []MapEdge `json:"edges"`
	Notes []string  `json:"notes"`
}

// MapItem is een knoop. Parent is de host waarin hij getekend wordt; leeg
// staat hij los.
type MapItem struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Name        string     `json:"name"`
	Parent      string     `json:"parent"`
	Status      string     `json:"status"`
	Detail      string     `json:"detail"`
	NodeID      *uuid.UUID `json:"node_id"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	Cluster     string     `json:"cluster"`
	Environment string     `json:"environment"`
	// Self is true voor de VM waarop ClusterForge zelf draait.
	Self  bool   `json:"self"`
	Link  string `json:"link"`
	Facts []Fact `json:"facts"`
}

// Fact is een regel in het detailpaneel.
type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// MapEdge is een lijn. Kind is disk, member, uplink, gateway, owner of
// vrrp.
type MapEdge struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// MapFilter beperkt de kaart tot een omgeving of een cluster.
type MapFilter struct {
	Environment string
	ClusterID   *uuid.UUID
}

func (f MapFilter) active() bool { return f.Environment != "" || f.ClusterID != nil }

func (f MapFilter) keep(clusterID *uuid.UUID, env string) bool {
	if f.ClusterID != nil && (clusterID == nil || *clusterID != *f.ClusterID) {
		return false
	}
	return f.Environment == "" || env == f.Environment
}

func status(ok bool) string {
	if ok {
		return "healthy"
	}
	return "down"
}

// gb schrijft een grootte als "4,2 GB".
func gb(b int64) string {
	return strings.Replace(strconv.FormatFloat(float64(b)/(1<<30), 'f', 1, 64), ".", ",", 1) + " GB"
}

func nodeByVM(inv Inventory) map[GuestRef]InvNode {
	out := map[GuestRef]InvNode{}
	for _, n := range inv.Nodes {
		if n.VMID != 0 {
			out[GuestRef{n.Conn, n.VMID}] = n
		}
	}
	return out
}

// GuestRef wijst een gast aan.
type GuestRef struct {
	Conn uuid.UUID
	VMID int
}

// Physical is de fysieke laag: per Proxmox-host zijn VM's, containers en
// lokale storage; gedeelde storage hangt tussen de hosts, met een lijn van
// elke gast naar de storage van zijn schijven.
func Physical(m Model, inv Inventory, f MapFilter) Map {
	out := Map{Layer: "physical", Items: []MapItem{}, Edges: []MapEdge{}, Notes: []string{}}
	byVM := nodeByVM(inv)
	hostUsed := map[string]bool{}
	storageUsed := map[string]bool{}
	var unread []string
	var items []MapItem
	for _, g := range m.Guests {
		if g.Template || g.Pool == proxmox.SandboxPool {
			continue
		}
		n, isNode := byVM[GuestRef{g.Conn, g.VMID}]
		if f.active() && (!isNode || !f.keep(n.ClusterID, n.Env)) {
			continue
		}
		host := HostKey(g.Conn, g.Host)
		hostUsed[host] = true
		it := MapItem{
			ID: GuestKey(g.Conn, g.VMID), Kind: "guest", Name: g.Name, Parent: host,
			Link: "/proxmox", Facts: []Fact{{"VMID", itoa(g.VMID)}, {"Soort", guestType(g.Type)}, {"Host", g.Host}, {"Status in Proxmox", g.Status}},
		}
		if it.Name == "" {
			it.Name = "VM " + itoa(g.VMID)
		}
		switch {
		case isNode:
			id := n.ID
			it.Name, it.NodeID, it.ClusterID, it.Cluster, it.Environment = n.Hostname, &id, n.ClusterID, n.Cluster, n.Env
			it.Status, it.Link = n.Status, "/nodes/detail?id="+n.ID.String()
			it.Detail = guestType(g.Type) + " " + itoa(g.VMID)
			if n.Cluster != "" {
				it.Detail += " · " + n.Cluster
			}
			it.Facts = append(it.Facts, Fact{"Node", n.Hostname})
			if n.Reason != "" {
				it.Facts = append(it.Facts, Fact{"Status", n.Reason})
			}
		case g.Status == "running":
			it.Status, it.Detail = "healthy", guestType(g.Type)+" "+itoa(g.VMID)+" · geen node"
		default:
			it.Status, it.Detail = "unknown", guestType(g.Type)+" "+itoa(g.VMID)+" · "+g.Status
		}
		if g.HA() {
			it.Facts = append(it.Facts, Fact{"Proxmox HA", g.HAState})
		}
		if s := m.Self; s.Found() && s.Conn == g.Conn && s.VMID == g.VMID {
			it.Self = true
			it.Detail += " · ClusterForge"
			it.Facts = append(it.Facts, Fact{"ClusterForge", "draait op deze " + guestType(g.Type)})
		}
		if c := g.Config; c != nil {
			seen := map[string]bool{}
			for _, d := range c.Disks {
				if d.Storage == "" {
					it.Facts = append(it.Facts, Fact{"Schijf " + d.Key, d.Path + " (map van de host)"})
					continue
				}
				size := ""
				if d.SizeBytes > 0 {
					size = " (" + gb(d.SizeBytes) + ")"
				}
				it.Facts = append(it.Facts, Fact{"Schijf " + d.Key, d.Storage + size})
				key := StorageKey(g.Conn, d.Storage, g.Host, m.Shared(g.Conn, d.Storage))
				if seen[key] {
					continue
				}
				seen[key] = true
				storageUsed[key] = true
				out.Edges = append(out.Edges, MapEdge{ID: it.ID + ">" + key, From: it.ID, To: key, Kind: "disk"})
			}
			for _, nic := range c.NICs {
				it.Facts = append(it.Facts, Fact{"Netwerkkaart " + nic.Key, nicText(nic)})
			}
			if len(c.Addresses) > 0 {
				it.Facts = append(it.Facts, Fact{"Adressen", strings.Join(c.Addresses, ", ")})
			}
		} else {
			unread = append(unread, g.Label())
		}
		items = append(items, it)
	}
	for _, h := range m.Hosts {
		key := HostKey(h.Conn, h.Name)
		if f.active() && !hostUsed[key] {
			continue
		}
		it := MapItem{ID: key, Kind: "host", Name: h.Name, Status: status(h.Online), Link: "/proxmox", Detail: m.ConnName(h.Conn)}
		it.Facts = []Fact{{"Koppeling", m.ConnName(h.Conn)}, {"Status", map[bool]string{true: "online", false: "offline"}[h.Online]}}
		if h.MaxMem > 0 {
			it.Detail += " · " + gb(h.Mem) + " van " + gb(h.MaxMem)
			it.Facts = append(it.Facts, Fact{"Geheugen", gb(h.Mem) + " van " + gb(h.MaxMem) + " in gebruik"})
		}
		out.Items = append(out.Items, it)
	}
	seen := map[string]bool{}
	for _, s := range m.Storages {
		shared := m.Shared(s.Conn, s.Name)
		key := StorageKey(s.Conn, s.Name, s.Host, shared)
		if seen[key] || (f.active() && !storageUsed[key]) {
			continue
		}
		seen[key] = true
		it := MapItem{ID: key, Kind: "storage", Name: s.Name, Status: status(s.Active), Link: "/proxmox", Detail: s.Type}
		it.Facts = []Fact{{"Soort", s.Type}}
		if shared {
			it.Detail = "gedeeld · " + s.Type
			it.Facts = append(it.Facts, Fact{"Gedeeld", "ja, door alle hosts van " + m.ConnName(s.Conn)})
			// Gedeelde storage is actief als minstens één host hem kan gebruiken.
			for _, o := range m.Storages {
				if o.Conn == s.Conn && o.Name == s.Name && o.Active {
					it.Status = "healthy"
				}
			}
		} else {
			it.Parent = HostKey(s.Conn, s.Host)
			if f.active() && !hostUsed[it.Parent] {
				continue
			}
		}
		out.Items = append(out.Items, it)
	}
	out.Items = append(out.Items, items...)
	if len(unread) > 0 {
		slices.Sort(unread)
		out.Notes = append(out.Notes, "Van "+list(unread)+" is de configuratie nog niet gelezen; de schijven en netwerkkaarten ontbreken nog.")
	}
	for _, c := range m.Connections {
		if !c.Read {
			out.Notes = append(out.Notes, "Proxmox-koppeling "+c.Name+" is bij de laatste sync niet gelezen; wat er staat, is van daarvoor.")
		}
	}
	return out
}

func guestType(t string) string {
	if t == "lxc" {
		return "container"
	}
	return "VM"
}

func nicText(n proxmox.NIC) string {
	s := "aan " + n.Bridge
	if n.Tag != 0 {
		s += ", VLAN " + itoa(n.Tag)
	}
	if n.MAC != "" {
		s += ", " + n.MAC
	}
	if n.LinkDown {
		s += ", losgekoppeld"
	}
	return s
}

// list maakt "a, b en c".
func list(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " en " + s[len(s)-1]
}

// skipIface zijn interfaces die niet op de kaart horen: loopback en wat
// Docker en containers zelf maken.
func skipIface(i protocol.Interface) bool {
	return i.Name == "lo" || i.Kind == "veth" || i.Name == "docker0" || strings.HasPrefix(i.Name, "br-") ||
		strings.HasPrefix(i.Name, "veth")
}

// subnetOf geeft het IPv4-netwerk van een adres met prefix, zoals
// 10.0.20.0/24 voor 10.0.20.11/24.
func subnetOf(cidr string) (netip.Prefix, netip.Addr, bool) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() || p.Addr().IsLoopback() || p.Addr().IsLinkLocalUnicast() || p.Bits() < 8 || p.Bits() > 30 {
		return netip.Prefix{}, netip.Addr{}, false
	}
	return p.Masked(), p.Addr(), true
}

func subnetKey(p netip.Prefix) string { return "subnet:" + p.String() }

// Network is de netwerklaag: de subnetten met hun nodes, de bridges van de
// hosts met hun VLAN's, de gateways, de VIP's met een lijn naar hun eigenaar
// en de VRRP-peers als stippellijn tussen nodes.
func Network(m Model, inv Inventory, f MapFilter) Map {
	out := Map{Layer: "network", Items: []MapItem{}, Edges: []MapEdge{}, Notes: []string{}}
	type subnet struct {
		p       netip.Prefix
		members []string
		vlans   map[string]bool
	}
	subnets := map[netip.Prefix]*subnet{}
	addSubnet := func(p netip.Prefix) *subnet {
		if subnets[p] == nil {
			subnets[p] = &subnet{p: p, vlans: map[string]bool{}}
		}
		return subnets[p]
	}
	edges := map[string]MapEdge{}
	edge := func(from, to, kind, label string) {
		id := from + ">" + to
		if e, ok := edges[id]; ok {
			if label != "" && !strings.Contains(e.Label, label) {
				e.Label = strings.TrimPrefix(e.Label+", "+label, ", ")
				edges[id] = e
			}
			return
		}
		edges[id] = MapEdge{ID: id, From: from, To: to, Kind: kind, Label: label}
	}
	byAddr := map[netip.Addr]uuid.UUID{}
	gateways := map[netip.Addr]bool{}
	var nodes []MapItem
	var noFacts []string
	kept := map[uuid.UUID]bool{}
	macs := map[string]protocol.Interface{}
	macNode := map[string]InvNode{}

	// 1. De nodes en hun subnetten.
	for _, n := range inv.Nodes {
		if !f.keep(n.ClusterID, n.Env) {
			continue
		}
		kept[n.ID] = true
		id := n.ID
		it := MapItem{
			ID: NodeKey(n.ID), Kind: "node", Name: n.Hostname, Status: n.Status, NodeID: &id, ClusterID: n.ClusterID,
			Cluster: n.Cluster, Environment: n.Env, Link: "/nodes/detail?id=" + n.ID.String(), Facts: []Fact{},
		}
		if n.Cluster != "" {
			it.Facts = append(it.Facts, Fact{"Cluster", n.Cluster})
		}
		var addrs []string
		for _, i := range n.Interfaces {
			if skipIface(i) {
				continue
			}
			if i.MAC != "" {
				macs[strings.ToLower(i.MAC)] = i
				macNode[strings.ToLower(i.MAC)] = n
			}
			var shown []string
			for _, a := range i.Addresses {
				p, addr, ok := subnetOf(a)
				if !ok {
					continue
				}
				byAddr[addr] = n.ID
				s := addSubnet(p)
				s.members = append(s.members, n.Hostname)
				edge(subnetKey(p), it.ID, "member", "")
				shown = append(shown, a)
				addrs = append(addrs, addr.String())
			}
			v := i.Name
			if i.VLANID != 0 {
				v += " (VLAN " + itoa(i.VLANID) + ")"
			}
			if len(shown) > 0 {
				it.Facts = append(it.Facts, Fact{"Interface " + v, strings.Join(shown, ", ")})
			}
		}
		if len(addrs) > 0 {
			it.Detail = strings.Join(addrs, ", ")
		} else if n.PrimaryIP != "" {
			it.Detail = n.PrimaryIP
		}
		if n.Gateway != "" {
			it.Facts = append(it.Facts, Fact{"Gateway", n.Gateway})
			if a, err := netip.ParseAddr(n.Gateway); err == nil && a.Is4() {
				gateways[a] = true
			}
		}
		if len(addrs) == 0 {
			noFacts = append(noFacts, n.Hostname)
		}
		nodes = append(nodes, it)
	}

	// 2. VRRP: unicast-peers, en bij multicast de nodes van een cluster met
	// dezelfde VRID.
	type vr struct {
		cluster uuid.UUID
		vrid    int
	}
	byVRID := map[vr][]InvNode{}
	for _, n := range inv.Nodes {
		if !kept[n.ID] || n.Keepalived == nil {
			continue
		}
		for _, in := range n.Keepalived.Instances {
			for _, p := range in.Peers {
				a, err := netip.ParseAddr(p)
				if err != nil {
					continue
				}
				if peer, ok := byAddr[a]; ok && peer != n.ID {
					vrrpEdge(edge, n.ID, peer, in.VRID)
				}
			}
			if len(in.Peers) == 0 && n.ClusterID != nil {
				k := vr{*n.ClusterID, in.VRID}
				byVRID[k] = append(byVRID[k], n)
			}
		}
	}
	for k, ns := range byVRID {
		for i := range ns {
			for j := i + 1; j < len(ns); j++ {
				vrrpEdge(edge, ns[i].ID, ns[j].ID, k.vrid)
			}
		}
	}

	// 3. De bridges van de hosts: hun eigen adressen, hun VLAN-interfaces,
	// en de netwerkkaarten van gasten die een node zijn.
	type br struct {
		conn       uuid.UUID
		host, name string
	}
	bridges := map[br]proxmox.HostIface{}
	var hostGateways []netip.Addr
	ifaceByHost := map[HostRef][]proxmox.HostIface{}
	for _, i := range m.HostIfaces {
		ifaceByHost[HostRef{i.Conn, i.Host}] = append(ifaceByHost[HostRef{i.Conn, i.Host}], i.HostIface)
		if i.Bridge() {
			bridges[br{i.Conn, i.Host, i.Iface}] = i.HostIface
		}
	}
	link := func(b br, p netip.Prefix, vlan int) {
		label := ""
		if vlan != 0 {
			label = "VLAN " + itoa(vlan)
		}
		addSubnet(p).vlans[label] = true
		edge(BridgeKey(b.conn, b.host, b.name), subnetKey(p), "uplink", label)
		if _, ok := bridges[b]; !ok {
			bridges[b] = proxmox.HostIface{Iface: b.name, Type: "bridge"}
		}
	}
	for _, i := range m.HostIfaces {
		p, _, ok := subnetOf(i.CIDR)
		if !ok {
			continue
		}
		if f.active() {
			// Alleen subnetten waarin een gekozen node zit.
			if s := subnets[p]; s == nil || len(s.members) == 0 {
				continue
			}
		}
		switch {
		case i.Bridge():
			link(br{i.Conn, i.Host, i.Iface}, p, 0)
		case i.Type == "vlan" && i.VLANRaw != "":
			link(br{i.Conn, i.Host, i.VLANRaw}, p, i.VLANID)
		}
		if a, err := netip.ParseAddr(i.Gateway); err == nil && a.Is4() {
			hostGateways = append(hostGateways, a)
		}
	}
	for _, g := range m.Guests {
		if g.Config == nil || g.NodeID == nil || !kept[*g.NodeID] {
			continue
		}
		for _, nic := range g.Config.NICs {
			ni, ok := macs[strings.ToLower(nic.MAC)]
			if !ok || nic.Bridge == "" || macNode[strings.ToLower(nic.MAC)].ID != *g.NodeID {
				continue
			}
			for _, a := range ni.Addresses {
				if p, _, ok := subnetOf(a); ok {
					link(br{g.Conn, g.Host, nic.Bridge}, p, nic.Tag)
				}
			}
		}
	}
	if !f.active() {
		for _, a := range hostGateways {
			gateways[a] = true
		}
	}

	// 4. Gateways in het subnet waarin ze vallen.
	for a := range gateways {
		id := "gateway:" + a.String()
		it := MapItem{ID: id, Kind: "gateway", Name: a.String(), Status: "unknown", Detail: "gateway", Facts: []Fact{{"Adres", a.String()}}}
		for p := range subnets {
			if p.Contains(a) {
				edge(id, subnetKey(p), "gateway", "")
				it.Facts = append(it.Facts, Fact{"Subnet", p.String()})
			}
		}
		if id, ok := byAddr[a]; ok {
			if n, found := inv.Node(id); found {
				it.Facts = append(it.Facts, Fact{"Is ook", "node " + n.Hostname})
			}
		}
		out.Items = append(out.Items, it)
	}

	// 5. VIP's met een lijn naar hun eigenaar; zonder eigenaar naar hun
	// subnet.
	for _, v := range inv.VIPs {
		c, ok := inv.Cluster(v.ClusterID)
		if !ok || !f.keep(&c.ID, c.Env) {
			continue
		}
		cid := c.ID
		it := MapItem{
			ID: "vip:" + v.ID.String(), Kind: "vip", Name: v.Address, Status: status(v.Owner != nil), ClusterID: &cid,
			Cluster: c.Name, Environment: c.Env, Link: "/clusters/detail?id=" + c.ID.String(), Detail: "VIP van " + c.Name,
			Facts: []Fact{{"Cluster", c.Name}},
		}
		if v.VRID != 0 {
			it.Facts = append(it.Facts, Fact{"VRID", itoa(v.VRID)})
		}
		if v.Interface != "" {
			it.Facts = append(it.Facts, Fact{"Interface", v.Interface})
		}
		a, _ := netip.ParseAddr(v.Address)
		if v.Owner != nil && kept[*v.Owner] {
			if n, found := inv.Node(*v.Owner); found {
				it.Facts = append(it.Facts, Fact{"Eigenaar", n.Hostname})
			}
			edge(it.ID, NodeKey(*v.Owner), "owner", "eigenaar")
		} else {
			it.Facts = append(it.Facts, Fact{"Eigenaar", "geen"})
			for p := range subnets {
				if p.Contains(a) {
					edge(it.ID, subnetKey(p), "member", "")
				}
			}
		}
		for p := range subnets {
			if p.Contains(a) {
				it.Facts = append(it.Facts, Fact{"Subnet", p.String()})
			}
		}
		out.Items = append(out.Items, it)
	}

	// 6. De subnetten, hosts en bridges zelf.
	for _, s := range subnets {
		it := MapItem{ID: subnetKey(s.p), Kind: "subnet", Name: s.p.String(), Status: "unknown", Facts: []Fact{}}
		var vlans []string
		for v := range s.vlans {
			if v != "" {
				vlans = append(vlans, v)
			}
		}
		slices.Sort(vlans)
		members := uniqSorted(s.members)
		switch len(members) {
		case 0:
			it.Detail = "geen nodes"
		case 1:
			it.Detail = "1 node"
		default:
			it.Detail = fmt.Sprintf("%d nodes", len(members))
		}
		if len(vlans) > 0 {
			it.Detail = strings.Join(vlans, ", ") + " · " + it.Detail
			it.Facts = append(it.Facts, Fact{"VLAN", strings.Join(vlans, ", ")})
		}
		if len(members) > 0 {
			it.Facts = append(it.Facts, Fact{"Nodes", strings.Join(members, ", ")})
		}
		out.Items = append(out.Items, it)
	}
	hosts := map[HostRef]bool{}
	for b, i := range bridges {
		key := BridgeKey(b.conn, b.host, b.name)
		used := false
		for _, e := range edges {
			if e.From == key {
				used = true
			}
		}
		if !used {
			continue
		}
		hosts[HostRef{b.conn, b.host}] = true
		it := MapItem{ID: key, Kind: "bridge", Name: b.name, Parent: HostKey(b.conn, b.host), Status: "unknown", Link: "/proxmox", Facts: []Fact{{"Host", b.host}}}
		if i.Active {
			it.Status = "healthy"
		}
		if len(i.Ports) > 0 {
			ports := make([]string, 0, len(i.Ports))
			for _, p := range i.Ports {
				ports = append(ports, portText(p, ifaceByHost[HostRef{b.conn, b.host}]))
			}
			it.Detail = strings.Join(ports, ", ")
			it.Facts = append(it.Facts, Fact{"Poorten", strings.Join(ports, ", ")})
		}
		if i.VLANAware {
			it.Facts = append(it.Facts, Fact{"VLAN-aware", "ja"})
		}
		if i.CIDR != "" {
			it.Facts = append(it.Facts, Fact{"Adres van de host", i.CIDR})
		}
		if i.MTU != 0 {
			it.Facts = append(it.Facts, Fact{"MTU", itoa(i.MTU)})
		}
		if i.Comments != "" {
			it.Facts = append(it.Facts, Fact{"Opmerking", i.Comments})
		}
		out.Items = append(out.Items, it)
	}
	var unread []string
	for _, h := range m.Hosts {
		ref := HostRef{h.Conn, h.Name}
		if _, read := m.NetworkRead[ref]; !read && h.Online {
			unread = append(unread, h.Name)
		}
		if !hosts[ref] {
			continue
		}
		out.Items = append(out.Items, MapItem{
			ID: HostKey(h.Conn, h.Name), Kind: "host", Name: h.Name, Status: status(h.Online), Detail: m.ConnName(h.Conn), Link: "/proxmox",
			Facts: []Fact{{"Koppeling", m.ConnName(h.Conn)}},
		})
	}
	out.Items = append(out.Items, nodes...)
	for _, e := range edges {
		out.Edges = append(out.Edges, e)
	}
	slices.SortFunc(out.Items, func(a, b MapItem) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ID, b.ID)) })
	slices.SortFunc(out.Edges, func(a, b MapEdge) int { return cmp.Compare(a.ID, b.ID) })
	if len(noFacts) > 0 {
		slices.Sort(noFacts)
		out.Notes = append(out.Notes, "Van "+list(noFacts)+" is geen adres met subnet bekend: er is geen agent, of hij meldde nog geen facts. Ze staan los op de kaart.")
	}
	if len(unread) > 0 {
		slices.Sort(unread)
		out.Notes = append(out.Notes, "Het netwerk van "+list(unread)+" is nog niet gelezen; bridges komen dan alleen uit de netwerkkaarten van de VM's.")
	}
	return out
}

func vrrpEdge(edge func(from, to, kind, label string), a, b uuid.UUID, vrid int) {
	if a.String() > b.String() {
		a, b = b, a
	}
	label := "VRRP"
	if vrid != 0 {
		label += " " + itoa(vrid)
	}
	edge(NodeKey(a), NodeKey(b), "vrrp", label)
}

// portText noemt een poort van een bridge, met de leden als het een bond
// is: "bond0 (eno1, eno2)".
func portText(name string, ifaces []proxmox.HostIface) string {
	for _, i := range ifaces {
		if i.Iface == name && len(i.Ports) > 0 && (i.Type == "bond" || i.Type == "OVSBond") {
			return name + " (" + strings.Join(i.Ports, ", ") + ")"
		}
	}
	return name
}

func uniqSorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return slices.Compact(s)
}
