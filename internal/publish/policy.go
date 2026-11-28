package publish

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// Fase 49: uitzonderingen per dienst, eigenaars per naam en de extra's in
// Advanced (open paden en terugval na een verhuizing).

// Soorten uitzondering.
const (
	// PolicyAuthentik: achter Authentik (de standaard).
	PolicyAuthentik = "authentik"
	// PolicyNever: nooit achter Authentik (Authentik zelf, een gastportaal).
	PolicyNever = "never"
	// PolicyOwnLogin: de app heeft een eigen login, dus geen Authentik.
	PolicyOwnLogin = "own_login"
	// PolicyExceptPaths: achter Authentik behalve de paden.
	PolicyExceptPaths = "except_paths"
)

// Policy is de uitzondering voor één naam (het eerste deel van de naam).
type Policy struct {
	Name  string   `json:"name"`
	Mode  string   `json:"mode"`
	Paths []string `json:"paths"`
	Note  string   `json:"note,omitempty"`
	// Implicit: niet ingesteld, maar uit de lijst "eigen login" van de
	// standaarden (proxmox, plex, ...).
	Implicit  bool       `json:"implicit,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// WantsAuthentik zegt of de dienst achter Authentik hoort.
func (p Policy) WantsAuthentik() bool {
	return p.Mode == PolicyAuthentik || p.Mode == PolicyExceptPaths
}

// ModeText is de soort in gewone taal.
func (p Policy) ModeText() string {
	switch p.Mode {
	case PolicyNever:
		return "nooit Authentik"
	case PolicyOwnLogin:
		return "eigen login"
	case PolicyExceptPaths:
		return "Authentik behalve " + strings.Join(p.Paths, ", ")
	}
	return "achter Authentik"
}

var pathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)

// policyName is het eerste deel van een naam (matomo voor matomo.jbogaert.be).
func policyName(domain string) string {
	first, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(domain)), ".")
	return first
}

// policyFor zoekt de uitzondering voor een naam: eerst het eerste deel zelf,
// dan zonder cijfers op het eind (proxmox50 → proxmox).
func policyFor(pols map[string]Policy, domain string) (Policy, bool) {
	label, short := themeLabel(domain)
	if p, ok := pols[label]; ok {
		return p, true
	}
	if p, ok := pols[short]; ok {
		return p, true
	}
	return Policy{}, false
}

// effectivePolicy is de uitzondering die geldt, met de standaarden als
// terugval: een woord uit "eigen login" is eigen login, anders Authentik als
// dat de standaard is.
func effectivePolicy(pols map[string]Policy, d Defaults, domain string, words ...string) Policy {
	if p, ok := policyFor(pols, domain); ok {
		return p
	}
	label := policyName(domain)
	if skipAuthentik(d, append(words, label)...) {
		return Policy{Name: label, Mode: PolicyOwnLogin, Implicit: true, Note: "uit de standaardlijst eigen login"}
	}
	if d.Authentik {
		return Policy{Name: label, Mode: PolicyAuthentik, Implicit: true}
	}
	return Policy{Name: label, Mode: PolicyNever, Implicit: true, Note: "Authentik staat standaard uit"}
}

// applyPolicies zet bij elke dienst de naam en de uitzondering.
func applyPolicies(services []ServiceRow, pols map[string]Policy, st Settings) {
	for i := range services {
		r := &services[i]
		r.DefaultName = defaultName(*r, st.Domain)
		if p, ok := policyFor(pols, r.Label); ok {
			r.Policy = p.Mode
			r.AuthentikDefault = p.WantsAuthentik()
		}
	}
}

// defaultName is de volledige naam bij publiceren: testclusters krijgen een
// eigen subdomein (app.test.jbogaert.be), zodat test en productie nooit om
// dezelfde naam vechten tot Jonas bewust verhuist.
func defaultName(r ServiceRow, domain string) string {
	if r.Label == "" || domain == "" {
		return ""
	}
	switch r.Environment {
	case "test", "lab":
		return r.Label + "." + r.Environment + "." + domain
	}
	return r.Label + "." + domain
}

func (s *Service) policyMap(ctx context.Context) (map[string]Policy, error) {
	list, err := s.Policies(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Policy, len(list))
	for _, p := range list {
		out[p.Name] = p
	}
	return out, nil
}

// Policies geeft de ingestelde uitzonderingen.
func (s *Service) Policies(ctx context.Context) ([]Policy, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, mode, paths, note, updated_at FROM publish_policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		var p Policy
		var at time.Time
		if err := rows.Scan(&p.Name, &p.Mode, &p.Paths, &p.Note, &at); err != nil {
			return nil, err
		}
		p.UpdatedAt = &at
		if p.Paths == nil {
			p.Paths = []string{}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// cleanPolicy controleert een uitzondering.
func cleanPolicy(p Policy) (Policy, error) {
	p.Name = policyName(p.Name)
	if !labelRe.MatchString(p.Name) {
		return Policy{}, invalid("%q is geen naam: het eerste deel van de naam, zoals matomo", p.Name)
	}
	switch p.Mode {
	case PolicyAuthentik, PolicyNever, PolicyOwnLogin:
		p.Paths = []string{}
	case PolicyExceptPaths:
		var paths []string
		for _, x := range p.Paths {
			x = strings.TrimSpace(x)
			if x == "" {
				continue
			}
			if !pathRe.MatchString(x) || strings.Contains(x, "..") || x == "/" {
				return Policy{}, invalid("%q is geen pad zoals /api of /guide.xml", x)
			}
			if !containsStr(paths, x) {
				paths = append(paths, x)
			}
		}
		if len(paths) == 0 {
			return Policy{}, invalid("geef minstens één pad zonder Authentik, zoals /api")
		}
		sort.Strings(paths)
		p.Paths = paths
	default:
		return Policy{}, invalid("onbekende soort %q", p.Mode)
	}
	p.Note = strings.TrimSpace(p.Note)
	return p, nil
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// SavePolicy maakt of wijzigt een uitzondering.
func (s *Service) SavePolicy(ctx context.Context, actor events.Actor, userID *uuid.UUID, in Policy) (Policy, error) {
	p, err := cleanPolicy(in)
	if err != nil {
		return Policy{}, err
	}
	var at time.Time
	if err := s.pool.QueryRow(ctx, `INSERT INTO publish_policies (name, mode, paths, note, updated_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE SET mode = EXCLUDED.mode, paths = EXCLUDED.paths, note = EXCLUDED.note,
		updated_by = EXCLUDED.updated_by, updated_at = now() RETURNING updated_at`, p.Name, p.Mode, p.Paths, p.Note, userID).Scan(&at); err != nil {
		return Policy{}, err
	}
	p.UpdatedAt = &at
	s.event(ctx, actor, "publish.policy_saved", map[string]any{"name": p.Name, "mode": p.Mode, "paths": p.Paths})
	return p, nil
}

// DeletePolicy haalt een uitzondering weg (dan gelden de standaarden).
func (s *Service) DeletePolicy(ctx context.Context, actor events.Actor, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM publish_policies WHERE name = $1`, policyName(name))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.event(ctx, actor, "publish.policy_deleted", map[string]any{"name": policyName(name)})
	return nil
}

// Fallback is het vorige doel van een naam na een verhuizing.
type Fallback struct {
	Scheme  string `json:"scheme"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Cluster string `json:"cluster,omitempty"`
}

// URL is het adres voor proxy_pass.
func (f Fallback) URL() string {
	scheme := f.Scheme
	if scheme != "https" {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, f.Host, f.Port)
}

// OwnerSet is wat een geslaagde wijziging als eigenaar vastlegt.
type OwnerSet struct {
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	ClusterName string     `json:"cluster_name,omitempty"`
	ServiceKey  string     `json:"service_key,omitempty"`
	ServiceName string     `json:"service_name,omitempty"`
	// Fallback: na een verhuizing; ClearFallback haalt hem weg.
	Fallback      *Fallback `json:"fallback,omitempty"`
	ClearFallback bool      `json:"clear_fallback,omitempty"`
	// KeepOwner: alleen de terugval aanpassen.
	KeepOwner bool `json:"keep_owner,omitempty"`
}

func ownerOf(r ServiceRow) *OwnerSet {
	name := r.Title
	if name == "" {
		name = r.Name
	}
	return &OwnerSet{ClusterID: r.ClusterID, ClusterName: r.ClusterName, ServiceKey: r.Key, ServiceName: name}
}

// Owner is de eigenaar van een naam (NPM-host en Authentik-app samen).
type Owner struct {
	Domain      string     `json:"domain"`
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	ClusterName string     `json:"cluster_name,omitempty"`
	ServiceKey  string     `json:"service_key,omitempty"`
	ServiceName string     `json:"service_name,omitempty"`
	Source      string     `json:"source"`
	Fallback    *Fallback  `json:"fallback,omitempty"`
	MovedAt     *time.Time `json:"moved_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	// Uit het overzicht: de host in NPM en de app in Authentik.
	HostID int    `json:"host_id,omitempty"`
	AKSlug string `json:"ak_slug,omitempty"`
}

// Label is de eigenaar in gewone taal.
func (o Owner) Label() string {
	switch {
	case o.ClusterName != "" && o.ServiceName != "":
		return o.ClusterName + " / " + o.ServiceName
	case o.ClusterName != "":
		return o.ClusterName
	}
	return o.ServiceName
}

func (s *Service) owners(ctx context.Context) (map[string]Owner, error) {
	rows, err := s.pool.Query(ctx, `SELECT domain, cluster_id, cluster_name, service_key, service_name, source,
		fallback_scheme, fallback_host, fallback_port, fallback_cluster, moved_at, updated_at FROM publish_owners`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Owner{}
	for rows.Next() {
		var o Owner
		var fs, fh *string
		var fp *int
		var fc string
		if err := rows.Scan(&o.Domain, &o.ClusterID, &o.ClusterName, &o.ServiceKey, &o.ServiceName, &o.Source,
			&fs, &fh, &fp, &fc, &o.MovedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		if fh != nil && fp != nil {
			o.Fallback = &Fallback{Host: *fh, Port: *fp, Cluster: fc}
			if fs != nil {
				o.Fallback.Scheme = *fs
			}
		}
		out[o.Domain] = o
	}
	return out, rows.Err()
}

// saveOwner legt de eigenaar van een naam vast na een geslaagde wijziging.
func (s *Service) saveOwner(ctx context.Context, domain string, o OwnerSet, by *uuid.UUID) error {
	domain = strings.ToLower(domain)
	if !o.KeepOwner {
		if _, err := s.pool.Exec(ctx, `INSERT INTO publish_owners (domain, cluster_id, cluster_name, service_key, service_name, source, updated_by)
			VALUES ($1, $2, $3, $4, $5, 'scan', $6)
			ON CONFLICT (domain) DO UPDATE SET cluster_id = EXCLUDED.cluster_id, cluster_name = EXCLUDED.cluster_name,
			service_key = EXCLUDED.service_key, service_name = EXCLUDED.service_name, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			domain, o.ClusterID, o.ClusterName, o.ServiceKey, o.ServiceName, by); err != nil {
			return err
		}
	}
	switch {
	case o.Fallback != nil:
		_, err := s.pool.Exec(ctx, `UPDATE publish_owners SET fallback_scheme = $2, fallback_host = $3, fallback_port = $4, fallback_cluster = $5,
			moved_at = now(), updated_at = now() WHERE domain = $1`, domain, o.Fallback.Scheme, o.Fallback.Host, o.Fallback.Port, o.Fallback.Cluster)
		return err
	case o.ClearFallback:
		_, err := s.pool.Exec(ctx, `UPDATE publish_owners SET fallback_scheme = NULL, fallback_host = NULL, fallback_port = NULL, fallback_cluster = '',
			updated_at = now() WHERE domain = $1`, domain)
		return err
	}
	return nil
}

// recordOwners legt bij een scan de eigenaar vast van elke host waarvan
// ClusterForge de dienst kent en die nog geen eigenaar heeft.
func (s *Service) recordOwners(ctx context.Context, ov *Overview, owners map[string]Owner) {
	byKey := map[string]ServiceRow{}
	for _, r := range ov.Services {
		byKey[r.Key] = r
	}
	for _, h := range ov.Hosts {
		if len(h.Domains) == 0 || h.ServiceKey == "" {
			continue
		}
		d := strings.ToLower(h.Domains[0])
		if _, ok := owners[d]; ok {
			continue
		}
		r, ok := byKey[h.ServiceKey]
		if !ok {
			continue
		}
		o := ownerOf(r)
		if err := s.saveOwner(ctx, d, *o, nil); err != nil {
			s.log.Warn("publicatie: eigenaar vastleggen", "domain", d, "err", err)
			continue
		}
		owners[d] = Owner{Domain: d, ClusterID: o.ClusterID, ClusterName: o.ClusterName, ServiceKey: o.ServiceKey, ServiceName: o.ServiceName, Source: "scan"}
	}
}

// OwnersView is de lijst eigenaars voor de GUI.
type OwnersView struct {
	Owners []Owner `json:"owners"`
	// Without: namen in NPM of Authentik zonder eigenaar.
	Without []string `json:"without"`
}

// Owners geeft de eigenaars met hun host en app.
func (s *Service) Owners(ctx context.Context) (*OwnersView, error) {
	ov, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	owners, err := s.owners(ctx)
	if err != nil {
		return nil, err
	}
	hostBy := map[string]int{}
	for _, h := range ov.Hosts {
		for _, d := range h.Domains {
			hostBy[strings.ToLower(d)] = h.ID
		}
	}
	akBy := map[string]string{}
	provByPK := map[int]AKProxyProvider{}
	for _, p := range ov.rawAK.Providers {
		provByPK[p.PK] = p
	}
	for _, a := range ov.rawAK.Applications {
		if a.Provider != nil {
			if p, ok := provByPK[*a.Provider]; ok && p.Host() != "" {
				akBy[p.Host()] = a.Slug
			}
		}
	}
	v := &OwnersView{Owners: []Owner{}, Without: []string{}}
	seen := map[string]bool{}
	for d, o := range owners {
		o.HostID, o.AKSlug = hostBy[d], akBy[d]
		v.Owners = append(v.Owners, o)
		seen[d] = true
	}
	for d := range hostBy {
		if !seen[d] && inDomain(d, ov.Settings.Domain) {
			v.Without = append(v.Without, d)
			seen[d] = true
		}
	}
	for d := range akBy {
		if !seen[d] && inDomain(d, ov.Settings.Domain) {
			v.Without = append(v.Without, d)
			seen[d] = true
		}
	}
	sort.Slice(v.Owners, func(i, j int) bool { return v.Owners[i].Domain < v.Owners[j].Domain })
	sort.Strings(v.Without)
	return v, nil
}

// OwnerInput zet de eigenaar van een naam met de hand.
type OwnerInput struct {
	Domain     string     `json:"domain"`
	ClusterID  *uuid.UUID `json:"cluster_id,omitempty"`
	ServiceKey string     `json:"service_key,omitempty"`
	// Clear haalt de eigenaar weg.
	Clear bool `json:"clear,omitempty"`
}

// SetOwner zet de eigenaar van een naam met de hand.
func (s *Service) SetOwner(ctx context.Context, actor events.Actor, userID *uuid.UUID, in OwnerInput) error {
	d := strings.ToLower(strings.TrimSpace(in.Domain))
	if d == "" {
		return invalid("geef de naam")
	}
	if in.Clear {
		if _, err := s.pool.Exec(ctx, `DELETE FROM publish_owners WHERE domain = $1`, d); err != nil {
			return err
		}
		s.event(ctx, actor, "publish.owner_cleared", map[string]any{"domain": d})
		return nil
	}
	var o OwnerSet
	if in.ServiceKey != "" {
		ov, err := s.Overview(ctx)
		if err != nil {
			return err
		}
		for _, r := range ov.Services {
			if r.Key == in.ServiceKey {
				o = *ownerOf(r)
			}
		}
		if o.ServiceKey == "" {
			return invalid("die dienst bestaat niet (meer)")
		}
	} else if in.ClusterID != nil {
		cl, err := s.clusters(ctx)
		if err != nil {
			return err
		}
		for _, c := range cl {
			if c.ID == *in.ClusterID {
				id := c.ID
				o = OwnerSet{ClusterID: &id, ClusterName: c.Name}
			}
		}
		if o.ClusterID == nil {
			return invalid("dat cluster bestaat niet")
		}
	} else {
		return invalid("kies een cluster of een dienst")
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO publish_owners (domain, cluster_id, cluster_name, service_key, service_name, source, updated_by)
		VALUES ($1, $2, $3, $4, $5, 'hand', $6)
		ON CONFLICT (domain) DO UPDATE SET cluster_id = EXCLUDED.cluster_id, cluster_name = EXCLUDED.cluster_name,
		service_key = EXCLUDED.service_key, service_name = EXCLUDED.service_name, source = 'hand', updated_by = EXCLUDED.updated_by, updated_at = now()`,
		d, o.ClusterID, o.ClusterName, o.ServiceKey, o.ServiceName, userID); err != nil {
		return err
	}
	s.event(ctx, actor, "publish.owner_set", map[string]any{"domain": d, "cluster": o.ClusterName, "service": o.ServiceName})
	return nil
}

// extrasFor geeft de open paden (uitzondering) en de terugval (eigenaar)
// van een naam, voor in Advanced.
func (s *Service) extrasFor(ctx context.Context, domain string) ([]string, *Fallback) {
	var paths []string
	if pols, err := s.policyMap(ctx); err == nil {
		if p, ok := policyFor(pols, domain); ok && p.Mode == PolicyExceptPaths {
			paths = p.Paths
		}
	}
	var fb *Fallback
	var fs, fh *string
	var fp *int
	var fc string
	err := s.pool.QueryRow(ctx, `SELECT fallback_scheme, fallback_host, fallback_port, fallback_cluster FROM publish_owners WHERE domain = $1`,
		strings.ToLower(domain)).Scan(&fs, &fh, &fp, &fc)
	if err == nil && fh != nil && fp != nil {
		fb = &Fallback{Host: *fh, Port: *fp, Cluster: fc}
		if fs != nil {
			fb.Scheme = *fs
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.log.Warn("publicatie: terugval lezen", "domain", domain, "err", err)
	}
	return paths, fb
}

// De extra's van ClusterForge in Advanced: open paden en terugval.
const (
	extrasBegin = "# clusterforge-extra begin (beheerd door ClusterForge)"
	extrasEnd   = "# clusterforge-extra eind"
	// fallbackMark staat achter de regels die ClusterForge in de location
	// van Authentik zet (daar geldt de error_page van de server niet).
	fallbackMark = "# clusterforge-terugval"
)

var (
	akSigninLine = regexp.MustCompile(`(?m)^([ \t]*)error_page\s+401\s*=\s*@goauthentik_proxy_signin;[^\n]*$`)
	fallbackLine = regexp.MustCompile(`(?m)^[^\n]*` + regexp.QuoteMeta(fallbackMark) + `[^\n]*\n?`)
)

// withoutExtras haalt de extra's van ClusterForge uit een config.
func withoutExtras(adv string) string {
	adv = fallbackLine.ReplaceAllString(adv, "")
	i := strings.Index(adv, extrasBegin)
	if i < 0 {
		return adv
	}
	j := strings.Index(adv[i:], extrasEnd)
	if j < 0 {
		return adv
	}
	rest := strings.TrimLeft(adv[i+j+len(extrasEnd):], "\n")
	return strings.TrimRight(adv[:i], "\n") + joinNL(adv[:i], rest)
}

// extrasPaths geeft de open paden die nu in de config staan.
func extrasPaths(adv string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^location (?:= |\^~ )(/\S*) \{ `+regexp.QuoteMeta("# open")+`$`).FindAllStringSubmatch(adv, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// hasFallback zegt of de config een terugval van ClusterForge heeft.
func hasFallback(adv string) bool { return strings.Contains(adv, "location @cf_fallback") }

// openLocation is een pad zonder Authentik: een bestand exact, een map met
// alles eronder.
func openLocation(path string, fb *Fallback) string {
	match := "^~ "
	last := path[strings.LastIndex(path, "/")+1:]
	if strings.Contains(last, ".") {
		match = "= "
	}
	b := "location " + match + path + " { # open\n" +
		"    proxy_pass          $forward_scheme://$server:$port;\n" +
		"    proxy_set_header    Host $host;\n" +
		"    proxy_set_header    X-Forwarded-For $proxy_add_x_forwarded_for;\n" +
		"    proxy_set_header    X-Forwarded-Proto $scheme;\n" +
		"    proxy_set_header    X-Real-IP $remote_addr;\n" +
		"    proxy_set_header    Upgrade $http_upgrade;\n" +
		"    proxy_set_header    Connection $http_connection;\n" +
		"    proxy_http_version  1.1;\n"
	if fb != nil {
		b += "    error_page          502 504 = @cf_fallback;\n"
	}
	return b + "}\n"
}

// decorate zet de extra's in een config: de paden zonder Authentik (alleen
// als de host forward auth heeft) en de terugval naar het vorige doel. Wat
// ClusterForge er eerder zette, gaat eerst weg.
func decorate(adv string, paths []string, fb *Fallback) string {
	base := withoutExtras(adv)
	protected := hasSnippet(base)
	if !protected {
		paths = nil
	}
	if len(paths) == 0 && fb == nil {
		return base
	}
	var b strings.Builder
	b.WriteString(extrasBegin + "\n")
	if fb != nil {
		// Geeft het nieuwe doel geen antwoord (502/504), dan het vorige.
		b.WriteString("error_page 502 504 = @cf_fallback;\n")
		base = akSigninLine.ReplaceAllString(base, "$0\n${1}error_page       502 504 = @cf_fallback; "+fallbackMark)
	}
	for _, p := range paths {
		b.WriteString(openLocation(p, fb))
	}
	if fb != nil {
		b.WriteString("location @cf_fallback { # terugval naar " + fb.URL())
		if fb.Cluster != "" {
			b.WriteString(" (" + fb.Cluster + ")")
		}
		b.WriteString("\n    proxy_pass          " + fb.URL() + ";\n" +
			"    proxy_set_header    Host $host;\n" +
			"    proxy_set_header    X-Forwarded-For $proxy_add_x_forwarded_for;\n" +
			"    proxy_set_header    X-Forwarded-Proto $scheme;\n" +
			"    proxy_set_header    Upgrade $http_upgrade;\n" +
			"    proxy_set_header    Connection $http_connection;\n" +
			"    proxy_http_version  1.1;\n}\n")
	}
	b.WriteString(extrasEnd)
	return joinBlocks(base, b.String())
}
