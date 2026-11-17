package httpapi

import (
	"net/http"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/aiverslag"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// dashboardAIV is de stand van AI-verslag (fase 38 M3) voor een tegel op een
// eigen dashboard, zoals Homepage: het dreigingsniveau, de netwerkscore en
// wat er open staat. Uit de laatste achtergrondronde (elke 5 minuten).
type dashboardAIV struct {
	OK bool `json:"ok"`
	// Level: normaal, waakzaam, verhoogd, kritiek of onbekend.
	Level      string   `json:"level"`
	LevelLabel string   `json:"level_label"`
	Reasons    []string `json:"reasons"`
	Reason     string   `json:"reason"`
	// Score is de netwerkscore (0-100) of null.
	Score     *float64 `json:"score"`
	Incidents int      `json:"incidents"`
	OpenHigh  int      `json:"open_high"`
	Unseen    int      `json:"unseen_alarms"`
	Warnings  int      `json:"warnings"`
	LastRun   int64    `json:"last_run,omitempty"`
	Machine   string   `json:"machine"`
	URL       string   `json:"url"`
}

var aivLevelLabel = map[string]string{
	"normaal": "Normaal", "waakzaam": "Waakzaam", "verhoogd": "Verhoogd", "kritiek": "Kritiek", "onbekend": "Geen AI-verslag",
}

// dashboardAIVHandler: GET /api/dashboard/v1/aiverslag (ook
// /api/dashboard/v2/sources/aiverslag); ?strict=1 geeft 503 bij verhoogd,
// kritiek of een machine die niet te lezen is.
func (s *Server) dashboardAIVHandler(w http.ResponseWriter, r *http.Request) {
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	if len(t.ClusterIds) > 0 {
		writeError(w, http.StatusForbidden, "forbidden", "AI-verslag gaat over het hele netwerk; maak er een token voor alle clusters voor")
		return
	}
	if s.aiverslag == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "AI-verslag is niet beschikbaar")
		return
	}
	d := dashboardAIVFrom(s.aiverslag.Cached())
	d.URL = baseURL(r, s.cfg.TrustProxyHeaders) + "/aiverslag"
	writeJSON(w, strictCode(r, d.OK), d)
}

func dashboardAIVFrom(sums []aiverslag.Summary) dashboardAIV {
	d := dashboardAIV{Level: "onbekend", Reasons: []string{}}
	rank := map[string]int{"normaal": 1, "waakzaam": 2, "verhoogd": 3, "kritiek": 4}
	var errs []string
	for _, sum := range sums {
		if d.Machine == "" {
			d.Machine = sum.Instance.Hostname
		}
		if sum.Error != "" && sum.Niveau == "" {
			errs = append(errs, sum.Instance.Hostname+": "+sum.Error)
		}
		if rank[sum.Niveau] > rank[d.Level] {
			d.Level, d.Reasons = sum.Niveau, nonNil(sum.Redenen)
		}
		if sum.Score != nil && d.Score == nil {
			sc := *sum.Score
			d.Score = &sc
		}
		d.Incidents += sum.Incidenten
		d.OpenHigh += sum.OpenHoog
		d.Unseen += sum.Unseen
		d.Warnings += len(sum.Warnings)
		if sum.LastRun > d.LastRun {
			d.LastRun = sum.LastRun
		}
	}
	d.LevelLabel = aivLevelLabel[d.Level]
	if d.LevelLabel == "" {
		d.LevelLabel = d.Level
	}
	d.OK = len(errs) == 0 && rank[d.Level] > 0 && rank[d.Level] <= 2
	switch {
	case len(errs) > 0:
		d.Reason = strings.Join(errs, "; ")
	case len(d.Reasons) > 0:
		d.Reason = strings.Join(d.Reasons, "; ")
	case len(sums) == 0:
		d.Reason = "geen machine met AI-verslag"
	}
	return d
}
