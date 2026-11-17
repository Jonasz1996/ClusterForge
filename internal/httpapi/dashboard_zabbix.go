package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/zabbixmon"
)

// dashboardZabbix is de stand van Zabbix (fase 33) voor een tegel op een eigen
// dashboard, zoals Homepage: platte velden. Met een token voor enkele
// clusters (of ?cluster=) telt alleen wat bij die clusters hoort; de
// gezondheid van Zabbix zelf (proxy, HA, wachtrij) staat er dan niet bij.
type dashboardZabbix struct {
	OK bool `json:"ok"`
	// Level: normaal, waarschuwing, probleem, kritiek of onbekend.
	Level      string `json:"level"`
	LevelLabel string `json:"level_label"`
	Reason     string `json:"reason"`
	Problems   int    `json:"problems"`
	Disaster   int    `json:"disaster"`
	High       int    `json:"high"`
	Average    int    `json:"average"`
	Warning    int    `json:"warning"`
	Info       int    `json:"info"`
	Unacked    int    `json:"unacknowledged"`
	Hosts      int    `json:"hosts"`
	HostsDown  int    `json:"hosts_down"`
	// Available: bereikbare hosts van het totaal, bv. "18/20".
	Available string `json:"available"`
	// Alleen voor een token voor alle clusters:
	Proxies        string   `json:"proxies,omitempty"`
	ProxiesOffline int      `json:"proxies_offline"`
	HAActive       string   `json:"ha_active,omitempty"`
	NVPS           *float64 `json:"nvps,omitempty"`
	Queue          *float64 `json:"queue,omitempty"`
	// Top: de vijf zwaarste open problemen.
	Top       []dashboardZabbixProblem `json:"top"`
	UpdatedAt *time.Time               `json:"updated_at"`
	URL       string                   `json:"url"`
}

type dashboardZabbixProblem struct {
	Name          string    `json:"name"`
	Severity      int       `json:"severity"`
	SeverityLabel string    `json:"severity_label"`
	Host          string    `json:"host"`
	Since         time.Time `json:"since"`
	Acknowledged  bool      `json:"acknowledged"`
}

var zabbixSeverityLabel = []string{"Niet ingedeeld", "Informatie", "Waarschuwing", "Gemiddeld", "Hoog", "Ramp"}

// dashboardZabbixHandler: GET /api/dashboard/v1/zabbix. ?cluster= (slug, id
// of naam) beperkt tot één cluster; ?strict=1 geeft 503 vanaf probleem.
func (s *Server) dashboardZabbixHandler(w http.ResponseWriter, r *http.Request) {
	if s.zabbixMon == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Zabbix-monitoring is niet beschikbaar")
		return
	}
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	var scope []uuid.UUID
	if key := strings.TrimSpace(r.URL.Query().Get("cluster")); key != "" {
		o, ok := s.dashboardScoped(w, r)
		if !ok {
			return
		}
		found := false
		for _, c := range o.Clusters {
			if strings.EqualFold(c.Slug, key) || c.Id.String() == strings.ToLower(key) || strings.EqualFold(c.Name, key) {
				scope, found = []uuid.UUID{c.Id}, true
				break
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden (of niet zichtbaar met dit token)")
			return
		}
	} else if len(t.ClusterIds) > 0 {
		scope = t.ClusterIds
	}
	o, err := s.zabbixMon.Overview(r.Context(), "24h")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !o.Configured {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "de Zabbix-API is nog niet ingesteld in ClusterForge")
		return
	}
	d := dashboardZabbixFrom(o, scope)
	d.URL = baseURL(r, s.cfg.TrustProxyHeaders) + "/zabbix/overzicht"
	code := http.StatusOK
	if r.URL.Query().Get("strict") == "1" && !d.OK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, d)
}

func dashboardZabbixFrom(o zabbixmon.Overview, scope []uuid.UUID) dashboardZabbix {
	d := dashboardZabbix{Top: []dashboardZabbixProblem{}, UpdatedAt: o.UpdatedAt}
	inScope := func(n *zabbixmon.NodeLink) bool {
		return len(scope) == 0 || (n != nil && n.ClusterID != nil && slices.Contains(scope, *n.ClusterID))
	}
	hostOK := map[string]bool{}
	ok := 0
	for _, h := range o.Hosts {
		if !inScope(h.Node) {
			continue
		}
		hostOK[h.HostID] = true
		if !h.Enabled {
			continue
		}
		d.Hosts++
		switch {
		case h.Available == zabbixmon.AvailDown && !h.Maintenance:
			d.HostsDown++
		case h.Available != zabbixmon.AvailDown:
			ok++
		}
	}
	d.Available = strconv.Itoa(ok) + "/" + strconv.Itoa(d.Hosts)

	var probs []zabbixmon.Problem
	for _, p := range o.Problems {
		if p.Suppressed {
			continue
		}
		if len(scope) > 0 && !slices.ContainsFunc(p.Hosts, func(h zabbixmon.HostRef) bool { return hostOK[h.HostID] }) {
			continue
		}
		probs = append(probs, p)
		d.Problems++
		if !p.Acknowledged {
			d.Unacked++
		}
		switch p.Severity {
		case 5:
			d.Disaster++
		case 4:
			d.High++
		case 3:
			d.Average++
		case 2:
			d.Warning++
		case 1:
			d.Info++
		}
	}
	slices.SortStableFunc(probs, func(a, b zabbixmon.Problem) int {
		if a.Severity != b.Severity {
			return b.Severity - a.Severity
		}
		return a.Since.Compare(b.Since)
	})
	for _, p := range probs[:min(len(probs), 5)] {
		x := dashboardZabbixProblem{Name: p.Name, Severity: p.Severity, Since: p.Since, Acknowledged: p.Acknowledged}
		if p.Severity >= 0 && p.Severity < len(zabbixSeverityLabel) {
			x.SeverityLabel = zabbixSeverityLabel[p.Severity]
		}
		if len(p.Hosts) > 0 {
			x.Host = p.Hosts[0].Name
			if p.Hosts[0].Node != nil {
				x.Host = p.Hosts[0].Node.Hostname
			}
		}
		d.Top = append(d.Top, x)
	}

	if len(scope) == 0 {
		// Alles: de toestand zoals op de overzichtspagina, met Zabbix zelf.
		d.Level = o.State.Level
		if len(o.State.Reasons) > 0 {
			d.Reason = o.State.Reasons[0]
		}
		online := 0
		for _, p := range o.Proxies {
			switch p.State {
			case "offline":
				d.ProxiesOffline++
			case "online":
				online++
			}
		}
		if len(o.Proxies) > 0 {
			d.Proxies = strconv.Itoa(online) + "/" + strconv.Itoa(len(o.Proxies)) + " online"
		}
		for _, n := range o.HA {
			if n.Status == "active" {
				d.HAActive = n.Name
			}
		}
		if len(o.Health) > 0 {
			d.NVPS, d.Queue = o.Health[0].NVPS, o.Health[0].Queue
		}
	} else {
		// Een deel: dezelfde regels, maar alleen voor die hosts.
		switch {
		case o.UpdatedAt == nil:
			d.Level, d.Reason = zabbixmon.LevelUnknown, "nog geen gegevens van Zabbix"
		case d.Disaster > 0:
			d.Level, d.Reason = zabbixmon.LevelCritical, plural(d.Disaster, "ramp open", "rampen open")
		case d.High > 0:
			d.Level, d.Reason = zabbixmon.LevelProblem, plural(d.High, "probleem met ernst hoog", "problemen met ernst hoog")
		case d.HostsDown > 0:
			d.Level, d.Reason = zabbixmon.LevelProblem, plural(d.HostsDown, "host onbereikbaar", "hosts onbereikbaar")
		case d.Average > 0:
			d.Level, d.Reason = zabbixmon.LevelWarning, plural(d.Average, "probleem met gemiddelde ernst", "problemen met gemiddelde ernst")
		default:
			d.Level, d.Reason = zabbixmon.LevelNormal, "geen open problemen van belang; alle hosts bereikbaar"
		}
	}
	d.OK = d.Level == zabbixmon.LevelNormal || d.Level == zabbixmon.LevelWarning
	if d.Level != "" {
		d.LevelLabel = strings.ToUpper(d.Level[:1]) + d.Level[1:]
	}
	return d
}
