package deps

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
)

// Alles in dit bestand is puur: de API leest de wereld uit de database en
// rekent hier de status, het doorgeven en de impact uit, zodat graph_test.go
// het zonder database test.

// Svc is een dienst zoals de graaf hem ziet.
type Svc struct {
	ID        uuid.UUID
	ClusterID *uuid.UUID
	NodeID    *uuid.UUID
	Name      string
	Kind      string
	Unit      string
	Port      int
	Address   string
	Source    string
	State     string
	// Description, LastSeenAt, CreatedAt en UpdatedAt rekent de graaf niet
	// mee; de API toont ze.
	Description string
	LastSeenAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// External is true voor een dienst buiten ClusterForge, met adres en poort.
func (s Svc) External() bool { return s.ClusterID == nil && s.NodeID == nil }

// Dep is een pijl van afnemer (From) naar leverancier (To).
type Dep struct {
	ID       uuid.UUID
	From, To uuid.UUID
	Strength string
	Source   string
	State    string
	Note     string
}

// Node is een node met de units uit zijn laatste heartbeat.
type Node struct {
	ID        uuid.UUID
	Hostname  string
	ClusterID *uuid.UUID
	// Active is lifecycle active; alleen die nodes tellen, zoals in de
	// statusregels.
	Active bool
	// Fresh is true met een agent en een heartbeat van hoogstens
	// status.HeartbeatDown oud.
	Fresh bool
	// Units is de toestand per gevolgde unit; een unit die niet
	// geïnstalleerd is, ontbreekt.
	Units map[string]string
	// Status en Reason komen uit de evaluator, voor het vak van een losse
	// node.
	Status status.Status
	Reason string
}

// Cluster is een cluster met zijn status uit de evaluator.
type Cluster struct {
	ID          uuid.UUID
	Name        string
	Slug        string
	Environment string
	Type        string
	Status      status.Status
	Reason      string
	// VIPs zijn de adressen met hun eigenaar; uuid.Nil zonder eigenaar.
	VIPs []VIP
}

type VIP struct {
	Address string
	Owner   uuid.UUID
}

// World is alles waaruit de graaf rekent.
type World struct {
	Clusters []Cluster
	Nodes    []Node
	Services []Svc
	Deps     []Dep
}

// Instance is een dienst op één node.
type Instance struct {
	NodeID   uuid.UUID
	Hostname string
	// State is de ActiveState van de unit; leeg als de node geen verse
	// heartbeat heeft.
	State   string
	Running bool
}

// Own is de eigen status van een dienst, zonder afhankelijkheden.
type Own struct {
	Status    status.Status
	Reason    string
	Instances []Instance
}

// Impact is wat een afhankelijkheid met een dienst doet.
type Impact string

const (
	ImpactNone     Impact = "none"
	ImpactDegraded Impact = "degraded"
	ImpactDown     Impact = "down"
)

// Effect is de doorgegeven uitval op een dienst.
type Effect struct {
	Impact Impact
	// Cause is de dienst waar de uitval begint.
	Cause uuid.UUID
	// Via is de leverancier waar deze dienst rechtstreeks van afhangt, Edge
	// die pijl.
	Via  uuid.UUID
	Edge uuid.UUID
	// Path loopt van de oorzaak naar deze dienst, beide inbegrepen.
	Path []uuid.UUID
}

// Graph is een wereld met zijn uitgerekende status.
type Graph struct {
	World
	svc       map[uuid.UUID]*Svc
	clusters  map[uuid.UUID]*Cluster
	nodes     map[uuid.UUID]*Node
	consumers map[uuid.UUID][]Dep
	providers map[uuid.UUID][]Dep
	own       map[uuid.UUID]Own
	effects   map[uuid.UUID]Effect
}

// Build rekent de eigen status van elke dienst uit en geeft de uitval door.
func Build(w World) *Graph {
	g := &Graph{
		World: w, svc: map[uuid.UUID]*Svc{}, clusters: map[uuid.UUID]*Cluster{}, nodes: map[uuid.UUID]*Node{},
		consumers: map[uuid.UUID][]Dep{}, providers: map[uuid.UUID][]Dep{}, own: map[uuid.UUID]Own{},
	}
	for i := range g.Services {
		g.svc[g.Services[i].ID] = &g.Services[i]
	}
	for i := range g.Clusters {
		g.clusters[g.Clusters[i].ID] = &g.Clusters[i]
	}
	for i := range g.Nodes {
		g.nodes[g.Nodes[i].ID] = &g.Nodes[i]
	}
	for _, d := range g.Deps {
		if g.svc[d.From] == nil || g.svc[d.To] == nil {
			continue
		}
		g.consumers[d.To] = append(g.consumers[d.To], d)
		g.providers[d.From] = append(g.providers[d.From], d)
	}
	// Vaste volgorde, zodat paden en oorzaken niet per aanroep verschillen.
	byName := func(id func(Dep) uuid.UUID) func(a, b Dep) int {
		return func(a, b Dep) int { return g.compareSvc(id(a), id(b)) }
	}
	for k := range g.consumers {
		slices.SortFunc(g.consumers[k], byName(func(d Dep) uuid.UUID { return d.From }))
	}
	for k := range g.providers {
		slices.SortFunc(g.providers[k], byName(func(d Dep) uuid.UUID { return d.To }))
	}
	for _, s := range g.Services {
		g.own[s.ID] = g.ownStatus(s)
	}
	var roots []uuid.UUID
	for _, s := range g.Services {
		if s.State == StateConfirmed && g.own[s.ID].Status == status.Down {
			roots = append(roots, s.ID)
		}
	}
	g.effects = g.propagate(roots)
	return g
}

// Service geeft een dienst.
func (g *Graph) Service(id uuid.UUID) (Svc, bool) {
	s, ok := g.svc[id]
	if !ok {
		return Svc{}, false
	}
	return *s, true
}

// Cluster geeft een cluster.
func (g *Graph) Cluster(id uuid.UUID) (Cluster, bool) {
	c, ok := g.clusters[id]
	if !ok {
		return Cluster{}, false
	}
	return *c, true
}

// Node geeft een node.
func (g *Graph) Node(id uuid.UUID) (Node, bool) {
	n, ok := g.nodes[id]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// Own geeft de eigen status van een dienst.
func (g *Graph) Own(id uuid.UUID) Own { return g.own[id] }

// Effect geeft de doorgegeven uitval op een dienst; Impact is none zonder.
func (g *Graph) Effect(id uuid.UUID) Effect {
	if e, ok := g.effects[id]; ok {
		return e
	}
	return Effect{Impact: ImpactNone}
}

// ImpactText zegt waar de doorgegeven uitval op een dienst vandaan komt,
// zoals "mariadb in db-prod is down, via php8.2-fpm", met het pad van de
// oorzaak naar de dienst in namen zoals de dienst ze ziet. Zonder uitval is
// alles leeg.
func (g *Graph) ImpactText(id uuid.UUID) (string, []string) {
	e := g.Effect(id)
	if e.Impact == ImpactNone {
		return "", []string{}
	}
	path := make([]string, 0, len(e.Path))
	for _, p := range e.Path {
		path = append(path, g.labelFrom(id, p))
	}
	reason := g.labelFrom(id, e.Cause) + " is down"
	if len(path) > 2 {
		reason += ", via " + strings.Join(path[1:len(path)-1], " en ")
	}
	return reason, path
}

// Providers zijn de pijlen waarmee een dienst van andere afhangt, en
// Consumers de pijlen van de diensten die van hem afhangen.
func (g *Graph) Providers(id uuid.UUID) []Dep { return g.providers[id] }
func (g *Graph) Consumers(id uuid.UUID) []Dep { return g.consumers[id] }

// Affected is true voor de pijl waarlangs uitval een dienst bereikte: die
// kleurt in de graaf.
func (g *Graph) Affected(d Dep) bool {
	e, ok := g.effects[d.From]
	return ok && e.Edge == d.ID
}

func running(state string) bool { return state == "active" || state == "reloading" }

// ownStatus is de status van een dienst uit de heartbeats, zonder
// afhankelijkheden.
func (g *Graph) ownStatus(s Svc) Own {
	if s.External() {
		return Own{Status: status.Unknown, Reason: "extern: ClusterForge bewaakt deze dienst niet"}
	}
	var c *Cluster
	if s.ClusterID != nil {
		c = g.clusters[*s.ClusterID]
	}
	scope := g.scope(s)
	if s.Kind == "vip" && c != nil && len(c.VIPs) > 0 {
		var orphans []string
		for _, v := range c.VIPs {
			if v.Owner == uuid.Nil {
				orphans = append(orphans, "VIP "+v.Address+" heeft geen eigenaar")
			}
		}
		switch {
		case c.Status == status.Unknown:
			// Een cluster in uitrol of zonder agents lijkt niet down.
			return Own{Status: status.Unknown, Reason: or(c.Reason, "de status van het cluster is onbekend")}
		case c.Status == status.SplitBrain:
			return Own{Status: status.Down, Reason: "split-brain: " + c.Reason, Instances: g.instances(s, scope)}
		case len(orphans) > 0:
			return Own{Status: status.Down, Reason: strings.Join(orphans, "; "), Instances: g.instances(s, scope)}
		case s.Unit == "":
			return Own{Status: status.Healthy}
		}
	}
	if s.Unit == "" {
		return Own{Status: status.Unknown, Reason: "geen unit om te volgen"}
	}
	inst := g.instances(s, scope)
	if len(inst) == 0 {
		return Own{Status: status.Unknown, Reason: "geen actieve node meldt " + s.Unit, Instances: inst}
	}
	var up int
	var notUp []string
	for _, i := range inst {
		if i.Running {
			up++
			continue
		}
		notUp = append(notUp, i.Hostname+": "+or(i.State, "geen heartbeat"))
	}
	switch {
	case up == len(inst):
		return Own{Status: status.Healthy, Instances: inst}
	case up == 0:
		return Own{Status: status.Down, Reason: s.Unit + " draait op geen enkele node (" + strings.Join(notUp, ", ") + ")", Instances: inst}
	}
	return Own{Status: status.Degraded, Reason: fmt.Sprintf("%s draait op %d van %d nodes (%s)", s.Unit, up, len(inst), strings.Join(notUp, ", ")), Instances: inst}
}

// scope zijn de actieve nodes waarop een dienst kan draaien.
func (g *Graph) scope(s Svc) []*Node {
	var out []*Node
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if !n.Active {
			continue
		}
		switch {
		case s.ClusterID != nil && n.ClusterID != nil && *n.ClusterID == *s.ClusterID:
		case s.NodeID != nil && n.ID == *s.NodeID:
		default:
			continue
		}
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b *Node) int { return cmp.Compare(a.Hostname, b.Hostname) })
	return out
}

// instances zijn de nodes waarop de unit van de dienst in de heartbeat staat.
func (g *Graph) instances(s Svc, scope []*Node) []Instance {
	out := []Instance{}
	if s.Unit == "" {
		return out
	}
	for _, n := range scope {
		st, ok := n.Units[s.Unit]
		if !ok {
			continue
		}
		if !n.Fresh {
			st = ""
		}
		out = append(out, Instance{NodeID: n.ID, Hostname: n.Hostname, State: st, Running: n.Fresh && running(st)})
	}
	return out
}

// counts is true als een dienst uitval doorgeeft: alleen bevestigde
// diensten. Voorstellen en genegeerde rijen geven niets door.
func (g *Graph) counts(id uuid.UUID) bool {
	s := g.svc[id]
	return s != nil && s.State == StateConfirmed
}

// propagate zoekt omgekeerd langs de bevestigde pijlen vanaf diensten die
// down zijn. Een harde pijl maakt de afnemer down en gaat verder, een zachte
// maakt hem verminderd en stopt. Elke dienst komt één keer aan de beurt, dus
// cycli zijn geen probleem, en een wortel krijgt geen tweede markering.
func (g *Graph) propagate(roots []uuid.UUID) map[uuid.UUID]Effect {
	roots = slices.Clone(roots)
	slices.SortFunc(roots, g.compareSvc)
	out := map[uuid.UUID]Effect{}
	down := map[uuid.UUID]bool{}
	path := map[uuid.UUID][]uuid.UUID{}
	queue := []uuid.UUID{}
	for _, r := range roots {
		if down[r] {
			continue
		}
		down[r] = true
		path[r] = []uuid.UUID{r}
		queue = append(queue, r)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, d := range g.consumers[id] {
			c := d.From
			if d.State != StateConfirmed || !g.counts(c) || down[c] {
				continue
			}
			p := append(slices.Clone(path[id]), c)
			e := Effect{Cause: path[id][0], Via: id, Edge: d.ID, Path: p}
			if d.Strength == Hard {
				e.Impact = ImpactDown
				out[c] = e
				down[c] = true
				path[c] = p
				queue = append(queue, c)
				continue
			}
			if _, ok := out[c]; !ok {
				e.Impact = ImpactDegraded
				out[c] = e
			}
		}
	}
	return out
}

// compareSvc ordent diensten op naam, dan op id.
func (g *Graph) compareSvc(a, b uuid.UUID) int {
	sa, sb := g.svc[a], g.svc[b]
	if sa != nil && sb != nil {
		if c := cmp.Compare(strings.ToLower(sa.Name), strings.ToLower(sb.Name)); c != 0 {
			return c
		}
	}
	return cmp.Compare(a.String(), b.String())
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
