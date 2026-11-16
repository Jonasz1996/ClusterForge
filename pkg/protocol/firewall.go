package protocol

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// FirewallSince is de eerste protocolversie die ufw beheert (fase 16).
const FirewallSince = 30

// SubjectFirewall draagt de firewallverzoeken: cf.node.<id>.firewall.
const SubjectFirewall = "firewall"

const (
	TypeFirewallRequest = "firewall_request"
	TypeFirewallResult  = "firewall_result"
)

// Soorten firewallverzoek.
const (
	// FirewallOpStatus geeft ufw, de regels, de luisterende poorten en de
	// containers. Werkt ook zonder ufw.
	FirewallOpStatus = "status"
	// FirewallOpApply zet de regels, met een terugdraaitimer.
	FirewallOpApply = "apply"
	// FirewallOpConfirm bevestigt een toepassing; de timer stopt.
	FirewallOpConfirm = "confirm"
	// FirewallOpRollback zet de regels van vóór een toepassing meteen terug.
	FirewallOpRollback = "rollback"
	// FirewallOpDisable schakelt ufw uit.
	FirewallOpDisable = "disable"
	// FirewallOpProbe probeert TCP-verbindingen naar andere nodes.
	FirewallOpProbe = "probe"
	// FirewallOpBlocked geeft het verkeer dat ufw weggooide sinds de
	// vorige keer en zet de tellers op nul.
	FirewallOpBlocked = "blocked"
)

// FirewallRequestTimeout is hoe lang een firewallverzoek mag duren; een
// apply kan ufw eerst installeren.
const FirewallRequestTimeout = 3 * time.Minute

// FirewallNflogGroup is de NFLOG-groep waarop de agent het geblokkeerde
// verkeer telt.
const FirewallNflogGroup = 4711

// FirewallDir houdt de kopie van ufw van vóór een toepassing, het
// terugdraaiscript en de openstaande toepassing.
const FirewallDir = "/var/lib/clusterforge/firewall"

// FirewallStateFile is wat de agent onthoudt van de laatste bevestigde
// toepassing.
const FirewallStateFile = "/var/lib/clusterforge/firewall.json"

// FirewallRollbackUnit is de systemd-timer die terugdraait als niemand
// bevestigt.
const FirewallRollbackUnit = "cf-firewall-rollback"

// FirewallCommentPrefix staat voor het commentaar van elke regel van
// ClusterForge.
const FirewallCommentPrefix = "cf:"

// Grenzen.
const (
	MaxFirewallRules  = 600
	MaxFirewallProbes = 64
	MaxFirewallHits   = 5000
	// MinFirewallConfirm en MaxFirewallConfirm begrenzen de wachttijd.
	MinFirewallConfirm = 30
	MaxFirewallConfirm = 900
)

type FirewallRequest struct {
	ID       string         `json:"id"`
	Op       string         `json:"op"`
	Deadline time.Time      `json:"deadline"`
	Apply    *FirewallApply `json:"apply,omitempty"`
	// ApplyID hoort bij confirm en rollback.
	ApplyID string `json:"apply_id,omitempty"`
	// Probe zijn host:poort-paren (TCP) voor probe.
	Probe []string `json:"probe,omitempty"`
}

// FirewallApply is wat de server op een node wil.
type FirewallApply struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
	// Rules zijn alle regels die er na de toepassing moeten staan (alleen
	// allow, inkomend); de agent rekent zelf het verschil uit.
	Rules []FirewallRule `json:"rules"`
	// Strict haalt ook regels weg die niet van ClusterForge zijn.
	Strict bool `json:"strict,omitempty"`
	// Install installeert ufw als het ontbreekt.
	Install bool `json:"install,omitempty"`
	// ConfirmSeconds is hoe lang de agent op confirm wacht.
	ConfirmSeconds int `json:"confirm_seconds"`
	// Monitor zet de NFLOG-regel in after.rules (als de agent NFLOG kan
	// lezen).
	Monitor bool `json:"monitor,omitempty"`
}

type FirewallResponse struct {
	Error   string               `json:"error,omitempty"`
	Status  *FirewallStatus      `json:"status,omitempty"`
	Apply   *FirewallApplyResult `json:"apply,omitempty"`
	Probe   []FirewallProbe      `json:"probe,omitempty"`
	Blocked *FirewallBlocked     `json:"blocked,omitempty"`
}

// FirewallRule is een ufw-regel. Leeg Port is elke poort; From en To zijn
// "any" of een adres of netwerk.
type FirewallRule struct {
	// Action is allow, deny, reject of limit.
	Action string `json:"action"`
	// Proto is tcp, udp, vrrp, esp, ah, gre, igmp of any.
	Proto string `json:"proto"`
	Port  string `json:"port,omitempty"`
	From  string `json:"from"`
	To    string `json:"to,omitempty"`
	SPort string `json:"sport,omitempty"`
	// Direction is in of out; leeg is in.
	Direction string `json:"direction,omitempty"`
	Iface     string `json:"iface,omitempty"`
	// App is een ufw-applicatieprofiel (alleen bij gelezen regels); zo'n
	// regel haalt ClusterForge nooit weg.
	App     string `json:"app,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// Owned: de regel is van ClusterForge.
func (r FirewallRule) Owned() bool { return strings.HasPrefix(r.Comment, FirewallCommentPrefix) }

func normAddr(s string) string {
	switch s {
	case "", "any", "0.0.0.0/0", "::/0":
		return "any"
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		if p.Bits() == p.Addr().BitLen() {
			return p.Addr().String()
		}
		return p.Masked().String()
	}
	return s
}

func normPort(s string) string {
	if s == "any" {
		return ""
	}
	return s
}

// Norm geeft de regel in de vorm waarin ze vergeleken wordt.
func (r FirewallRule) Norm() FirewallRule {
	r.From, r.To = normAddr(r.From), normAddr(r.To)
	r.Port, r.SPort = normPort(r.Port), normPort(r.SPort)
	if r.Direction == "" {
		r.Direction = "in"
	}
	if r.Proto == "" {
		r.Proto = "any"
	}
	return r
}

// Key is wat een regel uniek maakt voor ufw; het commentaar telt niet.
func (r FirewallRule) Key() string {
	n := r.Norm()
	return strings.Join([]string{n.Action, n.Proto, n.Port, n.From, n.To, n.SPort, n.Direction, n.Iface, n.App}, "|")
}

// String is de regel zoals ufw ze schrijft, zonder "ufw".
func (r FirewallRule) String() string {
	s := strings.Join(r.Args(), " ")
	if r.Comment != "" {
		s += " comment '" + r.Comment + "'"
	}
	return s
}

// Args zijn de argumenten van ufw voor de regel, zonder commentaar.
func (r FirewallRule) Args() []string {
	n := r.Norm()
	args := []string{n.Action}
	if n.Direction == "out" || n.Iface != "" {
		args = append(args, n.Direction)
		if n.Iface != "" {
			args = append(args, "on", n.Iface)
		}
	}
	if n.Proto != "any" {
		args = append(args, "proto", n.Proto)
	}
	args = append(args, "from", n.From)
	if n.SPort != "" {
		args = append(args, "port", n.SPort)
	}
	args = append(args, "to", n.To)
	if n.Port != "" {
		args = append(args, "port", n.Port)
	}
	return args
}

var (
	firewallPort    = regexp.MustCompile(`^[0-9]{1,5}(:[0-9]{1,5})?$`)
	firewallComment = regexp.MustCompile(`^cf:[A-Za-z0-9 ._/:+()-]{0,80}$`)
)

// ValidPort controleert een poort of reeks (2379:2380).
func ValidPort(p string) error {
	if !firewallPort.MatchString(p) {
		return fmt.Errorf("ongeldige poort %q", p)
	}
	lo, hi, rng := strings.Cut(p, ":")
	a, _ := strconv.Atoi(lo)
	b := a
	if rng {
		b, _ = strconv.Atoi(hi)
	}
	if a < 1 || b > 65535 || b < a {
		return fmt.Errorf("ongeldige poort %q", p)
	}
	return nil
}

// ValidSource controleert een bron: any, een adres of een netwerk.
func ValidSource(s string) error {
	if s == "any" {
		return nil
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		return nil
	}
	if _, err := netip.ParsePrefix(s); err == nil {
		return nil
	}
	return fmt.Errorf("ongeldige bron %q", s)
}

// Validate controleert een regel die de server zet: alleen allow, inkomend,
// tcp/udp met een poort of vrrp zonder. De waarden gaan als argumenten naar
// ufw.
func (r FirewallRule) Validate() error {
	if r.Action != "allow" {
		return fmt.Errorf("alleen allow-regels, niet %q", r.Action)
	}
	if r.Direction != "" && r.Direction != "in" || r.Iface != "" || r.SPort != "" || r.App != "" || normAddr(r.To) != "any" {
		return fmt.Errorf("alleen inkomende regels naar elk adres")
	}
	switch r.Proto {
	case "tcp", "udp":
		if err := ValidPort(r.Port); err != nil {
			return err
		}
	case "vrrp":
		if r.Port != "" {
			return fmt.Errorf("vrrp heeft geen poort")
		}
	default:
		return fmt.Errorf("onbekend protocol %q", r.Proto)
	}
	if err := ValidSource(r.From); err != nil {
		return err
	}
	if !firewallComment.MatchString(r.Comment) {
		return fmt.Errorf("ongeldig commentaar %q (begint met cf:)", r.Comment)
	}
	return nil
}

// FirewallDiff is wat een toepassing verandert.
type FirewallDiff struct {
	// Add zijn nieuwe regels.
	Add []FirewallRule `json:"add"`
	// Adopt zijn regels die er al staan (zonder of met ander commentaar) en
	// die ClusterForge overneemt.
	Adopt []FirewallRule `json:"adopt"`
	// Remove zijn regels die weggaan.
	Remove []FirewallRule `json:"remove"`
	// Keep zijn regels van ClusterForge die blijven.
	Keep []FirewallRule `json:"keep"`
	// Foreign zijn regels die niet van ClusterForge zijn en blijven.
	Foreign []FirewallRule `json:"foreign"`
}

// Empty: de regels kloppen al.
func (d FirewallDiff) Empty() bool { return len(d.Add)+len(d.Adopt)+len(d.Remove) == 0 }

// DiffFirewall vergelijkt de regels op de node met de gewenste. Met strict
// gaan ook vreemde regels weg (behalve regels met een applicatieprofiel).
func DiffFirewall(current, desired []FirewallRule, strict bool) FirewallDiff {
	d := FirewallDiff{Add: []FirewallRule{}, Adopt: []FirewallRule{}, Remove: []FirewallRule{}, Keep: []FirewallRule{}, Foreign: []FirewallRule{}}
	cur := map[string]FirewallRule{}
	var order []string
	for _, r := range current {
		k := r.Key()
		if _, ok := cur[k]; !ok {
			order = append(order, k)
			cur[k] = r
		}
	}
	want := map[string]bool{}
	for _, r := range desired {
		k := r.Key()
		if want[k] {
			continue
		}
		want[k] = true
		c, ok := cur[k]
		switch {
		case !ok:
			d.Add = append(d.Add, r)
		case c.Comment != r.Comment:
			d.Adopt = append(d.Adopt, r)
		default:
			d.Keep = append(d.Keep, r)
		}
	}
	for _, k := range order {
		if want[k] {
			continue
		}
		c := cur[k]
		if c.Owned() || strict && c.App == "" {
			d.Remove = append(d.Remove, c)
		} else {
			d.Foreign = append(d.Foreign, c)
		}
	}
	return d
}

// ParseUfwTuples leest de "### tuple ###"-regels uit user.rules of
// user6.rules. Een regel voor any staat in beide bestanden; ze komt één keer
// terug.
func ParseUfwTuples(files ...string) []FirewallRule {
	var out []FirewallRule
	seen := map[string]bool{}
	for _, f := range files {
		for line := range strings.SplitSeq(f, "\n") {
			rest, ok := strings.CutPrefix(strings.TrimSpace(line), "### tuple ###")
			if !ok {
				continue
			}
			r, ok := parseTuple(strings.TrimSpace(rest))
			if !ok {
				continue
			}
			k := r.Key()
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// parseTuple leest "allow tcp 22 0.0.0.0/0 any 192.168.0.0/16 in
// comment=6366…": actie, protocol, doelpoort, doel, bronpoort, bron, [dapp
// sapp], richting[_iface].
func parseTuple(s string) (FirewallRule, bool) {
	var comment string
	if i := strings.Index(s, " comment="); i >= 0 {
		comment = decodeHex(s[i+len(" comment="):])
		s = s[:i]
	}
	f := strings.Fields(s)
	if len(f) != 7 && len(f) != 9 {
		return FirewallRule{}, false
	}
	action, _, _ := strings.Cut(f[0], "_") // allow_log → allow
	r := FirewallRule{Action: action, Proto: f[1], Port: f[2], To: f[3], SPort: f[4], From: f[5], Comment: comment}
	dir := f[len(f)-1]
	if len(f) == 9 {
		apps := slices.DeleteFunc([]string{f[6], f[7]}, func(a string) bool { return a == "-" })
		r.App = strings.Join(apps, ",")
	}
	r.Direction, r.Iface, _ = strings.Cut(dir, "_")
	if r.Direction != "in" && r.Direction != "out" {
		return FirewallRule{}, false
	}
	return r.Norm(), true
}

func decodeHex(s string) string {
	s = strings.TrimSpace(s)
	if len(s)%2 != 0 {
		return s
	}
	b := make([]byte, 0, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		v, err := strconv.ParseUint(s[i:i+2], 16, 8)
		if err != nil {
			return s
		}
		b = append(b, byte(v))
	}
	return string(b)
}

// FirewallStatus is de stand van ufw en de poorten op een node.
type FirewallStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Active    bool   `json:"active"`
	// Incoming, Outgoing en Routed zijn het standaardbeleid (deny, allow,
	// reject of disabled).
	Incoming string         `json:"incoming,omitempty"`
	Outgoing string         `json:"outgoing,omitempty"`
	Routed   string         `json:"routed,omitempty"`
	Logging  string         `json:"logging,omitempty"`
	Rules    []FirewallRule `json:"rules"`
	// Listeners zijn de poorten waarop iets luistert.
	Listeners  []FirewallListener  `json:"listeners"`
	Containers []FirewallContainer `json:"containers,omitempty"`
	// Monitor is hoe de agent geblokkeerd verkeer telt: nflog, kernel-log
	// of off; MonitorError zegt waarom nflog niet lukt.
	Monitor      string `json:"monitor"`
	MonitorError string `json:"monitor_error,omitempty"`
	// MonitorRule: de NFLOG-regel van ClusterForge staat in after.rules.
	MonitorRule bool                `json:"monitor_rule"`
	Pending     *FirewallPending    `json:"pending,omitempty"`
	Applied     *FirewallApplied    `json:"applied,omitempty"`
	Errors      []string            `json:"errors,omitempty"`
	RolledBack  *FirewallRolledBack `json:"rolled_back,omitempty"`
}

// FirewallListener is een luisterende poort met het proces erachter.
type FirewallListener struct {
	// Proto is tcp of udp.
	Proto   string `json:"proto"`
	Addr    string `json:"addr"`
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
	Unit    string `json:"unit,omitempty"`
	// Container en Image zijn er als het proces in een container draait
	// (zoals network_mode: host).
	Container string `json:"container,omitempty"`
	Image     string `json:"image,omitempty"`
}

// Local: luistert alleen op loopback.
func (l FirewallListener) Local() bool {
	a, err := netip.ParseAddr(l.Addr)
	return err == nil && a.Unmap().IsLoopback()
}

// FirewallContainer is een draaiende container met zijn gepubliceerde
// poorten.
type FirewallContainer struct {
	Name    string              `json:"name"`
	Image   string              `json:"image"`
	Network string              `json:"network,omitempty"`
	Ports   []FirewallPublished `json:"ports,omitempty"`
}

type FirewallPublished struct {
	Proto         string `json:"proto"`
	HostIP        string `json:"host_ip,omitempty"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
}

// FirewallPending is een toepassing die nog bevestigd moet worden.
type FirewallPending struct {
	ApplyID  string    `json:"apply_id"`
	Hash     string    `json:"hash"`
	Deadline time.Time `json:"deadline"`
	// WasActive: ufw stond aan vóór de toepassing.
	WasActive bool `json:"was_active"`
}

// FirewallApplied is wat in FirewallStateFile staat.
type FirewallApplied struct {
	ApplyID   string    `json:"apply_id"`
	Hash      string    `json:"hash"`
	AppliedAt time.Time `json:"applied_at"`
}

// FirewallRolledBack: de laatste toepassing werd teruggedraaid.
type FirewallRolledBack struct {
	ApplyID string    `json:"apply_id"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason,omitempty"`
}

type FirewallApplyResult struct {
	ApplyID string `json:"apply_id"`
	// Changed is false als er niets te doen was (dan is er ook geen timer
	// en geen confirm nodig).
	Changed  bool         `json:"changed"`
	Deadline time.Time    `json:"deadline,omitzero"`
	Diff     FirewallDiff `json:"diff"`
	Log      []string     `json:"log"`
}

// FirewallProbe is het resultaat van één verbinding.
type FirewallProbe struct {
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	// Refused: de poort weigert (er luistert niets of een reject); bij een
	// time-out gooit een firewall het pakket weg.
	Refused bool   `json:"refused,omitempty"`
	Error   string `json:"error,omitempty"`
	Millis  int    `json:"millis"`
}

// FirewallBlocked is het weggegooide verkeer sinds de vorige keer.
type FirewallBlocked struct {
	Source string        `json:"source"`
	Since  time.Time     `json:"since"`
	Hits   []FirewallHit `json:"hits"`
	// Dropped is het aantal pakketten dat niet meer in de tabel paste.
	Dropped int64 `json:"dropped,omitempty"`
}

// FirewallHit zijn de pakketten van één bron naar één poort.
type FirewallHit struct {
	Src   string `json:"src"`
	Proto string `json:"proto"`
	// Port is de doelpoort; 0 voor protocollen zonder poort.
	Port  int       `json:"port"`
	Count int64     `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}
