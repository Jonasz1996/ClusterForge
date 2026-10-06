package templates

import (
	"crypto/rand"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Param is een parameter die de gebruiker bij het uitrollen invult.
type Param struct {
	Name string `yaml:"name"`
	// Type is string, int, bool, ipv4, cidr, size of secret.
	Type     string `yaml:"type"`
	Label    string `yaml:"label"`
	Help     string `yaml:"help"`
	Default  any    `yaml:"default"`
	Optional bool   `yaml:"optional"`
	Min      any    `yaml:"min"`
	Max      any    `yaml:"max"`
	Pattern  string `yaml:"pattern"`
	// Length is de lengte van een gegenereerd secret.
	Length int `yaml:"length"`
	// Immutable zegt dat de waarde na de uitrol vastligt, zoals het VIP:
	// GitOps weigert een andere waarde voor een bestaand cluster.
	Immutable bool `yaml:"immutable"`

	re *regexp.Regexp
}

var (
	paramNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	secretRe    = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	sizeRe      = regexp.MustCompile(`(?i)^(\d+)\s*([KMGT])(i?B)?$`)
)

var paramTypes = map[string]bool{"string": true, "int": true, "bool": true, "ipv4": true, "cidr": true, "size": true, "secret": true}

func (p *Param) check() error {
	if !paramNameRe.MatchString(p.Name) {
		return fmt.Errorf("ongeldige naam %q", p.Name)
	}
	if !paramTypes[p.Type] {
		return fmt.Errorf("onbekend type %q", p.Type)
	}
	if p.Label == "" {
		return fmt.Errorf("label ontbreekt")
	}
	if p.Pattern != "" {
		re, err := regexp.Compile("^(?:" + p.Pattern + ")$")
		if err != nil {
			return fmt.Errorf("pattern: %w", err)
		}
		p.re = re
	}
	if p.Type == "secret" {
		if p.Length == 0 {
			p.Length = 16
		}
		if p.Length < 6 || p.Length > 64 {
			return fmt.Errorf("length moet tussen 6 en 64 liggen")
		}
		if p.Default != nil {
			return fmt.Errorf("een secret heeft geen standaardwaarde")
		}
	}
	for what, v := range map[string]any{"min": p.Min, "max": p.Max} {
		if v == nil {
			continue
		}
		if _, err := p.normalize(v, false); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	if p.Default != nil {
		if _, err := p.normalize(p.Default, true); err != nil {
			return fmt.Errorf("default: %w", err)
		}
	}
	return nil
}

// FieldError is een fout in één parameter.
type FieldError struct {
	Field string
	Msg   string
}

func (e FieldError) Error() string { return e.Msg }

// Validate controleert de ingevulde parameters van een nieuwe uitrol en geeft
// ze genormaliseerd terug: getallen als int, groottes als "2G", netwerken als
// 10.0.0.0/24. Lege secrets worden gegenereerd; Secrets zegt welke dat zijn.
func (t *Template) Validate(input map[string]any) (map[string]any, error) {
	return t.validate(input, true)
}

// Values geeft de waarden om een bestaand cluster mee te renderen: de
// parameters uit de spec met de opgeslagen geheimen erbij. Een ontbrekend
// geheim is hier een fout; een nieuwe waarde zou elke node een ander
// wachtwoord geven.
func (t *Template) Values(params map[string]any, secrets map[string]string) (map[string]any, error) {
	in := make(map[string]any, len(params)+len(secrets))
	for k, v := range params {
		in[k] = v
	}
	for k, v := range secrets {
		in[k] = v
	}
	return t.validate(in, false)
}

// SecretPlaceholder staat in een plan of diff op de plaats van een geheim.
// Het oude en het nieuwe render krijgen dezelfde plaatshouder, dus een diff
// laat nooit een geheim zien en er hoeft niets ontsleuteld te worden.
const SecretPlaceholder = "[geheim]"

// MaskedValues is Values met SecretPlaceholder voor elk geheim, om te tonen
// wat een wijziging zou doen. Wat ermee gerenderd is, gaat nooit naar een
// node.
func (t *Template) MaskedValues(params map[string]any) (map[string]any, error) {
	in := make(map[string]any, len(params))
	for k, v := range params {
		in[k] = v
	}
	return t.validate(in, false, t.Secrets()...)
}

// Param geeft de parameter met deze naam.
func (t *Template) Param(name string) (Param, bool) {
	for _, p := range t.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// Normalize controleert één waarde voor een parameter zoals Validate dat
// doet, met min, max en pattern, en geeft haar genormaliseerd terug.
func (t *Template) Normalize(name string, v any) (any, error) {
	for i := range t.Params {
		if t.Params[i].Name == name {
			return t.Params[i].normalize(v, true)
		}
	}
	return nil, fmt.Errorf("onbekende parameter %s", name)
}

var paramRefRe = regexp.MustCompile(`\.params\.([a-z][a-z0-9_]*)`)

// VMParams zijn de parameters die de vorm van de VM's bepalen. Na de uitrol
// liggen ze vast: een bestaande VM wordt niet groter gemaakt.
func (t *Template) VMParams() []string {
	var out []string
	for _, r := range t.Roles {
		for _, s := range []string{r.VM.CPU, r.VM.Memory, r.VM.Disk} {
			for _, m := range paramRefRe.FindAllStringSubmatch(s, -1) {
				if !slices.Contains(out, m[1]) {
					out = append(out, m[1])
				}
			}
		}
	}
	return out
}

// CountParam is de parameter waaruit het aantal nodes van een rol komt, of
// "" als het aantal vastligt.
func (t *Template) CountParam(role string) string {
	if r := t.role(role); r != nil {
		if m := paramRefRe.FindStringSubmatch(r.Count); m != nil {
			return m[1]
		}
	}
	return ""
}

// validate controleert de invoer. masked zijn geheimen die de plaatshouder
// krijgen in plaats van een waarde.
func (t *Template) validate(input map[string]any, generate bool, masked ...string) (map[string]any, error) {
	out := map[string]any{}
	known := map[string]bool{}
	for _, p := range t.Params {
		known[p.Name] = true
		if p.Type == "secret" && slices.Contains(masked, p.Name) {
			if _, given := input[p.Name]; given {
				return nil, FieldError{p.Name, "geheim " + p.Name + " hoort niet bij de parameters"}
			}
			out[p.Name] = SecretPlaceholder
			continue
		}
		v, ok := input[p.Name]
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			ok = false
		}
		if !ok || v == nil {
			switch {
			case p.Default != nil:
				v = p.Default
			case p.Type == "secret" && generate:
				out[p.Name] = randomSecret(p.Length)
				continue
			case p.Type == "secret":
				return nil, FieldError{p.Name, "geheim " + p.Name + " ontbreekt"}
			case p.Optional:
				out[p.Name] = nil
				continue
			default:
				return nil, FieldError{p.Name, p.Label + " is verplicht"}
			}
		}
		n, err := p.normalize(v, true)
		if err != nil {
			return nil, FieldError{p.Name, p.Label + ": " + err.Error()}
		}
		out[p.Name] = n
	}
	for k := range input {
		if !known[k] {
			return nil, FieldError{k, "onbekende parameter " + k}
		}
	}
	return out, nil
}

// Secrets geeft de namen van de parameters die geheim zijn.
func (t *Template) Secrets() []string {
	var out []string
	for _, p := range t.Params {
		if p.Type == "secret" {
			out = append(out, p.Name)
		}
	}
	return out
}

// normalize zet een waarde om naar het type van de parameter; bounds
// controleert ook min, max en pattern.
func (p *Param) normalize(v any, bounds bool) (any, error) {
	switch p.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("moet tekst zijn")
		}
		s = strings.TrimSpace(s)
		if len(s) > 200 || strings.ContainsAny(s, "\n\r") {
			return nil, fmt.Errorf("hoogstens 200 tekens op één regel")
		}
		if bounds && p.re != nil && !p.re.MatchString(s) {
			return nil, fmt.Errorf("heeft niet de juiste vorm")
		}
		return s, nil
	case "int":
		n, err := toInt(v)
		if err != nil {
			return nil, err
		}
		if bounds {
			if p.Min != nil {
				if lo, _ := toInt(p.Min); n < lo {
					return nil, fmt.Errorf("minstens %d", lo)
				}
			}
			if p.Max != nil {
				if hi, _ := toInt(p.Max); n > hi {
					return nil, fmt.Errorf("hoogstens %d", hi)
				}
			}
		}
		return n, nil
	case "bool":
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			if b, err := strconv.ParseBool(x); err == nil {
				return b, nil
			}
		}
		return nil, fmt.Errorf("moet ja of nee zijn")
	case "ipv4":
		s, _ := v.(string)
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil || !a.Is4() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
			return nil, fmt.Errorf("moet een IPv4-adres zijn, zoals 10.0.20.100")
		}
		return a.String(), nil
	case "cidr":
		s, _ := v.(string)
		pf, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil || !pf.Addr().Is4() {
			return nil, fmt.Errorf("moet een IPv4-netwerk zijn, zoals 10.0.20.0/24")
		}
		return pf.Masked().String(), nil
	case "size":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("moet een grootte zijn, zoals 2G")
		}
		b, err := ParseSize(s)
		if err != nil {
			return nil, err
		}
		if bounds {
			if p.Min != nil {
				if lo, _ := ParseSize(fmt.Sprint(p.Min)); b < lo {
					return nil, fmt.Errorf("minstens %s", FormatSize(lo))
				}
			}
			if p.Max != nil {
				if hi, _ := ParseSize(fmt.Sprint(p.Max)); b > hi {
					return nil, fmt.Errorf("hoogstens %s", FormatSize(hi))
				}
			}
		}
		return FormatSize(b), nil
	case "secret":
		s, ok := v.(string)
		if !ok || len(s) < 6 || len(s) > 64 || !secretRe.MatchString(s) {
			return nil, fmt.Errorf("6 tot 64 tekens: letters, cijfers en . _ ~ -")
		}
		if p.Length > 0 && len(s) > p.Length {
			return nil, fmt.Errorf("hoogstens %d tekens", p.Length)
		}
		return s, nil
	}
	return nil, fmt.Errorf("onbekend type %q", p.Type)
}

func toInt(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case float64:
		if x != math.Trunc(x) || math.Abs(x) > 1e9 {
			return 0, fmt.Errorf("moet een geheel getal zijn")
		}
		return int(x), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, fmt.Errorf("moet een geheel getal zijn")
		}
		return n, nil
	}
	return 0, fmt.Errorf("moet een geheel getal zijn")
}

// ParseSize leest een grootte zoals 512M, 2G of 1T (machten van 1024).
func ParseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("%q is geen grootte; gebruik bijvoorbeeld 512M, 2G of 1T", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 || n > 1<<20 {
		return 0, fmt.Errorf("%q is geen geldige grootte", s)
	}
	shift := map[string]uint{"K": 10, "M": 20, "G": 30, "T": 40}[strings.ToUpper(m[2])]
	return n << shift, nil
}

// FormatSize schrijft een grootte in de grootste hele eenheid.
func FormatSize(b int64) string {
	for _, u := range []struct {
		shift uint
		name  string
	}{{40, "T"}, {30, "G"}, {20, "M"}, {10, "K"}} {
		if b >= 1<<u.shift && b%(1<<u.shift) == 0 {
			return strconv.FormatInt(b>>u.shift, 10) + u.name
		}
	}
	return strconv.FormatInt(b, 10)
}

const secretAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomSecret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = secretAlphabet[int(b[i])%len(secretAlphabet)]
	}
	return string(b)
}

// sample is een geldige voorbeeldwaarde om sjablonen mee te testen.
func (p *Param) sample() any {
	if p.Default != nil {
		v, _ := p.normalize(p.Default, false)
		return v
	}
	switch p.Type {
	case "int":
		if p.Min != nil {
			n, _ := toInt(p.Min)
			return n
		}
		return 1
	case "bool":
		return false
	case "ipv4":
		return "192.0.2.100"
	case "cidr":
		return "192.0.2.0/24"
	case "size":
		if p.Min != nil {
			return fmt.Sprint(p.Min)
		}
		return "1G"
	case "secret":
		return randomSecret(p.Length)
	}
	return "voorbeeld"
}

// NewSecret maakt een waarde voor een geheime parameter, zoals bij een
// uitrol zonder ingevuld geheim.
func (t *Template) NewSecret(name string) (string, error) {
	p, ok := t.Param(name)
	if !ok || p.Type != "secret" {
		return "", fmt.Errorf("%s is geen geheim van %s %s", name, t.Name, t.Version)
	}
	return randomSecret(p.Length), nil
}
