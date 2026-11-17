package httpapi

import (
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// trueNASAttention zet de TrueNAS-meldingen in "Aandacht nodig", uit de
// laatste achtergrondronde.
func (s *Server) trueNASAttention(add func(id string, tone gen.AttentionItemTone, title, detail, href string, count int)) {
	for _, sum := range s.truenas.Cached() {
		id := sum.Server.NodeID.String()
		name := "TrueNAS " + sum.Server.Hostname
		if sum.Error != "" {
			add("truenas-unreachable-"+id, gen.AttentionItemToneAmber, name+" is niet te lezen", sum.Error, "/truenas", 1)
			continue
		}
		var crit, warn []string
		for _, w := range sum.Warnings {
			if w.Level == "critical" {
				crit = append(crit, w.Text)
			} else {
				warn = append(warn, w.Text)
			}
		}
		if len(crit) > 0 {
			add("truenas-red-"+id, gen.AttentionItemToneRed, name+": "+crit[0], strings.Join(crit[1:], " · "), "/truenas", len(crit))
		}
		if len(warn) > 0 {
			add("truenas-amber-"+id, gen.AttentionItemToneAmber, name+": "+plural(len(warn), "waarschuwing", "waarschuwingen"), strings.Join(warn, " · "), "/truenas", len(warn))
		}
		if sum.Update != "" {
			add("truenas-update-"+id, gen.AttentionItemToneAmber, name+": update "+sum.Update+" beschikbaar", "Bijwerken via TrueNAS → Overzicht → Updates.", "/truenas", 1)
		}
	}
}
