package publish

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// Sjabloon voor Advanced (fase 42b): één algemeen sjabloon, in een versie
// met Authentik en een zonder, met per host een eigen css-bestand. Het
// ingevulde sjabloon staat als eigen blok in de Advanced van de host; de
// Authentik-knop wisselt tussen de twee versies.

const (
	themeBegin = "# clusterforge-thema begin (beheerd door ClusterForge)"
	themeEnd   = "# clusterforge-thema eind"
	// ActThemeMany zet het sjabloon op hosts (leeg Hosts = alle).
	ActThemeMany = "theme_many"
	// NoCSS bij een host: geen thema op die host.
	NoCSS = "-"
)

// DefaultThemeAuth is het standaardsjabloon voor hosts achter Authentik.
const DefaultThemeAuth = `location / {
    # Thema
    sub_filter '</head>' '<link rel="stylesheet" type="text/css" href="{css}"></head>';
    sub_filter_once on;
    proxy_set_header Accept-Encoding "";

    proxy_pass $forward_scheme://$server:$port;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;

    proxy_buffers 8 16k;
    proxy_buffer_size 32k;

    add_header X-Xss-Protection "1; mode=block" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header Referrer-Policy "no-referrer";
    add_header Content-Security-Policy "frame-ancestors 'self'";

    port_in_redirect off;

    auth_request /outpost.goauthentik.io/auth/nginx;
    error_page 401 = @goauthentik_proxy_signin;
    auth_request_set $auth_cookie $upstream_http_set_cookie;
    add_header Set-Cookie $auth_cookie;

    auth_request_set $authentik_username $upstream_http_x_authentik_username;
    auth_request_set $authentik_groups $upstream_http_x_authentik_groups;
    auth_request_set $authentik_email $upstream_http_x_authentik_email;
    auth_request_set $authentik_name $upstream_http_x_authentik_name;
    auth_request_set $authentik_uid $upstream_http_x_authentik_uid;

    proxy_set_header X-authentik-username $authentik_username;
    proxy_set_header X-authentik-groups $authentik_groups;
    proxy_set_header X-authentik-email $authentik_email;
    proxy_set_header X-authentik-name $authentik_name;
    proxy_set_header X-authentik-uid $authentik_uid;
}

location /outpost.goauthentik.io {
    proxy_pass {outpost}/outpost.goauthentik.io;
    proxy_set_header Host $host;
    proxy_set_header X-Original-URL $scheme://$http_host$request_uri;
    add_header Set-Cookie $auth_cookie;
    auth_request_set $auth_cookie $upstream_http_set_cookie;
    proxy_pass_request_body off;
    proxy_set_header Content-Length "";
}

location @goauthentik_proxy_signin {
    internal;
    add_header Set-Cookie $auth_cookie;
    return 302 /outpost.goauthentik.io/start?rd=$request_uri;
}`

// DefaultThemePlain is het standaardsjabloon voor hosts zonder Authentik.
const DefaultThemePlain = `location / {
    # Thema
    sub_filter '</head>' '<link rel="stylesheet" type="text/css" href="{css}"></head>';
    sub_filter_once on;
    proxy_set_header Accept-Encoding "";

    proxy_pass $forward_scheme://$server:$port;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}`

// Theme is het sjabloon met de css per host.
type Theme struct {
	TemplateAuth  string `json:"template_auth"`
	TemplatePlain string `json:"template_plain"`
	// CSSDefault is het css-bestand voor elke host zonder eigen keuze, met
	// {naam} (eerste deel van de naam zonder cijfers) en {label}. Leeg =
	// alleen hosts met een eigen css krijgen het thema.
	CSSDefault string `json:"css_default"`
	// Hosts: css per proxy host; "-" = geen thema op die host.
	Hosts        map[int]string `json:"hosts"`
	DefaultAuth  string         `json:"default_auth"`
	DefaultPlain string         `json:"default_plain"`
	UpdatedAt    *time.Time     `json:"updated_at,omitempty"`
}

// ThemeInput verandert het sjabloon; leeg sjabloon = het standaardsjabloon.
type ThemeInput struct {
	TemplateAuth  string `json:"template_auth"`
	TemplatePlain string `json:"template_plain"`
	CSSDefault    string `json:"css_default"`
}

func (t Theme) auth() string {
	if strings.TrimSpace(t.TemplateAuth) == "" {
		return DefaultThemeAuth
	}
	return t.TemplateAuth
}

func (t Theme) plain() string {
	if strings.TrimSpace(t.TemplatePlain) == "" {
		return DefaultThemePlain
	}
	return t.TemplatePlain
}

var trailingDigits = regexp.MustCompile(`[-_]?[0-9]+$`)

func themeLabel(name string) (label, short string) {
	label, _, _ = strings.Cut(strings.ToLower(name), ".")
	short = trailingDigits.ReplaceAllString(label, "")
	if short == "" {
		short = label
	}
	return label, short
}

// cssFor geeft het css-bestand voor een host, of leeg (geen thema).
func (t Theme) cssFor(hostID int, name string) string {
	if v, ok := t.Hosts[hostID]; ok && hostID > 0 {
		if v == NoCSS {
			return ""
		}
		return v
	}
	if t.CSSDefault == "" {
		return ""
	}
	label, short := themeLabel(name)
	return strings.NewReplacer("{naam}", short, "{label}", label).Replace(t.CSSDefault)
}

// render vult het sjabloon in, met de markeringen eromheen.
func (t Theme) render(protect bool, name, css, outpost string) string {
	tpl := t.plain()
	if protect {
		tpl = t.auth()
	}
	label, short := themeLabel(name)
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(tpl, "\r\n", "\n"), "\n") {
		if css == "" && strings.Contains(l, "{css}") {
			continue
		}
		out = append(out, l)
	}
	body := strings.NewReplacer("{css}", css, "{outpost}", outpost, "{naam}", short, "{label}", label).Replace(strings.Join(out, "\n"))
	return themeBegin + "\n" + strings.TrimSpace(body) + "\n" + themeEnd
}

// themeBlock geeft het themablok in een config, of leeg.
func themeBlock(adv string) string {
	i := strings.Index(adv, themeBegin)
	if i < 0 {
		return ""
	}
	j := strings.Index(adv[i:], themeEnd)
	if j < 0 {
		return ""
	}
	return adv[i : i+j+len(themeEnd)]
}

func withoutTheme(adv string) string {
	b := themeBlock(adv)
	if b == "" {
		return adv
	}
	i := strings.Index(adv, b)
	rest := strings.TrimLeft(adv[i+len(b):], "\n")
	return strings.TrimRight(adv[:i], "\n") + joinNL(adv[:i], rest)
}

// ownAdvanced is de Advanced zonder de blokken van ClusterForge.
func ownAdvanced(adv string) string { return strings.TrimSpace(withoutTheme(withoutSnippet(adv))) }

func joinBlocks(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// Theme leest het sjabloon.
func (s *Service) Theme(ctx context.Context) (Theme, error) {
	t := Theme{Hosts: map[int]string{}, DefaultAuth: DefaultThemeAuth, DefaultPlain: DefaultThemePlain}
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT template_auth, template_plain, css_default, updated_at FROM publish_theme`).
		Scan(&t.TemplateAuth, &t.TemplatePlain, &t.CSSDefault, &at)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Theme{}, err
	default:
		t.UpdatedAt = &at
	}
	rows, err := s.pool.Query(ctx, `SELECT host_id, css FROM publish_host_css`)
	if err != nil {
		return Theme{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var css string
		if err := rows.Scan(&id, &css); err != nil {
			return Theme{}, err
		}
		t.Hosts[id] = css
	}
	return t, rows.Err()
}

var cssRe = regexp.MustCompile(`^https?://[^\s'"<>]+$`)

func checkCSS(css string) error {
	if css == "" || css == NoCSS {
		return nil
	}
	if !cssRe.MatchString(strings.NewReplacer("{naam}", "x", "{label}", "x").Replace(css)) {
		return invalid("%q is geen adres van een css-bestand (zoals https://css.jbogaert.be/{naam}.css)", css)
	}
	return nil
}

func checkTemplate(tpl string, auth bool) error {
	if strings.TrimSpace(tpl) == "" {
		return nil
	}
	if strings.Contains(tpl, themeBegin) || strings.Contains(tpl, themeEnd) || strings.Contains(tpl, snippetBegin) || strings.Contains(tpl, snippetEnd) {
		return invalid("de markeringen van ClusterForge horen niet in het sjabloon")
	}
	if strings.Count(tpl, "{") != strings.Count(tpl, "}") {
		return invalid("de accolades { } in het sjabloon zijn niet in evenwicht")
	}
	if auth && !strings.Contains(tpl, "outpost.goauthentik.io") {
		return invalid("het sjabloon met Authentik moet de outpost gebruiken (outpost.goauthentik.io)")
	}
	if !auth && strings.Contains(tpl, "auth_request") {
		return invalid("het sjabloon zonder Authentik mag geen auth_request hebben")
	}
	return nil
}

// SaveTheme bewaart het algemene sjabloon.
func (s *Service) SaveTheme(ctx context.Context, actor events.Actor, in ThemeInput) (Theme, error) {
	in.CSSDefault = strings.TrimSpace(in.CSSDefault)
	for _, tp := range []*string{&in.TemplateAuth, &in.TemplatePlain} {
		*tp = strings.TrimSpace(strings.ReplaceAll(*tp, "\r\n", "\n"))
	}
	if in.TemplateAuth == DefaultThemeAuth {
		in.TemplateAuth = ""
	}
	if in.TemplatePlain == DefaultThemePlain {
		in.TemplatePlain = ""
	}
	if err := checkCSS(in.CSSDefault); err != nil {
		return Theme{}, err
	}
	if err := checkTemplate(in.TemplateAuth, true); err != nil {
		return Theme{}, err
	}
	if err := checkTemplate(in.TemplatePlain, false); err != nil {
		return Theme{}, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO publish_theme (id, template_auth, template_plain, css_default, updated_at) VALUES (true, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE SET template_auth = $1, template_plain = $2, css_default = $3, updated_at = now()`,
		in.TemplateAuth, in.TemplatePlain, in.CSSDefault); err != nil {
		return Theme{}, err
	}
	s.event(ctx, actor, "publish.theme_saved", map[string]any{"css_default": in.CSSDefault, "custom": in.TemplateAuth != "" || in.TemplatePlain != ""})
	return s.Theme(ctx)
}

// SetHostCSS zet het css-bestand van één host; leeg = de standaard, "-" = geen thema.
func (s *Service) SetHostCSS(ctx context.Context, actor events.Actor, hostID int, css string) (Theme, error) {
	css = strings.TrimSpace(css)
	if hostID < 1 {
		return Theme{}, invalid("ongeldige host")
	}
	if err := checkCSS(css); err != nil {
		return Theme{}, err
	}
	var err error
	if css == "" {
		_, err = s.pool.Exec(ctx, `DELETE FROM publish_host_css WHERE host_id = $1`, hostID)
	} else {
		_, err = s.pool.Exec(ctx, `INSERT INTO publish_host_css (host_id, css) VALUES ($1, $2)
			ON CONFLICT (host_id) DO UPDATE SET css = $2, updated_at = now()`, hostID, css)
	}
	if err != nil {
		return Theme{}, err
	}
	s.event(ctx, actor, "publish.host_css", map[string]any{"host_id": hostID, "css": css})
	return s.Theme(ctx)
}

// themedAdvanced geeft de Advanced van een host met het sjabloon erin, of
// ok=false als het thema niet voor die host geldt.
func (t Theme) themedAdvanced(cur string, hostID int, name string, protect bool, outpost string) (adv string, replaced string, ok bool) {
	css := t.cssFor(hostID, name)
	if css == "" {
		return "", "", false
	}
	own := ownAdvanced(cur)
	if locationRoot.MatchString(own) || hasSnippet(own) {
		// Een eigen 'location /' of eigen forward auth botst met het
		// sjabloon: het sjabloon neemt die plaats in.
		replaced, own = own, ""
	}
	return joinBlocks(own, t.render(protect, name, css, outpost)), replaced, true
}

// planThemeMany zet het sjabloon op de gekozen hosts, of haalt het weg
// waar de host geen thema meer heeft.
func (s *Service) planThemeMany(ctx context.Context, st Settings, npm *NPMRead, managed map[int]managedHost, a Action, p *Plan) error {
	t, err := s.Theme(ctx)
	if err != nil {
		return err
	}
	outpost := ""
	if st.AuthentikLinked() {
		outpost, _ = s.outpostFor(ctx, st)
	}
	p.Title = "Sjabloon voor Advanced toepassen"
	skipped := 0
	for _, h := range npm.Hosts {
		if len(h.DomainNames) == 0 || (len(a.Hosts) > 0 && !slices.Contains(a.Hosts, h.ID)) {
			continue
		}
		name := strings.ToLower(h.DomainNames[0])
		protect := hasSnippet(h.AdvancedConfig)
		if protect && outpost == "" {
			p.Warnings = append(p.Warnings, name+": achter Authentik, maar het adres van de outpost is niet bekend; overgeslagen")
			continue
		}
		want, replaced, ok := t.themedAdvanced(h.AdvancedConfig, h.ID, name, protect, outpost)
		line := ""
		switch {
		case ok:
			line = fmt.Sprintf("%s: sjabloon %s, css %s", name, map[bool]string{true: "met Authentik", false: "zonder Authentik"}[protect], t.cssFor(h.ID, name))
			if replaced != "" {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s: de eigen Advanced (%d regels, met een eigen 'location /' of forward auth) wordt vervangen door het sjabloon; de oude staat in de kopie", name, strings.Count(replaced, "\n")+1))
			}
		case themeBlock(h.AdvancedConfig) != "":
			own := ownAdvanced(h.AdvancedConfig)
			want = own
			if protect {
				if want, err = withSnippet(own, outpost); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			line = name + ": thema weg (geen css meer voor deze host)"
		default:
			skipped++
			continue
		}
		if strings.TrimSpace(want) == strings.TrimSpace(h.AdvancedConfig) {
			skipped++
			continue
		}
		ph := PlannedHost{HostID: h.ID, Domain: name, Scheme: schemeOf(h),
			Body: map[string]any{"advanced_config": want}, Old: map[string]any{"advanced_config": h.AdvancedConfig}}
		p.Lines = append(p.Lines, line)
		if _, ok := managed[h.ID]; !ok {
			p.Warnings = append(p.Warnings, name+": nog niet beheerd; ClusterForge neemt hem over na de wijziging")
		}
		p.Hosts = append(p.Hosts, ph)
	}
	if len(p.Hosts) == 0 {
		return invalid("geen host om aan te passen: alle hosts hebben het sjabloon al, of er is geen css (stel een standaard-css of een css per host in)")
	}
	if skipped > 0 {
		p.Lines = append(p.Lines, fmt.Sprintf("%d hosts blijven zoals ze zijn (al in orde of geen css)", skipped))
	}
	if len(p.Hosts) > 1 {
		p.Lines = append(p.Lines, fmt.Sprintf("Host voor host, elk met een eigen kopie en controle; mislukt er een, dan gaat alleen die terug en gaan de andere door (%d hosts)", len(p.Hosts)))
	}
	return nil
}

// protectAdvanced geeft de Advanced van een host achter Authentik: met
// het sjabloon als de host een thema heeft, anders met het blok van
// ClusterForge.
func (s *Service) protectAdvanced(ctx context.Context, h NPMHost, name, outpost string) (string, error) {
	t, err := s.Theme(ctx)
	if err != nil {
		return "", err
	}
	if themeBlock(h.AdvancedConfig) != "" {
		if adv, _, ok := t.themedAdvanced(h.AdvancedConfig, h.ID, name, true, outpost); ok {
			return adv, nil
		}
	}
	return withSnippet(withoutTheme(h.AdvancedConfig), outpost)
}
