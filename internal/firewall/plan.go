package firewall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Settings zijn de instellingen die het plan raken.
type PlanSettings struct {
	LAN     []string `json:"lan"`
	Admin   []string `json:"admin"`
	SSHFrom string   `json:"ssh_from"`
	// Unknown is de klasse voor poorten van onbekende software: lan of
	// closed.
	Unknown string `json:"unknown"`
	Strict  bool   `json:"strict"`
}

// Port is een poort die op de node gezien is (nu of binnen de wachttijd).
type Port struct {
	Proto     string `json:"proto"`
	Port      int    `json:"port"`
	Addr      string `json:"addr,omitempty"`
	Process   string `json:"process,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Container string `json:"container,omitempty"`
	Image     string `json:"image,omitempty"`
	// Listening: nu te zien; anders onthouden.
	Listening bool `json:"listening"`
}

// Custom is een eigen regel die voor de node geldt.
type Custom struct {
	ID string `json:"id"`
	// Action is open of close.
	Action string `json:"action"`
	Proto  string `json:"proto"`
	Port   string `json:"port"`
	// Source is een klasse of een adres/netwerk (bij open).
	Source  string `json:"source"`
	Comment string `json:"comment"`
}

// Input is alles wat het plan van een node nodig heeft.
type Input struct {
	Template string
	Status   *protocol.FirewallStatus
	// Units zijn de systemd-units die enabled of actief zijn.
	Units []string
	// Keepalived: keepalived staat op de node; Unicast zijn zijn
	// unicast_peer-adressen.
	Keepalived bool
	Unicast    []string
	Ports      []Port
	Peers      []string
	VIPs       []string
	Settings   PlanSettings
	Custom     []Custom
}

// Entry is één regel van het plan zoals de pagina ze toont.
type Entry struct {
	Proto string `json:"proto"`
	Port  string `json:"port"`
	// Software is de naam uit de catalogus, of leeg.
	Software string `json:"software"`
	Label    string `json:"label,omitempty"`
	Process  string `json:"process,omitempty"`
	// Class is de bronklasse, of een adres bij een eigen regel.
	Class string `json:"class"`
	// Origin: ssh, listening, remembered, unit, template, vrrp, custom,
	// docker.
	Origin string `json:"origin"`
	// State: open, closed, docker of ignored.
	State string   `json:"state"`
	From  []string `json:"from"`
	// Unknown: software die de catalogus niet kent.
	Unknown bool   `json:"unknown,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Plan is wat ClusterForge op een node wil.
type Plan struct {
	Hash     string                  `json:"hash"`
	Strict   bool                    `json:"strict"`
	Rules    []protocol.FirewallRule `json:"rules"`
	Entries  []Entry                 `json:"entries"`
	Warnings []string                `json:"warnings"`
}

// Build rekent het plan uit.
func Build(in Input) Plan {
	b := builder{in: in, seen: map[string]int{}}
	b.ssh()
	b.template()
	b.units()
	b.listening()
	b.docker()
	b.vrrp()
	b.custom()
	return b.finish()
}

type builder struct {
	in       Input
	entries  []Entry
	seen     map[string]int // proto/port/class → index
	warnings []string
	closed   map[string]string // proto/port → reden
}

func (b *builder) resolve(class string) ([]string, string) {
	s := b.in.Settings
	switch class {
	case ClassAny:
		return []string{"any"}, ""
	case ClassLAN:
		return s.LAN, ""
	case ClassAdmin:
		if len(s.Admin) > 0 {
			return s.Admin, ""
		}
		return s.LAN, ""
	case ClassContainers:
		return ContainerNets, ""
	case ClassCluster:
		out := slices.Concat(b.in.Peers, b.in.VIPs)
		if len(out) == 0 {
			return nil, "geen andere clusterleden"
		}
		return out, ""
	case ClassSSH:
		switch s.SSHFrom {
		case ClassAny, ClassAdmin:
			return b.resolve(s.SSHFrom)
		}
		return b.resolve(ClassLAN)
	}
	if protocol.ValidSource(class) == nil {
		return []string{class}, ""
	}
	return nil, "onbekende bron " + class
}

// add zet een poort open (of noteert waarom niet). Een poort met dezelfde
// klasse komt één keer.
func (b *builder) add(e Entry) {
	key := e.Proto + "/" + e.Port + "/" + e.Class
	if i, ok := b.seen[key]; ok {
		cur := &b.entries[i]
		if e.Software != "" && (cur.Software == "" || cur.Origin == "template" && e.Origin == "listening") {
			cur.Software, cur.Unknown = e.Software, false
		}
		if cur.Process == "" {
			cur.Process = e.Process
		}
		if e.Origin == "listening" && cur.Origin != "ssh" {
			cur.Origin = "listening"
		}
		return
	}
	if e.State == "" {
		e.State = "open"
		if e.Class == ClassIgnore {
			e.State = "ignored"
		} else if from, why := b.resolve(e.Class); len(from) == 0 {
			e.State, e.Note = "closed", why
		} else {
			e.From = from
		}
	}
	if e.From == nil {
		e.From = []string{}
	}
	b.seen[key] = len(b.entries)
	b.entries = append(b.entries, e)
}

func (b *builder) ssh() {
	sshPorts := []int{}
	if b.in.Status != nil {
		for _, l := range b.in.Status.Listeners {
			if l.Proto == "tcp" && l.Process == "sshd" && !l.Local() && !slices.Contains(sshPorts, l.Port) {
				sshPorts = append(sshPorts, l.Port)
			}
		}
	}
	if len(sshPorts) == 0 {
		sshPorts = []int{22}
	}
	for _, p := range sshPorts {
		b.add(Entry{Proto: "tcp", Port: strconv.Itoa(p), Software: "SSH", Process: "sshd", Class: ClassSSH, Origin: "ssh", Note: "altijd open"})
	}
}

func (b *builder) template() {
	for _, p := range Templates[b.in.Template] {
		b.add(Entry{Proto: p.Proto, Port: p.Port, Software: "template " + b.in.Template, Label: p.Label, Class: p.Class, Origin: "template"})
	}
}

func (b *builder) units() {
	for _, u := range b.in.Units {
		for _, s := range Catalog {
			if !s.Fixed || !s.matchUnit(u) {
				continue
			}
			for _, p := range s.Ports {
				b.add(Entry{Proto: p.Proto, Port: p.Port, Software: s.Name, Label: p.Label, Class: p.Class, Origin: "unit", Note: "unit " + u})
			}
		}
	}
}

func (b *builder) listening() {
	ports := slices.Clone(b.in.Ports)
	sort.SliceStable(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	for _, p := range ports {
		if a, err := netip.ParseAddr(p.Addr); err == nil && a.Unmap().IsLoopback() {
			continue
		}
		origin := "listening"
		if !p.Listening {
			origin = "remembered"
		}
		port := strconv.Itoa(p.Port)
		s, ok := identify(p.Process, p.Unit, p.Image)
		if p.Process == "docker-proxy" {
			// Het hoort bij een gepubliceerde poort; docker() toont die.
			continue
		}
		e := Entry{Proto: p.Proto, Port: port, Process: p.Process, Origin: origin}
		switch {
		case !ok:
			e.Unknown = true
			e.Class = ClassLAN
			if b.in.Settings.Unknown == "closed" {
				e.State, e.Note = "closed", "onbekende software"
				e.Class = "closed"
			}
		default:
			e.Software = s.Name
			e.Class = s.Default
			for _, sp := range s.Ports {
				if sp.Proto == p.Proto && sp.Port == port {
					e.Class, e.Label = sp.Class, sp.Label
					break
				}
			}
			if e.Class == "" {
				e.Class, e.Unknown = ClassLAN, true
			}
		}
		if p.Container != "" {
			e.Note = strings.TrimSpace(e.Note + " container " + p.Container)
		}
		b.add(e)
	}
}

func (b *builder) docker() {
	if b.in.Status == nil {
		return
	}
	for _, c := range b.in.Status.Containers {
		s, _ := identify("", "", c.Image)
		for _, p := range c.Ports {
			if a, err := netip.ParseAddr(p.HostIP); err == nil && a.Unmap().IsLoopback() {
				continue
			}
			name := s.Name
			if name == "" {
				name = c.Image
			}
			b.add(Entry{Proto: p.Proto, Port: strconv.Itoa(p.HostPort), Software: name, Class: "docker", Origin: "docker", State: "docker",
				From: []string{}, Note: "gepubliceerd door container " + c.Name + "; Docker zet deze poort zelf open, buiten ufw om"})
		}
	}
}

func (b *builder) vrrp() {
	if !b.in.Keepalived && !slices.Contains(b.in.Units, "keepalived") {
		return
	}
	b.add(Entry{Proto: "vrrp", Port: "", Software: "Keepalived (VRRP)", Class: ClassCluster, Origin: "vrrp"})
	for _, p := range b.in.Unicast {
		if !slices.Contains(b.in.Peers, p) {
			b.add(Entry{Proto: "vrrp", Port: "", Software: "Keepalived (VRRP)", Label: "unicast_peer", Class: p, Origin: "vrrp"})
		}
	}
}

func (b *builder) custom() {
	b.closed = map[string]string{}
	for _, c := range b.in.Custom {
		switch c.Action {
		case "close":
			b.closed[c.Proto+"/"+c.Port] = c.Comment
		case "open":
			b.add(Entry{Proto: c.Proto, Port: c.Port, Software: "eigen regel", Label: c.Comment, Class: c.Source, Origin: "custom"})
		}
	}
}

func (b *builder) finish() Plan {
	p := Plan{Strict: b.in.Settings.Strict, Rules: []protocol.FirewallRule{}, Warnings: []string{}}
	for i := range b.entries {
		e := &b.entries[i]
		if why, ok := b.closed[e.Proto+"/"+e.Port]; ok && e.State == "open" && e.Origin != "ssh" && e.Origin != "custom" {
			e.State, e.From, e.Note = "closed", []string{}, strings.TrimSpace("eigen regel: dicht "+why)
		}
		if e.Unknown && e.State == "open" && e.Origin == "listening" {
			b.warnings = append(b.warnings, fmt.Sprintf("%s/%s (%s): onbekende software, open voor het LAN", e.Port, e.Proto, orDash(e.Process)))
		}
	}
	// Regels per proto/poort: een bron die binnen een andere valt, vervalt.
	type group struct {
		proto, port string
		froms       []string
		labels      []string
	}
	var order []string
	groups := map[string]*group{}
	for _, e := range b.entries {
		if e.State != "open" {
			continue
		}
		k := e.Proto + "/" + e.Port
		g, ok := groups[k]
		if !ok {
			g = &group{proto: e.Proto, port: e.Port}
			groups[k] = g
			order = append(order, k)
		}
		g.froms = append(g.froms, e.From...)
		name := e.Software
		if name == "" {
			name = orDash(e.Process)
		}
		if !slices.Contains(g.labels, name) {
			g.labels = append(g.labels, name)
		}
	}
	for _, k := range order {
		g := groups[k]
		comment := commentText(strings.Join(g.labels, ", "))
		for _, f := range minimal(g.froms) {
			p.Rules = append(p.Rules, protocol.FirewallRule{Action: "allow", Proto: g.proto, Port: g.port, From: f, Comment: comment})
		}
	}
	p.Entries, p.Warnings = b.entries, append(p.Warnings, b.warnings...)
	if p.Entries == nil {
		p.Entries = []Entry{}
	}
	h := sha256.New()
	_ = json.NewEncoder(h).Encode(struct {
		R []protocol.FirewallRule
		S bool
	}{p.Rules, p.Strict})
	p.Hash = hex.EncodeToString(h.Sum(nil))[:16]
	return p
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// commentText maakt een geldig ufw-commentaar.
func commentText(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune(" ._/:+()-", r):
			sb.WriteRune(r)
		case r == ',':
			sb.WriteString(" +")
		}
	}
	out := protocol.FirewallCommentPrefix + sb.String()
	if len(out) > 80 {
		out = out[:80]
	}
	return strings.TrimSpace(out)
}

// minimal haalt dubbele bronnen weg en bronnen die binnen een andere vallen.
func minimal(froms []string) []string {
	type src struct {
		raw string
		p   netip.Prefix
	}
	var list []src
	for _, f := range froms {
		if f == "any" {
			return []string{"any"}
		}
		if a, err := netip.ParseAddr(f); err == nil {
			list = append(list, src{raw: a.String(), p: netip.PrefixFrom(a, a.BitLen())})
			continue
		}
		if pr, err := netip.ParsePrefix(f); err == nil {
			list = append(list, src{raw: pr.Masked().String(), p: pr.Masked()})
		}
	}
	var out []string
	for i, s := range list {
		covered := false
		for j, o := range list {
			if i == j {
				continue
			}
			if o.p.Bits() < s.p.Bits() && o.p.Contains(s.p.Addr()) || o.p == s.p && j < i {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, s.raw)
		}
	}
	return out
}
