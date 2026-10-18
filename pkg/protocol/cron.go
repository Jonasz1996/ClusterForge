package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
)

// CronSince is de eerste protocolversie met cronverzoeken: de agent leest
// de cronjobs en systemd-timers op zijn node (fase 6).
const CronSince = 16

// CronLoggingSince is de eerste protocolversie met CronLogging: cron het
// einde en de exitcode van elke job laten loggen.
const CronLoggingSince = 17

// CronManagedSince is de eerste protocolversie met beheerde cronjobs:
// CronApply, CronRun en de runs als TypeCronRuns.
const CronManagedSince = 18

// CronFollowSince is de eerste protocolversie met CronOutput, CronStop en
// herhalen na een fout.
const CronFollowSince = 19

// SubjectCron draagt de cronverzoeken: cf.node.<id>.cron. Een eigen subject,
// zodat een trage journal de commando's op cmd niet ophoudt.
const SubjectCron = "cron"

const (
	TypeCronRequest = "cron_request"
	TypeCronResult  = "cron_result"
)

// Soorten cronverzoek. De agent kent alleen deze.
const (
	// CronList leest elke cronjob en timer op de node, met de laatste run
	// uit de journal. Wachtwoorden in commando's zijn gemaskeerd.
	CronList = "list"
	// CronLogging zet cron met -L 15 aan (of terug zoals het was), met een
	// drop-in voor cron.service, zodat het einde en de exitcode van elke job
	// in de journal komen. Lopende jobs draaien door; cron.service heeft
	// KillMode=process.
	CronLogging = "logging"
	// CronApply zet de volledige set beheerde jobs van de node: de
	// jobbestanden en timers komen er, of gaan weg als ze er niet meer in
	// staan. Het antwoord heeft de hash van de set.
	CronApply = "apply"
	// CronRun start een beheerde job nu, ook als de node het VIP niet heeft.
	CronRun = "run"
	// CronOutput geeft de uitvoer tot nu toe van een run (RunID) van JobID,
	// zolang de uitvoer op de node staat.
	CronOutput = "output"
	// CronStop stopt de lopende run van JobID.
	CronStop = "stop"
)

// Waar de agent de beheerde jobs bewaart.
const (
	// CronJobDir heeft per job <id>.json, de job zoals cf-agent cron-run
	// hem leest.
	CronJobDir = "/etc/clusterforge/cron"
	// CronStateDir heeft de sloten, de uitvoer van de laatste runs en de
	// wachtrij met runs voor de server.
	CronStateDir = "/var/lib/clusterforge/cron"
	// CronUnitDir is waar de timers staan: cf-cron-<id>.timer en de
	// gedeelde service cf-cron@.service.
	CronUnitDir = "/etc/systemd/system"
	// CronOutputMax is hoeveel uitvoer van een run naar de server gaat: de
	// laatste 64 KiB.
	CronOutputMax = 64 << 10
	// CronKeepOutputs is hoeveel volledige uitvoeren per job op de node
	// blijven.
	CronKeepOutputs = 10
	// MaxCronRuns is het grootste aantal runs per bericht.
	MaxCronRuns = 20
	// TypeCronRuns gaat op het subject events, als request met een Ack.
	TypeCronRuns = "cron_runs"
)

// Waar een beheerde job draait.
const (
	// CronOnVIP: op de node met het VIP van het cluster.
	CronOnVIP = "vip"
	// CronOnNode: op één vaste node.
	CronOnNode = "node"
	// CronOnAll: op elke node van het cluster.
	CronOnAll = "all"
)

// ManagedCron is een beheerde job zoals de agent hem op een node zet.
type ManagedCron struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	// Schedule is de cronregel; OnCalendar is dezelfde tijd voor systemd.
	Schedule   string `json:"schedule"`
	OnCalendar string `json:"on_calendar"`
	RunOn      string `json:"run_on"`
	// VIPs zijn de adressen waarvan deze node er één moet hebben bij
	// run_on vip.
	VIPs           []string `json:"vips,omitempty"`
	User           string   `json:"user"`
	Command        string   `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	// Retries is hoe vaak cron-run een mislukte run herhaalt (0 tot 3),
	// met RetryDelaySeconds ertussen.
	Retries           int  `json:"retries,omitempty"`
	RetryDelaySeconds int  `json:"retry_delay_seconds,omitempty"`
	Enabled           bool `json:"enabled"`
}

// Waarom een run startte.
const (
	CronReasonSchedule = "schedule"
	CronReasonManual   = "manual"
	// CronReasonRetry: een herhaling na een mislukte run.
	CronReasonRetry = "retry"
)

// Uitkomsten van een beheerde run, naast CronOK, CronFailed en CronRunning.
const (
	// CronSkipped: de vorige run liep nog; deze sloeg over.
	CronSkipped = "skipped"
	// CronTimeout: gestopt na de tijdslimiet.
	CronTimedOut = "timeout"
	// CronStopped: met de hand gestopt.
	CronStopped = "stopped"
)

// CronRunRecord is één run van een beheerde job. De agent stuurt hem bij
// de start (zonder EndedAt) en bij het einde.
type CronRunRecord struct {
	ID        string     `json:"id"`
	JobID     string     `json:"job_id"`
	Revision  int        `json:"revision"`
	Reason    string     `json:"reason"`
	Result    string     `json:"result"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Exit      *int       `json:"exit,omitempty"`
	// Output is het einde van de uitvoer, gemaskeerd.
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
}

// CronRuns is een bericht met runs.
type CronRuns struct {
	Runs []CronRunRecord `json:"runs"`
}

// CronLoggingDropIn is de drop-in die CronLogging schrijft.
const CronLoggingDropIn = "/etc/systemd/system/cron.service.d/50-clusterforge-log.conf"

// CronTimeout is hoe lang een cronverzoek mag duren.
const CronTimeout = 30 * time.Second

type CronRequest struct {
	ID string `json:"id"`
	Op string `json:"op"`
	// Deadline: na dit tijdstip weigert de agent het verzoek.
	Deadline time.Time `json:"deadline"`
	// Enable hoort bij CronLogging: aan of terug zoals het was.
	Enable bool `json:"enable,omitempty"`
	// Jobs hoort bij CronApply: de hele set van de node.
	Jobs []ManagedCron `json:"jobs,omitempty"`
	// JobID hoort bij CronRun, CronOutput en CronStop.
	JobID string `json:"job_id,omitempty"`
	// RunID hoort bij CronOutput.
	RunID string `json:"run_id,omitempty"`
}

type CronResponse struct {
	Error string `json:"error,omitempty"`
	// Output zegt bij CronLogging en CronApply wat de agent deed.
	Output []string `json:"output,omitempty"`
	// Hash is bij CronApply de hash van de toegepaste set.
	Hash string `json:"hash,omitempty"`
	// RunOutput is bij CronOutput het einde van de uitvoer (hoogstens
	// CronOutputMax), gemaskeerd.
	RunOutput          string `json:"run_output,omitempty"`
	RunOutputTruncated bool   `json:"run_output_truncated,omitempty"`
	// Daemon is de cron-daemon op de node: cron, cronie of leeg als er
	// geen draait.
	Daemon string `json:"daemon,omitempty"`
	// Logging is true als cron het einde en de exitcode van een job logt
	// (-L met bit 2 en 4); anders staat alleen de start in de journal.
	Logging bool `json:"logging,omitempty"`
	// Journal is false als de agent de journal niet kon lezen; dan zijn er
	// geen laatste runs.
	Journal bool        `json:"journal"`
	Entries []CronEntry `json:"entries"`
	// Truncated is true als er meer dan MaxCronEntries waren.
	Truncated bool `json:"truncated,omitempty"`
}

// MaxCronEntries is het grootste aantal jobs per antwoord.
const MaxCronEntries = 1000

// MaxCronCommand is de langste commandotekst die de agent stuurt.
const MaxCronCommand = 2000

// Bronnen van een cronjob.
const (
	// CronSourceCrontab: /etc/crontab.
	CronSourceCrontab = "crontab"
	// CronSourceCronD: een bestand in /etc/cron.d.
	CronSourceCronD = "cron.d"
	// CronSourceUser: de crontab van een gebruiker (crontab -e).
	CronSourceUser = "user"
	// CronSourcePeriodic: een script in /etc/cron.hourly, daily, weekly of
	// monthly; Schedule is hourly, daily, weekly of monthly.
	CronSourcePeriodic = "periodic"
	// CronSourceTimer: een systemd-timer; Schedule is zijn OnCalendar of
	// zijn monotone tijden.
	CronSourceTimer = "timer"
)

// Uitkomsten van de laatste run.
const (
	CronOK      = "ok"
	CronFailed  = "failed"
	CronRunning = "running"
	// CronStarted: cron logde de start, maar niet het einde (cron logt
	// standaard geen einde of exitcode).
	CronStarted = "started"
)

// CronEntry is één cronjob of timer.
type CronEntry struct {
	Source string `json:"source"`
	// File is het bestand of, bij een timer, de unit (zoals
	// apt-daily.timer).
	File string `json:"file"`
	// Line is het regelnummer in File; nul bij een script of timer.
	Line int    `json:"line,omitempty"`
	User string `json:"user,omitempty"`
	// Schedule is de tijdsaanduiding: vijf velden, een @-woord, een
	// periodiek woord of de OnCalendar van een timer.
	Schedule string `json:"schedule"`
	// Command is het commando, gemaskeerd; bij een timer de ExecStart van
	// de service.
	Command string `json:"command"`
	// Unit is bij een timer de service die hij start.
	Unit        string `json:"unit,omitempty"`
	Description string `json:"description,omitempty"`
	// Package is het Debian-pakket waar het bestand bij hoort; leeg voor
	// wat Jonas zelf maakte.
	Package string `json:"package,omitempty"`
	// Enabled is false voor een timer die niet aanstaat.
	Enabled bool `json:"enabled"`
	// ClusterForge is true voor een job die ClusterForge zelf beheert.
	ClusterForge bool `json:"clusterforge,omitempty"`

	LastRun *time.Time `json:"last_run,omitempty"`
	// LastEnd is wanneer de laatste run stopte, als dat bekend is.
	LastEnd *time.Time `json:"last_end,omitempty"`
	// LastExit is de exitcode van de laatste run, als die bekend is.
	LastExit *int `json:"last_exit,omitempty"`
	// LastResult is ok, failed, running of started; leeg zonder run.
	LastResult string `json:"last_result,omitempty"`
	// Runs is hoe vaak de job de laatste 24 uur startte, en Failures hoe
	// vaak dat mislukte (als cron of systemd dat zegt).
	Runs     int `json:"runs,omitempty"`
	Failures int `json:"failures,omitempty"`
	// NextRun is bij een timer wanneer systemd hem de volgende keer start.
	// Voor cron rekent de server zelf.
	NextRun *time.Time `json:"next_run,omitempty"`
}

// mysqlPatterns zijn -pGeheim en -p'Geheim' van mysql en mariadb-dump;
// alleen in een commando met mysql of mariadb, want rsync -pav is geen
// wachtwoord.
var mysqlPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(\s-p)([^\s'"-][^\s'"]*)`),
	regexp.MustCompile(`(\s-p['"])([^'"]+)`),
}

var mysqlCommand = regexp.MustCompile(`(?i)mysql|mariadb`)

var secretPatterns = []*regexp.Regexp{
	// --password=x, --pass x, --passwd=x en dergelijke.
	regexp.MustCompile(`(?i)(--?(?:password|passwd|pass|pwd|secret|token|api[-_]?key)[= ]['"]?)([^\s'";&|]+)`),
	// PASSWORD=x en PGPASSWORD=x, ook SECRET, TOKEN en KEY.
	regexp.MustCompile(`(?i)(\b[A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|TOKEN|API_?KEY)[A-Z0-9_]*=['"]?)([^\s'";&|]+)`),
	// Een URL met gebruiker en wachtwoord.
	regexp.MustCompile(`(://[^/\s:@]+:)([^@\s/]+)(@)`),
	// Een Authorization-header.
	regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic|token)\s+)([^\s'"]+)`),
}

// MaskSecrets vervangt wachtwoorden, tokens en sleutels in een commando of
// uitvoer door ***.
func MaskSecrets(s string) string {
	patterns := secretPatterns
	if mysqlCommand.MatchString(s) {
		patterns = append(slices.Clone(mysqlPatterns), patterns...)
	}
	for _, re := range patterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatch(m)
			if len(sub) < 3 || sub[2] == "***" {
				return m
			}
			out := sub[1] + "***"
			if len(sub) > 3 {
				out += sub[3]
			}
			return out
		})
	}
	return s
}

// CronSetHash is de hash van een set beheerde jobs; de server en de agent
// rekenen hem op dezelfde manier.
func CronSetHash(jobs []ManagedCron) string {
	s := slices.Clone(jobs)
	slices.SortFunc(s, func(a, b ManagedCron) int { return strings.Compare(a.ID, b.ID) })
	data, _ := json.Marshal(s)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:16])
}
