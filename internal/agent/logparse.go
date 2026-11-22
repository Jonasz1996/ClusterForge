package agent

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Ontleden van logregels (fase 23). De agent haalt uit elke regel de tijd,
// het niveau en het programma, zodat de GUI kan filteren en de regels van
// verschillende nodes op één tijdlijn kan leggen. Wat hij niet herkent blijft
// gewoon tekst; een regel zonder tijd is meestal het vervolg van de regel
// ervoor (een stacktrace) en hoort bij diens record.

var (
	// 2026-11-16T12:00:00.123456+01:00 (rsyslog op Debian 13), ook met
	// spatie of komma, en 2026/11/16 12:00:00 (nginx error, Go-log).
	isoTSRe = regexp.MustCompile(`^\[?(\d{4})[-/](\d{2})[-/](\d{2})[T ](\d{2}):(\d{2}):(\d{2})(?:[.,](\d{1,9}))?(Z|[+-]\d{2}:?\d{2})?\]?\s*`)
	// Nov 16 12:00:00 (klassieke syslog), met of zonder dagnaam ervoor.
	syslogTSRe = regexp.MustCompile(`^(?:[A-Z][a-z]{2} )?([A-Z][a-z]{2}) {1,2}(\d{1,2}) (\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?\s+`)
	// [16/Nov/2026:12:00:00 +0100] (nginx/apache access).
	clfTSRe = regexp.MustCompile(`\[(\d{2})/([A-Z][a-z]{2})/(\d{4}):(\d{2}):(\d{2}):(\d{2}) ([+-]\d{4})\]`)
	// [Sun Nov 16 12:00:00.123456 2026] (apache error).
	apacheTSRe = regexp.MustCompile(`^\[[A-Z][a-z]{2} ([A-Z][a-z]{2}) (\d{1,2}) (\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))? (\d{4})\]\s*`)
	// host proc[pid]: na een syslogtijd.
	hostProcRe = regexp.MustCompile(`^(\S+) ([A-Za-z0-9_./@()-]{1,64})(?:\[\d+\])?: `)
	// proc[pid]: zonder host (sommige programma's schrijven zo).
	procRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_./-]{0,63})\[\d+\]: `)

	levelKeyRe     = regexp.MustCompile(`(?i)\b(?:level|lvl|severity|loglevel)["']?\s*[=:]\s*["']?([A-Za-z]+)`)
	levelBracketRe = regexp.MustCompile(`(?i)(?:^|[\s\[<(|:])\[?(emerg|alert|crit|critical|fatal|panic|error|err|eror|warning|warn|notice|info|debug|trace)[\]>):|]`)
	levelWordRe    = regexp.MustCompile(`\b(EMERG|ALERT|CRIT|CRITICAL|FATAL|PANIC|SEVERE|ERROR|ERR|WARNING|WARN|NOTICE|INFO|DEBUG|TRACE)\b`)
	failWordRe     = regexp.MustCompile(`\b(?:[Ff]ailed|[Ff]ailure|[Dd]enied|[Rr]efused|[Tt]imed out|[Ee]xception|[Tt]raceback|[Ss]egfault|[Oo]ut of memory)\b`)
	// "GET / HTTP/1.1" 502 157 (access log): 5xx is een fout, 4xx een
	// waarschuwing.
	httpStatusRe = regexp.MustCompile(`" ([1-5]\d\d) (?:\d+|-)`)
)

var monthNums = map[string]time.Month{
	"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
	"Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12,
}

// logParser ontleedt regels. now is de huidige tijd (voor het jaar van een
// syslogtijd), loc de tijdzone van de node voor tijden zonder zone.
type logParser struct {
	now time.Time
	loc *time.Location
}

func newLogParser(now time.Time) logParser {
	return logParser{now: now, loc: time.Local}
}

func logAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func nanos(frac string) int {
	if frac == "" {
		return 0
	}
	for len(frac) < 9 {
		frac += "0"
	}
	return logAtoi(frac[:9])
}

func zoneOf(z string, def *time.Location) *time.Location {
	if z == "" {
		return def
	}
	if z == "Z" {
		return time.UTC
	}
	z = strings.ReplaceAll(z, ":", "")
	if len(z) != 5 {
		return def
	}
	off := (logAtoi(z[1:3])*60 + logAtoi(z[3:5])) * 60
	if z[0] == '-' {
		off = -off
	}
	return time.FixedZone("", off)
}

// parse ontleedt één regel. ok is false als de regel geen tijd heeft.
func (p logParser) parse(line string) (e protocol.LogEntry, ok bool) {
	line = strings.TrimRight(line, "\r")
	rest := line
	syslogLike := false
	if m := isoTSRe.FindStringSubmatch(line); m != nil {
		loc := zoneOf(m[8], p.loc)
		e.TS = time.Date(logAtoi(m[1]), time.Month(logAtoi(m[2])), logAtoi(m[3]), logAtoi(m[4]), logAtoi(m[5]), logAtoi(m[6]), nanos(m[7]), loc)
		rest = line[len(m[0]):]
		// rsyslog schrijft RFC 3339 met zone, gevolgd door host en tag.
		syslogLike = m[8] != "" && strings.Contains(m[0], "T")
	} else if m := syslogTSRe.FindStringSubmatch(line); m != nil {
		mon := monthNums[m[1]]
		year := p.now.Year()
		e.TS = time.Date(year, mon, logAtoi(m[2]), logAtoi(m[3]), logAtoi(m[4]), logAtoi(m[5]), nanos(m[6]), p.loc)
		// Een regel van december die in januari gelezen wordt.
		if e.TS.After(p.now.Add(48 * time.Hour)) {
			e.TS = e.TS.AddDate(-1, 0, 0)
		}
		rest = line[len(m[0]):]
		syslogLike = true
	} else if m := apacheTSRe.FindStringSubmatch(line); m != nil {
		e.TS = time.Date(logAtoi(m[7]), monthNums[m[1]], logAtoi(m[2]), logAtoi(m[3]), logAtoi(m[4]), logAtoi(m[5]), nanos(m[6]), p.loc)
		rest = line[len(m[0]):]
	} else if loc := clfTSRe.FindStringSubmatchIndex(line); loc != nil && loc[0] < 120 {
		m := clfTSRe.FindStringSubmatch(line)
		e.TS = time.Date(logAtoi(m[3]), monthNums[m[2]], logAtoi(m[1]), logAtoi(m[4]), logAtoi(m[5]), logAtoi(m[6]), 0, zoneOf(m[7], p.loc))
	} else if j, ok := p.parseJSON(line); ok {
		return j, !j.TS.IsZero()
	}
	if syslogLike {
		if m := hostProcRe.FindStringSubmatch(rest); m != nil && !levelWordRe.MatchString(m[1]) {
			e.Proc = m[2]
			rest = rest[len(m[0]):]
		}
	} else if m := procRe.FindStringSubmatch(rest); m != nil {
		e.Proc = m[1]
		rest = rest[len(m[0]):]
	}
	e.Msg = rest
	if !e.TS.IsZero() {
		e.Level = detectLevel(rest)
		// Een JSON-bericht na een tijd (Docker-log van een Go-programma).
		if strings.HasPrefix(rest, "{") {
			if j, ok := p.parseJSON(rest); ok && j.Level != "" {
				e.Level = j.Level
			}
		}
	}
	return e, !e.TS.IsZero()
}

// parseJSON ontleedt een JSON-regel ({"time":…,"level":…,"msg":…}).
func (p logParser) parseJSON(line string) (protocol.LogEntry, bool) {
	var e protocol.LogEntry
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return e, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return e, false
	}
	for _, k := range []string{"time", "ts", "timestamp", "@timestamp", "t", "date"} {
		switch v := m[k].(type) {
		case string:
			for _, f := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
				if t, err := time.ParseInLocation(f, v, p.loc); err == nil {
					e.TS = t
					break
				}
			}
		case float64:
			sec := v
			switch {
			case sec > 1e17:
				sec /= 1e9
			case sec > 1e14:
				sec /= 1e6
			case sec > 1e11:
				sec /= 1e3
			}
			if sec > 1e9 {
				e.TS = time.Unix(int64(sec), int64((sec-float64(int64(sec)))*1e9))
			}
		}
		if !e.TS.IsZero() {
			break
		}
	}
	for _, k := range []string{"level", "lvl", "severity", "loglevel", "log.level"} {
		if v, ok := m[k].(string); ok {
			e.Level = normLevel(v)
			break
		}
	}
	for _, k := range []string{"logger", "caller", "source", "component", "module"} {
		if v, ok := m[k].(string); ok && len(v) <= 64 {
			e.Proc = v
			break
		}
	}
	e.Msg = s
	for _, k := range []string{"msg", "message", "MESSAGE", "log"} {
		if v, ok := m[k].(string); ok {
			e.Msg = strings.TrimRight(v, "\n")
			// De andere velden achter het bericht, als key=value.
			var extra []string
			for key, val := range m {
				switch key {
				case k, "time", "ts", "timestamp", "@timestamp", "t", "date", "level", "lvl", "severity", "loglevel", "log.level":
					continue
				}
				b, _ := json.Marshal(val)
				extra = append(extra, key+"="+strings.Trim(string(b), `"`))
			}
			if len(extra) > 0 {
				slices.Sort(extra)
				e.Msg += " " + strings.Join(extra, " ")
			}
			break
		}
	}
	if e.Level == "" {
		e.Level = detectLevel(e.Msg)
	}
	return e, true
}

// normLevel zet een niveau om naar een van de vier van protocol.
func normLevel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "emerg", "emergency", "alert", "crit", "critical", "fatal", "panic", "severe", "error", "err", "eror", "e":
		return protocol.LevelError
	case "warning", "warn", "w":
		return protocol.LevelWarn
	case "notice", "info", "information", "informational", "i":
		return protocol.LevelInfo
	case "debug", "trace", "d", "verbose":
		return protocol.LevelDebug
	}
	return protocol.LevelUnknown
}

// detectLevel zoekt het niveau in een bericht: eerst level=…, dan [error]
// of error:, dan een woord in hoofdletters, dan een HTTP-status, en als
// laatste woorden als failed of denied (een waarschuwing).
func detectLevel(msg string) string {
	if len(msg) > 512 {
		msg = msg[:512]
	}
	if m := levelKeyRe.FindStringSubmatch(msg); m != nil {
		if l := normLevel(m[1]); l != "" {
			return l
		}
	}
	if m := levelBracketRe.FindStringSubmatch(msg); m != nil {
		if l := normLevel(m[1]); l != "" {
			return l
		}
	}
	if m := levelWordRe.FindStringSubmatch(msg); m != nil {
		if l := normLevel(m[1]); l != "" {
			return l
		}
	}
	if m := httpStatusRe.FindStringSubmatch(msg); m != nil {
		switch m[1][0] {
		case '5':
			return protocol.LevelError
		case '4':
			return protocol.LevelWarn
		default:
			return protocol.LevelInfo
		}
	}
	if failWordRe.MatchString(msg) {
		return protocol.LevelWarn
	}
	return protocol.LevelUnknown
}

// journalLevel zet een syslogprioriteit (0-7) om.
func journalLevel(prio string) string {
	switch prio {
	case "0", "1", "2", "3":
		return protocol.LevelError
	case "4":
		return protocol.LevelWarn
	case "5", "6":
		return protocol.LevelInfo
	case "7":
		return protocol.LevelDebug
	}
	return protocol.LevelUnknown
}
