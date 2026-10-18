package protocol

// Zelfherstel (fase 7): de agent stelt vast wat er met MariaDB, MySQL en
// PostgreSQL op zijn node mis is, ook als ClusterForge ze niet toepaste, en
// zet op verzoek de adressen in de Galera-config goed. Starten, stoppen en
// het cluster beginnen gaan met de gewone deploystappen.

// HealSince is de eerste protocolversie met DBHeal.
const HealSince = 22

// DBHeal is het databaseverzoek van Zelfherstel; Heal zegt wat.
const DBHeal = "heal"

// Wat een DBHeal doet.
const (
	// HealInspect leest units, processen, vrije ruimte en de Galera-config.
	HealInspect = "inspect"
	// HealSetGaleraAddress zet wsrep_cluster_address en wsrep_node_address
	// in het bestand waar ze staan, na een kopie. Een MariaDB die draait,
	// leest ze pas bij zijn volgende start.
	HealSetGaleraAddress = "set_galera_address"
)

// HealBackupSuffix staat achter de kopie van een configbestand dat
// Zelfherstel wijzigt, met de tijd erachter.
const HealBackupSuffix = ".cf-zelfherstel-"

// DBHealRequest zegt wat een DBHeal doet.
type DBHealRequest struct {
	Action string `json:"action"`
	// ClusterAddress is bij HealSetGaleraAddress de nieuwe
	// wsrep_cluster_address (gcomm://a,b,c) en NodeAddress de nieuwe
	// wsrep_node_address; leeg laat die waarde staan.
	ClusterAddress string `json:"cluster_address,omitempty"`
	NodeAddress    string `json:"node_address,omitempty"`
}

// DBHealResult is het antwoord op een DBHeal.
type DBHealResult struct {
	Engines []HealEngine `json:"engines"`
	// Changed zijn de bestanden die HealSetGaleraAddress wijzigde, met de
	// kopie.
	Changed []HealChange `json:"changed,omitempty"`
}

// HealEngine is wat de agent over één database op zijn node zegt.
type HealEngine struct {
	// Engine is DBMariaDB (ook MySQL) of DBPostgreSQL.
	Engine string `json:"engine"`
	// Unit is de unit, zoals mariadb, mysql of patroni; Active, Sub en
	// Result zijn wat systemd erover zegt, Enabled of hij bij een boot
	// start, en MainPID het hoofdproces (0 als systemd er geen heeft).
	Unit    string `json:"unit"`
	Active  string `json:"active"`
	Sub     string `json:"sub,omitempty"`
	Result  string `json:"result,omitempty"`
	Enabled string `json:"enabled,omitempty"`
	MainPID int    `json:"main_pid,omitempty"`
	// StateSeconds is hoe lang de unit in zijn huidige toestand is.
	StateSeconds int64 `json:"state_seconds,omitempty"`
	// Processes zijn de processen van de engine op de node.
	Processes []HealProcess `json:"processes,omitempty"`
	// DataDir is de datamap, met de vrije en totale ruimte van zijn
	// bestandssysteem.
	DataDir   string `json:"data_dir,omitempty"`
	DataFree  uint64 `json:"data_free,omitempty"`
	DataTotal uint64 `json:"data_total,omitempty"`
	// Galera is de Galera-config, voor MariaDB en MySQL met wsrep.
	Galera *HealGalera `json:"galera,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// HealProcess is een proces van een database.
type HealProcess struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
	// Unit is de service uit zijn cgroup; leeg als hij buiten een service
	// draait.
	Unit string `json:"unit,omitempty"`
	// AgeSeconds is hoe lang het proces draait.
	AgeSeconds int64 `json:"age_seconds"`
}

// HealGalera is de geldende Galera-config van een node, met waar elke
// waarde staat.
type HealGalera struct {
	// On: wsrep_on staat aan of er is een wsrep_provider.
	On                bool   `json:"on"`
	ClusterName       string `json:"cluster_name,omitempty"`
	ClusterAddress    string `json:"cluster_address,omitempty"`
	NodeAddress       string `json:"node_address,omitempty"`
	SSTReceiveAddress string `json:"sst_receive_address,omitempty"`
	// Settings zijn de regels in de configbestanden die de adressen
	// zetten, in de volgorde waarin MariaDB ze leest; de laatste geldt.
	Settings []HealSetting `json:"settings,omitempty"`
	// PeerTimeouts zijn de adressen die de journal van de laatste vijftien
	// minuten noemt bij "connection to peer … timed out".
	PeerTimeouts []string `json:"peer_timeouts,omitempty"`
}

// HealSetting is één regel in een configbestand.
type HealSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	File  string `json:"file"`
	Line  int    `json:"line"`
}

// HealChange is één bestand dat Zelfherstel wijzigde.
type HealChange struct {
	File   string `json:"file"`
	Backup string `json:"backup"`
	// Lines zijn de nieuwe regels.
	Lines []string `json:"lines"`
}
