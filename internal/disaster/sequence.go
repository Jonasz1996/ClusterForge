package disaster

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ActServiceStop stopt een dienst in een reeks, met een gewone servicestap.
// De andere acties zijn de vaste storingen van de agent (protocol 6), met
// een lease die de server verlengt en een harde bovengrens.
const ActServiceStop = "service_stop"

// Action is een soort stap met zijn label.
type Action struct {
	Key   string
	Label string
}

// Actions staan in de volgorde van het formulier.
var Actions = []Action{
	{protocol.FaultVRRPBlock, "VRRP blokkeren"},
	{protocol.FaultLinkDown, "Interface down"},
	{protocol.FaultPortBlock, "TCP-poort dicht"},
	{protocol.FaultNetem, "Vertraging of pakketverlies"},
	{protocol.FaultDiskFill, "Schijf vullen"},
	{ActServiceStop, "Dienst stoppen"},
}

func isFault(action string) bool { return action != ActServiceStop }

// Op welke nodes een stap werkt. De VIP-houder is de node die bij de start
// het VIP heeft; de andere nodes zijn alle actieve nodes behalve die.
const (
	OnOwner  = "owner"
	OnOthers = "others"
	OnAll    = "all"
	OnNode   = "node"
)

// Grenzen van een reeks.
const (
	MaxSteps       = 5
	MinFillPercent = 50
)

// stopUnits zijn de diensten die een stap mag stoppen: keepalived en de
// units van service_down.
var stopUnits = append([]string{"keepalived"}, Units...)

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)

// Step is één stap van een reeks: wat er op welk tijdstip op welke nodes
// gebeurt. Alles blijft aan tot het einde van de storing.
type Step struct {
	AtSeconds int    `json:"at_seconds"`
	Action    string `json:"action"`
	On        string `json:"on"`
	// NodeID en Hostname bij On node.
	NodeID   *uuid.UUID `json:"node_id,omitempty"`
	Hostname string     `json:"hostname,omitempty"`
	// Interface bij link_down en netem.
	Interface string `json:"interface,omitempty"`
	// Port bij port_block.
	Port int `json:"port,omitempty"`
	// DelayMS en LossPercent bij netem.
	DelayMS     int `json:"delay_ms,omitempty"`
	LossPercent int `json:"loss_percent,omitempty"`
	// FillPercent bij disk_fill.
	FillPercent int `json:"fill_percent,omitempty"`
	// Unit bij service_stop.
	Unit string `json:"unit,omitempty"`
}

func (st Step) at() time.Duration { return time.Duration(st.AtSeconds) * time.Second }

// onText zegt op welke nodes een stap werkt.
func (st Step) onText() string {
	switch st.On {
	case OnOwner:
		return "de VIP-houder"
	case OnOthers:
		return "de andere nodes"
	case OnAll:
		return "alle nodes"
	}
	return or(st.Hostname, "één node")
}

// Text beschrijft een stap in gewone taal.
func (st Step) Text() string {
	switch st.Action {
	case protocol.FaultVRRPBlock:
		return "VRRP blokkeren op " + st.onText()
	case protocol.FaultLinkDown:
		return st.Interface + " down op " + st.onText()
	case protocol.FaultPortBlock:
		return fmt.Sprintf("TCP-poort %d dicht op %s", st.Port, st.onText())
	case protocol.FaultNetem:
		var parts []string
		if st.DelayMS > 0 {
			parts = append(parts, fmt.Sprintf("%d ms vertraging", st.DelayMS))
		}
		if st.LossPercent > 0 {
			parts = append(parts, fmt.Sprintf("%d %% pakketverlies", st.LossPercent))
		}
		return strings.Join(parts, " en ") + " op " + st.Interface + " van " + st.onText()
	case protocol.FaultDiskFill:
		return fmt.Sprintf("schijf vullen tot %d %% op %s", st.FillPercent, st.onText())
	case ActServiceStop:
		return st.Unit + " stoppen op " + st.onText()
	}
	return st.Action
}

// key is wat twee stappen niet allebei op dezelfde node mogen doen: de agent
// weigert een tweede storing op dezelfde interface, een tweede VRRP-blokkade,
// dezelfde poort of een tweede vulbestand.
func (st Step) key() string {
	switch st.Action {
	case protocol.FaultLinkDown, protocol.FaultNetem:
		return "interface " + st.Interface
	case protocol.FaultPortBlock:
		return fmt.Sprintf("poort %d", st.Port)
	case ActServiceStop:
		return st.Unit
	}
	return st.Action
}

func describeSteps(steps []Step) string {
	if len(steps) == 0 {
		return "reeks zonder stappen"
	}
	parts := make([]string, len(steps))
	for i, st := range steps {
		parts[i] = fmt.Sprintf("%d s: %s", st.AtSeconds, st.Text())
	}
	return strings.Join(parts, "; ")
}

// NodeOption is een actieve node met agent, met wat een stap erover moet
// weten.
type NodeOption struct {
	ID       uuid.UUID
	Hostname string
	// Owner: deze node heeft nu het VIP, als enige.
	Owner bool
	// Protocol is de protocolversie van de agent; storingen vragen 6.
	Protocol int
	Nft, Tc  bool
	// Interfaces zijn de interfaces zonder lo. Primary is die met het adres
	// waarmee de agent ClusterForge bereikt; leeg als dat niet bekend is, en
	// dan geldt elke interface zo, net als in de agent.
	Interfaces []string
	Primary    string
	// Units zijn de diensten die een stap hier kan stoppen.
	Units []string
}

// cutOff is true als link_down op deze interface de agent van ClusterForge
// afsnijdt.
func (n NodeOption) cutOff(iface string) bool { return n.Primary == "" || n.Primary == iface }

// ActionOption zegt of een actie hier kan, en anders waarom niet.
type ActionOption struct {
	Action
	Available bool
	Reason    string
}

// faultNode is een NodeOption met wat de voorspelling en de voorcontrole
// verder gebruiken.
type faultNode struct {
	NodeOption
	services map[string]string
	// addressed zijn de interfaces met een adres; vipIface heeft het adres in
	// het netwerk van het VIP.
	addressed map[string]bool
	vipIface  string
	faults    []string
	self      bool
}

// faultNodes leest de actieve nodes met agent van een cluster, met hun
// interfaces, gereedschap en storingen uit de facts. De VIP-houder is de
// enige node met het eerste VIP in zijn heartbeat.
func (s *Service) faultNodes(ctx context.Context, clusterID uuid.UUID, vips []string, m topo.Model) ([]faultNode, error) {
	rows, err := s.q.ListFailoverNodes(ctx, &clusterID)
	if err != nil {
		return nil, err
	}
	facts, err := s.q.ListFaultFacts(ctx, &clusterID)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]store.ListFaultFactsRow{}
	for _, f := range facts {
		byID[f.NodeID] = f
	}
	var vip netip.Addr
	if len(vips) > 0 {
		vip, _ = netip.ParseAddr(vips[0])
	}
	out := []faultNode{}
	holders := 0
	for _, n := range rows {
		if n.Lifecycle != store.NodeLifecycleActive || !n.HasAgent {
			continue
		}
		fn := faultNode{
			NodeOption: NodeOption{ID: n.ID, Hostname: n.Hostname, Protocol: int(n.AgentProtocol), Interfaces: []string{}, Units: []string{}},
			services:   health.ServiceStates(n.Services), addressed: map[string]bool{},
		}
		if vip.IsValid() && slices.Contains(n.Addresses, vip.String()) {
			fn.Owner = true
			holders++
		}
		if f, ok := byID[n.ID]; ok {
			var ifs []protocol.Interface
			_ = json.Unmarshal(f.Interfaces, &ifs)
			for _, i := range ifs {
				if i.Name == "lo" || i.Name == "" {
					continue
				}
				fn.Interfaces = append(fn.Interfaces, i.Name)
				for _, a := range i.Addresses {
					p, err := netip.ParsePrefix(a)
					if err != nil {
						continue
					}
					fn.addressed[i.Name] = true
					if p.Addr().String() == f.PrimaryAddress {
						fn.Primary = i.Name
					}
					if vip.IsValid() && p.Masked().Contains(vip) && fn.vipIface == "" {
						fn.vipIface = i.Name
					}
				}
			}
			var tools *protocol.Tools
			if json.Unmarshal(f.Tools, &tools) == nil && tools != nil {
				fn.Nft, fn.Tc = tools.Nft, tools.Tc
			}
			_ = json.Unmarshal(f.Faults, &fn.faults)
		}
		for _, u := range stopUnits {
			if _, ok := fn.services[u]; ok {
				fn.Units = append(fn.Units, u)
			}
		}
		fn.self = m.Self.Found() && n.ProxmoxID != nil && n.PveVmid != nil && *n.ProxmoxID == m.Self.Conn && int(*n.PveVmid) == m.Self.VMID
		out = append(out, fn)
	}
	if holders > 1 {
		// Split-brain: er is geen VIP-houder.
		for i := range out {
			out[i].Owner = false
		}
	}
	return out, nil
}

func options(nodes []faultNode) []NodeOption {
	out := make([]NodeOption, len(nodes))
	for i, n := range nodes {
		out[i] = n.NodeOption
	}
	return out
}

// actionOptions zegt per actie of ze hier kan. Ontbreekt een gereedschap,
// dan is de actie grijs met de reden.
func actionOptions(nodes []faultNode) []ActionOption {
	var faults []faultNode
	for _, n := range nodes {
		if n.Protocol >= protocol.FaultsSince {
			faults = append(faults, n)
		}
	}
	anyOf := func(ns []faultNode, ok func(n faultNode) bool) bool { return slices.ContainsFunc(ns, ok) }
	out := make([]ActionOption, 0, len(Actions))
	for _, a := range Actions {
		o := ActionOption{Action: a, Available: true}
		switch {
		case len(nodes) == 0:
			o.Available, o.Reason = false, "geen actieve node van dit cluster heeft een agent"
		case a.Key == ActServiceStop:
			if !anyOf(nodes, func(n faultNode) bool { return len(n.Units) > 0 }) {
				o.Available, o.Reason = false, "op geen enkele node staat "+list(stopUnits)
			}
		case len(faults) == 0:
			o.Available, o.Reason = false, "geen agent van dit cluster kent storingen; werk de agents bij naar protocol 6 met install.sh --upgrade"
		case a.Key == protocol.FaultVRRPBlock && !anyOf(faults, func(n faultNode) bool { return slices.Contains(n.Units, "keepalived") }):
			o.Available, o.Reason = false, "op geen enkele node met een nieuwe agent staat keepalived"
		case (a.Key == protocol.FaultVRRPBlock || a.Key == protocol.FaultPortBlock) && !anyOf(faults, func(n faultNode) bool { return n.Nft }):
			o.Available, o.Reason = false, "nft staat op geen enkele node met een nieuwe agent; installeer nftables"
		case a.Key == protocol.FaultNetem && !anyOf(faults, func(n faultNode) bool { return n.Tc }):
			o.Available, o.Reason = false, "tc staat op geen enkele node met een nieuwe agent; installeer iproute2"
		}
		out = append(out, o)
	}
	return out
}

// resolve geeft de nodes van een stap. Een dienst stoppen op de andere of
// op alle nodes slaat nodes zonder die dienst over.
func resolve(st Step, nodes []NodeOption) []NodeOption {
	hasOwner := slices.ContainsFunc(nodes, func(n NodeOption) bool { return n.Owner })
	var out []NodeOption
	for _, n := range nodes {
		hit := false
		switch st.On {
		case OnOwner:
			hit = n.Owner
		case OnOthers:
			hit = hasOwner && !n.Owner
		case OnAll:
			hit = true
		case OnNode:
			hit = st.NodeID != nil && n.ID == *st.NodeID
		}
		if hit && st.Action == ActServiceStop && (st.On == OnOthers || st.On == OnAll) && !slices.Contains(n.Units, st.Unit) {
			hit = false
		}
		if hit {
			out = append(out, n)
		}
	}
	return out
}

// validateSteps controleert de stappen tegen wat het cluster nu heeft en
// geeft ze op volgorde van tijdstip, met alleen de velden van hun actie.
func validateSteps(in []Step, fault int, o Options) ([]Step, error) {
	switch {
	case len(in) == 0:
		return nil, fieldErr("steps", "geef de reeks minstens één stap")
	case len(in) > MaxSteps:
		return nil, fieldErr("steps", fmt.Sprintf("een reeks heeft hoogstens %d stappen", MaxSteps))
	}
	steps := slices.Clone(in)
	slices.SortStableFunc(steps, func(a, b Step) int { return cmp.Compare(a.AtSeconds, b.AtSeconds) })
	nodes := map[uuid.UUID]NodeOption{}
	for _, n := range o.Nodes {
		nodes[n.ID] = n
	}
	labels := make([]string, len(Actions))
	for i, a := range Actions {
		labels[i] = strings.ToLower(a.Label)
	}
	for i := range steps {
		in := steps[i]
		bad := func(msg string) error { return fieldErr("steps", fmt.Sprintf("stap %d: %s", i+1, msg)) }
		if in.AtSeconds < 0 || in.AtSeconds >= fault {
			return nil, bad(fmt.Sprintf("het tijdstip ligt tussen 0 en %d s, voor het einde van de storing", fault-1))
		}
		ai := slices.IndexFunc(o.Actions, func(a ActionOption) bool { return a.Key == in.Action })
		if ai < 0 {
			return nil, bad("kies wat er gebeurt: " + list(labels))
		}
		if a := o.Actions[ai]; !a.Available {
			return nil, bad(strings.ToLower(a.Label) + " kan hier niet: " + a.Reason)
		}
		st := Step{AtSeconds: in.AtSeconds, Action: in.Action, On: in.On}
		switch in.On {
		case OnOwner, OnOthers:
			if len(o.VIPs) == 0 {
				return nil, bad("dit cluster heeft geen VIP, dus ook geen VIP-houder")
			}
		case OnAll:
		case OnNode:
			if in.NodeID == nil {
				return nil, bad("kies een node")
			}
			n, ok := nodes[*in.NodeID]
			if !ok {
				return nil, bad("kies een actieve node van dit cluster met een agent")
			}
			st.NodeID, st.Hostname = in.NodeID, n.Hostname
		default:
			return nil, bad("kies op welke nodes: de VIP-houder, de andere nodes, alle nodes of één node")
		}
		switch in.Action {
		case protocol.FaultLinkDown, protocol.FaultNetem:
			st.Interface = strings.TrimSpace(in.Interface)
			if !ifaceName.MatchString(st.Interface) || st.Interface == "lo" {
				return nil, bad("kies een interface")
			}
			if in.Action == protocol.FaultNetem {
				st.DelayMS, st.LossPercent = in.DelayMS, in.LossPercent
				if st.DelayMS < 0 || st.DelayMS > protocol.NetemMaxDelayMS || st.LossPercent < 0 || st.LossPercent > protocol.NetemMaxLoss ||
					st.DelayMS == 0 && st.LossPercent == 0 {
					return nil, bad(fmt.Sprintf("geef een vertraging tot %d ms of een pakketverlies tot %d %%, of allebei",
						protocol.NetemMaxDelayMS, protocol.NetemMaxLoss))
				}
			}
		case protocol.FaultPortBlock:
			st.Port = in.Port
			if st.Port < 1 || st.Port > 65535 {
				return nil, bad("de poort ligt tussen 1 en 65535")
			}
		case protocol.FaultDiskFill:
			st.FillPercent = in.FillPercent
			if st.FillPercent < MinFillPercent || st.FillPercent > protocol.DiskFillMax {
				return nil, bad(fmt.Sprintf("vul de schijf tot tussen %d en %d %%", MinFillPercent, protocol.DiskFillMax))
			}
		case ActServiceStop:
			st.Unit = in.Unit
			if !slices.Contains(stopUnits, st.Unit) {
				return nil, bad("kies welke dienst stopt: " + list(stopUnits))
			}
		}
		steps[i] = st
	}
	if problems := stepProblems(steps, fault, o.Nodes); len(problems) > 0 {
		return nil, fieldErr("steps", problems[0])
	}
	return steps, nil
}

// stepProblems zoekt wat niet kan met de nodes zoals ze nu zijn: een stap
// zonder node, een interface die er niet is, link_down op de interface naar
// ClusterForge die te lang duurt of waarna nog een stap volgt, en twee
// stappen die hetzelfde doen op dezelfde node.
func stepProblems(steps []Step, fault int, nodes []NodeOption) []string {
	var out []string
	done := map[uuid.UUID]map[string]int{}
	cut := map[uuid.UUID]int{}
	for i, st := range steps {
		ns := resolve(st, nodes)
		if len(ns) == 0 {
			why := "geen enkele node"
			switch {
			case (st.On == OnOwner || st.On == OnOthers) && !slices.ContainsFunc(nodes, func(n NodeOption) bool { return n.Owner }):
				why = "geen node: het VIP heeft nu geen houder of staat op meer nodes"
			case st.Action == ActServiceStop:
				why = "geen node met " + st.Unit
			}
			out = append(out, fmt.Sprintf("stap %d raakt %s", i+1, why))
			continue
		}
		for _, n := range ns {
			if j, ok := cut[n.ID]; ok {
				out = append(out, fmt.Sprintf("na stap %d is %s afgesneden van ClusterForge; stap %d bereikt die node niet meer", j+1, n.Hostname, i+1))
				continue
			}
			switch st.Action {
			case protocol.FaultLinkDown, protocol.FaultNetem:
				if !slices.Contains(n.Interfaces, st.Interface) {
					out = append(out, fmt.Sprintf("stap %d: %s heeft geen interface %s", i+1, n.Hostname, st.Interface))
					continue
				}
			case ActServiceStop:
				if !slices.Contains(n.Units, st.Unit) {
					out = append(out, fmt.Sprintf("stap %d: op %s staat geen %s", i+1, n.Hostname, st.Unit))
					continue
				}
			}
			if st.Action == protocol.FaultLinkDown && n.cutOff(st.Interface) {
				if rest := fault - st.AtSeconds; time.Duration(rest)*time.Second > protocol.FaultMaxCutOff {
					limit := int(protocol.FaultMaxCutOff.Seconds())
					out = append(out, fmt.Sprintf("stap %d: %s is op %s de interface naar ClusterForge; daar duurt link_down hoogstens %d s, dus begint deze stap ten vroegste op %d s",
						i+1, st.Interface, n.Hostname, limit, fault-limit))
				}
				cut[n.ID] = i
			}
			if done[n.ID] == nil {
				done[n.ID] = map[string]int{}
			}
			if j, ok := done[n.ID][st.key()]; ok {
				out = append(out, fmt.Sprintf("stap %d en %d doen allebei iets met %s op %s", j+1, i+1, st.key(), n.Hostname))
				continue
			}
			done[n.ID][st.key()] = i
		}
	}
	return out
}

// PlannedStep is een stap met de nodes die hij bij de start raakt.
type PlannedStep struct {
	Step
	Text  string     `json:"text"`
	Nodes []StepNode `json:"nodes"`
}

// StepNode is een node van een stap. CutOff: link_down op de interface naar
// ClusterForge; dan telt alleen de bovengrens, want verlengen en opheffen
// kan niet.
type StepNode struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	CutOff   bool      `json:"cut_off,omitempty"`
}

func stepsKey(steps []PlannedStep) string {
	parts := make([]string, len(steps))
	for i, ps := range steps {
		ids := make([]string, len(ps.Nodes))
		for j, n := range ps.Nodes {
			ids[j] = n.NodeID.String()
		}
		slices.Sort(ids)
		parts[i] = strings.Join(ids, "+")
	}
	return strings.Join(parts, ",")
}

// stoppedUnits zijn de diensten die de reeks op een node stopt; het herstel
// start ze weer.
func (d Definition) stoppedUnits(nodeID uuid.UUID) []string {
	var out []string
	for _, ps := range d.Steps {
		if ps.Action == ActServiceStop && slices.ContainsFunc(ps.Nodes, func(n StepNode) bool { return n.NodeID == nodeID }) {
			out = append(out, ps.Unit)
		}
	}
	return out
}

// faultIDs zijn de storingen die de reeks op een node zet. Het id is
// <run>/<stap>, zodat het herstel ze ook na een herstart kent.
func (d Definition) faultIDs(runID, nodeID uuid.UUID) []string {
	var out []string
	for i, ps := range d.Steps {
		if isFault(ps.Action) && slices.ContainsFunc(ps.Nodes, func(n StepNode) bool { return n.NodeID == nodeID }) {
			out = append(out, faultID(runID, i))
		}
	}
	return out
}

func faultID(runID uuid.UUID, i int) string { return fmt.Sprintf("%s/%d", runID, i+1) }

// cutOffNode is true als een stap de node van ClusterForge afsnijdt.
func (d Definition) cutOffNode(nodeID uuid.UUID) bool {
	for _, ps := range d.Steps {
		if slices.ContainsFunc(ps.Nodes, func(n StepNode) bool { return n.NodeID == nodeID && n.CutOff }) {
			return true
		}
	}
	return false
}

// readyUnits zijn de units die na het herstel weer moeten draaien. Na een
// harde stop is een verse heartbeat genoeg.
func (d Definition) readyUnits(t Target) []string {
	if d.Kind == Sequence {
		return d.stoppedUnits(t.NodeID)
	}
	if t.Unit == "" {
		return nil
	}
	return []string{t.Unit}
}

// seqTargets zoekt bij een reeks de nodes van elke stap en doet de
// controles die alleen voor een reeks gelden.
func (s *Service) seqTargets(ctx context.Context, p *Preview, sc Scenario, c store.Cluster, m topo.Model,
	add func(string, bool, string)) (*seqPlan, error) {
	vips, err := s.q.ListVIPsByCluster(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(vips))
	probes := make([]failover.Probe, 0, len(vips))
	for _, v := range vips {
		addrs = append(addrs, v.Vip.Address.String())
		probes = append(probes, s.Probes(c, v.Vip.Address.String()))
	}
	nodes, err := s.faultNodes(ctx, c.ID, addrs, m)
	if err != nil {
		return nil, err
	}
	plan := &seqPlan{nodes: nodes, probes: probes, vips: addrs}
	opts := options(nodes)
	byID := map[uuid.UUID]faultNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	p.Steps = []PlannedStep{}
	seen := map[uuid.UUID]bool{}
	for _, st := range sc.P.Steps {
		ps := PlannedStep{Step: st, Text: st.Text(), Nodes: []StepNode{}}
		for _, n := range resolve(st, opts) {
			ps.Nodes = append(ps.Nodes, StepNode{NodeID: n.ID, Hostname: n.Hostname, CutOff: st.Action == protocol.FaultLinkDown && n.cutOff(st.Interface)})
			if !seen[n.ID] {
				seen[n.ID] = true
				p.Targets = append(p.Targets, Target{NodeID: n.ID, Hostname: n.Hostname, ClusterID: c.ID, Cluster: c.Name})
			}
		}
		p.Steps = append(p.Steps, ps)
	}
	slices.SortFunc(p.Targets, func(a, b Target) int { return cmp.Compare(a.Hostname, b.Hostname) })
	if len(sc.P.Steps) == 0 {
		add("Stappen", false, "de reeks heeft geen stappen")
		return plan, nil
	}

	if problems := stepProblems(sc.P.Steps, sc.E.FaultSeconds, opts); len(problems) > 0 {
		add("Stappen", false, strings.Join(problems, "; "))
	} else {
		add("Stappen", true, fmt.Sprintf("%d stappen in %s; daarna heft ClusterForge alles op", len(sc.P.Steps), failover.Seconds(sc.E.fault())))
	}
	if slices.ContainsFunc(sc.P.Steps, func(st Step) bool { return st.On == OnOwner || st.On == OnOthers }) {
		if i := slices.IndexFunc(nodes, func(n faultNode) bool { return n.Owner }); i >= 0 {
			add("VIP-houder", true, nodes[i].Hostname+" heeft "+addrs[0])
		} else {
			add("VIP-houder", false, "het VIP heeft nu geen houder of staat op meer nodes; dan is niet te zeggen welke node de VIP-houder is")
		}
	}

	// Per stap: de agent, het gereedschap en de dienst.
	var old, oldApply, tools, stopped []string
	cuts := map[string]bool{}
	for _, ps := range p.Steps {
		for _, sn := range ps.Nodes {
			n := byID[sn.NodeID]
			switch {
			case isFault(ps.Action) && n.Protocol < protocol.FaultsSince:
				old = append(old, n.Hostname)
			case !isFault(ps.Action) && n.Protocol < protocol.ApplySince:
				oldApply = append(oldApply, n.Hostname)
			}
			switch {
			case (ps.Action == protocol.FaultVRRPBlock || ps.Action == protocol.FaultPortBlock) && !n.Nft:
				tools = append(tools, "nft op "+n.Hostname+" (installeer nftables)")
			case ps.Action == protocol.FaultNetem && !n.Tc:
				tools = append(tools, "tc op "+n.Hostname+" (installeer iproute2)")
			case ps.Action == ActServiceStop && n.services[ps.Unit] != "active":
				stopped = append(stopped, ps.Unit+" draait niet op "+n.Hostname)
			}
			if sn.CutOff {
				cuts[fmt.Sprintf("%s op %s (%d s)", ps.Interface, n.Hostname, sc.E.FaultSeconds-ps.AtSeconds)] = true
			}
		}
	}
	old, oldApply, tools, stopped = uniq(old), uniq(oldApply), uniq(tools), uniq(stopped)
	switch {
	case len(old) > 0:
		add("Agent", false, "de agent op "+list(old)+" kent geen storingen (protocol 6 nodig); werk hem bij met install.sh --upgrade")
	case len(oldApply) > 0:
		add("Agent", false, "de agent op "+list(oldApply)+" is te oud voor deploystappen; werk hem bij met install.sh --upgrade")
	default:
		add("Agent", true, "de agents kennen de stappen, en een storing ruimt de agent zelf op als ClusterForge haar niet meer verlengt")
	}
	if slices.ContainsFunc(sc.P.Steps, func(st Step) bool {
		return st.Action == protocol.FaultVRRPBlock || st.Action == protocol.FaultPortBlock || st.Action == protocol.FaultNetem
	}) {
		if len(tools) > 0 {
			add("Gereedschap", false, "ontbreekt: "+list(tools))
		} else {
			add("Gereedschap", true, "nft en tc staan waar een stap ze nodig heeft")
		}
	}
	if len(cuts) > 0 {
		add("Beheerinterface", true, "ClusterForge kan link_down op "+list(slices.Sorted(mapKeys(cuts)))+
			" niet verlengen of opheffen; de agent zet de interface na die tijd zelf weer aan, ook na een herstart")
	}
	if slices.ContainsFunc(sc.P.Steps, func(st Step) bool { return st.Action == ActServiceStop }) {
		if len(stopped) > 0 {
			add("Dienst draait", false, list(stopped))
		} else {
			add("Dienst draait", true, "elke dienst die een stap stopt, draait nu")
		}
	}
	var busy, self []string
	for _, t := range p.Targets {
		n := byID[t.NodeID]
		if len(n.faults) > 0 {
			busy = append(busy, n.Hostname+" ("+strings.Join(n.faults, ", ")+")")
		}
		if n.self {
			self = append(self, n.Hostname)
		}
	}
	if len(busy) > 0 {
		add("Geen storing actief", false, "er staat nog een storing aan op "+list(busy)+"; de agent heft haar zelf op als de lease afloopt")
	} else if len(p.Targets) > 0 {
		add("Geen storing actief", true, "op geen enkele node staat nog een storing aan")
	}
	if len(self) > 0 {
		add("ClusterForge zelf", false, "ClusterForge draait zelf op "+list(self))
	}
	return plan, nil
}

// seqPlan is wat de voorspelling van een reeks nodig heeft.
type seqPlan struct {
	nodes  []faultNode
	probes []failover.Probe
	vips   []string
}

func uniq(xs []string) []string {
	slices.Sort(xs)
	return slices.Compact(xs)
}

func mapKeys(m map[string]bool) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// simNode is de toestand van een node terwijl de voorspelling de reeks
// naspeelt.
type simNode struct {
	*faultNode
	// cut: link_down op de interface naar ClusterForge, dus geen heartbeat
	// meer. dark: link_down op de interface van het VIP, dus geen VRRP.
	cut, dark bool
	vrrp      bool
	ports     map[int]bool
	fill      int
	stopped   map[string]bool
}

func (n *simNode) running(unit string) bool {
	return n.services[unit] == "active" && !n.stopped[unit]
}

// predictSequence voorspelt een reeks: voor het cluster zelf met simulate,
// voor clusters die ervan afhangen met de wat-als.
func predictSequence(w risk.WhatIfWorld, c store.Cluster, def Definition, sp *seqPlan) (map[uuid.UUID]Prediction, []string) {
	g := w.Graph
	worst, notes, keys := simulate(def, sp)
	// Een dienst die op alle nodes stilstaat, raakt zijn afnemers.
	for _, sv := range g.Services {
		if sv.State == deps.StateConfirmed && !sv.External() && inCluster(g, sv, c.ID) && slices.Contains(keys, "unit:"+sv.Unit) {
			keys = append(keys, topo.ServiceKey(sv.ID))
		}
	}
	keys = slices.DeleteFunc(keys, func(k string) bool { return strings.HasPrefix(k, "unit:") })
	if worst.Status == string(status.Down) {
		keys = append(keys, topo.ClusterKey(c.ID))
	}
	out := map[uuid.UUID]Prediction{}
	if len(keys) > 0 {
		comps, err := w.Resolve(uniq(keys))
		if err != nil {
			notes = append(notes, "De wat-als kent dit onderdeel niet: "+err.Error()+".")
		} else if len(comps) > 0 {
			res := w.Run(comps, false)
			worse := func(id uuid.UUID, st, reason string) {
				if id == c.ID {
					return
				}
				if cur, ok := out[id]; !ok || rank(st) > rank(cur.Status) {
					out[id] = Prediction{Status: st, Reason: reason}
				}
			}
			for _, co := range res.Clusters {
				f := co.Final()
				worse(co.ID, f.Status, f.Reason)
			}
			for _, so := range res.Services {
				sv, ok := g.Service(so.ID)
				if !ok {
					continue
				}
				if id := clusterOf(g, sv); id != nil {
					st := string(status.Degraded)
					if so.Impact == string(deps.ImpactDown) {
						st = string(status.Down)
					}
					worse(*id, st, so.Name+": "+so.Reason)
				}
			}
			notes = append(notes, res.Notes...)
		}
	}
	out[c.ID] = worst
	return out, notes
}

// simulate speelt de reeks stap voor stap na op de nodes van het cluster en
// geeft de ergste toestand. keys zijn voor de wat-als: een node zonder
// netwerk, en "unit:<naam>" voor een dienst die op alle nodes stilstaat.
func simulate(def Definition, sp *seqPlan) (Prediction, []string, []string) {
	nodes, probes, vips := sp.nodes, sp.probes, sp.vips
	sim := make([]*simNode, len(nodes))
	byID := map[uuid.UUID]*simNode{}
	for i := range nodes {
		sim[i] = &simNode{faultNode: &nodes[i], ports: map[int]bool{}, stopped: map[string]bool{}}
		byID[nodes[i].ID] = sim[i]
	}
	var notes, keys []string
	note := func(s string) {
		if !slices.Contains(notes, s) {
			notes = append(notes, s)
		}
	}
	worst := Prediction{Status: string(status.Healthy), Reason: "de reeks verandert volgens de voorspelling niets aan de status"}
	for i, ps := range def.Steps {
		for _, sn := range ps.Nodes {
			n := byID[sn.NodeID]
			if n == nil {
				continue
			}
			switch ps.Action {
			case protocol.FaultLinkDown:
				switch {
				case sn.CutOff:
					n.cut = true
					n.dark = n.dark || n.vipIface == "" || n.vipIface == ps.Interface
					keys = append(keys, topo.NodeKey(n.ID))
					note("Een node zonder heartbeat is na 30 s vertraagd en pas na 90 s down; link_down op de interface naar ClusterForge duurt hoogstens 120 s.")
				case ps.Interface == n.vipIface:
					n.dark = true
					keys = append(keys, topo.NodeKey(n.ID))
				case n.addressed[ps.Interface]:
					note(fmt.Sprintf("%s op %s heeft een adres; wat daarover loopt, valt weg, maar de status en de probes zien dat niet.", ps.Interface, n.Hostname))
				default:
					note(fmt.Sprintf("%s op %s heeft geen adres; de voorspelling verwacht daar geen uitval.", ps.Interface, n.Hostname))
				}
			case protocol.FaultVRRPBlock:
				n.vrrp = true
			case protocol.FaultPortBlock:
				n.ports[ps.Port] = true
			case protocol.FaultNetem:
				note("Vertraging en pakketverlies zijn geen uitval; de voorspelling verwacht er geen, de meting toont wat de probes zagen.")
			case protocol.FaultDiskFill:
				n.fill = max(n.fill, ps.FillPercent)
				if float64(ps.FillPercent)/100 < status.DiskFull {
					note(fmt.Sprintf("Een schijf tot %d %% vullen maakt een node niet verminderd; dat begint bij %.0f %%.", ps.FillPercent, status.DiskFull*100))
				}
			case ActServiceStop:
				n.stopped[ps.Unit] = true
			}
		}
		st, reason := simEval(sim, vips, probes, def.Expect.FaultSeconds-ps.AtSeconds, note)
		if rank(st) > rank(worst.Status) {
			worst = Prediction{Status: st, Reason: fmt.Sprintf("na stap %d: %s", i+1, reason)}
		}
	}
	for _, u := range Units {
		if allStopped(sim, u) && slices.ContainsFunc(sim, func(n *simNode) bool { return n.stopped[u] }) {
			keys = append(keys, "unit:"+u)
		}
	}
	return worst, notes, keys
}

// allStopped is true als een unit op elke node waar hij staat, stilstaat
// of niet bereikbaar is.
func allStopped(sim []*simNode, unit string) bool {
	found := false
	for _, n := range sim {
		if _, ok := n.services[unit]; !ok {
			continue
		}
		found = true
		if n.running(unit) && !n.dark && !n.cut {
			return false
		}
	}
	return found
}

// simEval is de status van het cluster na een stap, met de reden. rest is
// hoe lang de storing na deze stap nog duurt.
func simEval(sim []*simNode, vips []string, probes []failover.Probe, rest int, note func(string)) (string, string) {
	var keep, live []*simNode
	for _, n := range sim {
		if _, ok := n.services["keepalived"]; !ok {
			continue
		}
		keep = append(keep, n)
		if n.running("keepalived") && !n.dark {
			live = append(live, n)
		}
	}
	names := func(ns []*simNode) []string {
		out := make([]string, len(ns))
		for i, n := range ns {
			out[i] = n.Hostname
		}
		return out
	}
	hasVIP := len(vips) > 0 && len(keep) > 0
	if hasVIP {
		var blocked []*simNode
		for _, n := range live {
			if n.vrrp {
				blocked = append(blocked, n)
			}
		}
		switch {
		case len(blocked) > 0 && len(live) >= 2:
			if time.Duration(rest)*time.Second < status.VIPGrace+5*time.Second {
				note(fmt.Sprintf("Een VIP op twee nodes telt pas na %s als split-brain; de storing duurt na de VRRP-blokkade nog maar %d s.",
					failover.Seconds(status.VIPGrace), rest))
			}
			note("Bij split-brain antwoordt het VIP nu eens van de ene node en dan van de andere; een afnemer kan haperen.")
			return string(status.SplitBrain), "VRRP geblokkeerd op " + list(names(blocked)) +
				": een node die de andere niet hoort, neemt het VIP er ook bij"
		case len(blocked) > 0:
			note("VRRP blokkeren op " + list(names(blocked)) + ": zonder tweede node met keepalived verandert er niets.")
		}
		if len(live) == 0 {
			return string(status.Down), "keepalived draait op geen enkele bereikbare node, dus " + vips[0] + " heeft geen houder"
		}
		holders := live
		for _, n := range live {
			if n.Owner {
				holders = []*simNode{n}
			}
		}
		for i, pr := range probes {
			port := probePort(pr)
			if !slices.ContainsFunc(holders, func(n *simNode) bool { return !n.ports[port] }) {
				return string(status.Down), fmt.Sprintf("poort %d is dicht op %s, dus %s antwoordt niet", port, list(names(holders)), vips[i])
			}
		}
	}
	for _, n := range sim {
		for port := range n.ports {
			if !slices.ContainsFunc(probes, func(pr failover.Probe) bool { return probePort(pr) == port }) {
				note(fmt.Sprintf("Poort %d dicht op %s: de status en de probes zien dat niet, alleen een afnemer op die poort.", port, n.Hostname))
			}
		}
	}
	for _, u := range Units {
		if allStopped(sim, u) && slices.ContainsFunc(sim, func(n *simNode) bool { return n.stopped[u] }) {
			return string(status.Down), u + " staat op alle nodes stil"
		}
	}
	if !slices.ContainsFunc(sim, func(n *simNode) bool { return !n.cut }) {
		return string(status.Down), "alle nodes zijn afgesneden van ClusterForge"
	}
	var problems []string
	for _, n := range sim {
		if n.cut {
			problems = append(problems, n.Hostname+" is afgesneden van ClusterForge, dus zijn heartbeat blijft uit")
		}
		for _, u := range stopUnits {
			if n.stopped[u] && n.services[u] == "active" {
				problems = append(problems, u+" draait niet op "+n.Hostname)
			}
		}
		if float64(n.fill)/100 >= status.DiskFull {
			problems = append(problems, fmt.Sprintf("de schijf van %s staat op %d %%", n.Hostname, n.fill))
		}
	}
	if len(problems) > 0 {
		return string(status.Degraded), strings.Join(problems, "; ")
	}
	return string(status.Healthy), "geen uitval verwacht"
}

// probePort is de poort die een probe opvraagt.
func probePort(p failover.Probe) int {
	if p.TCP != nil {
		return p.TCP.Port
	}
	return 80
}
