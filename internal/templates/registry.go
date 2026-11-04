package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// De ingebouwde templates staan per versie in builtin/<naam>/<versie>/. Een
// uitgebrachte versie blijft er staan zolang een cluster haar kan gebruiken:
// clusters renderen altijd met de versie waarmee ze zijn uitgerold.
//
//go:embed builtin
var builtinFS embed.FS

// Registry is een verzameling templates, per naam in elke versie.
type Registry struct {
	mu     sync.RWMutex
	byName map[string][]*Template // per naam, oudste versie eerst
}

// Add zet een template die niet ingebouwd is in het register, zoals een
// zelf samengesteld cluster (fase 9). Een versie die er al is, blijft; een
// nieuwe tekst hoort een nieuwe versie te krijgen.
func (r *Registry) Add(t *Template) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.byName[t.Name]
	if slices.ContainsFunc(l, func(o *Template) bool { return o.Version == t.Version }) {
		return
	}
	l = append(l, t)
	slices.SortFunc(l, func(a, b *Template) int { return compareVersions(a.Version, b.Version) })
	r.byName[t.Name] = l
}

// Has zegt of er een template met deze naam is, in welke versie ook.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byName[name]) > 0
}

// Load leest elke map <naam>/<versie>/ in fsys als template. De naam en
// versie in template.yaml moeten bij de map passen.
func Load(fsys fs.FS) (*Registry, error) {
	r := &Registry{byName: map[string][]*Template{}}
	names, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if !n.IsDir() {
			continue
		}
		versions, err := fs.ReadDir(fsys, n.Name())
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			if !v.IsDir() {
				return nil, fmt.Errorf("%s/%s: een template staat in %s/<versie>/", n.Name(), v.Name(), n.Name())
			}
			dir := path.Join(n.Name(), v.Name())
			sub, _ := fs.Sub(fsys, dir)
			t, err := Parse(sub)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", dir, err)
			}
			if t.Name != n.Name() || t.Version != v.Name() {
				return nil, fmt.Errorf("%s: template.yaml zegt %s %s", dir, t.Name, t.Version)
			}
			r.byName[t.Name] = append(r.byName[t.Name], t)
		}
		slices.SortFunc(r.byName[n.Name()], func(a, b *Template) int { return compareVersions(a.Version, b.Version) })
	}
	return r, nil
}

// Get zoekt een template in precies deze versie.
func (r *Registry) Get(name, version string) (*Template, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.byName[name] {
		if t.Version == version {
			return t, true
		}
	}
	return nil, false
}

// Latest geeft de nieuwste versie van een template; een nieuwe uitrol
// gebruikt die.
func (r *Registry) Latest(name string) (*Template, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l := r.byName[name]
	if len(l) == 0 {
		return nil, false
	}
	return l[len(l)-1], true
}

// Versions geeft de versies van een template, oudste eerst.
func (r *Registry) Versions(name string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for _, t := range r.byName[name] {
		out = append(out, t.Version)
	}
	return out
}

// All geeft van elke template de nieuwste versie, op naam.
func (r *Registry) All() []*Template {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	slices.Sort(names)
	out := make([]*Template, 0, len(names))
	for _, n := range names {
		l := r.byName[n]
		out = append(out, l[len(l)-1])
	}
	return out
}

// UsesRecipeSteps zegt of de template stappen heeft die pas een agent met
// protocol.RecipeStepsSince kent: repo_deb, repo of ini_set.
func (t *Template) UsesRecipeSteps() bool {
	for _, r := range t.Roles {
		for _, raw := range r.Steps {
			m, _ := raw.(map[string]any)
			for _, k := range []string{"repo_deb", "repo", "ini_set"} {
				if _, ok := m[k]; ok {
					return true
				}
			}
		}
	}
	return false
}

// compareVersions vergelijkt versies van de vorm 1.2.3.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(pa), len(pb)) {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x - y
		}
	}
	return len(pa) - len(pb)
}

var builtin *Registry

func init() {
	sub, _ := fs.Sub(builtinFS, "builtin")
	r, err := Load(sub)
	if err != nil {
		panic("ingebouwde templates: " + err.Error())
	}
	builtin = r
}

// BuiltinRegistry geeft alle ingebouwde templates in alle versies.
func BuiltinRegistry() *Registry { return builtin }

// Builtin geeft van elke ingebouwde template de nieuwste versie.
func Builtin() []*Template { return builtin.All() }

// Get zoekt een ingebouwde template in precies deze versie.
func Get(name, version string) (*Template, bool) { return builtin.Get(name, version) }

// Latest geeft de nieuwste versie van een ingebouwde template.
func Latest(name string) (*Template, bool) { return builtin.Latest(name) }
