// Package protocol bevat de berichten tussen clusterforge-server en cf-agent.
// Server en agent gebruiken dezelfde structs; een wijziging hier raakt beide.
package protocol

import (
	"encoding/json"
	"strings"
	"time"
)

// Version is de protocolversie. Een agent meldt die bij elke heartbeat, zodat
// de server geen berichten stuurt die de agent niet kent.
const Version = 4

// CommandsSince is de eerste protocolversie met commando's.
const CommandsSince = 2

// ApplySince is de eerste protocolversie die deploystappen uitvoert.
const ApplySince = 3

// InspectSince is de eerste protocolversie die state.inspect kent. Een
// oudere agent weigert een onbekend commando, maar leest velden die hij niet
// kent zonder fout; daarom is inspecteren een eigen commando en geen vlag op
// apply.steps.
const InspectSince = 4

// Berichttypes.
const (
	TypeHeartbeat = "heartbeat"
	TypeFacts     = "facts"
	TypeMetrics   = "metrics"
	TypeAck       = "ack"
	TypeCommand   = "command"
	TypeResult    = "command_result"
)

// Envelope is de buitenkant van elk bericht.
type Envelope struct {
	V    int             `json:"v"`
	ID   string          `json:"id"`
	Type string          `json:"type"`
	TS   time.Time       `json:"ts"`
	Body json.RawMessage `json:"body"`
}

// Subjects per node. De agent mag alleen publiceren onder NodePrefix en
// alleen luisteren op zijn eigen inbox.
const (
	SubjectHeartbeat = "hb"
	SubjectFacts     = "facts"
	SubjectMetrics   = "metrics"
	SubjectEvents    = "events"
	SubjectCommands  = "cmd"
)

func NodePrefix(nodeID string) string { return "cf.node." + nodeID }

func Subject(nodeID, kind string) string { return NodePrefix(nodeID) + "." + kind }

// InboxPrefix is het inboxprefix van een agent; replies van de server komen
// daaronder binnen.
func InboxPrefix(nodeID string) string { return "_INBOX." + nodeID }

// ParseSubject haalt node-id en soort uit een subject als cf.node.<id>.hb.
func ParseSubject(subject string) (nodeID, kind string, ok bool) {
	rest, found := strings.CutPrefix(subject, "cf.node.")
	if !found {
		return "", "", false
	}
	nodeID, kind, ok = strings.Cut(rest, ".")
	return nodeID, kind, ok && nodeID != "" && kind != ""
}

// Heartbeat stuurt de agent elke HeartbeatInterval.
type Heartbeat struct {
	AgentVersion    string     `json:"agent_version"`
	ProtocolVersion int        `json:"protocol_version"`
	UptimeSeconds   int64      `json:"uptime_seconds"`
	Load            [3]float64 `json:"load"`
	// Addresses zijn alle IP-adressen op de interfaces, zonder prefix. Daaruit
	// leidt de server af welke node een VIP heeft.
	Addresses []string `json:"addresses"`
	// Services is de toestand (systemctl is-active) van de gevolgde units.
	Services map[string]string `json:"services"`
}

const (
	HeartbeatInterval = 10 * time.Second
	MetricsInterval   = 15 * time.Second
	FactsInterval     = 15 * time.Minute
)

// Metrics is een batch metingen. De namen volgen node_exporter, zodat
// bestaande Grafana-dashboards werken. De server voegt de labels voor node,
// cluster en omgeving toe en gebruikt zijn eigen klok als tijdstip.
type Metrics struct {
	Samples []Sample `json:"samples"`
}

type Sample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
}

// MaxSamples is het grootste aantal metingen dat de server per batch aanneemt.
const MaxSamples = 5000

// Facts beschrijven de node. De agent stuurt ze bij start, na een herverbinding
// en elke FactsInterval; de server bewaart alleen een gewijzigde set.
type Facts struct {
	Hostname       string       `json:"hostname"`
	MachineID      string       `json:"machine_id"`
	OS             OSInfo       `json:"os"`
	Kernel         string       `json:"kernel"`
	Arch           string       `json:"arch"`
	Virtualization string       `json:"virtualization"`
	CPUs           int          `json:"cpus"`
	MemoryBytes    uint64       `json:"memory_bytes"`
	SwapBytes      uint64       `json:"swap_bytes"`
	BootTime       time.Time    `json:"boot_time"`
	PrimaryAddress string       `json:"primary_address"`
	Interfaces     []Interface  `json:"interfaces"`
	Filesystems    []Filesystem `json:"filesystems"`
	Services       []Service    `json:"services"`
	Docker         *Docker      `json:"docker"`
	Keepalived     *Keepalived  `json:"keepalived"`
	Upgrades       *Upgrades    `json:"upgrades"`
}

type OSInfo struct {
	ID         string `json:"id"`
	VersionID  string `json:"version_id"`
	Codename   string `json:"codename"`
	PrettyName string `json:"pretty_name"`
}

type Interface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	Up        bool     `json:"up"`
	Addresses []string `json:"addresses"`
}

type Filesystem struct {
	Mount     string `json:"mount"`
	Device    string `json:"device"`
	Type      string `json:"type"`
	SizeBytes uint64 `json:"size_bytes"`
	UsedBytes uint64 `json:"used_bytes"`
}

type Service struct {
	Name    string `json:"name"`
	Active  string `json:"active"`
	Enabled string `json:"enabled"`
}

type Docker struct {
	Version    string `json:"version"`
	Containers int    `json:"containers"`
}

// Keepalived beschrijft wat er in keepalived.conf staat.
type Keepalived struct {
	Active string   `json:"active"`
	VIPs   []string `json:"vips"`
}

type Upgrades struct {
	Total    int      `json:"total"`
	Security int      `json:"security"`
	Packages []string `json:"packages"`
}

// Commando's die de server een agent kan sturen. De agent voert alleen deze
// uit; er is geen vrije shell.
const (
	// CmdFactsCollect verzamelt de facts meteen en stuurt ze naar de server.
	CmdFactsCollect = "facts.collect"
	// CmdReboot en CmdShutdown herstarten of stoppen de machine na
	// DelaySeconds, zodat het antwoord nog de deur uit kan.
	CmdReboot   = "system.reboot"
	CmdShutdown = "system.shutdown"
	// CmdMaintenanceEnter zet keepalived uit (stop en disable), zodat de VIP's
	// naar de andere nodes gaan en na een reboot niet terugkomen. De agent
	// onthoudt hoe keepalived stond.
	CmdMaintenanceEnter = "node.maintenance.enter"
	// CmdMaintenanceExit zet keepalived terug zoals het voor het onderhoud
	// stond.
	CmdMaintenanceExit = "node.maintenance.exit"
	// CmdApply voert deploystappen uit, in volgorde, en stopt bij de eerste
	// die mislukt.
	CmdApply = "apply.steps"
	// CmdInspect leest hoe de stappen in Inspect erbij staan, zonder iets te
	// veranderen: dpkg-query, systemctl show, id en stat, nooit apt-get, een
	// ander systemctl-werkwoord of sh.
	CmdInspect = "state.inspect"
)

// Command is een opdracht van de server. De server stuurt het als request op
// cf.node.<id>.cmd; de agent antwoordt met een Result. Een commando dat
// opnieuw gestuurd wordt, houdt hetzelfde ID; de agent voert het dan niet
// nog eens uit.
type Command struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	// Deadline: na dit tijdstip weigert de agent het commando.
	Deadline     time.Time `json:"deadline"`
	Reason       string    `json:"reason,omitempty"`
	DelaySeconds int       `json:"delay_seconds,omitempty"`
	// Steps zijn de stappen van CmdApply.
	Steps []Step `json:"steps,omitempty"`
	// Inspect zijn de stappen van CmdInspect.
	Inspect []InspectStep `json:"inspect,omitempty"`
}

// Result is het antwoord van de agent op een commando.
type Result struct {
	OK     bool     `json:"ok"`
	Error  string   `json:"error,omitempty"`
	Output []string `json:"output,omitempty"`
	// Repeat is true als de agent dit commando al eerder kreeg en het niet
	// opnieuw uitvoerde.
	Repeat bool `json:"repeat,omitempty"`
	// Steps is het resultaat per stap van CmdApply, tot en met de stap die
	// mislukte.
	Steps []StepResult `json:"steps,omitempty"`
	// Observations is wat CmdInspect zag, één per stap in dezelfde volgorde.
	Observations []Observation `json:"observations,omitempty"`
}

// Step is één deploystap. Precies één veld is gezet. Elke stap is
// idempotent: hij kijkt eerst hoe het ervoor staat en verandert alleen wat
// anders is.
type Step struct {
	Package   *PackageStep   `json:"package,omitempty"`
	File      *FileStep      `json:"file,omitempty"`
	Service   *ServiceStep   `json:"service,omitempty"`
	User      *UserStep      `json:"user,omitempty"`
	Directory *DirectoryStep `json:"directory,omitempty"`
	Command   *CommandStep   `json:"command,omitempty"`
}

// PackageStep installeert of verwijdert apt-pakketten.
type PackageStep struct {
	Names []string `json:"names"`
	// State is present (standaard) of absent.
	State string `json:"state,omitempty"`
}

// FileStep schrijft een bestand. De server rendert de inhoud; de agent
// schrijft hem alleen als hij anders is.
type FileStep struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Mode is octaal, zoals "0644"; leeg is 0644.
	Mode  string `json:"mode,omitempty"`
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
}

// ServiceStep zet een systemd-service aan of uit.
type ServiceStep struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled,omitempty"`
	// State is started, stopped, restarted of reloaded; leeg laat hem zoals
	// hij is.
	State string `json:"state,omitempty"`
}

// UserStep maakt een gebruiker aan als die nog niet bestaat.
type UserStep struct {
	Name   string `json:"name"`
	System bool   `json:"system,omitempty"`
	Home   string `json:"home,omitempty"`
	Shell  string `json:"shell,omitempty"`
}

// DirectoryStep maakt een map met de gegeven rechten.
type DirectoryStep struct {
	Path  string `json:"path"`
	Mode  string `json:"mode,omitempty"`
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
}

// CommandStep voert een shellcommando uit, maar alleen als Creates nog niet
// bestaat of Unless niet lukt. Een van beide is verplicht, zodat de stap
// idempotent blijft.
type CommandStep struct {
	Run     string `json:"run"`
	Creates string `json:"creates,omitempty"`
	Unless  string `json:"unless,omitempty"`
}

// StepResult is het resultaat van één stap.
type StepResult struct {
	Changed bool     `json:"changed"`
	Output  []string `json:"output,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Kind geeft het soort stap, of "" als er geen of meer dan één veld gezet is.
func (s Step) Kind() string {
	kind := ""
	for k, set := range map[string]bool{
		"package": s.Package != nil, "file": s.File != nil, "service": s.Service != nil,
		"user": s.User != nil, "directory": s.Directory != nil, "command": s.Command != nil,
	} {
		if set {
			if kind != "" {
				return ""
			}
			kind = k
		}
	}
	return kind
}

// InspectStep zegt wat de agent moet bekijken. Precies één veld is gezet.
// Er is bewust geen inhoud of commando: de agent leest alleen, en de server
// vergelijkt.
type InspectStep struct {
	Packages  []string `json:"packages,omitempty"`
	File      string   `json:"file,omitempty"`
	Directory string   `json:"directory,omitempty"`
	Service   string   `json:"service,omitempty"`
	User      string   `json:"user,omitempty"`
	// Creates is het creates-pad van een command-stap: bestaat het, dan zou
	// apply het commando overslaan.
	Creates string `json:"creates,omitempty"`
}

// Observation is wat de agent van één InspectStep zag.
type Observation struct {
	Packages []PackageState `json:"packages,omitempty"`
	// Path is gezet voor File, Directory en Creates.
	Path    *PathState `json:"path,omitempty"`
	Service *UnitState `json:"service,omitempty"`
	// UserExists is gezet voor User.
	UserExists *bool `json:"user_exists,omitempty"`
	// Skipped zegt waarom de agent deze stap niet kon bekijken, zoals een
	// node zonder dpkg.
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

type PackageState struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
}

// PathState beschrijft een bestand of map. Een symlink wordt gevolgd, zoals
// apply dat doet; Symlink zegt dan alleen waar hij naar wijst.
type PathState struct {
	Exists bool `json:"exists"`
	// Type is file, directory of other.
	Type    string    `json:"type,omitempty"`
	Size    int64     `json:"size,omitempty"`
	Mode    string    `json:"mode,omitempty"`
	Owner   string    `json:"owner,omitempty"`
	Group   string    `json:"group,omitempty"`
	ModTime time.Time `json:"mtime,omitzero"`
	// SHA256 is de hash van een gewoon bestand tot MaxInspectHash bytes. De
	// server rekent er in het geheugen mee en bewaart hem nooit.
	SHA256  string `json:"sha256,omitempty"`
	Symlink string `json:"symlink,omitempty"`
}

// MaxInspectHash is het grootste bestand dat state.inspect hasht.
const MaxInspectHash = 16 << 20

type UnitState struct {
	Loaded  bool `json:"loaded"`
	Enabled bool `json:"enabled"`
	Active  bool `json:"active"`
}

// Kind geeft het soort stap, of "" als er geen of meer dan één veld gezet is.
func (s InspectStep) Kind() string {
	kind := ""
	for k, set := range map[string]bool{
		"package": len(s.Packages) > 0, "file": s.File != "", "directory": s.Directory != "",
		"service": s.Service != "", "user": s.User != "", "command": s.Creates != "",
	} {
		if set {
			if kind != "" {
				return ""
			}
			kind = k
		}
	}
	return kind
}

// Ack is het antwoord van de server op een request.
type Ack struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// EnrollRequest stuurt de agent naar POST /api/v1/agents/enroll.
type EnrollRequest struct {
	Token           string `json:"token"`
	NkeyPublic      string `json:"nkey_public"`
	Hostname        string `json:"hostname"`
	MachineID       string `json:"machine_id"`
	AgentVersion    string `json:"agent_version"`
	ProtocolVersion int    `json:"protocol_version"`
}

type EnrollResponse struct {
	AgentID        string `json:"agent_id"`
	NodeID         string `json:"node_id"`
	NatsURL        string `json:"nats_url"`
	NatsCertSHA256 string `json:"nats_cert_sha256"`
}

// Stable geeft een kopie zonder velden die bij elke meting veranderen, zoals
// schijfgebruik. Daarover rekent de server de hash, zodat alleen een echte
// wijziging een nieuwe snapshot en een event oplevert.
func (f Facts) Stable() Facts {
	out := f
	out.Filesystems = make([]Filesystem, len(f.Filesystems))
	for i, fs := range f.Filesystems {
		fs.UsedBytes = 0
		out.Filesystems[i] = fs
	}
	if f.Docker != nil {
		d := *f.Docker
		d.Containers = 0
		out.Docker = &d
	}
	return out
}
