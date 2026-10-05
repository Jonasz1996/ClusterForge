package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// History is de gewenste staat van een cluster met al haar revisies.
type History struct {
	// Template is nil voor een cluster dat niet uit een template komt.
	Template  *HistoryTemplate
	Revision  int
	Params    []HistoryParam
	Notes     []string
	Revisions []Revision
}

type HistoryTemplate struct {
	Name, Version string
	// Available is true als deze versie in de server zit.
	Available bool
	Latest    string
}

type HistoryParam struct {
	Name, Label string
	Value       *string
	Secret      bool
}

// Revision is één versie van de spec.
type Revision struct {
	Revision        int
	Source          string
	CreatedAt       time.Time
	CreatedBy       *uuid.UUID
	CreatedByName   *string
	Template        string
	TemplateVersion string
	Nodes           []string
	// Changes is wat er veranderde tegenover de vorige revisie.
	Changes []Change
}

type Change struct {
	Label, From, To string
}

// History leest de gewenste staat van een cluster, haar revisies en wat er
// niet klopt: een templateversie die de server niet kent, nodes die niet
// bij de spec passen of een node die niet te renderen is.
func (s *Service) History(ctx context.Context, clusterID uuid.UUID) (History, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return History{}, err
	}
	h := History{Revision: int(c.SpecRevision), Params: []HistoryParam{}, Notes: []string{}, Revisions: []Revision{}}
	inv, nodes, err := s.loadNodes(ctx, clusterID)
	if err != nil {
		return History{}, err
	}
	spec, err := ParseSpec(c.Spec, templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)}, inv)
	if errors.Is(err, ErrNoSpec) {
		return h, nil
	}
	if err != nil {
		return History{}, err
	}

	tpl, ok := s.Templates.Get(spec.Template.Name, spec.Template.Version)
	h.Template = &HistoryTemplate{Name: spec.Template.Name, Version: spec.Template.Version, Available: ok}
	if latest, ok := s.Templates.Latest(spec.Template.Name); ok {
		h.Template.Latest = latest.Version
	}
	switch {
	case !ok:
		h.Notes = append(h.Notes, fmt.Sprintf("%s %s zit niet in deze server, dus ClusterForge kan de gewenste staat niet renderen. "+
			"Zet de server terug op een versie die haar kent.", spec.Template.Name, spec.Template.Version))
	case h.Template.Latest != spec.Template.Version:
		h.Notes = append(h.Notes, fmt.Sprintf("Er is een nieuwere versie van %s: %s. Het cluster blijft op %s tot de spec bewust gewijzigd wordt.",
			spec.Template.Name, h.Template.Latest, spec.Template.Version))
	}
	h.Notes = append(h.Notes, spec.Membership(inv)...)
	if ok {
		h.Notes = append(h.Notes, s.renderNotes(ctx, clusterID, tpl, spec, nodes)...)
	}
	h.Params = specParams(tpl, spec)

	revs, err := s.q.ListSpecRevisions(ctx, clusterID)
	if err != nil {
		return History{}, err
	}
	specs := make([]Spec, len(revs))
	for i, r := range revs {
		_ = json.Unmarshal(r.Spec, &specs[i])
		rev := Revision{
			Revision: int(r.Revision), Source: r.Source, CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy, CreatedByName: r.CreatedByName,
			Template: specs[i].Template.Name, TemplateVersion: specs[i].Template.Version, Nodes: []string{}, Changes: []Change{},
		}
		for _, n := range specs[i].Nodes {
			rev.Nodes = append(rev.Nodes, n.Hostname)
		}
		h.Revisions = append(h.Revisions, rev)
	}
	// Nieuwste eerst: vergelijk elke revisie met de volgende in de lijst.
	for i := 0; i+1 < len(specs); i++ {
		h.Revisions[i].Changes = specChanges(tpl, specs[i+1], specs[i])
	}
	return h, nil
}

// renderNotes rendert elke node één keer in het geheugen en zegt wat niet
// lukt. Er gaat niets naar een node.
func (s *Service) renderNotes(ctx context.Context, clusterID uuid.UUID, tpl *templates.Template, spec Spec, nodes map[uuid.UUID]NodeState) []string {
	secrets, err := s.openSecrets(ctx, clusterID, spec.Secrets)
	if err != nil {
		return []string{"De gewenste staat is niet te renderen: " + err.Error()}
	}
	c, err := spec.Context(tpl, secrets, nodes)
	if err != nil {
		return []string{"De gewenste staat is niet te renderen: " + err.Error()}
	}
	var out []string
	for i, n := range spec.Nodes {
		if n.NodeID == uuid.Nil {
			continue
		}
		c.Node = &c.Nodes[i]
		if _, err := tpl.Steps(n.Role, c); err != nil {
			out = append(out, n.Hostname+" is niet te renderen: "+err.Error())
		}
	}
	return out
}

// specParams geeft de parameters in de volgorde en met de labels van de
// template; zonder template op naam.
func specParams(tpl *templates.Template, spec Spec) []HistoryParam {
	out := []HistoryParam{}
	if tpl == nil {
		names := make([]string, 0, len(spec.Params))
		for k := range spec.Params {
			names = append(names, k)
		}
		slices.Sort(names)
		for _, k := range names {
			out = append(out, HistoryParam{Name: k, Label: k, Value: valueText(spec.Params[k])})
		}
		return out
	}
	for _, p := range tpl.Params {
		hp := HistoryParam{Name: p.Name, Label: p.Label, Secret: p.Type == "secret"}
		if !hp.Secret {
			hp.Value = valueText(spec.Params[p.Name])
		}
		out = append(out, hp)
	}
	return out
}

func valueText(v any) *string {
	if v == nil {
		return nil
	}
	s := fmt.Sprint(v)
	return &s
}

// specChanges zegt wat er tussen twee revisies veranderde.
func specChanges(tpl *templates.Template, old, cur Spec) []Change {
	out := []Change{}
	if old.Template != cur.Template {
		out = append(out, Change{"Template", old.Template.Name + " " + old.Template.Version, cur.Template.Name + " " + cur.Template.Version})
	}
	label := func(name string) string {
		if tpl != nil {
			if i := slices.IndexFunc(tpl.Params, func(p templates.Param) bool { return p.Name == name }); i >= 0 {
				return tpl.Params[i].Label
			}
		}
		return name
	}
	keys := map[string]bool{}
	for k := range old.Params {
		keys[k] = true
	}
	for k := range cur.Params {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	slices.Sort(names)
	text := func(v any) string {
		if t := valueText(v); t != nil {
			return *t
		}
		return ""
	}
	for _, k := range names {
		if a, b := text(old.Params[k]), text(cur.Params[k]); a != b {
			out = append(out, Change{label(k), a, b})
		}
	}
	hosts := func(s Spec) string {
		var l []string
		for _, n := range s.Nodes {
			l = append(l, n.Hostname)
		}
		return strings.Join(l, ", ")
	}
	if a, b := hosts(old), hosts(cur); a != b {
		out = append(out, Change{"Nodes", a, b})
	}
	return out
}
