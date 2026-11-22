package kube

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/internal/textdiff"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Grenzen van één keer toepassen.
const (
	MaxApplyObjects = 50
	MaxApplyBytes   = 512 << 10
)

// Wat er met een object gebeurt bij toepassen.
const (
	ApplyCreate    = "create"
	ApplyUpdate    = "update"
	ApplyUnchanged = "unchanged"
	ApplyFailed    = "failed"
	ApplySkipped   = "skipped"
)

// Manifest is één object uit de YAML.
type Manifest struct {
	// Doc is het nummer van het document, vanaf 1; Line de regel waar het
	// begint.
	Doc    int
	Line   int
	Object map[string]any
}

// YAMLError is een fout in de YAML zelf, met de regel als die bekend is.
type YAMLError struct {
	Line int
	Msg  string
}

func (e *YAMLError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("regel %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

var yamlLine = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// ParseManifests leest één of meer YAML-documenten, gescheiden door ---,
// zoals kubectl apply -f. Een List wordt uitgepakt. Elk object moet
// apiVersion, kind en een naam hebben.
func ParseManifests(text string) ([]Manifest, error) {
	if len(text) > MaxApplyBytes {
		return nil, &YAMLError{Msg: fmt.Sprintf("de YAML is groter dan %d kB", MaxApplyBytes>>10)}
	}
	dec := yaml.NewDecoder(strings.NewReader(text))
	var out []Manifest
	for doc := 1; ; doc++ {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
				line, _ := strconv.Atoi(m[1])
				return nil, &YAMLError{Line: line, Msg: m[2]}
			}
			return nil, &YAMLError{Msg: strings.TrimPrefix(err.Error(), "yaml: ")}
		}
		if len(n.Content) == 0 || (n.Content[0].Kind == yaml.ScalarNode && n.Content[0].Tag == "!!null") {
			continue
		}
		root := n.Content[0]
		var v any
		if err := root.Decode(&v); err != nil {
			return nil, &YAMLError{Line: root.Line, Msg: err.Error()}
		}
		j, err := jsonable(v)
		if err != nil {
			return nil, &YAMLError{Line: root.Line, Msg: err.Error()}
		}
		obj, ok := j.(map[string]any)
		if !ok {
			return nil, &YAMLError{Line: root.Line, Msg: fmt.Sprintf("document %d is geen object", doc)}
		}
		items := []map[string]any{obj}
		if k, _ := obj["kind"].(string); k == "List" || strings.HasSuffix(k, "List") && obj["items"] != nil {
			items = nil
			list, _ := obj["items"].([]any)
			for _, it := range list {
				m, ok := it.(map[string]any)
				if !ok {
					return nil, &YAMLError{Line: root.Line, Msg: "een item van de List is geen object"}
				}
				items = append(items, m)
			}
		}
		for _, o := range items {
			if err := checkManifest(o); err != nil {
				return nil, &YAMLError{Line: root.Line, Msg: err.Error()}
			}
			out = append(out, Manifest{Doc: doc, Line: root.Line, Object: o})
		}
		if len(out) > MaxApplyObjects {
			return nil, &YAMLError{Msg: fmt.Sprintf("meer dan %d objecten; pas ze in delen toe", MaxApplyObjects)}
		}
	}
	if len(out) == 0 {
		return nil, &YAMLError{Msg: "er staat geen object in de YAML"}
	}
	return out, nil
}

func checkManifest(o map[string]any) error {
	av, _ := o["apiVersion"].(string)
	kind, _ := o["kind"].(string)
	switch {
	case av == "" && kind == "":
		return errors.New("apiVersion en kind ontbreken")
	case av == "":
		return fmt.Errorf("%s zonder apiVersion", kind)
	case kind == "":
		return errors.New("kind ontbreekt")
	}
	md, _ := o["metadata"].(map[string]any)
	if name, _ := md["name"].(string); name == "" {
		if gn, _ := md["generateName"].(string); gn != "" {
			return fmt.Errorf("%s heeft generateName; toepassen kan alleen met een vaste naam in metadata.name", kind)
		}
		return fmt.Errorf("%s zonder metadata.name", kind)
	}
	return nil
}

// jsonable maakt van wat YAML gaf iets dat JSON kan worden: mappen met
// tekst als sleutel, en tijden als tekst.
func jsonable(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			j, err := jsonable(val)
			if err != nil {
				return nil, err
			}
			x[k] = j
		}
		return x, nil
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			j, err := jsonable(val)
			if err != nil {
				return nil, err
			}
			m[fmt.Sprint(k)] = j
		}
		return m, nil
	case []any:
		for i, val := range x {
			j, err := jsonable(val)
			if err != nil {
				return nil, err
			}
			x[i] = j
		}
		return x, nil
	case time.Time:
		return x.UTC().Format(time.RFC3339), nil
	}
	return v, nil
}

// ApplyOptions zegt hoe toe te passen.
type ApplyOptions struct {
	// Namespace is die van objecten zonder namespace; leeg is default.
	Namespace string
	// DryRun controleert alleen, zoals kubectl apply --dry-run=server.
	DryRun bool
	// Force neemt velden over die een ander programma beheert.
	Force bool
}

// FieldError is een fout bij een veld van een object.
type FieldError struct {
	Field   string
	Message string
}

// FieldConflict is een veld dat een ander programma beheert.
type FieldConflict struct {
	Field   string
	Manager string
}

// AppliedObject is wat er met één object gebeurt, of gebeurde.
type AppliedObject struct {
	Doc        int
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	// Ref is nil als het cluster de soort niet kent.
	Ref       *Ref
	Action    string
	Diff      string
	Errors    []FieldError
	Conflicts []FieldConflict
	Note      string
}

// ApplyResult is het resultaat van toepassen of controleren.
type ApplyResult struct {
	Objects []AppliedObject
	// OK is true als geen object faalde.
	OK bool
	// Changed telt de objecten die nieuw of anders zijn.
	Changed int
}

// applyRank zet namespaces en soorten eerst, zoals kubectl: wat erin
// moet, moet er eerst zijn.
func applyRank(kind string) int {
	switch kind {
	case "Namespace":
		return 0
	case "CustomResourceDefinition":
		return 1
	}
	return 2
}

var conflictManager = regexp.MustCompile(`conflict with "([^"]*)"`)

// Apply past objecten toe met server-side apply, als clusterforge. Bij
// DryRun verandert er niets, en zegt het resultaat per object of het
// nieuw is, anders of hetzelfde, met het verschil en de fouten per veld.
// Echt toepassen gaat object voor object en stopt bij het eerste dat
// faalt.
func (s *Service) Apply(ctx context.Context, clusterID uuid.UUID, ms []Manifest, o ApplyOptions) (ApplyResult, error) {
	if o.Namespace == "" {
		o.Namespace = "default"
	}
	if !dnsLabel.MatchString(o.Namespace) {
		return ApplyResult{}, fmt.Errorf("%w: namespace %q", ErrInvalid, o.Namespace)
	}
	ms = slices.Clone(ms)
	slices.SortStableFunc(ms, func(a, b Manifest) int {
		return applyRank(a.Object["kind"].(string)) - applyRank(b.Object["kind"].(string))
	})
	// Lees vers: het verschil moet met het cluster van nu zijn.
	s.forget(clusterID)
	res := ApplyResult{OK: true}
	newNS := map[string]bool{}
	stopped := false
	for _, m := range ms {
		ao, err := s.applyOne(ctx, clusterID, m, o, newNS, stopped)
		if err != nil {
			// Geen agent bereikt de API: dan heeft de rest ook geen zin.
			return res, err
		}
		switch ao.Action {
		case ApplyFailed:
			res.OK = false
			stopped = !o.DryRun
		case ApplyCreate, ApplyUpdate:
			res.Changed++
		}
		if ao.Kind == "Namespace" && ao.Action == ApplyCreate {
			newNS[ao.Name] = true
		}
		res.Objects = append(res.Objects, ao)
	}
	return res, nil
}

// applyOne past één object toe. De fout is er alleen als geen agent de
// API bereikt; wat de API van het object vindt, staat in het resultaat.
func (s *Service) applyOne(ctx context.Context, clusterID uuid.UUID, m Manifest, o ApplyOptions, newNS map[string]bool, stopped bool) (AppliedObject, error) {
	obj := m.Object
	md, _ := obj["metadata"].(map[string]any)
	ao := AppliedObject{Doc: m.Doc}
	ao.APIVersion, _ = obj["apiVersion"].(string)
	ao.Kind, _ = obj["kind"].(string)
	ao.Name, _ = md["name"].(string)
	fail := func(field, msg string) (AppliedObject, error) {
		ao.Action = ApplyFailed
		ao.Errors = append(ao.Errors, FieldError{Field: field, Message: msg})
		return ao, nil
	}
	rt, ok := s.ResourceFor(ctx, clusterID, ao.APIVersion, ao.Kind)
	if !ok {
		if ns, _ := md["namespace"].(string); ns != "" {
			ao.Namespace = ns
		}
		return fail("kind", fmt.Sprintf("het cluster kent de soort %s (%s) niet", ao.Kind, ao.APIVersion))
	}
	if rt.GroupVersion() != ao.APIVersion {
		return fail("apiVersion", fmt.Sprintf("het cluster kent %s %s niet, wel %s", ao.APIVersion, ao.Kind, rt.GroupVersion()))
	}
	ref := Ref{Group: rt.Group, Version: rt.Version, Resource: rt.Resource, Name: ao.Name}
	if rt.Namespaced {
		ns, _ := md["namespace"].(string)
		if ns == "" {
			ns = o.Namespace
		}
		md["namespace"] = ns
		ao.Namespace, ref.Namespace = ns, ns
	} else {
		delete(md, "namespace")
	}
	p, err := ref.Path()
	if err != nil {
		field := "metadata.name"
		if strings.Contains(err.Error(), "namespace") {
			field = "metadata.namespace"
		}
		return fail(field, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")+" is geen geldige naam")
	}
	ao.Ref = &ref
	if stopped {
		ao.Action = ApplySkipped
		ao.Note = "niet toegepast: een object ervoor faalde"
		return ao, nil
	}
	if msg := helmValuesError(rt, obj); msg != "" {
		return fail("spec.valuesContent", msg)
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return fail("", err.Error())
	}
	cur, err := s.Get(ctx, clusterID, p, nil)
	switch {
	case errors.Is(err, ErrNotFound):
		cur = nil
	case err != nil:
		var ue *UnavailableError
		if errors.As(err, &ue) {
			return ao, err
		}
		return fail("", err.Error())
	}
	q := map[string]string{}
	if o.DryRun {
		q["dryRun"] = "All"
	}
	if o.Force {
		q["force"] = "true"
	}
	res, err := s.send(ctx, clusterID, protocol.KubeRequest{Op: protocol.KubeApply, Path: p, Body: body, Query: q})
	if err != nil {
		var ue *UnavailableError
		var ae *APIError
		switch {
		case errors.As(err, &ue):
			return ao, err
		case errors.Is(err, ErrNotFound) && rt.Namespaced && strings.Contains(err.Error(), fmt.Sprintf("namespaces %q not found", ao.Namespace)):
			if o.DryRun && newNS[ao.Namespace] {
				ao.Action = ApplyCreate
				ao.Diff, _ = textdiff.Diff("", cleanYAML(hideValues(body)), "/dev/null", objectLabel(ao))
				ao.Note = "Kubernetes controleert dit object pas bij Toepassen, als namespace " + ao.Namespace + " bestaat."
				return ao, nil
			}
			return fail("metadata.namespace", "namespace "+ao.Namespace+" bestaat niet; zet een Namespace in de YAML of kies een andere")
		case errors.As(err, &ae) && ae.Status == 409:
			ao.Action = ApplyFailed
			for _, c := range ae.Causes {
				fc := FieldConflict{Field: strings.TrimPrefix(c.Field, ".")}
				if mm := conflictManager.FindStringSubmatch(c.Message); mm != nil {
					fc.Manager = mm[1]
				}
				ao.Conflicts = append(ao.Conflicts, fc)
			}
			if len(ao.Conflicts) == 0 {
				return fail("", ae.Message)
			}
			return ao, nil
		case errors.As(err, &ae) && len(ae.Causes) > 0:
			ao.Action = ApplyFailed
			for _, c := range ae.Causes {
				ao.Errors = append(ao.Errors, FieldError{Field: c.Field, Message: c.Message})
			}
			return ao, nil
		case errors.As(err, &ae):
			return fail("", ae.Message)
		}
		return fail("", err.Error())
	}
	switch {
	case res.Same:
		ao.Action = ApplyUnchanged
		return ao, nil
	case cur == nil:
		ao.Action = ApplyCreate
	default:
		ao.Action = ApplyUpdate
	}
	from, to := "/dev/null", objectLabel(ao)
	before := ""
	if cur != nil {
		before, from = cleanYAML(cur), objectLabel(ao)+" nu"
		to = objectLabel(ao) + " na toepassen"
	}
	ao.Diff, _ = textdiff.Diff(before, cleanYAML(res.Body), from, to)
	if ao.Action == ApplyUpdate && ao.Diff == "" {
		ao.Note = "Wat er verandert, zit in de waarden, en die toont ClusterForge niet."
	}
	return ao, nil
}

// helmValuesError controleert de values van een HelmChart. De
// helm-controller leest ze pas bij het installeren, en een fout zie je dan
// alleen in de logs van een Job in kube-system.
func helmValuesError(rt ResourceType, obj map[string]any) string {
	if rt.Group != "helm.cattle.io" || rt.Kind != "HelmChart" {
		return ""
	}
	spec, _ := obj["spec"].(map[string]any)
	vc, _ := spec["valuesContent"].(string)
	if strings.TrimSpace(vc) == "" {
		return ""
	}
	var v any
	if err := yaml.Unmarshal([]byte(vc), &v); err != nil {
		if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
			return fmt.Sprintf("regel %s van de values: %s", m[1], m[2])
		}
		return "de values zijn geen geldige YAML: " + strings.TrimPrefix(err.Error(), "yaml: ")
	}
	if _, ok := v.(map[string]any); !ok && v != nil {
		return "de values moeten sleutels met waarden zijn, zoals replicaCount: 2"
	}
	return ""
}

func objectLabel(ao AppliedObject) string {
	if ao.Namespace != "" {
		return ao.Kind + " " + ao.Namespace + "/" + ao.Name
	}
	return ao.Kind + " " + ao.Name
}

// cleanYAML maakt van een object YAML zonder wat bij elke wijziging
// verandert of wat Kubernetes zelf invult: status, uid, versies en
// tijdstippen. Zo toont het verschil alleen wat jij verandert. De velden
// blijven in de volgorde van de API.
func cleanYAML(body []byte) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	n, err := yamlNode(dec)
	if err != nil || n.Kind != yaml.MappingNode {
		return ""
	}
	dropKey(n, "status")
	if md := childNode(n, "metadata"); md != nil {
		for _, k := range []string{"managedFields", "resourceVersion", "generation", "uid", "creationTimestamp"} {
			dropKey(md, k)
		}
		if an := childNode(md, "annotations"); an != nil {
			dropKey(an, lastApplied)
			dropKey(an, revisionAnnotation)
			if len(an.Content) == 0 {
				dropKey(md, "annotations")
			}
		}
	}
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if enc.Encode(n) != nil || enc.Close() != nil {
		return ""
	}
	return b.String()
}

// childNode geeft de waarde bij een sleutel van een YAML-map, of nil.
func childNode(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key && m.Content[i+1].Kind == yaml.MappingNode {
			return m.Content[i+1]
		}
	}
	return nil
}

// dropKey haalt een sleutel uit een YAML-map.
func dropKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
}

// lastApplied is wat kubectl apply zonder --server-side bijhoudt.
const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// hideValues haalt uit een Secret de waarden, zoals de agent dat doet, voor
// een object dat nog niet bij de API was.
func hideValues(body []byte) []byte {
	var v map[string]any
	if json.Unmarshal(body, &v) != nil || v["kind"] != "Secret" {
		return body
	}
	for _, f := range []string{"data", "stringData"} {
		d, ok := v[f].(map[string]any)
		if !ok {
			delete(v, f)
			continue
		}
		for k, val := range d {
			s, _ := val.(string)
			n := len(s)
			if f == "data" {
				if dec, err := base64.StdEncoding.DecodeString(s); err == nil {
					n = len(dec)
				}
			}
			d[k] = protocol.KubeHidden(n)
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return out
}
