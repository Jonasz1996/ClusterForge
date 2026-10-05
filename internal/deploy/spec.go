package deploy

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Spec is de gewenste staat van een cluster uit een template, zoals ze in
// clusters.spec en in elke revisie staat. Met de templateversie, de
// geheimen en de facts van de nodes geeft ze voor elke node precies de
// stappen van de uitrol. Geheimen staan er niet in, alleen hun namen.
type Spec struct {
	Template SpecTemplate `json:"template"`
	// Cluster is een kopie van naam, slug en omgeving bij het uitrollen:
	// de sjablonen renderen daarmee, ook als het cluster later hernoemd wordt.
	Cluster templates.ClusterInfo `json:"cluster"`
	Params  map[string]any        `json:"params"`
	Secrets []string              `json:"secrets"`
	Target  Target                `json:"target"`
	Nodes   []SpecNode            `json:"nodes"`
}

type SpecTemplate struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SpecNode is een node in de gewenste staat.
type SpecNode struct {
	// NodeID is leeg in een spec van voor fase 2 tot ParseSpec hem op
	// hostname koppelt, en blijft leeg als die node niet meer bestaat.
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Role     string    `json:"role"`
	Index    int       `json:"index"`
	// Address en Prefix zijn leeg bij DHCP; dan komen ze uit de facts.
	Address string            `json:"address,omitempty"`
	Prefix  int               `json:"prefix,omitempty"`
	VM      templates.VMShape `json:"vm"`
}

// spec is de gewenste staat die een uitrol vastlegt.
func (p *params) spec() Spec {
	s := Spec{
		Template: SpecTemplate{Name: p.Template, Version: p.Version}, Cluster: p.Cluster,
		Params: p.Params, Secrets: p.Secrets, Target: p.Target, Nodes: make([]SpecNode, 0, len(p.Nodes)),
	}
	if s.Secrets == nil {
		s.Secrets = []string{}
	}
	for _, n := range p.Nodes {
		s.Nodes = append(s.Nodes, SpecNode{
			NodeID: n.ID, Hostname: n.Hostname, Role: n.Role, Index: n.Index, Address: n.Address, Prefix: n.Prefix, VM: n.VM,
		})
	}
	return s
}

// InventoryNode is een node van het cluster zoals de inventory hem kent.
type InventoryNode struct {
	ID        uuid.UUID
	Hostname  string
	Lifecycle string
}

var indexRe = regexp.MustCompile(`-(\d+)$`)

// ParseSpec leest een spec. Een spec van voor fase 2 mist node_id, index,
// prefix en de kopie van het cluster; die vult ParseSpec aan uit de
// inventory: de nodes op hostname, de index uit het nummer aan het eind van
// de hostname, de prefix uit het eerste adres en het cluster zoals het nu is.
func ParseSpec(raw []byte, cluster templates.ClusterInfo, nodes []InventoryNode) (Spec, error) {
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return Spec{}, fmt.Errorf("spec is ongeldig: %w", err)
	}
	if s.Template.Name == "" || s.Template.Version == "" {
		return Spec{}, ErrNoSpec
	}
	if s.Cluster == (templates.ClusterInfo{}) {
		s.Cluster = cluster
	}
	prefix := 0
	if pf, err := netip.ParsePrefix(s.Target.FirstIP); err == nil {
		prefix = pf.Bits()
	}
	perRole := map[string]int{}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		perRole[n.Role]++
		if n.NodeID == uuid.Nil {
			for _, inv := range nodes {
				if inv.Hostname == n.Hostname {
					n.NodeID = inv.ID
				}
			}
		}
		if n.Index == 0 {
			n.Index = perRole[n.Role]
			if m := indexRe.FindStringSubmatch(n.Hostname); m != nil {
				n.Index, _ = strconv.Atoi(m[1])
			}
		}
		if n.Prefix == 0 && n.Address != "" {
			n.Prefix = prefix
		}
	}
	return s, nil
}

// Membership vergelijkt de actieve nodes van het cluster met de nodes in de
// spec. Elke afwijking is een zin; zolang er een is, klopt de gewenste staat
// niet met het cluster.
func (s Spec) Membership(nodes []InventoryNode) []string {
	var out []string
	inSpec := map[uuid.UUID]bool{}
	byID := map[uuid.UUID]InventoryNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, n := range s.Nodes {
		inv, ok := byID[n.NodeID]
		switch {
		case n.NodeID == uuid.Nil || !ok:
			out = append(out, n.Hostname+" staat in de specificatie, maar is geen node van dit cluster meer")
		case inv.Hostname != n.Hostname:
			out = append(out, fmt.Sprintf("%s heet nu %s; de specificatie kent hem als %s", n.Hostname, inv.Hostname, n.Hostname))
		}
		inSpec[n.NodeID] = true
	}
	for _, n := range nodes {
		if n.Lifecycle == "active" && !inSpec[n.ID] {
			out = append(out, n.Hostname+" is een actieve node van dit cluster, maar staat niet in de specificatie")
		}
	}
	return out
}
