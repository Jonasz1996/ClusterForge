// Package templates leest clustertemplates: YAML met parameters, de vorm
// van de VM's per rol en de stappen die de agent op elke node uitvoert. Uit
// een template en ingevulde parameters rendert de server per node de
// stappen en bestanden.
package templates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Template is een geladen clustertemplate.
type Template struct {
	Name        string    `yaml:"name"`
	Version     string    `yaml:"version"`
	Title       string    `yaml:"title"`
	Description string    `yaml:"description"`
	ClusterType string    `yaml:"cluster_type"`
	Params      []Param   `yaml:"params"`
	Roles       []Role    `yaml:"roles"`
	Checks      []any     `yaml:"checks"`
	Services    []Service `yaml:"services"`

	files *template.Template
}

// Service is een dienst die een uitrol in de afhankelijkheidsgraaf zet. De
// soort komt uit de vaste lijst van internal/deps; een test daar controleert
// de ingebouwde templates.
type Service struct {
	Name string `yaml:"name" json:"name"`
	Kind string `yaml:"kind" json:"kind"`
	// Unit is de systemd-unit zonder .service; leeg als de dienst er geen
	// heeft.
	Unit string `yaml:"unit" json:"unit,omitempty"`
	Port int    `yaml:"port" json:"port,omitempty"`
	// DependsOn zijn de diensten uit dezelfde template waarvan deze afhangt,
	// met Strength hard (standaard) of soft.
	DependsOn []string `yaml:"depends_on" json:"depends_on,omitempty"`
	Strength  string   `yaml:"strength" json:"strength,omitempty"`
}

// Role is een groep gelijke nodes, zoals de webservers.
type Role struct {
	Name  string `yaml:"name"`
	Count string `yaml:"count"`
	VM    struct {
		CPU    string `yaml:"cpu"`
		Memory string `yaml:"memory"`
		Disk   string `yaml:"disk"`
	} `yaml:"vm"`
	Steps []any `yaml:"steps"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	roleRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// Parse leest een template uit een map met template.yaml en eventueel
// files/ met de bestandssjablonen.
func Parse(fsys fs.FS) (*Template, error) {
	b, err := fs.ReadFile(fsys, "template.yaml")
	if err != nil {
		return nil, err
	}
	var t Template
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("template.yaml: %w", err)
	}
	switch {
	case !nameRe.MatchString(t.Name):
		return nil, fmt.Errorf("ongeldige naam %q", t.Name)
	case !versionRe.MatchString(t.Version):
		return nil, fmt.Errorf("versie %q moet de vorm 1.2.3 hebben", t.Version)
	case t.Title == "" || t.ClusterType == "":
		return nil, errors.New("title en cluster_type zijn verplicht")
	case len(t.Roles) == 0:
		return nil, errors.New("een template heeft minstens één rol")
	}
	seen := map[string]bool{}
	for i := range t.Params {
		p := &t.Params[i]
		if err := p.check(); err != nil {
			return nil, fmt.Errorf("parameter %s: %w", p.Name, err)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("parameter %s staat er twee keer in", p.Name)
		}
		seen[p.Name] = true
	}
	roles := map[string]bool{}
	for _, r := range t.Roles {
		// De rol komt in hostnames (<slug>-<rol>-01), dus kort.
		if !roleRe.MatchString(r.Name) || roles[r.Name] {
			return nil, fmt.Errorf("ongeldige of dubbele rol %q (kleine letters, cijfers en streepjes, hoogstens 16 tekens)", r.Name)
		}
		roles[r.Name] = true
	}
	if err := t.checkServices(); err != nil {
		return nil, err
	}
	if err := t.checkUnsafe(); err != nil {
		return nil, err
	}
	t.files = template.New("files").Funcs(funcs).Option("missingkey=error")
	if sub, err := fs.Sub(fsys, "files"); err == nil {
		err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(sub, p)
			if err != nil {
				return err
			}
			_, err = t.files.New(p).Parse(string(b))
			return err
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("files: %w", err)
		}
	}
	// Alles een keer renderen met voorbeeldwaarden vangt fouten in de
	// sjablonen nu al, niet pas bij een uitrol.
	if err := t.selfTest(); err != nil {
		return nil, err
	}
	return &t, nil
}

var unitRe = regexp.MustCompile(`^[A-Za-z0-9@._:-]{1,63}$`)

// checkServices controleert de diensten: unieke namen, en depends_on wijst
// naar een andere dienst uit dezelfde template.
func (t *Template) checkServices() error {
	names := map[string]bool{}
	for _, s := range t.Services {
		if !unitRe.MatchString(s.Name) || names[s.Name] {
			return fmt.Errorf("dienst %q: de naam ontbreekt, is ongeldig of staat er twee keer in", s.Name)
		}
		names[s.Name] = true
	}
	for _, s := range t.Services {
		switch {
		case s.Kind == "":
			return fmt.Errorf("dienst %s: kind is verplicht", s.Name)
		case s.Unit != "" && !unitRe.MatchString(s.Unit):
			return fmt.Errorf("dienst %s: ongeldige unit %q", s.Name, s.Unit)
		case s.Port < 0 || s.Port > 65535:
			return fmt.Errorf("dienst %s: poort %d bestaat niet", s.Name, s.Port)
		case s.Strength != "" && s.Strength != "hard" && s.Strength != "soft":
			return fmt.Errorf("dienst %s: strength is hard of soft", s.Name)
		case s.Strength != "" && len(s.DependsOn) == 0:
			return fmt.Errorf("dienst %s: strength zonder depends_on", s.Name)
		}
		seen := map[string]bool{}
		for _, d := range s.DependsOn {
			switch {
			case d == s.Name:
				return fmt.Errorf("dienst %s hangt van zichzelf af", s.Name)
			case !names[d]:
				return fmt.Errorf("dienst %s hangt af van %q, maar die dienst staat niet in de template", s.Name, d)
			case seen[d]:
				return fmt.Errorf("dienst %s noemt %s twee keer in depends_on", s.Name, d)
			}
			seen[d] = true
		}
	}
	return nil
}

// --- renderen ---

var funcs = template.FuncMap{
	"add":  func(a, b int) int { return a + b },
	"sub":  func(a, b int) int { return a - b },
	"mul":  func(a, b int) int { return a * b },
	"join": strings.Join,
}

// NodeInfo beschrijft een node voor de sjablonen.
type NodeInfo struct {
	Hostname  string `json:"hostname"`
	Role      string `json:"role"`
	Index     int    `json:"index"`
	Address   string `json:"address"`
	Prefix    int    `json:"prefix"`
	Interface string `json:"interface"`
}

// Context is wat een sjabloon ziet: .params, .cluster, .node, .nodes en
// .peers (de andere nodes met dezelfde rol).
type Context struct {
	Params  map[string]any
	Cluster ClusterInfo
	Node    *NodeInfo
	Nodes   []NodeInfo
}

type ClusterInfo struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Environment string `json:"environment"`
}

func (n NodeInfo) data() map[string]any {
	return map[string]any{
		"hostname": n.Hostname, "role": n.Role, "index": n.Index, "address": n.Address,
		"prefix": n.Prefix, "interface": n.Interface,
	}
}

func (c Context) data() map[string]any {
	nodes := make([]any, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		nodes = append(nodes, n.data())
	}
	d := map[string]any{
		"params":  c.Params,
		"cluster": map[string]any{"name": c.Cluster.Name, "slug": c.Cluster.Slug, "environment": c.Cluster.Environment},
		"nodes":   nodes,
	}
	if c.Node != nil {
		d["node"] = c.Node.data()
		peers := []any{}
		for _, n := range c.Nodes {
			if n.Role == c.Node.Role && n.Hostname != c.Node.Hostname {
				peers = append(peers, n.data())
			}
		}
		d["peers"] = peers
	}
	return d
}

// expandString rendert één tekst met {{ }}; tekst zonder blijft zoals hij is.
func expandString(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	tpl, err := template.New("").Funcs(funcs).Option("missingkey=error").Parse(s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	return noEmpty(b.String())
}

// noEmpty weigert uitvoer met een lege parameter; text/template schrijft
// daar "<no value>".
func noEmpty(s string) (string, error) {
	if strings.Contains(s, "<no value>") {
		return "", errors.New("een gebruikte parameter is leeg")
	}
	return s, nil
}

// expand rendert alle teksten in een YAML-boom.
func expand(v any, data map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		return expandString(x, data)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := expand(e, data)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := expand(e, data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = r
		}
		return out, nil
	}
	return v, nil
}

// decodeStrict zet een gerenderde boom om naar een struct; onbekende velden
// zijn een fout.
func decodeStrict(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

// Counts geeft het aantal nodes per rol, in de volgorde van de template.
func (t *Template) Counts(c Context) ([]int, error) {
	data := c.data()
	out := make([]int, len(t.Roles))
	for i, r := range t.Roles {
		s, err := expandString(r.Count, data)
		if err != nil {
			return nil, fmt.Errorf("rol %s: count: %w", r.Name, err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > 20 {
			return nil, fmt.Errorf("rol %s: ongeldig aantal %q (1 tot 20)", r.Name, s)
		}
		out[i] = n
	}
	return out, nil
}

// VMShape is de vorm van een nieuwe VM.
type VMShape struct {
	CPU       int `json:"cpu"`
	MemoryMiB int `json:"memory_mib"`
	DiskGiB   int `json:"disk_gib"`
}

// VM geeft de vorm van de VM's van een rol.
func (t *Template) VM(role string, c Context) (VMShape, error) {
	r := t.role(role)
	if r == nil {
		return VMShape{}, fmt.Errorf("onbekende rol %s", role)
	}
	data := c.data()
	var vm VMShape
	cpu, err := expandString(r.VM.CPU, data)
	if err != nil {
		return vm, err
	}
	if vm.CPU, err = strconv.Atoi(strings.TrimSpace(cpu)); err != nil || vm.CPU < 1 || vm.CPU > 128 {
		return vm, fmt.Errorf("rol %s: ongeldig aantal vCPU %q", role, cpu)
	}
	mem, err := expandString(r.VM.Memory, data)
	if err != nil {
		return vm, err
	}
	mb, err := ParseSize(mem)
	if err != nil || mb < 256<<20 {
		return vm, fmt.Errorf("rol %s: ongeldig geheugen %q", role, mem)
	}
	vm.MemoryMiB = int(mb >> 20)
	disk, err := expandString(r.VM.Disk, data)
	if err != nil {
		return vm, err
	}
	db, err := ParseSize(disk)
	if err != nil || db < 1<<30 || db%(1<<30) != 0 {
		return vm, fmt.Errorf("rol %s: schijf %q moet een geheel aantal GB zijn", role, disk)
	}
	vm.DiskGiB = int(db >> 30)
	return vm, nil
}

func (t *Template) role(name string) *Role {
	for i := range t.Roles {
		if t.Roles[i].Name == name {
			return &t.Roles[i]
		}
	}
	return nil
}

// Step is een gerenderde stap met wat de server erbij moet weten.
type Step struct {
	protocol.Step
	// Title is een korte omschrijving voor het logboek.
	Title string
	// Notify zijn services die herladen of herstart moeten worden als deze
	// stap iets veranderde.
	Notify []protocol.ServiceStep
}

type stepSpec struct {
	Package   *protocol.PackageStep   `json:"package"`
	File      *fileSpec               `json:"file"`
	Service   *protocol.ServiceStep   `json:"service"`
	User      *protocol.UserStep      `json:"user"`
	Directory *protocol.DirectoryStep `json:"directory"`
	Command   *protocol.CommandStep   `json:"command"`
}

type fileSpec struct {
	Path     string   `json:"path"`
	Content  *string  `json:"content"`
	Template string   `json:"template"`
	Mode     string   `json:"mode"`
	Owner    string   `json:"owner"`
	Group    string   `json:"group"`
	Notify   []string `json:"notify"`
}

var notifyActions = map[string]string{"reload": "reloaded", "restart": "restarted"}

// Steps rendert de stappen van een rol voor één node.
func (t *Template) Steps(role string, c Context) ([]Step, error) {
	r := t.role(role)
	if r == nil {
		return nil, fmt.Errorf("onbekende rol %s", role)
	}
	if c.Node == nil {
		return nil, errors.New("geen node om voor te renderen")
	}
	data := c.data()
	out := make([]Step, 0, len(r.Steps))
	for i, raw := range r.Steps {
		v, err := expand(raw, data)
		if err != nil {
			return nil, fmt.Errorf("stap %d: %w", i+1, err)
		}
		var spec stepSpec
		if err := decodeStrict(v, &spec); err != nil {
			return nil, fmt.Errorf("stap %d: %w", i+1, err)
		}
		s := Step{Step: protocol.Step{
			Package: spec.Package, Service: spec.Service, User: spec.User, Directory: spec.Directory, Command: spec.Command,
		}}
		if f := spec.File; f != nil {
			content, err := t.fileContent(f, data)
			if err != nil {
				return nil, fmt.Errorf("stap %d: %w", i+1, err)
			}
			s.File = &protocol.FileStep{Path: f.Path, Content: content, Mode: f.Mode, Owner: f.Owner, Group: f.Group}
			for _, n := range f.Notify {
				// service:keepalived:reload
				parts := strings.Split(n, ":")
				if len(parts) != 3 || parts[0] != "service" || notifyActions[parts[2]] == "" {
					return nil, fmt.Errorf("stap %d: notify %q moet de vorm service:naam:reload of service:naam:restart hebben", i+1, n)
				}
				s.Notify = append(s.Notify, protocol.ServiceStep{Name: parts[1], State: notifyActions[parts[2]]})
			}
		}
		if s.Kind() == "" {
			return nil, fmt.Errorf("stap %d heeft precies één soort nodig (package, file, service, user, directory of command)", i+1)
		}
		s.Title = title(s.Step)
		out = append(out, s)
	}
	return out, nil
}

func (t *Template) fileContent(f *fileSpec, data map[string]any) (string, error) {
	switch {
	case f.Content != nil && f.Template != "":
		return "", errors.New("een bestand heeft content of template, niet allebei")
	case f.Content != nil:
		return *f.Content, nil
	case f.Template == "":
		return "", fmt.Errorf("bestand %s heeft content of template nodig", f.Path)
	}
	tpl := t.files.Lookup(f.Template)
	if tpl == nil {
		return "", fmt.Errorf("sjabloon %s bestaat niet in files/", f.Template)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	out, err := noEmpty(b.String())
	if err != nil {
		return "", fmt.Errorf("%s: %w", f.Template, err)
	}
	return out, nil
}

// title is een korte omschrijving van een stap voor het logboek.
func title(s protocol.Step) string {
	switch s.Kind() {
	case "package":
		if s.Package.State == "absent" {
			return "pakketten verwijderen: " + strings.Join(s.Package.Names, ", ")
		}
		return "pakketten: " + strings.Join(s.Package.Names, ", ")
	case "file":
		return "bestand " + s.File.Path
	case "service":
		return "service " + s.Service.Name
	case "user":
		return "gebruiker " + s.User.Name
	case "directory":
		return "map " + s.Directory.Path
	case "command":
		return "commando"
	}
	return "stap"
}

// Check is een controle na de uitrol.
type Check struct {
	// VIPOwned wacht tot een node de VIP heeft.
	VIPOwned *struct {
		VIP    string `json:"vip"`
		Within string `json:"within"`
	} `json:"vip_owned,omitempty"`
	// HTTP vraagt een URL op en verwacht een statuscode.
	HTTP *struct {
		URL    string `json:"url"`
		Expect int    `json:"expect"`
		Within string `json:"within"`
	} `json:"http,omitempty"`
}

// Within geeft hoe lang de controle mag duren.
func (c Check) Within() time.Duration {
	s := ""
	switch {
	case c.VIPOwned != nil:
		s = c.VIPOwned.Within
	case c.HTTP != nil:
		s = c.HTTP.Within
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Minute
	}
	return min(d, 10*time.Minute)
}

// RenderChecks rendert de controles voor het hele cluster.
func (t *Template) RenderChecks(c Context) ([]Check, error) {
	data := c.data()
	out := make([]Check, 0, len(t.Checks))
	for i, raw := range t.Checks {
		v, err := expand(raw, data)
		if err != nil {
			return nil, fmt.Errorf("controle %d: %w", i+1, err)
		}
		var ch Check
		if err := decodeStrict(v, &ch); err != nil {
			return nil, fmt.Errorf("controle %d: %w", i+1, err)
		}
		if (ch.VIPOwned == nil) == (ch.HTTP == nil) {
			return nil, fmt.Errorf("controle %d heeft precies één soort nodig (vip_owned of http)", i+1)
		}
		out = append(out, ch)
	}
	return out, nil
}

// selfTest rendert alles met voorbeeldwaarden.
func (t *Template) selfTest() error {
	params := map[string]any{}
	for _, p := range t.Params {
		params[p.Name] = p.sample()
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Voorbeeld", Slug: "voorbeeld", Environment: "lab"}}
	counts, err := t.Counts(c)
	if err != nil {
		return err
	}
	for i, r := range t.Roles {
		for n := 1; n <= counts[i]; n++ {
			c.Nodes = append(c.Nodes, NodeInfo{
				Hostname: fmt.Sprintf("voorbeeld-%s-%02d", r.Name, n), Role: r.Name, Index: n,
				Address: fmt.Sprintf("192.0.2.%d", len(c.Nodes)+10), Prefix: 24, Interface: "eth0",
			})
		}
	}
	for _, r := range t.Roles {
		if _, err := t.VM(r.Name, c); err != nil {
			return err
		}
		node := slices.IndexFunc(c.Nodes, func(n NodeInfo) bool { return n.Role == r.Name })
		c.Node = &c.Nodes[node]
		if _, err := t.Steps(r.Name, c); err != nil {
			return fmt.Errorf("rol %s: %w", r.Name, err)
		}
	}
	c.Node = nil
	_, err = t.RenderChecks(c)
	return err
}
