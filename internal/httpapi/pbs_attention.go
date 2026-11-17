package httpapi

import (
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// pbsAttention zet de PBS-meldingen in "Aandacht nodig". Alleen uit de
// laatste achtergrondronde: een paginalading vraagt de PBS niets.
func (s *Server) pbsAttention(add func(id string, tone gen.AttentionItemTone, title, detail, href string, count int)) {
	sums, cov := s.pbs.Cached()
	for _, sum := range sums {
		id := sum.Server.NodeID.String()
		href := "/pbs?pbs=" + id
		if sum.Error != "" {
			add("pbs-unreachable-"+id, gen.AttentionItemToneAmber, "Backup Server "+sum.Server.Hostname+" is niet te lezen", sum.Error, href, 1)
			continue
		}
		var crit, warn []string
		for _, w := range sum.Warnings {
			if w.Level == "crit" {
				crit = append(crit, w.Text)
			} else {
				warn = append(warn, w.Text)
			}
		}
		if len(crit) > 0 {
			add("pbs-red-"+id, gen.AttentionItemToneRed, "Backup Server "+sum.Server.Hostname+": "+crit[0], strings.Join(crit[1:], " · "), href, len(crit))
		}
		if len(warn) > 0 {
			add("pbs-amber-"+id, gen.AttentionItemToneAmber, "Backup Server "+sum.Server.Hostname+": "+plural(len(warn), "waarschuwing", "waarschuwingen"), strings.Join(warn, " · "), href, len(warn))
		}
	}
	if cov == nil {
		return
	}
	// Alleen nodes in een cluster: prod rood, de rest oranje.
	var red, amber []string
	for _, n := range cov.Nodes {
		if n.ClusterID == nil || n.State == "ok" {
			continue
		}
		label := n.Hostname + " (" + n.Why + ")"
		if n.State == "crit" && n.Environment == "prod" {
			red = append(red, label)
		} else {
			amber = append(amber, label)
		}
	}
	add("pbs-backup-red", gen.AttentionItemToneRed, plural(len(red), "prod-node heeft", "prod-nodes hebben")+" geen goede back-up op PBS", names(red), "/pbs/dekking", len(red))
	add("pbs-backup-amber", gen.AttentionItemToneAmber, plural(len(amber), "clusternode heeft", "clusternodes hebben")+" een oude, kapotte of geen back-up op PBS", names(amber), "/pbs/dekking", len(amber))
}
