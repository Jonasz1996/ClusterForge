package deps

import (
	"cmp"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
)

// Group is een vak in de graaf: een cluster, een losse node of Extern.
type Group struct {
	Key         string
	Kind        string
	ClusterID   *uuid.UUID
	NodeID      *uuid.UUID
	Name        string
	Environment string
	Status      status.Status
	Reason      string
}

// ExternalKey is de groep van de externe diensten.
const ExternalKey = "external"

// GroupOf geeft de groep van een dienst.
func (g *Graph) GroupOf(s Svc) Group {
	switch {
	case s.ClusterID != nil:
		gr := Group{Key: "cluster:" + s.ClusterID.String(), Kind: "cluster", ClusterID: s.ClusterID, Name: "verwijderd cluster", Status: status.Unknown}
		if c := g.clusters[*s.ClusterID]; c != nil {
			gr.Name, gr.Environment, gr.Status, gr.Reason = c.Name, c.Environment, c.Status, c.Reason
		}
		return gr
	case s.NodeID != nil:
		gr := Group{Key: "node:" + s.NodeID.String(), Kind: "node", NodeID: s.NodeID, Name: "verwijderde node", Status: status.Unknown}
		if n := g.nodes[*s.NodeID]; n != nil {
			gr.Name, gr.Status, gr.Reason = n.Hostname, n.Status, n.Reason
		}
		return gr
	}
	return Group{Key: ExternalKey, Kind: "external", Name: "Extern", Status: status.Unknown}
}

// Filter kiest wat de graaf toont.
type Filter struct {
	// ClusterID toont dat cluster plus zijn directe buren.
	ClusterID *uuid.UUID
	// Environment toont de clusters van die omgeving plus hun directe buren.
	Environment string
	// Suggested toont ook voorgestelde diensten en pijlen, Ignored ook de
	// genegeerde diensten.
	Suggested bool
	Ignored   bool
}

func (f Filter) shows(state string) bool {
	switch state {
	case StateConfirmed:
		return true
	case StateSuggested:
		return f.Suggested
	case StateIgnored:
		return f.Ignored
	}
	return false
}

// Select geeft de diensten en pijlen die het filter toont. De status is al
// over de hele graaf uitgerekend: uitval van buiten het filter telt mee.
func (g *Graph) Select(f Filter) ([]Svc, []Dep) {
	primary := func(s Svc) bool {
		switch {
		case f.ClusterID != nil:
			return s.ClusterID != nil && *s.ClusterID == *f.ClusterID
		case f.Environment != "":
			return g.GroupOf(s).Environment == f.Environment
		}
		return true
	}
	in := map[uuid.UUID]bool{}
	for _, s := range g.Services {
		if f.shows(s.State) && primary(s) {
			in[s.ID] = true
		}
	}
	edge := func(d Dep) bool {
		return (d.State == StateConfirmed || (f.Suggested && d.State == StateSuggested)) &&
			f.shows(g.svc[d.From].State) && f.shows(g.svc[d.To].State)
	}
	if f.ClusterID != nil || f.Environment != "" {
		buren := map[uuid.UUID]bool{}
		for _, d := range g.Deps {
			if g.svc[d.From] == nil || g.svc[d.To] == nil || !edge(d) {
				continue
			}
			switch {
			case in[d.From] && !in[d.To]:
				buren[d.To] = true
			case in[d.To] && !in[d.From]:
				buren[d.From] = true
			}
		}
		for id := range buren {
			in[id] = true
		}
	}
	var svcs []Svc
	for _, s := range g.Services {
		if in[s.ID] {
			svcs = append(svcs, s)
		}
	}
	var deps []Dep
	for _, d := range g.Deps {
		if in[d.From] && in[d.To] && edge(d) {
			deps = append(deps, d)
		}
	}
	return svcs, deps
}

// GroupEdge zijn de pijlen tussen twee groepen, samengevoegd.
type GroupEdge struct {
	From, To string
	// Strength is hard als minstens één pijl hard is.
	Strength string
	Count    int
	Affected bool
}

// Collapse voegt pijlen tussen groepen samen voor de weergave Alleen
// clusters. Pijlen binnen een groep vallen weg.
func (g *Graph) Collapse(deps []Dep) []GroupEdge {
	idx := map[[2]string]int{}
	var out []GroupEdge
	for _, d := range deps {
		from, to := g.svc[d.From], g.svc[d.To]
		if from == nil || to == nil {
			continue
		}
		k := [2]string{g.GroupOf(*from).Key, g.GroupOf(*to).Key}
		if k[0] == k[1] {
			continue
		}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, GroupEdge{From: k[0], To: k[1], Strength: Soft})
		}
		out[i].Count++
		if d.Strength == Hard {
			out[i].Strength = Hard
		}
		out[i].Affected = out[i].Affected || g.Affected(d)
	}
	slices.SortFunc(out, func(a, b GroupEdge) int { return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To)) })
	return out
}

// GroupImpact is de ergste doorgegeven uitval op de bevestigde diensten van
// een groep, met de groepen waar ze vandaan komt. Uitval binnen de groep zelf
// telt niet: dat zegt de status van de groep al.
func (g *Graph) GroupImpact(key string) (Impact, []string) {
	worst := ImpactNone
	causes := map[string]bool{}
	for _, s := range g.Services {
		if s.State != StateConfirmed || g.GroupOf(s).Key != key {
			continue
		}
		e, ok := g.effects[s.ID]
		if !ok {
			continue
		}
		cause := g.svc[e.Cause]
		if cause == nil || g.GroupOf(*cause).Key == key {
			continue
		}
		causes[g.GroupOf(*cause).Name] = true
		if e.Impact == ImpactDown || worst == ImpactNone {
			worst = e.Impact
		}
	}
	names := make([]string, 0, len(causes))
	for n := range causes {
		names = append(names, n)
	}
	slices.Sort(names)
	return worst, names
}
