package zabbixmon

import (
	"time"
)

// --- Aandacht nodig (M5) -------------------------------------------------------------

// UnackedAfter: zo lang mag een ramp of hoog probleem open staan zonder dat
// iemand het bevestigt, voor het in "Aandacht nodig" komt.
const UnackedAfter = 15 * time.Minute

// Notice is een regel voor "Aandacht nodig".
type Notice struct {
	ID     string
	Red    bool
	Title  string
	Detail string
	Names  []string
	Href   string
	Count  int
}

// Notices geeft wat in Zabbix nu een actie vraagt. Zonder instellingen of
// voor de eerste ronde: niets.
func (s *Service) Notices() []Notice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.configured {
		return nil
	}
	return notices(s.snap, s.lastErr, s.lastOK, s.Now())
}

func notices(snap *Snapshot, lastErr string, lastOK, now time.Time) []Notice {
	var out []Notice
	// De API faalt al een tijdje: de rest is dan oud of leeg.
	if lastErr != "" && (lastOK.IsZero() || now.Sub(lastOK) > Stale) {
		d := lastErr
		if !lastOK.IsZero() {
			d = "sinds " + lastOK.In(brussels()).Format("15:04") + ": " + lastErr
		}
		out = append(out, Notice{ID: "zabbix-api", Red: true, Title: "De Zabbix-API antwoordt niet", Detail: d, Href: "/zabbix/overzicht", Count: 1})
	}
	if snap == nil {
		return out
	}
	if len(snap.HA) > 0 {
		active := false
		for _, n := range snap.HA {
			active = active || n.Status == "active"
		}
		if !active {
			out = append(out, Notice{ID: "zabbix-ha", Red: true, Title: "De Zabbix-server heeft geen actieve HA-node", Href: "/zabbix/overzicht", Count: 1})
		}
	}
	var offline []string
	for _, p := range snap.Proxies {
		if p.State == "offline" {
			offline = append(offline, p.Name)
		}
	}
	out = append(out, Notice{ID: "zabbix-proxy", Red: true, Title: plural(len(offline), "Zabbix-proxy is", "Zabbix-proxy's zijn") + " offline", Names: offline, Href: "/zabbix/overzicht", Count: len(offline)})

	var unacked []string
	seen := map[string]bool{}
	n := 0
	for _, p := range snap.Problems {
		if p.Severity < 4 || p.Acknowledged || p.Suppressed || now.Sub(p.Since) < UnackedAfter {
			continue
		}
		n++
		for _, h := range p.Hosts {
			if !seen[h.Name] {
				seen[h.Name] = true
				unacked = append(unacked, h.Name)
			}
		}
		if len(p.Hosts) == 0 {
			unacked = append(unacked, p.Name)
		}
	}
	out = append(out, Notice{ID: "zabbix-unacked", Red: true, Title: plural(n, "Zabbix-probleem (ramp of hoog) staat", "Zabbix-problemen (ramp of hoog) staan") + " langer dan 15 min open zonder bevestiging", Names: unacked, Href: "/zabbix/problemen", Count: n})

	var down []string
	for _, h := range snap.Hosts {
		if h.Node != nil && h.Enabled && !h.Maintenance && h.Available == AvailDown {
			down = append(down, h.Node.Hostname)
		}
	}
	out = append(out, Notice{ID: "zabbix-nodes-down", Title: plural(len(down), "node is", "nodes zijn") + " onbereikbaar voor Zabbix", Names: down, Href: "/zabbix/hosts", Count: len(down)})

	var unwatched []string
	for _, u := range snap.Unwatched {
		unwatched = append(unwatched, u.Hostname)
	}
	out = append(out, Notice{ID: "zabbix-unwatched", Title: plural(len(unwatched), "node wordt", "nodes worden") + " niet door Zabbix bewaakt", Names: unwatched, Href: "/zabbix", Count: len(unwatched)})

	// Regels zonder aantal vallen weg.
	keep := out[:0]
	for _, x := range out {
		if x.Count > 0 {
			keep = append(keep, x)
		}
	}
	return keep
}
