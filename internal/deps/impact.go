package deps

import (
	"cmp"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Target is wat er denkbeeldig uitvalt: precies één dienst, cluster of node.
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

// Impact rekent uit wat er geraakt wordt als het doel uitvalt. Het is
// dezelfde zoektocht als bij een echte uitval. Bij een node telt een tweede
// gezonde instantie mee, net als de overname van het VIP. Het doel zelf staat
// er niet in; bij een cluster of node staan zijn eigen diensten erin als
// Direct.
func (g *Graph) Impact(t Target) []Hit {
	hits := map[uuid.UUID]Hit{}
	var roots []uuid.UUID
	switch {
	case t.Service != nil:
		if g.svc[*t.Service] != nil {
			roots = append(roots, *t.Service)
		}
	case t.Cluster != nil:
		for _, s := range g.Services {
			if s.State == StateConfirmed && s.ClusterID != nil && *s.ClusterID == *t.Cluster {
				hits[s.ID] = Hit{Service: s.ID, Impact: ImpactDown, Direct: true, Reason: "valt uit met het cluster"}
				roots = append(roots, s.ID)
			}
		}
	case t.Node != nil:
		for _, h := range g.nodeOutage(*t.Node) {
			hits[h.Service] = h
			if h.Impact == ImpactDown {
				roots = append(roots, h.Service)
			}
		}
	}
	for id, e := range g.propagate(roots) {
		if t.Service != nil && id == *t.Service {
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

// nodeOutage zegt wat de uitval van één node rechtstreeks doet met de
// bevestigde diensten van zijn cluster, of met die op de node zelf.
func (g *Graph) nodeOutage(id uuid.UUID) []Hit {
	n := g.nodes[id]
	if n == nil {
		return nil
	}
	var others []*Node
	var cluster *Cluster
	if n.ClusterID != nil {
		cluster = g.clusters[*n.ClusterID]
		for i := range g.Nodes {
			o := &g.Nodes[i]
			if o.ID != id && o.Active && o.ClusterID != nil && *o.ClusterID == *n.ClusterID {
				others = append(others, o)
			}
		}
		slices.SortFunc(others, func(a, b *Node) int { return cmp.Compare(a.Hostname, b.Hostname) })
	}
	var out []Hit
	add := func(s Svc, impact Impact, reason string) {
		out = append(out, Hit{Service: s.ID, Impact: impact, Direct: true, Reason: reason})
	}
	for _, s := range g.Services {
		if s.State != StateConfirmed {
			continue
		}
		switch {
		case s.NodeID != nil && *s.NodeID == id:
			add(s, ImpactDown, "draait op "+n.Hostname)
		case cluster != nil && s.ClusterID != nil && *s.ClusterID == cluster.ID:
			if s.Kind == "vip" && len(cluster.VIPs) > 0 {
				g.vipOutage(s, n, cluster, others, add)
				continue
			}
			if s.Unit == "" {
				if len(others) == 0 {
					add(s, ImpactDown, n.Hostname+" is de enige actieve node van "+cluster.Name)
				}
				continue
			}
			if _, ok := n.Units[s.Unit]; !ok {
				continue
			}
			var elsewhere []string
			for _, o := range others {
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
	}
	return out
}

// vipOutage: neemt een andere node het VIP over, dan is de dienst alleen
// verminderd.
func (g *Graph) vipOutage(s Svc, n *Node, c *Cluster, others []*Node, add func(Svc, Impact, string)) {
	var standby []string
	for _, o := range others {
		if o.Fresh && (s.Unit == "" || running(o.Units[s.Unit])) {
			standby = append(standby, o.Hostname)
		}
	}
	var owned []string
	for _, v := range c.VIPs {
		if v.Owner == n.ID {
			owned = append(owned, v.Address)
		}
	}
	_, runsHere := n.Units[s.Unit]
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
