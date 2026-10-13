package agent

// De schrijftest van een failovertest (DBWriteProbe). Elke halve seconde
// schrijft de agent een rij via het VIP, met een nieuwe verbinding zoals een
// app na een storing, en onthoudt hij welke rijen de database bevestigde.
// Na de test leest hij via het VIP welke er nog zijn: een bevestigde rij die
// ontbreekt, ging verloren. Hij gebruikt de gewone clients, mariadb en psql,
// met het wachtwoord in een bestand dat alleen root kan lezen.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// probeMax is hoeveel schrijftests de agent tegelijk onthoudt.
	probeMax = 8
	// probeCheckFor is hoe lang finish probeert te tellen, want net na een
	// failover kan het VIP nog onderweg zijn.
	probeCheckFor = 15 * time.Second
	// probeDefaultSeconds is de limiet zonder MaxSeconds.
	probeDefaultSeconds = 10 * 60
)

var probePasswordRe = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)

type probeState struct {
	mu   sync.Mutex
	runs map[string]*writeProbe
}

// writeProbe is één schrijftest.
type writeProbe struct {
	engine string
	run    string
	start  time.Time
	cancel context.CancelFunc
	done   chan struct{}
	// dir is de map met het wachtwoord; write schrijft één rij.
	dir   string
	write func(ctx context.Context, sql string) ([][]string, error)

	mu       sync.Mutex
	attempts []protocol.DBProbeAttempt
	stopped  string
	finished *protocol.DBProbeResult
	endedAt  time.Time
}

func (a *Agent) probes() *probeState {
	a.initDB()
	st := &a.db.probes
	st.mu.Lock()
	if st.runs == nil {
		st.runs = map[string]*writeProbe{}
	}
	st.mu.Unlock()
	return st
}

// writeProbe beantwoordt een DBWriteProbe.
func (a *Agent) writeProbe(ctx context.Context, engine string, req *protocol.DBProbeRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if req == nil {
		res.Error = "schrijftest zonder probe"
		return res
	}
	if engine == "" {
		engine = protocol.DBMariaDB
	}
	if !dbsql.ProbeRunRe.MatchString(req.Run) {
		res.Error = fmt.Sprintf("%q is geen id van een run", req.Run)
		return res
	}
	st := a.probes()
	st.mu.Lock()
	a.pruneProbes(st)
	p := st.runs[req.Run]
	st.mu.Unlock()
	var out *protocol.DBProbeResult
	var err error
	switch req.Action {
	case protocol.DBProbeStart:
		if p != nil {
			out = p.result(req.From)
			break
		}
		out, err = a.startProbe(ctx, st, engine, req)
	case protocol.DBProbeRead:
		if p == nil {
			err = fmt.Errorf("deze node heeft geen schrijftest %s; startte de agent opnieuw?", req.Run)
			break
		}
		out = p.result(req.From)
	case protocol.DBProbeFinish:
		if p == nil {
			err = fmt.Errorf("deze node heeft geen schrijftest %s; startte de agent opnieuw?", req.Run)
			break
		}
		out = a.finishProbe(ctx, p, req.From)
	case protocol.DBProbeStop:
		if p != nil {
			p.stop()
			p.cleanup()
			out = p.result(req.From)
		} else {
			out = &protocol.DBProbeResult{Run: req.Run, Attempts: []protocol.DBProbeAttempt{}}
		}
	default:
		err = fmt.Errorf("onbekende actie %q voor een schrijftest", req.Action)
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Probe = out
	return res
}

// pruneProbes vergeet schrijftests die lang genoeg afgelopen zijn. st.mu is
// op slot.
func (a *Agent) pruneProbes(st *probeState) {
	now := time.Now()
	for run, p := range st.runs {
		p.mu.Lock()
		old := !p.endedAt.IsZero() && now.Sub(p.endedAt) > protocol.DBProbeKeep
		p.mu.Unlock()
		if old {
			p.cleanup()
			delete(st.runs, run)
		}
	}
}

func (a *Agent) startProbe(ctx context.Context, st *probeState, engine string, req *protocol.DBProbeRequest) (*protocol.DBProbeResult, error) {
	addr, err := netip.ParseAddr(req.Host)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%q is geen IP-adres", req.Host)
	case req.Port < 1 || req.Port > 65535:
		return nil, fmt.Errorf("%d is geen poort", req.Port)
	case !probePasswordRe.MatchString(req.Password):
		return nil, errors.New("het wachtwoord van de schrijftest is 16 tot 128 letters en cijfers")
	case req.MaxSeconds < 0 || req.MaxSeconds > protocol.DBProbeMaxSeconds:
		return nil, fmt.Errorf("een schrijftest duurt hoogstens %d s", protocol.DBProbeMaxSeconds)
	}
	st.mu.Lock()
	n := len(st.runs)
	st.mu.Unlock()
	if n >= probeMax {
		return nil, fmt.Errorf("deze node doet al %d schrijftests", n)
	}
	p := &writeProbe{engine: engine, run: req.Run, done: make(chan struct{})}
	host := addr.String()
	switch engine {
	case protocol.DBPostgreSQL:
		login, err := newPGLogin(host, req.Port, protocol.DBProbeUser, req.Password)
		if err != nil {
			return nil, err
		}
		login.timeout = int(protocol.DBProbeTimeout / time.Second)
		p.dir = login.dir
		p.write = func(ctx context.Context, q string) ([][]string, error) {
			out, err := a.dbRun(ctx, "psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-F", dbsql.PGFieldSep, "-R", dbsql.PGRecordSep,
				"-d", login.conninfo(protocol.DBProbeDatabase), "-c", q)
			if err != nil {
				return nil, probeError(ctx, err)
			}
			return psqlRows(out), nil
		}
	case protocol.DBMariaDB:
		// Een client vanaf MariaDB 11.4 eist TLS als de server het kan; de
		// begroeting zegt of dat zo is.
		reach := a.probe(ctx, host, req.Port)
		if !reach.Reached || reach.Code != 0 {
			return nil, fmt.Errorf("MariaDB via %s antwoordt niet: %s", host, reach.Error)
		}
		dir, err := os.MkdirTemp("", "cf-schrijftest-")
		if err != nil {
			return nil, err
		}
		cnf := filepath.Join(dir, "client.cnf")
		body := fmt.Sprintf("[client]\nhost=%s\nport=%d\nuser=%s\npassword=%s\nconnect-timeout=%d\n",
			host, req.Port, protocol.DBProbeUser, req.Password, int(protocol.DBProbeTimeout/time.Second))
		if err := os.WriteFile(cnf, []byte(body), 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		p.dir = dir
		// Zonder de query in de fout, zodat de eerste regel de fout is.
		client := append(clientArgs(cnf, reach.TLS), "--skip-print-query-on-error")
		p.write = func(ctx context.Context, q string) ([][]string, error) {
			rows, err := a.mariaWith(ctx, client, q)
			if err != nil {
				return nil, probeError(ctx, err)
			}
			return rows, nil
		}
	default:
		return nil, fmt.Errorf("onbekende database %s", engine)
	}
	// Eerst de tabel, met dezelfde gebruiker: lukt dat niet, dan begint de
	// test niet.
	cctx, cancel := context.WithTimeout(ctx, protocol.DBProbeTimeout+time.Second)
	_, err = p.write(cctx, dbsql.ProbeCreate(engine))
	cancel()
	if err != nil {
		p.cleanup()
		return nil, fmt.Errorf("de tabel %s.%s maken via %s: %v", protocol.DBProbeDatabase, protocol.DBProbeTable, host, err)
	}
	limit := time.Duration(cmp.Or(req.MaxSeconds, probeDefaultSeconds)) * time.Second
	runCtx, stop := context.WithTimeout(context.Background(), limit)
	p.cancel, p.start = stop, time.Now()
	st.mu.Lock()
	st.runs[req.Run] = p
	st.mu.Unlock()
	go a.runProbe(runCtx, p, limit)
	a.Log.Info("schrijftest gestart", "run", req.Run, "engine", engine, "host", host, "port", req.Port)
	return p.result(0), nil
}

var exitRe = regexp.MustCompile(`^exit status \d+: `)

// probeError maakt van een fout van mariadb of psql één regel zonder
// exitcode, en van een poging die te lang duurde een eigen tekst.
func probeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("geen antwoord binnen %d s", int(protocol.DBProbeTimeout/time.Second))
	}
	return errors.New(exitRe.ReplaceAllString(dbError(err), ""))
}

// runProbe schrijft tot de test stopt. Een poging die nog liep toen de test
// stopte, telt niet: of ze bevestigd werd, is dan niet bekend.
func (a *Agent) runProbe(ctx context.Context, p *writeProbe, limit time.Duration) {
	defer close(p.done)
	t := time.NewTicker(cmp.Or(a.ProbeInterval, protocol.DBProbeInterval))
	defer t.Stop()
	for seq := int64(1); ; seq++ {
		at := time.Since(p.start)
		q, err := dbsql.ProbeInsert(p.engine, p.run, seq)
		if err == nil {
			wctx, cancel := context.WithTimeout(ctx, protocol.DBProbeTimeout)
			_, err = p.write(wctx, q)
			cancel()
		}
		if ctx.Err() != nil {
			p.end(stoppedWhy(ctx, limit))
			return
		}
		took := time.Since(p.start) - at
		att := protocol.DBProbeAttempt{Seq: seq, AtMS: at.Milliseconds(), MS: took.Milliseconds(), OK: err == nil}
		if err != nil {
			att.Error = clip(firstLine(err.Error()), 200)
		}
		p.mu.Lock()
		p.attempts = append(p.attempts, att)
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			p.end(stoppedWhy(ctx, limit))
			return
		case <-t.C:
		}
	}
}

func stoppedWhy(ctx context.Context, limit time.Duration) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("na %d s vanzelf gestopt", int(limit.Seconds()))
	}
	return ""
}

func (p *writeProbe) end(why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.endedAt.IsZero() {
		p.endedAt = time.Now()
		p.stopped = why
	}
}

// stop stopt met schrijven en wacht tot de lus klaar is.
func (p *writeProbe) stop() {
	p.cancel()
	<-p.done
}

func (p *writeProbe) cleanup() {
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// result geeft de toestand met de pogingen vanaf from.
func (p *writeProbe) result(from int) *protocol.DBProbeResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished != nil {
		out := *p.finished
		out.ElapsedMS = time.Since(p.start).Milliseconds()
		out.Attempts = slices.Clone(p.attempts[min(max(from, 0), len(p.attempts)):])
		return &out
	}
	return &protocol.DBProbeResult{
		Run: p.run, Running: p.endedAt.IsZero(), ElapsedMS: time.Since(p.start).Milliseconds(), Total: len(p.attempts),
		Attempts: slices.Clone(p.attempts[min(max(from, 0), len(p.attempts)):]), Stopped: p.stopped,
	}
}

// finishProbe stopt met schrijven, telt de rijen via het VIP en haalt ze
// weg. Het antwoord blijft bewaard.
func (a *Agent) finishProbe(ctx context.Context, p *writeProbe, from int) *protocol.DBProbeResult {
	p.mu.Lock()
	done := p.finished != nil
	p.mu.Unlock()
	if done {
		return p.result(from)
	}
	p.stop()
	p.mu.Lock()
	confirmed := map[int64]bool{}
	for _, att := range p.attempts {
		if att.OK {
			confirmed[att.Seq] = true
		}
	}
	p.mu.Unlock()
	res := protocol.DBProbeResult{Run: p.run, Confirmed: len(confirmed)}
	present, err := p.check(ctx)
	if err != nil {
		res.CheckError = clip(err.Error(), 300)
	} else {
		res.Checked = true
		for _, seq := range slices.Sorted(maps.Keys(confirmed)) {
			if !present[seq] {
				res.Missing++
				if len(res.MissingSeqs) < protocol.DBProbeMissing {
					res.MissingSeqs = append(res.MissingSeqs, seq)
				}
			}
		}
		for seq := range present {
			if !confirmed[seq] {
				res.Extra++
			}
		}
		if q, err := dbsql.ProbeDelete(p.engine, p.run); err == nil {
			dctx, cancel := context.WithTimeout(ctx, protocol.DBProbeTimeout+time.Second)
			if _, err := p.write(dctx, q); err != nil {
				a.Log.Warn("rijen van de schrijftest weghalen mislukt", "run", p.run, "err", err)
			}
			cancel()
		}
	}
	p.cleanup()
	p.mu.Lock()
	res.Total, res.Stopped = len(p.attempts), p.stopped
	p.finished = &res
	p.mu.Unlock()
	a.Log.Info("schrijftest afgerond", "run", p.run, "attempts", res.Total, "confirmed", res.Confirmed, "missing", res.Missing, "check_error", res.CheckError)
	return p.result(from)
}

// check leest de volgnummers van de run via het VIP, en probeert het een
// tijd opnieuw als dat niet lukt.
func (p *writeProbe) check(ctx context.Context) (map[int64]bool, error) {
	q, err := dbsql.ProbeSelect(p.engine, p.run)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(probeCheckFor)
	for {
		cctx, cancel := context.WithTimeout(ctx, protocol.DBProbeTimeout+time.Second)
		rows, err := p.write(cctx, q)
		cancel()
		if err == nil {
			out := map[int64]bool{}
			for _, r := range rows {
				if len(r) == 0 {
					continue
				}
				seq, err := strconv.ParseInt(strings.TrimSpace(r[0]), 10, 64)
				if err != nil {
					return nil, fmt.Errorf("onverwacht volgnummer %q", r[0])
				}
				out[seq] = true
			}
			return out, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("de rijen tellen via het VIP lukte niet: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
