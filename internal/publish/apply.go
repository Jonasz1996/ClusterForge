package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// KindApply is de taak die NPM en Authentik verandert.
const KindApply = "publish.apply"

type jobsDone = store.Job

// Acties.
const (
	// ActPublish maakt een nieuwe proxy host voor een dienst.
	ActPublish = "publish"
	// ActUpdate verandert een beheerde host (doel, opties).
	ActUpdate = "update"
	// ActRetarget zet het doel van een host op het VIP.
	ActRetarget = "retarget"
	// ActProtect zet een host achter Authentik.
	ActProtect = "protect"
	// ActUnprotect haalt Authentik weg.
	ActUnprotect = "unprotect"
	// ActDelete verwijdert een beheerde host.
	ActDelete = "delete"
	// ActRestore zet een beheerde host terug zoals ClusterForge hem wil (drift).
	ActRestore = "restore"
	// ActRename geeft beheerde hosts een naam onder een nieuw hoofddomein (M6).
	ActRename = "rename"
	// ActRetargetMany zet hosts op het voorgestelde nieuwe doel (fase 42);
	// Hosts leeg is alle hosts met een voorstel.
	ActRetargetMany = "retarget_many"
)

// Action is een vraag van de GUI.
type Action struct {
	Action string `json:"action"`
	HostID int    `json:"host_id,omitempty"`
	// ServiceKey is de dienst bij ActPublish.
	ServiceKey string `json:"service_key,omitempty"`
	// Label is het subdomein (homepage) of een volledige naam.
	Label         string `json:"label,omitempty"`
	ForwardScheme string `json:"forward_scheme,omitempty"`
	ForwardHost   string `json:"forward_host,omitempty"`
	ForwardPort   int    `json:"forward_port,omitempty"`
	// SSL is auto (passend certificaat of Let's Encrypt), none of het id
	// van een certificaat in NPM.
	SSL           string `json:"ssl,omitempty"`
	Websockets    *bool  `json:"websockets,omitempty"`
	BlockExploits *bool  `json:"block_exploits,omitempty"`
	HTTP2         *bool  `json:"http2,omitempty"`
	HSTS          *bool  `json:"hsts,omitempty"`
	Authentik     *bool  `json:"authentik,omitempty"`
	// AuthentikGroup is de groep van de applicatie in Authentik (alleen
	// voor de weergave in Authentik).
	AuthentikGroup string `json:"authentik_group,omitempty"`
	// Confirm is de overgetikte naam bij verwijderen.
	Confirm string `json:"confirm,omitempty"`
	// Hosts zijn de hosts bij ActRename; leeg is alle beheerde hosts onder
	// het oude domein.
	Hosts []int `json:"hosts,omitempty"`
	// OldDomain en NewDomain bij ActRename.
	OldDomain string `json:"old_domain,omitempty"`
	NewDomain string `json:"new_domain,omitempty"`
	// KeepOld: de oude naam blijft als extra naam op de host.
	KeepOld bool `json:"keep_old,omitempty"`
	// Fase 42, bij ActUpdate: eigen Advanced (zonder het blok van
	// ClusterForge, dat blijft staan), SSL afdwingen en de namen.
	AdvancedConfig *string  `json:"advanced_config,omitempty"`
	SSLForced      *bool    `json:"ssl_forced,omitempty"`
	Domains        []string `json:"domains,omitempty"`
	// Theme bij ActPublish: het sjabloon voor Advanced gebruiken (leeg =
	// ja als er een standaard-css is).
	Theme *bool `json:"theme,omitempty"`
}

// Plan is wat een actie zou doen, voor het voorbeeldscherm en de taak.
type Plan struct {
	Title string `json:"title"`
	// Lines zijn de stappen in gewone taal.
	Lines    []string `json:"lines"`
	Warnings []string `json:"warnings"`
	// Hosts zijn de hosts die veranderen (meer bij ActRename).
	Hosts []PlannedHost `json:"hosts"`
	// Confirm: de naam die overgetikt moet worden; leeg als het niet hoeft.
	Confirm string `json:"confirm,omitempty"`
}

// PlannedHost is één host in een plan.
type PlannedHost struct {
	HostID int    `json:"host_id"`
	Domain string `json:"domain"`
	// Body zijn de velden die NPM krijgt (bij maken alle, anders alleen wat
	// verandert); Old de waarden van nu, om terug te zetten.
	Body map[string]any `json:"body,omitempty"`
	Old  map[string]any `json:"old,omitempty"`
	// Cert: new vraagt een certificaat aan bij Let's Encrypt.
	CertNew bool `json:"cert_new,omitempty"`
	// Delete verwijdert de host.
	Delete bool `json:"delete,omitempty"`
	// Protect zet hem achter Authentik, Unprotect haalt dat weg.
	Protect   bool   `json:"protect,omitempty"`
	Unprotect bool   `json:"unprotect,omitempty"`
	AppName   string `json:"app_name,omitempty"`
	AppSlug   string `json:"app_slug,omitempty"`
	AppGroup  string `json:"app_group,omitempty"`
	Outpost   string `json:"outpost,omitempty"`
	// Advanced is de eigen Advanced van een nieuwe host (fase 42b). Met een
	// eigen forward auth erin komt hij pas na Authentik in NPM.
	Advanced string `json:"advanced,omitempty"`
	// AKUndo is wat ClusterForge eerder in Authentik maakte (bij weghalen).
	AKUndo *akCreated `json:"ak_undo,omitempty"`
	// Waar de dienst draait, voor publish_hosts.
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	NodeID      *uuid.UUID `json:"node_id,omitempty"`
	ServicePort int        `json:"service_port,omitempty"`
	// Expect is wat de controle van het adres moet zien.
	Scheme string `json:"scheme"`
	// ProviderHost: bij hernoemen krijgt de provider in Authentik deze naam.
	ProviderPK int `json:"provider_pk,omitempty"`
}

var (
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252})?$`)
)

// FullName maakt van een label of naam de volledige naam.
func FullName(label, domain string) (string, error) {
	label = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(label, ".")))
	switch {
	case label == "":
		return "", invalid("geef een naam voor het subdomein")
	case strings.Contains(label, "."):
		if !protocol.ValidDomain(label) || strings.HasPrefix(label, "*.") {
			return "", invalid("%q is geen geldige naam", label)
		}
		return label, nil
	case !labelRe.MatchString(label):
		return "", invalid("%q is geen geldig subdomein: kleine letters, cijfers en -", label)
	}
	return label + "." + domain, nil
}

// Snippet-markeringen: alles ertussen is van ClusterForge.
const (
	snippetBegin = "# clusterforge-authentik begin (beheerd door ClusterForge)"
	snippetEnd   = "# clusterforge-authentik eind"
)

var locationRoot = regexp.MustCompile(`(?m)^\s*location\s+/\s*\{`)

// AuthentikSnippet is de forward auth van Authentik voor NPM, zoals de
// documentatie van Authentik hem geeft, met de outpost als doel.
func AuthentikSnippet(outpost string) string {
	return snippetBegin + `
proxy_buffers 8 16k;
proxy_buffer_size 32k;
port_in_redirect off;

location / {
    proxy_pass          $forward_scheme://$server:$port;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $http_connection;
    proxy_http_version 1.1;

    auth_request     /outpost.goauthentik.io/auth/nginx;
    error_page       401 = @goauthentik_proxy_signin;
    auth_request_set $auth_cookie $upstream_http_set_cookie;
    add_header       Set-Cookie $auth_cookie;

    auth_request_set $authentik_username $upstream_http_x_authentik_username;
    auth_request_set $authentik_groups $upstream_http_x_authentik_groups;
    auth_request_set $authentik_entitlements $upstream_http_x_authentik_entitlements;
    auth_request_set $authentik_email $upstream_http_x_authentik_email;
    auth_request_set $authentik_name $upstream_http_x_authentik_name;
    auth_request_set $authentik_uid $upstream_http_x_authentik_uid;

    proxy_set_header X-authentik-username $authentik_username;
    proxy_set_header X-authentik-groups $authentik_groups;
    proxy_set_header X-authentik-entitlements $authentik_entitlements;
    proxy_set_header X-authentik-email $authentik_email;
    proxy_set_header X-authentik-name $authentik_name;
    proxy_set_header X-authentik-uid $authentik_uid;
}

location /outpost.goauthentik.io {
    proxy_pass              ` + outpost + `/outpost.goauthentik.io;
    proxy_set_header        Host $host;
    proxy_set_header        X-Original-URL $scheme://$http_host$request_uri;
    add_header              Set-Cookie $auth_cookie;
    auth_request_set        $auth_cookie $upstream_http_set_cookie;
    proxy_pass_request_body off;
    proxy_set_header        Content-Length "";
}

location @goauthentik_proxy_signin {
    internal;
    add_header Set-Cookie $auth_cookie;
    return 302 /outpost.goauthentik.io/start?rd=$scheme://$http_host$request_uri;
}
` + snippetEnd
}

// withoutSnippet haalt het blok van ClusterForge uit een config.
func withoutSnippet(adv string) string {
	i := strings.Index(adv, snippetBegin)
	if i < 0 {
		return adv
	}
	j := strings.Index(adv[i:], snippetEnd)
	if j < 0 {
		return adv
	}
	rest := strings.TrimLeft(adv[i+j+len(snippetEnd):], "\n")
	return strings.TrimRight(adv[:i], "\n") + joinNL(adv[:i], rest)
}

func joinNL(before, rest string) string {
	if strings.TrimSpace(before) != "" && rest != "" {
		return "\n" + rest
	}
	return rest
}

// withSnippet zet het blok van ClusterForge in een config.
func withSnippet(adv, outpost string) (string, error) {
	base := strings.TrimRight(withoutSnippet(adv), "\n")
	if locationRoot.MatchString(base) {
		return "", invalid("de host heeft al een eigen 'location /' in Advanced; forward auth van ClusterForge zou daarmee botsen. Haal die eerst weg in NPM of zet Authentik er met de hand op")
	}
	if strings.TrimSpace(base) == "" {
		return AuthentikSnippet(outpost), nil
	}
	return base + "\n\n" + AuthentikSnippet(outpost), nil
}

// outpostFor geeft het adres van de outpost: de instelling, anders het VIP
// van het Authentik-cluster.
func (s *Service) outpostFor(ctx context.Context, st Settings) (string, error) {
	if st.AuthentikOutpost != "" {
		return st.AuthentikOutpost, nil
	}
	if st.AuthentikClusterID != nil {
		cl, err := s.clusters(ctx)
		if err != nil {
			return "", err
		}
		for _, c := range cl {
			if c.ID == *st.AuthentikClusterID && len(c.VIPs) > 0 {
				return "http://" + c.VIPs[0].Address + ":" + strconv.Itoa(st.AuthentikPort), nil
			}
		}
	}
	if st.AuthentikNodeID != nil {
		nodes, err := s.nodes(ctx)
		if err != nil {
			return "", err
		}
		for _, n := range nodes {
			if n.ID == *st.AuthentikNodeID && n.PrimaryIP != "" {
				return "http://" + n.PrimaryIP + ":" + strconv.Itoa(st.AuthentikPort), nil
			}
		}
	}
	return "", invalid("vul in Instellingen → Domeinen en publicatie het adres van de Authentik-outpost in (zoals http://192.168.103.244:9000)")
}

func boolOr(p *bool, d bool) bool {
	if p != nil {
		return *p
	}
	return d
}

// Plan berekent wat een actie zou doen, zonder iets te veranderen.
func (s *Service) Plan(ctx context.Context, a Action) (Plan, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return Plan{}, err
	}
	if !st.NPMLinked() {
		return Plan{}, invalid("koppel eerst NPM in Instellingen → Domeinen en publicatie")
	}
	node, err := s.npmNode(ctx, st)
	if err != nil {
		return Plan{}, err
	}
	npm, err := s.readNPM(ctx, st)
	if err != nil {
		return Plan{}, err
	}
	managed, err := s.managed(ctx)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Lines: []string{}, Warnings: []string{}, Hosts: []PlannedHost{}}
	byID := map[int]NPMHost{}
	taken := map[string]int{}
	for _, h := range npm.Hosts {
		byID[h.ID] = h
		for _, d := range h.DomainNames {
			taken[strings.ToLower(d)] = h.ID
		}
	}
	host := func() (NPMHost, error) {
		h, ok := byID[a.HostID]
		if !ok {
			return NPMHost{}, ErrNotFound
		}
		return h, nil
	}
	akNeeded := false
	wantCertNew, renamePK := false, 0
	switch a.Action {
	case ActPublish:
		ph, lines, warns, err := s.planPublish(ctx, st, npm, taken, a)
		if err != nil {
			return Plan{}, err
		}
		p.Title = "Publiceren: " + ph.Domain
		p.Hosts = append(p.Hosts, ph)
		p.Lines, p.Warnings = append(p.Lines, lines...), append(p.Warnings, warns...)
		akNeeded = ph.Protect
	case ActUpdate, ActRetarget, ActRestore:
		h, err := host()
		if err != nil {
			return Plan{}, err
		}
		m, isManaged := managed[h.ID]
		have := h.fields()
		want := map[string]any{}
		switch a.Action {
		case ActRetarget:
			if a.ForwardHost == "" {
				return Plan{}, invalid("geen VIP om naar te wijzen")
			}
			want["forward_host"] = a.ForwardHost
			p.Title = "Doel naar het VIP: " + h.DomainNames[0]
		case ActRestore:
			if !isManaged {
				return Plan{}, invalid("ClusterForge beheert deze host niet; neem hem eerst over")
			}
			want = m.Desired
			p.Title = "Terugzetten zoals ClusterForge hem wil: " + h.DomainNames[0]
		default:
			if a.ForwardHost != "" {
				if !hostRe.MatchString(a.ForwardHost) {
					return Plan{}, invalid("%q is geen geldig doel", a.ForwardHost)
				}
				want["forward_host"] = a.ForwardHost
			}
			if a.ForwardPort != 0 {
				if a.ForwardPort < 1 || a.ForwardPort > 65535 {
					return Plan{}, invalid("ongeldige poort")
				}
				want["forward_port"] = a.ForwardPort
			}
			if a.ForwardScheme != "" {
				if a.ForwardScheme != "http" && a.ForwardScheme != "https" {
					return Plan{}, invalid("schema is http of https")
				}
				want["forward_scheme"] = a.ForwardScheme
			}
			if a.Websockets != nil {
				want["allow_websocket_upgrade"] = *a.Websockets
			}
			if a.BlockExploits != nil {
				want["block_exploits"] = *a.BlockExploits
			}
			if a.HTTP2 != nil {
				want["http2_support"] = *a.HTTP2
			}
			if a.HSTS != nil {
				want["hsts_enabled"] = *a.HSTS
			}
			if a.SSLForced != nil {
				want["ssl_forced"] = *a.SSLForced
			}
			if a.AdvancedConfig != nil {
				adv, err := keepSnippet(h.AdvancedConfig, *a.AdvancedConfig)
				if err != nil {
					return Plan{}, err
				}
				want["advanced_config"] = adv
			}
			if len(a.Domains) > 0 {
				names, err := s.planDomains(ctx, st, h, a.Domains, taken, &p, &renamePK)
				if err != nil {
					return Plan{}, err
				}
				want["domain_names"] = names
				// Dekt het certificaat de nieuwe naam niet, kies er dan een.
				if a.SSL == "" && h.CertID() > 0 {
					covered := false
					for _, c := range npm.Certs {
						if c.ID == h.CertID() && c.Covers(names[0]) {
							covered = true
						}
					}
					if !covered {
						a.SSL = "auto"
					}
				}
			}
			if a.SSL != "" {
				first := strings.ToLower(h.DomainNames[0])
				if names, ok := want["domain_names"].([]string); ok {
					first = names[0]
				}
				certID, certNew, line, warn, err := s.chooseCert(st, npm, first, a.SSL)
				if err != nil {
					return Plan{}, err
				}
				if line != "" {
					p.Lines = append(p.Lines, line)
				}
				if warn != "" {
					p.Warnings = append(p.Warnings, warn)
				}
				if certNew {
					wantCertNew = true
				} else {
					want["certificate_id"] = certID
				}
				if certID == 0 && !certNew {
					want["ssl_forced"], want["http2_support"], want["hsts_enabled"] = false, false, false
				}
			}
			p.Title = "Wijzigen: " + h.DomainNames[0]
		}
		body := diffFields(have, want)
		if wantCertNew {
			body["certificate_id"] = 0
		}
		if len(body) == 0 {
			return Plan{}, invalid("er verandert niets")
		}
		old := map[string]any{}
		for k := range body {
			old[k] = have[k]
			p.Lines = append(p.Lines, fmt.Sprintf("%s: %s → %s", k, jsonText(have[k]), jsonText(body[k])))
		}
		ph := PlannedHost{HostID: h.ID, Domain: strings.ToLower(h.DomainNames[0]), Body: body, Old: old, Scheme: schemeOf(h),
			CertNew: wantCertNew, ProviderPK: renamePK}
		if names, ok := body["domain_names"].([]string); ok {
			ph.Domain = names[0]
		}
		if id, ok := body["certificate_id"].(int); ok {
			ph.Scheme = "http"
			if id > 0 || wantCertNew {
				ph.Scheme = "https"
			}
		}
		if isManaged {
			ph.ClusterID, ph.NodeID = m.ClusterID, m.NodeID
			if m.ServicePort != nil {
				ph.ServicePort = *m.ServicePort
			}
		}
		p.Hosts = append(p.Hosts, ph)
	case ActProtect, ActUnprotect:
		h, err := host()
		if err != nil {
			return Plan{}, err
		}
		if !st.AuthentikLinked() {
			return Plan{}, invalid("koppel eerst Authentik in Instellingen → Domeinen en publicatie")
		}
		akNeeded = true
		name := strings.ToLower(h.DomainNames[0])
		ph := PlannedHost{HostID: h.ID, Domain: name, Scheme: schemeOf(h), Old: map[string]any{"advanced_config": h.AdvancedConfig}}
		if a.Action == ActProtect {
			outpost, err := s.outpostFor(ctx, st)
			if err != nil {
				return Plan{}, err
			}
			adv, err := s.protectAdvanced(ctx, h, name, outpost)
			if err != nil {
				return Plan{}, err
			}
			label := strings.TrimSuffix(name, "."+st.Domain)
			ph.Protect, ph.Outpost, ph.Body = true, outpost, map[string]any{"advanced_config": adv}
			ph.AppName, ph.AppSlug, ph.AppGroup = appName(label), slugFor(label), strings.TrimSpace(a.AuthentikGroup)
			p.Title = "Achter Authentik zetten: " + name
			p.Lines = append(p.Lines,
				fmt.Sprintf("Authentik: proxy provider (forward auth, één domein) voor https://%s, tenzij die er al is", name),
				fmt.Sprintf("Authentik: applicatie %q (%s)", ph.AppName, ph.AppSlug),
				"Authentik: provider in de ingebouwde outpost",
				"NPM: forward auth in Advanced, naar de outpost "+outpost)
		} else {
			if !hasSnippet(h.AdvancedConfig) {
				return Plan{}, invalid("deze host heeft geen forward auth naar Authentik")
			}
			tb := themeBlock(h.AdvancedConfig)
			if !strings.Contains(h.AdvancedConfig, snippetBegin) && !hasSnippet(tb) {
				return Plan{}, invalid("de forward auth van deze host is niet door ClusterForge gezet; haal hem in NPM weg (Advanced)")
			}
			adv := withoutSnippet(h.AdvancedConfig)
			if tb != "" {
				t, err := s.Theme(ctx)
				if err != nil {
					return Plan{}, err
				}
				if themed, _, ok := t.themedAdvanced(h.AdvancedConfig, h.ID, name, false, ""); ok {
					adv = themed
					p.Lines = append(p.Lines, "NPM: sjabloon zonder Authentik in Advanced")
				} else {
					adv = ownAdvanced(h.AdvancedConfig)
				}
			}
			ph.Unprotect, ph.Body = true, map[string]any{"advanced_config": adv}
			p.Title = "Authentik weghalen: " + name
			p.Lines = append(p.Lines, "NPM: forward auth uit Advanced")
			if m, ok := managed[h.ID]; ok && m.AKCreated {
				undo := akCreated{OutpostAdded: true}
				if m.AKProvider != nil {
					undo.ProviderPK, undo.ProviderCreated = *m.AKProvider, true
				}
				if m.AKAppSlug != nil {
					undo.AppSlug = *m.AKAppSlug
				}
				ph.AKUndo = &undo
				p.Lines = append(p.Lines, "Authentik: applicatie, provider en plaats in de outpost weg (ClusterForge maakte ze)")
			} else {
				p.Warnings = append(p.Warnings, "ClusterForge maakte de applicatie en provider in Authentik niet; die blijven staan")
			}
		}
		p.Hosts = append(p.Hosts, ph)
	case ActDelete:
		h, err := host()
		if err != nil {
			return Plan{}, err
		}
		m, ok := managed[h.ID]
		if !ok {
			return Plan{}, invalid("ClusterForge verwijdert alleen hosts die het beheert; neem hem eerst over of verwijder hem in NPM")
		}
		name := strings.ToLower(h.DomainNames[0])
		ph := PlannedHost{HostID: h.ID, Domain: name, Delete: true, Old: h.fields(), Scheme: schemeOf(h)}
		p.Title, p.Confirm = "Verwijderen: "+name, name
		p.Lines = append(p.Lines, "NPM: proxy host "+name+" verwijderen (de kopie van vooraf blijft op de NPM-node)")
		if m.AKCreated && st.AuthentikLinked() {
			undo := akCreated{OutpostAdded: true}
			if m.AKProvider != nil {
				undo.ProviderPK, undo.ProviderCreated = *m.AKProvider, true
			}
			if m.AKAppSlug != nil {
				undo.AppSlug = *m.AKAppSlug
			}
			ph.AKUndo = &undo
			akNeeded = true
			p.Lines = append(p.Lines, "Authentik: applicatie en provider van ClusterForge weg")
		}
		p.Hosts = append(p.Hosts, ph)
	case ActRetargetMany:
		if err := s.planRetargetMany(ctx, st, npm, managed, a, &p); err != nil {
			return Plan{}, err
		}
	case ActAKRepair:
		if err := s.planAKRepair(ctx, st, managed, a, &p); err != nil {
			return Plan{}, err
		}
		akNeeded = true
	case ActThemeMany:
		if err := s.planThemeMany(ctx, st, npm, managed, a, &p); err != nil {
			return Plan{}, err
		}
	case ActMoveAddress:
		if err := s.planMoveAddress(ctx, st, npm, managed, a, &p); err != nil {
			return Plan{}, err
		}
	case ActRename:
		if err := s.planRename(ctx, st, npm, managed, taken, a, &p); err != nil {
			return Plan{}, err
		}
		akNeeded = st.AuthentikLinked()
	default:
		return Plan{}, invalid("onbekende actie %q", a.Action)
	}
	if akNeeded {
		if _, err := s.akNode(ctx, st); err != nil {
			return Plan{}, err
		}
	}
	p.Lines = append([]string{fmt.Sprintf("Kopie van de NPM-database en nginx-config op %s", node.Hostname)}, p.Lines...)
	p.Lines = append(p.Lines, "Controle: database, config, geen .err, nginx -t en het adres zelf; faalt er een, dan gaat alles terug")
	if st.NPMClusterID != nil {
		p.Lines = append(p.Lines, "Daarna meteen de sync NPM-configuratie naar de andere nodes")
	}
	return p, nil
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return trimText(string(b), 80)
}

func schemeOf(h NPMHost) string {
	if h.CertID() > 0 {
		return "https"
	}
	return "http"
}

func appName(label string) string {
	if label == "" {
		return "App"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func slugFor(label string) string {
	s := labelClean.ReplaceAllString(strings.ToLower(strings.ReplaceAll(label, ".", "-")), "-")
	return strings.Trim(s, "-")
}

// planPublish rekent een nieuwe host uit.
func (s *Service) planPublish(ctx context.Context, st Settings, npm *NPMRead, taken map[string]int, a Action) (PlannedHost, []string, []string, error) {
	var lines, warns []string
	ov, err := s.Overview(ctx)
	if err != nil {
		return PlannedHost{}, nil, nil, err
	}
	var svc *ServiceRow
	for i := range ov.Services {
		if ov.Services[i].Key == a.ServiceKey {
			svc = &ov.Services[i]
		}
	}
	if svc == nil && a.ForwardHost == "" {
		return PlannedHost{}, nil, nil, invalid("kies een dienst of geef een doel")
	}
	label := a.Label
	if label == "" && svc != nil {
		label = svc.Label
	}
	name, err := FullName(label, st.Domain)
	if err != nil {
		return PlannedHost{}, nil, nil, err
	}
	if id, ok := taken[name]; ok {
		return PlannedHost{}, nil, nil, invalid("%s bestaat al in NPM (host %d); wijzig die host", name, id)
	}
	ph := PlannedHost{Domain: name}
	scheme, fhost, fport := a.ForwardScheme, a.ForwardHost, a.ForwardPort
	if svc != nil {
		if scheme == "" {
			scheme = svc.Kind
		}
		if fhost == "" {
			fhost = svc.Target
		}
		if fport == 0 {
			fport = svc.Port
		}
		ph.ClusterID, ph.ServicePort = svc.ClusterID, svc.Port
		if len(svc.Nodes) == 1 && !svc.TargetVIP {
			ph.NodeID = &svc.Nodes[0].ID
		}
		if !svc.Web {
			warns = append(warns, fmt.Sprintf("ClusterForge zag op poort %d geen webdienst (%s); controleer het doel", svc.Port, svc.Kind))
		}
	}
	if scheme != "https" {
		scheme = "http"
	}
	if !hostRe.MatchString(fhost) || fport < 1 || fport > 65535 {
		return PlannedHost{}, nil, nil, invalid("ongeldig doel %s:%d", fhost, fport)
	}
	d := st.Defaults
	body := map[string]any{
		"domain_names": []string{name}, "forward_scheme": scheme, "forward_host": fhost, "forward_port": fport,
		"certificate_id": 0, "ssl_forced": false, "http2_support": false, "hsts_enabled": false, "hsts_subdomains": false,
		"block_exploits": boolOr(a.BlockExploits, d.BlockExploits), "caching_enabled": false,
		"allow_websocket_upgrade": boolOr(a.Websockets, d.Websockets), "access_list_id": 0, "advanced_config": "",
		"locations": []any{}, "meta": map[string]any{},
	}
	lines = append(lines, fmt.Sprintf("NPM: proxy host %s → %s://%s:%d", name, scheme, fhost, fport))
	ssl := a.SSL
	if ssl == "" {
		ssl = "auto"
	}
	certID := 0
	switch ssl {
	case "none":
	case "auto":
		if c := pickCert(npm.Certs, name, s.Now()); c != nil {
			certID = c.ID
			lines = append(lines, fmt.Sprintf("Certificaat: bestaand %s (%s, geldig tot %s)", certLabel(*c), c.Provider, c.Expires().Format("2006-01-02")))
		} else if d.LetsEncrypt {
			ph.CertNew = true
			lines = append(lines, "Certificaat: nieuw bij Let's Encrypt voor "+name+" (HTTP-challenge: de naam moet publiek naar NPM wijzen)")
		} else {
			warns = append(warns, "geen certificaat voor "+name+" gevonden en Let's Encrypt staat uit: alleen http")
		}
	default:
		id, err := strconv.Atoi(ssl)
		if err != nil {
			return PlannedHost{}, nil, nil, invalid("ongeldig certificaat %q", ssl)
		}
		found := false
		for _, c := range npm.Certs {
			if c.ID == id {
				found = true
				if !c.Covers(name) {
					warns = append(warns, fmt.Sprintf("certificaat %s dekt %s niet", certLabel(c), name))
				}
				lines = append(lines, "Certificaat: "+certLabel(c))
			}
		}
		if !found {
			return PlannedHost{}, nil, nil, invalid("certificaat %d bestaat niet in NPM", id)
		}
		certID = id
	}
	if certID > 0 || ph.CertNew {
		body["certificate_id"] = certID
		body["ssl_forced"] = d.SSLForced
		body["http2_support"] = boolOr(a.HTTP2, d.HTTP2)
		body["hsts_enabled"] = boolOr(a.HSTS, d.HSTS)
		ph.Scheme = "https"
	} else {
		ph.Scheme = "http"
	}
	ph.Body = body
	protect := false
	if svc != nil {
		protect = svc.AuthentikDefault
	}
	if a.Authentik != nil {
		protect = *a.Authentik
	}
	own := ""
	if a.AdvancedConfig != nil {
		var err error
		if own, err = keepSnippet("", *a.AdvancedConfig); err != nil {
			return PlannedHost{}, nil, nil, err
		}
	}
	manual := hasSnippet(own)
	th, err := s.Theme(ctx)
	if err != nil {
		return PlannedHost{}, nil, nil, err
	}
	css := ""
	if a.Theme == nil || *a.Theme {
		css = th.cssFor(0, name)
	}
	if css != "" && (locationRoot.MatchString(own) || manual) {
		warns = append(warns, "je eigen Advanced heeft een 'location /' of eigen forward auth; het sjabloon (thema) blijft daarom weg")
		css = ""
	}
	protectOK := protect && st.AuthentikLinked()
	switch {
	case css != "":
		body["advanced_config"] = joinBlocks(own, th.render(false, name, css, ""))
		lines = append(lines, "Sjabloon voor Advanced met css "+css)
		if protectOK {
			outpost, err := s.outpostFor(ctx, st)
			if err != nil {
				return PlannedHost{}, nil, nil, err
			}
			ph.Advanced = joinBlocks(own, th.render(true, name, css, outpost))
		}
	case own == "":
	case protectOK && !manual && locationRoot.MatchString(own):
		return PlannedHost{}, nil, nil, invalid("je eigen Advanced heeft een 'location /'; die botst met de forward auth van ClusterForge. Laat 'location /' weg of zet hem in het sjabloon (Publicatie → Proxy hosts → Sjabloon)")
	case protectOK && manual:
		ph.Advanced = own
		lines = append(lines, "Eigen Advanced met eigen forward auth: komt in NPM nadat Authentik de provider heeft")
	case manual:
		body["advanced_config"] = own
		warns = append(warns, "je eigen Advanced gebruikt de outpost van Authentik, maar \"Achter Authentik\" staat uit: zonder provider voor deze naam geeft de outpost een fout")
		lines = append(lines, "Eigen Advanced (nginx) meegegeven")
	default:
		body["advanced_config"] = own
		ph.Advanced = own
		lines = append(lines, "Eigen Advanced (nginx) meegegeven")
	}
	if protect {
		if !st.AuthentikLinked() {
			warns = append(warns, "Authentik is niet gekoppeld: de host komt er zonder login")
		} else {
			outpost, err := s.outpostFor(ctx, st)
			if err != nil {
				return PlannedHost{}, nil, nil, err
			}
			l := strings.TrimSuffix(name, "."+st.Domain)
			ph.Protect, ph.Outpost = true, outpost
			ph.AppName, ph.AppSlug, ph.AppGroup = appName(l), slugFor(l), strings.TrimSpace(a.AuthentikGroup)
			lines = append(lines, fmt.Sprintf("Authentik: provider, applicatie %q en outpost; daarna forward auth in NPM", ph.AppName))
		}
	} else {
		lines = append(lines, "Zonder Authentik (de dienst heeft een eigen login of je koos zo)")
	}
	return ph, lines, warns, nil
}

func certLabel(c NPMCert) string {
	if c.NiceName != "" {
		return c.NiceName
	}
	return strings.Join(c.DomainNames, ", ")
}

// planRename rekent het omzetten naar een nieuw hoofddomein uit.
func (s *Service) planRename(ctx context.Context, st Settings, npm *NPMRead, managed map[int]managedHost, taken map[string]int, a Action, p *Plan) error {
	oldD, newD := strings.ToLower(a.OldDomain), strings.ToLower(a.NewDomain)
	if oldD == "" {
		oldD = st.Domain
	}
	if !protocol.ValidDomain(oldD) || !protocol.ValidDomain(newD) || oldD == newD {
		return invalid("geef het oude en het nieuwe domein (verschillend)")
	}
	var ak AKRead
	if st.AuthentikLinked() {
		if r, err := s.readAuthentik(ctx, st); err == nil {
			ak = *r
		} else {
			return err
		}
	}
	p.Title = fmt.Sprintf("Hoofddomein %s → %s", oldD, newD)
	for _, h := range npm.Hosts {
		if _, ok := managed[h.ID]; !ok {
			continue
		}
		if len(a.Hosts) > 0 && !slices.Contains(a.Hosts, h.ID) {
			continue
		}
		cur := strings.ToLower(h.DomainNames[0])
		if !strings.HasSuffix(cur, "."+oldD) {
			continue
		}
		name := strings.TrimSuffix(cur, "."+oldD) + "." + newD
		if id, ok := taken[name]; ok && id != h.ID {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s overgeslagen: %s bestaat al (host %d)", cur, name, id))
			continue
		}
		names := []string{name}
		if a.KeepOld {
			names = append(names, cur)
		}
		body := map[string]any{"domain_names": names}
		ph := PlannedHost{HostID: h.ID, Domain: name, Old: map[string]any{"domain_names": h.DomainNames}, Scheme: schemeOf(h)}
		if h.CertID() > 0 {
			if c := pickCert(npm.Certs, name, s.Now()); c != nil && (!a.KeepOld || c.Covers(cur)) {
				body["certificate_id"] = c.ID
			} else if st.Defaults.LetsEncrypt && !a.KeepOld {
				ph.CertNew = true
			} else {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s: geen certificaat dat %s dekt; de host houdt zijn huidige certificaat", cur, strings.Join(names, " en ")))
			}
			ph.Old["certificate_id"] = h.CertID()
		}
		for _, pr := range ak.Providers {
			if pr.Host() == cur {
				ph.ProviderPK = pr.PK
			}
		}
		ph.Body = body
		p.Hosts = append(p.Hosts, ph)
		line := fmt.Sprintf("%s → %s", cur, name)
		if a.KeepOld {
			line += " (oude naam blijft als extra naam)"
		}
		if ph.ProviderPK > 0 {
			line += "; provider in Authentik krijgt de nieuwe naam"
		}
		p.Lines = append(p.Lines, line)
	}
	if len(p.Hosts) == 0 {
		return invalid("geen beheerde hosts onder %s om om te zetten", oldD)
	}
	return nil
}

// applyParams zijn de parameters van de taak.
type applyParams struct {
	Action Action `json:"action"`
	Plan   Plan   `json:"plan"`
}

// Start zet een actie als taak in de wachtrij.
func (s *Service) Start(ctx context.Context, actor events.Actor, a Action) (store.Job, Plan, error) {
	p, err := s.Plan(ctx, a)
	if err != nil {
		return store.Job{}, Plan{}, err
	}
	if p.Confirm != "" && !strings.EqualFold(strings.TrimSpace(a.Confirm), p.Confirm) {
		return store.Job{}, p, invalid("tik %s over om te bevestigen", p.Confirm)
	}
	st, err := s.Settings(ctx)
	if err != nil {
		return store.Job{}, Plan{}, err
	}
	if busy, err := s.applyBusy(ctx); err != nil {
		return store.Job{}, p, err
	} else if busy != nil {
		return store.Job{}, p, *busy
	}
	spec := jobs.Spec{Kind: KindApply, Title: p.Title, Actor: actor, ClusterID: st.NPMClusterID, Params: applyParams{Action: a, Plan: p}}
	var j store.Job
	if st.NPMClusterID != nil {
		j, err = s.jobs.EnqueueForCluster(ctx, spec)
	} else {
		if st.NPMNodeID != nil {
			spec.NodeID = st.NPMNodeID
		}
		j, err = s.jobs.Enqueue(ctx, spec)
	}
	if err != nil {
		return store.Job{}, p, err
	}
	domains := make([]string, 0, len(p.Hosts))
	for _, h := range p.Hosts {
		domains = append(domains, h.Domain)
	}
	s.event(ctx, actor, "publish.requested", map[string]any{"action": a.Action, "title": p.Title, "domains": domains, "job_id": j.ID.String()})
	return j, p, nil
}

// runApply voert een actie uit, host voor host.
func (s *Service) runApply(ctx context.Context, j *jobs.Job) error {
	var p applyParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	var node nodeRow
	var snapshot string
	if err := j.Step(ctx, "Kopie van NPM", func(ctx context.Context, step *jobs.Step) error {
		node, err = s.npmNode(ctx, st)
		if err != nil {
			return err
		}
		res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMSnapshot})
		if err != nil {
			return err
		}
		snapshot = res.NPM.Snapshot
		step.Logf("Kopie %s op %s (container %s, %s)", snapshot, node.Hostname, res.NPM.Container, res.NPM.DataDir)
		for _, c := range res.NPM.Checks {
			step.Logf("✓ %s", c.Detail)
		}
		return step.SetState(ctx, map[string]string{"snapshot": snapshot, "node": node.ID.String()})
	}); err != nil {
		return err
	}
	if snapshot == "" {
		// Hervat na een herstart: de kopie staat in de vorige stap.
		var prev struct{ Snapshot, Node string }
		j.Done("Kopie van NPM", &prev)
		snapshot = prev.Snapshot
		if node, err = s.npmNode(ctx, st); err != nil {
			return err
		}
	}
	if (p.Action.Action == ActRetargetMany || p.Action.Action == ActThemeMany || p.Action.Action == ActAKRepair || p.Action.Action == ActMoveAddress) && len(p.Plan.Hosts) > 1 {
		return s.runMany(ctx, j, st, node, snapshot, p)
	}
	for i := range p.Plan.Hosts {
		if err := s.applyHost(ctx, j, st, node, snapshot, p.Action, &p.Plan.Hosts[i]); err != nil {
			return err
		}
	}
	return s.syncNow(ctx, j, st)
}

// runMany doet veel hosts na elkaar, elk met een eigen kopie: mislukt er
// een, dan gaat alleen die terug en gaan de andere door.
func (s *Service) runMany(ctx context.Context, j *jobs.Job, st Settings, node nodeRow, snapshot string, p applyParams) error {
	var failed []string
	for i := range p.Plan.Hosts {
		ph := &p.Plan.Hosts[i]
		snap := snapshot
		if i > 0 {
			if err := j.Step(ctx, "Kopie van NPM: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
				res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMSnapshot})
				if err != nil {
					return err
				}
				snap = res.NPM.Snapshot
				step.Logf("Kopie %s", snap)
				return nil
			}); err != nil {
				failed = append(failed, ph.Domain+": geen kopie, overgeslagen")
				continue
			}
		}
		if err := s.applyHost(ctx, j, st, node, snap, p.Action, ph); err != nil {
			failed = append(failed, err.Error())
		}
	}
	if err := s.syncNow(ctx, j, st); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d van %d mislukt (die staan terug zoals ze waren, de andere zijn aangepast): %s",
			len(failed), len(p.Plan.Hosts), strings.Join(failed, "; "))
	}
	return nil
}

func (s *Service) web(ctx context.Context, node nodeRow, req protocol.WebRequest) (protocol.WebResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, protocol.WebRequestTimeout)
	defer cancel()
	res, err := s.bus.Web(cctx, node.ID, req)
	if err != nil {
		return res, fmt.Errorf("%s: %w", node.Hostname, err)
	}
	if res.Error != "" {
		return res, fmt.Errorf("%s: %s", node.Hostname, res.Error)
	}
	return res, nil
}

// applyHost doet de wijziging aan één host met controle en terugdraaien.
func (s *Service) applyHost(ctx context.Context, j *jobs.Job, st Settings, node nodeRow, snapshot string, a Action, ph *PlannedHost) error {
	created := false
	var ak akCreated
	var hostID = ph.HostID
	// 1. De wijziging.
	err := j.Step(ctx, "NPM: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
		if ph.CertNew {
			c, err := s.npmRequestCert(ctx, st, node, ph.Domain)
			if err != nil {
				return fmt.Errorf("%w (kies een bestaand certificaat of zonder SSL)", err)
			}
			ph.Body["certificate_id"] = c.ID
			step.Logf("Certificaat %d van Let's Encrypt voor %s, geldig tot %s", c.ID, ph.Domain, c.Expires().Format("2006-01-02"))
		}
		switch {
		case ph.Delete:
			if _, err := s.npmWrite(ctx, st, node, hostID, nil, true); err != nil {
				return err
			}
			step.Logf("Host %d (%s) verwijderd", hostID, ph.Domain)
		case hostID == 0:
			h, err := s.npmWrite(ctx, st, node, 0, ph.Body, false)
			if err != nil {
				return err
			}
			hostID, created = h.ID, true
			ph.HostID = h.ID
			step.Logf("Host %d gemaakt: %s → %v://%v:%v", h.ID, ph.Domain, ph.Body["forward_scheme"], ph.Body["forward_host"], ph.Body["forward_port"])
		case ph.Protect:
			// Eerst Authentik, dan pas NPM: zonder provider zou de outpost
			// elke aanvraag weigeren.
			akNode, err := s.akNode(ctx, st)
			if err != nil {
				return err
			}
			ak, err = s.akProtect(ctx, st, akNode, ph.AppName, ph.AppSlug, ph.Domain, ph.AppGroup, step.Logf)
			if err != nil {
				if uerr := s.akUndo(ctx, st, akNode, ak, step.Logf); uerr != nil {
					step.Logf("Opruimen in Authentik mislukt: %v", uerr)
				}
				return err
			}
			if _, err := s.npmWrite(ctx, st, node, hostID, ph.Body, false); err != nil {
				if uerr := s.akUndo(ctx, st, akNode, ak, step.Logf); uerr != nil {
					step.Logf("Opruimen in Authentik mislukt: %v", uerr)
				}
				return err
			}
			step.Logf("Forward auth in NPM gezet (outpost %s)", ph.Outpost)
		default:
			if ph.ProviderPK > 0 && st.AuthentikLinked() {
				if akNode, err := s.akNode(ctx, st); err == nil {
					status, data, err := s.akCall(ctx, st, akNode, http.MethodPatch, fmt.Sprintf("/providers/proxy/%d/", ph.ProviderPK), map[string]any{"external_host": "https://" + ph.Domain})
					if err == nil && status >= 300 {
						err = fmt.Errorf("%s", akMessage(status, data))
					}
					if err != nil {
						return fmt.Errorf("provider in Authentik hernoemen: %w", err)
					}
					step.Logf("Provider %d in Authentik: https://%s", ph.ProviderPK, ph.Domain)
				}
			}
			if _, err := s.npmWrite(ctx, st, node, hostID, ph.Body, false); err != nil {
				return err
			}
			for k, v := range ph.Body {
				step.Logf("%s → %s", k, jsonText(v))
			}
		}
		return nil
	})
	if err != nil {
		// NPM weigerde: er veranderde niets in NPM (NPM bewaart pas na een
		// geldige vraag). Toch nakijken of nginx goed staat.
		return err
	}
	// 2. De controles.
	checkErr := j.Step(ctx, "Controle: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
		return s.verify(ctx, step, st, node, hostID, ph, ph.Protect)
	})
	if checkErr == nil && created && ph.Protect {
		// Een nieuwe host achter Authentik: eerst gewoon (doel antwoordt),
		// dan Authentik ervoor.
		checkErr = j.Step(ctx, "Authentik: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
			akNode, err := s.akNode(ctx, st)
			if err != nil {
				return err
			}
			ak, err = s.akProtect(ctx, st, akNode, ph.AppName, ph.AppSlug, ph.Domain, ph.AppGroup, step.Logf)
			if err != nil {
				return err
			}
			adv := ph.Advanced
			if !hasSnippet(adv) {
				if adv, err = withSnippet(ph.Advanced, ph.Outpost); err != nil {
					return err
				}
			}
			if _, err := s.npmWrite(ctx, st, node, hostID, map[string]any{"advanced_config": adv}, false); err != nil {
				return err
			}
			step.Logf("Forward auth in NPM gezet (outpost %s)", ph.Outpost)
			return s.verify(ctx, step, st, node, hostID, ph, true)
		})
	}
	if checkErr != nil {
		_ = j.Step(ctx, "Terugdraaien: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
			return s.rollback(ctx, step, st, node, snapshot, hostID, created, ph, ak)
		})
		return fmt.Errorf("%s: controle mislukt, teruggedraaid: %w", ph.Domain, checkErr)
	}
	// 3. Onthouden.
	return s.remember(ctx, j, st, node, a, hostID, created, ph, ak)
}

// verify doet de vijf controles.
func (s *Service) verify(ctx context.Context, step *jobs.Step, st Settings, node nodeRow, hostID int, ph *PlannedHost, protected bool) error {
	// NPM schrijft de config net na zijn antwoord; even wachten.
	time.Sleep(s.settle())
	res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMCheck, NPM: &protocol.WebNPM{HostID: hostID, Domain: firstName(ph), Absent: ph.Delete}})
	if err != nil {
		return err
	}
	var failed []string
	for _, c := range res.NPM.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
			failed = append(failed, c.Name+": "+c.Detail)
		}
		step.Logf("%s %s: %s", mark, c.Name, c.Detail)
	}
	if ph.Delete {
		if len(failed) > 0 {
			return errors.New(strings.Join(failed, "; "))
		}
		return nil
	}
	if h, err := s.npmHost(ctx, st, node, hostID); err != nil {
		failed = append(failed, "host teruglezen: "+err.Error())
	} else if h == nil {
		failed = append(failed, fmt.Sprintf("host %d bestaat niet in NPM", hostID))
	} else {
		var diff []string
		have := h.fields()
		for k, v := range ph.Body {
			if !slices.Contains(npmHostFields, k) {
				continue
			}
			if k == "domain_names" {
				// NPM zet de namen op alfabet.
				if !sameValue(sortedNames(have[k]), sortedNames(v)) {
					diff = append(diff, k)
				}
				continue
			}
			if !sameValue(have[k], v) {
				diff = append(diff, k)
			}
		}
		switch {
		case len(diff) > 0:
			failed = append(failed, "NPM gaf andere waarden terug voor "+strings.Join(diff, ", "))
			step.Logf("✗ API: andere waarden voor %s", strings.Join(diff, ", "))
		case h.Meta.NginxOnline != nil && !*h.Meta.NginxOnline:
			msg := ""
			if h.Meta.NginxErr != nil {
				msg = *h.Meta.NginxErr
			}
			failed = append(failed, "NPM meldt een nginx-fout: "+trimText(msg, 200))
			step.Logf("✗ API: nginx-fout %s", trimText(msg, 200))
		default:
			step.Logf("✓ API: host %d teruggelezen, waarden kloppen", hostID)
		}
	}
	// Het adres zelf, op de NPM-node via 127.0.0.1.
	url := ph.Scheme + "://" + firstName(ph) + "/"
	cr, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: url, Connect: "127.0.0.1"}})
	switch {
	case err != nil:
		failed = append(failed, "adres: "+err.Error())
	case cr.Check.Error != "":
		failed = append(failed, "adres: "+cr.Check.Error)
		step.Logf("✗ %s: %s", url, cr.Check.Error)
	default:
		c := cr.Check
		if c.CertError != "" {
			step.Logf("! certificaat: %s (geen reden om terug te draaien)", trimText(c.CertError, 160))
		}
		switch {
		case c.Status >= 500:
			failed = append(failed, fmt.Sprintf("adres: %s gaf HTTP %d (doel niet bereikbaar?)", url, c.Status))
			step.Logf("✗ %s: HTTP %d", url, c.Status)
		case protected && !strings.Contains(c.Location, "outpost.goauthentik.io") && !strings.Contains(c.Location, "/application/o/") &&
			c.Status != http.StatusUnauthorized:
			failed = append(failed, fmt.Sprintf("adres: %s stuurt niet door naar Authentik (HTTP %d %s)", url, c.Status, c.Location))
			step.Logf("✗ %s: HTTP %d zonder doorsturen naar Authentik", url, c.Status)
		default:
			extra := ""
			if c.Location != "" {
				extra = " → " + trimText(c.Location, 120)
			}
			step.Logf("✓ %s: HTTP %d%s", url, c.Status, extra)
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

func firstName(ph *PlannedHost) string {
	if names, ok := ph.Body["domain_names"].([]string); ok && len(names) > 0 {
		return names[0]
	}
	if names, ok := ph.Body["domain_names"].([]any); ok && len(names) > 0 {
		if s, ok := names[0].(string); ok {
			return s
		}
	}
	return ph.Domain
}

// settle is de wachttijd na een wijziging; tests zetten hem op nul.
func (s *Service) settle() time.Duration {
	if s.SettleTime != nil {
		return *s.SettleTime
	}
	return 2 * time.Second
}

// rollback zet een host terug: eerst via de API, en als dat niet lukt of
// nginx daarna nog niet goed is, met de kopie van vooraf.
func (s *Service) rollback(ctx context.Context, step *jobs.Step, st Settings, node nodeRow, snapshot string, hostID int, created bool,
	ph *PlannedHost, ak akCreated) error {
	var apiErr error
	switch {
	case created:
		_, apiErr = s.npmWrite(ctx, st, node, hostID, nil, true)
		if apiErr == nil {
			step.Logf("Nieuwe host %d weer verwijderd", hostID)
		}
	case ph.Delete:
		// Een verwijderde host komt terug met de kopie.
		apiErr = errors.New("een verwijderde host komt terug met de kopie")
	default:
		_, apiErr = s.npmWrite(ctx, st, node, hostID, ph.Old, false)
		if apiErr == nil {
			step.Logf("Host %d terug naar de vorige waarden", hostID)
		}
	}
	if ak.ProviderCreated || ak.AppSlug != "" || ak.OutpostAdded {
		if akNode, err := s.akNode(ctx, st); err == nil {
			if err := s.akUndo(ctx, st, akNode, ak, step.Logf); err != nil {
				step.Logf("Opruimen in Authentik mislukt: %v", err)
			}
		}
	}
	if apiErr == nil {
		time.Sleep(s.settle())
		res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMCheck, NPM: &protocol.WebNPM{}})
		if err == nil && res.NPM.AllOK() {
			step.Logf("✓ nginx -t en database in orde na het terugdraaien")
			return nil
		}
		if err != nil {
			apiErr = err
		} else {
			apiErr = fmt.Errorf("na het terugdraaien via de API is nginx of de database nog niet in orde")
		}
	}
	step.Logf("Terugdraaien via de API volstaat niet (%v); de kopie %s gaat terug", apiErr, snapshot)
	if snapshot == "" {
		return errors.New("geen kopie om terug te zetten")
	}
	res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMRestore, NPM: &protocol.WebNPM{Snapshot: snapshot}})
	if err != nil {
		return err
	}
	for _, c := range res.NPM.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		step.Logf("%s %s: %s", mark, c.Name, c.Detail)
	}
	if !res.NPM.AllOK() {
		return errors.New("NPM antwoordt niet na het terugzetten van de kopie")
	}
	return nil
}

// remember werkt publish_hosts bij na een geslaagde wijziging.
func (s *Service) remember(ctx context.Context, j *jobs.Job, st Settings, node nodeRow, a Action, hostID int, created bool, ph *PlannedHost, ak akCreated) error {
	if ph.Delete {
		_, err := s.pool.Exec(ctx, `DELETE FROM publish_hosts WHERE npm_host_id = $1`, hostID)
		return err
	}
	h, err := s.npmHost(ctx, st, node, hostID)
	if err != nil || h == nil {
		return err
	}
	desired, _ := json.Marshal(h.fields())
	userID := j.RequestedBy
	var port *int
	if ph.ServicePort > 0 {
		port = &ph.ServicePort
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO publish_hosts (npm_host_id, domain, desired, cluster_id, node_id, service_port, created_by_cf, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (npm_host_id) DO UPDATE SET domain = EXCLUDED.domain, desired = EXCLUDED.desired,
		cluster_id = coalesce(EXCLUDED.cluster_id, publish_hosts.cluster_id), node_id = coalesce(EXCLUDED.node_id, publish_hosts.node_id),
		service_port = coalesce(EXCLUDED.service_port, publish_hosts.service_port), updated_at = now(), updated_by = EXCLUDED.updated_by`,
		hostID, strings.ToLower(h.DomainNames[0]), desired, ph.ClusterID, ph.NodeID, port, created, userID); err != nil {
		return err
	}
	switch {
	case ph.Protect && (ak.ProviderCreated || ak.AppSlug != ""):
		var app *string
		if ak.AppSlug != "" {
			app = &ak.AppSlug
		}
		_, err = s.pool.Exec(ctx, `UPDATE publish_hosts SET ak_provider_pk = $2, ak_app_slug = $3, ak_created = true WHERE npm_host_id = $1`,
			hostID, ak.ProviderPK, app)
	case ph.Unprotect:
		_, err = s.pool.Exec(ctx, `UPDATE publish_hosts SET ak_provider_pk = NULL, ak_app_slug = NULL, ak_created = false WHERE npm_host_id = $1`, hostID)
	}
	if err == nil && ph.Unprotect && ph.AKUndo != nil {
		_ = j.Step(ctx, "Authentik opruimen: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
			akNode, err := s.akNode(ctx, st)
			if err != nil {
				return err
			}
			return s.akUndo(ctx, st, akNode, *ph.AKUndo, step.Logf)
		})
	}
	return err
}

// syncNow start de sync NPM-configuratie, zodat de andere nodes meteen de
// nieuwe database en config krijgen.
func (s *Service) syncNow(ctx context.Context, j *jobs.Job, st Settings) error {
	if st.NPMClusterID == nil || s.Sync == nil {
		return nil
	}
	return j.Step(ctx, "Sync naar de andere nodes", func(ctx context.Context, step *jobs.Step) error {
		var id uuid.UUID
		err := s.pool.QueryRow(ctx, `SELECT id FROM cron_jobs WHERE cluster_id = $1 AND name = 'NPM-configuratie'`, *st.NPMClusterID).Scan(&id)
		if err != nil {
			step.Logf("Geen beheerde sync NPM-configuratie in dit cluster; de andere nodes volgen bij hun volgende sync")
			return nil
		}
		nodes, err := s.Sync.RunJob(ctx, events.System(), id)
		if err != nil && len(nodes) == 0 {
			step.Logf("Sync starten mislukt: %v (de volgende geplande sync neemt het mee)", err)
			return nil
		}
		step.Logf("Sync NPM-configuratie gestart op %s", strings.Join(nodes, ", "))
		return nil
	})
}

// Adopt neemt hosts over: ClusterForge onthoudt hun waarden van nu als
// gewenst, zonder iets in NPM te veranderen.
func (s *Service) Adopt(ctx context.Context, actor events.Actor, userID *uuid.UUID, ids []int) (int, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	var npm NPMRead
	if _, _, err := s.read(ctx, "npm", &npm); err != nil {
		return 0, err
	}
	if !st.NPMLinked() || len(npm.Hosts) == 0 {
		return 0, invalid("NPM is nog niet gelezen; koppel NPM en vernieuw")
	}
	ov, err := s.Overview(ctx)
	if err != nil {
		return 0, err
	}
	svcByKey := map[string]ServiceRow{}
	for _, r := range ov.Services {
		svcByKey[r.Key] = r
	}
	hostSvc := map[int]string{}
	for _, h := range ov.Hosts {
		hostSvc[h.ID] = h.ServiceKey
	}
	n := 0
	var names []string
	for _, h := range npm.Hosts {
		if !slices.Contains(ids, h.ID) || len(h.DomainNames) == 0 {
			continue
		}
		desired, _ := json.Marshal(h.fields())
		var clusterID, nodeID *uuid.UUID
		var port *int
		if r, ok := svcByKey[hostSvc[h.ID]]; ok {
			clusterID = r.ClusterID
			p := r.Port
			port = &p
			if len(r.Nodes) == 1 && !r.TargetVIP {
				nodeID = &r.Nodes[0].ID
			}
		}
		tag, err := s.pool.Exec(ctx, `INSERT INTO publish_hosts (npm_host_id, domain, desired, cluster_id, node_id, service_port, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (npm_host_id) DO NOTHING`,
			h.ID, strings.ToLower(h.DomainNames[0]), desired, clusterID, nodeID, port, userID)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() > 0 {
			n++
			names = append(names, h.DomainNames[0])
		}
	}
	if n > 0 {
		s.event(ctx, actor, "publish.adopted", map[string]any{"count": n, "domains": names})
	}
	return n, nil
}

// Accept neemt de waarden van nu in NPM over als gewenst (drift aanvaarden).
func (s *Service) Accept(ctx context.Context, actor events.Actor, id int) error {
	var npm NPMRead
	if _, _, err := s.read(ctx, "npm", &npm); err != nil {
		return err
	}
	for _, h := range npm.Hosts {
		if h.ID != id {
			continue
		}
		desired, _ := json.Marshal(h.fields())
		tag, err := s.pool.Exec(ctx, `UPDATE publish_hosts SET desired = $2, updated_at = now() WHERE npm_host_id = $1`, id, desired)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		s.event(ctx, actor, "publish.adopted", map[string]any{"count": 1, "domains": h.DomainNames, "accepted": true})
		return nil
	}
	return ErrNotFound
}

// Release laat een host los: ClusterForge beheert hem niet meer, in NPM
// verandert niets.
func (s *Service) Release(ctx context.Context, actor events.Actor, id int) error {
	var domain string
	err := s.pool.QueryRow(ctx, `DELETE FROM publish_hosts WHERE npm_host_id = $1 RETURNING domain`, id).Scan(&domain)
	if err != nil {
		return ErrNotFound
	}
	s.event(ctx, actor, "publish.released", map[string]any{"domain": domain, "host_id": id})
	return nil
}

// Test probeert de koppeling met NPM of Authentik.
func (s *Service) Test(ctx context.Context, what string) (map[string]any, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	switch what {
	case "npm":
		r, err := s.readNPM(ctx, st)
		if err != nil {
			return nil, err
		}
		s.saveRead(ctx, "npm", r, nil)
		return map[string]any{"node": r.Hostname, "version": r.Version, "hosts": len(r.Hosts), "certificates": len(r.Certs)}, nil
	case "authentik":
		r, err := s.readAuthentik(ctx, st)
		if err != nil {
			return nil, err
		}
		s.saveRead(ctx, "authentik", r, nil)
		out := map[string]any{"node": r.Hostname, "version": r.Version, "applications": len(r.Applications), "providers": len(r.Providers)}
		if o := r.EmbeddedOutpost(); o != nil {
			out["outpost"] = o.Name
		} else {
			out["outpost"] = ""
		}
		return out, nil
	}
	return nil, invalid("test npm of authentik")
}

func sortedNames(v any) []string {
	var out []string
	switch x := v.(type) {
	case []string:
		out = slices.Clone(x)
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	slices.Sort(out)
	return out
}
