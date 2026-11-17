package aiverslag

// Het overzicht (fase 38 M1): de SOC-band van AI-verslag (dreigingsniveau,
// score, NGFW-tellers, nu belangrijk, incidenten, bronnen), de laatste
// alarmen, de bevindingen per categorie en de staat van de diensten. Elk
// deel wordt apart gelezen; wat niet lukt, komt in Errors.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Overview is wat de pagina AI-verslag toont. Start en App zijn de
// antwoorden van AI-verslag zoals ze zijn; web/src/lib/aiverslag.ts typt ze.
type Overview struct {
	Instance Instance          `json:"instance"`
	Start    json.RawMessage   `json:"start,omitempty"`
	App      json.RawMessage   `json:"app,omitempty"`
	RunInfo  json.RawMessage   `json:"runinfo,omitempty"`
	State    *StateSummary     `json:"state,omitempty"`
	Warnings []Warning         `json:"warnings"`
	Errors   map[string]string `json:"errors"`
}

// Warning is iets dat aandacht vraagt.
type Warning struct {
	Level string `json:"level"` // critical, warning
	Text  string `json:"text"`
}

// StateSummary vat /api/state samen: tellers per categorie en de
// belangrijkste open bevindingen.
type StateSummary struct {
	Running bool         `json:"running"`
	Last    string       `json:"last,omitempty"`
	Summary string       `json:"summary,omitempty"`
	Busy    []string     `json:"busy"`
	Cats    []CatSummary `json:"cats"`
	Top     []Item       `json:"top"`
}

type CatSummary struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Hoog   int    `json:"hoog"`
	Midden int    `json:"midden"`
	Laag   int    `json:"laag"`
	Actie  int    `json:"actie"`
	Err    string `json:"err,omitempty"`
	Model  string `json:"model,omitempty"`
}

// Item is één bevinding zoals /api/state hem geeft.
type Item struct {
	Cat        string `json:"cat"`
	ID         int64  `json:"id"`
	Sev        string `json:"sev"`
	Titel      string `json:"titel"`
	Uitleg     string `json:"uitleg"`
	ActieNodig bool   `json:"actie_nodig"`
	Actie      string `json:"actie"`
	Fix        bool   `json:"fix"`
	Status     string `json:"status"`
	Ond        string `json:"ond,omitempty"`
	Events     int64  `json:"ev"`
	First      any    `json:"first,omitempty"`
	Last       any    `json:"last,omitempty"`
	Count      int64  `json:"count"`
	Vervalt    any    `json:"vervalt,omitempty"`
}

type rawState struct {
	Running bool     `json:"running"`
	Last    any      `json:"last"`
	Sam     string   `json:"sam"`
	Busy    []string `json:"busy"`
	Cats    []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Model string `json:"model"`
		Err   string `json:"err"`
		Items []Item `json:"items"`
	} `json:"cats"`
}

var sevRank = map[string]int{"hoog": 3, "midden": 2, "laag": 1}

// Summarize maakt van /api/state de samenvatting.
func Summarize(raw json.RawMessage, top int) (*StateSummary, error) {
	var st rawState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("onverwacht antwoord van /api/state: %w", err)
	}
	out := &StateSummary{Running: st.Running, Summary: st.Sam, Busy: st.Busy, Cats: []CatSummary{}, Top: []Item{}}
	if out.Busy == nil {
		out.Busy = []string{}
	}
	if st.Last != nil {
		out.Last = fmt.Sprint(st.Last)
	}
	var all []Item
	for _, c := range st.Cats {
		cs := CatSummary{ID: c.ID, Label: c.Label, Err: c.Err, Model: c.Model}
		for _, it := range c.Items {
			switch it.Sev {
			case "hoog":
				cs.Hoog++
			case "midden":
				cs.Midden++
			default:
				cs.Laag++
			}
			if it.ActieNodig {
				cs.Actie++
			}
			it.Cat = c.ID
			if strings.HasPrefix(it.Status, "goedgekeurd") {
				continue
			}
			all = append(all, it)
		}
		out.Cats = append(out.Cats, cs)
	}
	// Belangrijkste eerst: ernst, dan actie nodig (AI-verslag sorteert per
	// categorie al op laatst gezien).
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && less(all[j], all[j-1]); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	for _, it := range all {
		if len(out.Top) >= top || sevRank[it.Sev] < 2 {
			break
		}
		it.Ond = clip(it.Ond, 400)
		it.Uitleg = clip(it.Uitleg, 400)
		out.Top = append(out.Top, it)
	}
	return out, nil
}

func less(a, b Item) bool {
	if sevRank[a.Sev] != sevRank[b.Sev] {
		return sevRank[a.Sev] > sevRank[b.Sev]
	}
	return a.ActieNodig && !b.ActieNodig
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var overviewParts = map[string]string{
	"start":   "/api/inzicht/start",
	"app":     "/api/app/start",
	"state":   "/api/state",
	"runinfo": "/api/runinfo",
}

// Overview leest alle delen tegelijk.
func (s *Service) Overview(ctx context.Context, id uuid.UUID) (Overview, error) {
	v, err := s.Instance(ctx, id)
	if err != nil {
		return Overview{}, err
	}
	o := Overview{Instance: v, Errors: map[string]string{}, Warnings: []Warning{}}
	if v.Why != "" {
		o.Warnings = Warnings(v, nil, nil, nil)
		return o, ValidationError{v.Why}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, path := range overviewParts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := s.get(ctx, id, path, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				o.Errors[name] = err.Error()
				return
			}
			switch name {
			case "start":
				o.Start = data
			case "app":
				o.App = data
			case "runinfo":
				o.RunInfo = data
			case "state":
				st, err := Summarize(data, 12)
				if err != nil {
					o.Errors[name] = err.Error()
					return
				}
				o.State = st
			}
		}()
	}
	wg.Wait()
	o.Warnings = Warnings(v, o.Start, o.App, o.State)
	return o, nil
}

type startInfo struct {
	Niveau *struct {
		Niveau  string   `json:"niveau"`
		Redenen []string `json:"redenen"`
	} `json:"niveau"`
	Feeds *struct {
		Fout int `json:"fout"`
	} `json:"feeds"`
	Firewall *struct {
		Updates   any `json:"updates"`
		Kwetsbaar int `json:"kwetsbaar"`
	} `json:"firewall"`
}

type appInfo struct {
	Meldingen []struct {
		ID     int64  `json:"id"`
		Titel  string `json:"titel"`
		Gezien bool   `json:"gezien"`
		TS     int64  `json:"ts"`
	} `json:"meldingen"`
}

// Warnings haalt uit de facts en de delen wat aandacht vraagt.
func Warnings(v Instance, start, app json.RawMessage, st *StateSummary) []Warning {
	out := []Warning{}
	for _, u := range v.Units {
		switch {
		case u.Active == "failed":
			out = append(out, Warning{"critical", fmt.Sprintf("%s is mislukt (%s)", u.Name, firstNon(u.Result, u.Sub))})
		case u.Name == "aiverslag-web.service" && u.Active != "active":
			out = append(out, Warning{"critical", "de webdienst van AI-verslag draait niet"})
		case u.Name == "ollama.service" && u.Active != "active":
			out = append(out, Warning{"warning", "Ollama draait niet: AI-verslag kan geen AI-analyses maken"})
		case strings.HasSuffix(u.Name, ".timer") && u.Active != "active":
			out = append(out, Warning{"warning", fmt.Sprintf("%s staat niet aan", u.Name)})
		}
	}
	if v.Online && !v.Facts.Reachable && v.Protocol >= protocol.AIVSince {
		out = append(out, Warning{"critical", "de webdienst van AI-verslag antwoordt niet op " + v.Facts.URL})
	}
	switch {
	case v.Facts.DataUsedPct >= 95:
		out = append(out, Warning{"critical", fmt.Sprintf("schijf van AI-verslag %.0f%% vol", v.Facts.DataUsedPct)})
	case v.Facts.DataUsedPct >= 90:
		out = append(out, Warning{"warning", fmt.Sprintf("schijf van AI-verslag %.0f%% vol", v.Facts.DataUsedPct)})
	}
	var si startInfo
	if len(start) > 0 && json.Unmarshal(start, &si) == nil {
		if si.Niveau != nil {
			reden := ""
			if len(si.Niveau.Redenen) > 0 {
				reden = ": " + strings.Join(si.Niveau.Redenen, "; ")
			}
			switch si.Niveau.Niveau {
			case "kritiek":
				out = append(out, Warning{"critical", "dreigingsniveau kritiek" + reden})
			case "verhoogd":
				out = append(out, Warning{"warning", "dreigingsniveau verhoogd" + reden})
			}
		}
		if si.Feeds != nil && si.Feeds.Fout > 0 {
			out = append(out, Warning{"warning", fmt.Sprintf("%d dreigingsfeed(s) werken niet", si.Feeds.Fout)})
		}
	}
	var ai appInfo
	if len(app) > 0 && json.Unmarshal(app, &ai) == nil {
		n, titel := 0, ""
		for _, m := range ai.Meldingen {
			if !m.Gezien && time.Since(time.Unix(m.TS, 0)) < 7*24*time.Hour {
				if n == 0 {
					titel = m.Titel
				}
				n++
			}
		}
		if n == 1 {
			out = append(out, Warning{"warning", "alarm niet gezien: " + titel})
		} else if n > 1 {
			out = append(out, Warning{"warning", fmt.Sprintf("%d alarmen niet gezien (laatste: %s)", n, titel)})
		}
	}
	if st != nil {
		for _, c := range st.Cats {
			if c.Err != "" {
				out = append(out, Warning{"warning", fmt.Sprintf("analyse %s: %s", strings.ToLower(c.Label), clip(c.Err, 140))})
			}
		}
	}
	return out
}

func firstNon(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

type levelInfo struct {
	Niveau *struct {
		Niveau  string   `json:"niveau"`
		Redenen []string `json:"redenen"`
	} `json:"niveau"`
	Score *struct {
		Score *float64 `json:"score"`
	} `json:"score"`
}

// fillLevel neemt niveau, score en open hoog over in een samenvatting.
func fillLevel(sum *Summary, o Overview) {
	var li levelInfo
	if len(o.Start) > 0 && json.Unmarshal(o.Start, &li) == nil {
		if li.Niveau != nil {
			sum.Niveau, sum.Redenen = li.Niveau.Niveau, li.Niveau.Redenen
		}
		if li.Score != nil {
			sum.Score = li.Score.Score
		}
	}
	if o.State != nil {
		for _, c := range o.State.Cats {
			sum.OpenHoog += c.Hoog
		}
	}
}
