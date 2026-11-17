package aiverslag

// De achtergrondronde: elke WatchEvery leest ClusterForge het overzicht
// van elk AI-verslag, voor "Aandacht nodig", de SOC-pagina en de
// dashboard-API. Een paginalading hoeft daarvoor niets te vragen.

import (
	"context"
	"sync"
	"time"
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
}

type watchState struct {
	mu   sync.Mutex
	sums []Summary
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
		}
		sums = append(sums, sum)
	}
	s.watch.mu.Lock()
	s.watch.sums = sums
	s.watch.mu.Unlock()
}
