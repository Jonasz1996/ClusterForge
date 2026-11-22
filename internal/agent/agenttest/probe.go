package agenttest

// De schrijftest van een failovertest: de tabel writes in
// clusterforge_probe, met per run de volgnummers. Nodes van één database
// delen dezelfde ProbeTable, zoals Galera of de replicatie van PostgreSQL de
// rijen deelt. Een node die niet mag schrijven, weigert zoals MariaDB of
// PostgreSQL.

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"sync"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ProbeTable is de tabel van de schrijftest.
type ProbeTable struct {
	mu      sync.Mutex
	created bool
	rows    map[string]map[int64]bool
	// writes telt de rijen per run die er ooit kwamen.
	writes map[string]int
}

func NewProbeTable() *ProbeTable {
	return &ProbeTable{rows: map[string]map[int64]bool{}, writes: map[string]int{}}
}

// Rows zijn de volgnummers van run die er nu zijn.
func (t *ProbeTable) Rows(run string) []int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Sorted(maps.Keys(t.rows[run]))
}

// Written is hoeveel rijen van run er ooit kwamen, ook als ze daarna
// weggehaald zijn.
func (t *ProbeTable) Written(run string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writes[run]
}

// Lose haalt rij seq van run weg, alsof hij verloren ging.
func (t *ProbeTable) Lose(run string, seq int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.rows[run], seq)
}

// Runs zijn de runs met rijen.
func (t *ProbeTable) Runs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for run, rows := range t.rows {
		if len(rows) > 0 {
			out = append(out, run)
		}
	}
	slices.Sort(out)
	return out
}

var (
	probeCreateRe = regexp.MustCompile(`^CREATE TABLE IF NOT EXISTS (?:` + "`clusterforge_probe`\\.`writes`" + `|writes) `)
	probeInsertRe = regexp.MustCompile(`^INSERT INTO (?:` + "`clusterforge_probe`\\.`writes`" + `|writes) \(run, seq\) VALUES \('([0-9a-f-]{36})', (\d+)\)$`)
	probeSelectRe = regexp.MustCompile(`^SELECT seq FROM (?:` + "`clusterforge_probe`\\.`writes`" + `|writes) WHERE run = '([0-9a-f-]{36})' ORDER BY seq$`)
	probeDeleteRe = regexp.MustCompile(`^DELETE FROM (?:` + "`clusterforge_probe`\\.`writes`" + `|writes) WHERE run = '([0-9a-f-]{36})' OR at < `)
)

// isProbe is true voor een query van de schrijftest.
func isProbe(q string) bool {
	return probeCreateRe.MatchString(q) || probeInsertRe.MatchString(q) || probeSelectRe.MatchString(q) || probeDeleteRe.MatchString(q)
}

// query voert een query van de schrijftest uit. readOnly geeft de fout van
// een node die niet mag schrijven; lezen mag dan wel.
func (t *ProbeTable) query(q, readOnly string) (rows [][]string, failed string) {
	if t == nil {
		return nil, fmt.Sprintf("database %s bestaat niet", protocol.DBProbeDatabase)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case probeCreateRe.MatchString(q):
		if readOnly != "" {
			return nil, readOnly
		}
		t.created = true
	case !t.created:
		return nil, "tabel writes bestaat niet"
	case probeInsertRe.MatchString(q):
		if readOnly != "" {
			return nil, readOnly
		}
		r := probeInsertRe.FindStringSubmatch(q)
		seq, _ := strconv.ParseInt(r[2], 10, 64)
		if t.rows[r[1]] == nil {
			t.rows[r[1]] = map[int64]bool{}
		}
		t.rows[r[1]][seq] = true
		t.writes[r[1]]++
	case probeSelectRe.MatchString(q):
		run := probeSelectRe.FindStringSubmatch(q)[1]
		for _, seq := range slices.Sorted(maps.Keys(t.rows[run])) {
			rows = append(rows, []string{strconv.FormatInt(seq, 10)})
		}
	case probeDeleteRe.MatchString(q):
		if readOnly != "" {
			return nil, readOnly
		}
		delete(t.rows, probeDeleteRe.FindStringSubmatch(q)[1])
	}
	return rows, ""
}

// mariaProbe is de schrijftest op deze MariaDB.
func (h *Host) mariaProbe(q string) ([][]string, []byte) {
	readOnly := ""
	if !h.mariaWritable() {
		readOnly = "WSREP has not yet prepared node for application use"
	}
	rows, failed := h.MariaDB.Probe.query(q, readOnly)
	switch failed {
	case "":
		return rows, nil
	case readOnly:
		return nil, []byte("ERROR 1047 (08S01) at line 1: " + failed + "\n")
	}
	return nil, []byte("ERROR 1146 (42S02) at line 1: " + failed + "\n")
}

// pgProbe is de schrijftest op deze PostgreSQL.
func (p *Postgres) pgProbe(q string) ([][]string, []byte) {
	readOnly := ""
	if p.Recovery {
		readOnly = "cannot execute statement in a read-only transaction"
	}
	rows, failed := p.Probe.query(q, readOnly)
	if failed != "" {
		return nil, []byte("ERROR:  " + failed + "\n")
	}
	return rows, nil
}
