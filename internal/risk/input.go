package risk

import (
	"time"

	"github.com/google/uuid"
)

// Acceptance is een bevinding die een beheerder bewust laat staan. Ze kost
// geen punten zolang ze niet verlopen is.
type Acceptance struct {
	ID        uuid.UUID  `json:"id"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"`
	By        string     `json:"by"`
	At        time.Time  `json:"at"`
}

// ClusterInput is alles wat de score van één cluster nodig heeft.
type ClusterInput struct {
	ID   uuid.UUID
	Name string
	// Env is lab, test of prod.
	Env          string
	Status       string
	StatusReason string

	Nodes []NodeInput
	VIPs  []VIPInput

	// Beschikbaarheid.

	// History zijn de statusovergangen van het cluster in Window, plus de
	// laatste ervoor, oudste eerst.
	History []Transition
	// Since is wanneer het cluster aangemaakt is; de beschikbaarheid telt
	// pas vanaf dan.
	Since time.Time
	// Incidents is het aantal incidenten in Window, zonder die van tests.
	Incidents int
	// VIPMoves is per VIP hoe vaak het in VIPWindow van eigenaar wisselde,
	// zonder tests.
	VIPMoves map[uuid.UUID]int

	// Back-ups; Backups is leeg als geen node aan Proxmox gekoppeld is.
	Backups []BackupInput
	// LastVerified is de laatste geslaagde back-upcontrole in de sandbox van
	// een node van dit cluster.
	LastVerified *time.Time

	// Herstelbaarheid.
	FailoverTests []FailoverInput
	// Unrestored zijn de tests die niet hersteld zijn.
	Unrestored []RunInput
	// Rehearsals zijn de DR-repetities van het cluster.
	Rehearsals []RehearsalInput

	// Redundantie.
	NonProdDeps []DepInput
	// SPOFs zijn de onderdelen waarvan de uitval dit cluster of een van
	// zijn prod-diensten down maakt.
	SPOFs []SPOFInput

	// Configuratie.
	DesiredState    bool
	Drift           []DriftInput
	WildcardIgnores []IgnoreInput
	SpecRevision    int
	AppliedRevision int
	// RevisionSince is wanneer de eerste niet toegepaste revisie gemaakt is.
	RevisionSince *time.Time
	// RevisionRunning is true als een taak de revisie nu toepast.
	RevisionRunning bool
	FailedGit       *GitInput

	// Capaciteit: MetricsRead is false zonder VictoriaMetrics.
	MetricsRead bool

	// Kube is wat de kaart Kubernetes nodig heeft; nil bij een ander
	// clustertype.
	Kube *KubeInput
	// DB is wat de kaart Database nodig heeft; nil als er op de nodes
	// geen database staat.
	DB *DatabaseCardInput
}

// KubeInput is wat de sync laatst van een Kubernetes-cluster zag.
type KubeInput struct {
	// Synced is false als de sync het cluster nog nooit las.
	Synced bool
	// Version is de versie van de API-server, zoals v1.35.9+k3s1.
	Version string
	Nodes   []KubeNodeInput
	// SnapshotsRead is false als de lijst van de etcd-snapshots nog nooit
	// of laatst niet te lezen was. NewestSnapshot is de nieuwste die klaar
	// is; nil als er geen is.
	SnapshotsRead  bool
	NewestSnapshot *time.Time
	Apps           []KubeAppInput
}

// KubeNodeInput is een node in Kubernetes. NodeID is de ClusterForge-node,
// als die gekoppeld is.
type KubeNodeInput struct {
	Name   string
	NodeID *uuid.UUID
	Server bool
	Ready  bool
}

// KubeAppInput is een Deployment, StatefulSet of DaemonSet.
type KubeAppInput struct {
	ID                    uuid.UUID
	Namespace, Kind, Name string
	// System is true in een namespace van Kubernetes zelf.
	System  bool
	Desired int
	Images  []string
	Trouble string
	// Restarts telt de herstarts van de containers van de huidige pods,
	// RestartedAt wanneer een container het laatst stopte.
	Restarts    int
	RestartedAt *time.Time
	// NoMemoryLimit zijn de containers zonder geheugenlimiet.
	NoMemoryLimit []string
}

// Transition is een statusovergang.
type Transition struct {
	At time.Time
	To string
}

// NodeInput is een node van het cluster.
type NodeInput struct {
	ID       uuid.UUID
	Hostname string
	// Lifecycle is active, maintenance, draining, provisioning of
	// decommissioned; LifecycleSince wanneer hij zo werd.
	Lifecycle      string
	LifecycleSince *time.Time
	// Host is de Proxmox-host van de VM; leeg zonder koppeling.
	Host string

	// HasFacts is false als de agent nog niets meldde.
	HasFacts        bool
	OSID            string
	OSVersion       string
	OSName          string
	Updates         int
	SecurityUpdates int
	// SecuritySince is sinds wanneer er beveiligingsupdates open staan,
	// uit het journaal; nil als dat niet te zien is.
	SecuritySince   *time.Time
	AptUpdatedAt    time.Time
	RebootRequired  bool
	Kernel          string
	KernelInstalled string
	Certificates    []CertInput
	Filesystems     []FSInput
	// ProtocolVersion is die van de agent; 0 zonder agent.
	ProtocolVersion int

	// Metrics is nil als ze niet te lezen waren.
	Metrics *NodeMetrics
}

// CertInput is een servercertificaat op een node.
type CertInput struct {
	Path     string
	Name     string
	NotAfter time.Time
}

// FSInput is een bestandssysteem uit de facts.
type FSInput struct {
	Mount string
	Size  uint64
	Used  uint64
}

// NodeMetrics zijn de vaste risicoqueries over zeven dagen.
type NodeMetrics struct {
	CPUP95    *float64
	MemoryP95 *float64
	SwapAvg   *float64
	// NetErrors is het aantal netwerkfouten in zeven dagen.
	NetErrors *float64
	// DiskDaysLeft is per mountpoint over hoeveel dagen het vol is volgens
	// de trend; alleen voor bestandssystemen die voller worden.
	DiskDaysLeft map[string]float64
}

// VIPInput is een VIP met de nodes die het in hun keepalived-configuratie
// hebben.
type VIPInput struct {
	ID         uuid.UUID
	Address    string
	Candidates []uuid.UUID
}

// BackupInput is de back-upstand van de VM van een node.
type BackupInput struct {
	NodeID   uuid.UUID
	Hostname string
	// Freshness is ok, stale, missing of unknown.
	Freshness   string
	Latest      *time.Time
	MaxAgeHours int
	// PBSFailed is true als Proxmox Backup Server de nieuwste back-up niet
	// kon verifiëren.
	PBSFailed bool
	// Verification is de uitslag van de laatste back-upcontrole in de
	// sandbox: pass, warning, fail, error, of leeg als er geen was.
	Verification   string
	VerificationAt *time.Time
	VerificationID uuid.UUID
}

// FailoverInput is een failovertest met zijn laatste afgeronde run.
type FailoverInput struct {
	ID      uuid.UUID
	Name    string
	VIPID   uuid.UUID
	LastRun *RunInput
}

// RehearsalInput is een DR-repetitie met haar laatste afgeronde run.
type RehearsalInput struct {
	ID      uuid.UUID
	Name    string
	LastRun *RunInput
}

// RunInput is een afgeronde testrun.
type RunInput struct {
	ID uuid.UUID
	// Kind is failover.test of disaster.run.
	Kind     string
	Name     string
	Result   string
	Summary  string
	Finished time.Time
}

// DepInput is een afhankelijkheid van een prod-dienst op een dienst in lab
// of test.
type DepInput struct {
	ID        uuid.UUID
	From      string
	To        string
	ToCluster string
	ToEnv     string
}

// DriftInput is de drift van één node.
type DriftInput struct {
	NodeID   uuid.UUID
	Hostname string
	Findings int
	Since    *time.Time
}

// IgnoreInput is een negeerregel met * die alle drift van een node of het
// cluster stil zet.
type IgnoreInput struct {
	ID        uuid.UUID
	Node      string
	ExpiresAt time.Time
}

// GitInput is de laatste Git-wijziging van het cluster als die mislukte.
type GitInput struct {
	ID     uuid.UUID
	Commit string
	Reason string
	At     time.Time
}

// PlatformInput is wat de kaart Platform nodig heeft.
type PlatformInput struct {
	AdminsWithoutTOTP []UserInput
	Proxmox           []ProxmoxInput
	// Self is nil zonder Proxmox-koppeling.
	Self *SelfInput
}

// SelfInput is de VM waarop ClusterForge zelf draait.
type SelfInput struct {
	// Found is false als niet bekend is waar ClusterForge draait.
	Found bool
	ID    uuid.UUID
	Name  string
	// BackupsRead is false als de back-ups van zijn koppeling niet te lezen
	// zijn; dan zegt Latest niets.
	BackupsRead bool
	Latest      *time.Time
}

type UserInput struct {
	ID       uuid.UUID
	Username string
}

// ProxmoxInput is een Proxmox-koppeling.
type ProxmoxInput struct {
	ID         uuid.UUID
	Name       string
	LastError  string
	LastSyncAt *time.Time
	CreatedAt  time.Time
	// CertNotAfter is de vervaldatum van het certificaat van Proxmox, bij
	// de laatste sync gelezen.
	CertNotAfter *time.Time
}
