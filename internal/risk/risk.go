// Package risk rekent de health score: per cluster zeven scorekaarten met
// bevindingen, een totaal van 0 tot 100 en een cijfer, en één score voor
// heel ClusterForge met de kaart Platform. Score en de kaarten rekenen
// zonder database, uit vaste regels; dezelfde invoer geeft altijd dezelfde
// uitkomst. De Service verzamelt de invoer en bewaart de uitkomst.
package risk

import (
	"cmp"
	"math"
	"slices"

	"github.com/google/uuid"
)

// Severity is hoe zwaar een bevinding weegt.
type Severity string

const (
	Low      Severity = "low"
	Medium   Severity = "medium"
	High     Severity = "high"
	Critical Severity = "critical"
)

// Penalty is wat een bevinding van haar kaart aftrekt.
var Penalty = map[Severity]int{Low: 5, Medium: 10, High: 20, Critical: 40}

var severities = []Severity{Low, Medium, High, Critical}

// bump verzwaart een ernst met n stappen (negatief maakt lichter), binnen
// laag en maxSev.
func bump(s Severity, n int, maxSev Severity) Severity {
	r := min(max(slices.Index(severities, s)+n, 0), slices.Index(severities, maxSev))
	return severities[r]
}

// envBump is de verzwaring per omgeving: prod een stap zwaarder, lab een
// stap lichter dan test.
func envBump(env string) int {
	switch env {
	case "prod":
		return 1
	case "lab":
		return -1
	}
	return 0
}

// CardID is een scorekaart.
type CardID string

const (
	Availability  CardID = "availability"
	Backups       CardID = "backups"
	Recovery      CardID = "recovery"
	Redundancy    CardID = "redundancy"
	Configuration CardID = "configuration"
	Maintenance   CardID = "maintenance"
	Capacity      CardID = "capacity"
	Platform      CardID = "platform"
)

// Weights zijn de gewichten van de kaarten van een cluster, in de volgorde
// waarin ze getoond worden.
var Weights = []struct {
	Card   CardID
	Weight int
}{
	{Availability, 25}, {Backups, 20}, {Recovery, 15}, {Redundancy, 15},
	{Configuration, 10}, {Maintenance, 10}, {Capacity, 5},
}

// Subject is waar een bevinding over gaat. Een acceptatie hoort bij een
// check en een subject.
type Subject struct {
	Type string    `json:"type"`
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Finding is één bevinding uit de catalogus.
type Finding struct {
	Check    string   `json:"check"`
	Card     CardID   `json:"card"`
	Severity Severity `json:"severity"`
	Subject  Subject  `json:"subject"`
	Text     string   `json:"text"`
	// Link is het scherm waar je het oplost.
	Link string `json:"link"`
	// Points is wat de bevinding van haar kaart aftrekt; 0 als ze
	// geaccepteerd is.
	Points int `json:"points"`
	// Gain is hoeveel de score van het cluster (of de kaart Platform) stijgt
	// als de bevinding opgelost is; FleetGain hoeveel het geheel stijgt.
	Gain      float64     `json:"gain"`
	FleetGain float64     `json:"fleet_gain"`
	Accepted  *Acceptance `json:"accepted,omitempty"`
}

// Key is wat een acceptatie herkent.
func (f Finding) Key() Key { return Key{f.Check, f.Subject.Type, f.Subject.ID} }

type Key struct {
	Check       string
	SubjectType string
	SubjectID   uuid.UUID
}

// Card is een scorekaart met haar bevindingen. Score is nil als de kaart
// onbekend is: dan telt ze niet mee.
type Card struct {
	ID       CardID    `json:"id"`
	Weight   int       `json:"weight"`
	Score    *int      `json:"score"`
	Note     string    `json:"note,omitempty"`
	Findings []Finding `json:"findings"`
}

// Cap is een plafond op het totaal, met de reden.
type Cap struct {
	Max    int    `json:"max"`
	Reason string `json:"reason"`
}

// Result is de score van een cluster.
type Result struct {
	// Score is nil als geen enkele kaart bekend is.
	Score *int   `json:"score"`
	Grade string `json:"grade"`
	Cap   *Cap   `json:"cap,omitempty"`
	Cards []Card `json:"cards"`
}

// Grade geeft het cijfer bij een score.
func Grade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	case score >= 50:
		return "E"
	}
	return "F"
}

// cardScore rekent een kaart: 100 min de punten, nooit onder 0.
func cardScore(findings []Finding) int {
	s := 100
	for _, f := range findings {
		s -= f.Points
	}
	return max(s, 0)
}

// total is het gewogen gemiddelde van de bekende kaarten, met het plafond.
func total(cards []Card, c *Cap) *int {
	f, ok := weighted(cards)
	if !ok {
		return nil
	}
	t := int(math.Round(f))
	if c != nil {
		t = min(t, c.Max)
	}
	return &t
}

// weighted is het gewogen gemiddelde van de bekende kaarten, zonder
// afronden; false als geen kaart bekend is.
func weighted(cards []Card) (float64, bool) {
	var sum, weights float64
	for _, cd := range cards {
		if cd.Score == nil {
			continue
		}
		sum += float64(*cd.Score * cd.Weight)
		weights += float64(cd.Weight)
	}
	if weights == 0 {
		return 0, false
	}
	return sum / weights, true
}

// finish zet de punten (0 voor geaccepteerd), de scores van de kaarten, het
// totaal en per bevinding wat het oplossen oplevert.
func finish(cards []Card, c *Cap) Result {
	for i := range cards {
		cd := &cards[i]
		if cd.Findings == nil {
			cd.Findings = []Finding{}
		}
		for j := range cd.Findings {
			f := &cd.Findings[j]
			f.Points = Penalty[f.Severity]
			if f.Accepted != nil {
				f.Points = 0
			}
		}
		if cd.Score != nil {
			s := cardScore(cd.Findings)
			cd.Score = &s
		}
	}
	res := Result{Cards: cards, Cap: c, Score: total(cards, c)}
	if res.Score == nil {
		return res
	}
	res.Grade = Grade(*res.Score)
	for i := range cards {
		cd := &cards[i]
		if cd.Score == nil {
			continue
		}
		before := *cd.Score
		lost := 0
		for _, f := range cd.Findings {
			lost += f.Points
		}
		for j := range cd.Findings {
			f := &cd.Findings[j]
			if f.Points == 0 {
				continue
			}
			after := max(100-(lost-f.Points), 0)
			// Hoeveel het totaal stijgt als deze kaart after haalt, zonder
			// plafond: het plafond verdwijnt niet door één bevinding.
			cd.Score = &after
			up, _ := weighted(cards)
			cd.Score = &before
			base, _ := weighted(cards)
			f.Gain = round1(up - base)
		}
		sortFindings(cd.Findings)
	}
	return res
}

func sortFindings(fs []Finding) {
	slices.SortStableFunc(fs, func(a, b Finding) int {
		if a.Accepted == nil != (b.Accepted == nil) {
			if a.Accepted == nil {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.Points, a.Points); c != 0 {
			return c
		}
		return cmp.Compare(a.Text, b.Text)
	})
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// EnvWeight is hoe zwaar een cluster in het geheel telt: prod drie keer zo
// zwaar als test, lab half zo zwaar.
func EnvWeight(env string) float64 {
	switch env {
	case "prod":
		return 3
	case "lab":
		return 0.5
	}
	return 1
}

// PlatformShare is het deel van de score van het geheel dat van de kaart
// Platform komt; de rest is het gewogen gemiddelde van de clusters.
const PlatformShare = 0.2

// Scored is een cluster met zijn uitkomst, voor het geheel.
type Scored struct {
	ClusterID uuid.UUID
	Env       string
	Result    Result
}

// Fleet rekent de score van het geheel: het gewogen gemiddelde van de
// clusters met een score, en de kaart Platform voor PlatformShare. Het zet
// FleetGain op elke bevinding.
func Fleet(clusters []Scored, platform *Card) (score *int, grade string) {
	var sum, weights float64
	for _, c := range clusters {
		if c.Result.Score != nil {
			w := EnvWeight(c.Env)
			sum += float64(*c.Result.Score) * w
			weights += w
		}
	}
	clusterShare, platShare := 1-PlatformShare, PlatformShare
	switch {
	case weights == 0 && (platform == nil || platform.Score == nil):
		return nil, ""
	case weights == 0:
		clusterShare, platShare = 0, 1
	case platform == nil || platform.Score == nil:
		clusterShare, platShare = 1, 0
	}
	t := 0.0
	if weights > 0 {
		t += clusterShare * sum / weights
	}
	if platShare > 0 {
		t += platShare * float64(*platform.Score)
	}
	for _, c := range clusters {
		if c.Result.Score == nil {
			continue
		}
		share := clusterShare * EnvWeight(c.Env) / weights
		for i := range c.Result.Cards {
			for j := range c.Result.Cards[i].Findings {
				f := &c.Result.Cards[i].Findings[j]
				f.FleetGain = round1(f.Gain * share)
			}
		}
	}
	if platShare > 0 {
		for j := range platform.Findings {
			f := &platform.Findings[j]
			f.FleetGain = round1(f.Gain * platShare)
		}
	}
	s := int(math.Round(t))
	return &s, Grade(s)
}
