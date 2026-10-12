// Package docs schrijft de documentatie van de clusters als Markdown, uit
// wat ClusterForge al weet: de specificatie, de inventory, Proxmox, de
// afhankelijkheden, de back-ups, de tests en de wat-als. De tekst is
// deterministisch, zonder tijdstempel, zodat een nieuwe versie alleen echte
// wijzigingen toont. De documentatie leest alleen opgeslagen gegevens en
// stuurt nooit een commando naar een agent.
package docs

import (
	"slices"
	"time"

	"github.com/google/uuid"
)

// De scopes van een document.
const (
	ScopeOverview = "overview"
	ScopeCluster  = "cluster"
)

// De soorten documenten.
const (
	KindOverview      = "overzicht"
	KindArchitecture  = "architectuur"
	KindNetwork       = "netwerk"
	KindConfiguration = "configuratie"
	KindRecovery      = "herstel"
	KindRunbooks      = "runbooks"
	KindKubernetes    = "kubernetes"
)

// ClusterKinds zijn de hoofdstukken van elk cluster, in hun volgorde.
var ClusterKinds = []string{KindArchitecture, KindNetwork, KindConfiguration, KindRecovery, KindRunbooks}

// Kinds zijn de hoofdstukken van een cluster van dit type: een
// Kubernetes-cluster heeft na Architectuur het hoofdstuk Kubernetes.
func Kinds(clusterType string) []string {
	if clusterType != "kubernetes" {
		return ClusterKinds
	}
	return []string{KindArchitecture, KindKubernetes, KindNetwork, KindConfiguration, KindRecovery, KindRunbooks}
}

var titles = map[string]string{
	KindOverview: "Overzicht", KindArchitecture: "Architectuur", KindNetwork: "Netwerk", KindConfiguration: "Configuratie",
	KindRecovery: "Herstel", KindRunbooks: "Runbooks", KindKubernetes: "Kubernetes",
}

// Title is de titel van een soort document.
func Title(kind string) string { return titles[kind] }

// Valid zegt of scope en kind samen een document zijn.
func Valid(scope, kind string) bool {
	switch scope {
	case ScopeOverview:
		return kind == KindOverview
	case ScopeCluster:
		return slices.Contains(Kinds("kubernetes"), kind)
	}
	return false
}

// Path is waar een document staat, in de zip en straks in Git: het
// overzicht als README.md, zodat GitHub het bij de map toont, en de
// hoofdstukken per cluster in een eigen map.
func Path(scope, kind, slug string) string {
	if scope == ScopeOverview {
		return "README.md"
	}
	return "clusters/" + slug + "/" + kind + ".md"
}

// Opts bepaalt hoe de tekst geschreven wordt.
type Opts struct {
	// Loc is de tijdzone van datums; nil is die van de server.
	Loc *time.Location
	// Usernames zet de gebruiker bij een revisie (CF_DOCS_USERNAMES).
	Usernames bool
}

func (o Opts) loc() *time.Location {
	if o.Loc != nil {
		return o.Loc
	}
	return time.Local
}

// Rendered is één geschreven document.
type Rendered struct {
	Scope     string
	Kind      string
	ClusterID *uuid.UUID
	Slug      string
	Content   string
}

// Render schrijft alle documenten van de fleet: eerst het overzicht, dan
// per cluster de hoofdstukken.
func Render(f Fleet, o Opts) []Rendered {
	out := []Rendered{{Scope: ScopeOverview, Kind: KindOverview, Content: Overview(f, o)}}
	for i := range f.Clusters {
		out = append(out, RenderCluster(&f.Clusters[i], o)...)
	}
	return out
}

// RenderCluster schrijft de hoofdstukken van één cluster.
func RenderCluster(c *Cluster, o Opts) []Rendered {
	id := c.ID
	kinds := Kinds(c.Type)
	out := make([]Rendered, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, Rendered{Scope: ScopeCluster, Kind: k, ClusterID: &id, Slug: c.Slug, Content: Chapter(c, k, o)})
	}
	return out
}

// Chapter schrijft één hoofdstuk van een cluster.
func Chapter(c *Cluster, kind string, o Opts) string {
	switch kind {
	case KindArchitecture:
		return architecture(c, o)
	case KindNetwork:
		return network(c, o)
	case KindConfiguration:
		return configuration(c, o)
	case KindRecovery:
		return recovery(c, o)
	case KindRunbooks:
		return runbooks(c, o)
	case KindKubernetes:
		if c.Kube != nil {
			return kubernetes(c, o)
		}
	}
	return ""
}

// header is de titel en de navigatie bovenaan een hoofdstuk.
func header(d *doc, c *Cluster, kind string) {
	d.h1(esc(c.Name) + ": " + lower(Title(kind)))
	nav := link("Overzicht", "../../README.md")
	for _, k := range Kinds(c.Type) {
		if k == kind {
			nav += " · **" + Title(k) + "**"
		} else {
			nav += " · " + link(Title(k), k+".md")
		}
	}
	d.block(nav)
}

func lower(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'A' && r[0] <= 'Z' {
		r[0] += 'a' - 'A'
	}
	return string(r)
}
