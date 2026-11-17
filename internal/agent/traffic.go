package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Verkeer van NPM (fase 39). NPM schrijft per proxy host een toegangslog
// in data/logs/proxy-host-<id>_access.log met dit formaat (log_format proxy
// in de image van NPM):
//
//	[17/Nov/2026:08:00:01 +0000] - 200 200 - GET https app.jbogaert.be "/" [Client 1.2.3.4] [Length 30] [Gzip -] [Sent-to 10.0.0.5] "UA" "-"
//
// De agent leest alleen wat er sinds de vorige vraag bijkwam, onthoudt per
// bestand inode en positie, en geeft alles samengevat terug: per host en
// minuut, per host en client-IP. De regels zelf verlaten de node niet.

var (
	trafficLogName = regexp.MustCompile(`^proxy-host-(\d+)_access\.log$`)
	trafficLine    = regexp.MustCompile(`^\[([^\]]+)\] \S+ \S+ (\d{3}) - \S+ \S+ \S+ "[^"]*" \[Client ([^\]]+)\] \[Length (\d+)\]`)
)

const trafficStateFile = "traffic-state.json"

type trafficPos struct {
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}

// trafficMu: één lezer tegelijk, anders telt een regel twee keer.
var trafficMu sync.Mutex

func (a *Agent) npmTraffic(ctx context.Context) (*protocol.TrafficResult, error) {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	n, err := a.findNPM(ctx)
	if err != nil {
		return nil, err
	}
	res := &protocol.TrafficResult{Container: n.container, Connections: a.npmConnections(n)}
	logs := filepath.Join(a.rootPath(n.dataDir), "logs")
	entries, err := os.ReadDir(logs)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("logs van NPM: %w", err)
	}
	statePath := filepath.Join(a.npmSnapshots(), trafficStateFile)
	state := map[string]trafficPos{}
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	agg := newTrafficAgg()
	budget := int64(protocol.MaxTrafficRead)
	seen := map[string]bool{}
	for _, e := range entries {
		m := trafficLogName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		host, _ := strconv.Atoi(m[1])
		path := filepath.Join(logs, e.Name())
		seen[e.Name()] = true
		pos, more := a.readTrafficLog(path, host, state[e.Name()], agg, &budget)
		state[e.Name()] = pos
		res.More = res.More || more
		if ctx.Err() != nil {
			break
		}
	}
	// Hosts die weg zijn, vergeten.
	for name := range state {
		if !seen[name] {
			delete(state, name)
		}
	}
	if err := os.MkdirAll(a.npmSnapshots(), 0o700); err == nil {
		if b, err := json.Marshal(state); err == nil {
			tmp := statePath + ".tmp"
			if os.WriteFile(tmp, b, 0o600) == nil {
				_ = os.Rename(tmp, statePath)
			}
		}
	}
	res.Minutes, res.IPs = agg.result()
	res.Lines, res.Skipped = agg.lines, agg.skipped
	return res, nil
}

// readTrafficLog leest de nieuwe regels van één log. Na een rotatie (andere
// inode) leest hij eerst de rest van het oude bestand (.1) en dan het nieuwe
// van voren af.
func (a *Agent) readTrafficLog(path string, host int, pos trafficPos, agg *trafficAgg, budget *int64) (trafficPos, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return pos, false
	}
	ino := inodeOf(st)
	size := st.Size()
	aligned := true
	if pos.Inode == 0 && pos.Offset == 0 {
		// Nieuw voor de agent: niet de hele geschiedenis inlezen.
		start := size - protocol.TrafficFirstRead
		if start < 0 {
			start = 0
		}
		pos = trafficPos{Inode: ino, Offset: start}
		aligned = start == 0
	}
	if pos.Inode != ino {
		if old, err := os.Stat(path + ".1"); err == nil && inodeOf(old) == pos.Inode && old.Size() > pos.Offset {
			readLines(path+".1", pos.Offset, old.Size(), host, agg, budget, true)
		}
		pos = trafficPos{Inode: ino, Offset: 0}
	}
	if size < pos.Offset {
		// Ingekort (copytruncate): opnieuw van voren.
		pos.Offset = 0
		aligned = true
	}
	end, more := readLines(path, pos.Offset, size, host, agg, budget, aligned)
	pos.Offset = end
	return pos, more
}

// readLines leest [from, to) regel per regel en geeft terug tot waar hij
// kwam: altijd tot het einde van een volledige regel.
func readLines(path string, from, to int64, host int, agg *trafficAgg, budget *int64, aligned bool) (int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return from, false
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return from, false
	}
	limit := to - from
	more := false
	if limit > *budget {
		limit, more = *budget, true
	}
	r := bufio.NewReaderSize(io.LimitReader(f, limit), 64<<10)
	pos := from
	first := !aligned
	for {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Een regel langer dan 64 kB: overslaan tot de volgende.
			pos += int64(len(line))
			for err == bufio.ErrBufferFull {
				line, err = r.ReadSlice('\n')
				pos += int64(len(line))
			}
			agg.skipped++
			continue
		}
		if err != nil {
			// Een halve regel aan het einde: volgende keer.
			break
		}
		pos += int64(len(line))
		if first {
			// Begonnen midden in het bestand: de eerste regel is maar half.
			first = false
			continue
		}
		agg.add(host, line)
	}
	*budget -= pos - from
	return pos, more
}

func inodeOf(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return s.Ino
	}
	return 0
}

type trafficKey struct {
	host int
	t    time.Time
}

type ipKey struct {
	host int
	ip   string
}

type trafficAgg struct {
	minutes map[trafficKey]*protocol.TrafficMinute
	ips     map[ipKey]*protocol.TrafficIP
	lines   int
	skipped int
}

func newTrafficAgg() *trafficAgg {
	return &trafficAgg{minutes: map[trafficKey]*protocol.TrafficMinute{}, ips: map[ipKey]*protocol.TrafficIP{}}
}

// parseTrafficLine haalt tijd, status, client en grootte uit een regel.
func parseTrafficLine(line []byte) (t time.Time, status int, ip string, size int64, ok bool) {
	m := trafficLine.FindSubmatch(line)
	if m == nil {
		return
	}
	t, err := time.Parse("02/Jan/2006:15:04:05 -0700", string(m[1]))
	if err != nil {
		return
	}
	status, _ = strconv.Atoi(string(m[2]))
	ip = strings.TrimSpace(string(m[3]))
	size, _ = strconv.ParseInt(string(m[4]), 10, 64)
	return t.UTC(), status, ip, size, true
}

func (g *trafficAgg) add(host int, line []byte) {
	t, status, ip, size, ok := parseTrafficLine(line)
	if !ok {
		g.skipped++
		return
	}
	g.lines++
	k := trafficKey{host, t.Truncate(time.Minute)}
	m := g.minutes[k]
	if m == nil {
		m = &protocol.TrafficMinute{HostID: host, Minute: k.t}
		g.minutes[k] = m
	}
	m.Requests++
	m.Bytes += size
	switch status / 100 {
	case 2:
		m.S2xx++
	case 3:
		m.S3xx++
	case 4:
		m.S4xx++
	case 5:
		m.S5xx++
	}
	ik := ipKey{host, ip}
	p := g.ips[ik]
	if p == nil {
		p = &protocol.TrafficIP{HostID: host, IP: ip, First: t, Last: t}
		g.ips[ik] = p
	}
	p.Requests++
	p.Bytes += size
	switch {
	case status == 401 || status == 403:
		p.Denied++
		p.S4xx++
	case status/100 == 4:
		p.S4xx++
	case status/100 == 5:
		p.S5xx++
	}
	if t.Before(p.First) {
		p.First = t
	}
	if t.After(p.Last) {
		p.Last = t
	}
}

// result geeft de minuten op volgorde en de drukste IP's (met eerst wie
// het meest geweigerd werd).
func (g *trafficAgg) result() ([]protocol.TrafficMinute, []protocol.TrafficIP) {
	mins := make([]protocol.TrafficMinute, 0, len(g.minutes))
	for _, m := range g.minutes {
		mins = append(mins, *m)
	}
	sort.Slice(mins, func(i, j int) bool {
		if !mins[i].Minute.Equal(mins[j].Minute) {
			return mins[i].Minute.Before(mins[j].Minute)
		}
		return mins[i].HostID < mins[j].HostID
	})
	ips := make([]protocol.TrafficIP, 0, len(g.ips))
	for _, p := range g.ips {
		ips = append(ips, *p)
	}
	sort.Slice(ips, func(i, j int) bool {
		if ips[i].Denied != ips[j].Denied {
			return ips[i].Denied > ips[j].Denied
		}
		if ips[i].Requests != ips[j].Requests {
			return ips[i].Requests > ips[j].Requests
		}
		return ips[i].IP < ips[j].IP
	})
	if len(ips) > protocol.MaxTrafficIPs {
		ips = ips[:protocol.MaxTrafficIPs]
	}
	return mins, ips
}

// npmConnections telt de open TCP-verbindingen op poort 80 en 443 in het
// netwerk van de container (/proc/<pid>/net/tcp en tcp6). Met host-netwerk
// is dat het netwerk van de node.
func (a *Agent) npmConnections(n npmInstance) int {
	if n.pid <= 0 {
		return -1
	}
	total, ok := 0, false
	for _, f := range []string{"tcp", "tcp6"} {
		b, err := os.ReadFile(a.rootPath(fmt.Sprintf("/proc/%d/net/%s", n.pid, f)))
		if err != nil {
			continue
		}
		ok = true
		total += countEstablished(string(b), 80, 443)
	}
	if !ok {
		return -1
	}
	return total
}

// countEstablished telt regels in /proc/net/tcp met toestand 01
// (ESTABLISHED) waarvan de lokale poort een van ports is.
func countEstablished(table string, ports ...int) int {
	want := map[string]bool{}
	for _, p := range ports {
		want[fmt.Sprintf("%04X", p)] = true
	}
	n := 0
	for i, line := range strings.Split(table, "\n") {
		if i == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != "01" {
			continue
		}
		_, port, found := strings.Cut(f[1], ":")
		if found && want[port] {
			n++
		}
	}
	return n
}
