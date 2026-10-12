package whatbroke

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Scope is waarover een tijdlijn gaat.
type Scope struct {
	Clusters []uuid.UUID
	Nodes    []uuid.UUID
	// Others zijn andere onderwerpen in het logboek: een incident, een
	// Proxmox-koppeling.
	Others []uuid.UUID
}

func (s Scope) empty() bool {
	return len(s.Clusters) == 0 && len(s.Nodes) == 0 && len(s.Others) == 0
}

// MaxEntries is het grootste aantal regels in één tijdlijn.
const MaxEntries = 1000

// timelineActions zijn alle actions behalve de beveiliging en de
// assistent: een viewer mag de tijdlijn zien, en een login op de
// webinterface of een vraag aan de assistent is nooit de oorzaak van een
// storing.
var timelineActions = func() []string {
	var out []string
	for _, c := range events.Categories {
		if c.Key != events.CategorySecurity && c.Key != events.CategoryAssistant {
			out = append(out, events.Actions(c.Key)...)
		}
	}
	return out
}()

// Timeline voegt het logboek, het journaal en de waarschuwingen uit
// Kubernetes over sc samen, van from tot to.
// Het geeft de laatste limit regels, oudste eerst, en of er meer waren.
func Timeline(ctx context.Context, q *store.Queries, au *audit.Service, sc Scope, from, to time.Time, limit int) ([]Entry, bool, error) {
	if limit <= 0 || limit > MaxEntries {
		limit = MaxEntries
	}
	if sc.empty() {
		return []Entry{}, false, nil
	}
	anyOf := slices.Concat(sc.Clusters, sc.Nodes, sc.Others)
	logged, next, err := au.List(ctx, audit.Filter{From: &from, To: &to, AnyOf: anyOf, Actions: timelineActions, Limit: limit})
	if err != nil {
		return nil, false, err
	}
	changes, err := q.ListChangesInScope(ctx, store.ListChangesInScopeParams{
		NodeIds: orEmpty(sc.Nodes), ClusterIds: orEmpty(sc.Clusters), Since: from, Until: to, Lim: int32(limit + 1),
	})
	if err != nil {
		return nil, false, err
	}
	warnings, err := q.ListKubeEventsInScope(ctx, store.ListKubeEventsInScopeParams{
		ClusterIds: orEmpty(sc.Clusters), NodeIds: orEmpty(sc.Nodes), Since: from, Until: to, Lim: int32(limit + 1),
	})
	if err != nil {
		return nil, false, err
	}
	more := next != nil || len(changes) > limit || len(warnings) > limit
	out := make([]Entry, 0, len(logged)+len(changes)+len(warnings))
	for _, e := range logged {
		out = append(out, FromEvent(e))
	}
	for _, c := range changes {
		out = append(out, FromChange(c))
	}
	for _, w := range warnings {
		out = append(out, FromKubeEvent(w))
	}
	Sort(out)
	if len(out) > limit {
		out, more = out[len(out)-limit:], true
	}
	return out, more, nil
}

// Sort zet regels op tijd, oudste eerst, en bij gelijke tijd op id, zodat
// de volgorde vastligt.
func Sort(entries []Entry) {
	slices.SortStableFunc(entries, func(a, b Entry) int {
		if c := a.Ts.Compare(b.Ts); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func orEmpty(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
