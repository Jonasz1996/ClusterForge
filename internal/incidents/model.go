// Package incidents groepeert statusovergangen tot incidenten: één storing
// met een begin, een einde en wat er geraakt was. De detector draait na
// elke ronde van de evaluator en vergelijkt wat er nu misgaat met de open
// incidenten. Hij leest alleen; hij neemt nooit het clusterslot en stuurt
// geen commando's.
package incidents

import (
	"cmp"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Ernst.
const (
	SeverityLow      = "low"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// Soorten onderwerpen.
const (
	SubjectCluster = "cluster"
	SubjectNode    = "node"
	SubjectService = "service"
	SubjectTestRun = "test_run"
)

// Rules zijn de tijden van de detector.
type Rules struct {
	// Join is hoe ver een nieuwe overgang van het begin van een open
	// incident mag liggen om erbij te horen.
	Join time.Duration
	// Settle is hoe lang alles gezond moet zijn voor een incident sluit.
	Settle time.Duration
	// Reopen is hoe lang na het sluiten dezelfde storing het incident
	// weer opent.
	Reopen time.Duration
}

// DefaultRules zijn de tijden uit het ontwerp.
var DefaultRules = Rules{Join: 10 * time.Minute, Settle: 5 * time.Minute, Reopen: 15 * time.Minute}

// Key wijst een onderwerp aan.
type Key struct {
	Type string
	ID   uuid.UUID
}

// Problem is iets dat nu misgaat.
type Problem struct {
	Key
	Name      string
	ClusterID *uuid.UUID
	// Env is de omgeving van het cluster; leeg zonder cluster.
	Env    string
	Status string
	Reason string
	Since  time.Time
	// Primary kan een incident openen. Een node in een cluster komt alleen
	// bij een incident dat al bestaat: het cluster zelf is de storing.
	Primary bool
	// Point is een gebeurtenis zonder duur, zoals een mislukte test: hij is
	// voorbij zodra hij er is, en telt maar één keer.
	Point bool
	// TestRun is een failovertest van ClusterForge die in hetzelfde cluster
	// liep; dan is het een incident 'door test'.
	TestRun *uuid.UUID
}

// Subject is een geraakt onderwerp van een incident.
type Subject struct {
	Key
	Name               string
	ClusterID          *uuid.UUID
	First, Worst, Last string
	Reason             string
	Since              time.Time
	Until              *time.Time
	dirty              bool
}

// Incident is een incident zoals de detector het ziet.
type Incident struct {
	// ID is leeg voor een nieuw incident.
	ID           uuid.UUID
	Number       int32
	Title        string
	Severity     string
	Open         bool
	StartedAt    time.Time
	DetectedAt   time.Time
	ResolvedAt   *time.Time
	CausedByTest bool
	TestRunID    *uuid.UUID
	Subjects     []*Subject
	dirty        bool
}

func (i *Incident) subject(k Key) *Subject {
	for _, s := range i.Subjects {
		if s.Key == k {
			return s
		}
	}
	return nil
}

// World is wat er nu misgaat, met wat nodig is om overgangen te groeperen.
type World struct {
	Problems []Problem
	// Hosts zijn per onderwerp de Proxmox-hosts waarop het draait.
	Hosts map[Key][]string
	// Related zijn paren clusters waarvan het ene via de
	// afhankelijkheidsgraaf van het andere afhangt, in beide volgordes.
	Related map[[2]uuid.UUID]bool
	// Status is de huidige status van een onderwerp, voor wat er stond toen
	// het weer goed was.
	Status map[Key]string
}

// Event is een gebeurtenis voor het logboek.
type Event struct {
	Action   string
	Incident *Incident
	// From is de vorige ernst bij incident.escalated.
	From string
}

// rank ordent de statussen naar hoe erg ze zijn.
func rank(status string) int {
	switch status {
	case "degraded":
		return 1
	case "fail", "error":
		return 2
	case "down":
		return 3
	case "split_brain":
		return 4
	}
	return 0
}

func severityRank(s string) int {
	switch s {
	case SeverityHigh:
		return 1
	case SeverityCritical:
		return 2
	}
	return 0
}

// SeverityOf is de ernst van één overgang: op prod is down of split-brain
// kritiek en verminderd hoog, de rest is laag. Een node in een cluster telt
// niet mee: hoe erg het is, zegt de status van het cluster.
func SeverityOf(p Problem) string {
	if p.Env == "prod" && p.Primary {
		switch p.Status {
		case "down", "split_brain":
			return SeverityCritical
		case "degraded":
			return SeverityHigh
		}
	}
	return SeverityLow
}

// Step vergelijkt de wereld met de incidenten en geeft de incidenten terug,
// met de nieuwe erbij, en wat er in het logboek moet. incs zijn de open
// incidenten en die sinds rules.Reopen gesloten werden.
func Step(rules Rules, w World, incs []*Incident, now time.Time) ([]*Incident, []Event) {
	var evs []Event
	openOf := map[Key]*Incident{}
	recentOf := map[Key]*Incident{}
	seen := map[Key]bool{}
	for _, inc := range incs {
		for _, s := range inc.Subjects {
			seen[s.Key] = true
			if inc.Open {
				openOf[s.Key] = inc
			} else if inc.ResolvedAt != nil && now.Sub(*inc.ResolvedAt) <= rules.Reopen {
				if old := recentOf[s.Key]; old == nil || old.ResolvedAt.Before(*inc.ResolvedAt) {
					recentOf[s.Key] = inc
				}
			}
		}
	}

	problems := slices.Clone(w.Problems)
	// Eerst wat een incident kan openen, dan de nodes van clusters; binnen
	// elke groep de oudste eerst.
	slices.SortStableFunc(problems, func(a, b Problem) int {
		if a.Primary != b.Primary {
			if a.Primary {
				return -1
			}
			return 1
		}
		return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.Type, b.Type), cmp.Compare(a.Name, b.Name))
	})
	current := map[Key]bool{}
	for _, p := range problems {
		current[p.Key] = true
	}
	for _, p := range problems {
		if inc := openOf[p.Key]; inc != nil {
			update(inc, p, &evs)
			continue
		}
		if p.Point && seen[p.Key] {
			continue
		}
		if inc := joinable(rules, w, incs, p, current); inc != nil {
			add(inc, p, &evs)
			openOf[p.Key] = inc
			continue
		}
		if !p.Primary {
			continue
		}
		if inc := recentOf[p.Key]; inc != nil && !p.Point {
			inc.Open, inc.ResolvedAt, inc.dirty = true, nil, true
			evs = append(evs, Event{Action: "incident.reopened", Incident: inc})
			update(inc, p, &evs)
			for _, s := range inc.Subjects {
				openOf[s.Key] = inc
			}
			continue
		}
		inc := &Incident{
			Title: Title(p), Severity: SeverityOf(p), Open: true, StartedAt: p.Since, DetectedAt: now,
			CausedByTest: p.TestRun != nil, TestRunID: p.TestRun, dirty: true,
		}
		inc.Subjects = append(inc.Subjects, newSubject(p))
		incs = append(incs, inc)
		openOf[p.Key] = inc
		seen[p.Key] = true
		evs = append(evs, Event{Action: "incident.opened", Incident: inc})
	}

	// Wat niet meer misgaat, krijgt een einde; een incident waarin alles
	// lang genoeg weer goed is, sluit.
	for _, inc := range incs {
		if !inc.Open {
			continue
		}
		var last time.Time
		done := true
		for _, s := range inc.Subjects {
			if s.Until == nil && !current[s.Key] {
				t := now
				s.Until, s.Last, s.dirty = &t, statusOr(w.Status, s.Key), true
			}
			if s.Until == nil {
				done = false
			} else if s.Until.After(last) {
				last = *s.Until
			}
		}
		if done && now.Sub(last) >= rules.Settle {
			t := now
			inc.Open, inc.ResolvedAt, inc.dirty = false, &t, true
			evs = append(evs, Event{Action: "incident.resolved", Incident: inc})
		}
	}
	return incs, evs
}

func statusOr(m map[Key]string, k Key) string {
	if s, ok := m[k]; ok && s != "" {
		return s
	}
	return "unknown"
}

func newSubject(p Problem) *Subject {
	s := &Subject{
		Key: p.Key, Name: p.Name, ClusterID: p.ClusterID, First: p.Status, Worst: p.Status, Last: p.Status,
		Reason: p.Reason, Since: p.Since, dirty: true,
	}
	if p.Point {
		t := p.Since
		s.Until = &t
	}
	return s
}

// add zet een nieuw onderwerp bij een incident.
func add(inc *Incident, p Problem, evs *[]Event) {
	inc.Subjects = append(inc.Subjects, newSubject(p))
	inc.dirty = true
	if p.Since.Before(inc.StartedAt) {
		inc.StartedAt = p.Since
	}
	escalate(inc, p, evs)
}

// update werkt een onderwerp bij dat (weer) misgaat.
func update(inc *Incident, p Problem, evs *[]Event) {
	s := inc.subject(p.Key)
	if s == nil {
		add(inc, p, evs)
		return
	}
	worst := s.Worst
	if rank(p.Status) > rank(worst) {
		worst = p.Status
	}
	until := s.Until
	if !p.Point {
		until = nil
	}
	if s.Last != p.Status || s.Worst != worst || s.Reason != p.Reason || s.Name != p.Name || (s.Until == nil) != (until == nil) {
		s.Last, s.Worst, s.Reason, s.Name, s.Until, s.dirty = p.Status, worst, p.Reason, p.Name, until, true
	}
	escalate(inc, p, evs)
}

// escalate maakt een incident erger, nooit minder erg.
func escalate(inc *Incident, p Problem, evs *[]Event) {
	sev := SeverityOf(p)
	if severityRank(sev) <= severityRank(inc.Severity) {
		return
	}
	from := inc.Severity
	inc.Severity, inc.dirty = sev, true
	// Een nieuw incident in dezelfde ronde heeft nog geen vorige ernst.
	if inc.ID != uuid.Nil || !newInRound(*evs, inc) {
		*evs = append(*evs, Event{Action: "incident.escalated", Incident: inc, From: from})
	}
}

func newInRound(evs []Event, inc *Incident) bool {
	return slices.ContainsFunc(evs, func(e Event) bool { return e.Incident == inc && e.Action == "incident.opened" })
}

// joinable zoekt een open incident waar p bij hoort: een node bij het
// incident van zijn cluster, een dienst of test bij het incident van zijn
// cluster zolang dat cluster nog misgaat (current), en verder een verwante
// overgang die binnen rules.Join na het begin valt. Een incident door een
// test neemt alleen overgangen door een test.
func joinable(rules Rules, w World, incs []*Incident, p Problem, current map[Key]bool) *Incident {
	if p.ClusterID != nil && p.Type != SubjectCluster {
		ck := Key{SubjectCluster, *p.ClusterID}
		for _, inc := range incs {
			if !inc.Open || inc.subject(ck) == nil {
				continue
			}
			if !p.Primary || (current[ck] && inc.CausedByTest == (p.TestRun != nil)) {
				return inc
			}
		}
	}
	for _, inc := range incs {
		if !inc.Open || inc.CausedByTest != (p.TestRun != nil) {
			continue
		}
		if d := p.Since.Sub(inc.StartedAt); d > rules.Join || d < -rules.Join {
			continue
		}
		for _, s := range inc.Subjects {
			if related(w, p, s) {
				return inc
			}
		}
	}
	return nil
}

// related is true voor hetzelfde cluster, clusters die via de graaf van
// elkaar afhangen, en onderwerpen op dezelfde Proxmox-host.
func related(w World, p Problem, s *Subject) bool {
	if p.ClusterID != nil && s.ClusterID != nil {
		if *p.ClusterID == *s.ClusterID || w.Related[[2]uuid.UUID{*p.ClusterID, *s.ClusterID}] {
			return true
		}
	}
	for _, h := range w.Hosts[p.Key] {
		if slices.Contains(w.Hosts[s.Key], h) {
			return true
		}
	}
	return false
}
