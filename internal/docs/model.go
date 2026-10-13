package docs

import (
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/kube"
)

// Fleet is alles waaruit de documentatie geschreven wordt. Gather vult het
// uit de database; de rest van het pakket rekent alleen, zodat dezelfde
// invoer altijd dezelfde tekst geeft.
type Fleet struct {
	Clusters []Cluster
	Proxmox  []Proxmox
	SPOFs    []SPOF
	Findings []Finding
	// Links zijn de bevestigde afhankelijkheden tussen clusters en naar
	// externe diensten, samengeteld per paar.
	Links []Link
}

// Cluster is één cluster met alles wat zijn hoofdstukken nodig hebben.
type Cluster struct {
	ID          uuid.UUID
	Name, Slug  string
	Env         string
	Type        string
	Description string
	Tags        []string
	// Status, Grade en Score staan alleen in het overzicht.
	Status string
	Grade  string
	Score  *int
	// Template is nil voor een cluster dat niet uit een template komt.
	Template *Template
	// Network is het netwerk uit de specificatie; nil zonder.
	Network *Network
	Nodes   []Node
	VIPs    []VIP
	// Services zijn de bevestigde diensten van het cluster en van zijn
	// nodes; Deps de bevestigde pijlen waarvan een kant hier ligt.
	Services []Svc
	Deps     []Dep
	// Params zijn de parameters in de volgorde van de template.
	Params []Param
	// Notes zijn de opmerkingen bij de gewenste staat, zoals een nieuwere
	// templateversie.
	Notes []string
	// Files is per node wat ClusterForge er volgens de specificatie zet,
	// met plaatshouders voor de geheimen. FilesNote zegt waarom het
	// ontbreekt.
	Files     []NodeFiles
	FilesNote string
	// YAML is cluster.yaml zoals GitOps het zou exporteren; nil zonder.
	YAML              *File
	GitLinked         bool
	Revision, Applied int
	// Revisions zijn de nieuwste revisies; OlderRevisions telt wat er niet
	// meer bij staat.
	Revisions      []Revision
	OlderRevisions int
	Backup         BackupPolicy
	Failover       []FailoverTest
	DR             DR
	// Outages is wat de wat-als voorspelt bij uitval van elke node en
	// elke host met een node van dit cluster.
	Outages []Outage
	// Summary is de samenvatting 'In het kort' van de assistent; nil
	// zonder.
	Summary *Summary
	// Kube is wat de sync laatst van een Kubernetes-cluster zag; nil bij
	// een ander clustertype.
	Kube *Kube
	// DB zijn de databases op de nodes van het cluster; nil zonder.
	DB *ClusterDB
}

// ClusterDB zijn de databases op de nodes van een cluster: de onderdelen
// Database, en de servers die ClusterForge alleen leest. Er staan geen
// wachtwoorden, hashes of rijen in.
type ClusterDB struct {
	Components []DBComponent
	Servers    []DBServers
}

// DBComponent is een onderdeel Database dat ClusterForge uitrolde.
type DBComponent struct {
	Title, Engine string
	// Version is de versie die de server meldt; leeg als ClusterForge hem
	// nog niet las.
	Version string
	Port    int
	// VIP is het adres van de database voor de app; leeg zonder. OwnVIP
	// zegt of het een eigen VIP is dat de primary volgt.
	VIP    string
	OwnVIP bool
	// Data zijn de datanodes, Arbiters de scheidsrechters, op hostnaam in
	// de volgorde van de specificatie.
	Data, Arbiters []string
	// AutoRecover: ClusterForge begint Galera vanzelf opnieuw na een
	// volledige uitval.
	AutoRecover bool
	// De nachtelijke dumps.
	BackupOn bool
	BackupAt string
	KeepDays int
	// Databases zijn de databases van de apps.
	Databases []DBDatabase
}

// DBServers zijn databaseservers op de nodes van het cluster die
// ClusterForge niet uitrolde, zoals een gedeeld databasecluster.
type DBServers struct {
	Engine string
	// Kind is galera, patroni, replication of single.
	Kind    string
	Version string
	Nodes   []string
	// Databases zijn de databases van de apps.
	Databases []DBDatabase
}

// DBDatabase is een database van een app.
type DBDatabase struct {
	Name      string
	SizeBytes int64
	Tables    int
	// Users zijn wie de database de laatste dertig dagen gebruikte, zoals
	// 'Nextcloud (nc1, nc2) als nextcloud'.
	Users []string
	// Dump is ok, stale, missing of off; leeg bij een server die
	// ClusterForge alleen leest.
	Dump string
}

// Kube is een Kubernetes-cluster zoals de sync het laatst zag.
type Kube struct {
	// Synced is false als ClusterForge het cluster nog nooit las.
	Synced bool
	// Version is de versie van de API-server, zoals v1.35.9+k3s1.
	Version string
	// TemplateK3s is de k3s van de templateversie, NextVersion en NextK3s
	// de versie waarnaar het cluster kan bijwerken; leeg zonder.
	TemplateK3s, NextVersion, NextK3s string
	// Init is de server die het cluster begon; leeg als geen agent het
	// meldt.
	Init  string
	Nodes []KubeNode
	Apps  []KubeApp
	// Inventory is nil als de namespaces, webadressen en opslag nog nooit
	// gelezen werden.
	Inventory *kube.Inventory
	// Snapshots telt de etcd-snapshots die klaar zijn; Fresh is ok, stale,
	// none of unknown, zoals kube.Fresh.
	Snapshots int
	Fresh     string
}

// KubeNode is een node in Kubernetes, met de ClusterForge-node als die
// gekoppeld is.
type KubeNode struct {
	Name, Hostname string
	Server         bool
	Kubelet, IP    string
	CPUMillis      int64
	MemoryBytes    int64
}

// KubeApp is een Deployment, StatefulSet of DaemonSet.
type KubeApp struct {
	Namespace, Kind, Name string
	System                bool
	Desired               int
	Images                []string
}

// Summary is een samenvatting die de assistent op verzoek schreef. Text is
// al veilig gemaakt met SummaryText.
type Summary struct {
	Text  string
	Model string
	At    time.Time
	By    string
}

type Template struct {
	Name, Version, Title, Description string
	// Latest is de nieuwste versie in deze server; Available is false als
	// de versie van het cluster er niet meer in zit.
	Latest    string
	Available bool
	Failback  bool
}

// Network is het netwerk zoals de specificatie het vastlegt.
type Network struct {
	Proxmox   string
	Mode      string
	FirstIP   string
	Gateway   string
	DNS       []string
	Bridge    string
	VLAN      *int
	Storage   string
	ImageVMID int
}

type Node struct {
	ID          uuid.UUID
	Hostname    string
	Role        string
	Index       int
	Lifecycle   string
	Description string
	// Address is het primaire adres; Prefix en Interface komen uit de
	// facts.
	Address   string
	Prefix    int
	Interface string
	// Interfaces zijn de netwerkkaarten uit de facts, zonder loopback en
	// tijdelijke kaarten van containers.
	Interfaces []Iface
	Gateway    string
	DNS        []string
	OS         string
	HasAgent   bool
	HasFacts   bool
	// VM is de grootte uit de specificatie; nil zonder.
	VM *VMShape
	// Guest is de VM in Proxmox; nil als de node niet gekoppeld is.
	Guest  *Guest
	Backup *NodeBackup
	Verify *Verification
}

type Iface struct {
	Name, MAC string
	Addresses []string
}

type VMShape struct {
	CPU, MemoryMiB, DiskGiB int
}

type Guest struct {
	Proxmox string
	VMID    int
	Type    string
	Name    string
	Host    string
	HA      bool
	Pool    string
	MaxMem  int64
	// ConfigRead is false zolang de configuratie niet gelezen is; dan zijn
	// NICs en Disks leeg.
	ConfigRead bool
	OnBoot     bool
	NICs       []NIC
	Disks      []Disk
}

type NIC struct {
	Key, Bridge, MAC string
	Tag              int
}

type Disk struct {
	Key, Storage string
	SizeBytes    int64
}

// NodeBackup is de nieuwste back-up van de VM van een node.
type NodeBackup struct {
	// Volid is leeg als er geen back-up is.
	Volid     string
	Storage   string
	Host      string
	Time      time.Time
	Size      int64
	Freshness string
	Count     int
}

type Verification struct {
	Result          string
	FinishedAt      *time.Time
	RecoverySeconds *float64
}

type VIP struct {
	Address, Interface, Description string
	VRID                            *int
	// Nodes zijn de nodes met dit adres in hun keepalived-configuratie, de
	// hoogste prioriteit eerst.
	Nodes []string
}

// Svc is een dienst van het cluster.
type Svc struct {
	ID               uuid.UUID
	Name, Kind, Unit string
	Port             int
	// On is de node van een dienst op één node; leeg voor het hele cluster.
	On     string
	Source string
}

// Dep is een pijl van afnemer naar leverancier.
type Dep struct {
	From, To End
	Strength string
	Source   string
	Note     string
}

// End is een kant van een pijl.
type End struct {
	ID   uuid.UUID
	Name string
	// Group is het cluster, de node of "extern".
	Group     string
	ClusterID *uuid.UUID
	Slug      string
	Kind      string
	External  bool
	Address   string
	Port      int
}

type Param struct {
	Name, Label string
	// Value is nil als de parameter niet ingevuld is.
	Value  *string
	Secret bool
}

// NodeFiles is wat de template op één node zet.
type NodeFiles struct {
	Hostname string
	Packages []string
	Services []string
	Files    []File
	Error    string
}

type File struct {
	Name, Path, Mode, Owner, Group, Content string
}

type Revision struct {
	Revision int
	Source   string
	Commit   string
	At       time.Time
	By       string
	Template string
	Changes  []Change
}

type Change struct {
	Label, From, To string
}

type BackupPolicy struct {
	MaxAgeHours   int
	Default       bool
	VerifyEnabled bool
	Window        string
}

type FailoverTest struct {
	Name, Description, VIP string
	MaxTakeoverSeconds     int
	ExpectFailback         bool
	Scheduled              bool
	Last                   *TestRun
}

// DR is wat de documentatie weet van de DR-repetitie van een cluster.
type DR struct {
	// RTOMinutes en RPOHours zijn de doelen nu; 0 is geen doel. Default is
	// true als beide de standaard van de omgeving zijn.
	RTOMinutes, RPOHours int
	Default              bool
	Scenarios            []DRScenario
	// Last is de laatste afgeronde repetitie, Measured de laatste die de
	// RTO mat; meestal is dat dezelfde.
	Last, Measured *DRRun
}

type DRScenario struct {
	Name, Schedule, Bridge string
}

// DRRun is een afgeronde DR-repetitie.
type DRRun struct {
	ID                     uuid.UUID
	Name                   string
	Result, Summary        string
	At                     time.Time
	Proxmox, Host, Storage string
	Bridge                 string
	// RTO en RPO zijn nil als ze niet gemeten zijn.
	RTO, RPO *time.Duration
	Nodes    []DRNode
	VIPs     []DRVIP
}

type DRNode struct {
	Hostname      string
	SourceVMID    int
	SandboxVMID   int
	BackupTime    time.Time
	Restore, Boot *time.Duration
}

type DRVIP struct {
	Address string
	Holders []string
	// Checked is false als geen node zijn adressen kon melden.
	Checked bool
	Probe   string
	ProbeOK *bool
}

type TestRun struct {
	Result, Summary string
	At              time.Time
	TakeoverMS      *int64
	DowntimeMS      *int64
	FailbackMS      *int64
	TakeoverNode    string
	ReturnedTo      string
}

// Outage is wat er met een cluster gebeurt als één onderdeel uitvalt en de
// rest gezond is.
type Outage struct {
	// Kind is node of host.
	Kind string
	Name string
	// Status en Reason zijn de status van het cluster die blijft, na wat
	// HA herstart; During is de status daarvoor, als HA iets herstart.
	Status, Reason string
	During         string
	// Nodes zijn de nodes van dit cluster die uitvallen; Restarted die HA
	// elders start.
	Nodes     []string
	Restarted []string
	// Others zijn andere clusters en diensten die down of verminderd gaan.
	Others []string
	Self   bool
}

type Proxmox struct {
	Name  string
	Hosts []Host
}

type Host struct {
	Name     string
	Online   bool
	MaxMem   int64
	Storages []Storage
	Bridges  []Bridge
	Guests   int
}

type Storage struct {
	Name, Type     string
	Shared, Active bool
}

type Bridge struct {
	Name      string
	Ports     []string
	CIDR      string
	VLANAware bool
}

type SPOF struct {
	Kind, Name, Group string
	// Down zijn de prod-clusters en prod-diensten die down gaan, met de
	// reden.
	Down []string
	Self bool
}

type Finding struct {
	Where  string
	Text   string
	Points int
}

// Link is een of meer pijlen van diensten in From naar diensten in To.
type Link struct {
	From, To LinkEnd
	Count    int
	Hard     bool
	Services []string
}

type LinkEnd struct {
	Name, Slug, Env string
	External        bool
}
