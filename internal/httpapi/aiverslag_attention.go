package httpapi

import (
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// aivAttention zet de meldingen van AI-verslag (fase 38 M3) in "Aandacht
// nodig": dreigingsniveau, alarmen die niemand zag, diensten of timers
// die stuk zijn, Ollama uit, een oude analyse en een volle schijf. Uit de
// laatste achtergrondronde.
func (s *Server) aivAttention(add func(id string, tone gen.AttentionItemTone, title, detail, href string, count int)) {
	for _, sum := range s.aiverslag.Cached() {
		id := sum.Instance.NodeID.String()
		name := "AI-verslag"
		if sum.Instance.Hostname != "" {
			name += " (" + sum.Instance.Hostname + ")"
		}
		if sum.Error != "" && len(sum.Warnings) == 0 {
			add("aiv-unreachable-"+id, gen.AttentionItemToneAmber, name+" is niet te lezen", sum.Error, "/aiverslag", 1)
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
			add("aiv-red-"+id, gen.AttentionItemToneRed, name+": "+crit[0], strings.Join(crit[1:], " · "), "/aiverslag", len(crit))
		}
		if len(warn) > 0 {
			add("aiv-amber-"+id, gen.AttentionItemToneAmber, name+": "+warn[0], strings.Join(warn[1:], " · "), "/aiverslag", len(warn))
		}
	}
}
