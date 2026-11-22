package agent

// De cronjobs op de node (fase 6): de agent leest /etc/crontab, /etc/cron.d,
// de crontabs van gebruikers, de periodieke mappen en de systemd-timers, en
// haalt de laatste runs uit de journal. Commando's gaan gemaskeerd naar de
// server; de agent verandert hier niets.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// cronHistory is hoe ver de agent de eerste keer in de journal
	// terugkijkt: lang genoeg voor een wekelijkse job.
	cronHistory = 8 * 24 * time.Hour
	// cronWindow is de periode waarover Runs en Failures tellen.
	cronWindow = 24 * time.Hour
)

// cronSpoolDirs zijn de mappen met de crontabs van gebruikers: Debian
// (cron) en cronie.
var cronSpoolDirs = []string{"/var/spool/cron/crontabs", "/var/spool/cron"}

// cronPeriodic zijn de mappen die run-parts afloopt, met hun schema.
var cronPeriodic = []struct{ dir, schedule string }{
	{"/etc/cron.hourly", "hourly"}, {"/etc/cron.daily", "daily"}, {"/etc/cron.weekly", "weekly"}, {"/etc/cron.monthly", "monthly"},
}

// runPartsName zijn de namen die run-parts (en cron in /etc/cron.d)
// meeneemt; een bestand met een punt, zoals een .dpkg-old, slaan ze over.
var runPartsName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// cronLog onthoudt wat de agent uit de journal over cron las.
type cronLog struct {
	mu     sync.Mutex
	cursor string
	// runs per sleutel (gebruiker en commando), de nieuwste achteraan.
	runs map[string][]cronRun
	// open zijn de runs die gestart zijn en nog geen einde hebben, per
	// pid van het cron-kind.
	open map[string]string
}

type cronRun struct {
	start time.Time
	end   time.Time
	exit  *int
}

func cronKey(user, command string) string { return user + "\x00" + strings.TrimSpace(command) }

func (a *Agent) onCron(m *nats.Msg) {
	go func() {
		res := a.handleCron(m.Data)
		data, err := envelope(protocol.TypeCronResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op cronverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleCron(data []byte) protocol.CronResponse {
	var env protocol.Envelope
	var req protocol.CronRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeCronRequest {
		return protocol.CronResponse{Error: "ongeldig cronverzoek", Entries: []protocol.CronEntry{}}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil || req.ID == "" {
		return protocol.CronResponse{Error: "ongeldig cronverzoek", Entries: []protocol.CronEntry{}}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.CronResponse{Error: "cronverzoek verlopen; klopt de klok van deze node?", Entries: []protocol.CronEntry{}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.CronTimeout)
	defer cancel()
	if !req.Deadline.IsZero() {
		var c2 context.CancelFunc
		ctx, c2 = context.WithDeadline(ctx, req.Deadline)
		defer c2()
	}
	switch req.Op {
	case protocol.CronList:
		return a.cronList(ctx)
	case protocol.CronLogging:
		return a.cronLogging(ctx, req.Enable)
	case protocol.CronApply:
		return a.cronApply(ctx, req.Jobs, req.Receives)
	case protocol.CronRun:
		return a.cronRunNow(ctx, req.JobID)
	case protocol.CronOutput:
		return a.cronOutput(req.JobID, req.RunID)
	case protocol.CronStop:
		return a.cronStop(ctx, req.JobID)
	case protocol.CronToggle:
		return a.cronToggle(req.Entry, req.Enable)
	case protocol.CronKeys:
		return a.cronKeys(ctx, req.Paths)
	case protocol.CronDryRun:
		return a.cronDryRun(ctx, req.JobID, req.Target)
	default:
		return protocol.CronResponse{Error: "onbekend cronverzoek " + req.Op, Entries: []protocol.CronEntry{}}
	}
}

// cronList leest alle cronjobs en timers.
func (a *Agent) cronList(ctx context.Context) protocol.CronResponse {
	c := a.Collector
	if c == nil {
		c = &Collector{}
	}
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	raw := readCrontabs(c)
	raw = append(raw, readTimers(ctx, c)...)
	res.Daemon, res.Logging = cronDaemon(ctx, c)

	a.cronMu.Lock()
	if a.cron == nil {
		a.cron = &cronLog{runs: map[string][]cronRun{}, open: map[string]string{}}
	}
	log := a.cron
	a.cronMu.Unlock()
	now := time.Now()
	res.Journal = log.read(ctx, c, now)

	owners := packageOwners(ctx, c, raw)
	for _, r := range raw {
		e := r.entry
		e.Package = owners[r.path]
		if e.Source != protocol.CronSourceTimer && e.Source != protocol.CronSourcePeriodic {
			log.fill(&e, cronKey(e.User, r.command), now, res.Logging)
		}
		e.Command = protocol.MaskSecrets(e.Command)
		if len(e.Command) > protocol.MaxCronCommand {
			e.Command = e.Command[:protocol.MaxCronCommand] + "…"
		}
		res.Entries = append(res.Entries, e)
	}
	if len(res.Entries) > protocol.MaxCronEntries {
		res.Entries, res.Truncated = res.Entries[:protocol.MaxCronEntries], true
	}
	return res
}

// rawCron is een gevonden job met het ongemaskeerde commando, om het met de
// journal te vergelijken, en het pad voor dpkg.
type rawCron struct {
	entry   protocol.CronEntry
	command string
	path    string
}

// readCrontabs leest /etc/crontab, /etc/cron.d, de crontabs van gebruikers
// en de periodieke mappen.
func readCrontabs(c *Collector) []rawCron {
	var out []rawCron
	out = append(out, parseCrontabFile(c, "/etc/crontab", protocol.CronSourceCrontab, "")...)
	for _, name := range listDir(c, "/etc/cron.d") {
		if !runPartsName.MatchString(name) {
			continue
		}
		out = append(out, parseCrontabFile(c, path.Join("/etc/cron.d", name), protocol.CronSourceCronD, "")...)
	}
	seen := map[string]bool{}
	for _, dir := range cronSpoolDirs {
		for _, user := range listDir(c, dir) {
			if seen[user] || strings.HasPrefix(user, ".") || strings.HasPrefix(user, "tmp.") {
				continue
			}
			p := path.Join(dir, user)
			if st, err := os.Stat(c.path(p)); err != nil || !st.Mode().IsRegular() {
				continue
			}
			seen[user] = true
			out = append(out, parseCrontabFile(c, p, protocol.CronSourceUser, user)...)
		}
	}
	for _, per := range cronPeriodic {
		for _, name := range listDir(c, per.dir) {
			if !runPartsName.MatchString(name) {
				continue
			}
			p := path.Join(per.dir, name)
			st, err := os.Stat(c.path(p))
			if err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
				continue
			}
			out = append(out, rawCron{
				entry:   protocol.CronEntry{Source: protocol.CronSourcePeriodic, File: p, User: "root", Schedule: per.schedule, Command: p, Enabled: true},
				command: p, path: p,
			})
		}
	}
	return out
}

func listDir(c *Collector, dir string) []string {
	ents, err := os.ReadDir(c.path(dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

var cronEnvLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=`)

// parseCrontabFile leest een crontab. user leeg betekent dat elke regel een
// gebruikersveld heeft, zoals /etc/crontab en /etc/cron.d.
func parseCrontabFile(c *Collector, p, source, user string) []rawCron {
	b, err := os.ReadFile(c.path(p))
	if err != nil {
		return nil
	}
	var out []rawCron
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		// Een regel die ClusterForge uitzette, staat er als uit.
		enabled := true
		if rest, ok := strings.CutPrefix(line, protocol.CronOffPrefix); ok {
			line, enabled = strings.TrimSpace(rest), false
		}
		schedule, u, cmd, ok := parseCronLine(line, user)
		if !ok {
			continue
		}
		out = append(out, rawCron{
			entry:   protocol.CronEntry{Source: source, File: p, Line: n, User: u, Schedule: schedule, Command: cmd, Enabled: enabled},
			command: cmd, path: p,
		})
	}
	return out
}

// parseCronLine leest één regel van een crontab. user leeg betekent dat de
// regel een gebruikersveld heeft.
func parseCronLine(line, user string) (schedule, u, cmd string, ok bool) {
	if line == "" || strings.HasPrefix(line, "#") || cronEnvLine.MatchString(line) {
		return "", "", "", false
	}
	schedule, rest, ok := splitSchedule(line)
	if !ok {
		return "", "", "", false
	}
	u = user
	if u == "" {
		rest = strings.TrimLeft(rest, " \t")
		i := strings.IndexAny(rest, " \t")
		if i <= 0 {
			return "", "", "", false
		}
		u, rest = rest[:i], rest[i:]
	}
	cmd = strings.TrimSpace(rest)
	return schedule, u, cmd, cmd != ""
}

// splitSchedule haalt de tijdsaanduiding van een cronregel af: vijf velden
// of een @-woord.
func splitSchedule(line string) (schedule, rest string, ok bool) {
	if strings.HasPrefix(line, "@") {
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			return "", "", false
		}
		return line[:i], line[i:], true
	}
	fields := 0
	i := 0
	for fields < 5 {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		start := i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		if start == i {
			return "", "", false
		}
		fields++
	}
	return strings.Join(strings.Fields(line[:i]), " "), line[i:], true
}

// readTimers leest de systemd-timers met hun service.
func readTimers(ctx context.Context, c *Collector) []rawCron {
	names := map[string]bool{}
	if out, err := c.run(ctx, "systemctl", "list-units", "--type=timer", "--all", "--no-legend", "--plain", "--no-pager"); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Fields(l); len(f) > 0 && strings.HasSuffix(f[0], ".timer") {
				names[f[0]] = true
			}
		}
	}
	if out, err := c.run(ctx, "systemctl", "list-unit-files", "--type=timer", "--no-legend", "--no-pager"); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			// Een sjabloon als foo@.timer is zelf geen timer.
			if f := strings.Fields(l); len(f) > 0 && strings.HasSuffix(f[0], ".timer") && !strings.HasSuffix(f[0], "@.timer") {
				names[f[0]] = true
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	list := slices.Sorted(func(yield func(string) bool) {
		for n := range names {
			if !yield(n) {
				return
			}
		}
	})
	timers := systemctlShow(ctx, c, list, "Id", "Description", "Unit", "UnitFileState", "ActiveState", "NextElapseUSecRealtime",
		"LastTriggerUSec", "TimersCalendar", "TimersMonotonic", "FragmentPath")
	var svcNames []string
	for _, t := range timers {
		if u := t.get("Unit"); u != "" {
			svcNames = append(svcNames, u)
		}
	}
	services := map[string]unitProps{}
	for _, s := range systemctlShow(ctx, c, svcNames, "Id", "Description", "ExecStart", "Result", "ExecMainStatus",
		"ExecMainStartTimestamp", "ExecMainExitTimestamp", "ActiveState", "User", "FragmentPath") {
		services[s.get("Id")] = s
	}
	var out []rawCron
	for _, t := range timers {
		id := t.get("Id")
		if id == "" || t.get("FragmentPath") == "" {
			continue
		}
		svc := services[t.get("Unit")]
		e := protocol.CronEntry{
			Source: protocol.CronSourceTimer, File: id, Unit: t.get("Unit"), Description: t.get("Description"),
			User: svc.get("User"), Schedule: timerSchedule(t), Command: execStart(svc.get("ExecStart")),
			ClusterForge: strings.HasPrefix(id, "cf-cron-"),
		}
		if e.User == "" {
			e.User = "root"
		}
		if e.Description == "" {
			e.Description = svc.get("Description")
		}
		state := t.get("UnitFileState")
		e.Enabled = t.get("ActiveState") == "active" || state == "enabled" || state == "static" || state == "enabled-runtime"
		e.NextRun = unixTime(t.get("NextElapseUSecRealtime"))
		if last := unixTime(t.get("LastTriggerUSec")); last != nil {
			e.LastRun = last
			if start := unixTime(svc.get("ExecMainStartTimestamp")); start != nil && !start.Before(last.Add(-time.Minute)) {
				e.LastRun = start
			}
			switch svc.get("ActiveState") {
			case "activating", "active", "deactivating", "reloading":
				if svc.get("ExecMainExitTimestamp") == "" || isRunningOneshot(svc) {
					e.LastResult = protocol.CronRunning
				}
			}
			if e.LastResult == "" {
				e.LastEnd = unixTime(svc.get("ExecMainExitTimestamp"))
				code, err := strconv.Atoi(svc.get("ExecMainStatus"))
				if err == nil {
					e.LastExit = &code
				}
				if svc.get("Result") == "success" && (e.LastExit == nil || *e.LastExit == 0) {
					e.LastResult = protocol.CronOK
				} else if svc.get("Result") != "" {
					e.LastResult = protocol.CronFailed
				}
			}
		}
		out = append(out, rawCron{entry: e, command: e.Command, path: t.get("FragmentPath")})
	}
	return out
}

// isRunningOneshot: een oneshot-service staat tijdens de run op
// activating; een service die klaar is met RemainAfterExit op active.
func isRunningOneshot(svc unitProps) bool { return svc.get("ActiveState") == "activating" }

// unitProps zijn de eigenschappen van één unit uit systemctl show. Een
// eigenschap die vaker voorkomt (TimersCalendar), heeft meer waarden.
type unitProps map[string][]string

func (u unitProps) get(k string) string {
	if v := u[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// systemctlShow vraagt eigenschappen van units, met tijden als @seconden.
// Een systemd zonder --timestamp=unix (ouder dan 251) krijgt het zonder;
// de tijden ontbreken dan.
func systemctlShow(ctx context.Context, c *Collector, units []string, props ...string) []unitProps {
	if len(units) == 0 {
		return nil
	}
	args := []string{"show", "--no-pager", "--timestamp=unix", "-p", strings.Join(props, ",")}
	out, err := c.run(ctx, "systemctl", append(args, units...)...)
	if err != nil && len(out) == 0 {
		args = slices.Delete(args, 2, 3)
		out, _ = c.run(ctx, "systemctl", append(args, units...)...)
	}
	var res []unitProps
	cur := unitProps{}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == "" {
			if len(cur) > 0 {
				res = append(res, cur)
				cur = unitProps{}
			}
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		if v == "" || v == "[not set]" || v == "n/a" {
			continue
		}
		cur[k] = append(cur[k], v)
	}
	if len(cur) > 0 {
		res = append(res, cur)
	}
	return res
}

var (
	timerCalendar  = regexp.MustCompile(`OnCalendar=([^;]+?)\s*;`)
	timerMonotonic = regexp.MustCompile(`(On[A-Za-z]+)USec=([^;]+?)\s*;`)
	execArgv       = regexp.MustCompile(`argv\[\]=(.*?) ;`)
)

// timerSchedule maakt van TimersCalendar en TimersMonotonic één tekst, zoals
// "OnCalendar=*-*-* 06,18:00:00" of "OnBootSec=15min; OnUnitActiveSec=1d".
func timerSchedule(t unitProps) string {
	var parts []string
	for _, v := range t["TimersCalendar"] {
		if m := timerCalendar.FindStringSubmatch(v); m != nil {
			parts = append(parts, "OnCalendar="+m[1])
		}
	}
	for _, v := range t["TimersMonotonic"] {
		if m := timerMonotonic.FindStringSubmatch(v); m != nil {
			parts = append(parts, m[1]+"Sec="+m[2])
		}
	}
	return strings.Join(parts, "; ")
}

// execStart haalt de commandoregels uit ExecStart, zoals "{ path=/bin/x ;
// argv[]=/bin/x --y ; ... }".
func execStart(v string) string {
	var cmds []string
	for _, m := range execArgv.FindAllStringSubmatch(v+" ;", -1) {
		cmds = append(cmds, strings.TrimSpace(m[1]))
	}
	return strings.Join(cmds, "; ")
}

func unixTime(v string) *time.Time {
	s, ok := strings.CutPrefix(strings.TrimSpace(v), "@")
	if !ok {
		return nil
	}
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil || sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

// packageOwners vraagt dpkg bij welk pakket elk bestand hoort.
func packageOwners(ctx context.Context, c *Collector, raw []rawCron) map[string]string {
	var paths []string
	for _, r := range raw {
		if r.path != "" && !slices.Contains(paths, r.path) {
			paths = append(paths, r.path)
		}
	}
	owners := map[string]string{}
	if len(paths) == 0 {
		return owners
	}
	// dpkg kent een unit onder /lib ook als /usr/lib en omgekeerd.
	ask := slices.Clone(paths)
	for _, p := range paths {
		if rest, ok := strings.CutPrefix(p, "/usr/lib/"); ok {
			ask = append(ask, "/lib/"+rest)
		} else if rest, ok := strings.CutPrefix(p, "/lib/"); ok {
			ask = append(ask, "/usr/lib/"+rest)
		}
	}
	// dpkg-query geeft exitcode 1 als een pad bij geen pakket hoort; de
	// regels voor de andere paden staan dan nog op stdout.
	out, _ := c.run(ctx, "dpkg-query", append([]string{"-S"}, ask...)...)
	for _, l := range strings.Split(string(out), "\n") {
		pkgs, p, ok := strings.Cut(l, ": ")
		if !ok || strings.HasPrefix(pkgs, "diversion by") {
			continue
		}
		pkg, _, _ := strings.Cut(pkgs, ",")
		pkg, _, _ = strings.Cut(strings.TrimSpace(pkg), ":")
		p = strings.TrimSpace(p)
		owners[p] = pkg
		if rest, ok := strings.CutPrefix(p, "/usr/lib/"); ok {
			owners["/lib/"+rest] = pkg
		} else if rest, ok := strings.CutPrefix(p, "/lib/"); ok {
			owners["/usr/lib/"+rest] = pkg
		}
	}
	return owners
}

var cronLogFlag = regexp.MustCompile(`(?:^|\s)-L\s*(\d+)`)

// cronDaemon zegt welke cron draait, en of hij het einde en de exitcode
// van een job logt.
func cronDaemon(ctx context.Context, c *Collector) (string, bool) {
	for _, d := range []struct{ unit, name string }{{"cron", "cron"}, {"crond", "cronie"}, {"cronie", "cronie"}} {
		props := systemctlShow(ctx, c, []string{d.unit + ".service"}, "ActiveState", "MainPID")
		if len(props) == 0 || props[0].get("ActiveState") != "active" {
			continue
		}
		if d.name == "cronie" {
			// cronie logt CMDEND altijd, maar geen exitcode.
			return d.name, false
		}
		pid := props[0].get("MainPID")
		cmdline, err := os.ReadFile(c.path("/proc/" + pid + "/cmdline"))
		if err != nil {
			return d.name, false
		}
		m := cronLogFlag.FindStringSubmatch(strings.ReplaceAll(string(cmdline), "\x00", " "))
		if m == nil {
			return d.name, false
		}
		level, _ := strconv.Atoi(m[1])
		return d.name, level&2 != 0 && level&4 != 0
	}
	return "", false
}

var (
	cronLine  = regexp.MustCompile(`^(\d+(?:\.\d+)?) \S+ (?:CRON|CROND|cron)\[(\d+)\]: \((.+?)\) (CMD|END|CMDEND) \((?:\[\d+\] )?(.*)\)$`)
	cronError = regexp.MustCompile(`^(\d+(?:\.\d+)?) \S+ (?:CRON|CROND|cron)\[(\d+)\]: \(CRON\) error \(grandchild #\d+ failed with exit status (\d+)\)`)
	cronShowC = regexp.MustCompile(`^-- cursor: (.+)$`)
)

// read leest de cronregels uit de journal sinds de vorige keer. false als
// de journal niet te lezen is.
func (l *cronLog) read(ctx context.Context, c *Collector, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	args := []string{"--no-pager", "-q", "-o", "short-unix", "--show-cursor"}
	if l.cursor != "" {
		args = append(args, "--after-cursor="+l.cursor)
	} else {
		args = append(args, "--since=@"+strconv.FormatInt(now.Add(-cronHistory).Unix(), 10))
	}
	args = append(args, "SYSLOG_IDENTIFIER=CRON", "SYSLOG_IDENTIFIER=CROND", "SYSLOG_IDENTIFIER=cron")
	out, err := c.run(ctx, "journalctl", args...)
	if err != nil && len(out) == 0 {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := cronShowC.FindStringSubmatch(line); m != nil {
			l.cursor = m[1]
			continue
		}
		if m := cronError.FindStringSubmatch(line); m != nil {
			key, ok := l.open[m[2]]
			if !ok {
				continue
			}
			code, _ := strconv.Atoi(m[3])
			if runs := l.runs[key]; len(runs) > 0 {
				runs[len(runs)-1].exit = &code
			}
			continue
		}
		m := cronLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts := journalTime(m[1])
		key := cronKey(m[3], m[5])
		switch m[4] {
		case "CMD":
			l.runs[key] = append(l.runs[key], cronRun{start: ts})
			l.open[m[2]] = key
		default:
			if k, ok := l.open[m[2]]; ok {
				key = k
				delete(l.open, m[2])
			}
			if runs := l.runs[key]; len(runs) > 0 && runs[len(runs)-1].end.IsZero() {
				runs[len(runs)-1].end = ts
			}
		}
	}
	// Alleen de laatste run telt buiten het venster.
	for k, runs := range l.runs {
		i := 0
		for i < len(runs)-1 && now.Sub(runs[i].start) > cronWindow {
			i++
		}
		l.runs[k] = runs[i:]
	}
	if len(l.open) > 10000 {
		l.open = map[string]string{}
	}
	return true
}

func journalTime(s string) time.Time {
	sec, _ := strconv.ParseFloat(s, 64)
	return time.Unix(0, int64(sec*1e9)).UTC().Truncate(time.Second)
}

// fill zet de laatste run en de tellingen van een cronjob.
func (l *cronLog) fill(e *protocol.CronEntry, key string, now time.Time, logging bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	runs := l.runs[key]
	if len(runs) == 0 {
		return
	}
	for _, r := range runs {
		if now.Sub(r.start) <= cronWindow {
			e.Runs++
			if r.exit != nil && *r.exit != 0 {
				e.Failures++
			}
		}
	}
	last := runs[len(runs)-1]
	start := last.start
	e.LastRun = &start
	switch {
	case last.exit != nil:
		exit := *last.exit
		e.LastExit = &exit
		e.LastResult = protocol.CronFailed
		if !last.end.IsZero() {
			end := last.end
			e.LastEnd = &end
		}
	case !last.end.IsZero():
		end := last.end
		e.LastEnd = &end
		if logging {
			zero := 0
			e.LastExit = &zero
			e.LastResult = protocol.CronOK
		} else {
			e.LastResult = protocol.CronStarted
		}
	case logging:
		e.LastResult = protocol.CronRunning
	default:
		e.LastResult = protocol.CronStarted
	}
}

// cronLogging laat cron het einde en de exitcode van elke job loggen, met
// een drop-in die ExecStart van cron.service herhaalt met -L 15 erachter
// (de laatste -L telt), of haalt die drop-in weer weg.
func (a *Agent) cronLogging(ctx context.Context, enable bool) protocol.CronResponse {
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	c := a.Collector
	if c == nil {
		c = &Collector{}
	}
	daemon, _ := cronDaemon(ctx, c)
	if daemon != "cron" {
		res.Error = "op deze node draait geen cron van Debian"
		if daemon == "cronie" {
			res.Error = "cronie logt geen exitcode; dat kan ClusterForge niet aanzetten"
		}
		return res
	}
	p, err := a.path(protocol.CronLoggingDropIn)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if enable {
		start := "/usr/sbin/cron -f -P $EXTRA_OPTS"
		if props := systemctlShow(ctx, c, []string{"cron.service"}, "ExecStart"); len(props) > 0 {
			if cmd := execStart(props[0].get("ExecStart")); cmd != "" && !strings.Contains(cmd, ";") {
				start = strings.TrimSpace(strings.TrimSuffix(cmd, "-L 15"))
			}
		}
		content := "# ClusterForge: cron logt het einde en de exitcode van elke job (-L 15).\n" +
			"# Weghalen kan bij Cronjobs, of met: rm " + protocol.CronLoggingDropIn + " && systemctl daemon-reload && systemctl restart cron\n" +
			"[Service]\nExecStart=\nExecStart=" + start + " -L 15\n"
		if err := os.MkdirAll(path.Dir(p), 0o755); err != nil {
			res.Error = err.Error()
			return res
		}
		if err := writeFileAtomic(p, []byte(content), 0o644); err != nil {
			res.Error = err.Error()
			return res
		}
		res.Output = append(res.Output, protocol.CronLoggingDropIn+" geschreven: ExecStart="+start+" -L 15")
	} else {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			res.Error = err.Error()
			return res
		}
		res.Output = append(res.Output, protocol.CronLoggingDropIn+" weggehaald")
	}
	for _, args := range [][]string{{"daemon-reload"}, {"restart", "cron.service"}} {
		if out, err := a.exec(ctx, "systemctl", args...); err != nil {
			res.Error = fmt.Sprintf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			return res
		}
	}
	res.Output = append(res.Output, "cron herstart; lopende jobs draaien door")
	res.Daemon, res.Logging = cronDaemon(ctx, c)
	return res
}
