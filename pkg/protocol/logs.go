package protocol

import (
	"path"
	"regexp"
	"strings"
	"time"
)

// Logs (fase 23): de agent leest logs op zijn node, zodat de beheerder ze in
// de GUI kan bekijken, filteren en tussen nodes vergelijken. Drie soorten
// bron: een bestand onder /var/log, het systemd-journaal (heel het journaal
// of één unit) en de log van een Docker-container. De agent leest alleen; hij
// schrijft niets en bewaart niets. Geheimen maskeert hij met MaskSecrets
// voor hij antwoordt.

// LogsSince is de eerste protocolversie met het subject logs.
const LogsSince = 34

// SubjectLogs draagt de logverzoeken: cf.node.<id>.logs.
const SubjectLogs = "logs"

const (
	TypeLogsRequest = "logs_request"
	TypeLogsResult  = "logs_result"
)

// Soorten logverzoek.
const (
	// LogsOpSources geeft de bronnen op de node: bestanden onder /var/log,
	// de systemd-units in het journaal en de Docker-containers.
	LogsOpSources = "sources"
	// LogsOpRead leest één bron met een filter.
	LogsOpRead = "read"
)

// Soorten bron.
const (
	LogKindFile    = "file"
	LogKindJournal = "journal"
	LogKindDocker  = "docker"
)

// Niveaus, van ernstig naar onbelangrijk. Een regel zonder herkenbaar niveau
// krijgt LevelUnknown.
const (
	LevelError   = "error"
	LevelWarn    = "warn"
	LevelInfo    = "info"
	LevelDebug   = "debug"
	LevelUnknown = ""
)

const (
	// LogDir is de enige map waaruit de agent bestanden leest.
	LogDir = "/var/log"
	// LogsRequestTimeout is hoe lang een logverzoek mag duren.
	LogsRequestTimeout = 40 * time.Second
	// DefaultLogLimit is het aantal regels zonder limiet.
	DefaultLogLimit = 500
	// MaxLogLimit is het meeste aantal regels per bron.
	MaxLogLimit = 5000
	// MaxLogBytes is de som van de berichten in één antwoord; het antwoord
	// moet onder de grens van NATS (1 MB) blijven.
	MaxLogBytes = 640 << 10
	// MaxLogLine is de langste regel; de rest valt weg.
	MaxLogLine = 4 << 10
	// MaxLogScan is hoeveel bytes van een bestand de agent hoogstens leest
	// (van achteren naar voren) om genoeg passende regels te vinden.
	MaxLogScan = 64 << 20
	// MaxLogScanLines is hoeveel regels van het journaal of een container de
	// agent hoogstens bekijkt om genoeg passende regels te vinden.
	MaxLogScanLines = 200000
	// MaxLogFiles is het meeste aantal bestanden in de bronnenlijst.
	MaxLogFiles = 400
	// MaxLogPattern is de langste zoekterm.
	MaxLogPattern = 500
)

// LogSource is één bron op een node.
type LogSource struct {
	Kind string `json:"kind"`
	// Name is het pad (file), de unit (journal; leeg is heel het journaal)
	// of de containernaam (docker).
	Name string `json:"name"`
}

// String is de bron zoals de GUI hem toont.
func (s LogSource) String() string {
	switch s.Kind {
	case LogKindJournal:
		if s.Name == "" {
			return "journaal"
		}
		return "journaal: " + s.Name
	case LogKindDocker:
		return "container: " + s.Name
	}
	return s.Name
}

// LogFilter beperkt welke regels de agent geeft.
type LogFilter struct {
	// Since en Until begrenzen de tijd; nul is open.
	Since time.Time `json:"since,omitzero"`
	Until time.Time `json:"until,omitzero"`
	// Pattern is een zoekterm; met Regex een reguliere expressie (RE2).
	Pattern    string `json:"pattern,omitempty"`
	Regex      bool   `json:"regex,omitempty"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
	// Exclude laat regels weg die deze term bevatten (zelfde Regex en
	// IgnoreCase).
	Exclude string `json:"exclude,omitempty"`
	// Levels zijn de niveaus die meekomen; leeg is alle. LevelUnknown ("")
	// in de lijst laat ook regels zonder niveau toe.
	Levels []string `json:"levels,omitempty"`
	// Limit is het aantal regels (de nieuwste); 0 is DefaultLogLimit.
	Limit int `json:"limit,omitempty"`
}

// LogsRequest is een logverzoek van de server.
type LogsRequest struct {
	ID       string    `json:"id"`
	Op       string    `json:"op"`
	Deadline time.Time `json:"deadline"`
	// Source en Filter horen bij LogsOpRead.
	Source LogSource `json:"source,omitzero"`
	Filter LogFilter `json:"filter,omitzero"`
}

// LogFileInfo is een logbestand in de bronnenlijst.
type LogFileInfo struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Gzip is een geroteerd, gecomprimeerd bestand (.gz).
	Gzip bool `json:"gzip,omitempty"`
}

// LogContainerInfo is een container in de bronnenlijst.
type LogContainerInfo struct {
	Name    string `json:"name"`
	Image   string `json:"image,omitempty"`
	State   string `json:"state,omitempty"`
	Project string `json:"project,omitempty"`
}

// LogEntry is één logregel, al ontleed.
type LogEntry struct {
	// TS is de tijd van de regel; nul als de regel er geen heeft (dan
	// erft hij in de GUI de tijd van de regel ervoor).
	TS time.Time `json:"ts,omitzero"`
	// Level is het niveau (LevelError…), of leeg.
	Level string `json:"level,omitempty"`
	// Proc is het programma of de unit (syslog-tag, SYSLOG_IDENTIFIER,
	// stream van een container).
	Proc string `json:"proc,omitempty"`
	// Msg is het bericht zonder tijd en tag.
	Msg string `json:"msg"`
}

// LogsResponse is het antwoord van de agent.
type LogsResponse struct {
	Error string `json:"error,omitempty"`
	// Bij LogsOpSources.
	Files      []LogFileInfo      `json:"files,omitempty"`
	Units      []string           `json:"units,omitempty"`
	Containers []LogContainerInfo `json:"containers,omitempty"`
	Journal    bool               `json:"journal,omitempty"`
	Docker     bool               `json:"docker,omitempty"`
	// Bij LogsOpRead: de regels, oud naar nieuw.
	Entries []LogEntry `json:"entries,omitempty"`
	// Truncated: er waren meer passende regels dan Limit of MaxLogBytes,
	// of de agent stopte bij MaxLogScan/MaxLogScanLines.
	Truncated bool `json:"truncated,omitempty"`
	// Scanned is hoeveel regels de agent bekeek.
	Scanned int `json:"scanned,omitempty"`
}

var (
	unitRe    = regexp.MustCompile(`^[A-Za-z0-9:_.@\\-]{1,200}$`)
	logPathRe = regexp.MustCompile(`^/var/log/[A-Za-z0-9._@:+/-]{1,240}$`)
)

// ValidLogPath is true voor een schoon, absoluut pad onder LogDir.
func ValidLogPath(p string) bool {
	return logPathRe.MatchString(p) && path.Clean(p) == p && !strings.Contains(p, "/../")
}

// ValidUnit is true voor een unitnaam (zonder spaties of opties).
func ValidUnit(s string) bool { return unitRe.MatchString(s) && !strings.HasPrefix(s, "-") }

// ValidLogSource controleert een bron; de fout is in het Nederlands.
func ValidLogSource(s LogSource) string {
	switch s.Kind {
	case LogKindFile:
		if !ValidLogPath(s.Name) {
			return "alleen bestanden onder /var/log"
		}
	case LogKindJournal:
		if s.Name != "" && !ValidUnit(s.Name) {
			return "ongeldige unit"
		}
	case LogKindDocker:
		if !ValidContainerName(s.Name) {
			return "ongeldige containernaam"
		}
	default:
		return "onbekende soort bron " + s.Kind
	}
	return ""
}

// ValidLevel is true voor een bekend niveau (ook leeg).
func ValidLevel(l string) bool {
	switch l {
	case LevelError, LevelWarn, LevelInfo, LevelDebug, LevelUnknown:
		return true
	}
	return false
}

// CompileLogPattern maakt de matcher van een zoekterm; nil bij een lege
// term.
func CompileLogPattern(p string, isRegex, ignoreCase bool) (*regexp.Regexp, error) {
	if p == "" {
		return nil, nil
	}
	if !isRegex {
		p = regexp.QuoteMeta(p)
	}
	if ignoreCase {
		p = "(?i)" + p
	}
	return regexp.Compile(p)
}

// CheckLogFilter controleert een filter; de fout is in het Nederlands.
func CheckLogFilter(f LogFilter) string {
	if len(f.Pattern) > MaxLogPattern || len(f.Exclude) > MaxLogPattern {
		return "zoekterm te lang"
	}
	if _, err := CompileLogPattern(f.Pattern, f.Regex, f.IgnoreCase); err != nil {
		return "ongeldige reguliere expressie: " + err.Error()
	}
	if _, err := CompileLogPattern(f.Exclude, f.Regex, f.IgnoreCase); err != nil {
		return "ongeldige reguliere expressie bij uitsluiten: " + err.Error()
	}
	for _, l := range f.Levels {
		if !ValidLevel(l) {
			return "onbekend niveau " + l
		}
	}
	if f.Limit < 0 || f.Limit > MaxLogLimit {
		return "limiet tussen 1 en 5000"
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && f.Until.Before(f.Since) {
		return "het einde ligt voor het begin"
	}
	return ""
}
