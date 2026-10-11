package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type nodeLinkView struct {
	ID       string `json:"id"`
	VMID     int    `json:"vmid"`
	Guest    string `json:"guest"`
	Host     string `json:"host"`
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	Cluster  string `json:"cluster"`
	MAC      string `json:"mac"`
}

// mapIndex zet een laag om naar elementen op id en lijnen als "van>naar kind label".
func mapIndex(m topo.Map) (map[string]topo.MapItem, []string) {
	items := map[string]topo.MapItem{}
	for _, it := range m.Items {
		items[it.ID] = it
	}
	var edges []string
	for _, e := range m.Edges {
		edges = append(edges, strings.TrimSpace(e.From+">"+e.To+" "+e.Kind+" "+e.Label))
	}
	return items, edges
}

func factOf(it topo.MapItem, label string) string {
	for _, f := range it.Facts {
		if f.Label == label {
			return f.Value
		}
	}
	return ""
}

// TestMap: pve1 heeft een bond onder vmbr0 met VLAN 20 erop, pve2 een
// gewone bridge. web01 is VM 101, web02 is nog niet gekoppeld maar heeft
// het MAC-adres van VM 102, en dns01 dat van container 200.
func TestMap(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	q := store.New(e.pool)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}

	pve, srv, fp := newPVE(t)
	pve.SetHostNetwork("pve1", []map[string]any{
		{"iface": "vmbr0", "type": "bridge", "active": 1, "bridge_ports": "bond0", "bridge_vlan_aware": 1,
			"cidr": "192.168.1.11/24", "gateway": "192.168.1.1"},
		{"iface": "bond0", "type": "bond", "active": 1, "slaves": "eno1 eno2"},
		{"iface": "eno1", "type": "eth", "active": 1},
		{"iface": "eno2", "type": "eth", "active": 1},
	})
	pve.SetHostNetwork("pve2", []map[string]any{
		{"iface": "vmbr0", "type": "bridge", "active": 1, "bridge_ports": "eno1", "cidr": "192.168.1.12/24", "gateway": "192.168.1.1"},
		{"iface": "eno1", "type": "eth", "active": 1},
	})
	pve.SetGuestStatus(101, "running", "pve1")
	pve.SetGuestConfig(101, "scsi0", "local:vm-101-disk-0,size=32G")
	pve.SetGuestConfig(101, "net0", "virtio=BC:24:11:00:01:01,bridge=vmbr0,tag=20")
	pve.SetGuestConfig(102, "scsi0", "ceph:vm-102-disk-0,size=32G")
	pve.SetGuestConfig(102, "net0", "virtio=BC:24:11:00:01:02,bridge=vmbr0,tag=20")
	pve.SetGuestConfig(200, "net0", "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:00:02:00,ip=dhcp,type=veth")

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != http.StatusCreated {
		t.Fatalf("koppelen: %d", s)
	}
	connID := uuid.MustParse(conn.ID)
	var cl, infra cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "infra", "name": "Infra", "type": "generic", "environment": "lab"}, &infra)
	var web01, web02, dns01 node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{
		"hostname": "web01", "cluster_id": cl.ID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": 101},
	}, &web01); s != http.StatusCreated {
		t.Fatalf("web01: %d", s)
	}
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": cl.ID}, &web02)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "dns01", "cluster_id": infra.ID}, &dns01)

	// Facts zoals een agent ze meldt; web01 en web02 zijn VRRP-peers.
	setFacts := func(n node, mac, addr string, peer string) {
		t.Helper()
		f := map[string]any{
			"interfaces": []protocol.Interface{
				{Name: "lo", MAC: "00:00:00:00:00:00", Up: true, Addresses: []string{"127.0.0.1/8"}},
				{Name: "eth0", MAC: mac, Up: true, Addresses: []string{addr}, Kind: "physical"},
			},
			"default_gateway": "10.0.20.1",
		}
		if peer != "" {
			f["keepalived"] = protocol.Keepalived{Active: "active", VIPs: []string{"10.0.20.100"},
				Instances: []protocol.VRRPInstance{{Name: "VI_1", Interface: "eth0", VRID: 51, Peers: []string{peer}, VIPs: []string{"10.0.20.100"}}}}
		}
		b, _ := json.Marshal(f)
		if _, err := e.pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'h', $2, now())`, n.ID, b); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES ($1, $2, $3, 5)`,
			uuid.New(), n.ID, "U"+strings.ToUpper(strings.ReplaceAll(n.ID, "-", ""))); err != nil {
			t.Fatal(err)
		}
	}
	setFacts(web01, "BC:24:11:00:01:01", "10.0.20.11/24", "10.0.20.12")
	setFacts(web02, "bc:24:11:00:01:02", "10.0.20.12/24", "10.0.20.11")
	setFacts(dns01, "bc:24:11:00:02:00", "10.0.30.53/24", "")
	var vip struct{ ID string }
	if s := c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.20.100", "interface": "eth0", "vrid": 51}, &vip); s != http.StatusCreated {
		t.Fatalf("VIP: %d", s)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE vips SET owner_node_id = $1 WHERE id = $2`, web01.ID, vip.ID); err != nil {
		t.Fatal(err)
	}

	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != http.StatusOK {
		t.Fatalf("sync: %d", s)
	}
	if err := e.pve.ReadConfigs(ctx, connID); err != nil {
		t.Fatal(err)
	}

	// Voor het lezen staat het netwerk van de hosts nog leeg.
	var hn struct {
		Hosts []struct {
			Host       string    `json:"host"`
			Error      string    `json:"error"`
			ReadAt     time.Time `json:"read_at"`
			Interfaces []struct {
				Iface     string   `json:"iface"`
				Type      string   `json:"type"`
				Ports     []string `json:"ports"`
				CIDR      string   `json:"cidr"`
				VLANAware bool     `json:"vlan_aware"`
			} `json:"interfaces"`
		} `json:"hosts"`
	}
	if s := viewer.do("GET", "/api/v1/proxmox/"+conn.ID+"/network", nil, &hn); s != http.StatusOK || len(hn.Hosts) != 0 {
		t.Fatalf("netwerk voor het lezen: %d %+v", s, hn)
	}
	if s := viewer.do("GET", "/api/v1/proxmox/"+uuid.NewString()+"/network", nil, nil); s != http.StatusNotFound {
		t.Fatalf("onbekende koppeling: %d", s)
	}
	var net topo.Map
	viewer.do("GET", "/api/v1/map?layer=network", nil, &net)
	if !slices.ContainsFunc(net.Notes, func(n string) bool { return strings.Contains(n, "Het netwerk van pve1 en pve2 is nog niet gelezen") }) {
		t.Fatalf("opmerking over niet gelezen hosts: %q", net.Notes)
	}

	// Lezen; een tweede keer binnen tien minuten leest niets opnieuw.
	if err := e.pve.ReadNetworks(ctx, connID); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	if err := e.pool.QueryRow(ctx, `SELECT read_at FROM proxmox_host_network_reads WHERE pve_node = 'pve1'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	pve.SetHostNetwork("pve1", nil)
	if err := e.pve.ReadNetworks(ctx, connID); err != nil {
		t.Fatal(err)
	}
	viewer.do("GET", "/api/v1/proxmox/"+conn.ID+"/network", nil, &hn)
	if len(hn.Hosts) != 2 || hn.Hosts[0].Host != "pve1" || len(hn.Hosts[0].Interfaces) != 4 || hn.Hosts[0].Error != "" ||
		!hn.Hosts[0].ReadAt.Equal(first) {
		t.Fatalf("netwerk: %+v", hn)
	}
	if br := hn.Hosts[0].Interfaces[3]; br.Iface != "vmbr0" || br.Type != "bridge" || !br.VLANAware || br.CIDR != "192.168.1.11/24" ||
		!slices.Equal(br.Ports, []string{"bond0"}) {
		t.Fatalf("vmbr0 op pve1: %+v", br)
	}
	if bond := hn.Hosts[0].Interfaces[0]; bond.Iface != "bond0" || !slices.Equal(bond.Ports, []string{"eno1", "eno2"}) {
		t.Fatalf("bond0: %+v", bond)
	}

	// Voorstellen: web02 is VM 102 en dns01 container 200. web01 hangt al
	// aan een VM, en een tweede ronde maakt geen dubbele voorstellen.
	e.pve.ProposeLinks(ctx)
	e.pve.ProposeLinks(ctx)
	var links struct{ Items []nodeLinkView }
	if s := viewer.do("GET", "/api/v1/node-links", nil, &links); s != http.StatusOK || len(links.Items) != 2 {
		t.Fatalf("voorstellen: %d %+v", s, links)
	}
	byHost := map[string]nodeLinkView{}
	for _, l := range links.Items {
		byHost[l.Hostname] = l
	}
	if l := byHost["web02"]; l.VMID != 102 || l.Guest != "web02" || l.Host != "pve2" || l.Cluster != "Web" || l.MAC != "bc:24:11:00:01:02" {
		t.Fatalf("voorstel web02: %+v", l)
	}
	if l := byHost["dns01"]; l.VMID != 200 || l.Host != "pve2" {
		t.Fatalf("voorstel dns01: %+v", l)
	}
	var proposed int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = 'node.link_proposed'`).Scan(&proposed); err != nil || proposed != 2 {
		t.Fatalf("%d events node.link_proposed", proposed)
	}

	// Alleen een admin beslist.
	if s := viewer.do("POST", "/api/v1/node-links/"+byHost["web02"].ID+"/confirm", nil, nil); s != http.StatusForbidden {
		t.Fatalf("kijker bevestigt: %d", s)
	}
	if s := c.do("POST", "/api/v1/node-links/"+uuid.NewString()+"/confirm", nil, nil); s != http.StatusNotFound {
		t.Fatalf("onbekend voorstel: %d", s)
	}
	if s := c.do("POST", "/api/v1/node-links/"+byHost["web02"].ID+"/confirm", nil, nil); s != http.StatusNoContent {
		t.Fatalf("bevestigen: %d", s)
	}
	if s := c.do("POST", "/api/v1/node-links/"+byHost["web02"].ID+"/ignore", nil, nil); s != http.StatusConflict {
		t.Fatalf("na bevestigen negeren: %d", s)
	}
	if s := c.do("POST", "/api/v1/node-links/"+byHost["dns01"].ID+"/ignore", nil, nil); s != http.StatusNoContent {
		t.Fatalf("negeren: %d", s)
	}
	var w2 struct {
		Proxmox *struct {
			ConnectionID string `json:"connection_id"`
			VMID         int    `json:"vmid"`
		} `json:"proxmox"`
	}
	c.do("GET", "/api/v1/nodes/"+web02.ID, nil, &w2)
	if w2.Proxmox == nil || w2.Proxmox.VMID != 102 || w2.Proxmox.ConnectionID != conn.ID {
		t.Fatalf("web02 na bevestigen: %+v", w2.Proxmox)
	}
	e.pve.ProposeLinks(ctx)
	viewer.do("GET", "/api/v1/node-links", nil, &links)
	if len(links.Items) != 0 {
		t.Fatalf("na beslissen: %+v", links.Items)
	}
	var actions []string
	rows, _ := e.pool.Query(ctx, `SELECT action FROM events WHERE action LIKE 'node.%' AND subject_id IN ($1, $2) ORDER BY id`, web02.ID, dns01.ID)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if !slices.Contains(actions, "node.link_confirmed") || !slices.Contains(actions, "node.link_ignored") || !slices.Contains(actions, "node.updated") {
		t.Fatalf("events: %v", actions)
	}

	// Bevestigen terwijl de node intussen met de hand gekoppeld is.
	if _, err := q.InsertLinkProposal(ctx, store.InsertLinkProposalParams{ConnectionID: connID, Vmid: 200, NodeID: uuid.MustParse(web01.ID), Mac: "x"}); err != nil {
		t.Fatal(err)
	}
	viewer.do("GET", "/api/v1/node-links", nil, &links)
	if len(links.Items) != 1 {
		t.Fatalf("handmatig voorstel: %+v", links.Items)
	}
	var cf fieldErr
	if s := c.do("POST", "/api/v1/node-links/"+links.Items[0].ID+"/confirm", nil, &cf); s != http.StatusConflict || !strings.Contains(cf.Message, "hangt al aan een VM") {
		t.Fatalf("al gekoppeld: %d %+v", s, cf)
	}

	// De fysieke laag.
	var phys topo.Map
	if s := viewer.do("GET", "/api/v1/map?layer=physical", nil, &phys); s != http.StatusOK {
		t.Fatalf("fysiek: %d", s)
	}
	items, edges := mapIndex(phys)
	g101, g102, g200 := topo.GuestKey(connID, 101), topo.GuestKey(connID, 102), topo.GuestKey(connID, 200)
	if it := items[g101]; it.Name != "web01" || it.Parent != topo.HostKey(connID, "pve1") || it.Cluster != "Web" || it.NodeID == nil {
		t.Fatalf("web01: %+v", it)
	}
	if it := items[g102]; it.Name != "web02" || it.Parent != topo.HostKey(connID, "pve2") {
		t.Fatalf("web02 na koppelen: %+v", it)
	}
	if _, ok := items[topo.GuestKey(connID, 9000)]; ok {
		t.Fatal("template op de kaart")
	}
	local, ceph := topo.StorageKey(connID, "local", "pve1", false), topo.StorageKey(connID, "ceph", "", true)
	if items[local].Parent != topo.HostKey(connID, "pve1") || items[ceph].Parent != "" {
		t.Fatalf("storage: %+v %+v", items[local], items[ceph])
	}
	for _, want := range []string{g101 + ">" + local + " disk", g102 + ">" + ceph + " disk"} {
		if !slices.Contains(edges, want) {
			t.Fatalf("lijn %q ontbreekt: %v", want, edges)
		}
	}

	// Gefilterd op cluster Web: dns01 en zijn host vallen weg als ze
	// niets van Web dragen.
	viewer.do("GET", "/api/v1/map?layer=physical&cluster_id="+cl.ID, nil, &phys)
	items, _ = mapIndex(phys)
	if _, ok := items[g200]; ok || items[g101].ID == "" || items[g102].ID == "" {
		t.Fatalf("filter op Web: %v", phys.Items)
	}
	viewer.do("GET", "/api/v1/map?layer=physical&environment=lab", nil, &phys)
	if len(phys.Items) != 0 {
		t.Fatalf("lab zonder gekoppelde VM's: %+v", phys.Items)
	}

	// De netwerklaag.
	if s := viewer.do("GET", "/api/v1/map?layer=network", nil, &net); s != http.StatusOK {
		t.Fatalf("netwerk: %d", s)
	}
	items, edges = mapIndex(net)
	n1, n2 := "node:"+web01.ID, "node:"+web02.ID
	sub := "subnet:10.0.20.0/24"
	if it := items[sub]; it.Detail != "VLAN 20 · 2 nodes" || factOf(it, "Nodes") != "web01, web02" {
		t.Fatalf("subnet: %+v", it)
	}
	if it := items["subnet:192.168.1.0/24"]; it.Detail != "geen nodes" {
		t.Fatalf("beheernet: %+v", it)
	}
	br1 := topo.BridgeKey(connID, "pve1", "vmbr0")
	if it := items[br1]; it.Parent != topo.HostKey(connID, "pve1") || it.Detail != "bond0 (eno1, eno2)" || factOf(it, "VLAN-aware") != "ja" {
		t.Fatalf("vmbr0 op pve1: %+v", it)
	}
	vipKey := "vip:" + vip.ID
	for _, want := range []string{
		sub + ">" + n1 + " member", sub + ">" + n2 + " member",
		br1 + ">" + sub + " uplink VLAN 20", topo.BridgeKey(connID, "pve2", "vmbr0") + ">" + sub + " uplink VLAN 20",
		br1 + ">subnet:192.168.1.0/24 uplink",
		"gateway:10.0.20.1>" + sub + " gateway", "gateway:192.168.1.1>subnet:192.168.1.0/24 gateway",
		vipKey + ">" + n1 + " owner eigenaar",
	} {
		if !slices.Contains(edges, want) {
			t.Fatalf("lijn %q ontbreekt: %v", want, edges)
		}
	}
	a, b := n1, n2
	if web02.ID < web01.ID {
		a, b = n2, n1
	}
	if !slices.Contains(edges, a+">"+b+" vrrp VRRP 51") {
		t.Fatalf("VRRP-lijn ontbreekt: %v", edges)
	}
	if strings.Contains(strings.Join(net.Notes, " "), "nog niet gelezen") {
		t.Fatalf("opmerkingen na lezen: %q", net.Notes)
	}

	// Gefilterd op Web: geen dns01, en geen gateway van de hosts.
	viewer.do("GET", "/api/v1/map?layer=network&cluster_id="+cl.ID, nil, &net)
	items, _ = mapIndex(net)
	if _, ok := items["node:"+dns01.ID]; ok {
		t.Fatal("dns01 in filter op Web")
	}
	if _, ok := items["gateway:192.168.1.1"]; ok {
		t.Fatal("gateway van de hosts in filter op Web")
	}
	if items[vipKey].ID == "" || items[sub].ID == "" {
		t.Fatalf("filter op Web: %+v", net.Items)
	}

	if s := viewer.do("GET", "/api/v1/map?layer=diensten", nil, nil); s != http.StatusBadRequest {
		t.Fatalf("onbekende laag: %d", s)
	}
}
