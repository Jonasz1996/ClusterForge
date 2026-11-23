package publish

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ActDeleteDead ruimt proxy hosts op waarvan het doel niet meer antwoordt
// (testlijst 3, N14). Hosts leeg met Net (192.168.0.x) is alle hosts in
// dat net. Elke host krijgt een eigen kopie; de kopieën blijven op de
// NPM-node en staan in "Wat ClusterForge veranderde" om terug te zetten.
const ActDeleteDead = "delete_dead"

func (s *Service) planDeleteDead(ctx context.Context, npm *NPMRead, a Action, p *Plan) error {
	ov, err := s.Overview(ctx)
	if err != nil {
		return err
	}
	byID := map[int]NPMHost{}
	for _, h := range npm.Hosts {
		byID[h.ID] = h
	}
	ids := a.Hosts
	if len(ids) == 0 && a.Net != "" {
		for _, d := range ov.DeadNets {
			if d.Prefix == a.Net {
				ids = d.Hosts
			}
		}
	}
	if len(ids) == 0 {
		return invalid("kies de hosts of het net om op te ruimen")
	}
	for _, v := range ov.Hosts {
		if !slices.Contains(ids, v.ID) {
			continue
		}
		h, ok := byID[v.ID]
		if !ok || len(h.DomainNames) == 0 {
			continue
		}
		name := strings.ToLower(h.DomainNames[0])
		if !v.Cleanup {
			p.Warnings = append(p.Warnings, name+": overgeslagen, het doel antwoordt nog of er is een voorstel")
			continue
		}
		p.Hosts = append(p.Hosts, PlannedHost{HostID: h.ID, Domain: name, Delete: true, Old: h.fields(), Scheme: schemeOf(h)})
		p.Lines = append(p.Lines, fmt.Sprintf("NPM: %s verwijderen (wijst naar %s:%d, dat niet antwoordt)", name, h.ForwardHost, h.ForwardPort))
	}
	if len(p.Hosts) == 0 {
		return invalid("geen enkele van die hosts wijst nog naar een dood doel")
	}
	p.Title = fmt.Sprintf("%d proxy hosts opruimen", len(p.Hosts))
	if a.Net != "" {
		p.Title += " (net " + a.Net + ")"
	}
	p.Confirm = "opruimen"
	p.Lines = append(p.Lines, "Per host eerst een kopie; terugzetten kan per host via Wat ClusterForge veranderde")
	return nil
}
