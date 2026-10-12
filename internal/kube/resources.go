package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ResourceType is een soort object die de API kent, zoals deployments in
// apps/v1, ook van een geïnstalleerde app (een CRD).
type ResourceType struct {
	Group      string
	Version    string
	Resource   string
	Kind       string
	Namespaced bool
}

// GroupVersion is apps/v1, of v1 voor de kerngroep.
func (r ResourceType) GroupVersion() string {
	if r.Group == "" {
		return r.Version
	}
	return r.Group + "/" + r.Version
}

// discoveryTTL is hoe lang ClusterForge de soorten van een cluster
// onthoudt; een nieuwe CRD verschijnt dus binnen vijf minuten.
const discoveryTTL = 5 * time.Minute

type discovered struct {
	at    time.Time
	types []ResourceType
}

type apiResourceList struct {
	GroupVersion string `json:"groupVersion"`
	Resources    []struct {
		Name       string   `json:"name"`
		Kind       string   `json:"kind"`
		Namespaced bool     `json:"namespaced"`
		Verbs      []string `json:"verbs"`
	} `json:"resources"`
}

// Resources geeft de soorten objecten die het cluster kent en die te
// lijsten zijn: eerst de kerngroep, dan de groepen op naam.
func (s *Service) Resources(ctx context.Context, clusterID uuid.UUID) ([]ResourceType, error) {
	s.mu.Lock()
	d, ok := s.discovery[clusterID]
	s.mu.Unlock()
	if ok && s.Now().Sub(d.at) < discoveryTTL {
		return d.types, nil
	}
	core, err := GetInto[apiResourceList](ctx, s, clusterID, "/api/v1")
	if err != nil {
		return nil, err
	}
	groups, err := GetInto[struct {
		Groups []struct {
			Name             string `json:"name"`
			PreferredVersion struct {
				Version string `json:"version"`
			} `json:"preferredVersion"`
		} `json:"groups"`
	}](ctx, s, clusterID, "/apis")
	if err != nil {
		return nil, err
	}
	lists := make([]apiResourceList, len(groups.Groups))
	var wg sync.WaitGroup
	for i, g := range groups.Groups {
		if !dnsSubdomain.MatchString(g.Name) || !versionRe.MatchString(g.PreferredVersion.Version) {
			continue
		}
		// Een groep die niet antwoordt, zoals metrics.k8s.io als
		// metrics-server niet draait, valt weg; de rest blijft.
		wg.Go(func() {
			l, err := GetInto[apiResourceList](ctx, s, clusterID, "/apis/"+g.Name+"/"+g.PreferredVersion.Version)
			if err == nil {
				lists[i] = l
			}
		})
	}
	wg.Wait()
	var out []ResourceType
	for _, l := range append([]apiResourceList{core}, lists...) {
		group, version, found := strings.Cut(l.GroupVersion, "/")
		if !found {
			group, version = "", l.GroupVersion
		}
		for _, r := range l.Resources {
			if strings.Contains(r.Name, "/") || !slices.Contains(r.Verbs, "list") || !resourceRe.MatchString(r.Name) {
				continue
			}
			out = append(out, ResourceType{Group: group, Version: version, Resource: r.Name, Kind: r.Kind, Namespaced: r.Namespaced})
		}
	}
	slices.SortStableFunc(out, func(a, b ResourceType) int {
		if (a.Group == "") != (b.Group == "") {
			if a.Group == "" {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Kind, b.Kind))
	})
	s.mu.Lock()
	s.discovery[clusterID] = discovered{at: s.Now(), types: out}
	s.mu.Unlock()
	return out, nil
}

// ResourceFor zoekt de soort bij een apiVersion en kind, zoals apps/v1
// Deployment.
func (s *Service) ResourceFor(ctx context.Context, clusterID uuid.UUID, apiVersion, kind string) (ResourceType, bool) {
	types, err := s.Resources(ctx, clusterID)
	if err != nil {
		return ResourceType{}, false
	}
	for _, t := range types {
		if t.GroupVersion() == apiVersion && t.Kind == kind {
			return t, true
		}
	}
	// Een andere versie van dezelfde groep, zoals autoscaling/v1 tegen v2.
	group, _, _ := strings.Cut(apiVersion, "/")
	if !strings.Contains(apiVersion, "/") {
		group = ""
	}
	for _, t := range types {
		if t.Group == group && t.Kind == kind {
			return t, true
		}
	}
	return ResourceType{}, false
}

var (
	versionRe  = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)
	resourceRe = regexp.MustCompile(`^[a-z][a-z0-9]{0,62}$`)
	// objectName is ruimer dan een DNS-naam: een ClusterRole heet
	// bijvoorbeeld system:controller:job-controller.
	objectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,252}$`)
)

// Ref wijst een object of een soort objecten aan.
type Ref struct {
	Group     string
	Version   string
	Resource  string
	Namespace string
	Name      string
}

// Path is het pad op de API: van het object, of van de lijst als Name leeg
// is.
func (r Ref) Path() (string, error) {
	invalid := func(what, v string) error { return fmt.Errorf("%w: %s %q", ErrInvalid, what, v) }
	var p string
	switch {
	case r.Group == "" && r.Version == "v1":
		p = "/api/v1"
	case r.Group == "":
		return "", invalid("versie", r.Version)
	case !dnsSubdomain.MatchString(r.Group) || len(r.Group) > 253:
		return "", invalid("groep", r.Group)
	case !versionRe.MatchString(r.Version):
		return "", invalid("versie", r.Version)
	default:
		p = "/apis/" + r.Group + "/" + r.Version
	}
	if !resourceRe.MatchString(r.Resource) {
		return "", invalid("soort", r.Resource)
	}
	if r.Namespace != "" {
		if !dnsLabel.MatchString(r.Namespace) {
			return "", invalid("namespace", r.Namespace)
		}
		p += "/namespaces/" + r.Namespace
	}
	p += "/" + r.Resource
	if r.Name != "" {
		if !objectName.MatchString(r.Name) {
			return "", invalid("naam", r.Name)
		}
		p += "/" + r.Name
	}
	return p, nil
}

// ObjectSummary is een object in een lijst van 'Alles'.
type ObjectSummary struct {
	Namespace string
	Name      string
	Created   time.Time
	// Status is de fase of de conditie Ready, als het object die heeft.
	Status string
}

type genericObject struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   Meta   `json:"metadata"`
	Status     struct {
		Phase      string          `json:"phase"`
		Conditions json.RawMessage `json:"conditions"`
	} `json:"status"`
}

// conditions leest de condities lenig: een CRD kan ze anders vullen.
func (o genericObject) conditions() []Condition {
	var raw []struct {
		Type               string `json:"type"`
		Status             string `json:"status"`
		Reason             string `json:"reason"`
		Message            string `json:"message"`
		LastTransitionTime string `json:"lastTransitionTime"`
	}
	if json.Unmarshal(o.Status.Conditions, &raw) != nil {
		return nil
	}
	out := make([]Condition, 0, len(raw))
	for _, c := range raw {
		if c.Type == "" {
			continue
		}
		t, _ := time.Parse(time.RFC3339, c.LastTransitionTime)
		out = append(out, Condition{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, LastTransitionTime: t})
	}
	return out
}

func (o genericObject) statusText() string {
	if o.Status.Phase != "" {
		return o.Status.Phase
	}
	for _, typ := range []string{"Ready", "Available", "Established"} {
		if c, ok := condition(o.conditions(), typ); ok {
			if c.Status == "True" {
				return typ
			}
			return "Niet " + typ
		}
	}
	return ""
}

// Objects geeft de objecten van één soort, in één namespace of in alle.
func (s *Service) Objects(ctx context.Context, clusterID uuid.UUID, ref Ref) ([]ObjectSummary, error) {
	ref.Name = ""
	p, err := ref.Path()
	if err != nil {
		return nil, err
	}
	items, err := List[genericObject](ctx, s, clusterID, p, nil)
	if err != nil {
		return nil, err
	}
	out := make([]ObjectSummary, 0, len(items))
	for _, it := range items {
		out = append(out, ObjectSummary{
			Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Created: it.Metadata.CreationTimestamp, Status: it.statusText(),
		})
	}
	slices.SortFunc(out, func(a, b ObjectSummary) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

// errNoPrevious: de container herstartte nog niet.
var ErrNoPrevious = errors.New("deze container heeft geen vorige: hij is nog niet herstart")
