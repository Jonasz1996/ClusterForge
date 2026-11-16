package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/soc"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// dashboardSoc is het beveiligingsoverzicht (fase 25) voor een tegel op een
// eigen dashboard: platte velden, net als per cluster.
type dashboardSoc struct {
	OK bool `json:"ok"`
	// Level is normaal, waakzaam, verhoogd of kritiek; LevelLabel met
	// hoofdletter, Reason de eerste reden.
	Level         string `json:"level"`
	LevelLabel    string `json:"level_label"`
	Reason        string `json:"reason"`
	Score         int    `json:"score"`
	Attacks       int    `json:"attacks_24h"`
	ActiveBans    int    `json:"active_bans"`
	Blocked       int64  `json:"blocked_24h"`
	LoginFailures int    `json:"login_failures_24h"`
	Protected     string `json:"protected"`
	Unprotected   int    `json:"nodes_unprotected"`
	Attention     int    `json:"attention"`
	URL           string `json:"url"`
}

// dashboardSocHandler: GET /api/dashboard/v1/soc. Alleen voor een token voor
// alle clusters: het overzicht gaat over alles. ?strict=1 geeft 503 vanaf
// verhoogd, zodat de check van de tegel rood wordt.
func (s *Server) dashboardSocHandler(w http.ResponseWriter, r *http.Request) {
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	if len(t.ClusterIds) > 0 {
		writeError(w, http.StatusForbidden, "forbidden", "het beveiligingsoverzicht gaat over alle clusters; maak er een token voor alle clusters voor")
		return
	}
	if s.soc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "het beveiligingsoverzicht is niet beschikbaar")
		return
	}
	o, err := s.soc.Overview(r.Context(), soc.Window24h)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d := dashboardSoc{
		Level: o.Level.Level, Score: o.Score.Score, Attacks: o.Stats.Attacks, ActiveBans: o.Stats.ActiveBans,
		Blocked: o.Stats.Blocked, LoginFailures: o.Stats.LoginFailures,
		Protected: fmt.Sprintf("%d/%d", o.Stats.Protected, o.Stats.Nodes), Unprotected: o.Stats.Nodes - o.Stats.Protected,
		URL: baseURL(r, s.cfg.TrustProxyHeaders) + "/soc",
	}
	d.OK = d.Level == soc.LevelNormal || d.Level == soc.LevelWatch
	d.LevelLabel = strings.ToUpper(d.Level[:1]) + d.Level[1:]
	if len(o.Level.Reasons) > 0 {
		d.Reason = o.Level.Reasons[0]
	}
	for _, it := range o.Now {
		if it.Tone != soc.ToneInfo {
			d.Attention++
		}
	}
	code := http.StatusOK
	if r.URL.Query().Get("strict") == "1" && !d.OK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, d)
}
