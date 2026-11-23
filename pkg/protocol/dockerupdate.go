package protocol

import "time"

// UpdatesSince is de eerste protocolversie die apt herstelt en wacht op het
// slot, en Docker Compose bijwerkt met ontdekken via docker inspect, testen,
// logs, terugzetten, tags en het compose-bestand in de GUI (fase 15).
const UpdatesSince = 29

const (
	// DockerHealthDefault is hoe lang de test na up -d standaard mag duren.
	DockerHealthDefault = 2 * time.Minute
	// DockerHealthMax is de langste test.
	DockerHealthMax = 15 * time.Minute
	// DockerStable is hoe lang een container aan één stuk moet draaien.
	DockerStable = 20 * time.Second
	// MaxComposeFile is het grootste bestand dat de editor leest of schrijft.
	MaxComposeFile = 256 << 10
	// MaxDockerLog is de meeste log per project in het rapport.
	MaxDockerLog = 16 << 10
	// SecretPlaceholder begint een plaatshouder voor een geheime waarde in
	// de editor: «geheim-1», «geheim-2», …
	SecretPlaceholder = "«geheim-"
)

// DockerReport is wat een Docker-run met een project deed.
type DockerReport struct {
	Project     string   `json:"project"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	ConfigFiles []string `json:"config_files,omitempty"`
	EnvFiles    []string `json:"env_files,omitempty"`
	// Result is up_to_date (geen nieuw image), updated, failed of
	// rolled_back.
	Result string `json:"result"`
	// Problem zegt in gewone taal wat er misging.
	Problem  string          `json:"problem,omitempty"`
	Services []ServiceReport `json:"services,omitempty"`
	// Logs zijn de laatste regels van docker compose logs, gemaskeerd.
	Logs string `json:"logs,omitempty"`
	// Edited zijn de bestanden die de run aanpaste (tags of de editor).
	Edited []string `json:"edited,omitempty"`
	// Unchanged: het project mislukte vóór er iets veranderde (zoals een
	// pull die faalde); het draait zoals voordien. Een update van een
	// cluster gaat dan door met de rest.
	Unchanged bool `json:"unchanged,omitempty"`
}

// Resultaten van een project.
const (
	DockerUpToDate   = "up_to_date"
	DockerUpdated    = "updated"
	DockerFailed     = "failed"
	DockerRolledBack = "rolled_back"
)

// ServiceReport is één dienst van een project, voor en na.
type ServiceReport struct {
	Service  string `json:"service"`
	Image    string `json:"image"`
	OldID    string `json:"old_id,omitempty"`
	NewID    string `json:"new_id,omitempty"`
	State    string `json:"state,omitempty"`
	Health   string `json:"health,omitempty"`
	Restarts int    `json:"restarts,omitempty"`
	// OldVersion en NewVersion komen uit het label
	// org.opencontainers.image.version; Source uit
	// org.opencontainers.image.source.
	OldVersion string `json:"old_version,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
	Source     string `json:"source,omitempty"`
	// EnvAdded en EnvRemoved zijn de namen van omgevingsvariabelen die het
	// nieuwe image erbij heeft of niet meer heeft (geen waarden).
	EnvAdded   []string `json:"env_added,omitempty"`
	EnvRemoved []string `json:"env_removed,omitempty"`
}

// ComposeFile is een bestand van een project zoals de editor het toont:
// geheime waarden zijn vervangen door plaatshouders.
type ComposeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
	// Env is true voor een env-bestand.
	Env bool `json:"env,omitempty"`
	// Secrets is het aantal plaatshouders.
	Secrets int `json:"secrets,omitempty"`
}
