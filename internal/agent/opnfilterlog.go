package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Het filterlog van OPNsense (fase 40). syslog-ng schrijft het per dag in
// /var/log/filter/filter_YYYYMMDD.log; latest.log wijst naar het bestand van
// vandaag. Een regel ziet er zo uit (RFC 5424, sinds 21.x):
//
//	<134>1 2026-11-17T18:50:01+01:00 fw1.lan filterlog 812 - [meta sequenceId="4"] 96,,,fae5593,vtnet1,match,pass,in,4,0x0,,64,0,0,DF,6,tcp,60,192.168.90.20,192.168.10.50,52144,445,0,S,...
//
// of in het oude formaat "Nov 17 18:50:01 fw1 filterlog[812]: 96,,,...". Na
// de kop komt de CSV van pf: regelnummer, subregel, anker, label, interface,
// reden, actie, richting, IP-versie, en dan per versie de rest.

const (
	opnFilterDir   = "/var/log/filter"
	opnFilterState = "filterlog-state.json"
)

type filterPos struct {
	// File is het bestand waar latest.log naar wees.
	File   string `json:"file"`
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}

var filterMu sync.Mutex

func (c *Collector) opnFilterLog() (*protocol.FilterLogResult, error) {
	filterMu.Lock()
	defer filterMu.Unlock()
	dir := c.path(opnFilterDir)
	cur := filepath.Join(dir, "latest.log")
	if t, err := os.Readlink(cur); err == nil {
		cur = filepath.Join(dir, filepath.Base(t))
	}
	statePath := c.path(filepath.Join(opnStateDir, opnFilterState))
	var pos filterPos
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &pos)
	}
	agg := newFilterAgg(time.Now())
	budget := int64(protocol.MaxFilterLogRead)
	res := &protocol.FilterLogResult{}
	st, err := os.Stat(cur)
	if err != nil {
		if os.IsNotExist(err) {
			return &protocol.FilterLogResult{Flows: []protocol.FilterFlow{}}, nil
		}
		return nil, err
	}
	name, ino, size := filepath.Base(cur), inodeOf(st), st.Size()
	aligned := true
	switch {
	case pos.File == "" && pos.Offset == 0:
		start := max(size-protocol.FilterLogFirstRead, 0)
		pos = filterPos{File: name, Inode: ino, Offset: start}
		aligned = start == 0
	case pos.File != name || pos.Inode != ino:
		// Een nieuwe dag: eerst de rest van het vorige bestand.
		old := filepath.Join(dir, filepath.Base(pos.File))
		if ost, err := os.Stat(old); err == nil && inodeOf(ost) == pos.Inode && ost.Size() > pos.Offset {
			_, more := readFilterLines(old, pos.Offset, ost.Size(), agg, &budget, true)
			res.More = more
		}
		pos = filterPos{File: name, Inode: ino}
	}
	if size < pos.Offset {
		pos.Offset, aligned = 0, true
	}
	end, more := readFilterLines(cur, pos.Offset, size, agg, &budget, aligned)
	pos.Offset = end
	res.More = res.More || more
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err == nil {
		if b, err := json.Marshal(pos); err == nil {
			tmp := statePath + ".tmp"
			if os.WriteFile(tmp, b, 0o600) == nil {
				_ = os.Rename(tmp, statePath)
			}
		}
	}
	res.Flows, res.Dropped = agg.result()
	res.Lines, res.Skipped = agg.lines, agg.skipped
	return res, nil
}

func readFilterLines(path string, from, to int64, agg *filterAgg, budget *int64, aligned bool) (int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return from, false
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return from, false
	}
	limit, more := to-from, false
	if limit > *budget {
		limit, more = *budget, true
	}
	r := bufio.NewReaderSize(io.LimitReader(f, limit), 64<<10)
	pos, first := from, !aligned
	for {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			pos += int64(len(line))
			for err == bufio.ErrBufferFull {
				line, err = r.ReadSlice('\n')
				pos += int64(len(line))
			}
			agg.skipped++
			continue
		}
		if err != nil {
			break
		}
		pos += int64(len(line))
		if first {
			first = false
			continue
		}
		agg.add(string(line))
	}
	*budget -= pos - from
	return pos, more
}

type filterAgg struct {
	now     time.Time
	flows   map[protocol.FilterFlow]int
	lines   int
	skipped int
	dropped int
}

func newFilterAgg(now time.Time) *filterAgg {
	return &filterAgg{now: now, flows: map[protocol.FilterFlow]int{}}
}

func (a *filterAgg) add(line string) {
	a.lines++
	k, ok := parseFilterLine(strings.TrimRight(line, "\r\n"), a.now)
	if !ok {
		a.skipped++
		return
	}
	if _, seen := a.flows[k]; !seen && len(a.flows) >= protocol.MaxFilterFlows {
		a.dropped++
		return
	}
	a.flows[k]++
}

func (a *filterAgg) result() ([]protocol.FilterFlow, int) {
	out := make([]protocol.FilterFlow, 0, len(a.flows))
	for k, n := range a.flows {
		k.Count = n
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Hour.Equal(out[j].Hour) {
			return out[i].Hour.Before(out[j].Hour)
		}
		return out[i].Count > out[j].Count
	})
	return out, a.dropped
}

// parseFilterLine leest één regel van het filterlog. De sleutel heeft
// Count 0; het uur is de tijd van de regel, of now als die niet te lezen is.
func parseFilterLine(line string, now time.Time) (protocol.FilterFlow, bool) {
	var k protocol.FilterFlow
	csv, t := "", time.Time{}
	if i := strings.Index(line, " filterlog"); i >= 0 {
		head, rest := line[:i], line[i+len(" filterlog"):]
		// De CSV begint na "]: " (oud) of na het meta-blok "] " (5424).
		if j := strings.Index(rest, "]: "); j >= 0 {
			csv = rest[j+3:]
		} else if j := strings.Index(rest, "] "); j >= 0 {
			csv = rest[j+2:]
		} else if j := strings.Index(rest, ": "); j >= 0 {
			csv = rest[j+2:]
		}
		t = filterTime(head, now)
	} else {
		return k, false
	}
	f := strings.Split(csv, ",")
	if len(f) < 9 {
		return k, false
	}
	k.Rule, k.Interface, k.Action, k.Dir = f[3], f[4], f[6], f[7]
	var proto, src, dst, dport string
	switch f[8] {
	case "4":
		if len(f) < 20 {
			return k, false
		}
		proto, src, dst = f[16], f[18], f[19]
		if len(f) > 21 {
			dport = f[21]
		}
	case "6":
		if len(f) < 17 {
			return k, false
		}
		proto, src, dst = f[12], f[15], f[16]
		if len(f) > 18 {
			dport = f[18]
		}
	default:
		return k, false
	}
	switch k.Action {
	case "pass", "block", "reject":
	default:
		return k, false
	}
	k.Proto, k.Src, k.Dst = strings.ToLower(proto), src, dst
	if k.Proto == "tcp" || k.Proto == "udp" {
		if p, err := strconv.Atoi(dport); err == nil && p > 0 && p < 65536 {
			k.Port = p
		}
	}
	if t.IsZero() {
		t = now
	}
	k.Hour = t.UTC().Truncate(time.Hour)
	return k, true
}

// filterTime leest de tijd uit de kop: RFC 3339 (na "<pri>1 ") of het oude
// "Nov 17 18:50:01".
func filterTime(head string, now time.Time) time.Time {
	parts := strings.Fields(head)
	for _, p := range parts {
		if t, err := time.Parse(time.RFC3339Nano, p); err == nil {
			return t
		}
	}
	if len(parts) >= 3 {
		s := strings.Join(parts[:3], " ")
		if t, err := time.ParseInLocation("Jan _2 15:04:05", s, time.Local); err == nil {
			t = t.AddDate(now.Year(), 0, 0)
			if t.After(now.Add(24 * time.Hour)) {
				t = t.AddDate(-1, 0, 0)
			}
			return t
		}
	}
	return time.Time{}
}
