package agent

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Logs lezen (fase 23). De agent leest op vraag van de server een bestand
// onder /var/log, het systemd-journaal of de log van een container, ontleedt
// de regels (logparse.go), filtert ze en geeft de nieuwste terug. Hij leest
// alleen: hij bewaart niets en volgt geen symlinks buiten /var/log.

func (a *Agent) onLogs(m *nats.Msg) {
	go func() {
		res := a.handleLogs(m.Data)
		data, err := envelope(protocol.TypeLogsResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op logverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleLogs(data []byte) protocol.LogsResponse {
	var env protocol.Envelope
	var req protocol.LogsRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeLogsRequest {
		return protocol.LogsResponse{Error: "ongeldig logverzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.LogsResponse{Error: "ongeldig logverzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.LogsResponse{Error: "logverzoek verlopen; klopt de klok van deze node?"}
	}
	timeout := protocol.LogsRequestTimeout - 5*time.Second
	if !req.Deadline.IsZero() {
		if d := time.Until(req.Deadline) - time.Second; d < timeout {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	lr := logReader{root: a.Root, stream: a.streamCmd, run: func(ctx context.Context, argv ...string) ([]byte, error) {
		return a.exec(ctx, argv[0], argv[1:]...)
	}, now: time.Now}
	switch req.Op {
	case protocol.LogsOpSources:
		return lr.sources(ctx)
	case protocol.LogsOpRead:
		if msg := protocol.ValidLogSource(req.Source); msg != "" {
			return protocol.LogsResponse{Error: msg}
		}
		if msg := protocol.CheckLogFilter(req.Filter); msg != "" {
			return protocol.LogsResponse{Error: msg}
		}
		return lr.read(ctx, req.Source, req.Filter)
	}
	return protocol.LogsResponse{Error: "onbekend logverzoek " + req.Op}
}

// streamFunc voert een commando uit en geeft elke regel van stdout en stderr
// aan fn, tot fn false geeft (dan stopt het commando).
type streamFunc func(ctx context.Context, argv []string, fn func(line string) bool) error

// streamCmd is de streamFunc van de agent. Met een vervangen Exec (tests)
// komt alle uitvoer in één keer.
func (a *Agent) streamCmd(ctx context.Context, argv []string, fn func(line string) bool) error {
	if a.Exec != nil {
		out, err := a.Exec(ctx, argv[0], argv[1:]...)
		stopped := false
		_ = eachLine(strings.NewReader(string(out)), func(l string) bool {
			if !fn(l) {
				stopped = true
				return false
			}
			return true
		})
		if stopped {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "SYSTEMD_PAGER=", "PAGER=cat")
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = pw.CloseWithError(io.EOF)
		done <- err
	}()
	stopped := false
	_ = eachLine(pr, func(l string) bool {
		if !fn(l) {
			stopped = true
			return false
		}
		return true
	})
	if stopped {
		cancel()
		_ = pr.Close()
		<-done
		return nil
	}
	return <-done
}

// eachLine leest regels; een regel langer dan MaxLogLine wordt afgekapt.
func eachLine(r io.Reader, fn func(line string) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var long []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(long) < protocol.MaxLogLine {
				long = append(long, chunk...)
			}
			continue
		}
		if len(chunk) > 0 || len(long) > 0 {
			line := chunk
			if long != nil {
				line = append(long, chunk...)
				long = nil
			}
			line = []byte(strings.TrimRight(string(line), "\r\n"))
			if len(line) > protocol.MaxLogLine {
				line = append(line[:protocol.MaxLogLine:protocol.MaxLogLine], "…"...)
			}
			if !fn(string(line)) {
				return nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// logReader leest de bronnen.
type logReader struct {
	root   string
	run    runFunc
	stream streamFunc
	now    func() time.Time
}

func (l logReader) path(p string) string {
	if l.root == "" {
		return p
	}
	return filepath.Join(l.root, p)
}

// Bestanden onder /var/log die geen tekst zijn.
var binaryLogs = []string{"wtmp", "btmp", "lastlog", "faillog", "utmp"}

var skipLogExt = []string{".xz", ".bz2", ".zst", ".zip", ".journal", ".journal~", ".db", ".sqlite", ".pid", ".lock", ".sock"}

func (l logReader) sources(ctx context.Context) protocol.LogsResponse {
	var res protocol.LogsResponse
	base := l.path(protocol.LogDir)
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		if d.IsDir() {
			if rel != "." && (strings.Count(rel, string(filepath.Separator)) >= 3 || d.Name() == "journal") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || len(res.Files) >= protocol.MaxLogFiles {
			return nil
		}
		name := d.Name()
		if slices.Contains(binaryLogs, name) || slices.ContainsFunc(skipLogExt, func(e string) bool { return strings.HasSuffix(name, e) }) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		vp := protocol.LogDir + "/" + filepath.ToSlash(rel)
		if !protocol.ValidLogPath(vp) {
			return nil
		}
		res.Files = append(res.Files, protocol.LogFileInfo{Path: vp, Size: info.Size(), Modified: info.ModTime().UTC(), Gzip: strings.HasSuffix(name, ".gz")})
		return nil
	})
	if out, err := l.run(ctx, "journalctl", "--no-pager", "--field=_SYSTEMD_UNIT"); err == nil {
		res.Journal = true
		for _, u := range strings.Split(string(out), "\n") {
			u = strings.TrimSpace(u)
			if u != "" && protocol.ValidUnit(u) {
				res.Units = append(res.Units, u)
			}
		}
		slices.Sort(res.Units)
		res.Units = slices.Compact(res.Units)
	}
	if out, err := l.run(ctx, "docker", "ps", "-a", "--format", "{{.Names}}\t{{.Image}}\t{{.State}}\t{{.Label \""+labelProject+"\"}}"); err == nil {
		res.Docker = true
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Split(line, "\t")
			if len(f) < 3 || !protocol.ValidContainerName(f[0]) {
				continue
			}
			c := protocol.LogContainerInfo{Name: f[0], Image: f[1], State: f[2]}
			if len(f) > 3 {
				c.Project = f[3]
			}
			res.Containers = append(res.Containers, c)
		}
		slices.SortFunc(res.Containers, func(a, b protocol.LogContainerInfo) int { return strings.Compare(a.Name, b.Name) })
	}
	return res
}

// logMatcher is een gecompileerd filter.
type logMatcher struct {
	pat, excl    *regexp.Regexp
	levels       map[string]bool
	since, until time.Time
}

func newLogMatcher(f protocol.LogFilter) (logMatcher, error) {
	var m logMatcher
	var err error
	if m.pat, err = protocol.CompileLogPattern(f.Pattern, f.Regex, f.IgnoreCase); err != nil {
		return m, err
	}
	if m.excl, err = protocol.CompileLogPattern(f.Exclude, f.Regex, f.IgnoreCase); err != nil {
		return m, err
	}
	if len(f.Levels) > 0 {
		m.levels = map[string]bool{}
		for _, l := range f.Levels {
			m.levels[l] = true
		}
	}
	m.since, m.until = f.Since, f.Until
	return m, nil
}

// timeFiltered is true als het filter een tijd heeft.
func (m logMatcher) timeFiltered() bool { return !m.since.IsZero() || !m.until.IsZero() }

func (m logMatcher) match(e protocol.LogEntry) bool {
	if e.TS.IsZero() {
		if m.timeFiltered() {
			return false
		}
	} else {
		if !m.since.IsZero() && e.TS.Before(m.since) {
			return false
		}
		if !m.until.IsZero() && e.TS.After(m.until) {
			return false
		}
	}
	if m.levels != nil && !m.levels[e.Level] {
		return false
	}
	text := e.Msg
	if e.Proc != "" {
		text = e.Proc + ": " + text
	}
	if m.pat != nil && !m.pat.MatchString(text) {
		return false
	}
	if m.excl != nil && m.excl.MatchString(text) {
		return false
	}
	return true
}

// collector houdt de nieuwste limit passende regels bij (de regels komen
// van oud naar nieuw binnen).
type collector struct {
	limit   int
	ring    []protocol.LogEntry
	next    int
	matched int
}

func newCollector(limit int) *collector {
	if limit <= 0 {
		limit = protocol.DefaultLogLimit
	}
	return &collector{limit: limit}
}

func (c *collector) add(e protocol.LogEntry) {
	c.matched++
	if len(c.ring) < c.limit {
		c.ring = append(c.ring, e)
		return
	}
	c.ring[c.next] = e
	c.next = (c.next + 1) % c.limit
}

func (c *collector) full() bool { return c.matched >= c.limit }

// entries geeft de regels van oud naar nieuw.
func (c *collector) entries() []protocol.LogEntry {
	if len(c.ring) < c.limit {
		return c.ring
	}
	return append(slices.Clone(c.ring[c.next:]), c.ring[:c.next]...)
}

// records voegt vervolgregels (zonder tijd) bij de regel met tijd ervoor.
type records struct {
	p    logParser
	cur  *protocol.LogEntry
	n    int
	emit func(protocol.LogEntry)
	// first is de tijd van het eerste record met een tijd.
	first time.Time
}

const maxContinuation = 100

func (r *records) line(s string) {
	e, ok := r.p.parse(s)
	if ok {
		r.flush()
		if r.first.IsZero() {
			r.first = e.TS
		}
		r.cur, r.n = &e, 0
		return
	}
	if r.cur != nil && r.n < maxContinuation && len(r.cur.Msg) < 4*protocol.MaxLogLine {
		r.cur.Msg += "\n" + s
		r.n++
		return
	}
	if strings.TrimSpace(s) == "" {
		return
	}
	r.flush()
	r.emit(e)
}

func (r *records) flush() {
	if r.cur != nil {
		r.emit(*r.cur)
		r.cur = nil
	}
}

// finish maskeert geheimen en houdt het antwoord onder MaxLogBytes.
func finish(es []protocol.LogEntry, truncated bool, scanned int) protocol.LogsResponse {
	total := 0
	cut := len(es)
	for i := len(es) - 1; i >= 0; i-- {
		es[i].Msg = protocol.MaskSecrets(es[i].Msg)
		total += len(es[i].Msg) + len(es[i].Proc) + 64
		if total > protocol.MaxLogBytes {
			truncated = true
			break
		}
		cut = i
	}
	es = es[cut:]
	if es == nil {
		es = []protocol.LogEntry{}
	}
	return protocol.LogsResponse{Entries: es, Truncated: truncated, Scanned: scanned}
}

func (l logReader) read(ctx context.Context, src protocol.LogSource, f protocol.LogFilter) protocol.LogsResponse {
	m, err := newLogMatcher(f)
	if err != nil {
		return protocol.LogsResponse{Error: err.Error()}
	}
	limit := f.Limit
	if limit <= 0 {
		limit = protocol.DefaultLogLimit
	}
	p := newLogParser(l.now())
	switch src.Kind {
	case protocol.LogKindFile:
		return l.readFile(ctx, src.Name, m, limit, p)
	case protocol.LogKindJournal:
		return l.readJournal(ctx, src.Name, m, f, limit)
	case protocol.LogKindDocker:
		return l.readDocker(ctx, src.Name, m, f, limit, p)
	}
	return protocol.LogsResponse{Error: "onbekende soort bron"}
}

// openLog opent een bestand onder /var/log zonder symlinks te volgen.
func (l logReader) openLog(name string) (*os.File, os.FileInfo, error) {
	full := l.path(name)
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, errors.New(name + " bestaat niet op deze node")
		}
		return nil, nil, err
	}
	if filepath.Clean(real) != filepath.Clean(full) {
		return nil, nil, errors.New(name + " is een symlink; open het bestand waar hij naar wijst")
	}
	fh, err := os.Open(full)
	if err != nil {
		return nil, nil, err
	}
	info, err := fh.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = fh.Close()
		return nil, nil, errors.New(name + " is geen gewoon bestand")
	}
	return fh, info, nil
}

func looksBinary(b []byte) bool {
	if len(b) > 1024 {
		b = b[:1024]
	}
	return slices.Contains(b, 0)
}

func (l logReader) readFile(ctx context.Context, name string, m logMatcher, limit int, p logParser) protocol.LogsResponse {
	fh, info, err := l.openLog(name)
	if err != nil {
		return protocol.LogsResponse{Error: err.Error()}
	}
	defer func() { _ = fh.Close() }()
	if strings.HasSuffix(name, ".gz") {
		return l.readGzip(ctx, fh, m, limit, p)
	}
	size := info.Size()
	head := make([]byte, 1024)
	n, _ := fh.ReadAt(head, max(0, size-1024))
	if looksBinary(head[:n]) {
		return protocol.LogsResponse{Error: name + " is geen tekstbestand"}
	}
	// Van achteren lezen: eerst de laatste MB, en een groter stuk zolang er
	// niet genoeg passende regels zijn en het begin van de tijdsgrens nog
	// niet bereikt is.
	window := int64(1 << 20)
	for {
		off := max(0, size-window)
		c := newCollector(limit)
		scanned := 0
		rec := &records{p: p, emit: func(e protocol.LogEntry) {
			if m.match(e) {
				c.add(e)
			}
		}}
		skipFirst := off > 0
		err := eachLine(io.NewSectionReader(fh, off, size-off), func(s string) bool {
			if skipFirst {
				skipFirst = false
				return true
			}
			scanned++
			rec.line(s)
			return ctx.Err() == nil
		})
		rec.flush()
		if err != nil {
			return protocol.LogsResponse{Error: "lezen: " + err.Error()}
		}
		if ctx.Err() != nil {
			return finish(c.entries(), true, scanned)
		}
		sinceReached := !m.since.IsZero() && !rec.first.IsZero() && rec.first.Before(m.since)
		if off == 0 || c.matched >= limit || sinceReached || window >= protocol.MaxLogScan {
			truncated := c.matched > limit || (off > 0 && !sinceReached)
			return finish(c.entries(), truncated, scanned)
		}
		window *= 4
	}
}

func (l logReader) readGzip(ctx context.Context, fh *os.File, m logMatcher, limit int, p logParser) protocol.LogsResponse {
	zr, err := gzip.NewReader(fh)
	if err != nil {
		return protocol.LogsResponse{Error: "gzip: " + err.Error()}
	}
	defer func() { _ = zr.Close() }()
	c := newCollector(limit)
	scanned := 0
	rec := &records{p: p, emit: func(e protocol.LogEntry) {
		if m.match(e) {
			c.add(e)
		}
	}}
	lr := &io.LimitedReader{R: zr, N: 4 * protocol.MaxLogScan}
	if err := eachLine(lr, func(s string) bool {
		scanned++
		rec.line(s)
		return ctx.Err() == nil
	}); err != nil {
		return protocol.LogsResponse{Error: "gzip: " + err.Error()}
	}
	rec.flush()
	return finish(c.entries(), c.matched > limit || lr.N <= 0 || ctx.Err() != nil, scanned)
}

// journalEntry is een regel van journalctl -o json.
type journalEntry struct {
	Message  json.RawMessage `json:"MESSAGE"`
	Priority string          `json:"PRIORITY"`
	Ident    string          `json:"SYSLOG_IDENTIFIER"`
	Unit     string          `json:"_SYSTEMD_UNIT"`
	Comm     string          `json:"_COMM"`
	Realtime string          `json:"__REALTIME_TIMESTAMP"`
}

func (j journalEntry) msg() string {
	var s string
	if json.Unmarshal(j.Message, &s) == nil {
		return s
	}
	// Een bericht met bytes die geen UTF-8 zijn komt als lijst van getallen.
	var b []byte
	var nums []int
	if json.Unmarshal(j.Message, &nums) == nil {
		for _, n := range nums {
			b = append(b, byte(n))
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	return ""
}

func (l logReader) readJournal(ctx context.Context, unit string, m logMatcher, f protocol.LogFilter, limit int) protocol.LogsResponse {
	filtered := f.Pattern != "" || f.Exclude != "" || len(f.Levels) > 0
	n := limit
	if filtered {
		n = protocol.MaxLogScanLines
	}
	argv := []string{"journalctl", "--no-pager", "-o", "json", "-r", "-n", strconv.Itoa(n),
		"--output-fields=MESSAGE,PRIORITY,SYSLOG_IDENTIFIER,_SYSTEMD_UNIT,_COMM"}
	if unit != "" {
		argv = append(argv, "-u", unit)
	}
	if !f.Since.IsZero() {
		argv = append(argv, "--since", "@"+strconv.FormatInt(f.Since.Unix(), 10))
	}
	if !f.Until.IsZero() {
		argv = append(argv, "--until", "@"+strconv.FormatInt(f.Until.Unix()+1, 10))
	}
	var out []protocol.LogEntry
	scanned, matched := 0, 0
	var other []string
	err := l.stream(ctx, argv, func(line string) bool {
		if !strings.HasPrefix(line, "{") {
			if len(other) < 5 && strings.TrimSpace(line) != "" {
				other = append(other, line)
			}
			return true
		}
		scanned++
		var je journalEntry
		if json.Unmarshal([]byte(line), &je) != nil {
			return true
		}
		e := protocol.LogEntry{Msg: je.msg(), Level: journalLevel(je.Priority), Proc: je.Ident}
		if e.Proc == "" {
			e.Proc = je.Comm
		}
		if us, err := strconv.ParseInt(je.Realtime, 10, 64); err == nil {
			e.TS = time.UnixMicro(us).UTC()
		}
		if m.match(e) {
			matched++
			if len(out) < limit {
				out = append(out, e)
			}
		}
		return matched <= limit && ctx.Err() == nil
	})
	if err != nil && len(out) == 0 && scanned == 0 {
		msg := "journalctl: " + err.Error()
		if len(other) > 0 {
			msg = "journalctl: " + strings.Join(other, " ")
		}
		return protocol.LogsResponse{Error: msg}
	}
	slices.Reverse(out)
	return finish(out, matched > limit || (filtered && scanned >= n) || ctx.Err() != nil, scanned)
}

func (l logReader) readDocker(ctx context.Context, name string, m logMatcher, f protocol.LogFilter, limit int, p logParser) protocol.LogsResponse {
	filtered := f.Pattern != "" || f.Exclude != "" || len(f.Levels) > 0
	n := limit
	if filtered {
		n = protocol.MaxLogScanLines
	}
	argv := []string{"docker", "logs", "--timestamps", "--tail", strconv.Itoa(n)}
	if !f.Since.IsZero() {
		argv = append(argv, "--since", strconv.FormatInt(f.Since.Unix(), 10))
	}
	if !f.Until.IsZero() {
		argv = append(argv, "--until", strconv.FormatInt(f.Until.Unix()+1, 10))
	}
	argv = append(argv, name)
	c := newCollector(limit)
	scanned := 0
	var other []string
	err := l.stream(ctx, argv, func(line string) bool {
		ts, rest, ok := strings.Cut(line, " ")
		t, terr := time.Parse(time.RFC3339Nano, ts)
		if !ok && terr == nil {
			rest = ""
		}
		if terr != nil {
			if len(other) < 5 && strings.TrimSpace(line) != "" {
				other = append(other, line)
			}
			return true
		}
		scanned++
		e, inner := p.parse(rest)
		if !inner && e.Level == "" {
			e.Level = detectLevel(e.Msg)
		}
		e.TS = t
		if m.match(e) {
			c.add(e)
		}
		return ctx.Err() == nil
	})
	if scanned == 0 {
		if len(other) > 0 {
			msg := strings.Join(other, " ")
			if strings.Contains(msg, "No such container") {
				msg = "container " + name + " bestaat niet op deze node"
			}
			return protocol.LogsResponse{Error: msg}
		}
		if err != nil {
			return protocol.LogsResponse{Error: "docker logs: " + err.Error()}
		}
	}
	return finish(c.entries(), c.matched > limit || (filtered && scanned >= n) || ctx.Err() != nil, scanned)
}
