// Package health wacht op bewijs dat een node of cluster gezond is. Alleen
// waarnemingen die nieuwer zijn dan een gegeven tijdstip tellen: de
// opgeslagen status kan na een herstart van de server nog oud zijn, en een
// heartbeat van voor een commando zegt niets over wat het commando deed.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Poll roept check aan tot die true geeft, een fout geeft of ctx afloopt.
func Poll(ctx context.Context, every time.Duration, check func(context.Context) (bool, error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		ok, err := check(ctx)
		if err != nil && ctx.Err() == nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-t.C:
		}
	}
}

// Gate wacht op verse waarnemingen.
type Gate struct {
	q *store.Queries
	// Poll is hoe vaak de gate kijkt; korter dan een heartbeatinterval, zodat
	// geen heartbeat gemist wordt.
	Poll time.Duration
}

func NewGate(pool *pgxpool.Pool) *Gate {
	return &Gate{q: store.New(pool), Poll: time.Second}
}

// Heartbeats is hoeveel heartbeats na het tijdstip een node gezond moeten
// tonen: één kan nog onderweg geweest zijn toen het commando klaar was.
const Heartbeats = 2

// NodeReady wacht tot de node na since minstens twee heartbeats stuurde
// waarin alle services active zijn. progress hoort elke nieuwe heartbeat.
func (g *Gate) NodeReady(ctx context.Context, nodeID uuid.UUID, since time.Time, services []string, progress func(string)) error {
	seen := map[time.Time]bool{}
	last := ""
	return Poll(ctx, g.Poll, func(ctx context.Context) (bool, error) {
		rt, err := g.q.GetNodeRuntime(ctx, nodeID)
		if err != nil {
			return false, err
		}
		if rt.HeartbeatAt == nil || !rt.HeartbeatAt.After(since) || seen[*rt.HeartbeatAt] {
			return false, nil
		}
		states := ServiceStates(rt.Services)
		var waiting []string
		for _, s := range services {
			switch st := states[s]; st {
			case "active":
			case "failed":
				return false, fmt.Errorf("%s start niet op %s; kijk op de node met journalctl -u %s", s, rt.Hostname, s)
			default:
				if st == "" {
					st = "onbekend"
				}
				waiting = append(waiting, s+" is "+st)
			}
		}
		if len(waiting) > 0 {
			if msg := strings.Join(waiting, ", "); msg != last && progress != nil {
				progress(rt.Hostname + ": " + msg)
				last = msg
			}
			return false, nil
		}
		seen[*rt.HeartbeatAt] = true
		return len(seen) >= Heartbeats, nil
	})
}

// Holder is een node die een VIP heeft.
type Holder struct {
	ID       uuid.UUID `json:"id"`
	Hostname string    `json:"hostname"`
}

// Snapshot is wat de verse heartbeats van een cluster zeggen.
type Snapshot struct {
	// Holders per VIP: de actieve nodes, en die in also, die het adres in
	// een heartbeat na since melden.
	Holders map[string][]Holder
	// Waiting zijn de actieve nodes met een agent die nog geen heartbeat na
	// since stuurden.
	Waiting []string
}

// Owner geeft de enige houder van een VIP.
func (s Snapshot) Owner(vip string) (Holder, bool) {
	if h := s.Holders[vip]; len(h) == 1 {
		return h[0], true
	}
	return Holder{}, false
}

// Look leest de verse heartbeats van de actieve nodes van een cluster, en
// van de nodes in also.
func (g *Gate) Look(ctx context.Context, clusterID uuid.UUID, vips []string, since time.Time, also ...uuid.UUID) (Snapshot, error) {
	nodes, err := g.q.ListFailoverNodes(ctx, &clusterID)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Holders: map[string][]Holder{}}
	for _, v := range vips {
		snap.Holders[v] = []Holder{}
	}
	for _, n := range nodes {
		if (n.Lifecycle != store.NodeLifecycleActive && !slices.Contains(also, n.ID)) || !n.HasAgent {
			continue
		}
		if n.HeartbeatAt == nil || !n.HeartbeatAt.After(since) {
			snap.Waiting = append(snap.Waiting, n.Hostname)
			continue
		}
		for _, v := range vips {
			if slices.Contains(n.Addresses, v) {
				snap.Holders[v] = append(snap.Holders[v], Holder{ID: n.ID, Hostname: n.Hostname})
			}
		}
	}
	return snap, nil
}

// Settled wacht tot elke actieve node na since een heartbeat stuurde en elk
// VIP precies één houder heeft. Met want moet een VIP bij die node staan.
// also zijn nodes die meetellen hoewel ze (nog) niet actief zijn, zoals een
// nieuwe node bij omhoog schalen.
func (g *Gate) Settled(ctx context.Context, clusterID uuid.UUID, vips []string, since time.Time, want map[string]uuid.UUID,
	progress func(string), also ...uuid.UUID) (Snapshot, error) {
	var snap Snapshot
	last := ""
	err := Poll(ctx, g.Poll, func(ctx context.Context) (bool, error) {
		var err error
		snap, err = g.Look(ctx, clusterID, vips, since, also...)
		if err != nil {
			return false, err
		}
		var waiting []string
		if len(snap.Waiting) > 0 {
			waiting = append(waiting, "wacht op een heartbeat van "+strings.Join(snap.Waiting, ", "))
		}
		for _, v := range vips {
			h := snap.Holders[v]
			switch {
			case len(h) == 0:
				waiting = append(waiting, v+" heeft geen houder")
			case len(h) > 1:
				waiting = append(waiting, v+" staat op "+hostnames(h))
			case want[v] != uuid.Nil && h[0].ID != want[v]:
				waiting = append(waiting, v+" staat nog op "+h[0].Hostname)
			}
		}
		if len(waiting) > 0 {
			if msg := strings.Join(waiting, "; "); msg != last && progress != nil {
				progress(msg)
				last = msg
			}
			return false, nil
		}
		return true, nil
	})
	return snap, err
}

func hostnames(hs []Holder) string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.Hostname
	}
	return strings.Join(out, " en ")
}

// ServiceStates leest de toestand per unit uit een heartbeat.
func ServiceStates(raw []byte) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal(raw, &out)
	return out
}
