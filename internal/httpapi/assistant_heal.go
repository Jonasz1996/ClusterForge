package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/heal"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// De tool heal leest Zelfherstel via dezelfde GET-handlers als de kaart
// Zelfherstel bij Databases: wat er mis is, het plan en de laatste poging,
// en met een cluster de taken heal.run. De hash van een plan is de
// bevestiging voor Nu herstellen en gaat niet mee. Wat de agent meegeeft,
// zoals een fout van Patroni of de adressen van etcd, gaat door
// assistant.Mask.

const (
	// healDays is hoe ver de geschiedenis terugkijkt.
	healDays = 30
	// healJobs is hoeveel taken van het cluster de tool doorzoekt.
	healJobs = 200
	// healHistoryBudget is wat de geschiedenis mag innemen; de databases
	// krijgen de rest van listBudget.
	healHistoryBudget = 4 * 1024
)

func (s *Server) healTools() []assistant.Tool {
	return []assistant.Tool{{
		Name: "heal",
		Description: "Zelfherstel van de databases (MariaDB en Galera, MySQL, PostgreSQL en Patroni), ook die ClusterForge niet " +
			"uitrolde: per database wat er mis is (code, ernst, waarom en op welke nodes), het herstelplan met de stappen en wat " +
			"het per node doet, of het plan vanzelf mag en waarom niet, of het nu niet kan, Vanzelf herstellen van het cluster " +
			"(aan, gepauzeerd, niet op prod) en de laatste herstelpoging van het afgelopen etmaal. Met een cluster ook de " +
			"herstelpogingen (taken heal.run) van de laatste 30 dagen. Leeg voor alle databases, ook die op losse nodes. De " +
			"kloktijden in vanzelf.toelichting staan al in de tijdzone uit de context, niet in UTC.",
		Properties: map[string]any{"cluster": strProp(clusterDesc)},
		Required:   []string{"cluster"},
		Label: func(in map[string]any) string {
			if v := arg(in, "cluster"); v != "" {
				return "Zelfherstel van " + v + " bekijken"
			}
			return "Zelfherstel van alle databases bekijken"
		},
		Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
			var in struct {
				Cluster string `json:"cluster"`
			}
			if err := assistant.Decode(raw, &in); err != nil {
				return nil, err
			}
			var id uuid.UUID
			if strings.TrimSpace(in.Cluster) != "" {
				var err error
				if id, err = s.findCluster(ctx, c, in.Cluster); err != nil {
					return nil, err
				}
			}
			var reps list[gen.HealReport]
			if err := s.view(ctx, c, s.ListHeal, &reps); err != nil {
				return nil, err
			}
			// Een losse node heet naar zijn hostname, zoals bij Databases.
			var groups map[string]gen.DatabaseGroup
			if id == uuid.Nil {
				var l list[gen.DatabaseGroup]
				if err := s.view(ctx, c, s.ListDatabases, &l); err != nil {
					return nil, err
				}
				groups = map[string]gen.DatabaseGroup{}
				for _, g := range l.Items {
					groups[g.Key] = g
				}
			}
			var hits []gen.HealReport
			for _, r := range reps.Items {
				if cid, err := r.ClusterId.Get(); id == uuid.Nil || (err == nil && cid == id) {
					hits = append(hits, r)
				}
			}
			out := map[string]any{}
			budget := listBudget
			if id != uuid.Nil {
				hist, more, err := s.healHistory(ctx, c, id)
				if err != nil {
					return nil, err
				}
				hist, cut := fit(hist, healHistoryBudget, false)
				note := fmt.Sprintf("Zelfherstel deed de laatste %d dagen niets op dit cluster", healDays)
				if cut > 0 || more {
					note = fmt.Sprintf("geen herstelpoging bij de nieuwste %d taken van dit cluster; oudere staan bij Taken", healJobs)
					out["geschiedenis_afgekapt"] = "niet alle herstelpogingen staan erin: de oudste ontbreken. Ze staan bij Taken."
				}
				out["geschiedenis"] = orNote(hist, note)
				budget -= healHistoryBudget
			}
			if len(hits) == 0 {
				out["opmerking"] = "ClusterForge las nog geen enkele databaseserver, dus Zelfherstel heeft niets te bekijken"
				if id != uuid.Nil {
					out["opmerking"] = "Zelfherstel kent op dit cluster geen databaseserver: ClusterForge las er geen MariaDB, MySQL of PostgreSQL op de nodes"
				}
				return out, nil
			}
			items, cut, worst := healItems(hits, groups, budget)
			out["databases"] = items
			if cut > 0 {
				note := fmt.Sprintf("%d databases zonder probleem vielen weg", cut)
				switch worst {
				case 0:
					note = fmt.Sprintf("%d databases vielen weg, ook databases die plat liggen", cut)
				case 1:
					note = fmt.Sprintf("%d databases vielen weg, ook databases met een probleem; geen ervan ligt plat", cut)
				}
				if id == uuid.Nil {
					note += "; vraag één cluster"
				}
				out["afgekapt"] = note
			}
			return out, nil
		},
	}}
}

// healItems zet de databases die plat liggen vooraan, dan die met een
// ander probleem, en de gezonde achteraan, zodat fit die laatste laat
// wegvallen. groups noemt een losse node. worst is de rang (healRank) van de
// ergste database die wegviel.
func healItems(reps []gen.HealReport, groups map[string]gen.DatabaseGroup, budget int) (kept []any, cut, worst int) {
	reps = slices.Clone(reps)
	slices.SortStableFunc(reps, func(a, b gen.HealReport) int { return cmp.Compare(healRank(a), healRank(b)) })
	items := make([]any, 0, len(reps))
	for _, r := range reps {
		name := r.ClusterName
		if _, err := r.ClusterId.Get(); err != nil {
			name = groupName(groups[r.Key])
		}
		items = append(items, healForModel(r, name))
	}
	kept, cut = fit(items, budget, false)
	worst = 2
	if cut > 0 {
		worst = healRank(reps[len(kept)])
	}
	return kept, cut, worst
}

func healRank(r gen.HealReport) int {
	if slices.ContainsFunc(r.Problems, func(p gen.HealProblem) bool { return p.Level == gen.HealProblemLevelDown }) {
		return 0
	}
	if _, err := r.Plan.Get(); err == nil || len(r.Problems) > 0 {
		return 1
	}
	return 2
}

// healForModel is Zelfherstel voor één database; name is het cluster, of de
// node van een database die in geen cluster zit.
func healForModel(r gen.HealReport, name string) any {
	out := map[string]any{"engine": r.Engine, "soort": r.Kind, "gezondheid": r.Health, "omgeving": r.ClusterEnvironment}
	if id, err := r.ClusterId.Get(); err == nil {
		out["cluster"], out["cluster_id"] = name, id
	} else {
		out["node"] = name
		if _, node, ok := strings.Cut(r.Key, "/node/"); ok {
			out["node_id"] = node
		}
	}
	problems := make([]any, 0, len(r.Problems))
	for _, p := range r.Problems {
		problems = append(problems, map[string]any{
			"code": p.Code, "ernst": p.Level, "titel": assistant.Mask(p.Title), "waarom": assistant.Mask(p.Why), "nodes": p.Nodes,
		})
	}
	out["problemen"] = problems
	if len(problems) == 0 {
		out["opmerking"] = "Zelfherstel ziet niets mis"
	}
	if p, err := r.Plan.Get(); err == nil {
		steps := make([]string, 0, len(p.Steps))
		for _, st := range p.Steps {
			steps = append(steps, assistant.Mask(st))
		}
		nodes := make([]any, 0, len(p.Nodes))
		for _, n := range p.Nodes {
			n.OldClusterAddress, n.ClusterAddress = assistant.Mask(n.OldClusterAddress), assistant.Mask(n.ClusterAddress)
			n.OldNodeAddress, n.NodeAddress = assistant.Mask(n.OldNodeAddress), assistant.Mask(n.NodeAddress)
			nodes = append(nodes, compact(n, "node_id"))
		}
		plan := map[string]any{
			"actie": p.Action, "stappen": steps, "nodes": nodes, "mag_vanzelf": p.Auto, "waarom_vanzelf": assistant.Mask(p.AutoWhy),
			"kan_niet": assistant.Mask(p.Blocked),
		}
		// Het herstel gaat met de acties van het onderdeel; de stappen
		// zeggen welke.
		if _, err := p.ManagedComponentId.Get(); err == nil {
			plan["hoort_bij_onderdeel"] = true
		}
		out["plan"] = plan
	}
	if a, err := r.Auto.Get(); err == nil {
		out["vanzelf"] = map[string]any{
			"aan": a.Enabled, "kan_aan": a.Allowed, "nu_vanzelf": a.Due, "toelichting": assistant.Mask(a.Note),
			"gepauzeerd_sinds": a.PausedAt, "reden_pauze": assistant.Mask(a.PausedReason),
		}
	}
	if l, err := r.LastRun.Get(); err == nil {
		out["laatste_herstel"] = map[string]any{
			"taak": l.JobId, "status": l.Status, "fout": assistant.Mask(l.Error), "aangemaakt": l.CreatedAt, "klaar": l.FinishedAt,
		}
	}
	return compact(out)
}

// healHistory zijn de taken heal.run van een cluster van de laatste
// healDays dagen, nieuwste eerst; more zegt dat er oudere kunnen zijn dan de
// taken die de tool doorzocht.
func (s *Server) healHistory(ctx context.Context, c assistant.Caller, id uuid.UUID) ([]any, bool, error) {
	p := gen.ListJobsParams{ClusterId: &id, Limit: ptr(healJobs)}
	var l list[gen.Job]
	if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListJobs(w, r, p) }, &l); err != nil {
		return nil, false, err
	}
	since := time.Now().AddDate(0, 0, -healDays)
	var out []any
	for _, j := range l.Items {
		if j.Kind != heal.Kind || j.CreatedAt.Before(since) {
			continue
		}
		out = append(out, compact(map[string]any{
			"taak": j.Id, "titel": j.Title, "status": j.Status, "fout": assistant.Mask(j.Error), "door": j.RequestedBy,
			"aangemaakt": j.CreatedAt, "klaar": j.FinishedAt,
		}))
	}
	more := len(l.Items) == healJobs && !l.Items[len(l.Items)-1].CreatedAt.Before(since)
	return out, more, nil
}
