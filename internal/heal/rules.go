// Package heal is Zelfherstel (fase 7): het stelt vast wat er mis is met de
// MariaDB-, MySQL- en PostgreSQL-servers op de nodes, ook als ClusterForge
// ze niet toepaste, en maakt per database een herstelplan. Diagnose is een
// zuivere functie over de groepen van de pagina Databases en wat de agents
// met het verzoek heal inspect zeggen; de taak heal.run voert het plan uit.
package heal

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Hoe erg een probleem is.
const (
	LevelDown    = "down"
	LevelWarning = "warning"
)

// De problemen.
const (
	CodeNoInspect    = "no_inspect"
	CodeUnitStopped  = "unit_stopped"
	CodeProcessHung  = "process_hung"
	CodeOutsideUnit  = "process_outside_unit"
	CodeWrongAddress = "galera_wrong_address"
	CodeNoPrimary    = "galera_no_primary"
	CodeDiskFull     = "disk_full"
	CodeNoQuorum     = "no_quorum"
	CodeUnitDisabled = "unit_disabled"
)

// Wat een plan doet.
const (
	// ActionRestart start de gestopte servers een voor een, na het
	// opruimen van een hangend proces. Bij Galera sluiten ze aan bij de
	// nodes die al Primary zijn.
	ActionRestart = "restart"
	// ActionGalera zet zo nodig de adressen goed en begint het cluster
	// opnieuw op de node met de nieuwste data, als niemand Primary is;
	// anders sluiten de gestopte nodes aan.
	ActionGalera = "galera"
)

// Grenzen.
const (
	// DiskMin is de minste vrije ruimte in de datamap: 1 GiB, of 5 % van
	// het bestandssysteem als dat meer is.
	DiskMin = 1 << 30
	// HungAfter is hoe lang een server mag starten zonder te antwoorden
	// terwijl Galera geen enkele peer bereikt, voor hij als hangend telt.
	HungAfter = 10 * 60
)

// Node is wat Zelfherstel over een node weet buiten de databaseserver.
type Node struct {
	ID        uuid.UUID
	Hostname  string
	Protocol  int
	Online    bool
	PrimaryIP string
	// Addresses zijn de adressen uit de laatste heartbeat.
	Addresses []string
	// Inspect is het antwoord op heal inspect voor de engine van de groep;
	// nil als de agent het niet gaf, en InspectError zegt dan waarom.
	Inspect      *protocol.HealEngine
	InspectError string
}

// Input is één database met de nodes van haar servers.
type Input struct {
	Group databases.Group
	Nodes map[uuid.UUID]Node
	// VIPs zijn alle VIP's; een node die er een heeft, is daar niet zelf op
	// te bereiken.
	VIPs []string
	// Managed is het onderdeel van ClusterForge voor deze database, als
	// ClusterForge haar toepaste.
	Managed *Managed
	// Known zijn de adressen uit de heartbeat van elke node met een agent,
	// met zijn hostname. Een adres in wsrep_cluster_address van een node
	// die (nog) niet bij de groep hoort, is niet verouderd en blijft staan.
	Known map[string]string
}

// Managed is een toegepast onderdeel; het herstel daarvan gaat met de
// acties van het onderdeel zelf.
type Managed struct {
	ComponentID uuid.UUID
	Title       string
}

// Problem is één ding dat mis is.
type Problem struct {
	Code  string
	Level string
	Title string
	Why   string
	Nodes []string
}

// PlanNode is wat het plan met een node doet.
type PlanNode struct {
	NodeID   uuid.UUID
	Hostname string
	Unit     string
	// Start: de server staat stil en start in het plan; Kill: eerst het
	// hangende proces stoppen.
	Start bool
	Kill  bool
	// KillPIDs zijn de processen die Kill stopt.
	KillPIDs []int
	// Primary: de node zit al in een Primary component en blijft draaien.
	Primary bool
	// Addresses: de Galera-adressen gaan van Old naar New, in File.
	SetAddress        bool
	OldClusterAddress string
	ClusterAddress    string
	OldNodeAddress    string
	NodeAddress       string
	File              string
}

// Plan is wat Zelfherstel zou doen.
type Plan struct {
	Action string
	Steps  []string
	Nodes  []PlanNode
	// Auto: het plan mag vanzelf, als Zelfherstel op het cluster vanzelf
	// mag. AutoWhy zegt waarom niet, of waarom wel.
	Auto    bool
	AutoWhy string
	// Blocked zegt waarom het plan nu niet kan; dan is er geen knop.
	Blocked string
	// Managed: het herstel hoort bij het onderdeel van ClusterForge.
	Managed *Managed
	Hash    string
}

// Report is Zelfherstel voor één database.
type Report struct {
	Key         string
	ClusterID   *uuid.UUID
	ClusterName string
	ClusterSlug string
	ClusterEnv  string
	Engine      string
	Kind        string
	Health      string
	Problems    []Problem
	// Plan is nil als er niets te doen is.
	Plan *Plan
	// Last is de laatste herstelpoging van het afgelopen etmaal; Diagnose
	// laat hem leeg.
	Last *LastRun
	// Auto is Vanzelf herstellen van het cluster; nil bij een losse node.
	// Diagnose laat het leeg.
	Auto *AutoState
}

// LastRun is een taak heal.run van een database.
type LastRun struct {
	JobID      uuid.UUID
	Status     string
	Error      string
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// nodeState is wat de regels over één server zeggen.
type nodeState struct {
	srv     databases.Server
	node    Node
	insp    *protocol.HealEngine
	down    bool
	hung    bool
	kill    []int
	outside []int
	primary bool
}

// Diagnose maakt het rapport van één database.
func Diagnose(in Input) Report {
	g := in.Group
	r := Report{
		Key: g.Key(), ClusterID: g.ClusterID, ClusterName: g.ClusterName, ClusterSlug: g.ClusterSlug, ClusterEnv: g.ClusterEnv,
		Engine: g.Engine, Kind: g.Kind, Health: g.Health, Problems: []Problem{},
	}
	add := func(code, level, title, why string, nodes ...string) {
		r.Problems = append(r.Problems, Problem{Code: code, Level: level, Title: title, Why: why, Nodes: nodes})
	}
	name := engineName(g)
	var states []nodeState
	var unknown []string
	for _, s := range g.Servers {
		n, ok := in.Nodes[s.NodeID]
		if !ok {
			n = Node{ID: s.NodeID, Hostname: s.Hostname}
		}
		st := nodeState{srv: s, node: n, insp: n.Inspect}
		st.primary = s.Running && s.Galera != nil && s.Galera.ClusterStatus == "Primary"
		if st.insp == nil {
			unknown = append(unknown, s.Hostname)
			states = append(states, st)
			continue
		}
		e := st.insp
		stopped := e.Active == "failed" || e.Active == "inactive"
		for _, p := range e.Processes {
			switch p.Unit {
			case e.Unit:
				st.kill = append(st.kill, p.PID)
			case "":
				st.outside = append(st.outside, p.PID)
			}
		}
		switch {
		case s.Running:
		case stopped && len(st.kill) > 0:
			st.hung = true
		case stopped:
			st.down = true
		case e.Active == "activating" && e.Galera != nil && len(e.Galera.PeerTimeouts) > 0 && e.StateSeconds >= HungAfter:
			st.hung = true
		}
		states = append(states, st)
	}
	for _, h := range unknown {
		n := in.Nodes[nodeID(g, h)]
		add(CodeNoInspect, LevelWarning, "Zelfherstel kan "+h+" niet bekijken", cmp.Or(n.InspectError, "de agent gaf geen antwoord"), h)
	}
	galera := g.Kind == databases.KindGalera || slices.ContainsFunc(states, func(s nodeState) bool { return s.insp != nil && s.insp.Galera != nil && s.insp.Galera.On })

	// Per node: stilstaand, hangend, buiten systemd, schijf.
	var outside []string
	diskLow := map[string]bool{}
	for _, st := range states {
		if st.insp == nil {
			continue
		}
		h, e := st.srv.Hostname, st.insp
		switch {
		case st.hung && e.Active == "activating":
			add(CodeProcessHung, LevelDown, fmt.Sprintf("%s op %s hangt bij het aansluiten", name, h),
				fmt.Sprintf("%s start al %s, maar bereikt geen enkele andere node (%s).", e.Unit, duration(e.StateSeconds), strings.Join(e.Galera.PeerTimeouts, ", ")), h)
		case st.hung:
			add(CodeProcessHung, LevelDown, fmt.Sprintf("%s op %s hangt", name, h),
				fmt.Sprintf("systemd zette %s %s op %s%s, maar het proces (pid %s) draait nog en antwoordt niet.",
					e.Unit, duration(e.StateSeconds), stateWord(e.Active), result(e.Result), pids(st.kill)), h)
		case st.down:
			add(CodeUnitStopped, LevelDown, fmt.Sprintf("%s staat stil op %s", name, h),
				fmt.Sprintf("%s is %s%s, sinds %s.", e.Unit, stateWord(e.Active), result(e.Result), duration(e.StateSeconds)), h)
		}
		if (st.down || st.hung) && e.Enabled == "disabled" {
			add(CodeUnitDisabled, LevelWarning, fmt.Sprintf("%s start niet mee bij een boot van %s", e.Unit, h),
				fmt.Sprintf("%s staat op disabled. Start een cronjob of script hem, dan ziet ClusterForge dat niet; met systemctl enable %s start hij zelf.", e.Unit, e.Unit), h)
		}
		if len(st.outside) > 0 && !st.srv.Running {
			outside = append(outside, h)
			add(CodeOutsideUnit, LevelWarning, fmt.Sprintf("%s draait op %s buiten systemd", name, h),
				fmt.Sprintf("Proces %s hoort niet bij %s, bijvoorbeeld een --wsrep-recover of een server die met de hand gestart is. ClusterForge laat het met rust en herstelt niets op deze node tot het weg is.",
					pids(st.outside), e.Unit), h)
		}
		if e.DataTotal > 0 && (e.DataFree < DiskMin || e.DataFree*20 < e.DataTotal) {
			diskLow[h] = true
			add(CodeDiskFull, LevelWarning, fmt.Sprintf("De schijf van %s is bijna vol", h),
				fmt.Sprintf("%s heeft nog %s vrij van %s. Een database die niet kan schrijven, stopt; een node die opnieuw aansluit, heeft ruimte nodig voor een volledige kopie. Maak ruimte of vergroot de schijf; ClusterForge gooit niets weg.",
					e.DataDir, bytes(e.DataFree), bytes(e.DataTotal)), h)
		}
	}

	if in.Managed != nil {
		if hasDown(r.Problems) || g.Health == databases.HealthDown {
			r.Plan = finish(r, &Plan{Managed: in.Managed, Steps: []string{
				fmt.Sprintf("Deze database is het onderdeel %s van ClusterForge. Herstel haar met de acties van het onderdeel (Opnieuw starten, Opnieuw synchroniseren of Toepassen), zodat er nooit twee systemen hetzelfde doen.", in.Managed.Title),
			}})
		}
		return r
	}

	var wrong []PlanNode
	var primaries []string
	if galera {
		wrong = wrongAddresses(in, states, add)
		for _, st := range states {
			if st.primary {
				primaries = append(primaries, st.srv.Hostname)
			}
		}
		if len(primaries) == 0 && len(states) > 0 {
			add(CodeNoPrimary, LevelDown, "Geen node zit in een Primary component",
				"Zonder meerderheid neemt Galera geen query's aan: elke app op deze database ligt stil.", hostsOf(states)...)
		}
	}

	var todo []nodeState
	for _, st := range states {
		if st.down || st.hung {
			todo = append(todo, st)
		}
	}
	if len(todo) == 0 && len(wrong) == 0 && (!galera || len(primaries) > 0) {
		return r
	}
	p := &Plan{}
	block := func(why string) {
		if p.Blocked == "" {
			p.Blocked = why
		}
	}
	if len(outside) > 0 {
		block(joinHosts(outside) + " heeft een proces buiten systemd; stop het eerst zelf")
	}
	switch {
	case galera && len(primaries) == 0:
		p.Action = ActionGalera
		if len(unknown) > 0 {
			add(CodeNoQuorum, LevelWarning, "Niet alle nodes zijn te bekijken",
				fmt.Sprintf("Opnieuw beginnen kan alleen vanaf de node met de nieuwste data, en daarvoor moet ClusterForge elke node zien. %s kan nieuwere data hebben: zorg dat de agent er weer antwoordt.", joinHosts(unknown)), unknown...)
			block(joinHosts(unknown) + " is niet te bekijken; zonder alle nodes kan ClusterForge niet zeker weten wie de nieuwste data heeft")
		}
		byHost := map[string]PlanNode{}
		for _, w := range wrong {
			byHost[w.Hostname] = w
		}
		for _, st := range states {
			pn := byHost[st.srv.Hostname]
			pn.NodeID, pn.Hostname, pn.Start = st.srv.NodeID, st.srv.Hostname, true
			if st.hung {
				pn.Kill, pn.KillPIDs = true, st.kill
			}
			if st.insp != nil {
				pn.Unit = st.insp.Unit
			}
			p.Nodes = append(p.Nodes, pn)
		}
		if len(wrong) > 0 {
			p.Steps = append(p.Steps, "Op "+joinHosts(hostsOfPlan(wrong))+" de Galera-adressen bijwerken naar de huidige adressen van de nodes (met een kopie van het bestand).")
		}
		p.Steps = append(p.Steps,
			"MariaDB stoppen op elke node"+killNote(p.Nodes)+", zodat Galera de positie (seqno) van elke node vastlegt.",
			"De posities lezen uit grastate.dat, of na een crash met galera_recovery.",
			"Het cluster beginnen op de node met de hoogste seqno (galera_new_cluster). Zijn de posities onbekend of hebben de nodes een andere geschiedenis, dan stopt de taak zonder te beginnen.",
			"De andere nodes een voor een starten; elke node sluit aan en haalt de ontbrekende data (IST of een volledige kopie), en de taak wacht tot hij Synced is.")
		switch {
		case len(wrong) > 0:
			p.AutoWhy = "Adressen wijzigen gebeurt nooit vanzelf: een fout adres splitst het cluster. Bevestig het plan."
		case len(unknown) > 0:
			p.AutoWhy = "Niet alle nodes zijn te bekijken."
		case len(diskLow) > 0:
			p.AutoWhy = "Een node heeft te weinig vrije ruimte voor een volledige kopie."
		default:
			p.Auto = true
			p.AutoWhy = "Alle nodes zijn te bekijken; de taak begint alleen op de node met de nieuwste data."
		}
	default:
		if galera {
			p.Action = ActionGalera
		} else {
			p.Action = ActionRestart
		}
		p.Nodes = append(p.Nodes, wrong...)
		for _, st := range states {
			i := slices.IndexFunc(p.Nodes, func(n PlanNode) bool { return n.NodeID == st.srv.NodeID })
			if i < 0 {
				if !st.down && !st.hung && !st.primary {
					continue
				}
				p.Nodes = append(p.Nodes, PlanNode{NodeID: st.srv.NodeID, Hostname: st.srv.Hostname})
				i = len(p.Nodes) - 1
			}
			pn := &p.Nodes[i]
			pn.Primary = st.primary
			if st.insp != nil {
				pn.Unit = st.insp.Unit
			}
			if st.down || st.hung {
				pn.Start = true
				if st.hung {
					pn.Kill, pn.KillPIDs = true, st.kill
				}
			}
		}
		if len(wrong) > 0 {
			p.Steps = append(p.Steps, "Op "+joinHosts(hostsOfPlan(wrong))+" de Galera-adressen bijwerken naar de huidige adressen van de nodes (met een kopie van het bestand). Een node die draait, leest ze bij zijn volgende start.")
		}
		var starts []string
		for _, n := range p.Nodes {
			if n.Start {
				starts = append(starts, n.Hostname)
			}
		}
		if len(starts) > 0 {
			step := "Op " + joinHosts(starts) + killNote(p.Nodes) + " " + name + " een voor een starten"
			if galera {
				step += " en laten aansluiten bij " + joinHosts(primaries) + "; de taak wacht telkens tot de node Synced is"
			} else {
				step += " en wachten tot hij antwoordt"
			}
			p.Steps = append(p.Steps, step+".")
		}
		switch {
		case len(wrong) > 0:
			p.AutoWhy = "Adressen wijzigen gebeurt nooit vanzelf: een fout adres splitst het cluster. Bevestig het plan."
		case anyDisk(diskLow, starts):
			p.AutoWhy = "Een node heeft te weinig vrije ruimte; starten zou meteen weer stoppen."
		default:
			p.Auto = true
			p.AutoWhy = "Alleen starten van wat stilstaat; er gaat geen data verloren."
		}
	}
	if p.Blocked != "" {
		p.Auto = false
	}
	r.Plan = finish(r, p)
	return r
}

// wrongAddresses zoekt nodes waarvan de Galera-adressen niet kloppen met de
// adressen van de nodes, en geeft voor elke node met een fout het nieuwe
// adres.
func wrongAddresses(in Input, states []nodeState, add func(code, level, title, why string, nodes ...string)) []PlanNode {
	all := map[string]bool{}
	for _, st := range states {
		for _, a := range st.node.Addresses {
			all[a] = true
		}
	}
	vips := map[string]bool{}
	for _, v := range in.VIPs {
		vips[v] = true
	}
	// Het adres van elke node voor Galera.
	addr := map[string]string{}
	for _, st := range states {
		addr[st.srv.Hostname] = nodeAddress(st.node, vips)
	}
	var want []string
	for _, st := range states {
		if a := addr[st.srv.Hostname]; a != "" {
			want = append(want, a)
		}
	}
	complete := len(want) == len(states)
	// Een andere node van ClusterForge in de config, zoals een node waarvan
	// de agent zijn MariaDB nog niet meldde, blijft erin.
	other := func(x string) bool {
		ip, err := netip.ParseAddr(x)
		return err == nil && !all[ip.String()] && !vips[ip.String()] && in.Known[ip.String()] != ""
	}
	for _, st := range states {
		if st.insp == nil || st.insp.Galera == nil {
			continue
		}
		for _, x := range gcommHosts(st.insp.Galera.ClusterAddress) {
			if other(x) && !slices.Contains(want, x) {
				want = append(want, x)
			}
		}
	}
	newCluster := "gcomm://" + strings.Join(want, ",")
	var out []PlanNode
	for _, st := range states {
		if st.insp == nil || st.insp.Galera == nil || st.insp.Galera.ClusterAddress == "" {
			continue
		}
		g := st.insp.Galera
		h := st.srv.Hostname
		hosts := gcommHosts(g.ClusterAddress)
		var stale, missing []string
		for _, x := range hosts {
			if ip, err := netip.ParseAddr(x); err == nil && !all[ip.String()] && !vips[ip.String()] && !other(x) {
				stale = append(stale, x)
			}
		}
		for _, o := range states {
			if len(o.node.Addresses) == 0 {
				continue
			}
			if !slices.ContainsFunc(hosts, func(x string) bool {
				return slices.Contains(o.node.Addresses, x) || strings.EqualFold(x, o.srv.Hostname)
			}) {
				missing = append(missing, o.srv.Hostname)
			}
		}
		nodeWrong := ""
		if na := hostOnly(g.NodeAddress); na != "" {
			if ip, err := netip.ParseAddr(na); err == nil && len(st.node.Addresses) > 0 && !slices.Contains(st.node.Addresses, ip.String()) {
				nodeWrong = na
			}
		}
		if len(stale) == 0 && len(missing) == 0 && nodeWrong == "" {
			continue
		}
		var why []string
		if len(stale) > 0 {
			what := "dat adres heeft"
			if len(stale) > 1 {
				what = "die adressen heeft"
			}
			why = append(why, fmt.Sprintf("wsrep_cluster_address noemt %s; %s geen enkele node van het cluster", strings.Join(stale, ", "), what))
		}
		if len(missing) > 0 {
			why = append(why, fmt.Sprintf("%s staat er niet in", joinHosts(missing)))
		}
		if nodeWrong != "" {
			why = append(why, fmt.Sprintf("wsrep_node_address is %s, maar %s heeft dat adres niet", nodeWrong, h))
		}
		if len(g.PeerTimeouts) > 0 {
			why = append(why, "Galera probeert sinds de laatste start "+strings.Join(g.PeerTimeouts, ", ")+" en krijgt geen antwoord")
		}
		text := upper(strings.Join(why, "; ")) + "."
		if complete {
			text += " De nodes hebben nu " + strings.Join(want, ", ") + "."
		}
		add(CodeWrongAddress, LevelDown, "Galera zoekt de andere nodes op verkeerde adressen", text, h)
		if !complete {
			continue
		}
		pn := PlanNode{NodeID: st.srv.NodeID, Hostname: h, SetAddress: true, OldClusterAddress: g.ClusterAddress, ClusterAddress: newCluster, OldNodeAddress: g.NodeAddress}
		if g.NodeAddress != "" {
			pn.NodeAddress = addr[h]
		}
		for _, s := range g.Settings {
			if s.Key == "wsrep_cluster_address" {
				pn.File = s.File
			}
		}
		if pn.ClusterAddress == pn.OldClusterAddress && pn.NodeAddress == pn.OldNodeAddress {
			continue
		}
		out = append(out, pn)
	}
	return out
}

// nodeAddress kiest het adres van een node voor Galera: zijn vaste adres
// als hij dat nog heeft, anders zijn eerste IPv4 dat geen VIP is.
func nodeAddress(n Node, vips map[string]bool) string {
	if n.PrimaryIP != "" && slices.Contains(n.Addresses, n.PrimaryIP) && !vips[n.PrimaryIP] {
		return n.PrimaryIP
	}
	for _, a := range n.Addresses {
		ip, err := netip.ParseAddr(a)
		if err == nil && ip.Is4() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !vips[a] {
			return a
		}
	}
	return ""
}

// gcommHosts haalt de hosts uit gcomm://a,b:4567,[c].
func gcommHosts(s string) []string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "gcomm://")
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if h := hostOnly(part); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func hostOnly(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		if i := strings.IndexByte(s, ']'); i > 0 {
			return s[1:i]
		}
	}
	if strings.Count(s, ":") == 1 {
		s, _, _ = strings.Cut(s, ":")
	}
	return s
}

func finish(r Report, p *Plan) *Plan {
	v := struct {
		Key     string
		Action  string
		Nodes   []PlanNode
		Blocked string
		Managed *Managed
	}{r.Key, p.Action, p.Nodes, p.Blocked, p.Managed}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	p.Hash = hex.EncodeToString(sum[:])
	if p.Steps == nil {
		p.Steps = []string{}
	}
	if p.Nodes == nil {
		p.Nodes = []PlanNode{}
	}
	return p
}

func hasDown(ps []Problem) bool {
	return slices.ContainsFunc(ps, func(p Problem) bool { return p.Level == LevelDown })
}

func anyDisk(low map[string]bool, hosts []string) bool {
	return slices.ContainsFunc(hosts, func(h string) bool { return low[h] })
}

func nodeID(g databases.Group, host string) uuid.UUID {
	for _, s := range g.Servers {
		if s.Hostname == host {
			return s.NodeID
		}
	}
	return uuid.Nil
}

func engineName(g databases.Group) string {
	if g.Engine == protocol.DBMariaDB {
		for _, s := range g.Servers {
			if s.Unit == "mysql" && !strings.Contains(strings.ToLower(s.Version), "mariadb") {
				return "MySQL"
			}
		}
		return "MariaDB"
	}
	for _, s := range g.Servers {
		if s.Unit == "patroni" {
			return "Patroni"
		}
	}
	return databases.EngineName(g.Engine)
}

func killNote(nodes []PlanNode) string {
	var hosts []string
	for _, n := range nodes {
		if n.Kill {
			hosts = append(hosts, fmt.Sprintf("%s (pid %s)", n.Hostname, pids(n.KillPIDs)))
		}
	}
	if len(hosts) == 0 {
		return ""
	}
	return ", na het stoppen van het hangende proces op " + strings.Join(hosts, " en ") + " (TERM, na een minuut KILL)"
}

func hostsOf(states []nodeState) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = s.srv.Hostname
	}
	return out
}

func hostsOfPlan(nodes []PlanNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Hostname
	}
	return out
}

func joinHosts(h []string) string {
	switch len(h) {
	case 0:
		return ""
	case 1:
		return h[0]
	}
	return strings.Join(h[:len(h)-1], ", ") + " en " + h[len(h)-1]
}

func pids(p []int) string {
	s := make([]string, len(p))
	for i, x := range p {
		s[i] = fmt.Sprint(x)
	}
	return strings.Join(s, ", ")
}

func stateWord(active string) string {
	switch active {
	case "failed":
		return "failed"
	case "inactive":
		return "gestopt"
	}
	return active
}

func result(r string) string {
	if r == "" || r == "success" {
		return ""
	}
	return " (" + r + ")"
}

func duration(sec int64) string {
	switch {
	case sec <= 0:
		return "kort"
	case sec < 120:
		return fmt.Sprintf("%d s", sec)
	case sec < 2*3600:
		return fmt.Sprintf("%d min", sec/60)
	case sec < 2*86400:
		return fmt.Sprintf("%d uur", sec/3600)
	}
	return fmt.Sprintf("%d dagen", sec/86400)
}

func bytes(n uint64) string {
	const gib = 1 << 30
	if n >= gib {
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	}
	return fmt.Sprintf("%d MiB", n>>20)
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
