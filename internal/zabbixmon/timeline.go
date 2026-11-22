package zabbixmon

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// --- problemen in het logboek (M5) ------------------------------------------------------
//
// Een Zabbix-probleem op een ClusterForge-node komt als event in het
// logboek, en daarmee in de tijdlijn, What Broke en de assistent: zo zie je
// wat er in ClusterForge veranderde vlak voor Zabbix iets zag. Alleen vanaf
// ernst gemiddeld, niet onderdrukt, en één keer bij het begin en één keer bij
// het einde.

// LogFrom: vanaf deze ernst gaat een probleem naar het logboek.
const LogFrom = 3

type loggedProblem struct {
	Problem
	nodes []NodeLink
}

var severityName = []string{"niet ingedeeld", "informatie", "waarschuwing", "gemiddeld", "hoog", "ramp"}

func logWorthy(p Problem) []NodeLink {
	if p.Suppressed || p.Severity < LogFrom {
		return nil
	}
	var out []NodeLink
	seen := map[uuid.UUID]bool{}
	for _, h := range p.Hosts {
		if h.Node != nil && !seen[h.Node.NodeID] {
			seen[h.Node.NodeID] = true
			out = append(out, *h.Node)
		}
	}
	return out
}

// logProblems schrijft nieuwe en opgeloste problemen naar het logboek. De
// eerste ronde na het starten onthoudt alleen wat er openstaat, behalve
// problemen die pas begonnen: anders zou elke herstart alles opnieuw melden.
func (s *Service) logProblems(ctx context.Context, snap *Snapshot, now time.Time) {
	first := s.logged == nil
	cur := map[string]loggedProblem{}
	for _, p := range snap.Problems {
		if nodes := logWorthy(p); len(nodes) > 0 {
			cur[p.EventID] = loggedProblem{p, nodes}
		}
	}
	if s.ev != nil {
		for id, p := range cur {
			if _, ok := s.logged[id]; ok {
				continue
			}
			if first && now.Sub(p.Since) > 2*s.Every {
				continue
			}
			s.writeProblem(ctx, "zabbix.problem_started", p, now)
		}
		for id, p := range s.logged {
			if _, ok := cur[id]; !ok {
				s.writeProblem(ctx, "zabbix.problem_resolved", p, now)
			}
		}
	}
	s.logged = cur
}

func (s *Service) writeProblem(ctx context.Context, action string, p loggedProblem, now time.Time) {
	sev := min(max(p.Severity, 0), 5)
	for _, n := range p.nodes {
		payload := map[string]any{
			"eventid": p.EventID, "name": p.Name, "severity": sev, "severity_label": severityName[sev],
			"since": p.Since.UTC().Format(time.RFC3339), "host": n.Hostname,
		}
		if action == "zabbix.problem_resolved" {
			payload["duration_s"] = int64(now.Sub(p.Since).Seconds())
		}
		e := events.Event{Actor: events.System(), SubjectType: "node", SubjectID: n.NodeID.String(), ClusterID: n.ClusterID, Action: action, Payload: payload}
		if err := s.ev.Write(ctx, nil, e); err != nil {
			s.log.Warn("Zabbix-probleem naar het logboek mislukt", "eventid", p.EventID, "err", err)
		}
	}
}
