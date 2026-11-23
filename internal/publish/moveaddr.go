package publish

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ActMoveAddress zet elke proxy host die precies naar OldAddress wijst op
// NewAddress (fase 48: na een overname of verhuizing, oud IP → VIP). Geen
// gok: alleen hosts met exact dat doel; poort en schema blijven. NPM moet
// het nieuwe adres op die poort bereiken, anders slaat het plan de host
// over.
const ActMoveAddress = "move_address"

// MoveAddressAction maakt de actie.
func MoveAddressAction(oldAddr, newAddr string) Action {
	return Action{Action: ActMoveAddress, OldDomain: oldAddr, NewDomain: newAddr}
}

func (s *Service) planMoveAddress(ctx context.Context, st Settings, npm *NPMRead, managed map[int]managedHost, a Action, p *Plan) error {
	oldAddr, newAddr := strings.TrimSpace(a.OldDomain), strings.TrimSpace(a.NewDomain)
	if net.ParseIP(oldAddr) == nil || net.ParseIP(newAddr) == nil || oldAddr == newAddr {
		return invalid("geef het oude en het nieuwe IP-adres")
	}
	p.Title = fmt.Sprintf("Proxy hosts van %s naar %s", oldAddr, newAddr)
	node, nerr := s.npmNode(ctx, st)
	for _, h := range npm.Hosts {
		if h.ForwardHost != oldAddr || len(h.DomainNames) == 0 {
			continue
		}
		name := strings.ToLower(h.DomainNames[0])
		if nerr == nil {
			u := fmt.Sprintf("%s://%s:%d/", h.ForwardScheme, newAddr, h.ForwardPort)
			res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: checkFor(u)})
			msg := ""
			switch {
			case err != nil:
				msg = err.Error()
			case res.Check != nil && res.Check.Error != "":
				msg = res.Check.Error
			case res.Check != nil && res.Check.Status >= 500:
				msg = fmt.Sprintf("HTTP %d", res.Check.Status)
			}
			if msg != "" {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s: overgeslagen, NPM bereikt %s niet (%s)", name, u, trimText(msg, 120)))
				continue
			}
		}
		ph := PlannedHost{HostID: h.ID, Domain: name, Scheme: schemeOf(h), ServicePort: h.ForwardPort,
			Body: map[string]any{"forward_host": newAddr}, Old: map[string]any{"forward_host": h.ForwardHost}}
		p.Lines = append(p.Lines, fmt.Sprintf("%s: %s://%s:%d → %s:%d", name, h.ForwardScheme, oldAddr, h.ForwardPort, newAddr, h.ForwardPort))
		// Testlijst 3, N4: een host zonder certificaat krijgt het passende
		// (wildcard)certificaat; via Cloudflare gaf https anders 525.
		if h.CertID() == 0 {
			if c := pickCert(npm.Certs, name, s.Now()); c != nil {
				ph.Body["certificate_id"], ph.Body["ssl_forced"] = c.ID, true
				ph.Old["certificate_id"], ph.Old["ssl_forced"] = 0, bool(h.SSLForced)
				p.Lines = append(p.Lines, fmt.Sprintf("%s: certificaat %s erbij, SSL afdwingen", name, certLabel(*c)))
			} else {
				p.Warnings = append(p.Warnings, name+": heeft geen certificaat en er is geen passend certificaat in NPM; https werkt pas na een certificaat (bewerken → SSL)")
			}
		}
		if _, ok := managed[h.ID]; !ok {
			p.Warnings = append(p.Warnings, name+": nog niet beheerd; ClusterForge neemt hem over na de wijziging")
		}
		p.Hosts = append(p.Hosts, ph)
	}
	if len(p.Hosts) == 0 {
		if len(p.Warnings) > 0 {
			return invalid("geen host om om te zetten: %s", strings.Join(p.Warnings, " "))
		}
		return invalid("geen proxy host wijst naar %s", oldAddr)
	}
	return nil
}

// HostsTo telt de proxy hosts die precies naar een adres wijzen (uit de
// laatste lezing van NPM), voor de wizards.
func (s *Service) HostsTo(ctx context.Context, addr string) ([]string, error) {
	var npm NPMRead
	if _, _, err := s.read(ctx, "npm", &npm); err != nil {
		return nil, err
	}
	out := []string{}
	for _, h := range npm.Hosts {
		if h.ForwardHost == addr && len(h.DomainNames) > 0 {
			out = append(out, fmt.Sprintf("%s → %s:%d", h.DomainNames[0], addr, h.ForwardPort))
		}
	}
	return out, nil
}

// StartMoveAddress start ActMoveAddress als taak.
func (s *Service) StartMoveAddress(ctx context.Context, actor events.Actor, oldAddr, newAddr string) (store.Job, error) {
	j, _, err := s.Start(ctx, actor, MoveAddressAction(oldAddr, newAddr))
	return j, err
}
