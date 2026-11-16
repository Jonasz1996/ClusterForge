package soc

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Drempels van de regels. Ze staan in docs/design/fase25-soc.md.
const (
	// BruteForceLogins mislukte logins van één extern adres in één uur.
	BruteForceLogins = 10
	// SpreadNodes: een extern adres dat zoveel nodes aanvalt en nergens
	// geband is.
	SpreadNodes = 3
	// SpikeFactor × het daggemiddelde van de week ervoor, en minstens
	// SpikeMin aanvallen in 24 uur.
	SpikeFactor = 3.0
	SpikeMin    = 20
)

// Incident is een open incident.
type Incident struct {
	ID       uuid.UUID
	Number   int
	Title    string
	Severity string
}

// Burst is het adres met de meeste mislukte logins in het laatste uur.
type Burst struct {
	IP    string
	Count int
}

// Inputs is wat evaluate nodig heeft; gather vult het uit de database.
type Inputs struct {
	Incidents []Incident
	Coverage  []CoverageRow
	// AttackedNodes: CrowdSec-alerts in de laatste 24 uur per node.
	AttackedNodes map[uuid.UUID]int
	// Attackers over het venster; evaluate zoekt er de verspreide uit.
	Attackers []Attacker
	// Attacks24h en AttacksBaseline (gemiddelde per dag over de 7 dagen
	// daarvoor) voor een piek.
	Attacks24h      int
	AttacksBaseline float64
	BruteForce      *Burst
	// ExternalLoginFailures in het venster.
	ExternalLoginFailures int
	AdminsWithoutTOTP     []string
	SSHFromAny            bool
	// Vulns: de beveiligingsmodule (fase 27); nil als die er niet is.
	Vulns *Vulns
}

// Vulns zijn de open bevindingen van Kwetsbaarheden.
type Vulns struct {
	// KEVNodes: nodes met een actief misbruikte kwetsbaarheid.
	KEVNodes []string
	// CriticalNet: adressen met een kritieke bevinding uit een netwerkscan
	// (Docker-API open, standaardwachtwoord, ...).
	CriticalNet []string
	// HighCVE en HighNet: aantal open bevindingen met ernst hoog.
	HighCVE, HighNet int
	HardeningAvg     *int
	LowHardening     []string
}

// result is wat evaluate teruggeeft.
type result struct {
	Level Level
	Score Score
	Now   []Item
}

// reason is een kandidaatreden voor het niveau.
type reason struct {
	level string
	prio  float64
	text  string
}

// evaluate past de vaste regels toe: niveau, score en "Nu belangrijk".
func evaluate(in Inputs) result {
	var items []Item
	var reasons []reason
	item := func(level string, it Item) {
		items = append(items, it)
		if level != "" {
			reasons = append(reasons, reason{level, it.Prio, it.Title})
		}
	}

	// Incidenten.
	for _, inc := range in.Incidents {
		it := Item{Key: "incident-" + inc.ID.String(), Title: fmt.Sprintf("Incident #%d: %s", inc.Number, inc.Title),
			Href: "/incidenten/detail?id=" + inc.ID.String(), Action: "Incident openen"}
		switch inc.Severity {
		case "critical":
			it.Tone, it.Prio, it.Detail = ToneRed, 10, "Kritiek incident, nog open."
			item(LevelCritical, it)
		case "high":
			it.Tone, it.Prio, it.Detail = ToneRed, 8, "Ernstig incident, nog open."
			item(LevelElevated, it)
		default:
			it.Tone, it.Prio, it.Detail = ToneAmber, 4, "Incident, nog open."
			item(LevelWatch, it)
		}
	}

	// Brute force op ClusterForge zelf.
	if b := in.BruteForce; b != nil && b.Count >= BruteForceLogins {
		item(LevelCritical, Item{Key: "bruteforce-" + b.IP, Tone: ToneRed, Prio: 9,
			Title:  fmt.Sprintf("Brute force op ClusterForge vanaf %s", b.IP),
			Detail: fmt.Sprintf("%d mislukte logins in het laatste uur. Blokkeer het adres in de firewall voor ClusterForge, of zet de server achter een VPN.", b.Count),
			Href:   "/soc?ip=" + b.IP, Action: "Aanvaller bekijken"})
	} else if in.ExternalLoginFailures > 0 {
		item(LevelElevated, Item{Key: "logins-extern", Tone: ToneAmber, Prio: 5,
			Title:  plural(in.ExternalLoginFailures, "mislukte login", "mislukte logins") + " van buiten je eigen netwerken",
			Detail: "ClusterForge is bereikbaar van buitenaf en iemand probeert in te loggen.",
			Href:   "/logboek?category=security&q=auth.login_failed", Action: "Logboek openen"})
	}

	// Coverage: bouncers en engines die niet draaien, toepassen mislukt.
	var bouncerHit, bouncerDown, csDown, applyErr, offline []string
	var noFirewall, preview, noCrowdSec, noBouncer []string
	var fwUnmanaged, csUnmanaged []string
	var updNodes []string
	updPkgs := 0
	for _, c := range in.Coverage {
		h := c.Hostname
		if !c.Online {
			offline = append(offline, h)
		}
		if c.Bouncer == StateDown {
			if in.AttackedNodes[c.NodeID] > 0 {
				bouncerHit = append(bouncerHit, h)
			} else {
				bouncerDown = append(bouncerDown, h)
			}
		}
		if c.CrowdSec == StateDown {
			csDown = append(csDown, h)
		}
		if c.Firewall == StateError || c.CrowdSec == StateError || c.Firewall == StateDown {
			applyErr = append(applyErr, h)
		}
		switch c.Firewall {
		case StateMissing:
			noFirewall = append(noFirewall, h)
		case StatePreview:
			preview = append(preview, h)
		case StateUnmanaged:
			fwUnmanaged = append(fwUnmanaged, h)
		}
		switch c.CrowdSec {
		case StateMissing:
			noCrowdSec = append(noCrowdSec, h)
		case StateUnmanaged:
			csUnmanaged = append(csUnmanaged, h)
		}
		if c.CrowdSec == StateOn && c.Bouncer == StateOff {
			noBouncer = append(noBouncer, h)
		}
		if c.SecurityUpdates > 0 {
			updNodes = append(updNodes, h)
			updPkgs += c.SecurityUpdates
		}
	}
	if len(bouncerHit) > 0 {
		item(LevelCritical, Item{Key: "bouncer-hit", Tone: ToneRed, Prio: 9.5,
			Title:  "Bouncer ligt plat op een aangevallen node: " + names(bouncerHit),
			Detail: "CrowdSec beslist bans, maar niets houdt het verkeer tegen.",
			Href:   "/crowdsec?tab=nodes", Action: "Bouncer nakijken"})
	}
	if len(bouncerDown) > 0 {
		item(LevelElevated, Item{Key: "bouncer-down", Tone: ToneAmber, Prio: 7,
			Title:  "Bouncer draait niet op " + names(bouncerDown),
			Detail: "Bans worden niet tegengehouden zolang de bouncer stilstaat.",
			Href:   "/crowdsec?tab=nodes", Action: "Bouncer nakijken"})
	}
	if len(csDown) > 0 {
		item(LevelElevated, Item{Key: "crowdsec-down", Tone: ToneAmber, Prio: 7,
			Title:  "CrowdSec draait niet op " + names(csDown),
			Detail: "Deze nodes zien geen aanvallen meer.",
			Href:   "/crowdsec?tab=nodes", Action: "CrowdSec nakijken"})
	}
	if len(applyErr) > 0 {
		item(LevelElevated, Item{Key: "apply-error", Tone: ToneAmber, Prio: 6,
			Title:  "Bescherming niet toegepast op " + names(applyErr),
			Detail: "Firewall of CrowdSec toepassen mislukte, of ufw staat uit op een beheerde node.",
			Href:   "/soc#dekking", Action: "Dekking bekijken"})
	}

	// Aanvallers die zich verspreiden.
	for _, a := range in.Attackers {
		if a.Kind == KindExternal && !a.Banned && len(a.Nodes) >= SpreadNodes && a.Alerts > 0 {
			item(LevelCritical, Item{Key: "spread-" + a.IP, Tone: ToneRed, Prio: 8.5,
				Title:  fmt.Sprintf("%s valt %d nodes aan en is nergens geband", a.IP, len(a.Nodes)),
				Detail: strings.Join(firstN(a.Scenarios, 3), ", "),
				Href:   "/soc?ip=" + a.IP, Action: "Aanvaller bekijken"})
		}
	}

	// Piek.
	if in.Attacks24h >= SpikeMin && in.Attacks24h > int(math.Ceil(SpikeFactor*in.AttacksBaseline)) {
		factor := "veel"
		if in.AttacksBaseline > 0 {
			factor = fmt.Sprintf("%.0f×", float64(in.Attacks24h)/in.AttacksBaseline)
		}
		item(LevelElevated, Item{Key: "spike", Tone: ToneAmber, Prio: 6,
			Title:  fmt.Sprintf("%d aanvallen in 24 uur, %s meer dan normaal", in.Attacks24h, factor),
			Detail: fmt.Sprintf("Gemiddeld %.0f per dag de week ervoor.", in.AttacksBaseline),
			Href:   "/crowdsec?tab=alerts", Action: "Alerts bekijken"})
	}

	// Blinde vlekken en gaten: waakzaam.
	if len(offline) > 0 {
		item(LevelWatch, Item{Key: "offline", Tone: ToneAmber, Prio: 5,
			Title:  plural(len(offline), "agent antwoordt", "agents antwoorden") + " niet: " + names(offline),
			Detail: "Daar ziet ClusterForge geen aanvallen, bans of updates.",
			Href:   "/nodes", Action: "Nodes openen"})
	}
	if len(noFirewall) > 0 {
		item(LevelWatch, Item{Key: "gap-firewall", Tone: ToneAmber, Prio: 3,
			Title:  plural(len(noFirewall), "node", "nodes") + " zonder firewallbeheer: " + names(noFirewall),
			Detail: "Zet de firewall aan in voorbeeld en daarna beheerd, of zet hem bewust uit.",
			Href:   "/firewall?tab=nodes", Action: "Firewall openen"})
	}
	if len(noCrowdSec) > 0 {
		item(LevelWatch, Item{Key: "gap-crowdsec", Tone: ToneAmber, Prio: 3,
			Title:  plural(len(noCrowdSec), "node", "nodes") + " zonder CrowdSec: " + names(noCrowdSec),
			Detail: "Aanvallen op deze nodes worden niet gezien of geband.",
			Href:   "/crowdsec?tab=nodes", Action: "CrowdSec openen"})
	}
	if len(updNodes) > 0 {
		item(LevelWatch, Item{Key: "security-updates", Tone: ToneAmber, Prio: 3.5,
			Title:  fmt.Sprintf("%s op beveiligingsupdates (%s)", plural(len(updNodes), "node wacht", "nodes wachten"), plural(updPkgs, "pakket", "pakketten")),
			Detail: names(updNodes),
			Href:   "/updates", Action: "Updates openen"})
	}
	if len(in.AdminsWithoutTOTP) > 0 {
		item(LevelWatch, Item{Key: "totp", Tone: ToneAmber, Prio: 4,
			Title:  "Tweestapsverificatie staat uit voor " + names(in.AdminsWithoutTOTP),
			Detail: "Een beheerder zonder tweede stap is één gelekt wachtwoord verwijderd van al je clusters.",
			Href:   "/instellingen#tweestap", Action: "Instellen"})
	}
	if in.SSHFromAny {
		item(LevelWatch, Item{Key: "ssh-any", Tone: ToneAmber, Prio: 3,
			Title:  "SSH staat open voor iedereen op de beheerde nodes",
			Detail: "Beperk SSH tot het LAN of je beheernetwerken.",
			Href:   "/firewall?tab=instellingen", Action: "Instellingen"})
	}
	if len(preview) > 0 {
		items = append(items, Item{Key: "firewall-preview", Tone: ToneInfo, Prio: 1,
			Title:  "Firewall staat alleen in voorbeeld op " + names(preview),
			Detail: "Het plan staat klaar; zet de node op beheerd om het toe te passen.",
			Href:   "/firewall?tab=nodes", Action: "Firewall openen"})
	}
	if len(csUnmanaged) > 0 {
		items = append(items, Item{Key: "crowdsec-unmanaged", Tone: ToneInfo, Prio: 2,
			Title:  "CrowdSec draait, maar ClusterForge beheert het niet op " + names(csUnmanaged),
			Detail: "Zet het aan bij CrowdSec → Nodes: dan worden bans gedeeld en ziet ClusterForge of de bouncer werkt.",
			Href:   "/crowdsec?tab=nodes", Action: "CrowdSec openen"})
	}
	if len(fwUnmanaged) > 0 {
		items = append(items, Item{Key: "firewall-unmanaged", Tone: ToneInfo, Prio: 2,
			Title:  "ufw staat aan, maar ClusterForge beheert het niet op " + names(fwUnmanaged),
			Detail: "Zet de node op voorbeeld bij Firewall → Nodes om de regels te zien en verkeer naar dichte poorten te tellen.",
			Href:   "/firewall?tab=nodes", Action: "Firewall openen"})
	}
	if len(noBouncer) > 0 {
		items = append(items, Item{Key: "no-bouncer", Tone: ToneInfo, Prio: 1,
			Title:  "CrowdSec zonder bouncer op " + names(noBouncer),
			Detail: "Alleen detectie: aanvallers worden niet tegengehouden.",
			Href:   "/crowdsec?tab=nodes", Action: "CrowdSec openen"})
	}

	// Kwetsbaarheden en verharding (fase 27).
	if v := in.Vulns; v != nil {
		if len(v.CriticalNet) > 0 {
			item(LevelCritical, Item{Key: "vuln-network", Tone: ToneRed, Prio: 8.8,
				Title:  "Kritieke netwerkbevinding op " + names(v.CriticalNet),
				Detail: "Een netwerkscan vond een dienst die iedereen op je netwerk volledige toegang geeft, zoals een open Docker-API of een standaardwachtwoord.",
				Href:   "/kwetsbaarheden/scans?tab=bevindingen", Action: "Bevindingen openen"})
		}
		if len(v.KEVNodes) > 0 {
			item(LevelElevated, Item{Key: "vuln-kev", Tone: ToneRed, Prio: 7.5,
				Title:  "Actief misbruikte kwetsbaarheid op " + names(v.KEVNodes),
				Detail: "CISA ziet aanvallers deze kwetsbaarheid nu gebruiken. Werk de pakketten bij.",
				Href:   "/kwetsbaarheden", Action: "Kwetsbaarheden openen"})
		}
		if n := v.HighCVE + v.HighNet; n > 0 {
			item(LevelWatch, Item{Key: "vuln-high", Tone: ToneAmber, Prio: 3.2,
				Title:  plural(n, "ernstige kwetsbaarheid", "ernstige kwetsbaarheden") + " open",
				Detail: "Bekende kwetsbaarheden met ernst hoog in pakketten of open diensten.",
				Href:   "/kwetsbaarheden", Action: "Kwetsbaarheden openen"})
		}
		if len(v.LowHardening) > 0 {
			items = append(items, Item{Key: "hardening-low", Tone: ToneInfo, Prio: 2.5,
				Title:  "Zwakke verharding op " + names(v.LowHardening),
				Detail: "Deze nodes halen minder dan 70% op de verhardingscontroles.",
				Href:   "/kwetsbaarheden/verharding", Action: "Verharding openen"})
		}
	}

	slices.SortStableFunc(items, func(a, b Item) int {
		switch {
		case a.Prio > b.Prio:
			return -1
		case a.Prio < b.Prio:
			return 1
		}
		return 0
	})
	if items == nil {
		items = []Item{}
	}

	return result{
		Level: level(reasons),
		Score: score(in, items),
		Now:   items,
	}
}

// level kiest het hoogste niveau en de twee zwaarste redenen daarvan.
func level(rs []reason) Level {
	top := LevelNormal
	for _, r := range rs {
		if levelRank[r.level] > levelRank[top] {
			top = r.level
		}
	}
	if top == LevelNormal {
		return Level{Level: LevelNormal, Reasons: []string{"Geen open incidenten, aanvallen worden tegengehouden en elke node is beschermd."}}
	}
	var same []reason
	for _, r := range rs {
		if r.level == top {
			same = append(same, r)
		}
	}
	slices.SortStableFunc(same, func(a, b reason) int {
		switch {
		case a.prio > b.prio:
			return -1
		case a.prio < b.prio:
			return 1
		}
		return 0
	})
	out := Level{Level: top, Reasons: []string{}}
	for _, r := range same[:min(2, len(same))] {
		out.Reasons = append(out.Reasons, r.text)
	}
	if len(same) > 2 {
		out.Reasons = append(out.Reasons, fmt.Sprintf("en %d andere", len(same)-2))
	}
	return out
}

// Gewichten van de dekking per node.
const (
	wFirewall = 0.4
	wCrowdSec = 0.3
	wBouncer  = 0.2
	wOnline   = 0.1
)

// score rekent de vier delen uit. Elke aftrekpost zegt waarom en waar je
// het oplost.
func score(in Inputs, items []Item) Score {
	n := float64(len(in.Coverage))
	part := func(key, label string, max int) ScorePart {
		return ScorePart{Key: key, Label: label, Max: max, Points: float64(max), Deductions: []Deduction{}}
	}
	deduct := func(p *ScorePart, pts float64, text, href string) {
		if pts <= 0 {
			return
		}
		pts = math.Min(pts, p.Points)
		p.Points -= pts
		p.Deductions = append(p.Deductions, Deduction{Text: text, Points: round1(pts), Href: href})
	}

	cov := part("dekking", "Dekking", 35)
	pat := part("patches", "Patches", 25)
	vul := part("kwetsbaarheden", "Kwetsbaarheden", 10)
	if in.Vulns != nil {
		// Met de beveiligingsmodule telt Patches 15 en Kwetsbaarheden 10.
		pat = part("patches", "Patches", 15)
	}
	if n > 0 {
		per := float64(cov.Max) / n
		lost := map[string]float64{}
		count := map[string]int{}
		var order []string
		miss := func(credit, weight float64, label string) {
			if credit >= 1 {
				return
			}
			if _, ok := lost[label]; !ok {
				order = append(order, label)
			}
			lost[label] += per * weight * (1 - credit)
			count[label]++
		}
		updNodes, updPkgs := 0, 0
		for _, c := range in.Coverage {
			cr, label := firewallCredit(c.Firewall)
			miss(cr, wFirewall, label)
			cr, label = crowdSecCredit(c.CrowdSec)
			miss(cr, wCrowdSec, label)
			if c.CrowdSec == StateOn || c.CrowdSec == StateDown || c.CrowdSec == StateError || c.CrowdSec == StateUnknown {
				cr, label = bouncerCredit(c.Bouncer)
				miss(cr, wBouncer, label)
			} else {
				// Zonder (beheerde) CrowdSec is er ook geen bewaakte bouncer.
				miss(0, wBouncer, "zonder bewaakte bouncer")
			}
			if !c.Online {
				miss(0, wOnline, "met de agent offline")
			}
			if c.SecurityUpdates > 0 {
				updNodes++
				updPkgs += c.SecurityUpdates
			}
		}
		href := func(label string) string {
			switch {
			case strings.Contains(label, "agent offline"):
				return "/nodes"
			case strings.Contains(label, "firewall"), strings.Contains(label, "ufw"):
				return "/firewall?tab=nodes"
			}
			return "/crowdsec?tab=nodes"
		}
		for _, l := range order {
			deduct(&cov, lost[l], plural(count[l], "node", "nodes")+" "+l, href(l))
		}
		deduct(&pat, float64(pat.Max)*float64(updNodes)/n,
			fmt.Sprintf("%s met beveiligingsupdates (%s)", plural(updNodes, "node", "nodes"), plural(updPkgs, "pakket", "pakketten")), "/updates")
	}

	if v := in.Vulns; v != nil {
		deduct(&vul, 4*float64(len(v.CriticalNet)), plural(len(v.CriticalNet), "adres", "adressen")+" met een kritieke netwerkbevinding", "/kwetsbaarheden/scans?tab=bevindingen")
		deduct(&vul, 3*float64(len(v.KEVNodes)), plural(len(v.KEVNodes), "node", "nodes")+" met een actief misbruikte kwetsbaarheid", "/kwetsbaarheden")
		deduct(&vul, 0.5*float64(v.HighCVE+v.HighNet), plural(v.HighCVE+v.HighNet, "ernstige kwetsbaarheid", "ernstige kwetsbaarheden"), "/kwetsbaarheden")
		if v.HardeningAvg != nil && *v.HardeningAvg < 90 {
			deduct(&vul, float64(90-*v.HardeningAvg)/5, fmt.Sprintf("Gemiddelde verharding %d%%", *v.HardeningAvg), "/kwetsbaarheden/verharding")
		}
	}

	acc := part("toegang", "Toegang", 15)
	if k := len(in.AdminsWithoutTOTP); k > 0 {
		deduct(&acc, math.Min(10, 5*float64(k)), "Tweestap uit voor "+names(in.AdminsWithoutTOTP), "/instellingen#tweestap")
	}
	if in.SSHFromAny {
		deduct(&acc, 5, "SSH open voor iedereen", "/firewall?tab=instellingen")
	}

	thr := part("dreiging", "Dreiging", 25)
	for _, it := range items {
		var pts float64
		switch {
		case strings.HasPrefix(it.Key, "incident-") && it.Prio >= 10:
			pts = 10
		case strings.HasPrefix(it.Key, "incident-") && it.Prio >= 8:
			pts = 6
		case strings.HasPrefix(it.Key, "incident-"):
			pts = 2
		case strings.HasPrefix(it.Key, "bruteforce-"):
			pts = 10
		case it.Key == "bouncer-hit":
			pts = 8
		case strings.HasPrefix(it.Key, "spread-"):
			pts = 5
		case it.Key == "spike":
			pts = 5
		case it.Key == "logins-extern":
			pts = 3
		}
		deduct(&thr, pts, it.Title, it.Href)
	}

	parts := []ScorePart{cov, pat, acc, thr}
	if in.Vulns != nil {
		parts = []ScorePart{cov, pat, vul, acc, thr}
	}
	total := 0.0
	for i := range parts {
		parts[i].Points = round1(parts[i].Points)
		total += parts[i].Points
	}
	return Score{Score: int(math.Round(total)), Parts: parts, Trend: []TrendPoint{}}
}

// firewallCredit is hoeveel de firewall van een node meetelt, en anders
// waarom niet.
func firewallCredit(state string) (float64, string) {
	switch state {
	case StateOn:
		return 1, ""
	case StatePreview:
		return 0.5, "met de firewall alleen in voorbeeld"
	case StateUnmanaged:
		return 0.5, "met ufw buiten ClusterForge om"
	case StateUnknown:
		return 0.5, "waarvan de firewallstatus nog onbekend is"
	case StateOff:
		return 0, "met de firewall bewust uit"
	case StateDown:
		return 0, "waar ufw uit staat"
	case StateError:
		return 0, "waar de firewall niet toegepast is"
	case StateUnsupported:
		return 0, "met een agent te oud voor de firewall"
	}
	return 0, "zonder firewallbeheer"
}

func crowdSecCredit(state string) (float64, string) {
	switch state {
	case StateOn, StateUnmanaged:
		return 1, ""
	case StateUnknown:
		return 0.5, "waar CrowdSec nog geïnstalleerd wordt"
	case StateOff:
		return 0, "met CrowdSec bewust uit"
	case StateDown:
		return 0, "waar CrowdSec niet draait"
	case StateError:
		return 0, "waar CrowdSec niet toegepast is"
	case StateUnsupported:
		return 0, "met een agent te oud voor CrowdSec"
	}
	return 0, "zonder CrowdSec"
}

func bouncerCredit(state string) (float64, string) {
	switch state {
	case StateOn:
		return 1, ""
	case StateUnknown:
		return 0.5, "waarvan de bouncer nog onbekend is"
	case StateOff:
		return 0, "met CrowdSec zonder bouncer"
	}
	return 0, "waar de bouncer niet draait"
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

const namesShown = 4

// names geeft de eerste paar namen, met "en n andere" erachter.
func names(list []string) string {
	l := slices.Clone(list)
	slices.Sort(l)
	if len(l) <= namesShown {
		return strings.Join(l, ", ")
	}
	return fmt.Sprintf("%s en %d andere", strings.Join(l[:namesShown], ", "), len(l)-namesShown)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
