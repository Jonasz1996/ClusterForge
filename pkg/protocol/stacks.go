package protocol

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Docker-stacks (fase 21): ClusterForge beheert de Docker Compose-projecten
// van een cluster als gewenste staat. De server bewaart per stack de
// bestanden (compose, env en kleine configuratie) en waar hij draait; de
// agent schrijft ze, brengt de containers in de gewenste stand, leest de
// containers en hun logs, en zet de lijsten voor de keepalived-check van de
// template docker-ha.

// StacksSince is de eerste protocolversie met stacks: het subject stacks
// (lijst, logs, actie), runs van de soort stack en de pauze bij een sync
// (SyncPausePrefix).
const StacksSince = 33

// SubjectStacks draagt de stackverzoeken: cf.node.<id>.stacks.
const SubjectStacks = "stacks"

const (
	TypeStacksRequest = "stacks_request"
	TypeStacksResult  = "stacks_result"
)

// RunStack brengt stacks in hun gewenste stand (een run van de soort stack).
const RunStack = "stack"

// Soorten stackverzoek.
const (
	// StacksOpList geeft elke container en elk Compose-project; met Files
	// en Project ook de bestanden van dat project (voor de overname).
	StacksOpList = "list"
	// StacksOpLogs geeft de laatste regels van een container.
	StacksOpLogs = "logs"
)

const (
	// StacksRequestTimeout is hoe lang een stackverzoek mag duren.
	StacksRequestTimeout = 45 * time.Second
	// MaxStackFile is het grootste bestand van een stack.
	MaxStackFile = 256 << 10
	// MaxStackFiles is het meeste aantal bestanden van een stack.
	MaxStackFiles = 40
	// MaxStackBytes is de som van de bestanden van een stack; een antwoord
	// van de agent moet onder de grens van NATS (1 MB) blijven.
	MaxStackBytes = 640 << 10
	// MaxStackLog is de meeste log die een logsverzoek geeft.
	MaxStackLog = 128 << 10
	// StackListFile is de lijst van de stacks op deze node, voor
	// docker-ha.sh (keepalived): per regel naam, map, compose-bestanden
	// (komma's), plaatsing en kritiek (0/1), met tabs ertussen.
	StackListFile = "/etc/clusterforge/stacks.list"
	// SyncPausePrefix begint de voorbereiding (Before) van een sync die
	// stacks pauzeert: "cf-agent stack-pause <project> [<project>…]". De
	// agent kopieert dan eerst terwijl alles draait, pauzeert de containers
	// van die projecten, kopieert nog eens (alleen wat intussen veranderde)
	// en laat ze weer lopen. Zo is de kopie van een database in een
	// container consistent, zoals na een stroomonderbreking.
	SyncPausePrefix = "cf-agent stack-pause "
	// MaxStackPause is hoe lang de containers hoogstens gepauzeerd blijven.
	MaxStackPause = 10 * time.Minute
)

// Plaatsing van een stack in zijn cluster.
const (
	// PlaceAll: op elke node van het cluster, elk met zijn eigen data.
	PlaceAll = "alle"
	// PlaceVIP: alleen op de node met het VIP; de andere nodes hebben de
	// bestanden en (met een sync) de data, en starten de stack zodra ze het
	// VIP krijgen.
	PlaceVIP = "vip"
	// PlaceNode: alleen op de gekozen nodes.
	PlaceNode = "node"
)

// Wat een run met een stack doet.
const (
	// StackUp schrijft de bestanden en start de stack (docker compose up
	// -d); met Pull eerst nieuwe images.
	StackUp = "up"
	// StackStop schrijft de bestanden en stopt de containers (ze blijven
	// bestaan).
	StackStop = "stop"
	// StackFiles schrijft alleen de bestanden; de containers blijven zoals
	// ze zijn.
	StackFiles = "files"
	// StackRestart herstart de containers (docker compose restart).
	StackRestart = "restart"
	// StackDown haalt de containers en netwerken weg (docker compose
	// down); de data en de bestanden blijven.
	StackDown = "down"
)

// Resultaten van een stack in een run.
const (
	StackResultOK        = "ok"
	StackResultFailed    = "failed"
	StackResultUnchanged = "unchanged"
)

// StacksRequest is een stackverzoek van de server.
type StacksRequest struct {
	ID       string    `json:"id"`
	Op       string    `json:"op"`
	Deadline time.Time `json:"deadline"`
	// Project en Files horen bij StacksOpList: Files geeft de bestanden
	// van Project mee.
	Project string `json:"project,omitempty"`
	Files   bool   `json:"files,omitempty"`
	// Container en Tail horen bij StacksOpLogs.
	Container string `json:"container,omitempty"`
	Tail      int    `json:"tail,omitempty"`
}

// StacksResponse is het antwoord van de agent.
type StacksResponse struct {
	Error string `json:"error,omitempty"`
	// Docker is false als Docker niet op de node staat of niet antwoordt.
	Docker     bool               `json:"docker"`
	Containers []StackContainer   `json:"containers,omitempty"`
	Projects   []StackProjectInfo `json:"projects,omitempty"`
	// Files zijn de bestanden van Project (met Files); echte waarden, de
	// server versleutelt ze meteen.
	Files []StackFile `json:"files,omitempty"`
	// Skipped zijn bestanden in de map van het project die niet
	// meekwamen (te groot, binair), met de reden.
	Skipped []string `json:"skipped,omitempty"`
	Logs    string   `json:"logs,omitempty"`
	// Lists is de inhoud van StackListFile.
	Lists string `json:"lists,omitempty"`
}

// StackContainer is één container op de node.
type StackContainer struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Image    string `json:"image"`
	State    string `json:"state"`
	Health   string `json:"health,omitempty"`
	Restarts int    `json:"restarts,omitempty"`
	// Status is zoals docker ps het zegt: "Up 4 days (healthy)".
	Status  string `json:"status,omitempty"`
	Project string `json:"project,omitempty"`
	Service string `json:"service,omitempty"`
	// Ports zijn de gepubliceerde poorten, zoals 8080->80/tcp.
	Ports   []string     `json:"ports,omitempty"`
	Mounts  []StackMount `json:"mounts,omitempty"`
	Network string       `json:"network,omitempty"`
	Restart string       `json:"restart,omitempty"`
	// StartedAt is wanneer de container laatst startte.
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// StackMount is een gemapte map of een volume van een container.
type StackMount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}

// StackProjectInfo is een Compose-project op de node.
type StackProjectInfo struct {
	Name        string   `json:"name"`
	WorkingDir  string   `json:"working_dir"`
	ConfigFiles []string `json:"config_files"`
	EnvFiles    []string `json:"env_files,omitempty"`
	Status      string   `json:"status"`
	Missing     []string `json:"missing,omitempty"`
	// Managed: ClusterForge beheert dit project (het staat in de lijst).
	Managed bool `json:"managed,omitempty"`
}

// StackFile is een bestand van een stack; Path is relatief aan de map van
// de stack.
type StackFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Mode zijn de rechten, zoals 0644; 0 is 0644.
	Mode uint32 `json:"mode,omitempty"`
}

// StackRun is een run van de soort stack.
type StackRun struct {
	Stacks []StackApply `json:"stacks"`
	// List is de nieuwe StackListFile van deze node; nil laat hem staan.
	List *string `json:"list,omitempty"`
	// HealthSeconds is hoe lang de test na up mag duren; 0 is
	// DockerHealthDefault.
	HealthSeconds int `json:"health_seconds,omitempty"`
}

// StackApply is wat een run met één stack doet.
type StackApply struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	// ComposeFiles zijn de compose-bestanden, relatief aan Dir.
	ComposeFiles []string `json:"compose_files"`
	// Files worden geschreven (als ze anders zijn) vóór Want; leeg laat de
	// bestanden zoals ze zijn.
	Files []StackFile `json:"files,omitempty"`
	Want  string      `json:"want"`
	// Pull haalt eerst nieuwe images (bijwerken).
	Pull bool `json:"pull,omitempty"`
	// Services beperkt Want tot deze diensten; leeg is de hele stack.
	Services []string `json:"services,omitempty"`
}

// StackReport is wat een run met één stack deed.
type StackReport struct {
	Name    string   `json:"name"`
	Result  string   `json:"result"`
	Problem string   `json:"problem,omitempty"`
	Written []string `json:"written,omitempty"`
	// States zijn de containers na de run: naam → toestand.
	States map[string]string `json:"states,omitempty"`
}

var (
	stackDirRe  = regexp.MustCompile(`^/[A-Za-z0-9._/+@-]{1,250}$`)
	stackFileRe = regexp.MustCompile(`^[A-Za-z0-9._@+-][A-Za-z0-9._/@+-]{0,250}$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	serviceRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// ValidStackName is true voor een naam die docker compose als project
// aanvaardt.
func ValidStackName(s string) bool { return projectName.MatchString(s) }

// ValidContainerName is true voor een containernaam.
func ValidContainerName(s string) bool { return containerRe.MatchString(s) }

// forbiddenDirs zijn mappen die nooit een stack zijn.
var forbiddenDirs = []string{"/", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var", "/var/lib", "/var/lib/docker", "/tmp"}

// ValidStackDir is true voor een absolute, schone map die een stack mag
// zijn: geen systeemmap en niet in /proc, /sys of /dev.
func ValidStackDir(d string) bool {
	if !stackDirRe.MatchString(d) || path.Clean(d) != d || slices.Contains(forbiddenDirs, d) {
		return false
	}
	for _, p := range []string{"/proc/", "/sys/", "/dev/", "/run/", "/boot/", "/usr/", "/etc/clusterforge/"} {
		if strings.HasPrefix(d+"/", p) {
			return false
		}
	}
	return true
}

// ValidStackFile is true voor een relatief pad in de map van een stack,
// zonder .. of een absolute vorm.
func ValidStackFile(p string) bool {
	if !stackFileRe.MatchString(p) || path.Clean(p) != p || strings.HasPrefix(p, "../") || p == ".." {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return false
		}
	}
	return true
}

// Validate controleert de bestanden van een stack.
func ValidateStackFiles(files []StackFile) error {
	if len(files) > MaxStackFiles {
		return fmt.Errorf("hoogstens %d bestanden per stack", MaxStackFiles)
	}
	total := 0
	seen := map[string]bool{}
	for _, f := range files {
		switch {
		case !ValidStackFile(f.Path):
			return fmt.Errorf("ongeldig bestand %q (een pad in de map van de stack)", f.Path)
		case seen[f.Path]:
			return fmt.Errorf("%s staat er twee keer in", f.Path)
		case len(f.Content) > MaxStackFile:
			return fmt.Errorf("%s is groter dan %d bytes", f.Path, MaxStackFile)
		case strings.ContainsRune(f.Content, 0):
			return fmt.Errorf("%s is geen tekstbestand", f.Path)
		case f.Mode&^0o777 != 0:
			return fmt.Errorf("%s: ongeldige rechten %o", f.Path, f.Mode)
		}
		seen[f.Path] = true
		total += len(f.Content)
	}
	if total > MaxStackBytes {
		return fmt.Errorf("de bestanden van een stack samen zijn hoogstens %d KB", MaxStackBytes>>10)
	}
	return nil
}

// Validate controleert een stackrun; de agent en de server gebruiken
// dezelfde regels.
func (r *StackRun) Validate() error {
	if r == nil {
		return fmt.Errorf("stack-run zonder stacks")
	}
	if len(r.Stacks) == 0 && r.List == nil {
		return fmt.Errorf("stack-run zonder stacks")
	}
	if r.HealthSeconds < 0 || time.Duration(r.HealthSeconds)*time.Second > DockerHealthMax {
		return fmt.Errorf("de test duurt hoogstens %s", DockerHealthMax)
	}
	if r.List != nil {
		if len(*r.List) > 64<<10 || strings.ContainsRune(*r.List, 0) {
			return fmt.Errorf("ongeldige stacklijst")
		}
	}
	seen := map[string]bool{}
	for _, s := range r.Stacks {
		switch {
		case !ValidStackName(s.Name):
			return fmt.Errorf("ongeldige stacknaam %q", s.Name)
		case seen[s.Name]:
			return fmt.Errorf("stack %s staat er twee keer in", s.Name)
		case !ValidStackDir(s.Dir):
			return fmt.Errorf("%s: ongeldige map %q", s.Name, s.Dir)
		case len(s.ComposeFiles) == 0:
			return fmt.Errorf("%s: geen compose-bestand", s.Name)
		}
		seen[s.Name] = true
		for _, f := range s.ComposeFiles {
			if !ValidStackFile(f) {
				return fmt.Errorf("%s: ongeldig compose-bestand %q", s.Name, f)
			}
		}
		for _, sv := range s.Services {
			if !serviceRe.MatchString(sv) {
				return fmt.Errorf("%s: ongeldige dienst %q", s.Name, sv)
			}
		}
		switch s.Want {
		case StackUp, StackStop, StackFiles, StackRestart, StackDown:
		default:
			return fmt.Errorf("%s: onbekende actie %q", s.Name, s.Want)
		}
		if err := ValidateStackFiles(s.Files); err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return nil
}

// StackListLine is één regel van StackListFile.
type StackListLine struct {
	Name         string
	Dir          string
	ComposeFiles []string
	Placement    string
	Critical     bool
}

// FormatStackList maakt de inhoud van StackListFile.
func FormatStackList(lines []StackListLine) string {
	var b strings.Builder
	b.WriteString("# Beheerd door ClusterForge (Containers). naam\tmap\tcompose-bestanden\tplaatsing\tkritiek\n")
	for _, l := range lines {
		crit := "0"
		if l.Critical {
			crit = "1"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", l.Name, l.Dir, strings.Join(l.ComposeFiles, ","), l.Placement, crit)
	}
	return b.String()
}

// ParseStackList leest StackListFile; ongeldige regels slaat het over.
func ParseStackList(s string) []StackListLine {
	var out []StackListLine
	for line := range strings.SplitSeq(s, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 5 || !ValidStackName(f[0]) || !ValidStackDir(f[1]) {
			continue
		}
		out = append(out, StackListLine{Name: f[0], Dir: f[1], ComposeFiles: strings.Split(f[2], ","), Placement: f[3], Critical: f[4] == "1"})
	}
	return out
}

// PauseProjects leest de projecten uit de voorbereiding van een sync die
// met SyncPausePrefix begint; ok is false voor een gewone voorbereiding.
func PauseProjects(before string) ([]string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(before)+" ", SyncPausePrefix)
	if !ok {
		return nil, false
	}
	var out []string
	for f := range strings.FieldsSeq(rest) {
		if ValidStackName(f) && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out, len(out) > 0
}

// --- geheimen in de editor ---------------------------------------------------

// secretLine herkent een sleutel die op een wachtwoord lijkt, in YAML
// (KEY: waarde, - KEY=waarde) en in een env-bestand (KEY=waarde).
var secretLine = regexp.MustCompile(`(?i)^(\s*(?:-\s*)?["']?(?:export\s+)?[A-Za-z0-9_.-]*(?:PASS|PWD|SECRET|TOKEN|API_?KEY|PRIVATE_?KEY|ACCESS_?KEY|CREDENTIAL|AUTH_?KEY|HMAC)[A-Za-z0-9_.-]*["']?\s*[:=]\s*)(\S.*?)(\s*)$`)

// MaskFile vervangt de geheime waarden door «geheim-N» en geeft ze terug.
func MaskFile(content string) (string, []string) {
	lines := strings.Split(content, "\n")
	var secrets []string
	for i, l := range lines {
		m := secretLine.FindStringSubmatchIndex(l)
		if m == nil {
			continue
		}
		val := l[m[4]:m[5]]
		if strings.HasPrefix(val, "${") || strings.HasPrefix(val, SecretPlaceholder) || val == `""` || val == "''" || strings.HasPrefix(val, "#") || val == "|" || val == ">" {
			continue
		}
		secrets = append(secrets, val)
		lines[i] = l[:m[4]] + fmt.Sprintf("%s%d»", SecretPlaceholder, len(secrets)) + l[m[5]:]
	}
	return strings.Join(lines, "\n"), secrets
}

var placeholderRe = regexp.MustCompile(regexp.QuoteMeta(SecretPlaceholder) + `(\d+)»`)

// UnmaskFile zet de echte waarden terug in plaats van de plaatshouders.
func UnmaskFile(content string, secrets []string) (string, error) {
	var bad error
	out := placeholderRe.ReplaceAllStringFunc(content, func(m string) string {
		n, _ := strconv.Atoi(placeholderRe.FindStringSubmatch(m)[1])
		if n < 1 || n > len(secrets) {
			bad = fmt.Errorf("de plaatshouder %s bestaat niet in het bewaarde bestand", m)
			return m
		}
		return secrets[n-1]
	})
	return out, bad
}
