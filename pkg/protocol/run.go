package protocol

import (
	"fmt"
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
}

// DockerRun zegt welke Compose-projecten bijgewerkt worden.
type DockerRun struct {
	// Projects zijn de namen; leeg is elk project dat docker compose ls
	// kent.
	Projects []string `json:"projects,omitempty"`
	// Prune ruimt daarna de images op die niets meer gebruikt.
	Prune bool `json:"prune,omitempty"`
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
	for _, b := range []bool{s.Script != "", s.Apt != nil, s.Docker != nil} {
		if b {
			set++
		}
	}
	if set > 1 {
		return fmt.Errorf("een run is een script, apt of docker, niet meer tegelijk")
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
// compose ls het meldt.
type ComposeProject struct {
	Name string `json:"name"`
	// Status is zoals "running(3)" of "exited(1)".
	Status      string   `json:"status"`
	ConfigFiles []string `json:"config_files,omitempty"`
}
