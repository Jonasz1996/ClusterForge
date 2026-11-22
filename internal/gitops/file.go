package gitops

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FieldError is een fout in een clusterbestand, met het veld en de regel.
// Message noemt nooit de waarde van een geheim.
type FieldError struct {
	Field   string `json:"field"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func (e FieldError) String() string {
	if e.Line > 0 {
		return fmt.Sprintf("regel %d, %s: %s", e.Line, e.Field, e.Message)
	}
	return e.Field + ": " + e.Message
}

// File is een gelezen cluster.yaml.
type File struct {
	Format   int
	Cluster  FileCluster
	Template FileTemplate
	// Params zijn de waarden zoals ze in het bestand staan; nil voor een
	// lege waarde. Has zegt welke er staan.
	Params map[string]any
	Target *FileTarget

	lines map[string]int
}

type FileCluster struct {
	Name        string
	Slug        string
	Environment string
	Description string
	Tags        []string
}

type FileTemplate struct {
	Name    string
	Version string
}

// FileTarget zegt waar de VM's van een nieuw cluster komen. Bij een
// bestaand cluster wordt het genegeerd.
type FileTarget struct {
	Proxmox   string
	ImageVMID int
	Storage   string
	Bridge    string
	VLAN      *int
	FirstIP   string
	Gateway   string
	DNS       []string
	SSHKeys   []string
}

// Line geeft de regel van een veld zoals cluster.name of params.vip; voor
// een veld dat ontbreekt de regel van het dichtstbijzijnde veld erboven.
func (f *File) Line(field string) int {
	for field != "" {
		if l, ok := f.lines[field]; ok {
			return l
		}
		i := strings.LastIndexByte(field, '.')
		if i < 0 {
			break
		}
		field = field[:i]
	}
	return 1
}

// HasParam zegt of een parameter in het bestand staat.
func (f *File) HasParam(name string) bool {
	_, ok := f.lines["params."+name]
	return ok
}

var yamlLineRe = regexp.MustCompile(`line (\d+): (.*)$`)

// yamlMessages zegt de meest voorkomende fouten van de YAML-lezer in
// gewone woorden.
var yamlMessages = map[string]string{
	"mapping values are not allowed in this context":  "hier mag geen tweede dubbele punt staan; zet de waarde tussen aanhalingstekens",
	"found a tab character that violates indentation": "een tab in de inspringing; gebruik spaties",
	"did not find expected key":                       "hier verwachtte ik een veldnaam; klopt de inspringing?",
	"found unexpected end of stream":                  "het bestand houdt onverwacht op; is een aanhalingsteken niet gesloten?",
	"did not find expected ',' or ']'":                "een lijst met [ is niet gesloten met ]",
	"did not find expected ',' or '}'":                "een map met { is niet gesloten met }",
	"could not find expected ':'":                     "na een veldnaam hoort een dubbele punt",
}

func yamlMessage(msg string) string {
	if nl, ok := yamlMessages[msg]; ok {
		return nl
	}
	return msg
}

// parser loopt door de YAML-knopen en verzamelt fouten met hun regel.
type parser struct {
	f    *File
	errs []FieldError
}

func (p *parser) fail(field string, line int, format string, a ...any) {
	p.errs = append(p.errs, FieldError{Field: field, Line: line, Message: fmt.Sprintf(format, a...)})
}

// Parse leest een clusterbestand. Het is strikt: een onbekend veld, een
// veld dat twee keer staat of een waarde van de verkeerde soort is een
// fout. Parse controleert alleen de vorm; Check de inhoud.
func Parse(data []byte) (*File, []FieldError) {
	f := &File{Params: map[string]any{}, lines: map[string]int{}}
	if len(data) > MaxFileSize {
		return nil, []FieldError{{Field: "bestand", Message: fmt.Sprintf("het bestand is groter dan %d KB", MaxFileSize>>10)}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		line, msg := 0, err.Error()
		if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
			line, _ = strconv.Atoi(m[1])
			msg = m[2]
		}
		return nil, []FieldError{{Field: "bestand", Line: line, Message: "geen geldige YAML: " + yamlMessage(strings.TrimPrefix(msg, "yaml: "))}}
	}
	if len(doc.Content) == 0 {
		return nil, []FieldError{{Field: "bestand", Line: 1, Message: "het bestand is leeg"}}
	}
	root := doc.Content[0]
	p := &parser{f: f}
	if root.Kind != yaml.MappingNode {
		p.fail("bestand", root.Line, "het bestand moet beginnen met velden als clusterforge, cluster en template")
		return nil, p.errs
	}
	p.mapping(root, "", func(key string, v *yaml.Node) bool {
		switch key {
		case "clusterforge":
			if n, ok := p.int(v, key); ok {
				f.Format = n
			}
		case "cluster":
			p.mapping(v, key, func(k string, v *yaml.Node) bool {
				field := key + "." + k
				switch k {
				case "name":
					f.Cluster.Name, _ = p.str(v, field)
				case "slug":
					f.Cluster.Slug, _ = p.str(v, field)
				case "environment":
					f.Cluster.Environment, _ = p.str(v, field)
				case "description":
					f.Cluster.Description, _ = p.str(v, field)
				case "tags":
					f.Cluster.Tags = p.strs(v, field)
				default:
					return false
				}
				return true
			})
		case "template":
			p.mapping(v, key, func(k string, v *yaml.Node) bool {
				field := key + "." + k
				switch k {
				case "name":
					f.Template.Name, _ = p.str(v, field)
				case "version":
					f.Template.Version, _ = p.str(v, field)
				default:
					return false
				}
				return true
			})
		case "params":
			p.mapping(v, key, func(k string, v *yaml.Node) bool {
				field := key + "." + k
				if v.Kind != yaml.ScalarNode {
					p.fail(field, v.Line, "een parameter is één waarde, geen lijst of map")
					return true
				}
				var val any
				if err := v.Decode(&val); err != nil {
					p.fail(field, v.Line, "onleesbare waarde")
					return true
				}
				f.Params[k] = val
				return true
			})
		case "target":
			f.Target = &FileTarget{}
			p.target(v, key)
		default:
			return false
		}
		return true
	})
	if len(p.errs) > 0 {
		return f, p.errs
	}
	return f, nil
}

func (p *parser) target(v *yaml.Node, key string) {
	t := p.f.Target
	p.mapping(v, key, func(k string, v *yaml.Node) bool {
		field := key + "." + k
		switch k {
		case "proxmox":
			t.Proxmox, _ = p.str(v, field)
		case "image_vmid":
			t.ImageVMID, _ = p.int(v, field)
		case "storage":
			t.Storage, _ = p.str(v, field)
		case "bridge":
			t.Bridge, _ = p.str(v, field)
		case "vlan":
			if isNull(v) {
				return true
			}
			if n, ok := p.int(v, field); ok {
				t.VLAN = &n
			}
		case "first_ip":
			t.FirstIP, _ = p.str(v, field)
		case "gateway":
			t.Gateway, _ = p.str(v, field)
		case "dns":
			t.DNS = p.strs(v, field)
		case "ssh_keys":
			t.SSHKeys = p.strs(v, field)
		default:
			return false
		}
		return true
	})
}

// mapping loopt door een map. fn zegt of hij de sleutel kent; een
// onbekende of dubbele sleutel is een fout.
func (p *parser) mapping(n *yaml.Node, field string, fn func(key string, v *yaml.Node) bool) {
	if _, known := p.f.lines[field]; field != "" && !known {
		p.f.lines[field] = n.Line
	}
	if isNull(n) {
		return
	}
	if n.Kind != yaml.MappingNode {
		p.fail(field, n.Line, "moet een map met velden zijn")
		return
	}
	seen := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		name := k.Value
		full := name
		if field != "" {
			full = field + "." + name
		}
		if k.Kind != yaml.ScalarNode {
			p.fail(field, k.Line, "een veldnaam moet tekst zijn")
			continue
		}
		if prev, dup := seen[name]; dup {
			p.fail(full, k.Line, "staat twee keer in het bestand (ook op regel %d)", prev)
			continue
		}
		seen[name] = k.Line
		p.f.lines[full] = k.Line
		if !fn(name, v) {
			p.fail(full, k.Line, "onbekend veld")
		}
	}
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func (p *parser) str(n *yaml.Node, field string) (string, bool) {
	if n.Kind != yaml.ScalarNode || isNull(n) {
		p.fail(field, n.Line, "moet tekst zijn")
		return "", false
	}
	return n.Value, true
}

func (p *parser) int(n *yaml.Node, field string) (int, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		p.fail(field, n.Line, "moet een geheel getal zijn")
		return 0, false
	}
	v, err := strconv.Atoi(n.Value)
	if err != nil {
		p.fail(field, n.Line, "moet een geheel getal zijn")
		return 0, false
	}
	return v, true
}

func (p *parser) strs(n *yaml.Node, field string) []string {
	if isNull(n) {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		p.fail(field, n.Line, "moet een lijst zijn, zoals [web, prod]")
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		if s, ok := p.str(c, field); ok {
			out = append(out, s)
		}
	}
	return out
}
