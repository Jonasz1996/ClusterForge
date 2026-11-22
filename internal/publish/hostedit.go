package publish

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// keepSnippet zet de eigen Advanced van de gebruiker en houdt de blokken
// van ClusterForge (forward auth naar Authentik, sjabloon) erachter.
func keepSnippet(current, own string) (string, error) {
	own = strings.TrimSpace(strings.ReplaceAll(own, "\r\n", "\n"))
	if strings.Contains(own, snippetBegin) || strings.Contains(own, snippetEnd) || strings.Contains(own, themeBegin) || strings.Contains(own, themeEnd) {
		return "", invalid("de blokken van ClusterForge horen niet in je eigen Advanced; zet Authentik aan of uit met de knop en het thema via Sjabloon")
	}
	var blocks []string
	if i := strings.Index(current, snippetBegin); i >= 0 {
		if j := strings.Index(current[i:], snippetEnd); j >= 0 {
			blocks = append(blocks, current[i:i+j+len(snippetEnd)])
		}
	}
	if b := themeBlock(current); b != "" {
		blocks = append(blocks, b)
	}
	if len(blocks) > 0 && locationRoot.MatchString(own) {
		return "", invalid("een eigen 'location /' botst met het blok van ClusterForge op deze host (forward auth of sjabloon). Zet het in het sjabloon, of zet de host in Sjabloon op \"geen thema\" en Authentik uit")
	}
	return joinBlocks(append([]string{own}, blocks...)...), nil
}

// planDomains controleert nieuwe namen voor een host. Staat er in Authentik
// een provider op de oude naam en nog niet op de nieuwe, dan krijgt die
// provider de nieuwe naam mee.
func (s *Service) planDomains(ctx context.Context, st Settings, h NPMHost, in []string, taken map[string]int, p *Plan, providerPK *int) ([]string, error) {
	var names []string
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(d, ".")))
		if d == "" || slices.Contains(names, d) {
			continue
		}
		if !protocol.ValidDomain(d) {
			return nil, invalid("%q is geen geldige naam", d)
		}
		if id, ok := taken[d]; ok && id != h.ID {
			return nil, invalid("%s bestaat al in NPM (host %d)", d, id)
		}
		names = append(names, d)
	}
	if len(names) == 0 {
		return nil, invalid("een host heeft minstens één naam nodig")
	}
	if !inDomain(names[0], st.Domain) {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s valt niet onder het hoofddomein %s", names[0], st.Domain))
	}
	oldName := strings.ToLower(h.DomainNames[0])
	if names[0] != oldName && st.AuthentikLinked() && hasSnippet(h.AdvancedConfig) {
		if ak, err := s.readAuthentik(ctx, st); err == nil {
			hasNew := false
			for _, pr := range ak.Providers {
				if pr.Host() == names[0] {
					hasNew = true
				}
			}
			for _, pr := range ak.Providers {
				if pr.Host() == oldName && !hasNew {
					*providerPK = pr.PK
					p.Lines = append(p.Lines, "Authentik: provider krijgt de nieuwe naam https://"+names[0])
				}
			}
			if hasNew {
				p.Lines = append(p.Lines, "Authentik: er staat al een provider voor "+names[0]+"; die gaat de host beschermen")
			}
		}
	}
	return names, nil
}

// chooseCert kiest het certificaat bij het wijzigen: auto, new, none of een id.
func (s *Service) chooseCert(st Settings, npm *NPMRead, name, ssl string) (int, bool, string, string, error) {
	switch ssl {
	case "none":
		return 0, false, "Certificaat: geen (alleen http)", "", nil
	case "new":
		return 0, true, "Certificaat: nieuw bij Let's Encrypt voor " + name + " (HTTP-challenge: de naam moet publiek naar NPM wijzen)", "", nil
	case "auto":
		if c := pickCert(npm.Certs, name, s.Now()); c != nil {
			return c.ID, false, fmt.Sprintf("Certificaat: %s (geldig tot %s)", certLabel(*c), c.Expires().Format("2006-01-02")), "", nil
		}
		if st.Defaults.LetsEncrypt {
			return 0, true, "Certificaat: nieuw bij Let's Encrypt voor " + name, "", nil
		}
		return 0, false, "", "geen certificaat voor " + name + " gevonden en Let's Encrypt staat uit: alleen http", nil
	}
	id, err := strconv.Atoi(ssl)
	if err != nil {
		return 0, false, "", "", invalid("ongeldig certificaat %q", ssl)
	}
	for _, c := range npm.Certs {
		if c.ID == id {
			warn := ""
			if !c.Covers(name) {
				warn = fmt.Sprintf("certificaat %s dekt %s niet", certLabel(c), name)
			}
			return id, false, "Certificaat: " + certLabel(c), warn, nil
		}
	}
	return 0, false, "", "", invalid("certificaat %d bestaat niet in NPM", id)
}

// planRetargetMany zet hosts op het voorgestelde doel.
func (s *Service) planRetargetMany(ctx context.Context, npm *NPMRead, managed map[int]managedHost, a Action, p *Plan) error {
	ov, err := s.Overview(ctx)
	if err != nil {
		return err
	}
	byID := map[int]NPMHost{}
	for _, h := range npm.Hosts {
		byID[h.ID] = h
	}
	svcByKey := map[string]ServiceRow{}
	for _, r := range ov.Services {
		svcByKey[r.Key] = r
	}
	p.Title = "Nieuw doel voor proxy hosts"
	for _, v := range ov.Hosts {
		if v.Suggest == nil || (len(a.Hosts) > 0 && !slices.Contains(a.Hosts, v.ID)) {
			continue
		}
		h, ok := byID[v.ID]
		if !ok || len(h.DomainNames) == 0 {
			continue
		}
		sg := v.Suggest
		want := map[string]any{"forward_scheme": sg.Scheme, "forward_host": sg.Host, "forward_port": sg.Port}
		have := h.fields()
		body := diffFields(have, want)
		if len(body) == 0 {
			continue
		}
		old := map[string]any{}
		for k := range body {
			old[k] = have[k]
		}
		ph := PlannedHost{HostID: h.ID, Domain: strings.ToLower(h.DomainNames[0]), Body: body, Old: old, Scheme: schemeOf(h), ServicePort: sg.Port}
		if r, ok := svcByKey[sg.ServiceKey]; ok {
			ph.ClusterID = r.ClusterID
			if len(r.Nodes) == 1 && !r.TargetVIP {
				ph.NodeID = &r.Nodes[0].ID
			}
		}
		line := fmt.Sprintf("%s: %s://%s:%d → %s://%s:%d (%s, %s)", ph.Domain, h.ForwardScheme, h.ForwardHost, h.ForwardPort, sg.Scheme, sg.Host, sg.Port, sg.ServiceName, sg.Why)
		p.Lines = append(p.Lines, line)
		if !sg.Sure {
			p.Warnings = append(p.Warnings, ph.Domain+": voorstel is een gok ("+sg.Why+"); kijk het na")
		}
		if _, ok := managed[h.ID]; !ok {
			p.Warnings = append(p.Warnings, ph.Domain+": nog niet beheerd; ClusterForge neemt hem over na de wijziging")
		}
		p.Hosts = append(p.Hosts, ph)
	}
	if len(p.Hosts) == 0 {
		return invalid("geen hosts met een voorstel voor een nieuw doel")
	}
	if len(p.Hosts) > 1 {
		p.Lines = append(p.Lines, fmt.Sprintf("Host voor host, elk met een eigen kopie en controle; mislukt er een, dan gaat alleen die terug en gaan de andere door (%d hosts)", len(p.Hosts)))
	}
	return nil
}
