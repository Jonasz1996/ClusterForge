package recipes

// Zelf samenstellen (fase 9, M5): rollen met elk een recept, een VIP en een
// database worden samen een gewone template. Zo gebruikt een samengesteld
// cluster dezelfde uitrol, hetzelfde plan, dezelfde drift en GitOps als een
// ingebouwde template. De template staat als tekst in de database
// (blueprints); een nieuwe samenstelling is een nieuwe versie, zodat een
// cluster altijd rendert met de versie waarmee het is uitgerold.

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// TemplatePrefix gaat voor de naam van een samengestelde template, zodat
// ze nooit met een ingebouwde botst.
const TemplatePrefix = "eigen-"

// Composition is een zelf samengesteld cluster.
type Composition struct {
	// Name is de korte naam: kleine letters, cijfers en -. De template heet
	// eigen-<name>.
	Name  string     `json:"name"`
	Title string     `json:"title"`
	Roles []CompRole `json:"roles"`
	// VIP zet keepalived op de nodes van één rol; nil voor geen.
	VIP *CompVIP `json:"vip,omitempty"`
	// Database zet MariaDB Galera op de nodes van één rol; nil voor geen.
	Database *CompDB `json:"database,omitempty"`
}

// CompRole is een rol van het cluster met het recept dat erop komt.
type CompRole struct {
	Name          string `json:"name"`
	Recipe        string `json:"recipe"`
	RecipeVersion string `json:"recipe_version"`
	// RecipeRole is de ROLE uit het recept; leeg voor een recept zonder
	// ROLE.
	RecipeRole string `json:"recipe_role,omitempty"`
	Count      int    `json:"count"`
	// De vorm van een nieuwe VM; leeg is 2 vCPU, 4G en 32G. Bestaande
	// nodes houden hun eigen vorm.
	CPU    int    `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	Disk   string `json:"disk,omitempty"`
}

// CompVIP is het VIP: keepalived op de nodes van Role, en het VIP staat op
// een node waar de controles (HEALTH) slagen.
type CompVIP struct {
	Role string `json:"role"`
}

// CompDB is de database van het cluster: MariaDB Galera op de nodes van
// Role, met de instellingen die erbij horen.
type CompDB struct {
	Role string `json:"role"`
	// BufferPool is innodb_buffer_pool_size per node, zoals 1G; leeg is de
	// standaard van het onderdeel.
	BufferPool     string `json:"buffer_pool,omitempty"`
	MaxConnections int    `json:"max_connections,omitempty"`
	// Extra zijn regels voor [mysqld], zoals max_allowed_packet = 64M. Ze
	// gaan voor de DBCONFIG van de recepten.
	Extra string `json:"extra,omitempty"`
}

// ComposeError is een fout in één veld van de samenstelling.
type ComposeError struct {
	Field string
	Msg   string
}

func (e *ComposeError) Error() string { return e.Field + ": " + e.Msg }

func composeErr(field, format string, args ...any) error {
	return &ComposeError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// RecipeKey is de sleutel van een receptversie in de map van Compose.
func RecipeKey(name, version string) string { return name + "@" + version }

// Composed is een samengestelde template.
type Composed struct {
	YAML     []byte
	Template *templates.Template
	// Warnings zijn dingen die Jonas moet weten, zonder dat ze iets
	// tegenhouden.
	Warnings []string
}

// AppDBFile is het bestand met de DBCONFIG van de recepten en de extra
// regels; het komt voor de instellingen van Galera, die winnen.
const AppDBFile = "/etc/mysql/mariadb.conf.d/90-clusterforge-app.cnf"

var (
	compNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	compRoleRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	sizeRe     = regexp.MustCompile(`^[0-9]+[MGT]$`)
)

type tplParam struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type"`
	Label     string `yaml:"label"`
	Help      string `yaml:"help,omitempty"`
	Default   any    `yaml:"default,omitempty"`
	Optional  bool   `yaml:"optional,omitempty"`
	Immutable bool   `yaml:"immutable,omitempty"`
	Min       any    `yaml:"min,omitempty"`
	Max       any    `yaml:"max,omitempty"`
	Pattern   string `yaml:"pattern,omitempty"`
	Length    int    `yaml:"length,omitempty"`
}

type tplRole struct {
	Name  string            `yaml:"name"`
	Count string            `yaml:"count"`
	VM    map[string]string `yaml:"vm"`
	Steps []map[string]any  `yaml:"steps"`
}

type tplService struct {
	Name      string   `yaml:"name"`
	Kind      string   `yaml:"kind"`
	Unit      string   `yaml:"unit,omitempty"`
	DependsOn []string `yaml:"depends_on,omitempty"`
	Strength  string   `yaml:"strength,omitempty"`
}

type tplFile struct {
	Name        string                  `yaml:"name"`
	Version     string                  `yaml:"version"`
	Title       string                  `yaml:"title"`
	Description string                  `yaml:"description"`
	ClusterType string                  `yaml:"cluster_type"`
	Database    *templates.DatabaseInfo `yaml:"database,omitempty"`
	Params      []tplParam              `yaml:"params"`
	Roles       []tplRole               `yaml:"roles"`
	Checks      []map[string]any        `yaml:"checks,omitempty"`
	Services    []tplService            `yaml:"services,omitempty"`
}

// Compose maakt van een samenstelling een template in versie version (de
// vorm 1.2.3). recipes heeft de gebruikte receptversies, met RecipeKey als
// sleutel.
func Compose(c Composition, version string, recipes map[string]*Recipe) (*Composed, error) {
	if !compNameRe.MatchString(c.Name) || strings.HasSuffix(c.Name, "-") {
		return nil, composeErr("name", "kleine letters, cijfers en -, hoogstens 40 tekens")
	}
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" || len(c.Title) > 80 || strings.ContainsAny(c.Title, "\n\r") {
		return nil, composeErr("title", "geef een titel van hoogstens 80 tekens")
	}
	if len(c.Roles) == 0 || len(c.Roles) > 6 {
		return nil, composeErr("roles", "kies één tot zes rollen")
	}
	out := &Composed{}
	f := tplFile{Name: TemplatePrefix + c.Name, Version: version, Title: c.Title, ClusterType: "generic"}
	params := map[string]tplParam{}
	var order []string
	addParam := func(field string, p tplParam) error {
		if o, ok := params[p.Name]; ok {
			if o.Type != p.Type {
				return composeErr(field, "%s staat in twee recepten met een ander type (%s en %s)", p.Name, o.Type, p.Type)
			}
			return nil
		}
		params[p.Name] = p
		order = append(order, p.Name)
		return nil
	}

	roleIdx := map[string]int{}
	byRole := map[string]*Role{}
	recipeOf := map[string]*Recipe{}
	for i, r := range c.Roles {
		field := fmt.Sprintf("roles.%d", i)
		if !compRoleRe.MatchString(r.Name) {
			return nil, composeErr(field+".name", "kleine letters, cijfers en -, hoogstens 16 tekens")
		}
		if _, dup := roleIdx[r.Name]; dup {
			return nil, composeErr(field+".name", "rol %s staat er twee keer", r.Name)
		}
		roleIdx[r.Name] = i
		if r.Count < 1 || r.Count > 9 {
			return nil, composeErr(field+".count", "tussen 1 en 9 nodes")
		}
		rec := recipes[RecipeKey(r.Recipe, r.RecipeVersion)]
		if rec == nil {
			return nil, composeErr(field+".recipe", "recept %s %s bestaat niet", r.Recipe, r.RecipeVersion)
		}
		ri := slices.IndexFunc(rec.Roles, func(x Role) bool { return x.Name == r.RecipeRole })
		if ri < 0 {
			var names []string
			for _, x := range rec.Roles {
				names = append(names, x.Name)
			}
			return nil, composeErr(field+".recipe_role", "het recept %s heeft de rollen %s", rec.Name, strings.Join(names, ", "))
		}
		byRole[r.Name], recipeOf[r.Name] = &rec.Roles[ri], rec
		for _, p := range rec.Params {
			tp, err := templateParam(p)
			if err != nil {
				return nil, composeErr(field+".recipe", "%s: %v", rec.Name, err)
			}
			if p.Name == "vip" || p.Name == "vrid" || p.Name == "auth_pass" {
				return nil, composeErr(field+".recipe", "%s: de naam %s houdt ClusterForge voor het VIP", rec.Name, p.Name)
			}
			if err := addParam(field+".recipe", tp); err != nil {
				return nil, err
			}
		}
	}

	// Wat de recepten vragen en de samenstelling geeft.
	if c.VIP != nil {
		if _, ok := roleIdx[c.VIP.Role]; !ok {
			return nil, composeErr("vip.role", "kies een van de rollen")
		}
	}
	if c.Database != nil {
		if _, ok := roleIdx[c.Database.Role]; !ok {
			return nil, composeErr("database.role", "kies een van de rollen")
		}
	}
	for i, r := range c.Roles {
		rec := recipeOf[r.Name]
		field := fmt.Sprintf("roles.%d.recipe", i)
		if e := rec.NeedsDatabase(); e != "" {
			switch {
			case e != "mariadb":
				return nil, composeErr(field, "%s vraagt %s; zelf samenstellen kan nu alleen met MariaDB Galera", rec.Name, e)
			case c.Database == nil:
				return nil, composeErr("database", "%s vraagt een database: zet Database aan", rec.Name)
			}
		}
		if rec.NeedsVIP() && c.VIP == nil {
			return nil, composeErr("vip", "%s vraagt een VIP: zet VIP aan", rec.Name)
		}
	}

	// db.host: het VIP als het op de nodes met de database staat, anders
	// localhost als de app op die nodes zelf draait.
	dbHost := func(role string) (string, error) {
		switch {
		case c.Database == nil:
			return "", nil
		case c.VIP != nil && c.VIP.Role == c.Database.Role:
			return "{{ .params.vip }}", nil
		case role == c.Database.Role:
			return "localhost", nil
		}
		return "", composeErr("vip.role", "de rol %s gebruikt de database op %s: zet het VIP op %s, dan verbindt de app via het VIP", role, c.Database.Role, c.Database.Role)
	}

	var dbSteps []templates.DBStep
	var dbConfig []DBSetting
	for i, r := range c.Roles {
		field := fmt.Sprintf("roles.%d", i)
		rec, role := recipeOf[r.Name], byRole[r.Name]
		vals := map[string]string{"{{ .db.port }}": "3306"}
		for _, st := range role.Steps {
			switch st.Kind {
			case "db_database":
				if _, ok := vals["{{ .db.name }}"]; !ok {
					vals["{{ .db.name }}"] = fmt.Sprint(st.Spec["db_database"].(map[string]any)["name"])
				}
			case "db_user":
				if _, ok := vals["{{ .db.user }}"]; !ok {
					vals["{{ .db.user }}"] = fmt.Sprint(st.Spec["db_user"].(map[string]any)["name"])
				}
			}
		}
		tr := tplRole{Name: r.Name, Count: strconv.Itoa(r.Count), VM: vmOf(r), Steps: []map[string]any{}}
		for _, st := range role.Steps {
			if strings.HasPrefix(st.Kind, "db_") {
				if st.Kind == "db_import" && c.Database != nil && r.Name != c.Database.Role {
					return nil, composeErr(field+".recipe", "%s regel %d: IMPORT leest het bestand op een node met de database; zet dit recept op de rol %s", rec.Name, st.Line, c.Database.Role)
				}
				ds, err := dbStepOf(st)
				if err != nil {
					return nil, composeErr(field+".recipe", "%s regel %d: %v", rec.Name, st.Line, err)
				}
				if !slices.Contains(dbSteps, ds) {
					dbSteps = append(dbSteps, ds)
				}
				continue
			}
			spec := cloneMap(st.Spec)
			if st.Once {
				spec["once"] = true
			}
			var err error
			spec = walkStrings(spec, func(s string) string {
				if err != nil {
					return s
				}
				if strings.Contains(s, "{{ .db.host }}") {
					h, e := dbHost(r.Name)
					if e != nil {
						err = e
						return s
					}
					s = strings.ReplaceAll(s, "{{ .db.host }}", h)
				}
				for k, v := range vals {
					s = strings.ReplaceAll(s, k, v)
				}
				for _, k := range []string{"{{ .db.name }}", "{{ .db.user }}"} {
					if strings.Contains(s, k) {
						err = composeErr(field+".recipe", "%s regel %d: %s zonder DATABASE of DBUSER in het recept", rec.Name, st.Line, strings.Trim(k, "{} ."))
					}
				}
				s = strings.ReplaceAll(s, "{{ .nodes }}", allNodes)
				s = strings.ReplaceAll(s, "{{ .role_nodes }}", roleNodes)
				return s
			}).(map[string]any)
			if err != nil {
				return nil, err
			}
			tr.Steps = append(tr.Steps, spec)
		}
		dbConfig = append(dbConfig, rec.DBConfig...)
		f.Roles = append(f.Roles, tr)
	}

	// De database.
	if d := c.Database; d != nil {
		info := &templates.DatabaseInfo{Engine: "mariadb", Role: d.Role, Steps: dbSteps}
		settings := map[string]any{}
		if d.BufferPool != "" {
			b, err := templates.ParseSize(d.BufferPool)
			if !sizeRe.MatchString(d.BufferPool) || err != nil || b < 128<<20 || b > 256<<30 {
				return nil, composeErr("database.buffer_pool", "een grootte tussen 128M en 256G, zoals 1G")
			}
			settings["buffer_pool"] = d.BufferPool
		}
		if d.MaxConnections != 0 {
			if d.MaxConnections < 20 || d.MaxConnections > 5000 {
				return nil, composeErr("database.max_connections", "tussen 20 en 5000")
			}
			settings["max_connections"] = d.MaxConnections
		}
		if len(settings) > 0 {
			info.Settings = settings
		}
		for _, ds := range dbSteps {
			if ds.Kind != "db_user" {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(ds.Password, "{{ .params."), " }}")
			if p, ok := params[name]; ok && p.Length < 16 {
				return nil, composeErr("roles", "het wachtwoord %s van databasegebruiker %s heeft minstens 16 tekens nodig (SECRET %s 32)", name, ds.User, name)
			}
		}
		cnf, warns, err := mysqldConfig(dbConfig, d.Extra)
		if err != nil {
			return nil, err
		}
		out.Warnings = append(out.Warnings, warns...)
		if cnf != "" {
			i := roleIdx[d.Role]
			f.Roles[i].Steps = append([]map[string]any{{"file": map[string]any{"path": AppDBFile, "mode": "0644", "content": cnf}}}, f.Roles[i].Steps...)
		}
		f.Database = info
	}

	// Het VIP.
	if v := c.VIP; v != nil {
		f.Params = append(f.Params,
			tplParam{Name: "vip", Type: "ipv4", Label: "VIP", Immutable: true, Help: "Een vrij adres in hetzelfde netwerk als de nodes."},
			tplParam{Name: "vrid", Type: "int", Label: "VRRP-id", Immutable: true, Optional: true, Min: 1, Max: 255, Help: "Uniek per keepalived-groep in hetzelfde netwerk. Leeg laten kiest een vrij nummer."},
			tplParam{Name: "auth_pass", Type: "secret", Label: "VRRP-wachtwoord", Length: 8, Help: "Leeg laten maakt er een."},
		)
		i := roleIdx[v.Role]
		check := healthScript(byRole[v.Role].Health, c.Database != nil && c.Database.Role == v.Role)
		f.Roles[i].Steps = append(f.Roles[i].Steps,
			map[string]any{"package": map[string]any{"names": []string{"keepalived", "curl"}}},
			map[string]any{"file": map[string]any{"path": "/etc/keepalived/check-cluster.sh", "mode": "0755", "content": check}},
			map[string]any{"file": map[string]any{"path": "/etc/keepalived/keepalived.conf", "mode": "0640", "content": keepalivedConf,
				"notify": []string{"service:keepalived:reload"}}},
			map[string]any{"service": map[string]any{"name": "keepalived", "enabled": true, "state": "started"}},
		)
		f.Checks = append(f.Checks, map[string]any{"vip_owned": map[string]any{"vip": "{{ .params.vip }}", "within": "120s"}})
		f.Services = append(f.Services, tplService{Name: "keepalived", Kind: "vip", Unit: "keepalived"})
	}
	for _, n := range order {
		f.Params = append(f.Params, params[n])
	}
	// De diensten die de recepten starten, voor het overzicht van de
	// afhankelijkheden.
	for _, r := range f.Roles {
		for _, st := range r.Steps {
			s, _ := st["service"].(map[string]any)
			name, _ := s["name"].(string)
			if s == nil || s["state"] != "started" || name == "keepalived" ||
				slices.ContainsFunc(f.Services, func(o tplService) bool { return o.Name == name }) {
				continue
			}
			sv := tplService{Name: name, Kind: "app", Unit: name}
			if c.VIP != nil && r.Name == c.VIP.Role {
				sv.DependsOn, sv.Strength = []string{"keepalived"}, "soft"
			}
			f.Services = append(f.Services, sv)
		}
	}
	f.Description = describe(c)

	b, err := yaml.Marshal(f)
	if err != nil {
		return nil, err
	}
	t, err := templates.Parse(fstest.MapFS{"template.yaml": &fstest.MapFile{Data: b}})
	if err != nil {
		return nil, composeErr("roles", "de samenstelling geeft geen geldige template: %v", err)
	}
	out.YAML, out.Template = b, t
	return out, nil
}

// ParseComposed leest een opgeslagen samengestelde template.
func ParseComposed(yamlText string) (*templates.Template, error) {
	var fsys fs.FS = fstest.MapFS{"template.yaml": &fstest.MapFile{Data: []byte(yamlText)}}
	return templates.Parse(fsys)
}

func vmOf(r CompRole) map[string]string {
	vm := map[string]string{"cpu": "2", "memory": "4G", "disk": "32G"}
	if r.CPU > 0 {
		vm["cpu"] = strconv.Itoa(r.CPU)
	}
	if sizeRe.MatchString(r.Memory) {
		vm["memory"] = r.Memory
	}
	if sizeRe.MatchString(r.Disk) {
		vm["disk"] = r.Disk
	}
	return vm
}

// templateParam zet een PARAM of SECRET om naar een parameter van een
// template.
func templateParam(p Param) (tplParam, error) {
	tp := tplParam{Name: p.Name, Type: p.Type, Label: p.Name}
	switch p.Type {
	case "secret":
		if p.Length > 64 {
			return tp, fmt.Errorf("SECRET %s: hoogstens 64 tekens", p.Name)
		}
		tp.Length = max(p.Length, 6)
		tp.Help = "Leeg laten maakt er een; ClusterForge bewaart het versleuteld."
		return tp, nil
	case "version":
		tp.Type = "string"
		tp.Pattern = strings.TrimSuffix(strings.TrimPrefix(VersionPattern, "^"), "$")
		tp.Help = "Een versie, zoals 7.0 of 8.0."
	}
	if p.Default == "" {
		return tp, nil
	}
	switch p.Type {
	case "int":
		n, err := strconv.Atoi(p.Default)
		if err != nil {
			return tp, fmt.Errorf("PARAM %s: %q is geen getal", p.Name, p.Default)
		}
		tp.Default = n
	case "bool":
		v, err := strconv.ParseBool(p.Default)
		if err != nil {
			return tp, fmt.Errorf("PARAM %s: %q is geen true of false", p.Name, p.Default)
		}
		tp.Default = v
	default:
		tp.Default = p.Default
	}
	return tp, nil
}

func dbStepOf(st Step) (templates.DBStep, error) {
	m, _ := st.Spec[st.Kind].(map[string]any)
	str := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	switch st.Kind {
	case "db_database":
		return templates.DBStep{Kind: st.Kind, Database: str("name"), Charset: str("charset"), Collation: str("collate")}, nil
	case "db_user":
		return templates.DBStep{Kind: st.Kind, User: str("name"), Password: str("password"), GrantOn: str("grant_all_on")}, nil
	case "db_import":
		if str("as") != "" {
			return templates.DBStep{}, fmt.Errorf("IMPORT … AS kan nog niet bij zelf samenstellen")
		}
		tf, _ := m["trust_functions"].(bool)
		return templates.DBStep{Kind: st.Kind, Database: str("database"), Path: str("path"), TrustFunctions: tf, Unless: str("unless")}, nil
	}
	return templates.DBStep{}, fmt.Errorf("onbekende database-instructie %s", st.Kind)
}

// mysqldConfig maakt het bestand met de DBCONFIG van de recepten en de
// extra regels. Bij twee recepten met dezelfde sleutel wint MIN de grootste
// waarde; extra regels winnen altijd.
func mysqldConfig(recipe []DBSetting, extra string) (string, []string, error) {
	vals := map[string]string{}
	var keys, warns []string
	for _, s := range recipe {
		o, ok := vals[s.Key]
		switch {
		case !ok:
			keys = append(keys, s.Key)
			vals[s.Key] = s.Value
		case o == s.Value:
		case s.Min:
			if bigger(s.Value, o) {
				vals[s.Key] = s.Value
			}
		default:
			return "", nil, composeErr("database.extra", "twee recepten zetten %s anders (%s en %s); zet de waarde in Extra", s.Key, o, s.Value)
		}
	}
	for i, l := range strings.Split(extra, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") || l == "[mysqld]" {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		k = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(k)), "-", "_")
		v = strings.TrimSpace(v)
		if !ok || !identRe.MatchString(k) || !dbValueRe.MatchString(v) {
			return "", nil, composeErr("database.extra", "regel %d: sleutel = waarde, zoals max_allowed_packet = 64M", i+1)
		}
		if why, ok := managedDBKeys[k]; ok || strings.HasPrefix(k, "wsrep_") {
			if !ok {
				why = "ClusterForge beheert de Galera-instellingen"
			}
			if k == "innodb_buffer_pool_size" || k == "max_connections" {
				why = "kies het in de velden Buffer pool of Max. verbindingen"
			}
			return "", nil, composeErr("database.extra", "regel %d: %s kan hier niet: %s", i+1, k, why)
		}
		if o, ok := vals[k]; ok && o != v {
			warns = append(warns, fmt.Sprintf("%s = %s uit Extra gaat voor %s uit het recept", k, v, o))
		} else if !ok {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	if len(keys) == 0 {
		return "", warns, nil
	}
	var b strings.Builder
	b.WriteString("# Beheerd door ClusterForge: de instellingen die de apps van dit cluster\n# vragen. De instellingen van Galera staan in 99-clusterforge-galera.cnf.\n[mysqld]\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, vals[k])
	}
	return b.String(), warns, nil
}

// bigger vergelijkt twee waarden als getal of grootte (64M, 1G); anders als
// tekst.
func bigger(a, b string) bool {
	x, e1 := templates.ParseSize(a)
	y, e2 := templates.ParseSize(b)
	if e1 == nil && e2 == nil {
		return x > y
	}
	n, e1 := strconv.ParseFloat(a, 64)
	m, e2 := strconv.ParseFloat(b, 64)
	if e1 == nil && e2 == nil {
		return n > m
	}
	return a > b
}

// healthScript is de controle van keepalived: het VIP staat alleen op een
// node waar de HEALTH-regels slagen, en met de database erbij alleen op
// een node waarvan MariaDB mag schrijven.
func healthScript(health []Health, db bool) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# Beheerd door ClusterForge (cluster {{ .cluster.slug }}). keepalived vraagt\n# dit elke twee seconden; faalt het, dan geeft deze node het VIP af.\n")
	for _, h := range health {
		switch h.Kind {
		case "service":
			fmt.Fprintf(&b, "/usr/bin/systemctl is-active --quiet %s || exit 1\n", h.Target)
		case "http":
			if !safeURL(h.Target) {
				continue
			}
			fmt.Fprintf(&b, "[ \"$(curl -s -o /dev/null -m 2 -w '%%{http_code}' '%s')\" = %d ] || exit 1\n", h.Target, h.Expect)
		case "port":
			fmt.Fprintf(&b, "timeout 2 bash -c '</dev/tcp/127.0.0.1/%s' 2>/dev/null || exit 1\n", h.Target)
		case "db":
			db = true
		}
	}
	if db {
		b.WriteString("# De database komt na de uitrol; tot dan telt ze niet mee.\n[ -e /etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf ] || exit 0\nexec /usr/local/bin/cf-agent check db mariadb\n")
	} else {
		b.WriteString("exit 0\n")
	}
	return b.String()
}

var (
	urlExprRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	urlRe     = regexp.MustCompile(`^https?://[A-Za-z0-9._~:/?#\[\]@!$&()*+,;=%-]+$`)
)

// safeURL zegt of een URL veilig tussen enkele aanhalingstekens in het
// script kan: alleen het VIP, het adres of de naam van de node mag erin.
func safeURL(u string) bool {
	for _, m := range urlExprRe.FindAllString(u, -1) {
		if m != "{{ .params.vip }}" && m != "{{ .node.address }}" && m != "{{ .node.hostname }}" {
			return false
		}
	}
	return urlRe.MatchString(urlExprRe.ReplaceAllString(u, "x")) && !strings.Contains(u, "'")
}

const keepalivedConf = `# Beheerd door ClusterForge (cluster {{ .cluster.slug }}). Wijzigingen hier
# worden bij de volgende uitrol overschreven.

global_defs {
    router_id {{ .node.hostname }}
    enable_script_security
    script_user root
}

vrrp_script chk_cluster {
    script "/etc/keepalived/check-cluster.sh"
    interval 2
    timeout 2
    fall 2
    rise 2
}

vrrp_instance VI_{{ .params.vrid }} {
    state BACKUP
    nopreempt
    interface {{ .node.interface }}
    virtual_router_id {{ .params.vrid }}
    priority {{ sub 160 (mul .node.index 10) }}
    advert_int 1
    unicast_src_ip {{ .node.address }}
    unicast_peer {
{{- range .peers }}
        {{ .address }}
{{- end }}
    }
    authentication {
        auth_type PASS
        auth_pass {{ .params.auth_pass }}
    }
    virtual_ipaddress {
        {{ .params.vip }}/{{ .node.prefix }} dev {{ .node.interface }}
    }
    track_script {
        chk_cluster
    }
}
`

// De adressen van alle nodes of van de nodes van de eigen rol, met een
// spatie ertussen ({{ cluster.nodes }} en {{ role.nodes }} in een recept).
const (
	allNodes  = `{{ range $i, $n := .nodes }}{{ if $i }} {{ end }}{{ $n.address }}{{ end }}`
	roleNodes = `{{ $r := .node.role }}{{ $s := "" }}{{ range .nodes }}{{ if eq .role $r }}{{ $s }}{{ .address }}{{ $s = " " }}{{ end }}{{ end }}`
)

func describe(c Composition) string {
	var parts []string
	for _, r := range c.Roles {
		parts = append(parts, fmt.Sprintf("%s: %d× %s %s", r.Name, r.Count, r.Recipe, r.RecipeVersion))
	}
	s := "Zelf samengesteld uit recepten. " + strings.Join(parts, "; ") + "."
	if c.VIP != nil {
		s += " VIP op " + c.VIP.Role + "."
	}
	if c.Database != nil {
		s += " MariaDB Galera op " + c.Database.Role + "."
	}
	return s
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// walkStrings past fn toe op elke tekst in v en geeft een kopie.
func walkStrings(v any, fn func(string) string) any {
	switch x := v.(type) {
	case string:
		return fn(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = walkStrings(e, fn)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = walkStrings(e, fn)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = fn(e)
		}
		return out
	}
	return v
}
