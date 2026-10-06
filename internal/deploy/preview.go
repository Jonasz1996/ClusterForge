package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Wat hier staat, rendert en plant zonder iets te maken of te versturen:
// GitOps toont ermee wat een wijziging zou doen. Geheimen krijgen een
// plaatshouder, dus er wordt niets ontsleuteld.

// Nodes leest de nodes van een cluster: voor de lidmaatschapscontrole en
// met wat renderen van ze nodig heeft.
func (s *Service) Nodes(ctx context.Context, clusterID uuid.UUID) ([]InventoryNode, map[uuid.UUID]NodeState, error) {
	return s.loadNodes(ctx, clusterID)
}

// MaskedContext is Context met templates.SecretPlaceholder voor elk geheim.
func (s Spec) MaskedContext(tpl *templates.Template, nodes map[uuid.UUID]NodeState) (templates.Context, error) {
	if tpl.Name != s.Template.Name || tpl.Version != s.Template.Version {
		return templates.Context{}, fmt.Errorf("de spec hoort bij %s %s, niet bij %s %s", s.Template.Name, s.Template.Version, tpl.Name, tpl.Version)
	}
	values, err := tpl.MaskedValues(s.Params)
	if err != nil {
		return templates.Context{}, fmt.Errorf("parameters passen niet bij %s %s: %w", tpl.Name, tpl.Version, err)
	}
	c := templates.Context{Params: values, Cluster: s.Cluster}
	for _, n := range s.Nodes {
		info, err := nodeInfo(n, nodes[n.NodeID])
		if err != nil {
			return templates.Context{}, err
		}
		c.Nodes = append(c.Nodes, info)
	}
	return c, nil
}

// RenderMasked geeft de stappen van één node met plaatshouders voor de
// geheimen. Ze zijn om te tonen, nooit om toe te passen.
func RenderMasked(tpl *templates.Template, s Spec, nodes map[uuid.UUID]NodeState, nodeID uuid.UUID) ([]templates.Step, error) {
	c, err := s.MaskedContext(tpl, nodes)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(s.Nodes, func(n SpecNode) bool { return n.NodeID == nodeID })
	if i < 0 {
		return nil, fmt.Errorf("node %s staat niet in de specificatie", nodeID)
	}
	c.Node = &c.Nodes[i]
	return tpl.Steps(s.Nodes[i].Role, c)
}

// CheckNew controleert een nieuw cluster zoals een uitrol dat doet, met
// Proxmox, adressen, VIP en capaciteit, maar zonder het adres van
// ClusterForge en zonder iets te maken. version kiest de templateversie.
func (s *Service) CheckNew(ctx context.Context, req Request, version string) (Plan, error) {
	_, p, err := s.planVersion(ctx, req, version, false)
	if err != nil {
		return Plan{}, err
	}
	out := Plan{VIP: p.VIP}
	out.VRID, _ = p.Params["vrid"].(int)
	for _, n := range p.Nodes {
		out.Nodes = append(out.Nodes, PlannedNode{
			Hostname: n.Hostname, Role: n.Role, Address: n.Address, Prefix: n.Prefix, Host: n.Host, VM: n.VM,
		})
	}
	return out, nil
}

// PlannedID is het id waarmee een node die nog niet bestaat in een plan
// staat; zo is hij te renderen.
func PlannedID(hostname string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("clusterforge planned node "+hostname))
}

// Growth zijn de nodes die er bij een hoger aantal bijkomen.
type Growth struct {
	Nodes []SpecNode
	// States is wat het plan van de nieuwe nodes aanneemt: het geplande
	// adres en de netwerkkaart van een bestaande node.
	States map[uuid.UUID]NodeState
	// Warnings zeggen wat het plan aanneemt of niet kon controleren.
	Warnings []string
}

// PlanGrowth plant de nodes die erbij komen als values meer nodes vragen
// dan de spec heeft. Een nieuwe node krijgt per rol de laagste vrije index,
// want de keepalived-prioriteit volgt uit de index; met vaste adressen het
// adres dat bij die index hoort. Een lager aantal is hier geen fout; dat
// controleert de aanroeper.
func (s *Service) PlanGrowth(ctx context.Context, spec Spec, tpl *templates.Template, values map[string]any,
	states map[uuid.UUID]NodeState) (Growth, error) {
	g := Growth{States: map[uuid.UUID]NodeState{}}
	c := templates.Context{Params: values, Cluster: spec.Cluster}
	counts, err := tpl.Counts(c)
	if err != nil {
		return g, invalid("params", "%s", err.Error())
	}
	var first netip.Prefix
	static := spec.Target.Network == "static"
	if static {
		if first, err = netip.ParsePrefix(spec.Target.FirstIP); err != nil {
			return g, invalid("target.first_ip", "het eerste adres in de spec is ongeldig: %s", spec.Target.FirstIP)
		}
	}
	used, err := s.usedAddresses(ctx)
	if err != nil {
		return g, err
	}
	// De netwerkkaart van een bestaande node, voor de sjablonen van een
	// nieuwe.
	iface := ""
	for _, n := range spec.Nodes {
		if info, err := nodeInfo(n, states[n.NodeID]); err == nil {
			iface = info.Interface
			break
		}
	}
	for i, r := range tpl.Roles {
		var have []int
		for _, n := range spec.Nodes {
			if n.Role == r.Name {
				have = append(have, n.Index)
			}
		}
		if counts[i] <= len(have) {
			continue
		}
		vm, err := tpl.VM(r.Name, c)
		if err != nil {
			return g, invalid("params", "%s", err.Error())
		}
		for idx := 1; len(have) < counts[i]; idx++ {
			if slices.Contains(have, idx) {
				continue
			}
			have = append(have, idx)
			host := fmt.Sprintf("%s-%02d", spec.Cluster.Slug, idx)
			if len(tpl.Roles) > 1 {
				host = fmt.Sprintf("%s-%s-%02d", spec.Cluster.Slug, r.Name, idx)
			}
			if _, err := s.q.FindNodeByHostname(ctx, host); err == nil {
				return g, invalid("params."+tpl.CountParam(r.Name), "er bestaat al een node %s", host)
			}
			n := SpecNode{NodeID: PlannedID(host), Hostname: host, Role: r.Name, Index: idx, VM: vm}
			st := NodeState{}
			if static {
				a, err := growthAddress(first, spec.Target.Gateway, idx, len(tpl.Roles) > 1, used)
				if err != nil {
					return g, invalid("params."+tpl.CountParam(r.Name), "%s", err.Error())
				}
				used[a.String()] = true
				n.Address, n.Prefix = a.String(), first.Bits()
				st.PrimaryIP = n.Address
				if iface != "" {
					st.Facts = &protocol.Facts{PrimaryAddress: n.Address, Interfaces: []protocol.Interface{
						{Name: iface, Addresses: []string{netip.PrefixFrom(a, first.Bits()).String()}},
					}}
				}
				g.Warnings = append(g.Warnings, fmt.Sprintf("%s bestaat nog niet; het plan rekent met adres %s en netwerkkaart %s van de bestaande nodes.",
					host, n.Address, or(iface, "(onbekend)")))
			} else {
				g.Warnings = append(g.Warnings, fmt.Sprintf("%s bestaat nog niet en krijgt zijn adres via DHCP; wat er op de andere nodes verandert, is pas te zien als dat adres bekend is.", host))
			}
			g.Nodes = append(g.Nodes, n)
			g.States[n.NodeID] = st
		}
	}
	if len(g.Nodes) > 0 {
		warn, err := s.capacity(ctx, spec.Target, g.Nodes)
		if err != nil {
			return g, err
		}
		g.Warnings = append(g.Warnings, warn...)
	}
	return g, nil
}

// growthAddress is het adres van een nieuwe node: bij één rol het adres
// dat bij zijn index hoort, anders het eerste vrije na het eerste adres.
func growthAddress(first netip.Prefix, gateway string, idx int, multiRole bool, used map[string]bool) (netip.Addr, error) {
	subnet := first.Masked()
	ok := func(a netip.Addr) bool {
		return subnet.Contains(a) && a != subnet.Addr() && a != lastAddr(subnet) && a.String() != gateway
	}
	a := first.Addr()
	if !multiRole {
		for range idx - 1 {
			a = a.Next()
		}
		switch {
		case !ok(a):
			return a, fmt.Errorf("het adres voor node %d (%s) valt buiten %s", idx, a, subnet)
		case used[a.String()]:
			return a, fmt.Errorf("%s, het adres voor node %d, is al in gebruik bij een node of VIP", a, idx)
		}
		return a, nil
	}
	for ; ok(a); a = a.Next() {
		if !used[a.String()] {
			return a, nil
		}
	}
	return a, fmt.Errorf("er is geen vrij adres meer in %s na %s", subnet, first.Addr())
}

// capacity controleert of de nieuwe VM's op de online hosts passen, zoals
// een uitrol dat doet. Is Proxmox niet te bereiken, dan is dat een
// waarschuwing: het plan kan dan niet alles controleren.
func (s *Service) capacity(ctx context.Context, t Target, nodes []SpecNode) ([]string, error) {
	if s.pve == nil || t.ProxmoxID == uuid.Nil {
		return []string{"De capaciteit in Proxmox is niet gecontroleerd: de spec noemt geen Proxmox-koppeling."}, nil
	}
	img, err := s.pve.Image(ctx, t.ProxmoxID, t.ImageVMID)
	var ve proxmox.ValidationError
	switch {
	case errors.As(err, &ve):
		return nil, invalid("target.image_vmid", "%s", ve.Msg)
	case errors.Is(err, proxmox.ErrNotFound):
		return nil, invalid("target.image_vmid", "het golden image %d bestaat niet meer in Proxmox", t.ImageVMID)
	case err != nil:
		return []string{"De capaciteit in Proxmox is niet gecontroleerd: " + err.Error() + "."}, nil
	}
	p := &params{Target: t, ImageShared: img.Shared}
	for _, n := range nodes {
		p.Nodes = append(p.Nodes, plannedNode{Hostname: n.Hostname, Role: n.Role, VM: n.VM})
	}
	if err := s.placeNodes(ctx, p); err != nil {
		var dv ValidationError
		if errors.As(err, &dv) {
			return nil, err
		}
		return []string{"De capaciteit in Proxmox is niet gecontroleerd: " + err.Error() + "."}, nil
	}
	return nil, nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
