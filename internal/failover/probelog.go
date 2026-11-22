package failover

import (
	"slices"
	"sync"
	"time"
)

// ProbeLog houdt de uitkomsten van de probe op één VIP bij, in ms na de
// storing. Rampoefeningen houden er één per VIP bij. Een WriteLog houdt zo
// de schrijftest via het VIP van een database bij.
type ProbeLog struct {
	mu        sync.Mutex
	start     time.Time
	vip       string
	writes    bool
	segs      []Segment
	events    []TimelineEvent
	firstFail time.Time
	firstErr  string
	down      bool
	okRun     int
	okStart   time.Time
}

func NewProbeLog(start time.Time, vip string) *ProbeLog { return &ProbeLog{start: start, vip: vip} }

// NewWriteLog houdt de schrijftest via het VIP van een database bij; de
// gebeurtenissen heten write_down en write_up.
func NewWriteLog(start time.Time, vip string) *ProbeLog {
	return &ProbeLog{start: start, vip: vip, writes: true}
}

func (p *ProbeLog) ms(t time.Time) int64 { return t.Sub(p.start).Milliseconds() }

// Add legt één uitkomst vast; err is nil als het VIP antwoordde.
func (p *ProbeLog) Add(at time.Time, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok, ms := err == nil, p.ms(at)
	if n := len(p.segs); n > 0 {
		p.segs[n-1].ToMS = ms
		if p.segs[n-1].OK != ok {
			p.segs = append(p.segs, Segment{FromMS: ms, ToMS: ms, OK: ok})
		}
	} else {
		p.segs = append(p.segs, Segment{FromMS: ms, ToMS: ms, OK: ok})
	}
	if !ok {
		if !p.down {
			p.down = true
			e := TimelineEvent{TMS: ms, Kind: "down", Text: p.vip + " onbereikbaar: " + short(err.Error())}
			if p.writes {
				e = TimelineEvent{TMS: ms, Kind: "write_down", Text: "schrijven via " + p.vip + " mislukt: " + short(err.Error())}
			}
			p.events = append(p.events, e)
		}
		if p.firstFail.IsZero() {
			p.firstFail, p.firstErr = at, short(err.Error())
		}
		p.okRun = 0
		return
	}
	if p.okRun == 0 {
		p.okStart = at
	}
	p.okRun++
	if p.down && p.okRun == 3 {
		p.down = false
		e := TimelineEvent{TMS: p.ms(p.okStart), Kind: "up", Text: p.vip + " weer bereikbaar"}
		if p.writes {
			e = TimelineEvent{TMS: p.ms(p.okStart), Kind: "write_up", Text: "schrijven via " + p.vip + " lukt weer"}
		}
		p.events = append(p.events, e)
	}
}

// Recovered: de probe slaagde drie keer op rij na de laatste fout.
func (p *ProbeLog) Recovered() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.okRun >= 3
}

// RecoveredAt is de eerste van de drie goede op rij, als de probe hersteld
// is.
func (p *ProbeLog) RecoveredAt() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.okStart, p.okRun >= 3
}

// Failed geeft de eerste fout, of false als het VIP altijd antwoordde.
func (p *ProbeLog) Failed() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.firstErr, !p.firstFail.IsZero()
}

// Downtime loopt van de eerste fout tot de eerste van de drie goede op rij,
// of tot end als het VIP niet terugkwam.
func (p *ProbeLog) Downtime(end time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.firstFail.IsZero():
		return 0
	case p.okRun >= 3:
		return p.okStart.Sub(p.firstFail)
	}
	return end.Sub(p.firstFail)
}

// Finish sluit het laatste stuk af op end.
func (p *ProbeLog) Finish(end time.Time) ([]Segment, []TimelineEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.segs); n > 0 {
		p.segs[n-1].ToMS = max(p.segs[n-1].ToMS, p.ms(end))
	}
	return slices.Clone(p.segs), slices.Clone(p.events)
}
