package docs

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

const (
	// healDays is hoe ver de sectie Zelfherstel terugkijkt.
	healDays   = 30
	healWindow = healDays * 24 * time.Hour
	// maxHealRuns is hoeveel pogingen er hoogstens in de sectie komen.
	maxHealRuns = 40
)

// healPayload is wat de events van Zelfherstel meegeven.
type healPayload struct {
	What     string   `json:"what"`
	Auto     bool     `json:"auto"`
	Reason   string   `json:"reason"`
	Problems []string `json:"problems"`
	Steps    []string `json:"steps"`
	Nodes    []string `json:"nodes"`
	Error    string   `json:"error"`
	Enabled  bool     `json:"enabled"`
}

// healOf is Zelfherstel op cluster r: Vanzelf herstellen nu, en per taak
// heal.run één poging uit de events van de laatste dertig dagen. Pauzeren
// is een wijziging, ook al noemt het de laatste taak. Nil als het cluster
// geen database heeft en Zelfherstel er niets deed.
func healOf(w *world, r store.ListDocClustersRow) *ClusterHeal {
	a := w.healAuto[r.ID]
	out := &ClusterHeal{Auto: HealAuto{Enabled: a.Enabled, Allowed: r.Environment != string(store.EnvironmentProd)}}
	if a.PausedAt != nil {
		out.Auto.Paused = or(a.PausedReason, "na mislukte pogingen")
	}
	byJob := map[string]int{}
	for _, e := range w.healEvents {
		if e.ClusterID != r.ID {
			continue
		}
		var p healPayload
		_ = json.Unmarshal(e.Payload, &p)
		switch e.Action {
		case "cluster.heal_auto_changed":
			kind := "off"
			if p.Enabled {
				kind = "on"
			}
			out.Changes = append(out.Changes, HealChange{At: e.Ts, Kind: kind, By: e.Username})
			continue
		case "cluster.heal_auto_paused":
			out.Changes = append(out.Changes, HealChange{At: e.Ts, Kind: "paused", Reason: p.Reason})
			continue
		}
		key := e.JobID
		if key == "" {
			key = "event " + strconv.FormatInt(e.ID, 10)
		}
		i, ok := byJob[key]
		if !ok {
			// Viel de start voor de laatste dertig dagen, dan begint de
			// poging bij haar einde.
			i = len(out.Runs)
			byJob[key] = i
			out.Runs = append(out.Runs, HealRun{JobID: e.JobID, At: e.Ts})
		}
		run := &out.Runs[i]
		run.What, run.Auto, run.By = or(run.What, p.What), run.Auto || p.Auto, or(run.By, e.Username)
		if len(run.Problems) == 0 {
			run.Problems = healProblems(p)
		}
		switch e.Action {
		case "cluster.heal_started":
			run.At, run.Steps, run.Nodes = e.Ts, p.Steps, p.Nodes
		case "cluster.heal_done":
			run.Result = "done"
		case "cluster.heal_failed":
			run.Result, run.Error = "failed", p.Error
		}
		run.jobStatus, run.jobError = e.JobStatus, e.JobError
	}
	// Een taak die geannuleerd werd voor ze begon, of die de runner na te
	// veel onderbrekingen opgaf, schreef geen einde: dan zegt de taak zelf
	// hoe het afliep.
	for i := range out.Runs {
		run := &out.Runs[i]
		if run.Result != "" {
			continue
		}
		switch run.jobStatus {
		case string(store.JobStatusSucceeded):
			run.Result = "done"
		case string(store.JobStatusFailed):
			run.Result, run.Error = "failed", run.jobError
		case string(store.JobStatusCanceled):
			run.Result = "canceled"
		}
	}
	if !r.HasDatabase && len(out.Runs)+len(out.Changes) == 0 {
		return nil
	}
	return out
}

// healProblems is wat er mis was: uit de start, of bij een oudere poging de
// reden van Vanzelf herstellen. Elke titel komt één keer.
func healProblems(p healPayload) []string {
	in := p.Problems
	if len(in) == 0 && p.Reason != "" {
		in = strings.Split(p.Reason, "; ")
	}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// healSection schrijft de sectie Zelfherstel van het hoofdstuk Herstel: of
// Vanzelf herstellen aan staat, wat Zelfherstel de laatste dertig dagen
// deed, nieuwste eerst, en wanneer Vanzelf herstellen veranderde. Wat
// Zelfherstel nu ziet, verandert elke minuut en staat er niet in.
func healSection(d *doc, c *Cluster, o Opts) {
	h := c.Heal
	days := strconv.Itoa(healDays) + " dagen"
	auto := "Vanzelf herstellen staat uit op dit cluster: Zelfherstel herstelt pas als een beheerder bij Databases Nu herstellen kiest."
	switch a := h.Auto; {
	case !a.Allowed:
		auto = "Op prod herstelt ClusterForge nooit vanzelf: elk herstel vraagt de bevestiging van een beheerder, met Nu herstellen bij Databases."
	case a.Paused != "":
		auto = "Vanzelf herstellen is gepauzeerd: " + esc(a.Paused) + ". Zelfherstel probeert het pas weer vanzelf als een beheerder het opnieuw aanzet; tot dan herstelt het alleen met Nu herstellen."
	case a.Enabled:
		auto = "Vanzelf herstellen staat aan op dit cluster: Zelfherstel voert een plan dat vanzelf mag zelf uit, zoals een gestopte server starten. " +
			"Adressen wijzigen gebeurt nooit vanzelf, en na drie mislukte pogingen op rij pauzeert het."
	}
	managed := ""
	if c.DB != nil && len(c.DB.Components) > 0 {
		managed = "Een onderdeel Database dat ClusterForge uitrolde, herstelt Zelfherstel niet zelf: dat gaat met de acties van het onderdeel, zoals het hoofdstuk " +
			link("Database", KindDatabase+".md") + " beschrijft."
	}
	d.p("Zelfherstel bekijkt elke minuut de MariaDB-, MySQL- en PostgreSQL-servers op de nodes en maakt bij een probleem een herstelplan.", auto, managed)

	if len(h.Runs) == 0 {
		d.p("Zelfherstel deed de laatste " + days + " niets op dit cluster.")
	} else {
		runs := slices.Clone(h.Runs)
		slices.Reverse(runs)
		slices.SortStableFunc(runs, func(a, b HealRun) int { return b.At.Compare(a.At) })
		var rows [][]string
		for i, r := range runs {
			if i == maxHealRuns {
				break
			}
			rows = append(rows, []string{o.timeText(r.At), esc(r.What), esc(strings.Join(r.Problems, "; ")), healDid(r, o), healResult(r)})
		}
		d.p("Wat Zelfherstel de laatste " + days + " deed, nieuwste eerst.")
		d.table([]string{"Wanneer", "Database", "Wat er mis was", "Wat Zelfherstel deed", "Uitkomst"}, rows)
		if len(runs) > maxHealRuns {
			d.p(fmt.Sprintf("En nog %d; ze staan allemaal in ClusterForge bij Taken.", len(runs)-maxHealRuns))
		}
	}

	if len(h.Changes) > 0 {
		var items []string
		for i := len(h.Changes) - 1; i >= 0; i-- {
			ch := h.Changes[i]
			s := "Op " + o.timeText(ch.At) + " "
			switch ch.Kind {
			case "on":
				s += "aangezet"
			case "off":
				s += "uitgezet"
			default:
				s += "gepauzeerd"
				if ch.Reason != "" {
					s += ": " + esc(ch.Reason)
				}
			}
			if o.Usernames && ch.By != "" {
				s += " door " + esc(ch.By)
			}
			items = append(items, s+".")
		}
		d.p("Vanzelf herstellen veranderde de laatste " + days + ", nieuwste eerst:")
		d.bullets(items)
	}
	d.p("Wat Zelfherstel nu ziet en zou doen, zie je in ClusterForge bij Databases, onder Zelfherstel bij de database.")
}

// healDid zegt wie het herstel vroeg en wat het plan deed.
func healDid(r HealRun, o Opts) string {
	who := "met de hand"
	if r.Auto {
		who = "vanzelf"
	} else if o.Usernames && r.By != "" {
		who += " door " + esc(r.By)
	}
	switch {
	case len(r.Steps) > 0:
		return who + ": " + esc(strings.Join(r.Steps, " "))
	case len(r.Nodes) > 0:
		return who + ", op " + esc(list(r.Nodes))
	}
	return who
}

func healResult(r HealRun) string {
	switch r.Result {
	case "done":
		return "gelukt"
	case "failed":
		if r.Error == "" {
			return "mislukt"
		}
		return "mislukt: " + esc(r.Error)
	case "canceled":
		return "geannuleerd"
	}
	return "bezig"
}
