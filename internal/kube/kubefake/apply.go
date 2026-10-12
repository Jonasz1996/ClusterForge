package kubefake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Server-side apply, nagespeeld. Het toegepaste object gaat in het bestaande:
// mappen worden samengevoegd, al de rest vervangen. Velden die clusterforge
// eerder zette en nu weglaat, blijven staan, waar de echte API ze weghaalt.
// De controle kent de fouten die een beginner het vaakst maakt: een
// ongeldige naam, een app zonder selector of image, een Service zonder
// poort, en een namespace die niet bestaat. Per veld wordt onthouden welk
// programma het beheert, zodat een conflict met kubectl te testen is.

// Own zegt dat een ander programma velden van een object beheert, zoals
// kubectl na een gewone kubectl apply. Een apply die zo'n veld anders zet,
// geeft dan een conflict, tenzij met force=true. Een veld is een pad zoals
// .spec.replicas.
func (s *Server) Own(apiVersion, kind, ns, name, manager string, fields ...string) {
	k, ok := KindOf(apiVersion, kind)
	if !ok {
		panic(fmt.Sprintf("kubefake: onbekende soort %s %s", apiVersion, kind))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := ownerKey(target{kind: k, ns: ns, name: name})
	if s.owners[key] == nil {
		s.owners[key] = map[string]string{}
	}
	for _, f := range fields {
		s.owners[key][f] = manager
	}
}

func ownerKey(t target) string { return t.key() + "/" + t.ns + "/" + t.name }

// cause is één fout bij een veld, zoals de API ze in details.causes zet.
type cause struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Field   string `json:"field"`
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request, t target, body []byte) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		status(w, http.StatusBadRequest, "BadRequest", "error decoding YAML: het object is geen JSON-object")
		return
	}
	if v, _ := obj["apiVersion"].(string); v != t.kind.GroupVersion() {
		status(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("the API version in the data (%s) does not match the expected API version (%s)", v, t.kind.GroupVersion()))
		return
	}
	if k, _ := obj["kind"].(string); k != t.kind.Kind {
		status(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("the kind in the data (%s) does not match the expected kind (%s)", k, t.kind.Kind))
		return
	}
	md, _ := obj["metadata"].(map[string]any)
	if md == nil {
		md = map[string]any{}
		obj["metadata"] = md
	}
	if n, _ := md["name"].(string); n != t.name {
		status(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("the name of the object (%s) does not match the name on the URL (%s)", n, t.name))
		return
	}
	if t.kind.Namespaced {
		if ns, _ := md["namespace"].(string); ns != "" && ns != t.ns {
			status(w, http.StatusBadRequest, "BadRequest", "the namespace of the provided object does not match the namespace sent on the request")
			return
		}
		md["namespace"] = t.ns
	}
	q := r.URL.Query()
	dry, force := q.Get("dryRun") == "All", q.Get("force") == "true"
	manager := q.Get("fieldManager")
	if manager == "" {
		status(w, http.StatusBadRequest, "BadRequest", "PATCH requests with apply content type require a fieldManager")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if t.kind.Namespaced && s.objects["v1/namespaces"]["/"+t.ns] == nil {
		status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("namespaces %q not found", t.ns))
		return
	}
	if causes := validate(t.kind.Kind, obj); len(causes) > 0 {
		invalid(w, t, causes)
		return
	}
	cur := s.objects[t.key()][t.ns+"/"+t.name]
	key := ownerKey(t)
	owners := s.owners[key]
	if cur == nil {
		owners = nil
	}
	leaves := leafPaths(obj, nil)
	var conflicts []cause
	for _, l := range leaves {
		f := "." + strings.Join(l.path, ".")
		m := owners[f]
		if m == "" || m == manager {
			continue
		}
		if have, err := get(cur, l.path); err == nil && reflect.DeepEqual(have, l.value) {
			continue
		}
		conflicts = append(conflicts, cause{Reason: "FieldManagerConflict", Message: fmt.Sprintf("conflict with %q using %s", m, t.kind.GroupVersion()), Field: f})
	}
	if len(conflicts) > 0 && !force {
		fields := make([]string, 0, len(conflicts))
		for _, c := range conflicts {
			fields = append(fields, c.Message+": "+c.Field)
		}
		word := "conflict"
		if len(conflicts) > 1 {
			word = "conflicts"
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{}, "status": "Failure", "reason": "Conflict", "code": http.StatusConflict,
			"message": fmt.Sprintf("Apply failed with %d %s: %s", len(conflicts), word, strings.Join(fields, ", ")),
			"details": map[string]any{"name": t.name, "group": t.kind.Group, "kind": t.kind.Resource, "causes": conflicts},
		})
		return
	}

	var next map[string]any
	if cur == nil {
		next = clone(obj)
	} else {
		next = mergeApply(clone(cur), clone(obj)).(map[string]any)
		if reflect.DeepEqual(next, cur) {
			writeJSON(w, http.StatusOK, cur)
			return
		}
	}
	nmd := next["metadata"].(map[string]any)
	code := http.StatusOK
	if cur == nil {
		code = http.StatusCreated
		nmd["uid"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", s.rv+1)
		nmd["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
		nmd["generation"] = float64(1)
	} else if !reflect.DeepEqual(cur["spec"], next["spec"]) {
		g, _ := nmd["generation"].(float64)
		nmd["generation"] = g + 1
	}
	if dry {
		if cur == nil {
			nmd["resourceVersion"] = ""
		}
		writeJSON(w, code, next)
		return
	}
	s.rv++
	nmd["resourceVersion"] = strconv.Itoa(s.rv)
	if s.objects[t.key()] == nil {
		s.objects[t.key()] = map[string]map[string]any{}
	}
	s.objects[t.key()][t.ns+"/"+t.name] = next
	if cur == nil || s.owners[key] == nil {
		s.owners[key] = map[string]string{}
	}
	for _, l := range leaves {
		s.owners[key]["."+strings.Join(l.path, ".")] = manager
	}
	writeJSON(w, code, next)
}

// invalid antwoordt 422 zoals de API: een zin met alle fouten, en per
// veld de oorzaak.
func invalid(w http.ResponseWriter, t target, causes []cause) {
	parts := make([]string, 0, len(causes))
	for _, c := range causes {
		parts = append(parts, c.Field+": "+c.Message)
	}
	all := parts[0]
	if len(parts) > 1 {
		all = "[" + strings.Join(parts, ", ") + "]"
	}
	what := t.kind.Kind
	if t.kind.Group != "" {
		what += "." + t.kind.Group
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{}, "status": "Failure", "reason": "Invalid", "code": http.StatusUnprocessableEntity,
		"message": fmt.Sprintf("%s %q is invalid: %s", what, t.name, all),
		"details": map[string]any{"name": t.name, "group": t.kind.Group, "kind": t.kind.Kind, "causes": causes},
	})
}

type leaf struct {
	path  []string
	value any
}

// leafPaths geeft elk veld van een object dat geen map is, met zijn pad;
// een lijst telt als één veld. apiVersion, kind, naam en namespace tellen
// niet: die beheert niemand.
func leafPaths(v any, at []string) []leaf {
	m, ok := v.(map[string]any)
	if !ok {
		return []leaf{{path: slices.Clone(at), value: v}}
	}
	var out []leaf
	for k, val := range m {
		p := append(slices.Clone(at), k)
		switch strings.Join(p, ".") {
		case "apiVersion", "kind", "metadata.name", "metadata.namespace":
			continue
		}
		out = append(out, leafPaths(val, p)...)
	}
	return out
}

// mergeApply zet het toegepaste object in het bestaande.
func mergeApply(cur, applied any) any {
	am, ok := applied.(map[string]any)
	if !ok {
		return applied
	}
	cm, ok := cur.(map[string]any)
	if !ok {
		cm = map[string]any{}
	}
	for k, v := range am {
		cm[k] = mergeApply(cm[k], v)
	}
	return cm
}

var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// validate doet een deel van de controle van de API.
func validate(kind string, o map[string]any) []cause {
	var out []cause
	required := func(f string) {
		out = append(out, cause{Reason: "FieldValueRequired", Message: "Required value", Field: f})
	}
	bad := func(f, msg string) {
		out = append(out, cause{Reason: "FieldValueInvalid", Message: "Invalid value: " + msg, Field: f})
	}
	name, _ := field(o, "metadata.name")
	switch {
	case strings.HasPrefix(kind, "ClusterRole"):
		// Rollen en bindings hebben een padnaam, zoals
		// system:controller:node-controller.
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/%") {
			bad("metadata.name", fmt.Sprintf("%q: may not contain '/' or '%%'", name))
		}
	case len(name) > 253 || !dns1123.MatchString(name):
		bad("metadata.name", fmt.Sprintf("%q: a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character", name))
	}
	spec, _ := o["spec"].(map[string]any)
	port := func(f string, v any) {
		n, ok := v.(float64)
		if !ok || n < 1 || n > 65535 || n != float64(int(n)) {
			bad(f, fmt.Sprintf("%v: must be between 1 and 65535, inclusive", v))
		}
	}
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		sel, _ := spec["selector"].(map[string]any)
		if sel == nil {
			required("spec.selector")
		}
		tpl, _ := spec["template"].(map[string]any)
		tmd, _ := tpl["metadata"].(map[string]any)
		labels, _ := tmd["labels"].(map[string]any)
		if ml, _ := sel["matchLabels"].(map[string]any); sel != nil {
			for k, v := range ml {
				if labels[k] != v {
					bad("spec.template.metadata.labels", fmt.Sprintf("%v: `selector` does not match template `labels`", labels))
					break
				}
			}
		}
		ps, _ := tpl["spec"].(map[string]any)
		cs, _ := ps["containers"].([]any)
		if len(cs) == 0 {
			required("spec.template.spec.containers")
		}
		for i, c := range cs {
			cm, _ := c.(map[string]any)
			at := fmt.Sprintf("spec.template.spec.containers[%d]", i)
			if n, _ := cm["name"].(string); n == "" {
				required(at + ".name")
			}
			if img, _ := cm["image"].(string); img == "" {
				required(at + ".image")
			}
			ports, _ := cm["ports"].([]any)
			for j, p := range ports {
				pm, _ := p.(map[string]any)
				port(fmt.Sprintf("%s.ports[%d].containerPort", at, j), pm["containerPort"])
			}
		}
	case "Service":
		ports, _ := spec["ports"].([]any)
		if typ, _ := spec["type"].(string); len(ports) == 0 && typ != "ExternalName" && spec["clusterIP"] != "None" {
			required("spec.ports")
		}
		for i, p := range ports {
			pm, _ := p.(map[string]any)
			port(fmt.Sprintf("spec.ports[%d].port", i), pm["port"])
		}
	}
	return out
}
