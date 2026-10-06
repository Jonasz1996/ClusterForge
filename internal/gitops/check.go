package gitops

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Format is de versie van het bestandsformaat.
const Format = 1

// Cluster is wat GitOps van een bestaand cluster weet.
type Cluster struct {
	ID          uuid.UUID
	Slug        string
	Name        string
	Description string
	Environment string
	Type        string
	Tags        []string
	Linked      bool
	Revision    int
	// Applied is de revisie die op alle nodes staat.
	Applied int
	// Spec is nil voor een cluster dat niet uit een template komt.
	Spec *deploy.Spec
}

// Metadata zijn de velden van een cluster die uit Git komen en niet in de
// spec staan.
type Metadata struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Environment string   `json:"environment"`
	Tags        []string `json:"tags"`
}

func (c *Cluster) metadata() Metadata {
	return Metadata{Name: c.Name, Description: c.Description, Environment: c.Environment, Tags: c.Tags}
}

// Checked is een geldig bestand, genormaliseerd.
type Checked struct {
	File     *File
	Template *templates.Template
	// Params zijn de parameters zonder geheimen, genormaliseerd zoals in
	// een spec.
	Params   map[string]any
	Metadata Metadata
}

// Check controleert een bestand tegen de templates en, als er een is, het
// cluster met dezelfde slug. secret zegt dat er een geheim in het bestand
// staat; zo'n bestand wordt nergens bewaard.
func Check(f *File, dir string, c *Cluster, reg *templates.Registry) (ch *Checked, errs []FieldError, secret bool) {
	fail := func(field, format string, a ...any) {
		errs = append(errs, FieldError{Field: field, Line: f.Line(field), Message: fmt.Sprintf(format, a...)})
	}
	if f.Format != Format {
		fail("clusterforge", "zet clusterforge: %d bovenaan; dat is de versie van dit formaat", Format)
	}
	meta := Metadata{
		Name: strings.TrimSpace(f.Cluster.Name), Description: strings.TrimSpace(f.Cluster.Description),
		Environment: f.Cluster.Environment, Tags: []string{},
	}
	switch {
	case meta.Name == "":
		fail("cluster.name", "de naam ontbreekt")
	case len(meta.Name) > 128:
		fail("cluster.name", "hoogstens 128 tekens")
	}
	if len(meta.Description) > 2000 {
		fail("cluster.description", "hoogstens 2000 tekens")
	}
	switch {
	case f.Cluster.Slug == "":
		fail("cluster.slug", "de slug ontbreekt; hij is gelijk aan de mapnaam %s", dir)
	case f.Cluster.Slug != dir:
		fail("cluster.slug", "de slug moet gelijk zijn aan de mapnaam %s", dir)
	}
	switch meta.Environment {
	case "lab", "test", "prod":
	case "":
		fail("cluster.environment", "de omgeving ontbreekt: lab, test of prod")
	default:
		fail("cluster.environment", "kies lab, test of prod")
	}
	if tags, err := inventory.NormalizeTags(f.Cluster.Tags); err != nil {
		fail("cluster.tags", "%s", err.Error())
	} else {
		meta.Tags = tags
	}

	tpl := checkTemplate(f, c, reg, fail)
	var params map[string]any
	if tpl != nil {
		params, secret = checkParams(f, c, tpl, fail)
	} else {
		// Ook zonder template: een geheim in het bestand moet gemeld worden.
		for _, name := range slices.Sorted(maps.Keys(f.Params)) {
			if isSecretName(reg, f.Template.Name, name) {
				secret = true
				fail("params."+name, "zet geen geheimen in Git; ClusterForge maakt %s zelf aan en bewaart het versleuteld", name)
			}
		}
	}
	if c == nil && f.Target == nil {
		fail("target", "een nieuw cluster heeft een target nodig: de Proxmox-koppeling, het golden image en de adressen")
	}
	if len(errs) > 0 {
		sortErrors(errs)
		return nil, errs, secret
	}
	return &Checked{File: f, Template: tpl, Params: params, Metadata: meta}, nil, false
}

func checkTemplate(f *File, c *Cluster, reg *templates.Registry, fail func(string, string, ...any)) *templates.Template {
	name, version := f.Template.Name, f.Template.Version
	if name == "" {
		fail("template.name", "de template ontbreekt")
		return nil
	}
	versions := reg.Versions(name)
	if len(versions) == 0 {
		fail("template.name", "onbekende template %s", name)
		return nil
	}
	if c != nil {
		if c.Spec == nil {
			fail("template.name", "cluster %s komt niet uit een template; alleen zulke clusters kunnen in Git", c.Slug)
			return nil
		}
		if c.Spec.Template.Name != name {
			fail("template.name", "de template ligt vast na de uitrol; dit cluster gebruikt %s", c.Spec.Template.Name)
			return nil
		}
	}
	if version == "" {
		fail("template.version", "de versie ontbreekt; deze server kent %s", strings.Join(versions, ", "))
		return nil
	}
	tpl, ok := reg.Get(name, version)
	if !ok {
		fail("template.version", "onbekende versie %s; deze server kent %s", version, strings.Join(versions, ", "))
		return nil
	}
	return tpl
}

// isSecretName zegt of een parameter een geheim is in een versie van de
// template, of er door zijn naam op lijkt.
func isSecretName(reg *templates.Registry, tpl, name string) bool {
	for _, v := range reg.Versions(tpl) {
		if t, ok := reg.Get(tpl, v); ok {
			if p, ok := t.Param(name); ok && p.Type == "secret" {
				return true
			}
		}
	}
	return looksSecret(name)
}

func looksSecret(name string) bool {
	return name == "auth_pass" || strings.Contains(name, "password") || strings.Contains(name, "secret") || strings.Contains(name, "token")
}

func checkParams(f *File, c *Cluster, tpl *templates.Template, fail func(string, string, ...any)) (map[string]any, bool) {
	secret := false
	out := map[string]any{}
	for _, name := range slices.Sorted(maps.Keys(f.Params)) {
		p, ok := tpl.Param(name)
		switch {
		case ok && p.Type == "secret", !ok && looksSecret(name):
			secret = true
			fail("params."+name, "zet geen geheimen in Git; ClusterForge maakt %s zelf aan en bewaart het versleuteld", name)
		case !ok:
			fail("params."+name, "onbekende parameter voor %s %s", tpl.Name, tpl.Version)
		}
	}
	if secret {
		return nil, true
	}
	vmParams := tpl.VMParams()
	for _, p := range tpl.Params {
		if p.Type == "secret" {
			continue
		}
		field := "params." + p.Name
		raw := f.Params[p.Name]
		if !f.HasParam(p.Name) {
			switch {
			case p.Optional && c == nil:
				out[p.Name] = nil
			case p.Optional:
				fail(field, "%s ontbreekt; alleen bij een nieuw cluster mag hij ontbreken", p.Name)
			default:
				fail(field, "%s ontbreekt; in Git is elke parameter verplicht, ook als hij de standaardwaarde heeft", p.Name)
			}
			continue
		}
		var v any
		switch {
		case strings.TrimSpace(text(raw)) == "" && !p.Optional:
			fail(field, "%s mag niet leeg zijn", p.Label)
			continue
		case strings.TrimSpace(text(raw)) != "":
			n, err := tpl.Normalize(p.Name, raw)
			if err != nil {
				fail(field, "%s: %s", p.Label, err.Error())
				continue
			}
			v = n
		}
		out[p.Name] = v
		if c == nil || c.Spec == nil {
			continue
		}
		cur := text(c.Spec.Params[p.Name])
		switch {
		case p.Immutable && text(v) != cur:
			fail(field, "%s ligt vast na de uitrol (nu %s)", p.Label, orNone(cur))
		case slices.Contains(vmParams, p.Name) && text(v) != cur:
			fail(field, "%s ligt vast na de uitrol (nu %s): een bestaande VM groter of kleiner maken gaat niet via Git", p.Label, orNone(cur))
		}
	}
	if c != nil && c.Spec != nil {
		checkCounts(c, tpl, out, fail)
	}
	return out, false
}

// checkCounts weigert minder nodes dan de spec heeft: omlaag schalen gaat
// met de lifecycle-acties, niet via Git.
func checkCounts(c *Cluster, tpl *templates.Template, params map[string]any, fail func(string, string, ...any)) {
	values, err := tpl.MaskedValues(params)
	if err != nil {
		return
	}
	counts, err := tpl.Counts(templates.Context{Params: values, Cluster: c.Spec.Cluster})
	if err != nil {
		fail("params", "%s", err.Error())
		return
	}
	for i, r := range tpl.Roles {
		have := 0
		for _, n := range c.Spec.Nodes {
			if n.Role == r.Name {
				have++
			}
		}
		if counts[i] < have {
			field := "params"
			if p := tpl.CountParam(r.Name); p != "" {
				field += "." + p
			}
			fail(field, "het cluster heeft nu %d nodes in rol %s; omlaag schalen gaat niet via Git maar met de lifecycle-acties: zet een node uit dienst en haal hem uit het cluster", have, r.Name)
		}
	}
}

func text(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func orNone(s string) string {
	if s == "" {
		return "leeg"
	}
	return s
}

func sortErrors(errs []FieldError) {
	slices.SortStableFunc(errs, func(a, b FieldError) int { return a.Line - b.Line })
}

// Same zegt of het bestand gelijk is aan het cluster: dezelfde metadata,
// templateversie en parameters. Dan is er niets te plannen.
func (ch *Checked) Same(c *Cluster) bool {
	if c.Spec == nil || ch.Metadata.Name != c.Name || ch.Metadata.Description != c.Description ||
		ch.Metadata.Environment != c.Environment || !slices.Equal(ch.Metadata.Tags, c.Tags) {
		return false
	}
	return ch.Template.Version == c.Spec.Template.Version && sameParams(ch.Params, c.Spec.Params)
}

func sameParams(a, b map[string]any) bool {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		if text(a[k]) != text(b[k]) {
			return false
		}
	}
	return true
}
