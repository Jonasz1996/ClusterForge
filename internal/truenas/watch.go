package truenas

// De achtergrondronde (fase 37 M4): elke WatchEvery leest ClusterForge
// het overzicht en of er een update is, voor "Aandacht nodig". Een
// paginalading vraagt TrueNAS daarvoor niets.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// WatchEvery is hoe vaak de ronde loopt.
const WatchEvery = 10 * time.Minute

// Summary is de stand van één TrueNAS na de laatste ronde.
type Summary struct {
	Server   Server    `json:"server"`
	At       time.Time `json:"at"`
	Warnings []Warning `json:"warnings"`
	// Update is de nieuwe versie als er een is.
	Update string `json:"update,omitempty"`
	Error  string `json:"error,omitempty"`
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
	t := time.NewTimer(45 * time.Second)
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

// Refresh leest alle TrueNAS-machines nu.
func (s *Service) Refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	servers, err := s.Servers(ctx)
	if err != nil {
		s.log.Warn("TrueNAS-ronde", "err", err)
		return
	}
	var sums []Summary
	for _, v := range servers {
		sum := Summary{Server: v, At: time.Now(), Warnings: []Warning{}}
		if v.Why != "" {
			sum.Error = v.Why
		} else if o, err := s.Overview(ctx, v.NodeID); err != nil {
			sum.Error = err.Error()
		} else {
			sum.Warnings = o.Warnings
			sum.Update = s.updateAvailable(ctx, v)
		}
		sums = append(sums, sum)
	}
	s.watch.mu.Lock()
	s.watch.sums = sums
	s.watch.mu.Unlock()
}

// updateAvailable vraagt TrueNAS of er een update is: update.check_available
// (tot 25.04) of update.status (25.10+). Leeg als er geen is of het niet
// lukt.
func (s *Service) updateAvailable(ctx context.Context, v Server) string {
	if data, err := s.get(ctx, v.NodeID, protocol.PVERequest{Op: protocol.TrueNASOpGet, Path: "update.check_available"}); err == nil {
		var u struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &u) == nil && u.Status == "AVAILABLE" {
			if u.Version == "" {
				return "nieuwe versie"
			}
			return u.Version
		}
		return ""
	}
	data, err := s.get(ctx, v.NodeID, protocol.PVERequest{Op: protocol.TrueNASOpGet, Path: "update.status"})
	if err != nil {
		return ""
	}
	var st struct {
		Status *struct {
			NewVersion *struct {
				Version string `json:"version"`
			} `json:"new_version"`
		} `json:"status"`
	}
	if json.Unmarshal(data, &st) == nil && st.Status != nil && st.Status.NewVersion != nil {
		return st.Status.NewVersion.Version
	}
	return ""
}
