package publish

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

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
func (s *Service) planRetargetMany(ctx context.Context, st Settings, npm *NPMRead, managed map[int]managedHost, a Action, p *Plan) error {
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
	type cand struct {
		ph    PlannedHost
		lines []string
		warns []string
		urls  []string
	}
	var cands []cand
	guesses := 0
	for _, v := range ov.Hosts {
		explicit := len(a.Hosts) > 0
		if explicit && !slices.Contains(a.Hosts, v.ID) {
			continue
		}
		h, ok := byID[v.ID]
		if !ok || len(h.DomainNames) == 0 {
			continue
		}
		sg := v.Suggest
		// Alle voorstellen samen: alleen de zekere. Een gok (alleen dezelfde
		// poort, of meerdere kandidaten) gaat per host, na nakijken.
		if sg != nil && !sg.Sure && !explicit {
			guesses++
			sg = nil
		}
		if sg == nil && len(v.AdvancedFix) == 0 {
			continue
		}
		have := h.fields()
		want := map[string]any{}
		c := cand{}
		name := strings.ToLower(h.DomainNames[0])
		ph := PlannedHost{HostID: h.ID, Domain: name, Scheme: schemeOf(h), ServicePort: h.ForwardPort}
		if sg != nil {
			want["forward_scheme"], want["forward_host"], want["forward_port"] = sg.Scheme, sg.Host, sg.Port
			c.lines = append(c.lines, fmt.Sprintf("%s: %s://%s:%d → %s://%s:%d (%s, %s)", name, h.ForwardScheme, h.ForwardHost, h.ForwardPort, sg.Scheme, sg.Host, sg.Port, sg.ServiceName, sg.Why))
			if !sg.Sure {
				c.warns = append(c.warns, name+": voorstel is een gok ("+sg.Why+"); kijk het na")
			}
			c.urls = append(c.urls, fmt.Sprintf("%s://%s:%d/", sg.Scheme, sg.Host, sg.Port))
			ph.ServicePort = sg.Port
			if r, ok := svcByKey[sg.ServiceKey]; ok {
				ph.ClusterID = r.ClusterID
				if len(r.Nodes) == 1 && !r.TargetVIP {
					ph.NodeID = &r.Nodes[0].ID
				}
			}
		}
		if len(v.AdvancedFix) > 0 {
			want["advanced_config"] = applyAdvFixes(h.AdvancedConfig, v.AdvancedFix)
			for _, f := range v.AdvancedFix {
				c.lines = append(c.lines, fmt.Sprintf("%s: in Advanced %s → %s (%s)", name, f.Old, f.New, f.Why))
				c.urls = append(c.urls, f.New+"/")
			}
		}
		body := diffFields(have, want)
		if len(body) == 0 {
			continue
		}
		old := map[string]any{}
		for k := range body {
			old[k] = have[k]
		}
		ph.Body, ph.Old = body, old
		if _, ok := managed[h.ID]; !ok {
			c.warns = append(c.warns, name+": nog niet beheerd; ClusterForge neemt hem over na de wijziging")
		}
		c.ph = ph
		cands = append(cands, c)
	}
	// Bereikt NPM het nieuwe adres? Zo niet, dan niet toepassen: de controle
	// achteraf zou toch mislukken en terugdraaien.
	if node, err := s.npmNode(ctx, st); err == nil && len(cands) > 0 {
		var mu sync.Mutex
		bad := map[string]string{}
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, c := range cands {
			for _, u := range c.urls {
				mu.Lock()
				_, done := bad[u]
				bad[u] = ""
				mu.Unlock()
				if done {
					continue
				}
				wg.Add(1)
				go func(u string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					msg := ""
					res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: checkFor(u)})
					switch {
					case err != nil:
						msg = err.Error()
					case res.Check == nil:
					case res.Check.Error != "":
						msg = res.Check.Error
					case res.Check.Status >= 500:
						msg = fmt.Sprintf("HTTP %d", res.Check.Status)
					}
					mu.Lock()
					bad[u] = msg
					mu.Unlock()
				}(u)
			}
		}
		wg.Wait()
		kept := cands[:0]
		for _, c := range cands {
			var why []string
			for _, u := range c.urls {
				if bad[u] != "" {
					why = append(why, u+": "+trimText(bad[u], 120))
				}
			}
			if len(why) > 0 {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s: overgeslagen, NPM (%s) bereikt het nieuwe adres niet (%s). Firewall tussen NPM en dat VLAN, of draait de dienst daar niet?", c.ph.Domain, node.Hostname, strings.Join(why, "; ")))
				continue
			}
			kept = append(kept, c)
		}
		cands = kept
	}
	for _, c := range cands {
		p.Lines = append(p.Lines, c.lines...)
		p.Warnings = append(p.Warnings, c.warns...)
		p.Hosts = append(p.Hosts, c.ph)
	}
	if guesses > 0 {
		p.Lines = append(p.Lines, fmt.Sprintf("%d hosts met alleen een gok (zelfde poort of meerdere kandidaten) blijven staan; die zet je per host via \"nieuw doel\" of Bewerken", guesses))
	}
	if len(p.Hosts) == 0 {
		if len(p.Warnings) > 0 {
			return invalid("geen host om om te zetten: %s", strings.Join(p.Warnings, " "))
		}
		return invalid("geen hosts met een zeker voorstel voor een nieuw doel")
	}
	if len(p.Hosts) > 1 {
		p.Lines = append(p.Lines, fmt.Sprintf("Host voor host, elk met een eigen kopie en controle; mislukt er een, dan gaat alleen die terug en gaan de andere door (%d hosts)", len(p.Hosts)))
	}
	return nil
}

// checkFor maakt een controle van een doeladres zoals http://10.0.0.5:8080/:
// de agent wil een naam in de URL, dus verbindt hij via Connect met het IP.
func checkFor(target string) *protocol.WebCheck {
	u, err := url.Parse(target)
	if err != nil {
		return &protocol.WebCheck{URL: target}
	}
	ip := u.Hostname()
	if net.ParseIP(ip) == nil {
		return &protocol.WebCheck{URL: target}
	}
	host := "doel.clusterforge.invalid"
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	return &protocol.WebCheck{URL: u.Scheme + "://" + host + u.EscapedPath(), Connect: ip}
}
