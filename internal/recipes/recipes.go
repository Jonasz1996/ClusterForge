// Package recipes leest recepten: een tekstformaat dat op een Dockerfile
// lijkt en zegt hoe software op de nodes van een cluster komt (fase 9, zie
// docs/design/fase9-recepten.md).
//
// Parse controleert een recept en maakt er stappen van. Een stap is een
// map in dezelfde vorm als de stappen van een template.yaml (package, file,
// service, …), zodat een samengesteld cluster later als gewone template door
// plan en uitrol gaat. Een paar soorten bestaan nog niet in de agent
// (repo_deb, repo, ini_set en de database-instructies); die krijgen hun
// agentstap in een volgende mijlpaal, de vorm ligt hier vast.
//
// Waarden tussen {{ … }} worden vertaald naar de namen van de templates:
// {{ secret.x }} en {{ param.x }} worden {{ .params.x }}, {{ node.ip }}
// wordt {{ .node.address }}, enzovoort. Een geheim staat nooit in de tekst.
package recipes

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Diag is een fout of waarschuwing bij een regel van het recept.
type Diag struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
	Warning bool   `json:"warning,omitempty"`
}

func (d Diag) Error() string { return fmt.Sprintf("regel %d: %s", d.Line, d.Message) }

// Param is een instelling of geheim dat het recept vraagt.
type Param struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
	// Length is de lengte van een gegenereerd geheim.
	Length int `json:"length,omitempty"`
	Line   int `json:"line"`
}

// Step is één stap voor de nodes van een rol.
type Step struct {
	Line int `json:"line"`
	// Kind is package, file, service, user, directory, command, repo_deb,
	// repo, ini_set, db_database, db_user of db_import.
	Kind string `json:"kind"`
	// Once zegt dat de stap op één node loopt: de VIP-houder, of voor een
	// database-instructie de schrijvende datanode.
	Once bool `json:"once,omitempty"`
	// Secret zegt dat er een geheim in de stap zit; plan en logboek tonen
	// dan geen inhoud.
	Secret bool `json:"secret,omitempty"`
	// Spec is de stap in de vorm van template.yaml: {"package": {...}}.
	Spec  map[string]any `json:"spec"`
	Title string         `json:"title"`
}

// Health is een controle van een node: service, http, port of db.
type Health struct {
	Line   int    `json:"line"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	Expect int    `json:"expect,omitempty"`
}

// Role is wat een recept op de nodes van één rol doet. Een recept zonder
// ROLE heeft één rol met een lege naam: die geldt voor de rol waar je het
// recept aan koppelt.
type Role struct {
	Name   string   `json:"name"`
	Steps  []Step   `json:"steps"`
	Health []Health `json:"health,omitempty"`
}

// Recipe is een gelezen recept.
type Recipe struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	From    string  `json:"from"`
	Needs   []Need  `json:"needs,omitempty"`
	Params  []Param `json:"params,omitempty"`
	Roles   []Role  `json:"roles"`
}

// Need is een clusterservice die aan moet staan.
type Need struct {
	Service string `json:"service"`
	// Engine is mariadb of postgresql bij database.
	Engine string `json:"engine,omitempty"`
}

// NeedsDatabase geeft de engine die het recept vraagt, of "".
func (r *Recipe) NeedsDatabase() string {
	for _, n := range r.Needs {
		if n.Service == "database" {
			return n.Engine
		}
	}
	return ""
}

// NeedsVIP zegt of het recept een VIP vraagt.
func (r *Recipe) NeedsVIP() bool {
	return slices.ContainsFunc(r.Needs, func(n Need) bool { return n.Service == "vip" })
}

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	versionRe  = regexp.MustCompile(`^[0-9][0-9A-Za-z.+~-]{0,31}$`)
	identRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	roleRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	pkgRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9+.:=~*-]*$`)
	unitRe     = regexp.MustCompile(`^[A-Za-z0-9@_.:-]+$`)
	userRe     = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	dbNameRe   = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	charsetRe  = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	modeRe     = regexp.MustCompile(`^0?[0-7]{3,4}$`)
	iniKeyRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	exprRe     = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)
	secretKeys = regexp.MustCompile(`(?i)pass|secret|token|key`)
)

var paramTypes = map[string]bool{"string": true, "int": true, "bool": true, "ipv4": true, "size": true}

// Parse leest een recept. Een recept met fouten geeft nil en de fouten;
// waarschuwingen staan er altijd bij.
func Parse(src string) (*Recipe, []Diag) {
	p := &parser{r: &Recipe{}, params: map[string]*Param{}}
	p.run(src)
	if p.errors() {
		return nil, p.diags
	}
	return p.r, p.diags
}

type parser struct {
	r      *Recipe
	diags  []Diag
	params map[string]*Param
	role   *Role
	line   int
	// since geeft per dienst de index in role.Steps vanaf waar een
	// wijziging hem laat herstarten: na zijn vorige SERVICE-regel.
	since map[string]int
}

func (p *parser) errorf(format string, a ...any) {
	p.diags = append(p.diags, Diag{Line: p.line, Message: fmt.Sprintf(format, a...)})
}

func (p *parser) warnf(format string, a ...any) {
	p.diags = append(p.diags, Diag{Line: p.line, Message: fmt.Sprintf(format, a...), Warning: true})
}

func (p *parser) errors() bool {
	return slices.ContainsFunc(p.diags, func(d Diag) bool { return !d.Warning })
}

// line is een logische regel: na voortzetten met \ en met een eventuele
// heredoc.
type line struct {
	no      int
	text    string
	heredoc *string
}

func split(src string, diag func(int, string)) []line {
	raw := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []line
	for i := 0; i < len(raw); i++ {
		no := i + 1
		t := strings.TrimSpace(raw[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		for strings.HasSuffix(t, `\`) && i+1 < len(raw) {
			i++
			t = strings.TrimSpace(strings.TrimSuffix(t, `\`) + " " + strings.TrimSpace(raw[i]))
		}
		l := line{no: no, text: stripComment(t)}
		if j := strings.LastIndex(l.text, "<<"); j >= 0 && !strings.ContainsAny(l.text[j:], `"'`) {
			tag := strings.TrimSpace(l.text[j+2:])
			if !regexp.MustCompile(`^[A-Z_]+$`).MatchString(tag) {
				diag(no, "na << komt een woord in hoofdletters, zoals EOF")
			} else {
				l.text = strings.TrimSpace(l.text[:j])
				var body []string
				end := false
				for i+1 < len(raw) {
					i++
					if strings.TrimSpace(raw[i]) == tag {
						end = true
						break
					}
					body = append(body, raw[i])
				}
				if !end {
					diag(no, fmt.Sprintf("%s sluit nergens af", tag))
				}
				s := strings.Join(body, "\n")
				if len(body) > 0 {
					s += "\n"
				}
				l.heredoc = &s
			}
		}
		out = append(out, l)
	}
	return out
}

// stripComment haalt een # weg die buiten aanhalingstekens en buiten
// {{ }} staat en na een spatie komt.
func stripComment(s string) string {
	var q rune
	depth := 0
	for i, c := range s {
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case strings.HasPrefix(s[i:], "{{"):
			depth++
		case strings.HasPrefix(s[i:], "}}") && depth > 0:
			depth--
		case c == '#' && depth == 0 && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t'):
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

// fields splitst op spaties; "…" en '…' zijn één veld, en {{ … }} blijft
// bij het woord waar het in staat.
func fields(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	in := false
	var q rune
	depth := 0
	for i := 0; i < len(s); i++ {
		c := rune(s[i])
		switch {
		case q != 0:
			if c == q {
				q = 0
				continue
			}
			b.WriteRune(c)
			continue
		case depth == 0 && (c == '"' || c == '\''):
			q, in = c, true
			continue
		case strings.HasPrefix(s[i:], "{{"):
			depth++
			b.WriteString("{{")
			i++
			in = true
			continue
		case depth > 0 && strings.HasPrefix(s[i:], "}}"):
			depth--
			b.WriteString("}}")
			i++
			continue
		case depth == 0 && (c == ' ' || c == '\t'):
			if in {
				out = append(out, b.String())
				b.Reset()
				in = false
			}
			continue
		}
		b.WriteRune(c)
		in = true
	}
	if q != 0 {
		return nil, fmt.Errorf("aanhalingsteken %c sluit niet", q)
	}
	if depth > 0 {
		return nil, fmt.Errorf("{{ sluit niet met }}")
	}
	if in {
		out = append(out, b.String())
	}
	return out, nil
}

func (p *parser) run(src string) {
	lines := split(src, func(no int, msg string) { p.line = no; p.errorf("%s", msg) })
	if len(lines) == 0 {
		p.line = 1
		p.errorf("het recept is leeg; begin met RECIPE naam versie")
		return
	}
	for i, l := range lines {
		p.line = l.no
		f, err := fields(l.text)
		if err != nil {
			p.errorf("%v", err)
			continue
		}
		op := strings.ToUpper(f[0])
		if f[0] != op {
			p.errorf("instructies staan in hoofdletters: %s", op)
			continue
		}
		if i == 0 && op != "RECIPE" {
			p.errorf("de eerste regel is RECIPE naam versie")
		}
		if l.heredoc != nil && op != "FILE" && !(op == "ONCE" && len(f) > 1 && strings.ToUpper(f[1]) == "FILE") {
			p.errorf("<< kan alleen bij FILE")
			continue
		}
		p.instr(op, f[1:], l)
	}
	if p.r.Name == "" {
		return
	}
	if p.r.From == "" {
		p.line = lines[0].no
		p.errorf("FROM ontbreekt: zeg op welk OS het recept draait, zoals FROM debian:13")
	}
	if len(p.r.Roles) == 0 {
		p.line = lines[len(lines)-1].no
		p.errorf("het recept doet niets: er staat geen enkele stap in")
	}
	for _, r := range p.r.Roles {
		if len(r.Steps) == 0 && len(r.Health) == 0 {
			p.warnf("rol %q heeft geen stappen", r.Name)
		}
	}
}

func (p *parser) instr(op string, args []string, l line) {
	switch op {
	case "RECIPE":
		if p.r.Name != "" {
			p.errorf("RECIPE staat er al")
			return
		}
		if len(args) != 2 {
			p.errorf("RECIPE naam versie, zoals RECIPE zabbix 8.0")
			return
		}
		if !nameRe.MatchString(args[0]) {
			p.errorf("naam %q: kleine letters, cijfers en -", args[0])
		}
		if !versionRe.MatchString(args[1]) {
			p.errorf("versie %q: begint met een cijfer, zoals 8.0 of 1.2.3", args[1])
		}
		p.r.Name, p.r.Version = args[0], args[1]
	case "FROM":
		if len(args) != 1 || !slices.Contains(supportedOS, args[0]) {
			p.errorf("FROM kent %s", strings.Join(supportedOS, ", "))
			return
		}
		p.r.From = args[0]
	case "NEEDS":
		p.needs(args)
	case "PARAM":
		p.param(args)
	case "SECRET":
		p.secret(args)
	case "ROLE":
		if len(args) != 1 || !roleRe.MatchString(args[0]) {
			p.errorf("ROLE naam, met kleine letters, zoals ROLE server")
			return
		}
		for _, r := range p.r.Roles {
			if r.Name == args[0] {
				p.errorf("rol %s staat er al", args[0])
				return
			}
		}
		if len(p.r.Roles) == 1 && p.r.Roles[0].Name == "" {
			p.errorf("ROLE komt voor de eerste stap; stappen zonder ROLE gelden voor het hele recept")
			return
		}
		p.r.Roles = append(p.r.Roles, Role{Name: args[0]})
		p.role = &p.r.Roles[len(p.r.Roles)-1]
		p.since = nil
	case "ONCE":
		if len(args) == 0 {
			p.errorf("ONCE gevolgd door RUN, FILE of SET")
			return
		}
		sub := strings.ToUpper(args[0])
		if sub != "RUN" && sub != "FILE" && sub != "SET" {
			p.errorf("ONCE kan alleen voor RUN, FILE of SET")
			return
		}
		n := len(p.cur().Steps)
		p.instr(sub, args[1:], l)
		for i := n; i < len(p.cur().Steps); i++ {
			p.cur().Steps[i].Once = true
		}
	case "HEALTH":
		p.health(args)
	case "REPO_DEB", "REPO", "APT", "FILE", "SET", "DIR", "USER", "SERVICE", "RUN", "DATABASE", "DBUSER", "IMPORT":
		if p.r.Name == "" {
			return
		}
		p.step(op, args, l)
	default:
		p.errorf("onbekende instructie %s", op)
	}
}

var supportedOS = []string{"debian:12", "debian:13", "ubuntu:24.04"}

func (p *parser) cur() *Role {
	if p.role == nil {
		p.r.Roles = append(p.r.Roles, Role{})
		p.role = &p.r.Roles[0]
	}
	return p.role
}

func (p *parser) needs(args []string) {
	if len(args) == 0 {
		p.errorf("NEEDS vip of NEEDS database mariadb|postgresql")
		return
	}
	var n Need
	switch {
	case args[0] == "vip" && len(args) == 1:
		n = Need{Service: "vip"}
	case args[0] == "database" && len(args) == 2 && (args[1] == "mariadb" || args[1] == "postgresql"):
		n = Need{Service: "database", Engine: args[1]}
	default:
		p.errorf("NEEDS vip of NEEDS database mariadb|postgresql")
		return
	}
	if slices.ContainsFunc(p.r.Needs, func(o Need) bool { return o.Service == n.Service }) {
		p.errorf("NEEDS %s staat er al", n.Service)
		return
	}
	p.r.Needs = append(p.r.Needs, n)
}

func (p *parser) addParam(pr Param) {
	if !identRe.MatchString(pr.Name) {
		p.errorf("naam %q: kleine letters, cijfers en _", pr.Name)
		return
	}
	if pr.Name == "vip" {
		p.errorf("vip is gereserveerd: gebruik {{ vip }}")
		return
	}
	if _, ok := p.params[pr.Name]; ok {
		p.errorf("%s staat er al", pr.Name)
		return
	}
	p.r.Params = append(p.r.Params, pr)
	p.params[pr.Name] = &p.r.Params[len(p.r.Params)-1]
}

func (p *parser) param(args []string) {
	if len(args) < 2 || len(args) > 3 {
		p.errorf("PARAM naam type [standaard], zoals PARAM workers int 4")
		return
	}
	if !paramTypes[args[1]] {
		p.errorf("type %q: kies string, int, bool, ipv4 of size; een wachtwoord is een SECRET", args[1])
		return
	}
	pr := Param{Name: args[0], Type: args[1], Line: p.line}
	if len(args) == 3 {
		pr.Default = args[2]
	}
	if secretKeys.MatchString(pr.Name) && pr.Type == "string" {
		p.warnf("%s klinkt als een geheim; gebruik SECRET, dan staat het versleuteld en nooit in het plan", pr.Name)
	}
	p.addParam(pr)
}

func (p *parser) secret(args []string) {
	if len(args) < 1 || len(args) > 2 {
		p.errorf("SECRET naam [lengte], zoals SECRET db_password 32")
		return
	}
	pr := Param{Name: args[0], Type: "secret", Length: 32, Line: p.line}
	if len(args) == 2 {
		var n int
		if _, err := fmt.Sscanf(args[1], "%d", &n); err != nil || n < 8 || n > 128 {
			p.errorf("lengte tussen 8 en 128")
			return
		}
		pr.Length = n
	}
	p.addParam(pr)
}

func (p *parser) health(args []string) {
	if len(args) == 0 {
		p.errorf("HEALTH service naam | http url [code] | port n | db")
		return
	}
	h := Health{Line: p.line, Kind: args[0]}
	switch {
	case args[0] == "service" && len(args) == 2 && unitRe.MatchString(args[1]):
		h.Target = args[1]
	case args[0] == "http" && (len(args) == 2 || len(args) == 3):
		u, ok := p.expand(args[1])
		if !ok {
			return
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			p.errorf("de URL begint met http:// of https://")
			return
		}
		h.Target, h.Expect = u, 200
		if len(args) == 3 {
			if _, err := fmt.Sscanf(args[2], "%d", &h.Expect); err != nil || h.Expect < 100 || h.Expect > 599 {
				p.errorf("statuscode %q", args[2])
				return
			}
		}
	case args[0] == "port" && len(args) == 2:
		var n int
		if _, err := fmt.Sscanf(args[1], "%d", &n); err != nil || n < 1 || n > 65535 {
			p.errorf("poort %q", args[1])
			return
		}
		h.Target = args[1]
	case args[0] == "db" && len(args) == 1:
		if p.r.NeedsDatabase() == "" {
			p.errorf("HEALTH db vraagt NEEDS database")
			return
		}
	default:
		p.errorf("HEALTH service naam | http url [code] | port n | db")
		return
	}
	r := p.cur()
	r.Health = append(r.Health, h)
}

// expand vertaalt {{ … }} naar de namen van de templates. Geheimen geven
// secret=true.
func (p *parser) expand(s string) (string, bool) {
	out, _, ok := p.expandSecret(s)
	return out, ok
}

func (p *parser) expandSecret(s string) (string, bool, bool) {
	ok, secret := true, false
	out := exprRe.ReplaceAllStringFunc(s, func(m string) string {
		e := strings.TrimSpace(exprRe.FindStringSubmatch(m)[1])
		root, rest, _ := strings.Cut(e, ".")
		switch {
		case e == "vip":
			p.needVIP()
			return "{{ .params.vip }}"
		case e == "node.name":
			return "{{ .node.hostname }}"
		case e == "node.ip":
			return "{{ .node.address }}"
		case e == "cluster.name":
			return "{{ .cluster.name }}"
		case e == "cluster.nodes":
			return "{{ .nodes }}"
		case e == "role.nodes":
			return "{{ .role_nodes }}"
		case e == "db.host" || e == "db.port":
			if p.r.NeedsDatabase() == "" {
				p.errorf("{{ %s }} vraagt NEEDS database (eerder in het recept)", e)
				ok = false
				return m
			}
			return "{{ ." + e + " }}"
		case root == "param" || root == "secret":
			pr := p.params[rest]
			if pr == nil {
				p.errorf("{{ %s }}: %s staat niet eerder als %s in het recept", e, rest, map[string]string{"param": "PARAM", "secret": "SECRET"}[root])
				ok = false
				return m
			}
			if (root == "secret") != (pr.Type == "secret") {
				if pr.Type == "secret" {
					p.errorf("%s is een SECRET: schrijf {{ secret.%s }}", rest, rest)
				} else {
					p.errorf("%s is een PARAM: schrijf {{ param.%s }}", rest, rest)
				}
				ok = false
				return m
			}
			if root == "secret" {
				secret = true
			}
			return "{{ .params." + rest + " }}"
		}
		p.errorf("onbekende waarde {{ %s }}; ken vip, node.name, node.ip, cluster.name, cluster.nodes, role.nodes, db.host, db.port, param.x en secret.x", e)
		ok = false
		return m
	})
	if ok && strings.Contains(out, "{{") && !exprRe.MatchString(out) {
		p.errorf("{{ zonder }}")
		ok = false
	}
	return out, secret, ok
}

func (p *parser) needVIP() {
	if !p.r.NeedsVIP() {
		p.r.Needs = append(p.r.Needs, Need{Service: "vip"})
	}
}

// opts leest KEY waarde-paren en losse vlaggen na de vaste argumenten.
// withValue zegt welke sleutels een waarde hebben.
func (p *parser) opts(args []string, withValue map[string]bool, flags map[string]bool) (map[string]string, bool) {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		k := args[i]
		switch {
		case withValue[k]:
			if i+1 >= len(args) {
				p.errorf("%s heeft een waarde nodig", k)
				return nil, false
			}
			if _, dup := out[k]; dup {
				p.errorf("%s staat er twee keer", k)
				return nil, false
			}
			out[k] = args[i+1]
			i++
		case flags[k]:
			out[k] = ""
		default:
			p.errorf("onverwacht %q", k)
			return nil, false
		}
	}
	return out, true
}

func (p *parser) checkMode(m string) bool {
	if m != "" && !modeRe.MatchString(m) {
		p.errorf("mode %q: octaal, zoals 0644", m)
		return false
	}
	return true
}

func (p *parser) add(s Step) {
	s.Line = p.line
	r := p.cur()
	r.Steps = append(r.Steps, s)
}

var ownerOpts = map[string]bool{"MODE": true, "OWNER": true, "GROUP": true}

func (p *parser) step(op string, args []string, l line) {
	switch op {
	case "REPO_DEB":
		if len(args) != 1 {
			p.errorf("REPO_DEB url van het release-pakket (.deb)")
			return
		}
		u, ok := p.expand(args[0])
		if !ok {
			return
		}
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ".deb") {
			p.errorf("REPO_DEB wil een http(s)-URL naar een .deb")
			return
		}
		if strings.HasPrefix(u, "http://") {
			p.warnf("http zonder s: de agent controleert het pakket niet; gebruik https als het kan")
		}
		p.add(Step{Kind: "repo_deb", Spec: map[string]any{"repo_deb": map[string]any{"url": u}}, Title: "repository uit " + u[strings.LastIndex(u, "/")+1:]})
	case "REPO":
		// REPO naam url suite comp… KEY url
		ki := slices.Index(args, "KEY")
		if ki < 3 || ki != len(args)-2 {
			p.errorf("REPO naam url suite onderdeel… KEY url-van-de-sleutel")
			return
		}
		if !nameRe.MatchString(args[0]) {
			p.errorf("naam %q: kleine letters, cijfers en -", args[0])
			return
		}
		var vals []string
		for _, a := range append(args[1:ki], args[ki+1]) {
			v, ok := p.expand(a)
			if !ok {
				return
			}
			vals = append(vals, v)
		}
		if !strings.HasPrefix(vals[0], "https://") && !strings.HasPrefix(vals[0], "http://") || !strings.HasPrefix(vals[len(vals)-1], "https://") {
			p.errorf("de repository is een http(s)-URL en de sleutel een https-URL")
			return
		}
		p.add(Step{Kind: "repo", Spec: map[string]any{"repo": map[string]any{
			"name": args[0], "url": vals[0], "suite": vals[1], "components": vals[2 : len(vals)-1], "key": vals[len(vals)-1],
		}}, Title: "repository " + args[0]})
	case "APT":
		state := ""
		if len(args) > 0 && args[len(args)-1] == "ABSENT" {
			state, args = "absent", args[:len(args)-1]
		}
		if len(args) == 0 {
			p.errorf("APT pakket…")
			return
		}
		for _, a := range args {
			if !pkgRe.MatchString(a) {
				p.errorf("pakket %q", a)
				return
			}
		}
		spec := map[string]any{"names": slices.Clone(args)}
		t := "pakketten: "
		if state != "" {
			spec["state"] = state
			t = "pakketten verwijderen: "
		}
		p.add(Step{Kind: "package", Spec: map[string]any{"package": spec}, Title: t + strings.Join(args, ", ")})
	case "FILE":
		if len(args) == 0 || !strings.HasPrefix(args[0], "/") {
			p.errorf("FILE /pad [MODE 0644] [OWNER x] [GROUP y] <<EOF")
			return
		}
		if l.heredoc == nil {
			p.errorf("FILE heeft een inhoud nodig: FILE %s <<EOF, dan de regels, dan EOF", args[0])
			return
		}
		o, ok := p.opts(args[1:], ownerOpts, nil)
		if !ok || !p.checkMode(o["MODE"]) {
			return
		}
		content, secret, ok := p.expandSecret(*l.heredoc)
		if !ok {
			return
		}
		f := map[string]any{"path": args[0], "content": content}
		p.ownerSpec(f, o, secret)
		p.add(Step{Kind: "file", Secret: secret, Spec: map[string]any{"file": f}, Title: "bestand " + args[0]})
	case "SET":
		if len(args) < 2 || !strings.HasPrefix(args[0], "/") || !strings.Contains(args[1], "=") {
			p.errorf("SET /pad sleutel=waarde [MODE 0640] [OWNER x] [GROUP y]")
			return
		}
		k, v, _ := strings.Cut(args[1], "=")
		if !iniKeyRe.MatchString(k) {
			p.errorf("sleutel %q", k)
			return
		}
		o, ok := p.opts(args[2:], ownerOpts, nil)
		if !ok || !p.checkMode(o["MODE"]) {
			return
		}
		val, secret, ok := p.expandSecret(v)
		if !ok {
			return
		}
		if !secret && secretKeys.MatchString(k) && v != "" && !strings.Contains(v, "{{") {
			p.errorf("%s ziet eruit als een wachtwoord: zet het in een SECRET en schrijf {{ secret.naam }}", k)
			return
		}
		s := map[string]any{"path": args[0], "key": k, "value": val}
		p.ownerSpec(s, o, secret)
		p.add(Step{Kind: "ini_set", Secret: secret, Spec: map[string]any{"ini_set": s}, Title: fmt.Sprintf("%s in %s", k, args[0])})
	case "DIR":
		if len(args) == 0 || !strings.HasPrefix(args[0], "/") {
			p.errorf("DIR /pad [MODE 0755] [OWNER x] [GROUP y]")
			return
		}
		o, ok := p.opts(args[1:], ownerOpts, nil)
		if !ok || !p.checkMode(o["MODE"]) {
			return
		}
		d := map[string]any{"path": args[0]}
		p.ownerSpec(d, o, false)
		p.add(Step{Kind: "directory", Spec: map[string]any{"directory": d}, Title: "map " + args[0]})
	case "USER":
		if len(args) == 0 || !userRe.MatchString(args[0]) {
			p.errorf("USER naam [SYSTEM] [HOME /pad]")
			return
		}
		o, ok := p.opts(args[1:], map[string]bool{"HOME": true}, map[string]bool{"SYSTEM": true})
		if !ok {
			return
		}
		u := map[string]any{"name": args[0]}
		if _, ok := o["SYSTEM"]; ok {
			u["system"] = true
		}
		if h := o["HOME"]; h != "" {
			u["home"] = h
		}
		p.add(Step{Kind: "user", Spec: map[string]any{"user": u}, Title: "gebruiker " + args[0]})
	case "SERVICE":
		p.service(args)
	case "RUN":
		p.runStep(args, l)
	case "DATABASE", "DBUSER", "IMPORT":
		p.dbStep(op, args)
	}
}

func (p *parser) ownerSpec(m map[string]any, o map[string]string, secret bool) {
	if v := o["MODE"]; v != "" {
		m["mode"] = v
	} else if secret {
		// Een bestand met een geheim leest alleen root (of de groep die je
		// zelf geeft).
		if o["GROUP"] != "" {
			m["mode"] = "0640"
		} else {
			m["mode"] = "0600"
		}
	}
	if v := o["OWNER"]; v != "" {
		m["owner"] = v
	}
	if v := o["GROUP"]; v != "" {
		m["group"] = v
	}
	if secret && o["MODE"] != "" && modeRe.MatchString(o["MODE"]) {
		if mode := o["MODE"]; mode[len(mode)-1] != '0' {
			p.warnf("er staat een geheim in en iedereen op de node mag het lezen (mode %s)", mode)
		}
	}
}

var serviceFlags = map[string]bool{"ENABLE": true, "DISABLE": true, "START": true, "STOP": true, "RELOAD": true, "NORESTART": true}

func (p *parser) service(args []string) {
	var names []string
	i := 0
	for ; i < len(args) && !serviceFlags[args[i]]; i++ {
		if !unitRe.MatchString(args[i]) {
			p.errorf("dienst %q", args[i])
			return
		}
		names = append(names, args[i])
	}
	if len(names) == 0 {
		p.errorf("SERVICE naam… ENABLE|DISABLE START|STOP [RELOAD|NORESTART]")
		return
	}
	o, ok := p.opts(args[i:], nil, serviceFlags)
	if !ok {
		return
	}
	has := func(k string) bool { _, ok := o[k]; return ok }
	if has("ENABLE") && has("DISABLE") || has("START") && has("STOP") || has("RELOAD") && has("NORESTART") {
		p.errorf("tegenstrijdige vlaggen")
		return
	}
	if !has("ENABLE") && !has("DISABLE") && !has("START") && !has("STOP") {
		p.errorf("zeg wat de dienst moet: ENABLE, DISABLE, START of STOP")
		return
	}
	action := "restart"
	if has("RELOAD") {
		action = "reload"
	}
	r := p.cur()
	for _, n := range names {
		// Stappen boven deze regel (sinds de vorige SERVICE-regel voor deze
		// dienst) die iets veranderen, laten de dienst herstarten.
		if has("START") && !has("NORESTART") {
			from := 0
			if p.since != nil {
				from = p.since[n]
			}
			for j := from; j < len(r.Steps); j++ {
				notify(&r.Steps[j], n, action)
			}
		}
		s := map[string]any{"name": n}
		switch {
		case has("ENABLE"):
			s["enabled"] = true
		case has("DISABLE"):
			s["enabled"] = false
		}
		switch {
		case has("START"):
			s["state"] = "started"
		case has("STOP"):
			s["state"] = "stopped"
		}
		p.add(Step{Kind: "service", Spec: map[string]any{"service": s}, Title: "dienst " + n})
		if p.since == nil {
			p.since = map[string]int{}
		}
		p.since[n] = len(r.Steps)
	}
}

// notify zet "service:naam:restart" bij een stap die iets kan veranderen.
func notify(s *Step, name, action string) {
	if s.Once {
		// Een stap op één node laat niet de dienst op alle nodes herstarten.
		return
	}
	switch s.Kind {
	case "file", "ini_set", "command":
	default:
		return
	}
	inner, _ := s.Spec[s.Kind].(map[string]any)
	if inner == nil {
		return
	}
	n := "service:" + name + ":" + action
	list, _ := inner["notify"].([]string)
	for i, o := range list {
		if strings.HasPrefix(o, "service:"+name+":") {
			if action == "restart" {
				list[i] = n
			}
			return
		}
	}
	inner["notify"] = append(list, n)
}

func (p *parser) runStep(args []string, l line) {
	o := map[string]string{}
	i := 0
	for i < len(args) && (args[i] == "CREATES" || args[i] == "UNLESS" || args[i] == "TITLE") {
		if i+1 >= len(args) {
			p.errorf("%s heeft een waarde nodig", args[i])
			return
		}
		o[args[i]] = args[i+1]
		i += 2
	}
	// De rest van de regel is het commando, zoals het er staat (de
	// aanhalingstekens blijven voor de shell).
	cmd := rawAfter(l.text, i+1)
	if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(l.text)), "ONCE") {
		cmd = rawAfter(l.text, i+2)
	}
	if cmd == "" {
		p.errorf("RUN [CREATES /pad] [UNLESS \"commando\"] commando")
		return
	}
	if o["CREATES"] == "" && o["UNLESS"] == "" {
		p.errorf("RUN heeft CREATES /pad of UNLESS \"commando\" nodig, anders loopt het bij elke uitrol opnieuw")
		return
	}
	run, secret, ok := p.expandSecret(cmd)
	if !ok {
		return
	}
	c := map[string]any{"run": run}
	for k, key := range map[string]string{"CREATES": "creates", "UNLESS": "unless"} {
		if v := o[k]; v != "" {
			e, ok := p.expand(v)
			if !ok {
				return
			}
			c[key] = e
		}
	}
	t := o["TITLE"]
	if t == "" {
		t = "commando"
		if !secret {
			t = "commando: " + truncate(cmd, 60)
		}
	}
	c["title"] = t
	if secret {
		p.warnf("een geheim in een commando kan in de proceslijst van de node staan; zet het liever met SET of FILE")
	}
	p.add(Step{Kind: "command", Secret: secret, Spec: map[string]any{"command": c}, Title: t})
}

// rawAfter geeft de tekst van een regel na de eerste n velden.
func rawAfter(s string, n int) string {
	rest := strings.TrimSpace(s)
	for k := 0; k < n && rest != ""; k++ {
		f, err := fields(rest)
		if err != nil || len(f) == 0 {
			return ""
		}
		// Sla het eerste veld over zoals het in de tekst staat.
		rest = strings.TrimSpace(skipField(rest))
	}
	return rest
}

func skipField(s string) string {
	var q rune
	depth := 0
	for i := 0; i < len(s); i++ {
		c := rune(s[i])
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case strings.HasPrefix(s[i:], "{{"):
			depth++
			i++
		case depth > 0 && strings.HasPrefix(s[i:], "}}"):
			depth--
			i++
		case depth == 0 && (c == ' ' || c == '\t'):
			return s[i:]
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (p *parser) dbStep(op string, args []string) {
	engine := p.r.NeedsDatabase()
	if engine == "" {
		p.errorf("%s vraagt NEEDS database mariadb of postgresql (eerder in het recept)", op)
		return
	}
	switch op {
	case "DATABASE":
		if len(args) == 0 || !dbNameRe.MatchString(args[0]) {
			p.errorf("DATABASE naam [CHARSET x] [COLLATE y]")
			return
		}
		o, ok := p.opts(args[1:], map[string]bool{"CHARSET": true, "COLLATE": true}, nil)
		if !ok {
			return
		}
		d := map[string]any{"name": args[0]}
		for k, key := range map[string]string{"CHARSET": "charset", "COLLATE": "collate"} {
			if v := o[k]; v != "" {
				if !charsetRe.MatchString(v) {
					p.errorf("%s %q", k, v)
					return
				}
				d[key] = v
			}
		}
		if engine == "postgresql" && d["collate"] != nil {
			p.warnf("COLLATE geldt alleen voor MariaDB")
		}
		p.add(Step{Kind: "db_database", Once: true, Spec: map[string]any{"db_database": d}, Title: "database " + args[0]})
	case "DBUSER":
		if len(args) == 0 || !dbNameRe.MatchString(args[0]) {
			p.errorf("DBUSER naam PASSWORD {{ secret.x }} [GRANT ALL ON db]")
			return
		}
		rest := args[1:]
		var grant string
		if gi := slices.Index(rest, "GRANT"); gi >= 0 {
			g := rest[gi:]
			if len(g) != 4 || g[1] != "ALL" || g[2] != "ON" || !dbNameRe.MatchString(g[3]) {
				p.errorf("GRANT ALL ON database")
				return
			}
			grant, rest = g[3], rest[:gi]
		}
		o, ok := p.opts(rest, map[string]bool{"PASSWORD": true}, nil)
		if !ok {
			return
		}
		pw := o["PASSWORD"]
		if pw == "" {
			p.errorf("DBUSER heeft PASSWORD {{ secret.naam }} nodig")
			return
		}
		val, secret, ok := p.expandSecret(pw)
		if !ok {
			return
		}
		if !secret || exprRe.ReplaceAllString(pw, "") != "" {
			p.errorf("na PASSWORD komt alleen {{ secret.naam }}, nooit het wachtwoord zelf")
			return
		}
		u := map[string]any{"name": args[0], "password": val}
		if grant != "" {
			u["grant_all_on"] = grant
		}
		p.add(Step{Kind: "db_user", Once: true, Secret: true, Spec: map[string]any{"db_user": u}, Title: "databasegebruiker " + args[0]})
	case "IMPORT":
		if len(args) < 3 || !dbNameRe.MatchString(args[0]) || args[1] != "FROM" || !strings.HasPrefix(args[2], "/") {
			p.errorf("IMPORT database FROM /pad/naar/schema.sql[.gz] [AS gebruiker] [TRUST_FUNCTIONS] [UNLESS \"sql\"]")
			return
		}
		if !strings.HasSuffix(args[2], ".sql") && !strings.HasSuffix(args[2], ".sql.gz") {
			p.errorf("IMPORT leest een .sql- of .sql.gz-bestand")
			return
		}
		o, ok := p.opts(args[3:], map[string]bool{"UNLESS": true, "AS": true}, map[string]bool{"TRUST_FUNCTIONS": true})
		if !ok {
			return
		}
		im := map[string]any{"database": args[0], "path": args[2]}
		if _, ok := o["TRUST_FUNCTIONS"]; ok {
			if engine != "mariadb" {
				p.errorf("TRUST_FUNCTIONS is er alleen voor MariaDB")
				return
			}
			im["trust_functions"] = true
		}
		if u := o["UNLESS"]; u != "" {
			im["unless"] = u
		} else {
			p.warnf("zonder UNLESS laadt de agent het schema alleen als de database nog leeg is")
		}
		if a := o["AS"]; a != "" {
			if !dbNameRe.MatchString(a) {
				p.errorf("AS %q", a)
				return
			}
			im["as"] = a
		}
		p.add(Step{Kind: "db_import", Once: true, Spec: map[string]any{"db_import": im}, Title: "schema in " + args[0]})
	}
}
