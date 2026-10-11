package journal

import (
	"context"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Bewaartermijnen naast die van het journaal zelf.
const (
	TaskRetention       = 30 * 24 * time.Hour
	ConnectionRetention = 30 * 24 * time.Hour
	// ConnectionHourRetention is hoe lang de verbindingen per uur blijven:
	// het bewijs kijkt 24 uur terug.
	ConnectionHourRetention = 48 * time.Hour
)

// Pruner ruimt het journaal, de Proxmox-taken en de verbindingen op. Hij
// draait als taak van de planner, hoogstens één keer per uur.
type Pruner struct {
	q    *store.Queries
	last time.Time
}

func NewPruner(q *store.Queries) *Pruner { return &Pruner{q: q} }

func (p *Pruner) Tick(ctx context.Context, now time.Time) error {
	if !p.last.IsZero() && now.Sub(p.last) < time.Hour {
		return nil
	}
	// Het journaal in porties, zodat een grote opruiming geen lang slot
	// houdt.
	for range 100 {
		n, err := p.q.PruneNodeChanges(ctx, now.Add(-Retention))
		if err != nil {
			return err
		}
		if n < 10000 {
			break
		}
	}
	if _, err := p.q.PruneProxmoxTasks(ctx, now.Add(-TaskRetention)); err != nil {
		return err
	}
	if _, err := p.q.PruneObservedConnections(ctx, now.Add(-ConnectionRetention)); err != nil {
		return err
	}
	if _, err := p.q.PruneConnectionHours(ctx, now.Add(-ConnectionHourRetention)); err != nil {
		return err
	}
	p.last = now
	return nil
}
