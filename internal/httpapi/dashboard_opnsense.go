package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/opnsense"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// dashboardOpnsense is de stand van de OPNsense-firewalls (fase 34) voor een
// tegel op een eigen dashboard, zoals Homepage: platte velden over alle
// firewallclusters samen, en per cluster een rij.
type dashboardOpnsense struct {
	OK bool `json:"ok"`
	// Level: normaal, waarschuwing, kritiek of onbekend.
	Level      string `json:"level"`
	LevelLabel string `json:"level_label"`
	Reason     string `json:"reason"`
	Clusters   int    `json:"clusters"`
	Firewalls  int    `json:"firewalls"`
	// Online: bereikbare firewalls van het totaal, bv. "2/2".
	Online string `json:"online"`
	// Master is de MASTER van het eerste cluster (of "geen").
	Master      string                     `json:"master"`
	VIPs        int                        `json:"vips"`
	Problems    int                        `json:"problems"`
	Maintenance int                        `json:"maintenance"`
	Version     string                     `json:"version"`
	Rows        []dashboardOpnsenseCluster `json:"rows"`
	URL         string                     `json:"url"`
}

type dashboardOpnsenseCluster struct {
	Name     string `json:"name"`
	Level    string `json:"level"`
	Master   string `json:"master"`
	HA       bool   `json:"ha"`
	Problems int    `json:"problems"`
	Reason   string `json:"reason,omitempty"`
}

var opnLevelLabel = map[string]string{"normaal": "Gezond", "waarschuwing": "Let op", "kritiek": "Aandacht nodig", "onbekend": "Geen firewalls"}

// dashboardOpnsenseHandler: GET /api/dashboard/v1/opnsense. Een token voor
// enkele clusters ziet alleen de firewalls in die clusters; ?strict=1 geeft
// 503 als het niet normaal is.
func (s *Server) dashboardOpnsenseHandler(w http.ResponseWriter, r *http.Request) {
	if s.opnsense == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "OPNsense-beheer is niet beschikbaar")
		return
	}
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	cs, err := s.opnsense.Clusters(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d := dashboardOpnsenseFrom(cs, t.ClusterIds)
	d.URL = baseURL(r, s.cfg.TrustProxyHeaders) + "/opnsense"
	code := http.StatusOK
	if r.URL.Query().Get("strict") == "1" && !d.OK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, d)
}

func dashboardOpnsenseFrom(cs []opnsense.Cluster, scope []uuid.UUID) dashboardOpnsense {
	d := dashboardOpnsense{Rows: []dashboardOpnsenseCluster{}, Master: "geen", Level: "normaal"}
	online := 0
	var reasons []string
	for _, c := range cs {
		if len(scope) > 0 && (c.ClusterID == nil || !slices.Contains(scope, *c.ClusterID)) {
			continue
		}
		row := dashboardOpnsenseCluster{Name: c.Name, HA: c.HA, Problems: len(c.Problems), Master: "geen"}
		switch c.Level {
		case "crit":
			row.Level = "kritiek"
		case "warn":
			row.Level = "waarschuwing"
		default:
			row.Level = "normaal"
		}
		if len(c.Problems) > 0 {
			row.Reason = c.Problems[0].Text
			reasons = append(reasons, c.Name+": "+c.Problems[0].Text)
		}
		for _, n := range c.Nodes {
			d.Firewalls++
			if n.Online {
				online++
			}
			if c.Master != nil && n.NodeID == *c.Master {
				row.Master = n.Hostname
			}
			if n.Facts != nil {
				if n.Facts.CARP.Maintenance {
					d.Maintenance++
				}
				if d.Version == "" {
					d.Version = n.Facts.Version
				}
			}
		}
		if len(c.Nodes) == 1 {
			row.Master = c.Nodes[0].Hostname
		}
		if d.Clusters == 0 {
			d.Master = row.Master
		}
		d.Clusters++
		d.VIPs += len(c.VIPs)
		d.Problems += row.Problems
		if rank(row.Level) > rank(d.Level) {
			d.Level = row.Level
		}
		d.Rows = append(d.Rows, row)
	}
	if d.Clusters == 0 {
		d.Level = "onbekend"
	}
	d.Online = strconv.Itoa(online) + "/" + strconv.Itoa(d.Firewalls)
	d.OK = d.Level == "normaal"
	d.LevelLabel = opnLevelLabel[d.Level]
	if len(reasons) > 0 {
		d.Reason = strings.Join(reasons, " · ")
	} else if d.Clusters > 0 {
		d.Reason = "alle firewalls gezond"
	}
	return d
}

func rank(level string) int {
	return map[string]int{"normaal": 0, "onbekend": 1, "waarschuwing": 2, "kritiek": 3}[level]
}
