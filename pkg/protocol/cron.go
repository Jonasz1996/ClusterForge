package protocol

import (
	"regexp"
	"slices"
	"time"
)

// CronSince is de eerste protocolversie met cronverzoeken: de agent leest
// de cronjobs en systemd-timers op zijn node (fase 6).
const CronSince = 16

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
)

// CronTimeout is hoe lang een cronverzoek mag duren.
const CronTimeout = 30 * time.Second

type CronRequest struct {
	ID string `json:"id"`
	Op string `json:"op"`
	// Deadline: na dit tijdstip weigert de agent het verzoek.
	Deadline time.Time `json:"deadline"`
}

type CronResponse struct {
	Error string `json:"error,omitempty"`
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
