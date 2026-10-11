package deps

import (
	"cmp"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Target is wat er denkbeeldig uitvalt: precies één dienst, cluster of node.
// Outage kan er meer tegelijk.
type Target struct {
	Service *uuid.UUID
	Cluster *uuid.UUID
	Node    *uuid.UUID
}

// Hit is een dienst die geraakt wordt.
type Hit struct {
	Service uuid.UUID
	Impact  Impact
	// Direct is true voor wat de storing zelf raakt; anders kwam de uitval
	// langs een pijl, en dan zijn Path en Edge gevuld.
	Direct bool
	Reason string
	Path   []uuid.UUID
	Edge   *Dep
}

var sourceLabels = map[string]string{SourceTemplate: "template", SourceManual: "handmatig", SourceDiscovered: "ontdekt"}

// SourceLabel is de bron in gewone woorden.
func SourceLabel(s string) string { return or(sourceLabels[s], s) }

// Outage is wat er tegelijk denkbeeldig uitvalt: diensten, clusters en
// nodes, zoals bij een Proxmox-host die uitvalt.
type Outage struct {
	Services []uuid.UUID
	Clusters []uuid.UUID
	Nodes    []uuid.UUID
}

// Impact rekent uit wat er geraakt wordt als het doel uitvalt. Het is
// dezelfde zoektocht als bij een echte uitval. Bij een node telt een tweede
// gezonde instantie mee, net als de overname van het VIP. Het doel zelf staat
// er niet in; bij een cluster of node staan zijn eigen diensten erin als
// Direct.
func (g *Graph) Impact(t Target) []Hit {
	var o Outage
	switch {
	case t.Service != nil:
		o.Services = []uuid.UUID{*t.Service}
	case t.Cluster != nil:
		o.Clusters = []uuid.UUID{*t.Cluster}
	case t.Node != nil:
		o.Nodes = []uuid.UUID{*t.Node}
	}
	return g.ImpactOf(o)
}

// ImpactOf rekent uit wat er geraakt wordt als alles in o tegelijk uitvalt.
// De gekozen diensten zelf staan er niet in.
func (g *Graph) ImpactOf(o Outage) []Hit {
	hits := map[uuid.UUID]Hit{}
	var roots []uuid.UUID
	chosen := map[uuid.UUID]bool{}
	for _, id := range o.Services {
		if g.svc[id] != nil {
			chosen[id] = true
			roots = append(roots, id)
		}
	}
	direct := func(h Hit) {
		if chosen[h.Service] {
			return
		}
		if prev, ok := hits[h.Service]; ok && (prev.Impact == ImpactDown || h.Impact != ImpactDown) {
			return
		}
		hits[h.Service] = h
		if h.Impact == ImpactDown {
			roots = append(roots, h.Service)
		}
	}
	for _, c := range o.Clusters {
		for _, s := range g.Services {
			if s.State == StateConfirmed && s.ClusterID != nil && *s.ClusterID == c {
				direct(Hit{Service: s.ID, Impact: ImpactDown, Direct: true, Reason: "valt uit met het cluster"})
			}
		}
	}
	for _, h := range g.nodeOutage(o.Nodes) {
		direct(h)
	}
	for id, e := range g.propagate(roots) {
		if chosen[id] {
			continue
		}
		if prev, ok := hits[id]; ok && (prev.Impact == ImpactDown || e.Impact != ImpactDown) {
			continue
		}
		var edge *Dep
		for _, d := range g.providers[id] {
			if d.ID == e.Edge {
				edge = &d
				break
			}
		}
		hits[id] = Hit{Service: id, Impact: e.Impact, Reason: g.effectReason(id, e, edge), Path: e.Path, Edge: edge}
	}
	out := make([]Hit, 0, len(hits))
	for _, h := range hits {
		out = append(out, h)
	}
	g.SortHits(out)
	return out
}

// effectReason zegt waarom een dienst geraakt wordt: "hangt hard af van
// mariadb in db-prod · handmatig" of "via php8.2-fpm".
func (g *Graph) effectReason(id uuid.UUID, e Effect, edge *Dep) string {
	if e.Via != e.Cause || edge == nil {
		return "via " + g.labelFrom(id, e.Via)
	}
	how := "hangt hard af van "
	if edge.Strength == Soft {
		how = "hangt zacht af van "
	}
	return how + g.labelFrom(id, e.Via) + " · " + SourceLabel(edge.Source)
}

// LabelFrom noemt een dienst zoals een andere hem ziet: met zijn groep
// erbij als die anders is.
func (g *Graph) LabelFrom(from, id uuid.UUID) string { return g.labelFrom(from, id) }

func (g *Graph) labelFrom(from, id uuid.UUID) string {
	s := g.svc[id]
	if s == nil {
		return ""
	}
	if f := g.svc[from]; f != nil && g.GroupOf(*f).Key == g.GroupOf(*s).Key {
		return s.Name
	}
	return s.Name + " in " + g.GroupOf(*s).Name
}

// nodeOutage zegt wat de uitval van een of meer nodes rechtstreeks doet met
// de bevestigde diensten van hun clusters, of met die op de nodes zelf.
func (g *Graph) nodeOutage(ids []uuid.UUID) []Hit {
	failed := map[uuid.UUID]*Node{}
	byCluster := map[uuid.UUID][]*Node{}
	for _, id := range ids {
		n := g.nodes[id]
		if n == nil || failed[id] != nil {
			continue
		}
		failed[id] = n
		if n.ClusterID != nil && g.clusters[*n.ClusterID] != nil {
			byCluster[*n.ClusterID] = append(byCluster[*n.ClusterID], n)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	byHostname := func(a, b *Node) int { return cmp.Compare(a.Hostname, b.Hostname) }
	others := map[uuid.UUID][]*Node{}
	for cid, down := range byCluster {
		slices.SortFunc(down, byHostname)
		for i := range g.Nodes {
			o := &g.Nodes[i]
			if failed[o.ID] == nil && o.Active && o.ClusterID != nil && *o.ClusterID == cid {
				others[cid] = append(others[cid], o)
			}
		}
		slices.SortFunc(others[cid], byHostname)
	}
	var out []Hit
	add := func(s Svc, impact Impact, reason string) {
		out = append(out, Hit{Service: s.ID, Impact: impact, Direct: true, Reason: reason})
	}
	for _, s := range g.Services {
		if s.State != StateConfirmed {
			continue
		}
		if s.NodeID != nil && failed[*s.NodeID] != nil {
			add(s, ImpactDown, "draait op "+failed[*s.NodeID].Hostname)
			continue
		}
		if s.ClusterID == nil || byCluster[*s.ClusterID] == nil {
			continue
		}
		cluster, down, rest := g.clusters[*s.ClusterID], byCluster[*s.ClusterID], others[*s.ClusterID]
		if s.Kind == "vip" && len(cluster.VIPs) > 0 {
			g.vipOutage(s, down, cluster, rest, add)
			continue
		}
		if s.Unit == "" {
			if len(rest) == 0 {
				add(s, ImpactDown, onlyNodes(down, cluster))
			}
			continue
		}
		if !slices.ContainsFunc(down, func(n *Node) bool { _, ok := n.Units[s.Unit]; return ok }) {
			continue
		}
		var elsewhere []string
		for _, o := range rest {
			if o.Fresh && running(o.Units[s.Unit]) {
				elsewhere = append(elsewhere, o.Hostname)
			}
		}
		if len(elsewhere) == 0 {
			add(s, ImpactDown, s.Unit+" draait op geen andere node")
			continue
		}
		add(s, ImpactDegraded, "draait nog op "+list(elsewhere))
	}
	return out
}

// onlyNodes zegt dat de uitgevallen nodes de enige actieve van het cluster
// waren.
func onlyNodes(down []*Node, c *Cluster) string {
	if len(down) == 1 {
		return down[0].Hostname + " is de enige actieve node van " + c.Name
	}
	return list(hostnames(down)) + " zijn alle actieve nodes van " + c.Name
}

func hostnames(nodes []*Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Hostname
	}
	return out
}

// vipOutage: neemt een andere node het VIP over, dan is de dienst alleen
// verminderd.
func (g *Graph) vipOutage(s Svc, down []*Node, c *Cluster, others []*Node, add func(Svc, Impact, string)) {
	var standby []string
	for _, o := range others {
		if o.Fresh && (s.Unit == "" || running(o.Units[s.Unit])) {
			standby = append(standby, o.Hostname)
		}
	}
	var owned []string
	for _, v := range c.VIPs {
		if slices.ContainsFunc(down, func(n *Node) bool { return v.Owner == n.ID }) {
			owned = append(owned, v.Address)
		}
	}
	runsHere := slices.ContainsFunc(down, func(n *Node) bool { _, ok := n.Units[s.Unit]; return ok })
	switch {
	case len(standby) == 0 && (len(owned) > 0 || runsHere || s.Unit == ""):
		add(s, ImpactDown, "geen andere node kan "+vipWord(owned, c)+" overnemen")
	case len(owned) > 0:
		add(s, ImpactDegraded, vipWord(owned, c)+" verhuist naar "+orList(standby))
	case runsHere:
		add(s, ImpactDegraded, "minder reserve: "+or(s.Unit, "het VIP")+" draait nog op "+list(standby))
	}
}

func vipWord(owned []string, c *Cluster) string {
	if len(owned) == 0 {
		owned = make([]string, len(c.VIPs))
		for i, v := range c.VIPs {
			owned[i] = v.Address
		}
	}
	if len(owned) == 1 {
		return "VIP " + owned[0]
	}
	return "de VIP's " + list(owned)
}

// list maakt van ["a", "b", "c"] "a, b en c"; orList "a, b of c".
func list(items []string) string   { return join(items, " en ") }
func orList(items []string) string { return join(items, " of ") }

func join(items []string, last string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + last + items[len(items)-1]
}

var envRank = map[string]int{"prod": 0, "test": 1, "lab": 2}

// SortHits zet prod bovenaan, dan down voor verminderd, dan op groep en naam.
func (g *Graph) SortHits(hits []Hit) {
	key := func(h Hit) (int, int, string, string) {
		s := g.svc[h.Service]
		if s == nil {
			return 9, 9, "", ""
		}
		gr := g.GroupOf(*s)
		env, ok := envRank[gr.Environment]
		if !ok {
			env = 3
		}
		sev := 1
		if h.Impact == ImpactDown {
			sev = 0
		}
		return env, sev, strings.ToLower(gr.Name), strings.ToLower(s.Name)
	}
	slices.SortFunc(hits, func(a, b Hit) int {
		ae, as, ag, an := key(a)
		be, bs, bg, bn := key(b)
		return cmp.Or(cmp.Compare(ae, be), cmp.Compare(as, bs), cmp.Compare(ag, bg), cmp.Compare(an, bn),
			cmp.Compare(a.Service.String(), b.Service.String()))
	})
}
