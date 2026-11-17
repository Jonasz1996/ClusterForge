package aiverslag

// Welke paden van AI-verslag ClusterForge gebruikt (fase 38). De agent
// geeft elk goed gevormd pad door; hier staat wat de GUI mag lezen en
// welke schrijfacties er zijn, met hun titel, of ze een overgetikte
// bevestiging vragen en of ze meteen (zonder taak) lopen.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	catName = `[a-z][a-z0-9_]{0,23}`
	itemID  = `[0-9]{1,9}`
	chatID  = `[0-9a-f]{6,32}`
)

// readExact zijn paden zonder variabelen, met de parameters die mogen.
var readExact = map[string][]string{
	"/api/state":                nil,
	"/api/runinfo":              nil,
	"/api/app/start":            nil,
	"/api/app/alarm":            {"id"},
	"/api/inzicht/start":        nil,
	"/api/inzicht/dreiging":     {"dagen"},
	"/api/inzicht/opzoeken":     {"q"},
	"/api/crowdsec/overzicht":   nil,
	"/api/triage":               nil,
	"/api/focus":                nil,
	"/api/geheugen":             nil,
	"/api/tijdlijn":             {"uren", "dagen", "ip"},
	"/api/tijdlijn/zoek":        {"q"},
	"/api/week":                 nil,
	"/api/apparaten":            nil,
	"/api/acties/recent":        nil,
	"/api/backup":               nil,
	"/api/dreiging":             nil,
	"/api/normaal":              nil,
	"/api/quarantaine":          nil,
	"/api/cti":                  nil,
	"/api/inzicht/clusterforge": nil,
}

// readPattern zijn paden met een categorie, item of chat.
var readPattern = []*regexp.Regexp{
	regexp.MustCompile(`^/api/fix/` + catName + `/` + itemID + `$`),
	regexp.MustCompile(`^/api/normaal/opties/` + catName + `/` + itemID + `$`),
	regexp.MustCompile(`^/api/chat/` + chatID + `$`),
}

func checkRead(path string, params map[string]string) error {
	if !protocol.ValidAIVPath(path) {
		return fmt.Errorf("ongeldig pad %q", path)
	}
	allowed, ok := readExact[path]
	if !ok {
		for _, re := range readPattern {
			if re.MatchString(path) {
				ok = true
				break
			}
		}
		if ok && len(params) > 0 {
			return fmt.Errorf("%s neemt geen parameters", path)
		}
	}
	if !ok {
		return fmt.Errorf("ClusterForge leest %s niet uit AI-verslag", path)
	}
	for k := range params {
		found := false
		for _, a := range allowed {
			found = found || a == k
		}
		if !found {
			return fmt.Errorf("parameter %q mag niet bij %s", k, path)
		}
	}
	return nil
}

// Rule beschrijft een schrijfactie.
type Rule struct {
	Title string `json:"title"`
	// Confirm is wat Jonas moet overtikken; leeg als het niet hoeft.
	Confirm string `json:"confirm,omitempty"`
	// Firewall: de actie verandert iets op de firewall (een fix); dat
	// vraagt tweestapsverificatie bij de gebruiker.
	Firewall bool `json:"firewall,omitempty"`
	// Direct: kort, loopt meteen en geeft het antwoord terug (geen taak).
	Direct bool `json:"direct,omitempty"`
	// LogBody: de inhoud komt in het logboek (een vraag, geen geheim).
	LogBody bool `json:"-"`
	// body controleert de inhoud en geeft de inhoud die doorgaat.
	body func(json.RawMessage) (json.RawMessage, error)
}

func noBody(b json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(b))) > 0 && strings.TrimSpace(string(b)) != "{}" && strings.TrimSpace(string(b)) != "null" {
		return nil, fmt.Errorf("deze actie neemt geen inhoud")
	}
	return nil, nil
}

type chatMsg struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

func chatBody(b json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Question string    `json:"question"`
		History  []chatMsg `json:"history"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("vraag ontbreekt")
	}
	in.Question = strings.TrimSpace(in.Question)
	if in.Question == "" || len([]rune(in.Question)) > 800 {
		return nil, fmt.Errorf("een vraag van 1 tot 800 tekens")
	}
	hist := []chatMsg{}
	for _, m := range in.History {
		if m.Role != "u" && m.Role != "a" {
			continue
		}
		if r := []rune(m.Text); len(r) > 600 {
			m.Text = string(r[:600])
		}
		hist = append(hist, m)
	}
	if len(hist) > 6 {
		hist = hist[len(hist)-6:]
	}
	return json.Marshal(map[string]any{"question": in.Question, "history": hist})
}

func seenBody(b json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Gezien int64 `json:"gezien"`
	}
	if err := json.Unmarshal(b, &in); err != nil || in.Gezien <= 0 {
		return nil, fmt.Errorf("welk alarm (gezien)")
	}
	return json.Marshal(map[string]int64{"gezien": in.Gezien})
}

type patternRule struct {
	re   *regexp.Regexp
	rule Rule
}

var writeExact = map[string]Rule{
	"/api/run":                       {Title: "Analyse van AI-verslag nu starten", body: noBody},
	"/api/dagrapport/nu":             {Title: "Dagrapport van AI-verslag nu maken", body: noBody},
	"/api/inzicht/dreiging/sync":     {Title: "Dreigingsfeeds van AI-verslag bijwerken", body: noBody},
	"/api/inzicht/dreiging/briefing": {Title: "Dreigingsbriefing van AI-verslag maken", body: noBody},
	"/api/chat":                      {Title: "Vraag aan AI-verslag", Direct: true, LogBody: true, body: chatBody},
	"/api/app/actie":                 {Title: "Alarm van AI-verslag als gezien markeren", Direct: true, LogBody: true, body: seenBody},
}

var writePattern = []patternRule{
	{regexp.MustCompile(`^/api/investigate/` + catName + `/` + itemID + `$`), Rule{Title: "Bevinding van AI-verslag laten onderzoeken", body: noBody}},
	{regexp.MustCompile(`^/api/approve/` + catName + `/` + itemID + `$`), Rule{Title: "Bevinding van AI-verslag goedkeuren (is normaal)", body: noBody}},
	{regexp.MustCompile(`^/api/delete/` + catName + `/` + itemID + `$`), Rule{Title: "Bevinding van AI-verslag verwijderen", body: noBody}},
	{regexp.MustCompile(`^/api/fix/` + catName + `/` + itemID + `/run$`), Rule{Title: "Fix van AI-verslag uitvoeren op de firewall", Confirm: "fix", Firewall: true, body: noBody}},
}

// LookupRule geeft de regel van een schrijfpad.
func LookupRule(path string) (Rule, bool) {
	if r, ok := writeExact[path]; ok {
		return r, true
	}
	for _, p := range writePattern {
		if p.re.MatchString(path) {
			return p.rule, true
		}
	}
	return Rule{}, false
}

func checkCall(path string, body json.RawMessage) (Rule, protocol.AIVCallSpec, error) {
	if !protocol.ValidAIVPath(path) {
		return Rule{}, protocol.AIVCallSpec{}, fmt.Errorf("ongeldig pad %q", path)
	}
	rule, ok := LookupRule(path)
	if !ok {
		return Rule{}, protocol.AIVCallSpec{}, fmt.Errorf("ClusterForge doet %s niet in AI-verslag", path)
	}
	b, err := rule.body(body)
	if err != nil {
		return Rule{}, protocol.AIVCallSpec{}, err
	}
	spec := protocol.AIVCallSpec{Method: "POST", Path: path, Body: b}
	if err := spec.Validate(); err != nil {
		return Rule{}, protocol.AIVCallSpec{}, err
	}
	return rule, spec, nil
}
