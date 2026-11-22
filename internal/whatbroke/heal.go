package whatbroke

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/audit"
)

// RepairActions zijn de herstelacties van Zelfherstel: het begin en de
// afloop van een herstel, en de pauze van vanzelf herstellen. Vanzelf
// herstellen aan- of uitzetten is een instelling, geen herstel.
var RepairActions = []string{
	"cluster.heal_started", "cluster.heal_done", "cluster.heal_failed", "cluster.heal_auto_paused",
	"node.heal_started", "node.heal_done", "node.heal_failed",
}

// maxRepairs is het grootste aantal herstelacties na het venster van de
// analyse.
const maxRepairs = 200

// isHeal is true voor een event van Zelfherstel: cluster.heal_* bij een
// cluster, node.heal_* bij een losse node.
func isHeal(action string) bool {
	return strings.HasPrefix(action, "cluster.heal_") || strings.HasPrefix(action, "node.heal_")
}

// healStarted is true voor het begin van een herstel.
func healStarted(action string) bool {
	return action == "cluster.heal_started" || action == "node.heal_started"
}

// responses zijn de taken van Zelfherstel die na het eerste symptoom
// begonnen. Zo'n herstel is een antwoord op de storing, geen oorzaak; dat
// geldt ook voor de andere regels van de taak.
func responses(entries []Entry, first Moment) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, e := range entries {
		if e.Source == SourceLog && healStarted(e.Action) && e.JobID != nil && e.From.After(first.To) {
			out[*e.JobID] = true
		}
	}
	return out
}

// RepairScope is waar de herstelacties van een incident over gaan: de
// geraakte clusters en hun leveranciers, de nodes die zelf misgingen en het
// incident. Anders dan bij ScopeFor horen nodes op dezelfde Proxmox-host er
// niet bij: hun herstel is geen antwoord op dit incident.
func RepairScope(incidentID uuid.UUID, subjects []Subject, w World) Scope {
	r := newReach(subjects, w)
	clusters := maps.Clone(r.clusters)
	maps.Copy(clusters, r.suppliers)
	sc := Scope{Clusters: sortedIDs(clusters), Nodes: sortedIDs(r.symptomNodes)}
	if incidentID != uuid.Nil {
		sc.Others = []uuid.UUID{incidentID}
	}
	return sc
}

// Repairs zijn de herstelacties over sc van from tot to, oudste eerst; more
// zegt dat er meer waren en dat de oudste ontbreken. De tijdlijn van de
// analyse stopt een half uur na het begin van een incident; een herstel dat
// later komt, hoort toch bij het incident.
func Repairs(ctx context.Context, au *audit.Service, sc Scope, from, to time.Time) ([]Entry, bool, error) {
	if sc.empty() || !from.Before(to) {
		return []Entry{}, false, nil
	}
	logged, next, err := au.List(ctx, audit.Filter{
		From: &from, To: &to, AnyOf: slices.Concat(sc.Clusters, sc.Nodes, sc.Others), Actions: RepairActions, Limit: maxRepairs,
	})
	if err != nil {
		return nil, false, err
	}
	out := make([]Entry, 0, len(logged))
	for _, e := range logged {
		out = append(out, FromEvent(e))
	}
	Sort(out)
	return out, next != nil, nil
}
