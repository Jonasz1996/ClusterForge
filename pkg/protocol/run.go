package protocol

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"time"
)

// RunSince is de eerste protocolversie met runs: een script, apt-updates of
// Docker Compose-updates die de agent los van zijn eigen proces uitvoert,
// met de uitvoer in een bestand op de node (fase 10).
const RunSince = 25

// SubjectRun draagt de runverzoeken: cf.node.<id>.run.
const SubjectRun = "run"

const (
	TypeRunRequest = "run_request"
	TypeRunResult  = "run_result"
)

// Soorten runverzoek.
const (
	// RunStart start de run in Spec. Bestaat een run met dat id al, dan
	// start de agent niets opnieuw en geeft hij de stand.
	RunStart = "start"
	// RunPoll geeft de stand en de uitvoer vanaf Offset.
	RunPoll = "poll"
	// RunStop stopt de run; het commando krijgt SIGTERM, met zijn hele
	// procesgroep.
	RunStop = "stop"
)

// Soorten run.
const (
	// RunScript draait Script als User.
	RunScript = "script"
	// RunApt werkt de pakketten bij met apt-get.
	RunApt = "apt"
	// RunDocker werkt Docker Compose-projecten bij: pull en up -d.
	RunDocker = "docker"
)

// Standen van een run.
const (
	RunRunning = "running"
	RunOK      = "ok"
	RunFailed  = "failed"
	RunTimeout = "timeout"
	RunStopped = "stopped"
)

// Waar de agent runs bewaart: per run een map met spec.json, output.log en
// status.json.
const (
	RunDir = "/var/lib/clusterforge/runs"
	// RunKeep is hoeveel afgelopen runs op de node blijven.
	RunKeep = 20
	// RunChunk is de meeste uitvoer per poll.
	RunChunk = 64 << 10
	// RunTimeoutDefault is de tijdslimiet zonder TimeoutSeconds.
	RunTimeoutDefault = 30 * time.Minute
	// RunTimeoutMax is de hoogste tijdslimiet.
	RunTimeoutMax = 24 * time.Hour
	// MaxRunScript is het langste script.
	MaxRunScript = 64 << 10
	// RunRequestTimeout is hoe lang een runverzoek mag duren.
	RunRequestTimeout = 30 * time.Second
	// RunHoldsFile onthoudt de pakketten die een apt-run tijdelijk
	// vasthield, zodat de agent ze na een crash bij zijn start loslaat.
	RunHoldsFile = "/var/lib/clusterforge/runs/holds.json"
)

// RunSpec is een run zoals de server hem vraagt.
type RunSpec struct {
	// ID is uniek per run; een uuid of een id van de server met letters,
	// cijfers en -.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Title staat bovenaan de uitvoer.
	Title          string `json:"title,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	// Script en User horen bij RunScript. Leeg User is root.
	Script string     `json:"script,omitempty"`
	User   string     `json:"user,omitempty"`
	Apt    *AptRun    `json:"apt,omitempty"`
	Docker *DockerRun `json:"docker,omitempty"`
	// PVE hoort bij RunPVE (vanaf PVESince).
	PVE *PVERun `json:"pve,omitempty"`
	// Stack hoort bij RunStack (vanaf StacksSince).
	Stack *StackRun `json:"stack,omitempty"`
}

// AptRun zegt hoe apt bijwerkt.
type AptRun struct {
	// Full is full-upgrade in plaats van upgrade: pakketten mogen er ook
	// bij komen of weg gaan.
	Full       bool `json:"full,omitempty"`
	Autoremove bool `json:"autoremove,omitempty"`
	// Hold zijn patronen (zoals mariadb-*) van geïnstalleerde pakketten die
	// tijdens deze run blijven staan; de agent zet een apt-mark hold en
	// haalt die na de run weg. Wat al vastgehouden werd, blijft dat.
	Hold []string `json:"hold,omitempty"`
	// Smart (vanaf UpdatesSince): wachten op het slot, dpkg herstellen en
	// na een dpkg-fout één keer opnieuw, nieuwe .dpkg-dist melden.
	Smart bool `json:"smart,omitempty"`
}

// DockerRun zegt welke Compose-projecten bijgewerkt worden.
type DockerRun struct {
	// Projects zijn de namen; leeg is elk project dat docker compose ls
	// kent.
	Projects []string `json:"projects,omitempty"`
	// Prune ruimt daarna de images op die niets meer gebruikt.
	Prune bool `json:"prune,omitempty"`

	// Vanaf UpdatesSince (fase 15). Zonder Smart werkt de agent zoals in
	// fase 10: pull en up -d.

	// Smart: ontdekken via docker ps -a en docker inspect, pull, alleen bij
	// een nieuw image down en up -d, testen, logs en eventueel terugzetten.
	Smart bool `json:"smart,omitempty"`
	// Always maakt de containers ook opnieuw als er geen nieuw image is.
	Always bool `json:"always,omitempty"`
	// HealthSeconds is hoe lang de test mag duren; 0 is DockerHealthDefault.
	HealthSeconds int `json:"health_seconds,omitempty"`
	// NoRollback laat een project dat de test niet haalt zoals het is, in
	// plaats van de vorige images en bestanden terug te zetten.
	NoRollback bool `json:"no_rollback,omitempty"`
	// Tags zetten een image in het compose-bestand op een nieuwe naam.
	Tags []ImageTag `json:"tags,omitempty"`
	// Edit schrijft één bestand van het project (Projects heeft dan precies
	// dat project) en start het project opnieuw.
	Edit *ComposeEdit `json:"edit,omitempty"`
	// Read leest alleen de bestanden van het project (voor de editor); er
	// verandert niets.
	Read bool `json:"read,omitempty"`
}

// ImageTag zet in project Project het image From op To, zoals
// nginx:1.27 naar nginx:1.29.
type ImageTag struct {
	Project string `json:"project"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// ComposeEdit is een nieuw compose- of env-bestand. Content heeft de
// plaatshouders «geheim-N» van ComposeFile.Content; de agent zet daar de
// echte waarden terug uit het bestand met sha256 OldSHA256.
type ComposeEdit struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	OldSHA256 string `json:"old_sha256"`
}

type RunRequest struct {
	ID       string    `json:"id"`
	Op       string    `json:"op"`
	Deadline time.Time `json:"deadline"`
	// Spec hoort bij RunStart.
	Spec *RunSpec `json:"spec,omitempty"`
	// RunID hoort bij RunPoll en RunStop.
	RunID string `json:"run_id,omitempty"`
	// Offset is bij RunPoll vanaf waar de uitvoer komt, in bytes.
	Offset int64 `json:"offset,omitempty"`
}

// RunStatus is de stand van een run.
type RunStatus struct {
	State     string     `json:"state"`
	StartedAt time.Time  `json:"started_at,omitzero"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Exit      *int       `json:"exit,omitempty"`
	// Error zegt waarom de run niet kon starten of afbrak.
	Error string `json:"error,omitempty"`
	// RebootRequired: na deze run vraagt de node om een herstart.
	RebootRequired bool `json:"reboot_required,omitempty"`
	// Held zijn de pakketten die tijdens een apt-run bleven staan.
	Held []string `json:"held,omitempty"`
	// ConfNew zijn de nieuwe standaardconfiguraties (.dpkg-dist) die een
	// apt-run naast de eigen bestanden zette; vanaf UpdatesSince.
	ConfNew []string `json:"conf_new,omitempty"`
	// Docker is het rapport per Compose-project; vanaf UpdatesSince.
	Docker []DockerReport `json:"docker,omitempty"`
	// Files zijn de bestanden van een Read-run.
	Files []ComposeFile `json:"files,omitempty"`
	// PVE is het resultaat van een run pve.
	PVE *PVERunResult `json:"pve,omitempty"`
	// Stacks is het rapport per stack van een run stack.
	Stacks []StackReport `json:"stacks,omitempty"`
}

type RunResponse struct {
	Error  string     `json:"error,omitempty"`
	Status *RunStatus `json:"status,omitempty"`
	// Output is de uitvoer vanaf de gevraagde offset tot Offset, gemaskeerd.
	Output string `json:"output,omitempty"`
	Offset int64  `json:"offset"`
	// Skipped is true als de uitvoer vóór de gevraagde offset weg is en
	// Output later begint.
	Skipped bool `json:"skipped,omitempty"`
}

// Done is true als de run afgelopen is.
func (s RunStatus) Done() bool { return s.State != "" && s.State != RunRunning }

var (
	runID       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,99}$`)
	runUser     = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	holdPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.+*-]{0,63}$`)
	projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// ValidRunID is true voor een id dat als map- en unitnaam kan.
func ValidRunID(id string) bool { return runID.MatchString(id) }

// Validate controleert een spec; de agent en de server gebruiken dezelfde
// regels.
func (s RunSpec) Validate() error {
	if !ValidRunID(s.ID) {
		return fmt.Errorf("ongeldig run-id %q", s.ID)
	}
	if s.TimeoutSeconds < 0 || time.Duration(s.TimeoutSeconds)*time.Second > RunTimeoutMax {
		return fmt.Errorf("de tijdslimiet moet tussen 0 en %s liggen", RunTimeoutMax)
	}
	set := 0
	for _, b := range []bool{s.Script != "", s.Apt != nil, s.Docker != nil, s.PVE != nil, s.Stack != nil} {
		if b {
			set++
		}
	}
	if set > 1 {
		return fmt.Errorf("een run is een script, apt, docker of pve, niet meer tegelijk")
	}
	switch s.Kind {
	case RunScript:
		if s.Script == "" {
			return fmt.Errorf("het script is leeg")
		}
		if len(s.Script) > MaxRunScript {
			return fmt.Errorf("het script is langer dan %d bytes", MaxRunScript)
		}
		if s.User != "" && !runUser.MatchString(s.User) {
			return fmt.Errorf("ongeldige gebruiker %q", s.User)
		}
	case RunApt:
		if s.Apt == nil {
			return fmt.Errorf("apt-run zonder instellingen")
		}
		for _, p := range s.Apt.Hold {
			if !holdPattern.MatchString(p) {
				return fmt.Errorf("ongeldig pakketpatroon %q", p)
			}
		}
	case RunDocker:
		if s.Docker == nil {
			return fmt.Errorf("docker-run zonder instellingen")
		}
		for _, p := range s.Docker.Projects {
			if !projectName.MatchString(p) {
				return fmt.Errorf("ongeldige projectnaam %q", p)
			}
		}
		if slices.Contains(s.Docker.Projects, "") {
			return fmt.Errorf("lege projectnaam")
		}
		if err := s.Docker.validate(); err != nil {
			return err
		}
	case RunStack:
		if err := s.Stack.Validate(); err != nil {
			return err
		}
	case RunPVE:
		if s.PVE == nil {
			return fmt.Errorf("pve-run zonder actie")
		}
		if err := s.PVE.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("onbekende soort run %q", s.Kind)
	}
	return nil
}

// Timeout is de tijdslimiet van de run.
func (s RunSpec) Timeout() time.Duration {
	if s.TimeoutSeconds <= 0 {
		return RunTimeoutDefault
	}
	return time.Duration(s.TimeoutSeconds) * time.Second
}

// ComposeProject is een Docker Compose-project op de node, zoals docker
// compose ls het meldt, of vanaf UpdatesSince zoals docker inspect het
// laat zien.
type ComposeProject struct {
	Name string `json:"name"`
	// Status is zoals "running(3)" of "exited(1)".
	Status      string   `json:"status"`
	ConfigFiles []string `json:"config_files,omitempty"`
	// Vanaf UpdatesSince.
	WorkingDir string           `json:"working_dir,omitempty"`
	EnvFiles   []string         `json:"env_files,omitempty"`
	Services   []ComposeService `json:"services,omitempty"`
	// Missing zijn compose-bestanden die niet meer bestaan.
	Missing []string `json:"missing,omitempty"`
}

// ComposeService is één container van een project.
type ComposeService struct {
	Service string `json:"service"`
	// Container is de naam van de container.
	Container string `json:"container"`
	Image     string `json:"image"`
	ImageID   string `json:"image_id,omitempty"`
	// State is running, exited, restarting, …; Health is healthy,
	// unhealthy, starting of leeg zonder healthcheck.
	State    string `json:"state"`
	Health   string `json:"health,omitempty"`
	Restarts int    `json:"restarts,omitempty"`
	// Mounts zijn de gemapte mappen en volumes, als "bron:doel" (":ro" erbij
	// als alleen lezen).
	Mounts []string `json:"mounts,omitempty"`
}

var imageRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,254}$`)

// ValidImageRef is true voor een imagenaam zoals nginx:1.29 of
// ghcr.io/owner/app@sha256:…; geen spaties of aanhalingstekens.
func ValidImageRef(s string) bool { return imageRef.MatchString(s) }

func (d *DockerRun) validate() error {
	if d.HealthSeconds < 0 || time.Duration(d.HealthSeconds)*time.Second > DockerHealthMax {
		return fmt.Errorf("de test moet tussen 0 en %s duren", DockerHealthMax)
	}
	if (len(d.Tags) > 0 || d.Edit != nil || d.Read) && !d.Smart {
		return fmt.Errorf("tags, de editor en lezen vragen de Docker-update van fase 15")
	}
	for _, t := range d.Tags {
		if !projectName.MatchString(t.Project) {
			return fmt.Errorf("ongeldige projectnaam %q", t.Project)
		}
		if !ValidImageRef(t.From) || !ValidImageRef(t.To) {
			return fmt.Errorf("ongeldige imagenaam %q of %q", t.From, t.To)
		}
	}
	if d.Edit != nil || d.Read {
		if len(d.Projects) != 1 {
			return fmt.Errorf("het compose-bestand lezen of aanpassen gaat per project")
		}
		if d.Edit != nil && d.Read {
			return fmt.Errorf("lezen of aanpassen, niet allebei")
		}
	}
	if e := d.Edit; e != nil {
		if !path.IsAbs(e.Path) || path.Clean(e.Path) != e.Path {
			return fmt.Errorf("ongeldig pad %q", e.Path)
		}
		if len(e.Content) > MaxComposeFile {
			return fmt.Errorf("het bestand is groter dan %d bytes", MaxComposeFile)
		}
		if len(e.OldSHA256) != 64 {
			return fmt.Errorf("de sha256 van het gelezen bestand ontbreekt")
		}
	}
	return nil
}
