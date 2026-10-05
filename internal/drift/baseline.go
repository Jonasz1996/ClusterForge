package drift

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Baseline is de gewenste staat van een cluster zonder template: per node
// wat er stond toen Jonas de baseline vastlegde. Van een bestand staan
// alleen grootte, rechten, eigenaar en een HMAC van de inhoud erin.
type Baseline struct {
	Kind  string        `json:"kind"`
	Items BaselineItems `json:"items"`
	Nodes []BaseNode    `json:"nodes"`
}

// BaselineItems is wat er per node wordt vastgelegd.
type BaselineItems struct {
	Packages []string `json:"packages"`
	Services []string `json:"services"`
	Files    []string `json:"files"`
}

// BaseNode is de baseline van één node.
type BaseNode struct {
	NodeID     uuid.UUID `json:"node_id"`
	Hostname   string    `json:"hostname"`
	Role       string    `json:"role"`
	CapturedAt time.Time `json:"captured_at"`
	Expect     []Expect  `json:"expect"`
}

// Expect is één verwachting: een pakket, een service, een bestand of een
// map zoals het er stond.
type Expect struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
	Active  *bool  `json:"active,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Group   string `json:"group,omitempty"`
	Size    int64  `json:"size,omitempty"`
	// ContentHMAC is de HMAC van de sha256 van de inhoud; leeg betekent dat
	// de inhoud niet vergeleken wordt.
	ContentHMAC string `json:"content_hmac,omitempty"`
}

// ParseBaseline leest de baseline uit clusters.spec; zonder baseline is het
// nil.
func ParseBaseline(raw []byte) (*Baseline, error) {
	var b Baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("spec is ongeldig: %w", err)
	}
	if b.Kind != "baseline" {
		return nil, nil
	}
	return &b, nil
}

// Node geeft de baseline van een node.
func (b *Baseline) Node(id uuid.UUID) (BaseNode, bool) {
	i := slices.IndexFunc(b.Nodes, func(n BaseNode) bool { return n.NodeID == id })
	if i < 0 {
		return BaseNode{}, false
	}
	return b.Nodes[i], true
}

// Limieten voor wat Jonas per baseline kiest.
const (
	maxItems   = 50
	maxPathLen = 1024
)

var (
	packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)
	serviceName = regexp.MustCompile(`^[A-Za-z0-9@_][A-Za-z0-9@._:-]*$`)
)

// FieldError noemt het veld dat niet klopt.
type FieldError struct {
	Field, Message string
}

func (e *FieldError) Error() string { return e.Message }

// Validate maakt de lijsten schoon: spaties weg, dubbele weg, .service weg.
func (it BaselineItems) Validate() (BaselineItems, error) {
	clean := func(field string, in []string, norm func(string) (string, error)) ([]string, error) {
		out := []string{}
		for _, v := range in {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			n, err := norm(v)
			if err != nil {
				return nil, &FieldError{Field: field, Message: err.Error()}
			}
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
		if len(out) > maxItems {
			return nil, &FieldError{Field: field, Message: fmt.Sprintf("hoogstens %d per baseline", maxItems)}
		}
		return out, nil
	}
	var out BaselineItems
	var err error
	if out.Packages, err = clean("packages", it.Packages, func(v string) (string, error) {
		if !packageName.MatchString(v) {
			return "", fmt.Errorf("%q is geen pakketnaam", v)
		}
		return v, nil
	}); err != nil {
		return out, err
	}
	if out.Services, err = clean("services", it.Services, func(v string) (string, error) {
		v = strings.TrimSuffix(v, ".service")
		if !serviceName.MatchString(v) {
			return "", fmt.Errorf("%q is geen servicenaam", v)
		}
		return v, nil
	}); err != nil {
		return out, err
	}
	if out.Files, err = clean("files", it.Files, func(v string) (string, error) {
		if !strings.HasPrefix(v, "/") || path.Clean(v) != v || len(v) > maxPathLen || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return "", fmt.Errorf("%q is geen volledig pad zoals /etc/nginx/nginx.conf", v)
		}
		if v == "/" {
			return "", errors.New("/ zelf kan niet in een baseline")
		}
		return v, nil
	}); err != nil {
		return out, err
	}
	if len(out.Packages)+len(out.Services)+len(out.Files) == 0 {
		return out, &FieldError{Field: "packages", Message: "kies minstens één pakket, service of bestand"}
	}
	return out, nil
}

// CaptureRequest is wat state.inspect moet bekijken om de items vast te
// leggen: alle pakketten in één stap, dan elke service en elk pad.
func (it BaselineItems) CaptureRequest() []protocol.InspectStep {
	var out []protocol.InspectStep
	if len(it.Packages) > 0 {
		out = append(out, protocol.InspectStep{Packages: it.Packages})
	}
	for _, s := range it.Services {
		out = append(out, protocol.InspectStep{Service: s})
	}
	for _, f := range it.Files {
		out = append(out, protocol.InspectStep{File: f})
	}
	return out
}

// Capture zet de observaties van één node om in verwachtingen. Wat er niet
// is, wordt niet vastgelegd maar staat in notes. Zonder key worden
// bestanden niet vastgelegd, want hun inhoud kan dan niet veilig vergeleken
// worden.
func (it BaselineItems) Capture(key []byte, obs []protocol.Observation) (expect []Expect, notes []string, err error) {
	if len(obs) != len(it.CaptureRequest()) {
		return nil, nil, fmt.Errorf("de agent gaf %d observaties voor %d stappen", len(obs), len(it.CaptureRequest()))
	}
	expect, notes = []Expect{}, []string{}
	i := 0
	if len(it.Packages) > 0 {
		o := obs[i]
		i++
		switch {
		case o.Skipped != "" || o.Error != "":
			notes = append(notes, "pakketten niet vastgelegd: "+o.Skipped+o.Error)
		default:
			for _, p := range o.Packages {
				if !p.Installed {
					notes = append(notes, "pakket "+p.Name+" is niet geïnstalleerd")
					continue
				}
				expect = append(expect, Expect{Kind: "package", Name: p.Name, Version: p.Version})
			}
		}
	}
	for _, name := range it.Services {
		o := obs[i]
		i++
		switch {
		case o.Error != "" || o.Service == nil:
			notes = append(notes, "service "+name+" niet te bekijken: "+o.Error)
		case !o.Service.Loaded:
			notes = append(notes, "service "+name+" bestaat niet")
		default:
			en, ac := o.Service.Enabled, o.Service.Active
			expect = append(expect, Expect{Kind: "service", Name: name, Enabled: &en, Active: &ac})
		}
	}
	for _, p := range it.Files {
		o := obs[i]
		i++
		st := o.Path
		switch {
		case key == nil:
			notes = append(notes, p+" niet vastgelegd: zonder CF_MASTER_KEY legt een baseline geen bestanden vast")
		case o.Error != "" || st == nil:
			notes = append(notes, p+" niet te bekijken: "+o.Error)
		case !st.Exists:
			notes = append(notes, p+" bestaat niet")
		case st.Type == "directory":
			expect = append(expect, Expect{Kind: "directory", Name: p, Mode: st.Mode, Owner: st.Owner, Group: st.Group})
		case st.Type == "file":
			e := Expect{Kind: "file", Name: p, Mode: st.Mode, Owner: st.Owner, Group: st.Group, Size: st.Size}
			if st.SHA256 != "" {
				e.ContentHMAC = secrets.Fingerprint(key, []byte(st.SHA256))
			} else {
				notes = append(notes, p+" is te groot om de inhoud te vergelijken; alleen rechten en eigenaar tellen")
			}
			expect = append(expect, e)
		default:
			notes = append(notes, p+" is geen bestand of map")
		}
	}
	return expect, notes, nil
}

// Steps zet de verwachtingen om in stappen, zodat Compare ze met dezelfde
// betekenis als bij een template vergelijkt.
func (n BaseNode) Steps() ([]templates.Step, []*contentWant) {
	steps := make([]templates.Step, 0, len(n.Expect))
	contents := make([]*contentWant, 0, len(n.Expect))
	for _, e := range n.Expect {
		var s protocol.Step
		var cw *contentWant
		switch e.Kind {
		case "package":
			s.Package = &protocol.PackageStep{Names: []string{e.Name}}
		case "service":
			st := protocol.ServiceStep{Name: e.Name, Enabled: e.Enabled}
			if e.Active != nil {
				st.State = "stopped"
				if *e.Active {
					st.State = "started"
				}
			}
			s.Service = &st
		case "file":
			s.File = &protocol.FileStep{Path: e.Name, Mode: e.Mode, Owner: e.Owner, Group: e.Group}
			if e.ContentHMAC != "" {
				cw = &contentWant{hmac: e.ContentHMAC, size: e.Size, label: "inhoud zoals vastgelegd"}
			}
		case "directory":
			s.Directory = &protocol.DirectoryStep{Path: e.Name, Mode: e.Mode, Owner: e.Owner, Group: e.Group}
		default:
			continue
		}
		steps = append(steps, templates.Step{Step: s})
		contents = append(contents, cw)
	}
	return steps, contents
}

// CompareBaseline vergelijkt een node met zijn baseline.
func CompareBaseline(key []byte, n BaseNode, obs []protocol.Observation) Result {
	steps, contents := n.Steps()
	return compare(key, steps, contents, obs)
}
