package deploy

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func facts(addr string) NodeState {
	return NodeState{Facts: &protocol.Facts{Interfaces: []protocol.Interface{{Name: "eth0", Addresses: []string{addr + "/24"}}}}}
}

// testDeploy is een uitrol zoals Request hem plant.
func testDeploy() (*templates.Template, params, map[uuid.UUID]NodeState) {
	tpl, _ := templates.Get("keepalived-nginx", "1.0.0")
	p := params{
		Template: "keepalived-nginx", Version: "1.0.0", ClusterID: uuid.New(),
		Cluster: templates.ClusterInfo{Name: "Web", Slug: "web", Environment: "prod"},
		Params:  map[string]any{"vip": "10.0.20.100", "node_count": 2, "vrid": 51, "cpu": 2, "memory": "2G", "disk": "20G"},
		Secrets: []string{"auth_pass"},
		Target:  Target{Network: "static", FirstIP: "10.0.20.11/24", Gateway: "10.0.20.1"},
	}
	nodes := map[uuid.UUID]NodeState{}
	for i, host := range []string{"web-01", "web-02"} {
		n := plannedNode{ID: uuid.New(), Hostname: host, Role: "web", Index: i + 1, Address: "10.0.20.1" + string(rune('1'+i)), Prefix: 24}
		p.Nodes = append(p.Nodes, n)
		nodes[n.ID] = facts(n.Address)
	}
	return tpl, p, nodes
}

func TestRenderNodeFromStoredSpec(t *testing.T) {
	tpl, p, nodes := testDeploy()
	secrets := map[string]string{"auth_pass": "geheim12"}

	// Wat de uitrol rendert, en wat later uit clusters.spec komt.
	raw, err := json.Marshal(p.spec())
	if err != nil {
		t.Fatal(err)
	}
	inv := []InventoryNode{{p.Nodes[0].ID, "web-01", "active"}, {p.Nodes[1].ID, "web-02", "active"}}
	stored, err := ParseSpec(raw, templates.ClusterInfo{Name: "Hernoemd", Slug: "web", Environment: "prod"}, inv)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Nodes {
		want, err := RenderNode(tpl, p.spec(), secrets, nodes, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := RenderNode(tpl, stored, secrets, nodes, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: uit de spec %+v, bij de uitrol %+v", n.Hostname, got, want)
		}
		conf := got[3].File.Content
		if !strings.Contains(conf, "auth_pass geheim12") || !strings.Contains(conf, "unicast_src_ip "+n.Address) {
			t.Fatalf("keepalived.conf van %s:\n%s", n.Hostname, conf)
		}
	}
	// De kopie van het cluster wint van de huidige naam.
	if stored.Cluster.Name != "Web" {
		t.Fatalf("cluster in de spec: %+v", stored.Cluster)
	}
	if m := stored.Membership(inv); len(m) != 0 {
		t.Fatalf("lidmaatschap: %v", m)
	}

	// Een ontbrekend geheim is een fout, geen nieuwe waarde.
	if _, err := RenderNode(tpl, stored, nil, nodes, p.Nodes[0].ID); err == nil || !strings.Contains(err.Error(), "geheim auth_pass ontbreekt") {
		t.Fatalf("zonder geheim: %v", err)
	}
	// Een andere templateversie rendert deze spec niet.
	other := *tpl
	other.Version = "1.1.0"
	if _, err := RenderNode(&other, stored, secrets, nodes, p.Nodes[0].ID); err == nil || !strings.Contains(err.Error(), "1.0.0") {
		t.Fatalf("andere versie: %v", err)
	}
}

func TestParseOldSpec(t *testing.T) {
	tpl, p, nodes := testDeploy()
	secrets := map[string]string{"auth_pass": "geheim12"}
	// Zo schreef fase 1 de spec: zonder node_id, index, prefix en cluster.
	old := `{"template":{"name":"keepalived-nginx","version":"1.0.0"},
		"params":{"vip":"10.0.20.100","node_count":2,"vrid":51,"cpu":2,"memory":"2G","disk":"20G"},
		"secrets":["auth_pass"],
		"target":{"proxmox_id":"` + uuid.NewString() + `","image_vmid":9000,"bridge":"vmbr0","network":"static","first_ip":"10.0.20.11/24","gateway":"10.0.20.1"},
		"nodes":[{"hostname":"web-01","role":"web","address":"10.0.20.11","vm":{"cpu":2,"memory_mib":2048,"disk_gib":20}},
		         {"hostname":"web-02","role":"web","address":"10.0.20.12","vm":{"cpu":2,"memory_mib":2048,"disk_gib":20}}]}`
	inv := []InventoryNode{{p.Nodes[1].ID, "web-02", "active"}, {p.Nodes[0].ID, "web-01", "active"}}
	s, err := ParseSpec([]byte(old), p.Cluster, inv)
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range s.Nodes {
		if n.NodeID != p.Nodes[i].ID || n.Index != i+1 || n.Prefix != 24 {
			t.Fatalf("node %d: %+v", i, n)
		}
	}
	for _, n := range p.Nodes {
		want, _ := RenderNode(tpl, p.spec(), secrets, nodes, n.ID)
		got, err := RenderNode(tpl, s, secrets, nodes, n.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s uit een oude spec: %v", n.Hostname, err)
		}
	}
	if _, err := ParseSpec([]byte(`{}`), p.Cluster, nil); err != ErrNoSpec {
		t.Fatalf("lege spec: %v", err)
	}
}

func TestMembership(t *testing.T) {
	_, p, _ := testDeploy()
	s := p.spec()
	extra := uuid.New()
	got := s.Membership([]InventoryNode{
		{p.Nodes[0].ID, "web-01-nieuw", "active"},
		{extra, "web-03", "active"},
		{uuid.New(), "web-04", "maintenance"},
	})
	want := []string{
		"web-01 heet nu web-01-nieuw; de specificatie kent hem als web-01",
		"web-02 staat in de specificatie, maar is geen node van dit cluster meer",
		"web-03 is een actieve node van dit cluster, maar staat niet in de specificatie",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("lidmaatschap:\n%q\nwil\n%q", got, want)
	}
}

func TestSpecChanges(t *testing.T) {
	tpl, p, _ := testDeploy()
	old := p.spec()
	cur := p.spec()
	cur.Template.Version = "1.1.0"
	cur.Params = map[string]any{"vip": "10.0.20.100", "node_count": 3, "vrid": 51, "cpu": 2, "memory": "4G", "disk": "20G"}
	cur.Nodes = append(cur.Nodes, SpecNode{Hostname: "web-03", Role: "web", Index: 3})
	got := specChanges(tpl, old, cur)
	want := []Change{
		{"Template", "keepalived-nginx 1.0.0", "keepalived-nginx 1.1.0"},
		{"Geheugen per node", "2G", "4G"},
		{"Aantal nodes", "2", "3"},
		{"Nodes", "web-01, web-02", "web-01, web-02, web-03"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wijzigingen:\n%q\nwil\n%q", got, want)
	}
}
