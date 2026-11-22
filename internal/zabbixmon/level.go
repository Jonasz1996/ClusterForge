package zabbixmon

import (
	"fmt"
	"time"
)

// Niveaus van de band bovenaan, van erg naar rustig.
const (
	LevelCritical = "kritiek"
	LevelProblem  = "probleem"
	LevelWarning  = "waarschuwing"
	LevelNormal   = "normaal"
	LevelUnknown  = "onbekend"
)

// State is de toestand in één woord, altijd met de reden.
type State struct {
	Level   string   `json:"level"`
	Reasons []string `json:"reasons"`
}

// Stale: zo lang mag de laatste geslaagde ronde oud zijn.
const Stale = 5 * time.Minute

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Evaluate bepaalt de toestand. Vaste regels, zodat de reden altijd klopt:
//   - kritiek: een open ramp; de Zabbix-server heeft geen actieve HA-node;
//   - probleem: een open probleem met ernst hoog; een proxy offline; een
//     ClusterForge-node onbereikbaar voor Zabbix; de API faalt al langer dan
//     5 minuten;
//   - waarschuwing: problemen van gemiddelde ernst; wachtrij ouder dan 10
//     minuten; ClusterForge-nodes zonder Zabbix-host; een verouderde proxy;
//   - normaal: niets daarvan.
func Evaluate(snap *Snapshot, lastErr string, lastOK time.Time, now time.Time) State {
	if snap == nil {
		if lastErr != "" {
			return State{Level: LevelUnknown, Reasons: []string{"Zabbix is niet bereikbaar: " + lastErr}}
		}
		return State{Level: LevelUnknown, Reasons: []string{"nog geen gegevens van Zabbix"}}
	}
	var crit, prob, warn []string
	c := snap.Counts
	if c.Disaster > 0 {
		crit = append(crit, plural(c.Disaster, "ramp open", "rampen open"))
	}
	if len(snap.HA) > 0 {
		active := false
		for _, n := range snap.HA {
			if n.Status == "active" {
				active = true
			}
		}
		if !active {
			crit = append(crit, "geen actieve node in het HA-cluster van de Zabbix-server")
		}
	}
	if c.High > 0 {
		prob = append(prob, plural(c.High, "probleem met ernst hoog", "problemen met ernst hoog"))
	}
	for _, p := range snap.Proxies {
		if p.State == "offline" {
			prob = append(prob, "proxy "+p.Name+" is offline")
		}
		if p.Compatibility == "outdated" || p.Compatibility == "unsupported" {
			warn = append(warn, "proxy "+p.Name+" draait een oudere versie ("+p.Version+")")
		}
	}
	down := 0
	for _, h := range snap.Hosts {
		if h.Node != nil && h.Enabled && !h.Maintenance && h.Available == AvailDown {
			down++
		}
	}
	if down > 0 {
		prob = append(prob, plural(down, "ClusterForge-node is", "ClusterForge-nodes zijn")+" onbereikbaar voor Zabbix")
	}
	if lastErr != "" && now.Sub(lastOK) > Stale {
		prob = append(prob, "de Zabbix-API faalt sinds "+lastOK.In(brussels()).Format("15:04")+": "+lastErr)
	}
	if c.Average > 0 {
		warn = append(warn, plural(c.Average, "probleem met gemiddelde ernst", "problemen met gemiddelde ernst"))
	}
	for _, h := range snap.Health {
		if h.Queue10m != nil && *h.Queue10m > 0 {
			warn = append(warn, fmt.Sprintf("%s: %.0f items wachten langer dan 10 minuten", h.Name, *h.Queue10m))
		}
	}
	if n := len(snap.Unwatched); n > 0 {
		warn = append(warn, plural(n, "ClusterForge-node zonder", "ClusterForge-nodes zonder")+" Zabbix-host")
	}
	switch {
	case len(crit) > 0:
		return State{Level: LevelCritical, Reasons: top(append(crit, prob...))}
	case len(prob) > 0:
		return State{Level: LevelProblem, Reasons: top(append(prob, warn...))}
	case len(warn) > 0:
		return State{Level: LevelWarning, Reasons: top(warn)}
	}
	return State{Level: LevelNormal, Reasons: []string{"geen open problemen van belang; alle hosts bereikbaar"}}
}

func top(r []string) []string {
	if len(r) > 3 {
		return r[:3]
	}
	return r
}

func brussels() *time.Location {
	if l, err := time.LoadLocation("Europe/Brussels"); err == nil {
		return l
	}
	return time.Local
}
