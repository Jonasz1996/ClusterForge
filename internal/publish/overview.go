package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Toestanden van Authentik bij een host.
const (
	AKProtected = "beschermd"
	AKHalf      = "half"
	AKOpen      = "open"
	// AKUnknown: Authentik is niet gekoppeld of niet gelezen.
	AKUnknown = ""
)

// Problem is iets dat niet klopt bij een host.
type Problem struct {
	Code  string `json:"code"`
	Level string `json:"level"` // crit of warn
	Text  string `json:"text"`
}

// HostView is een proxy host met wat ClusterForge erover weet.
type HostView struct {
	ID            int        `json:"id"`
	Domains       []string   `json:"domains"`
	ForwardScheme string     `json:"forward_scheme"`
	ForwardHost   string     `json:"forward_host"`
	ForwardPort   int        `json:"forward_port"`
	Enabled       bool       `json:"enabled"`
	SSLForced     bool       `json:"ssl_forced"`
	Websockets    bool       `json:"websockets"`
	CertID        int        `json:"certificate_id"`
	CertName      string     `json:"certificate_name,omitempty"`
	CertExpires   *time.Time `json:"certificate_expires,omitempty"`
	AccessList    string     `json:"access_list,omitempty"`
	HasAdvanced   bool       `json:"has_advanced"`
	// Managed: ClusterForge beheert de host (gemaakt of overgenomen).
	Managed     bool `json:"managed"`
	CreatedByCF bool `json:"created_by_cf"`
	// Authentik is beschermd, half, open of leeg.
	Authentik       string    `json:"authentik"`
	AuthentikDetail string    `json:"authentik_detail,omitempty"`
	AuthentikByCF   bool      `json:"authentik_by_cf"`
	Problems        []Problem `json:"problems"`
	Drift           []string  `json:"drift,omitempty"`
	// Service is de dienst waar de host naar wijst.
	ServiceKey  string `json:"service_key,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
	ClusterName string `json:"cluster_name,omitempty"`
	// VIP is het VIP om naar te wijzen als de host nu naar één node wijst.
	SuggestVIP string `json:"suggest_vip,omitempty"`
	// Suggest: nieuw doel voor een host die naar een oud of onbekend adres
	// wijst (fase 42).
	Suggest *TargetSuggestion `json:"suggest,omitempty"`
	// Voor het bewerken (fase 42).
	HTTP2         bool   `json:"http2"`
	HSTS          bool   `json:"hsts"`
	BlockExploits bool   `json:"block_exploits"`
	Advanced      string `json:"advanced,omitempty"`
	// Typo: verbeterde naam bij een tikfout in het domein.
	Typo string `json:"typo,omitempty"`
	// TypoDomains: alle namen met de tikfout rechtgezet (voor één klik).
	TypoDomains []string `json:"typo_domains,omitempty"`
}

// CertView is een certificaat in NPM, om te kiezen bij een host.
type CertView struct {
	ID       int        `json:"id"`
	Name     string     `json:"name"`
	Domains  []string   `json:"domains"`
	Provider string     `json:"provider"`
	Expires  *time.Time `json:"expires,omitempty"`
}

// AKAppView is een applicatie van Authentik met zijn provider en host.
type AKAppView struct {
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Provider  string `json:"provider,omitempty"`
	Mode      string `json:"mode,omitempty"`
	External  string `json:"external_host,omitempty"`
	InOutpost bool   `json:"in_outpost"`
	HostID    int    `json:"host_id,omitempty"`
	// Snippet: de NPM-host heeft de forward-auth-config.
	Snippet bool   `json:"snippet"`
	State   string `json:"state"`
	Detail  string `json:"detail,omitempty"`
}

// Overview is alles voor de pagina Publicatie.
type Overview struct {
	Settings  Settings     `json:"settings"`
	Services  []ServiceRow `json:"services"`
	Hosts     []HostView   `json:"hosts"`
	Apps      []AKAppView  `json:"apps"`
	Nodes     []NodeState  `json:"nodes"`
	NPM       ReadState    `json:"npm"`
	Authentik ReadState    `json:"authentik"`
	Notices   []Notice     `json:"notices"`
	Counts    Counts       `json:"counts"`
	Clusters  []ClusterRef `json:"clusters"`
	Certs     []CertView   `json:"certs"`
	Protocol  int          `json:"protocol"`
}

// ClusterRef is een cluster om in de instellingen te kiezen.
type ClusterRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Template string    `json:"template,omitempty"`
	VIPs     []string  `json:"vips,omitempty"`
}

// NodeState is de stand van de ontdekking op een node.
type NodeState struct {
	ID          uuid.UUID  `json:"id"`
	Hostname    string     `json:"hostname"`
	ClusterName string     `json:"cluster_name,omitempty"`
	Ready       string     `json:"ready,omitempty"`
	Error       string     `json:"error,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	Services    int        `json:"services"`
}

// ReadState zegt wanneer en van waar NPM of Authentik laatst gelezen is.
type ReadState struct {
	Linked   bool       `json:"linked"`
	Hostname string     `json:"hostname,omitempty"`
	Version  string     `json:"version,omitempty"`
	ReadAt   *time.Time `json:"read_at,omitempty"`
	Error    string     `json:"error,omitempty"`
}

// Counts zijn de aantallen bovenaan (ook voor de dashboard-API).
type Counts struct {
	Services    int `json:"services"`
	Web         int `json:"web"`
	Unpublished int `json:"unpublished"`
	// Internal: interne diensten die niet gepubliceerd zijn (standaard verborgen).
	Internal  int `json:"internal"`
	Hosts     int `json:"hosts"`
	Managed   int `json:"managed"`
	Protected int `json:"protected"`
	Problems  int `json:"problems"`
}

// Notice is een melding voor Aandacht nodig.
type Notice struct {
	ID     string   `json:"id"`
	Red    bool     `json:"red"`
	Title  string   `json:"title"`
	Detail string   `json:"detail,omitempty"`
	Names  []string `json:"names,omitempty"`
	Href   string   `json:"href"`
	Count  int      `json:"count"`
}

// managedHost is een rij uit publish_hosts.
type managedHost struct {
	ID          uuid.UUID
	NPMHostID   int
	Domain      string
	Desired     map[string]any
	ClusterID   *uuid.UUID
	NodeID      *uuid.UUID
	ServicePort *int
	CreatedByCF bool
	AKProvider  *int
	AKAppSlug   *string
	AKCreated   bool
}

func (s *Service) managed(ctx context.Context) (map[int]managedHost, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, npm_host_id, domain, desired, cluster_id, node_id, service_port, created_by_cf,
		ak_provider_pk, ak_app_slug, ak_created FROM publish_hosts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]managedHost{}
	for rows.Next() {
		var m managedHost
		var desired []byte
		if err := rows.Scan(&m.ID, &m.NPMHostID, &m.Domain, &desired, &m.ClusterID, &m.NodeID, &m.ServicePort, &m.CreatedByCF,
			&m.AKProvider, &m.AKAppSlug, &m.AKCreated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(desired, &m.Desired)
		out[m.NPMHostID] = m
	}
	return out, rows.Err()
}

// inDomain zegt of een naam onder het hoofddomein valt.
func inDomain(name, domain string) bool {
	name = strings.ToLower(name)
	return name == domain || strings.HasSuffix(name, "."+domain)
}

// hasSnippet zegt of de config van een host forward auth naar Authentik doet.
func hasSnippet(adv string) bool { return strings.Contains(adv, "outpost.goauthentik.io") }

// Overview bouwt de pagina Publicatie.
func (s *Service) Overview(ctx context.Context) (*Overview, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.nodes(ctx)
	if err != nil {
		return nil, err
	}
	clusters, err := s.clusters(ctx)
	if err != nil {
		return nil, err
	}
	found, err := s.loadServices(ctx, nodes)
	if err != nil {
		return nil, err
	}
	managed, err := s.managed(ctx)
	if err != nil {
		return nil, err
	}
	var npm NPMRead
	npmAt, npmErr, err := s.read(ctx, "npm", &npm)
	if err != nil {
		return nil, err
	}
	var ak AKRead
	akAt, akErr, err := s.read(ctx, "authentik", &ak)
	if err != nil {
		return nil, err
	}
	ov := &Overview{Settings: st, Protocol: 51}
	ov.Services = buildServices(nodes, clusters, found, st.Defaults)
	for _, c := range clusters {
		ref := ClusterRef{ID: c.ID, Name: c.Name, Template: c.Template}
		for _, v := range c.VIPs {
			ref.VIPs = append(ref.VIPs, v.Address)
		}
		ov.Clusters = append(ov.Clusters, ref)
	}
	for _, n := range nodes {
		ns := NodeState{ID: n.ID, Hostname: n.Hostname, ClusterName: n.ClusterName, Ready: n.Ready()}
		if f, ok := found[n.ID]; ok {
			ns.Error, ns.Services = f.err, len(f.services)
			at := f.at
			ns.CheckedAt = &at
		}
		ov.Nodes = append(ov.Nodes, ns)
	}
	ov.NPM = ReadState{Linked: st.NPMLinked(), Error: npmErr, Hostname: npm.Hostname, Version: npm.Version}
	if !npmAt.IsZero() {
		ov.NPM.ReadAt = &npmAt
	}
	ov.Authentik = ReadState{Linked: st.AuthentikLinked(), Error: akErr, Hostname: ak.Hostname, Version: ak.Version}
	if !akAt.IsZero() {
		ov.Authentik.ReadAt = &akAt
	}
	if !st.NPMLinked() {
		npm = NPMRead{}
	}
	akRead := st.AuthentikLinked() && !akAt.IsZero()
	ov.Hosts = buildHosts(st, nodes, clusters, ov.Services, npm, ak, akRead, managed, s.Now())
	ov.Apps = buildApps(npm, ak)
	ov.Certs = make([]CertView, 0, len(npm.Certs))
	for _, c := range npm.Certs {
		cv := CertView{ID: c.ID, Name: certLabel(c), Domains: c.DomainNames, Provider: c.Provider}
		if exp := c.Expires(); !exp.IsZero() {
			cv.Expires = &exp
		}
		ov.Certs = append(ov.Certs, cv)
	}
	// Bij elke dienst: de hosts die er nu naartoe wijzen.
	byKey := map[string]int{}
	for i, r := range ov.Services {
		byKey[r.Key] = i
	}
	for _, h := range ov.Hosts {
		if i, ok := byKey[h.ServiceKey]; ok && len(h.Domains) > 0 {
			ov.Services[i].Published = append(ov.Services[i].Published, PublishedRef{HostID: h.ID, Domain: h.Domains[0], Authentik: h.Authentik})
		}
	}
	ov.Counts = Counts{Services: len(ov.Services), Hosts: len(ov.Hosts)}
	for _, r := range ov.Services {
		if r.Internal && len(r.Published) == 0 {
			ov.Counts.Internal++
			continue
		}
		if r.Web {
			ov.Counts.Web++
			if len(r.Published) == 0 {
				ov.Counts.Unpublished++
			}
		}
	}
	for _, h := range ov.Hosts {
		if h.Managed {
			ov.Counts.Managed++
		}
		if h.Authentik == AKProtected {
			ov.Counts.Protected++
		}
		if len(h.Problems) > 0 || len(h.Drift) > 0 {
			ov.Counts.Problems++
		}
	}
	ov.Notices = notices(ov)
	return ov, nil
}

// targetIndex koppelt host:poort aan een dienst.
func targetIndex(nodes []nodeRow, services []ServiceRow) (map[string]int, map[string]bool) {
	idx := map[string]int{}
	known := map[string]bool{}
	for _, n := range nodes {
		if n.PrimaryIP != "" {
			known[n.PrimaryIP] = true
		}
		known[strings.ToLower(n.Hostname)] = true
		for _, v := range n.VIPs {
			known[v] = true
		}
	}
	for i, r := range services {
		if r.Proto != "tcp" {
			continue
		}
		port := strconv.Itoa(r.Port)
		if r.TargetVIP {
			idx[r.Target+":"+port] = i
			known[r.Target] = true
		}
		for _, n := range r.Nodes {
			if n.IP != "" {
				if _, taken := idx[n.IP+":"+port]; !taken {
					idx[n.IP+":"+port] = i
				}
			}
			idx[strings.ToLower(n.Hostname)+":"+port] = i
		}
	}
	return idx, known
}

func buildHosts(st Settings, nodes []nodeRow, clusters []clusterRow, services []ServiceRow, npm NPMRead, ak AKRead, akRead bool,
	managed map[int]managedHost, now time.Time) []HostView {
	idx, known := targetIndex(nodes, services)
	for _, c := range clusters {
		for _, v := range c.VIPs {
			known[v.Address] = true
		}
	}
	certByID := map[int]NPMCert{}
	for _, c := range npm.Certs {
		certByID[c.ID] = c
	}
	listByID := map[int]string{}
	for _, l := range npm.AccessLists {
		listByID[l.ID] = l.Name
	}
	provByHost := map[string]AKProxyProvider{}
	for _, p := range ak.Providers {
		if h := p.Host(); h != "" {
			provByHost[h] = p
		}
	}
	out := make([]HostView, 0, len(npm.Hosts))
	for _, h := range npm.Hosts {
		v := HostView{
			ID: h.ID, Domains: h.DomainNames, ForwardScheme: h.ForwardScheme, ForwardHost: h.ForwardHost, ForwardPort: h.ForwardPort,
			Enabled: h.IsEnabled(), SSLForced: bool(h.SSLForced), Websockets: bool(h.AllowWebsocketUpgrade), CertID: h.CertID(),
			HasAdvanced: strings.TrimSpace(h.AdvancedConfig) != "", AccessList: listByID[h.AccessListID], Problems: []Problem{},
			HTTP2: bool(h.HTTP2Support), HSTS: bool(h.HSTSEnabled), BlockExploits: bool(h.BlockExploits),
			Advanced: strings.TrimSpace(withoutSnippet(h.AdvancedConfig)),
		}
		add := func(code, level, format string, args ...any) {
			v.Problems = append(v.Problems, Problem{Code: code, Level: level, Text: fmt.Sprintf(format, args...)})
		}
		if c, ok := certByID[v.CertID]; ok {
			v.CertName = c.NiceName
			if v.CertName == "" && len(c.DomainNames) > 0 {
				v.CertName = c.DomainNames[0]
			}
			if exp := c.Expires(); !exp.IsZero() {
				v.CertExpires = &exp
				switch days := exp.Sub(now).Hours() / 24; {
				case days < 0:
					add("cert_verlopen", "crit", "het certificaat %s is verlopen op %s", v.CertName, exp.Format("2006-01-02"))
				case days < 21:
					add("cert_verloopt", "warn", "het certificaat %s verloopt over %d dagen", v.CertName, int(days))
				}
			}
		}
		if m, ok := managed[h.ID]; ok {
			v.Managed, v.CreatedByCF, v.AuthentikByCF = true, m.CreatedByCF, m.AKCreated
			for k, val := range diffFields(h.fields(), m.Desired) {
				v.Drift = append(v.Drift, driftText(k, h.fields()[k], val))
			}
			sort.Strings(v.Drift)
		}
		if h.Meta.NginxOnline != nil && !*h.Meta.NginxOnline {
			msg := "nginx weigert de config van deze host"
			if h.Meta.NginxErr != nil && *h.Meta.NginxErr != "" {
				msg += ": " + trimText(*h.Meta.NginxErr, 200)
			}
			add("nginx_fout", "crit", "%s", msg)
		}
		inDom := false
		for _, d := range h.DomainNames {
			if inDomain(d, st.Domain) {
				inDom = true
			}
		}
		var typoMsgs []string
		fixedNames := make([]string, 0, len(h.DomainNames))
		for _, d := range h.DomainNames {
			d = strings.ToLower(d)
			if tail, fixed, ok := domainTypo(d, st.Domain); ok {
				msg := fmt.Sprintf("%s staat onder %s, dat lijkt op %s: tikfout? Bedoeld is waarschijnlijk %s", d, tail, st.Domain, fixed)
				if _, ok := provByHost[fixed]; ok {
					msg += " (in Authentik staat die naam al)"
				}
				typoMsgs = append(typoMsgs, msg)
				if v.Typo == "" {
					v.Typo = fixed
				}
				d = fixed
			}
			if !slices.Contains(fixedNames, d) {
				fixedNames = append(fixedNames, d)
			}
		}
		if len(typoMsgs) > 0 {
			v.TypoDomains = fixedNames
			add("domein_tikfout", "warn", "%s", strings.Join(typoMsgs, "; "))
		} else if !inDom && len(h.DomainNames) > 0 {
			add("buiten_domein", "info", "niet onder %s", st.Domain)
		}
		fh := strings.ToLower(h.ForwardHost)
		if i, ok := idx[fh+":"+strconv.Itoa(h.ForwardPort)]; ok {
			r := services[i]
			v.ServiceKey, v.ServiceName, v.ClusterName = r.Key, r.Name, r.ClusterName
			if r.TargetVIP && fh != r.Target {
				v.SuggestVIP = r.Target
				add("naar_node", "warn", "wijst naar één node (%s) terwijl %s op het VIP %s draait", h.ForwardHost, r.Name, r.Target)
			}
		} else {
			if known[fh] && v.Enabled {
				add("doel_weg", "crit", "op %s:%d draait niets dat ClusterForge kent (dienst gestopt of verhuisd?)", h.ForwardHost, h.ForwardPort)
			}
			if len(h.DomainNames) > 0 {
				v.Suggest = suggestTarget(h.DomainNames[0], h.ForwardPort, services)
			}
			if v.Suggest != nil && !known[fh] {
				add("oud_doel", "warn", "wijst naar %s:%d, een adres dat ClusterForge niet kent; voorstel: %s://%s:%d (%s)",
					h.ForwardHost, h.ForwardPort, v.Suggest.Scheme, v.Suggest.Host, v.Suggest.Port, v.Suggest.ServiceName)
			}
		}
		// Authentik.
		if akRead && len(h.DomainNames) > 0 {
			name := strings.ToLower(h.DomainNames[0])
			p, hasProv := provByHost[name]
			snippet := hasSnippet(h.AdvancedConfig)
			inOut := hasProv && ak.InOutpost(p.PK)
			hasApp := hasProv && p.AssignedAppSlug != ""
			switch {
			case snippet && hasProv && inOut && hasApp:
				v.Authentik = AKProtected
			case !snippet && !hasProv:
				v.Authentik = AKOpen
			default:
				v.Authentik = AKHalf
				var miss []string
				if !snippet {
					miss = append(miss, "forward auth in NPM")
				}
				if !hasProv {
					miss = append(miss, "proxy provider")
				} else {
					if !hasApp {
						miss = append(miss, "applicatie")
					}
					if !inOut {
						miss = append(miss, "outpost")
					}
				}
				v.AuthentikDetail = "ontbreekt: " + strings.Join(miss, ", ")
				add("authentik_half", "warn", "Authentik half ingesteld (%s)", strings.Join(miss, ", "))
			}
		}
		out = append(out, v)
	}
	return out
}

func driftText(field string, have, want any) string {
	h, _ := json.Marshal(have)
	w, _ := json.Marshal(want)
	if field == "advanced_config" {
		return "advanced_config is in NPM aangepast"
	}
	return fmt.Sprintf("%s: in NPM %s, gewenst %s", field, trimText(string(h), 80), trimText(string(w), 80))
}

func buildApps(npm NPMRead, ak AKRead) []AKAppView {
	hostByName := map[string]NPMHost{}
	for _, h := range npm.Hosts {
		for _, d := range h.DomainNames {
			hostByName[strings.ToLower(d)] = h
		}
	}
	provByPK := map[int]AKProxyProvider{}
	for _, p := range ak.Providers {
		provByPK[p.PK] = p
	}
	out := []AKAppView{}
	for _, a := range ak.Applications {
		v := AKAppView{Name: a.Name, Slug: a.Slug}
		if a.Provider != nil {
			p, ok := provByPK[*a.Provider]
			if !ok {
				// Geen proxy provider (OAuth2, SAML, ...): niet voor NPM.
				continue
			}
			v.Provider, v.Mode, v.External, v.InOutpost = p.Name, p.Mode, p.ExternalHost, ak.InOutpost(p.PK)
			if h, ok := hostByName[p.Host()]; ok {
				v.HostID, v.Snippet = h.ID, hasSnippet(h.AdvancedConfig)
			}
		} else {
			continue
		}
		switch {
		case v.HostID == 0 && v.Mode != "proxy":
			v.State, v.Detail = AKHalf, "geen proxy host in NPM voor "+v.External
		case v.HostID == 0:
			v.State, v.Detail = AKOpen, "proxy-modus (eigen outpost), geen host in NPM"
		case !v.InOutpost:
			v.State, v.Detail = AKHalf, "provider zit in geen enkele outpost"
		case !v.Snippet && v.Mode != "proxy":
			v.State, v.Detail = AKHalf, "de NPM-host heeft geen forward auth"
		default:
			v.State = AKProtected
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// notices maakt de meldingen voor Aandacht nodig.
func notices(ov *Overview) []Notice {
	var out []Notice
	href := "/publicatie/npm"
	group := func(code string) []string {
		var names []string
		for _, h := range ov.Hosts {
			for _, p := range h.Problems {
				if p.Code == code && len(h.Domains) > 0 {
					names = append(names, h.Domains[0])
				}
			}
		}
		return names
	}
	if ov.NPM.Linked && ov.NPM.Error != "" {
		out = append(out, Notice{ID: "publish-npm", Red: true, Title: "NPM niet te lezen", Detail: ov.NPM.Error, Href: href, Count: 1})
	}
	if ov.Authentik.Linked && ov.Authentik.Error != "" {
		out = append(out, Notice{ID: "publish-authentik", Red: true, Title: "Authentik niet te lezen", Detail: ov.Authentik.Error, Href: "/publicatie/authentik", Count: 1})
	}
	for _, g := range []struct {
		code, title string
		red         bool
	}{
		{"doel_weg", "Proxy host naar een dienst die niet draait", true},
		{"nginx_fout", "Proxy host met een nginx-fout", true},
		{"cert_verlopen", "Certificaat verlopen", true},
		{"cert_verloopt", "Certificaat verloopt binnenkort", false},
		{"naar_node", "Proxy host wijst naar één node in plaats van het VIP", false},
		{"oud_doel", "Proxy host wijst naar een oud adres (voorstel klaar)", false},
		{"domein_tikfout", "Proxy host onder een domein dat op het hoofddomein lijkt (tikfout?)", false},
		{"authentik_half", "Authentik half ingesteld", false},
	} {
		if names := group(g.code); len(names) > 0 {
			out = append(out, Notice{ID: "publish-" + g.code, Red: g.red, Title: g.title, Names: names, Href: href, Count: len(names)})
		}
	}
	var drift []string
	for _, h := range ov.Hosts {
		if len(h.Drift) > 0 && len(h.Domains) > 0 {
			drift = append(drift, h.Domains[0])
		}
	}
	if len(drift) > 0 {
		out = append(out, Notice{ID: "publish-drift", Title: "Beheerde proxy host buiten ClusterForge gewijzigd", Names: drift, Href: href, Count: len(drift)})
	}
	return out
}

// Notices geeft de meldingen voor Aandacht nodig.
func (s *Service) Notices(ctx context.Context) ([]Notice, error) {
	ov, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	return append(ov.Notices, s.feedNotices(ctx)...), nil
}
