package agent

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// --- logbestanden volgen ---

// tailPos is waar de agent in een logbestand gebleven is.
type tailPos struct {
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}

// logLine is één volledige regel met de positie erna.
type logLine struct {
	text string
	next tailPos
}

// tail geeft de volledige regels van p na pos. Is p geroteerd (een ander
// inode of kleiner dan pos), dan komt eerst de rest van het oude bestand,
// uit p.1 of p.1.gz. Een regel zonder newline aan het eind wacht op de
// volgende ronde.
func (j *Journal) tail(p string, pos tailPos) []logLine {
	full := j.path(p)
	st, err := os.Stat(full)
	if err != nil {
		return nil
	}
	inode := fileInode(st)
	var out []logLine
	start := pos.Offset
	if pos.Inode != 0 && (pos.Inode != inode || st.Size() < pos.Offset) {
		for _, l := range j.rotatedRest(full, pos) {
			out = append(out, logLine{text: l, next: tailPos{Inode: inode}})
		}
		start = 0
	}
	f, err := os.Open(full)
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return out
	}
	r := bufio.NewReaderSize(f, 64<<10)
	off := start
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		off += int64(len(line))
		out = append(out, logLine{text: strings.TrimRight(line, "\r\n"), next: tailPos{Inode: inode, Offset: off}})
	}
	return out
}

// rotatedRest leest wat na pos in het geroteerde bestand staat.
func (j *Journal) rotatedRest(full string, pos tailPos) []string {
	var data []byte
	if st, err := os.Stat(full + ".1"); err == nil && fileInode(st) == pos.Inode {
		data, _ = os.ReadFile(full + ".1")
	} else if f, err := os.Open(full + ".1.gz"); err == nil {
		if zr, err := gzip.NewReader(f); err == nil {
			data, _ = io.ReadAll(io.LimitReader(zr, 64<<20))
		}
		_ = f.Close()
	}
	if int64(len(data)) <= pos.Offset {
		return nil
	}
	rest := string(data[pos.Offset:])
	if i := strings.LastIndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	} else {
		return nil
	}
	return strings.Split(rest, "\n")
}

func fileInode(st fs.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return s.Ino
	}
	return 0
}

// --- pakketten ---

// packages leest de apt-runs uit history.log en de losse dpkg-acties uit
// dpkg.log. Een apt-run komt pas als hij klaar is (End-Date); zolang hij
// loopt, wachten ook de dpkg-regels erna.
func (j *Journal) packages() {
	running := j.aptHistory()
	j.dpkgLog(running)
}

// aptHistory leest de afgesloten blokken uit history.log en geeft het begin
// van een blok dat nog loopt, of nul.
func (j *Journal) aptHistory() time.Time {
	lines := j.tail("/var/log/apt/history.log", j.st.History)
	var block []logLine
	pos := j.st.History
	// flush verwerkt een blok dat door een lege regel afgesloten is. Een
	// blok zonder End-Date daarvoor is kapot en wordt overgeslagen.
	flush := func() {
		if len(block) == 0 {
			return
		}
		if run, entries, complete := parseAptBlock(block, j.loc()); complete {
			j.add(entries...)
			j.st.AptRuns = append(j.st.AptRuns, run)
			if n := len(j.st.AptRuns); n > 50 {
				j.st.AptRuns = j.st.AptRuns[n-50:]
			}
		}
		pos = block[len(block)-1].next
		block = nil
	}
	for _, l := range lines {
		if strings.TrimSpace(l.text) == "" {
			flush()
			pos = l.next
			continue
		}
		block = append(block, l)
	}
	// Het laatste blok heeft nog geen lege regel erna: klaar als het een
	// End-Date heeft, anders loopt apt nog.
	if len(block) > 0 {
		if _, _, complete := parseAptBlock(block, j.loc()); complete {
			flush()
		}
	}
	var running time.Time
	if len(block) > 0 {
		running, _ = parseAptStart(block[0].text, j.loc())
		if running.IsZero() || j.now().Sub(running) > time.Hour {
			// Geen herkenbaar begin, of apt is afgebroken zonder End-Date:
			// niet blijven wachten.
			pos, running = block[len(block)-1].next, time.Time{}
		}
	}
	if pos != j.st.History {
		j.st.History = pos
		j.dirty = true
	}
	return running
}

func parseAptStart(line string, loc *time.Location) (time.Time, bool) {
	v, ok := strings.CutPrefix(line, "Start-Date:")
	if !ok {
		return time.Time{}, false
	}
	t, err := parseAptTime(v, loc)
	return t, err == nil
}

func parseAptTime(v string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04:05", strings.Join(strings.Fields(v), " "), loc)
}

// aptSections zijn de regels in een blok van history.log met de actie die
// ze in het journaal worden.
var aptSections = map[string]string{
	"Install": "install", "Upgrade": "upgrade", "Downgrade": "downgrade", "Remove": "remove", "Purge": "purge",
	"Reinstall": "reinstall",
}

// parseAptBlock zet één blok van history.log om in regels. De commandoregel
// zelf wordt niet bewaard, alleen het programma: een URL erin kan een
// wachtwoord bevatten.
func parseAptBlock(block []logLine, loc *time.Location) (aptRun, []protocol.Change, bool) {
	var run aptRun
	by, via := "", ""
	sections := map[string]string{}
	for _, l := range block {
		k, v, ok := strings.Cut(l.text, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Start-Date":
			run.Start, _ = parseAptTime(v, loc)
		case "End-Date":
			run.End, _ = parseAptTime(v, loc)
		case "Requested-By":
			by, _, _ = strings.Cut(v, " ")
		case "Commandline":
			if f := strings.Fields(v); len(f) > 0 {
				via = filepath.Base(f[0])
			}
		default:
			if _, known := aptSections[k]; known {
				sections[k] = v
			}
		}
	}
	if run.Start.IsZero() || run.End.IsZero() {
		return run, nil, false
	}
	if by == "" {
		by = "root"
		if strings.HasPrefix(via, "unattended-upgrade") {
			by = "unattended-upgrades"
		}
	}
	var out []protocol.Change
	keys := make([]string, 0, len(sections))
	for k := range sections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, p := range splitAptPackages(sections[k]) {
			c := protocol.Change{TS: run.Start, Kind: protocol.ChangePackage, Subject: p.name, Action: aptSections[k], By: by}
			if via != "" {
				c.Detail = map[string]string{"via": via}
			}
			switch k {
			case "Upgrade", "Downgrade":
				c.From, c.To = p.v1, p.v2
			case "Install", "Reinstall":
				c.To = p.v1
			case "Remove", "Purge":
				c.From = p.v1
			}
			out = append(out, c)
		}
	}
	return run, out, true
}

type aptPackage struct{ name, v1, v2 string }

// splitAptPackages leest "nginx:amd64 (1.26.0-1, 1.26.3-1), curl:amd64
// (7.88.1-10, automatic)".
func splitAptPackages(s string) []aptPackage {
	var out []aptPackage
	for s != "" {
		open := strings.IndexByte(s, '(')
		if open < 0 {
			break
		}
		closeIdx := strings.IndexByte(s[open:], ')')
		if closeIdx < 0 {
			break
		}
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s[:open]), ","))
		name, _, _ = strings.Cut(name, ":")
		inner := strings.Split(s[open+1:open+closeIdx], ",")
		p := aptPackage{name: name}
		if len(inner) > 0 {
			p.v1 = strings.TrimSpace(inner[0])
		}
		if len(inner) > 1 {
			if v := strings.TrimSpace(inner[1]); v != "automatic" {
				p.v2 = v
			}
		}
		if name != "" {
			out = append(out, p)
		}
		s = s[open+closeIdx+1:]
	}
	return out
}

// dpkgLog leest dpkg.log. Een actie binnen een apt-run staat er al via
// history.log; wat overblijft, is dpkg -i of iets anders zonder apt. Vanaf
// running (een apt-run die nog loopt) wacht hij.
func (j *Journal) dpkgLog(running time.Time) {
	pos := j.st.Dpkg
	for _, l := range j.tail("/var/log/dpkg.log", j.st.Dpkg) {
		ts, c, ok := parseDpkgLine(l.text, j.loc())
		if ts.IsZero() {
			pos = l.next
			continue
		}
		if !running.IsZero() && !ts.Before(running.Add(-time.Second)) {
			break
		}
		pos = l.next
		if !ok || j.inAptRun(ts) {
			continue
		}
		c.By = ""
		c.Detail = map[string]string{"via": "dpkg"}
		j.add(c)
	}
	if pos != j.st.Dpkg {
		j.st.Dpkg = pos
		j.dirty = true
	}
}

func (j *Journal) inAptRun(ts time.Time) bool {
	for _, r := range j.st.AptRuns {
		if !ts.Before(r.Start.Add(-time.Second)) && !ts.After(r.End.Add(time.Second)) {
			return true
		}
	}
	return false
}

// parseDpkgLine leest "2026-10-10 14:02:12 upgrade nginx:amd64 1.26.0-1
// 1.26.3-1". ok is false voor regels die geen install, upgrade, remove of
// purge zijn; ts is nul voor een regel zonder tijd.
func parseDpkgLine(line string, loc *time.Location) (time.Time, protocol.Change, bool) {
	f := strings.Fields(line)
	if len(f) < 3 {
		return time.Time{}, protocol.Change{}, false
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", f[0]+" "+f[1], loc)
	if err != nil {
		return time.Time{}, protocol.Change{}, false
	}
	switch f[2] {
	case "install", "upgrade", "remove", "purge":
	default:
		return ts, protocol.Change{}, false
	}
	if len(f) < 6 {
		return ts, protocol.Change{}, false
	}
	name, _, _ := strings.Cut(f[3], ":")
	none := func(v string) string {
		if v == "<none>" {
			return ""
		}
		return v
	}
	c := protocol.Change{TS: ts, Kind: protocol.ChangePackage, Subject: name, Action: f[2], From: none(f[4]), To: none(f[5])}
	if c.Action == "upgrade" && c.From == "" {
		c.Action = "install"
	}
	return ts, c, true
}

// --- bestanden ---

// files vergelijkt mtime, grootte, mode en eigenaar van de gevolgde paden
// met de vorige ronde. De eerste ronde legt alleen de stand vast.
func (j *Journal) files() {
	now := j.now()
	cur := map[string]fileSnap{}
	for _, w := range watchedPaths {
		j.scan(w, cur)
	}
	if j.st.Files == nil {
		j.st.Files = cur
		j.dirty = true
		return
	}
	var all []protocol.Change
	group := map[string][]protocol.Change{}
	rootOf := func(p string) string {
		for _, w := range watchedPaths {
			if p == w.path || strings.HasPrefix(p, w.path+"/") {
				return w.path
			}
		}
		return p
	}
	for p, n := range cur {
		o, ok := j.st.Files[p]
		var c protocol.Change
		switch {
		case !ok:
			c = protocol.Change{TS: time.Unix(0, n.MTime), Kind: protocol.ChangeFile, Subject: p, Action: "created"}
		case o.MTime != n.MTime || o.Size != n.Size:
			c = protocol.Change{TS: time.Unix(0, n.MTime), Kind: protocol.ChangeFile, Subject: p, Action: "modified"}
		case o.Mode != n.Mode:
			c = protocol.Change{TS: now, Kind: protocol.ChangeFile, Subject: p, Action: "mode",
				From: strconv.FormatUint(uint64(o.Mode&0o7777), 8), To: strconv.FormatUint(uint64(n.Mode&0o7777), 8)}
		case o.UID != n.UID || o.GID != n.GID:
			c = protocol.Change{TS: now, Kind: protocol.ChangeFile, Subject: p, Action: "owner",
				From: j.owner(o.UID, o.GID), To: j.owner(n.UID, n.GID)}
		default:
			continue
		}
		group[rootOf(p)] = append(group[rootOf(p)], c)
	}
	for p := range j.st.Files {
		if _, ok := cur[p]; !ok {
			r := rootOf(p)
			group[r] = append(group[r], protocol.Change{TS: now, Kind: protocol.ChangeFile, Subject: p, Action: "deleted"})
		}
	}
	for root, cs := range group {
		if len(cs) <= fileGroup {
			all = append(all, cs...)
			continue
		}
		// Veel tegelijk, zoals bij een pakketinstallatie: één regel.
		latest := cs[0].TS
		for _, c := range cs {
			if c.TS.After(latest) {
				latest = c.TS
			}
		}
		all = append(all, protocol.Change{TS: latest, Kind: protocol.ChangeFile, Subject: root, Action: "changed",
			Detail: map[string]string{"count": strconv.Itoa(len(cs))}})
	}
	j.add(all...)
	if !mapsEqual(j.st.Files, cur) {
		j.st.Files = cur
		j.dirty = true
	}
}

// scan legt de stand vast van w en, bij een map, van wat erin staat tot
// w.depth diep. Een symlink in een map telt als zichzelf; het pad zelf
// wordt gevolgd, zoals /etc/resolv.conf.
func (j *Journal) scan(w watchedPath, out map[string]fileSnap) {
	root := j.path(w.path)
	st, err := os.Stat(root)
	if err != nil {
		return
	}
	if !st.IsDir() {
		if len(out) < maxWatchedFiles {
			out[w.path] = snapOf(st)
		}
		return
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || len(out) >= maxWatchedFiles {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if rel != "." && strings.Count(rel, string(filepath.Separator))+1 >= w.depth {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, "~") || strings.HasSuffix(name, ".swp") || strings.HasPrefix(name, ".#") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out[filepath.Join(w.path, rel)] = snapOf(info)
		return nil
	})
}

func snapOf(st fs.FileInfo) fileSnap {
	s := fileSnap{MTime: st.ModTime().UnixNano(), Size: st.Size(), Mode: uint32(st.Mode().Perm()) | modeBits(st.Mode())}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		s.UID, s.GID = sys.Uid, sys.Gid
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		s.Size = 0
	}
	return s
}

// modeBits zet setuid, setgid en sticky op hun octale plek.
func modeBits(m fs.FileMode) uint32 {
	var b uint32
	if m&fs.ModeSetuid != 0 {
		b |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		b |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		b |= 0o1000
	}
	return b
}

// owner geeft "gebruiker:groep" uit /etc/passwd en /etc/group, of de
// nummers.
func (j *Journal) owner(uid, gid uint32) string {
	name := func(file string, id uint32) string {
		want := strconv.FormatUint(uint64(id), 10)
		for line := range strings.SplitSeq(j.read(file), "\n") {
			f := strings.Split(line, ":")
			if len(f) >= 3 && f[2] == want {
				return f[0]
			}
		}
		return want
	}
	return name("/etc/passwd", uid) + ":" + name("/etc/group", gid)
}

// --- journald: SSH-logins en OOM-kills ---

// DefaultJournaldCursor is waar journalctl zijn positie bewaart.
const DefaultJournaldCursor = "/var/lib/clusterforge/journald.cursor"

var (
	sshAccepted = regexp.MustCompile(`Accepted (\S+) for (\S+) from (\S+) port \d+`)
	oomKilled   = regexp.MustCompile(`Killed process \d+ \(([^)]+)\)`)
)

// journald vraagt journalctl om de SSH- en kernelberichten sinds de vorige
// keer. journalctl bewaart zelf de cursor; de eerste keer gaat hij
// JournalMaxAge terug.
func (j *Journal) journald(ctx context.Context) {
	cursor := j.path(DefaultJournaldCursor)
	if j.Path != "" {
		cursor = filepath.Join(filepath.Dir(j.Path), "journald.cursor")
	}
	if err := os.MkdirAll(filepath.Dir(cursor), 0o700); err != nil {
		return
	}
	args := []string{"--no-pager", "-q", "-o", "short-unix", "--cursor-file=" + cursor}
	if _, err := os.Stat(cursor); err != nil {
		args = append(args, "--since=@"+strconv.FormatInt(j.now().Add(-protocol.JournalMaxAge).Unix(), 10))
	}
	args = append(args, "SYSLOG_IDENTIFIER=sshd", "SYSLOG_IDENTIFIER=sshd-session", "+", "_TRANSPORT=kernel")
	out, err := j.run(ctx, "journalctl", args...)
	if err != nil && len(out) == 0 {
		return
	}
	j.add(parseJournalctl(out)...)
}

func (j *Journal) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if j.Run != nil {
		return j.Run(ctx, name, args...)
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Output()
}

// parseJournalctl leest regels als "1728569999.123456 web-02 sshd[812]:
// Accepted publickey for root from 10.0.0.5 port 50422 ssh2: ED25519 ...".
// Van een login blijven alleen gebruiker, bron-IP en methode over.
func parseJournalctl(out []byte) []protocol.Change {
	var res []protocol.Change
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		sec, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		ts := time.Unix(0, int64(sec*1e9)).UTC().Truncate(time.Millisecond)
		if m := sshAccepted.FindStringSubmatch(line); m != nil {
			res = append(res, protocol.Change{TS: ts, Kind: protocol.ChangeLogin, Subject: m[2], Action: "ssh", By: m[2],
				Detail: map[string]string{"ip": m[3], "method": m[1]}})
			continue
		}
		if m := oomKilled.FindStringSubmatch(line); m != nil {
			res = append(res, protocol.Change{TS: ts, Kind: protocol.ChangeOOM, Subject: m[1], Action: "killed"})
		}
	}
	return res
}
