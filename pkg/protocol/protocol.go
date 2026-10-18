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
const Version = 20

// CommandsSince is de eerste protocolversie met commando's.
const CommandsSince = 2

// ApplySince is de eerste protocolversie die deploystappen uitvoert.
const ApplySince = 3

// InspectSince is de eerste protocolversie die state.inspect kent. Een
// oudere agent weigert een onbekend commando, maar leest velden die hij niet
// kent zonder fout; daarom is inspecteren een eigen commando en geen vlag op
// apply.steps.
const InspectSince = 4

// JournalSince is de eerste protocolversie die een wijzigingsjournaal en
// verbindingen stuurt, en de netwerkfacts, keepalived-instanties en
// onderhoudsfacts meldt. Een oudere agent stuurt die niet; de server neemt
// dan zelf waar wat hij uit heartbeats en facts kan halen.
const JournalSince = 5

// FaultsSince is de eerste protocolversie die storingen kan injecteren
// (fault.inject, fault.renew en fault.clear) en in de facts meldt welke
// gereedschappen ervoor op de node staan.
const FaultsSince = 6

// RehearsalSince is de eerste protocolversie waarvan cf-agent verify zijn
// adressen meldt en probes naar het VIP en de andere nodes doet, voor de
// DR-repetitie. Een agent met protocol 6 kent ze niet en laat de velden
// zonder fout weg.
const RehearsalSince = 7

// Berichttypes.
const (
	TypeHeartbeat = "heartbeat"
	TypeFacts     = "facts"
	TypeMetrics   = "metrics"
	TypeAck       = "ack"
	TypeCommand   = "command"
	TypeResult    = "command_result"
	// TypeChanges en TypeConns gaan op het subject events.
	TypeChanges = "changes"
	TypeConns   = "conns"
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
	// SubjectEvents draagt het wijzigingsjournaal (een request met Ack) en
	// de verbindingen (publish).
	SubjectEvents   = "events"
	SubjectCommands = "cmd"
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

	// Vanaf protocol 5. Bij een oudere agent zijn ze leeg.

	// Routes zijn de IPv4- en IPv6-routes uit de hoofdtabel.
	Routes []Route `json:"routes,omitempty"`
	// DefaultGateway is de gateway van de standaardroute (IPv4).
	DefaultGateway string `json:"default_gateway,omitempty"`
	// DNS zijn de nameservers uit /etc/resolv.conf.
	DNS []string `json:"dns,omitempty"`
	// Listeners zijn de poorten waarop iets luistert, zonder de tijdelijke
	// poorten.
	Listeners []Listener `json:"listeners,omitempty"`
	// RebootRequired is true als /run/reboot-required bestaat of een
	// nieuwere kernel geïnstalleerd is dan er draait.
	RebootRequired bool `json:"reboot_required,omitempty"`
	// KernelInstalled is de nieuwste kernel in /boot.
	KernelInstalled string `json:"kernel_installed,omitempty"`
	// AptUpdatedAt is wanneer de pakketlijsten het laatst ververst zijn.
	AptUpdatedAt time.Time `json:"apt_updated_at,omitzero"`
	// Certificates zijn de servercertificaten op de vaste plekken, zonder
	// CA's. Alleen onderwerp, namen en vervaldatum; nooit een sleutel.
	Certificates []Certificate `json:"certificates,omitempty"`

	// Vanaf protocol 6.

	// Tools zegt welke gereedschappen voor storingen op de node staan.
	Tools *Tools `json:"tools,omitempty"`
	// Faults zijn de storingen die nu actief zijn, met hun id.
	Faults []string `json:"faults,omitempty"`

	// Vanaf protocol 8.

	// Kube beschrijft k3s op de node; nil zonder k3s.
	Kube *KubeFacts `json:"kube,omitempty"`
}

// Tools zijn de gereedschappen die storingsinjectie nodig heeft: nft voor
// vrrp_block en port_block, tc voor netem. ip is er altijd.
type Tools struct {
	Nft bool `json:"nft"`
	Tc  bool `json:"tc"`
}

type Route struct {
	// Dest is het netwerk met prefix, zoals 10.0.20.0/24 of 0.0.0.0/0.
	Dest      string `json:"dest"`
	Gateway   string `json:"gateway,omitempty"`
	Interface string `json:"interface"`
	Metric    int    `json:"metric,omitempty"`
}

type Listener struct {
	// Proto is tcp of udp.
	Proto string `json:"proto"`
	Addr  string `json:"addr"`
	Port  int    `json:"port"`
	// Unit is de systemd-unit van het proces, zonder .service; leeg als
	// het niet te zien is.
	Unit string `json:"unit,omitempty"`
}

type Certificate struct {
	Path     string    `json:"path"`
	Subject  string    `json:"subject"`
	DNSNames []string  `json:"dns_names,omitempty"`
	Issuer   string    `json:"issuer"`
	NotAfter time.Time `json:"not_after"`
}

type OSInfo struct {
	ID         string `json:"id"`
	VersionID  string `json:"version_id"`
	Codename   string `json:"codename"`
	PrettyName string `json:"pretty_name"`
}

type Interface struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
	Up   bool   `json:"up"`
	// Addresses hebben een prefix, zoals 10.0.20.11/24.
	Addresses []string `json:"addresses"`

	// Vanaf protocol 5.

	// Kind is physical, vlan, bridge, bond, veth, wireguard, tun, dummy of
	// other.
	Kind string `json:"kind,omitempty"`
	// Parent is bij een VLAN de onderliggende interface.
	Parent string `json:"parent,omitempty"`
	// Master is de bridge of bond waarin deze interface zit.
	Master string `json:"master,omitempty"`
	VLANID int    `json:"vlan_id,omitempty"`
	MTU    int    `json:"mtu,omitempty"`
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
	// Instances zijn de vrrp_instance-blokken; vanaf protocol 5.
	Instances []VRRPInstance `json:"instances,omitempty"`
}

type VRRPInstance struct {
	Name      string `json:"name"`
	Interface string `json:"interface"`
	VRID      int    `json:"vrid"`
	// State is de begintoestand uit de configuratie (MASTER of BACKUP).
	State    string `json:"state,omitempty"`
	Priority int    `json:"priority,omitempty"`
	// Peers zijn de unicast_peer-adressen; leeg bij multicast.
	Peers []string `json:"peers,omitempty"`
	VIPs  []string `json:"vips"`
	// TrackScripts zijn de vrrp_scripts in track_script, zoals cf_db.
	TrackScripts []string `json:"track_scripts,omitempty"`
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
	// CmdFaultInject zet de storing in Fault aan. De agent schrijft haar eerst
	// naar zijn state en draait haar zelf terug als de lease of de
	// bovengrens verloopt, ook na een herstart.
	CmdFaultInject = "fault.inject"
	// CmdFaultRenew verlengt de lease van de storingen in FaultIDs. Een id
	// dat de agent niet (meer) kent, staat in de fout.
	CmdFaultRenew = "fault.renew"
	// CmdFaultClear draait de storingen in FaultIDs terug; leeg is alles.
	// Een onbekend id is geen fout.
	CmdFaultClear = "fault.clear"
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
	// Fault is de storing van CmdFaultInject.
	Fault *Fault `json:"fault,omitempty"`
	// FaultIDs zijn de storingen van CmdFaultRenew en CmdFaultClear.
	FaultIDs []string `json:"fault_ids,omitempty"`
}

// Soorten storing. Er is geen vrije interface, regel of commando: de agent
// kent alleen deze, met vaste grenzen.
const (
	// FaultLinkDown zet een interface down (ip link set dev X down).
	FaultLinkDown = "link_down"
	// FaultVRRPBlock laat VRRP (IP-protocol 112) in en uit vallen, met een
	// eigen nft-tabel.
	FaultVRRPBlock = "vrrp_block"
	// FaultPortBlock laat inkomend TCP-verkeer op één poort vallen, behalve
	// van de node zelf.
	FaultPortBlock = "port_block"
	// FaultNetem geeft een interface vertraging of pakketverlies met tc.
	FaultNetem = "netem"
	// FaultDiskFill vult de schijf met de state van de agent tot een
	// percentage met één bestand.
	FaultDiskFill = "disk_fill"
)

// Grenzen van storingen; de agent weigert alles daarbuiten.
const (
	// FaultLease is hoe lang een storing blijft zonder fault.renew.
	FaultLease = 30 * time.Second
	// FaultMax is de harde bovengrens van elke storing.
	FaultMax = 10 * time.Minute
	// FaultMaxCutOff is de bovengrens van link_down op de interface waarover
	// de agent met de server praat: de server kan dan niet verlengen of
	// opheffen, dus geldt alleen deze grens.
	FaultMaxCutOff  = 2 * time.Minute
	NetemMaxDelayMS = 2000
	NetemMaxLoss    = 50
	DiskFillMax     = 95
)

// Fault is een storing. ID is uniek per storing op deze node; de server
// gebruikt het om te verlengen en op te heffen.
type Fault struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Interface bij link_down en netem.
	Interface string `json:"interface,omitempty"`
	// Port bij port_block.
	Port int `json:"port,omitempty"`
	// DelayMS en LossPercent bij netem.
	DelayMS     int `json:"delay_ms,omitempty"`
	LossPercent int `json:"loss_percent,omitempty"`
	// FillPercent bij disk_fill.
	FillPercent int `json:"fill_percent,omitempty"`
	// LeaseSeconds: zonder verlenging zo lang; nul is FaultLease.
	LeaseSeconds int `json:"lease_seconds,omitempty"`
	// MaxSeconds is de bovengrens, hoe vaak er ook verlengd wordt.
	MaxSeconds int `json:"max_seconds"`
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
	out.AptUpdatedAt = time.Time{}
	return out
}

// Het wijzigingsjournaal. De agent leest wat er op de node veranderde en
// stuurt het als Changes, als request op cf.node.<id>.events; na de Ack
// vergeet hij wat verstuurd is. Hij stuurt nooit de inhoud van een bestand,
// een hash, een commandoregel of een wachtwoord.
const (
	// MaxChanges is het grootste aantal regels per bericht.
	MaxChanges = 500
	// JournalMaxAge is hoe ver de agent na een onderbreking teruggaat.
	JournalMaxAge = 7 * 24 * time.Hour
	// JournalInterval is hoe vaak de agent kijkt en stuurt.
	JournalInterval = 30 * time.Second
)

// Soorten wijzigingen uit de agent.
const (
	ChangePackage = "package"
	ChangeBoot    = "boot"
	ChangeService = "service"
	ChangeFile    = "file"
	ChangeLogin   = "login"
	ChangeOOM     = "oom"
)

// Change is één wijziging op de node. TS is de tijd op de node; de server
// corrigeert hem voor de klokafwijking.
type Change struct {
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind"`
	// Subject is het pakket, de unit, het pad, de gebruiker of het proces.
	Subject string `json:"subject"`
	// Action hangt af van de soort:
	//   package: install, upgrade, downgrade, remove, purge
	//   boot:    boot
	//   service: started, stopped, failed, restarted, auto_restarted
	//   file:    created, modified, deleted, mode, owner
	//   login:   ssh
	//   oom:     killed
	Action string `json:"action"`
	// From en To zijn de oude en nieuwe versie, kernel, mode of eigenaar.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// By is wie het deed: de gebruiker achter apt of sudo, of
	// unattended-upgrades; bij een login de gebruiker.
	By string `json:"by,omitempty"`
	// Detail zijn een paar vaste velden, zoals het bron-IP en de methode
	// van een login, het Result van een unit of het programma (apt-get).
	Detail map[string]string `json:"detail,omitempty"`
}

// Changes is één bericht uit het journaal. Truncated is true als de agent
// regels moest laten vallen, omdat er meer waren dan hij bewaart.
type Changes struct {
	Entries   []Change `json:"entries"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Verbindingen. De agent kijkt elke ConnsSample naar de gevestigde
// TCP-verbindingen en stuurt elke ConnsInterval een samenvatting. Geen
// PID's, commandoregels of inhoud; van een inkomende verbinding alleen de
// afzender als die in een privénetwerk zit.
const (
	ConnsSample   = time.Minute
	ConnsInterval = 5 * time.Minute
	MaxConns      = 500
)

type Conns struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	Out   []ConnOut `json:"out"`
	In    []ConnIn  `json:"in"`
}

// ConnOut is een uitgaande verbinding van Unit naar Addr:Port. Count is in
// hoeveel steekproeven ze gezien is, opgeteld over de verbindingen.
type ConnOut struct {
	Unit  string `json:"unit,omitempty"`
	Addr  string `json:"addr"`
	Port  int    `json:"port"`
	Count int    `json:"count"`
}

// ConnIn is een inkomende verbinding op een eigen poort. Peer is leeg
// tenzij de afzender een privé-adres heeft.
type ConnIn struct {
	Unit  string `json:"unit,omitempty"`
	Port  int    `json:"port"`
	Peer  string `json:"peer,omitempty"`
	Count int    `json:"count"`
}

// De diepe back-upcontrole. ClusterForge start cf-agent verify via de QEMU
// guest agent in een teruggezette sandbox, niet via NATS: de kopie heeft
// geen netwerk. De aanvraag gaat als JSON naar stdin, het antwoord komt als
// JSON op stdout. Een agent zonder verify geeft exitcode 2 zonder JSON.
const (
	// VerifyPath is het programma dat de server in de sandbox start, met
	// de vaste argumenten "verify -".
	VerifyPath = "/usr/local/bin/cf-agent"
	// VerifyInvalid is de exitcode bij een ongeldige aanvraag.
	VerifyInvalid = 3
	// SandboxSerial is het SMBIOS-serienummer van een sandbox. cf-agent run
	// verbindt dan niet met de server.
	SandboxSerial = "cf-sandbox"
	// VerifyCheckLimit en VerifyLimit zijn de limieten van de agent per
	// controle en voor alles samen.
	VerifyCheckLimit = 60 * time.Second
	VerifyLimit      = 5 * time.Minute
)

// VerifyRequest zegt wat er in de sandbox moet draaien. Het zijn alleen
// namen, poorten en adressen op loopback: geen vrije SQL of shell.
type VerifyRequest struct {
	// Services zijn systemd-units die active moeten zijn, zoals nginx of
	// postgresql@16-main.
	Services []string `json:"services"`
	// TCP zijn poorten die op loopback verbindingen moeten aannemen.
	TCP []VerifyTCP `json:"tcp,omitempty"`
	// HTTP zijn adressen op loopback met de verwachte statuscode.
	HTTP []VerifyHTTP `json:"http,omitempty"`
	// PostgreSQL en MariaDB vragen de vaste databasecontroles.
	PostgreSQL bool `json:"postgresql,omitempty"`
	MariaDB    bool `json:"mariadb,omitempty"`
	// Addresses vraagt de adressen van de machine, zonder loopback: de
	// DR-repetitie leidt eruit af wie het VIP heeft. Vanaf protocol 7.
	Addresses bool `json:"addresses,omitempty"`
	// Probes zijn verbindingen naar het VIP en de andere nodes in het
	// afgesloten netwerk van de DR-repetitie. De agent doet ze alleen in een
	// sandbox (serienummer cf-sandbox). Vanaf protocol 7.
	Probes []VerifyProbe `json:"probes,omitempty"`
}

// VerifyProbe is een verbinding vanuit de sandbox naar een ander adres. Met
// Path een HTTP GET op Port, anders alleen een TCP-verbinding.
type VerifyProbe struct {
	// Name is wat het rapport toont, zoals "VIP 10.0.30.100" of "web02".
	Name   string `json:"name"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Path   string `json:"path,omitempty"`
	Expect int    `json:"expect,omitempty"`
	// Reach vraagt alleen of het adres antwoordt: een geweigerde
	// TCP-verbinding telt ook, want dan antwoordde de machine. Zonder Path.
	Reach bool `json:"reach,omitempty"`
}

type VerifyTCP struct {
	// Host is 127.0.0.1 of ::1.
	Host string `json:"host"`
	Port int    `json:"port"`
	// Service is de unit die op deze poort hoort te luisteren.
	Service string `json:"service,omitempty"`
}

type VerifyHTTP struct {
	// URL begint met http://127.0.0.1 of http://[::1].
	URL    string `json:"url"`
	Expect int    `json:"expect"`
}

// VerifyResult is het antwoord van cf-agent verify. Error is gezet bij een
// ongeldige aanvraag (exitcode 3); dan zijn er geen controles.
type VerifyResult struct {
	ProtocolVersion int           `json:"protocol_version"`
	AgentVersion    string        `json:"agent_version"`
	Error           string        `json:"error,omitempty"`
	Checks          []VerifyCheck `json:"checks"`
	// Addresses zijn de adressen van de machine, zonder loopback en
	// link-local, als de aanvraag erom vroeg.
	Addresses []string `json:"addresses,omitempty"`
	// Seconds is hoe lang alles samen duurde.
	Seconds float64 `json:"seconds"`
}

// Uitkomsten van een VerifyCheck.
const (
	VerifyOK      = "ok"
	VerifyWarning = "warning"
	VerifyFail    = "fail"
)

// VerifyCheck is één controle in de sandbox.
type VerifyCheck struct {
	// Kind is service, units, tcp, http, postgresql, mariadb of probe.
	Kind string `json:"kind"`
	// Name is de unit, de poort of het adres.
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	// Count is bij een database het aantal databases.
	Count int `json:"count,omitempty"`
}
