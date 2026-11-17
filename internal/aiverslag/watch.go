package aiverslag

// De achtergrondronde: elke WatchEvery leest ClusterForge het overzicht
// van elk AI-verslag, voor "Aandacht nodig", de SOC-pagina en de
// dashboard-API. Een paginalading hoeft daarvoor niets te vragen.

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// WatchEvery is hoe vaak de ronde loopt.
const WatchEvery = 5 * time.Minute

// Summary is de stand van één AI-verslag na de laatste ronde.
type Summary struct {
	Instance Instance  `json:"instance"`
	At       time.Time `json:"at"`
	Warnings []Warning `json:"warnings"`
	// Niveau en Score komen uit de SOC-band van AI-verslag.
	Niveau  string   `json:"niveau,omitempty"`
	Redenen []string `json:"redenen,omitempty"`
	Score   *float64 `json:"score,omitempty"`
	// OpenHoog is het aantal open bevindingen met ernst hoog.
	OpenHoog int    `json:"open_hoog"`
	Error    string `json:"error,omitempty"`
	// Ext is de stand van de koppeling ext56 (M4).
	Ext *ExtStatus `json:"ext,omitempty"`
}

type pushState struct {
	at  time.Time
	err string
}

type watchState struct {
	mu     sync.Mutex
	sums   []Summary
	pushed map[uuid.UUID]pushState
}

func (s *Service) setPushed(id uuid.UUID, err error) {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	if s.watch.pushed == nil {
		s.watch.pushed = map[uuid.UUID]pushState{}
	}
	p := pushState{at: time.Now()}
	if err != nil {
		p.err = err.Error()
	}
	s.watch.pushed[id] = p
}

// withPush vult de stand van de laatste context in.
func (s *Service) withPush(id uuid.UUID, st *ExtStatus) {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	if p, ok := s.watch.pushed[id]; ok {
		at := p.at
		st.PushedAt, st.PushError = &at, p.err
	}
}

// Cached geeft de laatste ronde.
func (s *Service) Cached() []Summary {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	return append([]Summary(nil), s.watch.sums...)
}

// Watch draait de ronde tot ctx stopt.
func (s *Service) Watch(ctx context.Context) {
	t := time.NewTimer(40 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.Refresh(ctx)
		t.Reset(WatchEvery)
	}
}

// Refresh leest alle machines met AI-verslag nu.
func (s *Service) Refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	list, err := s.Instances(ctx)
	if err != nil {
		s.log.Warn("AI-verslag-ronde", "err", err)
		return
	}
	var cfCtx *Context
	var sums []Summary
	for _, v := range list {
		sum := Summary{Instance: v, At: time.Now(), Warnings: []Warning{}}
		if v.Why != "" {
			sum.Error = v.Why
			sum.Warnings = Warnings(v, nil, nil, nil)
		} else if o, err := s.Overview(ctx, v.NodeID); err != nil {
			sum.Error = err.Error()
		} else {
			sum.Warnings = o.Warnings
			fillLevel(&sum, o)
			if o.Ext != nil && o.Ext.Installed {
				if cfCtx == nil {
					c, err := s.BuildContext(ctx, s.Version, time.Now())
					if err != nil {
						s.log.Warn("AI-verslag: context opbouwen", "err", err)
					} else {
						cfCtx = &c
					}
				}
				if cfCtx != nil {
					s.setPushed(v.NodeID, s.PushContext(ctx, v.NodeID, *cfCtx))
				}
				st := *o.Ext
				s.withPush(v.NodeID, &st)
				sum.Ext = &st
			}
		}
		sums = append(sums, sum)
	}
	s.watch.mu.Lock()
	s.watch.sums = sums
	s.watch.mu.Unlock()
}
