// Package drift vergelijkt wat er op een node staat met de gewenste staat
// van zijn cluster. Een controle verandert nooit iets op de node.
package drift

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Finding is één afwijking. Key noemt de stap en het aspect, zoals
// file:/etc/keepalived/keepalived.conf:content of service:nginx:enabled;
// Step is de stap zelf (file:/etc/keepalived/keepalived.conf), want negeren
// en herstellen gaan over hele stappen.
type Finding struct {
	Key      string `json:"key"`
	Step     string `json:"step"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Aspect   string `json:"aspect"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	// Detail zegt meer over een bestand: hoe groot het is tegenover de
	// template. Nooit de inhoud of een hash.
	Detail string `json:"detail,omitempty"`
	// ModTime is wanneer het bestand op de node het laatst veranderde.
	ModTime *time.Time `json:"mtime,omitempty"`
	// Since is wanneer ClusterForge deze afwijking voor het eerst zag.
	Since time.Time `json:"since"`
	// Fingerprint is de HMAC van de waargenomen waarde. Verandert hij, dan
	// is er op de node opnieuw iets gewijzigd.
	Fingerprint string `json:"fingerprint"`
}

// Unchecked is een stap die een controle niet kan bekijken, zoals een
// commando met unless: die test is vrije shell en draait nooit tijdens een
// controle.
type Unchecked struct {
	Step   string `json:"step"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Result is de uitkomst van Compare.
type Result struct {
	Findings  []Finding   `json:"findings"`
	Unchecked []Unchecked `json:"unchecked"`
}

// Request zet de stappen om in wat state.inspect bekijkt. Een stap die niet
// te bekijken is, zoals een commando met alleen unless, gaat niet mee;
// Align zet hem terug op zijn plaats.
func Request(steps []templates.Step) []protocol.InspectStep {
	out := make([]protocol.InspectStep, 0, len(steps))
	for _, s := range steps {
		if r, ok := inspectStep(s.Step); ok {
			out = append(out, r)
		}
	}
	return out
}

func inspectStep(s protocol.Step) (protocol.InspectStep, bool) {
	switch s.Kind() {
	case "package":
		return protocol.InspectStep{Packages: s.Package.Names}, true
	case "file":
		return protocol.InspectStep{File: s.File.Path}, true
	case "directory":
		return protocol.InspectStep{Directory: s.Directory.Path}, true
	case "service":
		return protocol.InspectStep{Service: s.Service.Name}, true
	case "user":
		return protocol.InspectStep{User: s.User.Name}, true
	case "command":
		if s.Command.Creates != "" {
			return protocol.InspectStep{Creates: s.Command.Creates}, true
		}
	}
	return protocol.InspectStep{}, false
}

// notInspected is de observatie van een stap die Request overslaat.
const notInspected = "niet gecontroleerd: unless is vrije shell en draait nooit tijdens een controle"

// Align legt de observaties van state.inspect naast de stappen: één per
// stap, met een overgeslagen stap op zijn plaats.
func Align(steps []templates.Step, obs []protocol.Observation) ([]protocol.Observation, error) {
	out := make([]protocol.Observation, 0, len(steps))
	i := 0
	for _, s := range steps {
		if _, ok := inspectStep(s.Step); !ok {
			out = append(out, protocol.Observation{Skipped: notInspected})
			continue
		}
		if i >= len(obs) {
			return nil, fmt.Errorf("de agent gaf %d observaties voor %d stappen", len(obs), len(Request(steps)))
		}
		out = append(out, obs[i])
		i++
	}
	if i != len(obs) {
		return nil, fmt.Errorf("de agent gaf %d observaties voor %d stappen", len(obs), i)
	}
	return out, nil
}

// Compare vergelijkt de stappen van een node met wat de agent zag, met de
// betekenis van apply: wat apply zou veranderen, is een afwijking. key is de
// afgeleide sleutel voor de vingerafdrukken.
func Compare(key []byte, steps []templates.Step, obs []protocol.Observation) Result {
	c := comparer{key: key, res: Result{Findings: []Finding{}, Unchecked: []Unchecked{}}}
	for i, s := range steps {
		var o protocol.Observation
		if i < len(obs) {
			o = obs[i]
		}
		c.step(s.Step, o)
	}
	return c.res
}

type comparer struct {
	key []byte
	res Result
}

func (c *comparer) step(s protocol.Step, o protocol.Observation) {
	step, title := stepID(s)
	switch {
	case o.Skipped != "":
		c.res.Unchecked = append(c.res.Unchecked, Unchecked{Step: step, Title: title, Reason: o.Skipped})
		return
	case o.Error != "":
		c.res.Unchecked = append(c.res.Unchecked, Unchecked{Step: step, Title: title, Reason: "niet te bekijken: " + o.Error})
		return
	}
	switch s.Kind() {
	case "package":
		c.packages(s.Package, o)
	case "file":
		f := s.File
		c.path(step, title, "file", o.Path, f.Mode, "0644", f.Owner, f.Group, &f.Content)
	case "directory":
		d := s.Directory
		c.path(step, title, "directory", o.Path, d.Mode, "0755", d.Owner, d.Group, nil)
	case "service":
		c.service(step, title, s.Service, o.Service)
	case "user":
		if o.UserExists != nil && !*o.UserExists {
			c.add(Finding{Step: step, Kind: "user", Title: title, Aspect: "exists", Expected: "bestaat", Actual: "ontbreekt"}, "missing")
		}
	case "command":
		if o.Path != nil && !o.Path.Exists {
			c.add(Finding{
				Step: step, Kind: "command", Title: title, Aspect: "creates",
				Expected: s.Command.Creates + " bestaat", Actual: s.Command.Creates + " ontbreekt, dus apply zou het commando opnieuw uitvoeren",
			}, "missing")
		}
	}
}

// stepID geeft de sleutel en de titel van een stap.
func stepID(s protocol.Step) (string, string) {
	switch s.Kind() {
	case "package":
		return "package:" + strings.Join(s.Package.Names, ","), "Pakketten " + strings.Join(s.Package.Names, ", ")
	case "file":
		return "file:" + s.File.Path, "Bestand " + s.File.Path
	case "directory":
		return "directory:" + s.Directory.Path, "Map " + s.Directory.Path
	case "service":
		return "service:" + s.Service.Name, "Service " + s.Service.Name
	case "user":
		return "user:" + s.User.Name, "Gebruiker " + s.User.Name
	case "command":
		if s.Command.Creates != "" {
			return "command:" + s.Command.Creates, "Commando dat " + s.Command.Creates + " maakt"
		}
		return "command:" + s.Command.Run, "Commando"
	}
	return "stap", "Stap"
}

// add zet een afwijking erbij met haar sleutel en vingerafdruk. observed is
// de waargenomen waarde.
func (c *comparer) add(f Finding, observed string) {
	f.Key = f.Step + ":" + f.Aspect
	f.Fingerprint = secrets.Fingerprint(c.key, []byte(f.Key+"\x00"+observed))
	c.res.Findings = append(c.res.Findings, f)
}

func (c *comparer) packages(p *protocol.PackageStep, o protocol.Observation) {
	absent := p.State == "absent"
	for _, st := range o.Packages {
		step := "package:" + st.Name
		f := Finding{Step: step, Kind: "package", Title: "Pakket " + st.Name, Aspect: "installed"}
		switch {
		case !absent && !st.Installed:
			f.Expected, f.Actual = "geïnstalleerd", "niet geïnstalleerd"
		case absent && st.Installed:
			f.Expected, f.Actual = "niet geïnstalleerd", "geïnstalleerd ("+st.Version+")"
		default:
			// Een versie die achterloopt op een rolgenoot is geen drift:
			// apply installeert alleen wat ontbreekt.
			continue
		}
		c.add(f, strconv.FormatBool(st.Installed)+" "+st.Version)
	}
}

func (c *comparer) service(step, title string, s *protocol.ServiceStep, u *protocol.UnitState) {
	if u == nil {
		c.res.Unchecked = append(c.res.Unchecked, Unchecked{Step: step, Title: title, Reason: "de agent gaf niets terug"})
		return
	}
	f := func(aspect, expected, actual string) Finding {
		return Finding{Step: step, Kind: "service", Title: title, Aspect: aspect, Expected: expected, Actual: actual}
	}
	if !u.Loaded {
		c.add(f("loaded", "aanwezig", "bestaat niet op deze node"), "missing")
		return
	}
	if s.Enabled != nil && *s.Enabled != u.Enabled {
		c.add(f("enabled", enabledText[*s.Enabled], enabledText[u.Enabled]), enabledText[u.Enabled])
	}
	// Na started, restarted of reloaded draait de service; na stopped niet.
	var want *bool
	switch s.State {
	case "started", "restarted", "reloaded":
		want = new(bool)
		*want = true
	case "stopped":
		want = new(bool)
	}
	if want != nil && *want != u.Active {
		c.add(f("active", activeText[*want], activeText[u.Active]), activeText[u.Active])
	}
}

var (
	enabledText = map[bool]string{true: "enabled", false: "disabled"}
	activeText  = map[bool]string{true: "active", false: "inactive"}
)

func (c *comparer) path(step, title, kind string, st *protocol.PathState, mode, defMode, owner, group string, content *string) {
	if st == nil {
		c.res.Unchecked = append(c.res.Unchecked, Unchecked{Step: step, Title: title, Reason: "de agent gaf niets terug"})
		return
	}
	f := func(aspect, expected, actual string) Finding {
		return Finding{Step: step, Kind: kind, Title: title, Aspect: aspect, Expected: expected, Actual: actual}
	}
	if !st.Exists {
		c.add(f("exists", "bestaat", "ontbreekt"), "missing")
		return
	}
	if st.Type != kind {
		c.add(f("type", typeName[kind], typeName[st.Type]), st.Type)
		return
	}
	if content != nil {
		c.content(step, title, f, st, *content)
	}
	if want := permText(mode, defMode); st.Mode != want {
		c.add(f("mode", want, st.Mode), st.Mode)
	}
	if owner != "" && st.Owner != owner {
		c.add(f("owner", owner, st.Owner), st.Owner)
	}
	if group != "" && st.Group != group {
		c.add(f("group", group, st.Group), st.Group)
	}
}

var typeName = map[string]string{"file": "een bestand", "directory": "een map", "other": "iets anders dan een bestand of map"}

func (c *comparer) content(step, title string, f func(aspect, expected, actual string) Finding, st *protocol.PathState, content string) {
	sum := sha256.Sum256([]byte(content))
	want := hex.EncodeToString(sum[:])
	var observed string
	switch {
	case st.SHA256 != "":
		if st.SHA256 == want {
			return
		}
		observed = st.SHA256
	case st.Size == int64(len(content)):
		// Te groot om te hashen en toch even groot: dat kan niet bij een
		// gerenderd bestand, maar zeg het liever dan te gokken.
		c.res.Unchecked = append(c.res.Unchecked, Unchecked{Step: step, Title: title, Reason: "inhoud niet vergeleken: te groot om te hashen"})
		return
	default:
		observed = strconv.FormatInt(st.Size, 10)
	}
	fd := f("content", "inhoud volgens template", "inhoud wijkt af")
	if st.Size == int64(len(content)) {
		fd.Detail = fmt.Sprintf("even groot (%s bytes)", thousands(st.Size))
	} else {
		fd.Detail = fmt.Sprintf("%s bytes in plaats van %s", thousands(st.Size), thousands(int64(len(content))))
	}
	if !st.ModTime.IsZero() {
		t := st.ModTime
		fd.ModTime = &t
	}
	c.add(fd, observed)
}

// permText geeft de rechten zoals apply ze vergelijkt: alleen de
// permissiebits, octaal met vier cijfers.
func permText(mode, def string) string {
	if mode == "" {
		mode = def
	}
	m, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return mode
	}
	return fmt.Sprintf("%04o", m&0o777)
}

// thousands zet een punt tussen de duizendtallen, zoals 1.231.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return s
}

// Keys geeft de sleutels van de afwijkingen.
func Keys(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Key)
	}
	return out
}
