package deps

import (
	"cmp"
	"encoding/json"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Window is hoe ver het bewijs voor een afhankelijkheid terugkijkt.
const Window = 24 * time.Hour

// Conn is een uitgaande verbinding zoals de agents haar zagen: van een unit
// op een node naar Addr:Port.
type Conn struct {
	NodeID    uuid.UUID
	Unit      string
	Addr      string
	Port      int
	FirstSeen time.Time
	LastSeen  time.Time
	// Recent is hoe vaak een agent haar zag binnen Window. Hij kijkt elke
	// minuut, dus een verbinding die een uur openstaat telt 60 keer.
	Recent int64
}

// Net zijn van één node de adressen en wie op welke TCP-poort luistert.
type Net struct {
	NodeID uuid.UUID
	Addrs  []string
	// Listen geeft per poort de unit; leeg als die niet te zien is.
	Listen map[int]string
}

// listener is één regel uit de facts; alleen wat Net nodig heeft.
type listener struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	Unit  string `json:"unit"`
}

// NetOf maakt een Net uit de rij van een node: het primaire adres, de
// adressen uit de heartbeat en de luisterende poorten uit de facts.
func NetOf(id uuid.UUID, primary string, addrs []string, listeners []byte) Net {
	n := Net{NodeID: id, Listen: map[int]string{}}
	if primary != "" {
		n.Addrs = append(n.Addrs, primary)
	}
	n.Addrs = append(n.Addrs, addrs...)
	var ls []listener
	_ = json.Unmarshal(listeners, &ls)
	for _, l := range ls {
		if l.Proto == "tcp" && l.Port > 0 && (n.Listen[l.Port] == "" || l.Unit != "") {
			n.Listen[l.Port] = l.Unit
		}
	}
	return n
}

// Nets maakt adressen opzoekbaar.
type Nets struct {
	byAddr map[netip.Addr]uuid.UUID
	listen map[uuid.UUID]map[int]string
}

func NewNets(ns []Net) Nets {
	out := Nets{byAddr: map[netip.Addr]uuid.UUID{}, listen: map[uuid.UUID]map[int]string{}}
	for _, n := range ns {
		for _, a := range n.Addrs {
			if ip, ok := parseIP(a); ok && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				out.byAddr[ip] = n.NodeID
			}
		}
		out.listen[n.NodeID] = n.Listen
	}
	return out
}

// parseIP leest een adres, ook met een prefix, en maakt van ::ffff:10.0.0.1
// gewoon 10.0.0.1.
func parseIP(s string) (netip.Addr, bool) {
	s, _, _ = strings.Cut(s, "/")
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap().WithZone(""), true
}

// Skip is true voor een bestemming die nooit een voorstel wordt: NATS en de
// webinterface van ClusterForge zelf.
type Skip func(addr netip.Addr, port int) bool

// SelfSkip slaat de opgegeven poorten op de eigen adressen van ClusterForge
// over.
func SelfSkip(addrs []string, ports []int) Skip {
	own := map[netip.Addr]bool{}
	for _, a := range addrs {
		if ip, ok := parseIP(a); ok {
			own[ip] = true
		}
	}
	return func(addr netip.Addr, port int) bool { return own[addr] && slices.Contains(ports, port) }
}

// agentUnit is de agent zelf: zijn verbinding met NATS en de downloads van
// de webinterface zijn nooit een afhankelijkheid.
const agentUnit = "cf-agent"

// Soorten bestemmingen.
const (
	DestService      = "service"
	DestNode         = "node"
	DestVIP          = "vip"
	DestClusterForge = "clusterforge"
	DestUnknown      = "unknown"
)

// Dest is waar een verbinding heen gaat.
type Dest struct {
	Kind string
	// Service is de dienst bij DestService.
	Service *Svc
	// NodeID is de node met dat adres, bij DestNode en bij een dienst op een
	// node-adres.
	NodeID uuid.UUID
	// ClusterID is het cluster van het VIP, bij DestVIP en bij een dienst
	// achter een VIP.
	ClusterID *uuid.UUID
}

// Dest zoekt waar een verbinding van een node heen gaat: een VIP, een node
// (een loopback-adres is de node zelf) of een externe dienst met dat adres
// en die poort. Op een node of een VIP zoekt hij eerst een dienst met die
// poort, en anders de dienst van de unit die er op die poort luistert.
func (g *Graph) Dest(nets Nets, c Conn, skip Skip) Dest {
	ip, ok := parseIP(c.Addr)
	if !ok {
		return Dest{Kind: DestUnknown}
	}
	if skip != nil && skip(ip, c.Port) {
		return Dest{Kind: DestClusterForge}
	}
	// Een loopback-adres is de node zelf.
	if ip.IsLoopback() {
		return g.onNode(nets, c.NodeID, c.Port)
	}
	// Eerst de VIP's: de eigenaar heeft het adres ook.
	for _, cl := range g.Clusters {
		for _, v := range cl.VIPs {
			if vip, ok := parseIP(v.Address); !ok || vip != ip {
				continue
			}
			id := cl.ID
			d := Dest{Kind: DestVIP, ClusterID: &id}
			// De units die op een node van het cluster op die poort luisteren.
			var units []string
			for _, n := range g.Nodes {
				if n.ClusterID != nil && *n.ClusterID == id && nets.listen[n.ID][c.Port] != "" {
					units = append(units, nets.listen[n.ID][c.Port])
				}
			}
			scope := func(s Svc) bool { return s.ClusterID != nil && *s.ClusterID == id }
			if s := g.serviceAt(scope, c.Port, units...); s != nil {
				d.Kind, d.Service = DestService, s
			}
			return d
		}
	}
	if nodeID, ok := nets.byAddr[ip]; ok {
		return g.onNode(nets, nodeID, c.Port)
	}
	for i := range g.Services {
		s := &g.Services[i]
		if s.External() && s.Port == c.Port {
			if a, ok := parseIP(s.Address); ok && a == ip {
				return Dest{Kind: DestService, Service: s}
			}
		}
	}
	return Dest{Kind: DestUnknown}
}

// onNode zoekt de dienst op een node achter een poort.
func (g *Graph) onNode(nets Nets, nodeID uuid.UUID, port int) Dest {
	d := Dest{Kind: DestNode, NodeID: nodeID}
	n := g.nodes[nodeID]
	scope := func(s Svc) bool {
		return (s.NodeID != nil && *s.NodeID == nodeID) || (n != nil && n.ClusterID != nil && s.ClusterID != nil && *s.ClusterID == *n.ClusterID)
	}
	if s := g.serviceAt(scope, port, nets.listen[nodeID][port]); s != nil {
		d.Kind, d.Service = DestService, s
	}
	return d
}

// stateRank zet bevestigde diensten voor voorstellen en genegeerde.
func stateRank(s string) int {
	return map[string]int{StateConfirmed: 0, StateSuggested: 1, StateIgnored: 2}[s]
}

// serviceAt zoekt binnen scope een dienst met die poort, en anders een met
// een van de units.
func (g *Graph) serviceAt(scope func(Svc) bool, port int, units ...string) *Svc {
	var byPort, byUnit []*Svc
	for i := range g.Services {
		s := &g.Services[i]
		if !scope(*s) {
			continue
		}
		if s.Port == port {
			byPort = append(byPort, s)
		}
		if s.Unit != "" && slices.Contains(units, s.Unit) {
			byUnit = append(byUnit, s)
		}
	}
	for _, list := range [][]*Svc{byPort, byUnit} {
		if len(list) == 0 {
			continue
		}
		slices.SortFunc(list, func(a, b *Svc) int {
			return cmp.Or(cmp.Compare(stateRank(a.State), stateRank(b.State)), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
		})
		return list[0]
	}
	return nil
}

// Origin zoekt de dienst die een verbinding maakte: een dienst met die unit
// in het cluster van de node, of op de node zelf.
func (g *Graph) Origin(c Conn) (Svc, bool) {
	if c.Unit == "" || c.Unit == agentUnit {
		return Svc{}, false
	}
	n := g.nodes[c.NodeID]
	scope := func(s Svc) bool {
		return (s.NodeID != nil && *s.NodeID == c.NodeID) || (n != nil && n.ClusterID != nil && s.ClusterID != nil && *s.ClusterID == *n.ClusterID)
	}
	var found []*Svc
	for i := range g.Services {
		s := &g.Services[i]
		if s.Unit == c.Unit && scope(*s) {
			found = append(found, s)
		}
	}
	if len(found) == 0 {
		return Svc{}, false
	}
	slices.SortFunc(found, func(a, b *Svc) int { return cmp.Compare(stateRank(a.State), stateRank(b.State)) })
	return *found[0], true
}

// Seen is wat de agents zagen van een dienst naar een andere, of naar een
// adres buiten ClusterForge (To nil, met Addr en Port).
type Seen struct {
	From Svc
	To   *Svc
	Addr string
	Port int
	// Recent, FirstSeen en LastSeen gelden voor alle regels samen.
	Recent    int64
	FirstSeen time.Time
	LastSeen  time.Time
	Lines     []SeenLine
}

// SeenLine is één verbinding van een unit op een node.
type SeenLine struct {
	NodeID    uuid.UUID
	Hostname  string
	Unit      string
	Addr      string
	Port      int
	Recent    int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// Observe zet verbindingen om naar paren van diensten. Alleen een
// verbinding van een dienst telt; een verbinding naar ClusterForge zelf,
// naar dezelfde dienst of naar een node of VIP zonder dienst op die poort
// valt weg. De paren staan met de meeste verbindingen bovenaan.
func (g *Graph) Observe(nets Nets, conns []Conn, skip Skip) []Seen {
	type key struct {
		from, to uuid.UUID
		addr     string
		port     int
	}
	idx := map[key]int{}
	var out []Seen
	for _, c := range conns {
		from, ok := g.Origin(c)
		if !ok {
			continue
		}
		d := g.Dest(nets, c, skip)
		k := key{from: from.ID}
		switch d.Kind {
		case DestService:
			if d.Service.ID == from.ID {
				continue
			}
			k.to = d.Service.ID
		case DestUnknown:
			ip, ok := parseIP(c.Addr)
			if !ok {
				continue
			}
			k.addr, k.port = ip.String(), c.Port
		default:
			continue
		}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			s := Seen{From: from, Addr: k.addr, Port: k.port, FirstSeen: c.FirstSeen, LastSeen: c.LastSeen}
			if d.Service != nil {
				to := *d.Service
				s.To = &to
			}
			out = append(out, s)
		}
		s := &out[i]
		s.Recent += c.Recent
		s.FirstSeen = minTime(s.FirstSeen, c.FirstSeen)
		s.LastSeen = maxTime(s.LastSeen, c.LastSeen)
		host := ""
		if n := g.nodes[c.NodeID]; n != nil {
			host = n.Hostname
		}
		s.Lines = append(s.Lines, SeenLine{
			NodeID: c.NodeID, Hostname: host, Unit: c.Unit, Addr: c.Addr, Port: c.Port,
			Recent: c.Recent, FirstSeen: c.FirstSeen, LastSeen: c.LastSeen,
		})
	}
	for i := range out {
		slices.SortFunc(out[i].Lines, func(a, b SeenLine) int {
			return cmp.Or(cmp.Compare(b.Recent, a.Recent), cmp.Compare(a.Hostname, b.Hostname), cmp.Compare(a.Addr, b.Addr))
		})
	}
	slices.SortStableFunc(out, func(a, b Seen) int {
		return cmp.Or(cmp.Compare(b.Recent, a.Recent), cmp.Compare(a.From.Name, b.From.Name), cmp.Compare(a.Addr, b.Addr), cmp.Compare(a.Port, b.Port))
	})
	return out
}

// Text is het bewijs in woorden: "php8.2-fpm op web-01 → mariadb in
// db-prod: 1.240 verbindingen gezien naar 10.0.30.100:3306 in de afgelopen
// 24 uur".
func (g *Graph) Text(s Seen) string {
	var hosts, addrs []string
	for _, l := range s.Lines {
		if l.Hostname != "" && !slices.Contains(hosts, l.Hostname) {
			hosts = append(hosts, l.Hostname)
		}
		if a := HostPort(l.Addr, l.Port); !slices.Contains(addrs, a) {
			addrs = append(addrs, a)
		}
	}
	// Naar een onbekend adres staat het adres al links van de dubbele punt.
	to, where := HostPort(s.Addr, s.Port), ""
	if s.To != nil {
		to, where = g.labelFrom(s.From.ID, s.To.ID), " naar "+listed(addrs, 2)
		if s.To.External() {
			to = s.To.Name + " (extern)"
		}
	}
	from := s.From.Name
	if len(hosts) > 0 {
		from += " op " + listed(hosts, 3)
	}
	what := "geen verbindingen meer gezien" + where + " in de afgelopen 24 uur"
	if s.Recent > 0 {
		what = Count(s.Recent, "verbinding", "verbindingen") + " gezien" + where + " in de afgelopen 24 uur"
	}
	return from + " → " + to + ": " + what
}

// HostPort schrijft 10.0.30.100:3306, en [2001:db8::1]:443 voor IPv6.
func HostPort(addr string, port int) string {
	if ip, ok := parseIP(addr); ok {
		return netip.AddrPortFrom(ip, uint16(port)).String()
	}
	return addr + ":" + strconv.Itoa(port)
}

// listed somt hoogstens max namen op: "a, b en 2 andere".
func listed(names []string, max int) string {
	if len(names) > max {
		rest := len(names) - max + 1
		names = append(slices.Clone(names[:max-1]), strconv.Itoa(rest)+" andere")
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " en " + names[len(names)-1]
}

// Count schrijft 1240 als "1.240 verbindingen".
func Count(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return b.String() + " " + many
}

func minTime(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// NodeAt geeft de node met dat adres.
func (n Nets) NodeAt(addr string) (uuid.UUID, bool) {
	ip, ok := parseIP(addr)
	if !ok {
		return uuid.Nil, false
	}
	id, ok := n.byAddr[ip]
	return id, ok
}

// DestLabel is een bestemming in woorden: "mariadb in db-prod", "web-02",
// "VIP van db-prod" of "ClusterForge".
func (g *Graph) DestLabel(d Dest) string {
	switch d.Kind {
	case DestService:
		if d.Service.External() {
			return d.Service.Name + " (extern)"
		}
		return d.Service.Name + " in " + g.GroupOf(*d.Service).Name
	case DestNode:
		if n := g.nodes[d.NodeID]; n != nil {
			return n.Hostname
		}
	case DestVIP:
		if d.ClusterID != nil {
			if c := g.clusters[*d.ClusterID]; c != nil {
				return "VIP van " + c.Name
			}
		}
	case DestClusterForge:
		return "ClusterForge"
	}
	return ""
}

// Dependency geeft de pijl van from naar to, als die er is.
func (g *Graph) Dependency(from, to uuid.UUID) (Dep, bool) {
	for _, d := range g.providers[from] {
		if d.To == to {
			return d, true
		}
	}
	return Dep{}, false
}
