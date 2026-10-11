package risk

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/topo"
)

// De soorten onderdelen die in de wat-als kunnen uitvallen.
const (
	KindHost    = "host"
	KindStorage = "storage"
	KindBridge  = "bridge"
	KindNode    = "node"
	KindService = "service"
	KindCluster = "cluster"
)

// kindOrder is de volgorde waarin de soorten getoond worden.
var kindOrder = []string{KindHost, KindStorage, KindBridge, KindCluster, KindNode, KindService}

// Component is een onderdeel dat in de wat-als kan uitvallen. Key herkent
// het, zoals "host:<koppeling>:pve1".
type Component struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Group string `json:"group"`

	conn   uuid.UUID
	host   string
	name   string
	shared bool
	id     uuid.UUID
}

// componentID is een vaste uuid voor een onderdeel, voor een acceptatie van
// een SPOF.
func (c Component) componentID() uuid.UUID { return uuid.NewSHA1(spofNamespace, []byte(c.Key)) }

var spofNamespace = uuid.MustParse("7d0e6f0c-3c1b-4c55-9a52-5f0c1e2a7b11")

// WhatIfWorld is alles waaruit de wat-als rekent.
type WhatIfWorld struct {
	Topo  topo.Model
	Graph *deps.Graph
	// Candidates zijn per cluster en VIP-adres de nodes die het adres in
	// hun keepalived-configuratie hebben.
	Candidates map[uuid.UUID]map[string][]uuid.UUID
}

// Components geeft alle onderdelen die kunnen uitvallen, in de volgorde
// waarin ze getoond worden.
func (w WhatIfWorld) Components() []Component {
	var out []Component
	m := w.Topo
	for _, h := range m.Hosts {
		out = append(out, Component{Key: hostKey(h.Conn, h.Name), Kind: KindHost, Name: h.Name, Group: m.ConnName(h.Conn), conn: h.Conn, host: h.Name})
	}
	seen := map[string]bool{}
	for _, s := range m.Storages {
		c := Component{Kind: KindStorage, Group: m.ConnName(s.Conn), conn: s.Conn, name: s.Name, shared: m.Shared(s.Conn, s.Name)}
		if c.shared {
			c.Key, c.Name = topo.StorageKey(s.Conn, s.Name, "", true), s.Name+" (gedeeld)"
		} else {
			c.Key, c.Name, c.host = topo.StorageKey(s.Conn, s.Name, s.Host, false), s.Name+" op "+s.Host, s.Host
		}
		if !seen[c.Key] {
			seen[c.Key] = true
			out = append(out, c)
		}
	}
	for _, b := range m.Bridges() {
		out = append(out, Component{
			Key: topo.BridgeKey(b.Conn, b.Host, b.Name), Kind: KindBridge, Name: b.Name + " op " + b.Host,
			Group: m.ConnName(b.Conn), conn: b.Conn, host: b.Host, name: b.Name,
		})
	}
	if g := w.Graph; g != nil {
		for _, c := range g.Clusters {
			out = append(out, Component{Key: topo.ClusterKey(c.ID), Kind: KindCluster, Name: c.Name, Group: c.Environment, id: c.ID})
		}
		for _, n := range g.Nodes {
			group := "zonder cluster"
			if n.ClusterID != nil {
				if c, ok := g.Cluster(*n.ClusterID); ok {
					group = c.Name
				}
			}
			out = append(out, Component{Key: topo.NodeKey(n.ID), Kind: KindNode, Name: n.Hostname, Group: group, id: n.ID})
		}
		for _, s := range g.Services {
			if s.State != deps.StateConfirmed || s.External() {
				continue
			}
			out = append(out, Component{Key: topo.ServiceKey(s.ID), Kind: KindService, Name: s.Name, Group: g.GroupOf(s).Name, id: s.ID})
		}
	}
	slices.SortStableFunc(out, func(a, b Component) int {
		return cmp.Or(cmp.Compare(slices.Index(kindOrder, a.Kind), slices.Index(kindOrder, b.Kind)),
			cmp.Compare(strings.ToLower(a.Group), strings.ToLower(b.Group)), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return out
}

func hostKey(conn uuid.UUID, host string) string { return topo.HostKey(conn, host) }

// UnknownComponentError noemt een onderdeel dat niet bestaat.
type UnknownComponentError struct{ Key string }

func (e UnknownComponentError) Error() string { return "onbekend onderdeel: " + e.Key }

// GuestOutcome is wat er met een VM of container gebeurt.
type GuestOutcome struct {
	// Key is de sleutel van de gast op de kaart.
	Key        string     `json:"key"`
	Connection string     `json:"connection"`
	VMID       int        `json:"vmid"`
	Name       string     `json:"name"`
	Host       string     `json:"host"`
	NodeID     *uuid.UUID `json:"node_id"`
	Hostname   string     `json:"hostname"`
	// Outcome is down, restarted (HA start hem elders) of partial (een
	// netwerkkaart valt weg, de VM draait door).
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	// RestartHost is waar HA hem start.
	RestartHost string `json:"restart_host"`
	// Unknown is true voor een gast zonder node in ClusterForge.
	Unknown bool `json:"unknown"`

	conn uuid.UUID
}

// NodeOutcome is een node die uitvalt.
type NodeOutcome struct {
	ID       uuid.UUID `json:"id"`
	Hostname string    `json:"hostname"`
	Outcome  string    `json:"outcome"`
}

// Prediction is een voorspelde status met de reden.
type Prediction struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// ClusterOutcome is de voorspelde status van een cluster. AfterHA is er
// alleen als HA in dit cluster iets herstart.
type ClusterOutcome struct {
	ID        uuid.UUID   `json:"id"`
	Name      string      `json:"name"`
	Env       string      `json:"environment"`
	Before    Prediction  `json:"before"`
	During    Prediction  `json:"during"`
	AfterHA   *Prediction `json:"after_ha"`
	NodesDown []string    `json:"nodes_down"`
}

// Final is de status die blijft: na de herstart door HA.
func (c ClusterOutcome) Final() Prediction {
	if c.AfterHA != nil {
		return *c.AfterHA
	}
	return c.During
}

// ServiceOutcome is een dienst die geraakt wordt.
type ServiceOutcome struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Group  string    `json:"group"`
	Env    string    `json:"environment"`
	Impact string    `json:"impact"`
	Direct bool      `json:"direct"`
	Reason string    `json:"reason"`
	// Recovers is true als de dienst terugkomt zodra HA de VM's herstart
	// heeft.
	Recovers bool `json:"recovers"`
}

// SelfOutcome is wat er met ClusterForge zelf gebeurt.
type SelfOutcome struct {
	Name        string `json:"name"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason"`
	RestartHost string `json:"restart_host"`
}

// WhatIf is de uitkomst van de wat-als.
type WhatIf struct {
	Components []Component    `json:"components"`
	Guests     []GuestOutcome `json:"guests"`
	// Nodes zijn de nodes die uitvallen: down blijft weg, restarted komt
	// terug als HA zijn VM herstart.
	Nodes    []NodeOutcome    `json:"nodes"`
	Clusters []ClusterOutcome `json:"clusters"`
	Services []ServiceOutcome `json:"services"`
	Self     *SelfOutcome     `json:"self"`
	Notes    []string         `json:"notes"`
}

// Resolve zoekt de onderdelen bij hun sleutels.
func (w WhatIfWorld) Resolve(keys []string) ([]Component, error) {
	all := map[string]Component{}
	for _, c := range w.Components() {
		all[c.Key] = c
	}
	var out []Component
	seen := map[string]bool{}
	for _, k := range keys {
		c, ok := all[k]
		if !ok {
			return nil, UnknownComponentError{k}
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// Run rekent uit wat er gebeurt als de onderdelen tegelijk uitvallen, met
// de toestand van nu. Met baseline rekent hij alsof alles nu gezond is: zo
// rekent de SPOF-lijst.
func (w WhatIfWorld) Run(comps []Component, baseline bool) WhatIf {
	g := w.Graph
	if baseline && g != nil {
		g = healthyGraph(g)
	}
	return w.run(comps, baseline, g)
}

// run rekent met g, die bij baseline al gezond gemaakt is.
func (w WhatIfWorld) run(comps []Component, baseline bool, g *deps.Graph) WhatIf {
	out := WhatIf{Components: comps, Guests: []GuestOutcome{}, Nodes: []NodeOutcome{}, Clusters: []ClusterOutcome{}, Services: []ServiceOutcome{}, Notes: []string{}}
	m := w.Topo
	hosts := map[string]bool{}
	type stKey struct {
		conn       uuid.UUID
		name, host string
	}
	storages := map[stKey]bool{}
	bridges := map[stKey]bool{}
	var outage deps.Outage
	for _, c := range comps {
		switch c.Kind {
		case KindHost:
			hosts[c.conn.String()+"/"+c.host] = true
		case KindStorage:
			storages[stKey{c.conn, c.name, c.host}] = true
		case KindBridge:
			bridges[stKey{c.conn, c.name, c.host}] = true
		case KindNode:
			outage.Nodes = append(outage.Nodes, c.id)
		case KindService:
			outage.Services = append(outage.Services, c.id)
		case KindCluster:
			outage.Clusters = append(outage.Clusters, c.id)
		}
	}
	hostDown := func(conn uuid.UUID, h string) bool { return hosts[conn.String()+"/"+h] }
	// touches is true als een gekozen storage of bridge bij de gast kan
	// horen; zonder zijn configuratie is dat niet zeker.
	touches := func(gu topo.Guest, storages, bridges map[stKey]bool) bool {
		for k := range storages {
			if k.conn == gu.Conn && (k.host == "" || k.host == gu.Host) {
				return true
			}
		}
		for k := range bridges {
			if k.conn == gu.Conn && k.host == gu.Host {
				return true
			}
		}
		return false
	}

	// 1. Welke gasten vallen uit, en welke herstart HA.
	active := map[uuid.UUID]bool{}
	if g != nil {
		for _, n := range g.Nodes {
			active[n.ID] = n.Active
		}
	}
	var unread []string
	var restart []int
	for _, gu := range m.Guests {
		if gu.Template || gu.Pool == proxmox.SandboxPool {
			continue
		}
		running := gu.Status == "running" || (baseline && gu.NodeID != nil && active[*gu.NodeID])
		if !running {
			continue
		}
		var reasons []string
		onHost := hostDown(gu.Conn, gu.Host)
		if onHost {
			reasons = append(reasons, "draait op "+gu.Host)
		}
		partial := ""
		if cfg := gu.Config; cfg != nil {
			for _, d := range cfg.Disks {
				if d.Storage == "" {
					continue
				}
				if storages[stKey{gu.Conn, d.Storage, ""}] || storages[stKey{gu.Conn, d.Storage, gu.Host}] {
					reasons = append(reasons, "schijf "+d.Key+" staat op "+d.Storage)
				}
			}
			var nics, lost []proxmox.NIC
			for _, n := range cfg.NICs {
				if n.LinkDown {
					continue
				}
				nics = append(nics, n)
				if bridges[stKey{gu.Conn, n.Bridge, gu.Host}] {
					lost = append(lost, n)
				}
			}
			switch {
			case len(lost) == 0:
			case len(lost) == len(nics) || lost[0].Key == nics[0].Key:
				reasons = append(reasons, nicText(lost))
			default:
				partial = nicText(lost)
			}
		} else if touches(gu, storages, bridges) {
			unread = append(unread, gu.Label())
		}
		if len(reasons) == 0 && partial == "" {
			continue
		}
		o := GuestOutcome{
			Key: topo.GuestKey(gu.Conn, gu.VMID), Connection: m.ConnName(gu.Conn), VMID: gu.VMID, Name: gu.Name, Host: gu.Host, NodeID: gu.NodeID, Hostname: gu.Hostname,
			Outcome: "down", Reason: strings.Join(reasons, "; "), Unknown: gu.NodeID == nil, conn: gu.Conn,
		}
		if len(reasons) == 0 {
			o.Outcome, o.Reason = "partial", partial
		}
		if o.Outcome == "down" && onHost && len(reasons) == 1 && gu.HA() {
			restart = append(restart, len(out.Guests))
		}
		out.Guests = append(out.Guests, o)
	}
	w.placeHA(&out, restart, hostDown)

	// 2. Welke nodes vallen uit, eerst en na de herstart door HA.
	during, after := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	for _, o := range out.Guests {
		if o.NodeID == nil || o.Outcome == "partial" {
			continue
		}
		during[*o.NodeID] = o.Reason
		if o.Outcome == "down" {
			after[*o.NodeID] = o.Reason
		}
	}
	if g != nil {
		for _, id := range outage.Nodes {
			during[id], after[id] = "gekozen", "gekozen"
		}
		for _, c := range outage.Clusters {
			for _, n := range g.Nodes {
				if n.ClusterID != nil && *n.ClusterID == c {
					during[n.ID], after[n.ID] = "het cluster valt uit", "het cluster valt uit"
				}
			}
		}
		for id := range during {
			n, ok := g.Node(id)
			if !ok {
				continue
			}
			o := NodeOutcome{ID: id, Hostname: n.Hostname, Outcome: "restarted"}
			if _, gone := after[id]; gone {
				o.Outcome = "down"
			}
			out.Nodes = append(out.Nodes, o)
		}
		slices.SortFunc(out.Nodes, func(a, b NodeOutcome) int {
			return cmp.Or(cmp.Compare(a.Hostname, b.Hostname), cmp.Compare(a.ID.String(), b.ID.String()))
		})
		out.Clusters = w.predictClusters(g, during, after, baseline)
		out.Services = servicesHit(g, outage, during, after)
	}

	// 3. ClusterForge zelf.
	if s := m.Self; s.Found() {
		for _, o := range out.Guests {
			if o.conn == s.Conn && o.VMID == s.VMID && o.Outcome != "partial" {
				gu, _ := m.Guest(s.Conn, s.VMID)
				out.Self = &SelfOutcome{Name: gu.Label(), Outcome: o.Outcome, Reason: o.Reason, RestartHost: o.RestartHost}
			}
		}
	}

	// 4. Wat de voorspelling onzeker maakt.
	if len(unread) > 0 {
		slices.Sort(unread)
		them := "hem"
		if len(unread) > 1 {
			them = "ze"
		}
		out.Notes = append(out.Notes, fmt.Sprintf("Van %s is de configuratie nog niet gelezen: waar de schijven en netwerkkaarten zitten "+
			"en of HA %s herstart, is onbekend.", list(unread), them))
	}
	for _, c := range m.Connections {
		if !c.Read {
			out.Notes = append(out.Notes, "Proxmox-koppeling "+c.Name+" is bij de laatste sync niet gelezen; de VM's komen van daarvoor.")
		}
	}
	if len(restart) > 0 {
		out.Notes = append(out.Notes, "HA-groepen en voorkeurshosts zijn niet bekend: HA kan een VM ook op een andere host starten dan hier staat.")
	}
	slices.SortStableFunc(out.Guests, func(a, b GuestOutcome) int {
		return cmp.Or(cmp.Compare(outcomeRank[a.Outcome], outcomeRank[b.Outcome]), cmp.Compare(a.Host, b.Host), cmp.Compare(a.VMID, b.VMID))
	})
	return out
}

var outcomeRank = map[string]int{"down": 0, "restarted": 1, "partial": 2}

func nicText(lost []proxmox.NIC) string {
	parts := make([]string, len(lost))
	for i, n := range lost {
		parts[i] = n.Key + " hangt aan " + n.Bridge
	}
	return strings.Join(parts, ", ")
}

// placeHA zoekt voor elke VM die HA beheert een host die niet uitvalt, met
// genoeg vrij geheugen. Dat kan alleen als al zijn schijven op gedeelde
// storage staan. De grootste VM's eerst, elk naar de host met het meeste
// vrije geheugen.
func (w WhatIfWorld) placeHA(out *WhatIf, idx []int, hostDown func(uuid.UUID, string) bool) {
	m := w.Topo
	free := map[string]int64{}
	for _, h := range m.Hosts {
		if h.Online && !hostDown(h.Conn, h.Name) {
			free[h.Conn.String()+"/"+h.Name] = h.MaxMem - h.Mem
		}
	}
	type cand struct {
		i   int
		g   topo.Guest
		mem int64
	}
	var cands []cand
	for _, i := range idx {
		o := out.Guests[i]
		if gu, ok := m.Guest(o.conn, o.VMID); ok {
			cands = append(cands, cand{i, gu, gu.MaxMem})
		}
	}
	slices.SortStableFunc(cands, func(a, b cand) int { return cmp.Compare(b.mem, a.mem) })
	for _, c := range cands {
		o := &out.Guests[c.i]
		if c.g.Config == nil {
			o.Reason += "; HA herstart hem misschien, maar zijn configuratie is nog niet gelezen"
			continue
		}
		var local []string
		for _, d := range c.g.Config.Disks {
			if d.Storage == "" || !m.Shared(c.g.Conn, d.Storage) {
				local = append(local, d.Key)
			}
		}
		if len(local) > 0 {
			verb := "staat"
			if len(local) > 1 {
				verb = "staan"
			}
			o.Reason += "; HA kan hem niet herstarten: " + list(local) + " " + verb + " niet op gedeelde storage"
			continue
		}
		best, bestFree := "", int64(-1)
		for _, h := range m.Hosts {
			k := h.Conn.String() + "/" + h.Name
			f, ok := free[k]
			if !ok || h.Conn != c.g.Conn || f < c.mem {
				continue
			}
			if f > bestFree || (f == bestFree && h.Name < best) {
				best, bestFree = h.Name, f
			}
		}
		if best == "" {
			o.Reason += "; HA kan hem niet herstarten: geen andere host heeft genoeg vrij geheugen"
			continue
		}
		free[c.g.Conn.String()+"/"+best] -= c.mem
		o.Outcome, o.RestartHost = "restarted", best
	}
}

// healthyGraph is de graaf alsof alles gezond is: elke actieve node verse
// heartbeats met al zijn units actief, en elk VIP op zijn eigenaar.
func healthyGraph(g *deps.Graph) *deps.Graph {
	w := deps.World{Services: g.Services, Deps: g.Deps}
	for _, n := range g.Nodes {
		units := make(map[string]string, len(n.Units))
		for u := range n.Units {
			units[u] = "active"
		}
		n.Units, n.Fresh = units, n.Active
		if n.Active {
			n.Status, n.Reason = status.Healthy, ""
		}
		w.Nodes = append(w.Nodes, n)
	}
	for _, c := range g.Clusters {
		c.Status, c.Reason = status.Healthy, ""
		c.VIPs = slices.Clone(c.VIPs)
		w.Clusters = append(w.Clusters, c)
	}
	return deps.Build(w)
}

// predictClusters past de statusregels toe op elk cluster met een node die
// uitvalt: eerst, en na de herstart door HA als die iets verandert.
func (w WhatIfWorld) predictClusters(g *deps.Graph, during, after map[uuid.UUID]string, baseline bool) []ClusterOutcome {
	var out []ClusterOutcome
	for _, c := range g.Clusters {
		var hit []string
		ha := false
		for _, n := range g.Nodes {
			if n.ClusterID == nil || *n.ClusterID != c.ID {
				continue
			}
			if _, ok := during[n.ID]; ok {
				hit = append(hit, n.Hostname)
				if _, still := after[n.ID]; !still {
					ha = true
				}
			}
		}
		if len(hit) == 0 {
			continue
		}
		slices.Sort(hit)
		o := ClusterOutcome{
			ID: c.ID, Name: c.Name, Env: c.Environment, NodesDown: hit,
			Before: w.predict(g, c, nil, baseline), During: w.predict(g, c, during, baseline),
		}
		if ha {
			p := w.predict(g, c, after, baseline)
			o.AfterHA = &p
		}
		out = append(out, o)
	}
	slices.SortStableFunc(out, func(a, b ClusterOutcome) int {
		return cmp.Or(cmp.Compare(envOrder(a.Env), envOrder(b.Env)), cmp.Compare(statusRank(a.Final().Status), statusRank(b.Final().Status)),
			cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return out
}

func envOrder(env string) int {
	switch env {
	case "prod":
		return 0
	case "test":
		return 1
	case "lab":
		return 2
	}
	return 3
}

func statusRank(s string) int {
	return slices.Index([]string{"down", "split_brain", "degraded", "unknown", "healthy"}, s)
}

// predict past status.ClusterWith toe met de uitgevallen nodes down. Een VIP
// waarvan de eigenaar uitvalt, gaat naar een node die het in zijn
// keepalived-configuratie heeft, of zonder die gegevens naar een andere
// actieve node.
func (w WhatIfWorld) predict(g *deps.Graph, c deps.Cluster, failed map[uuid.UUID]string, baseline bool) Prediction {
	var nodes []status.ClusterNode
	up := map[uuid.UUID]bool{}
	var members []deps.Node
	for _, n := range g.Nodes {
		if n.ClusterID == nil || *n.ClusterID != c.ID {
			continue
		}
		members = append(members, n)
		cn := status.ClusterNode{ID: n.ID, Hostname: n.Hostname, Counts: n.Active, Result: status.Result{Status: n.Status, Reason: n.Reason}}
		if baseline && n.Active {
			cn.Result = status.Result{Status: status.Healthy}
		}
		if why, ok := failed[n.ID]; ok {
			cn.Result = status.Result{Status: status.Down, Reason: "valt uit: " + why}
		}
		if n.Active && (cn.Result.Status == status.Healthy || cn.Result.Status == status.Degraded) {
			up[n.ID] = true
		}
		nodes = append(nodes, cn)
	}
	slices.SortFunc(members, func(a, b deps.Node) int { return cmp.Compare(a.Hostname, b.Hostname) })
	var vips []status.ClusterVIP
	holders := map[uuid.UUID][]uuid.UUID{}
	for _, v := range c.VIPs {
		id := uuid.NewSHA1(c.ID, []byte(v.Address))
		vips = append(vips, status.ClusterVIP{ID: id, Address: v.Address})
		owner := v.Owner
		if baseline && owner == uuid.Nil {
			owner = w.firstCandidate(c.ID, v.Address, members, nil)
		}
		switch {
		case owner == uuid.Nil:
			holders[id] = []uuid.UUID{}
		case up[owner] || (!baseline && failed[owner] == ""):
			holders[id] = []uuid.UUID{owner}
		default:
			if next := w.firstCandidate(c.ID, v.Address, members, up); next != uuid.Nil {
				holders[id] = []uuid.UUID{next}
			} else {
				holders[id] = []uuid.UUID{}
			}
		}
	}
	r := status.ClusterWith(nodes, vips, holders)
	return Prediction{Status: string(r.Status), Reason: r.Reason}
}

// firstCandidate is de eerste node, op hostnaam, die het VIP kan overnemen.
// Met up alleen nodes die nu draaien.
func (w WhatIfWorld) firstCandidate(cluster uuid.UUID, addr string, members []deps.Node, up map[uuid.UUID]bool) uuid.UUID {
	cands := w.Candidates[cluster][addr]
	for _, n := range members {
		if !n.Active || (up != nil && !up[n.ID]) {
			continue
		}
		if len(cands) == 0 || slices.Contains(cands, n.ID) {
			return n.ID
		}
	}
	return uuid.Nil
}

// servicesHit zijn de diensten die geraakt worden: met de nodes die eerst
// uitvallen, en Recovers als ze na de herstart door HA weer werken.
func servicesHit(g *deps.Graph, o deps.Outage, during, after map[uuid.UUID]string) []ServiceOutcome {
	first := o
	first.Nodes = keys(during)
	hits := g.ImpactOf(first)
	later := map[uuid.UUID]deps.Impact{}
	if len(after) != len(during) {
		last := o
		last.Nodes = keys(after)
		for _, h := range g.ImpactOf(last) {
			later[h.Service] = h.Impact
		}
	}
	out := []ServiceOutcome{}
	for _, h := range hits {
		s, _ := g.Service(h.Service)
		so := ServiceOutcome{
			ID: s.ID, Name: s.Name, Group: g.GroupOf(s).Name, Env: serviceEnv(g, s), Impact: string(h.Impact), Direct: h.Direct, Reason: h.Reason,
		}
		if len(after) != len(during) {
			l, still := later[h.Service]
			so.Recovers = !still || (h.Impact == deps.ImpactDown && l != deps.ImpactDown)
		}
		out = append(out, so)
	}
	return out
}

// serviceEnv is de omgeving van een dienst: die van zijn cluster, of van
// het cluster van zijn node.
func serviceEnv(g *deps.Graph, s deps.Svc) string {
	switch {
	case s.ClusterID != nil:
		if c, ok := g.Cluster(*s.ClusterID); ok {
			return c.Environment
		}
	case s.NodeID != nil:
		if n, ok := g.Node(*s.NodeID); ok && n.ClusterID != nil {
			if c, ok := g.Cluster(*n.ClusterID); ok {
				return c.Environment
			}
		}
	}
	return ""
}

func keys(m map[uuid.UUID]string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return cmp.Compare(a.String(), b.String()) })
	return out
}
