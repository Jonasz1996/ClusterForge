package kubefake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// De schrijvende kant van de nep-API: patch (merge en JSON patch, ook op de
// subresource scale), server-side apply (in apply.go), delete met het
// weghalen van wat het object bezit, en eviction met PodDisruptionBudgets.
// dryRun=All verandert niets.

// target is wat een pad aanwijst.
type target struct {
	kind Kind
	ns   string
	name string
	sub  string
}

func (s *Server) target(p string) (target, bool) {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	var gv string
	var rest []string
	switch {
	case segs[0] == "api" && len(segs) >= 3:
		gv, rest = segs[1], segs[2:]
	case segs[0] == "apis" && len(segs) >= 4:
		gv, rest = segs[1]+"/"+segs[2], segs[3:]
	default:
		return target{}, false
	}
	var t target
	if rest[0] == "namespaces" && len(rest) > 2 && rest[2] != "status" && rest[2] != "finalize" {
		t.ns, rest = rest[1], rest[2:]
	}
	if len(rest) < 2 || len(rest) > 3 {
		return target{}, false
	}
	found := false
	for _, k := range Kinds {
		if k.GroupVersion() == gv && k.Resource == rest[0] {
			t.kind, found = k, true
		}
	}
	if !found || t.kind.Namespaced != (t.ns != "") {
		return target{}, false
	}
	t.name = rest[1]
	if len(rest) == 3 {
		t.sub = rest[2]
	}
	return t, true
}

func (t target) key() string { return t.kind.GroupVersion() + "/" + t.kind.Resource }

func notFound(w http.ResponseWriter, t target) {
	status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("%s %q not found", t.kind.Resource, t.name))
}

func (s *Server) patch(w http.ResponseWriter, r *http.Request) {
	t, ok := s.target(r.URL.Path)
	if !ok || (t.sub != "" && t.sub != "scale" && t.sub != "status") {
		status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 3<<20))
	if err != nil {
		status(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	ctype, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if ctype == "application/apply-patch+yaml" {
		if t.sub != "" {
			status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
			return
		}
		s.apply(w, r, t, body)
		return
	}
	var patch any
	if err := json.Unmarshal(body, &patch); err != nil {
		status(w, http.StatusBadRequest, "BadRequest", "error decoding patch: "+err.Error())
		return
	}
	dry := r.URL.Query().Get("dryRun") == "All"
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.objects[t.key()][t.ns+"/"+t.name]
	if cur == nil {
		notFound(w, t)
		return
	}
	obj := clone(cur)
	if t.sub == "scale" {
		// Een patch op scale gaat over spec.replicas van een Scale.
		obj = scaleOf(obj)
	}
	switch ctype {
	case "application/merge-patch+json", "application/strategic-merge-patch+json":
		m, ok := mergePatch(obj, patch).(map[string]any)
		if !ok {
			status(w, http.StatusBadRequest, "BadRequest", "patch is geen object")
			return
		}
		obj = m
	case "application/json-patch+json":
		ops, ok := patch.([]any)
		if !ok {
			status(w, http.StatusBadRequest, "BadRequest", "json patch is geen lijst")
			return
		}
		var out any = obj
		for _, op := range ops {
			if out, err = applyOp(out, op); err != nil {
				status(w, http.StatusUnprocessableEntity, "Invalid", "the server rejected our request due to an error in our request: "+err.Error())
				return
			}
		}
		if obj, ok = out.(map[string]any); !ok {
			status(w, http.StatusUnprocessableEntity, "Invalid", "het object is geen object meer")
			return
		}
	default:
		status(w, http.StatusUnsupportedMediaType, "UnsupportedMediaType", "the body of the request was in an unknown format - accepted media types include: "+
			"application/json-patch+json, application/merge-patch+json, application/apply-patch+yaml")
		return
	}
	if t.sub == "scale" {
		n, ok := intOf(field(obj, "spec.replicas"))
		if !ok || n < 0 {
			status(w, http.StatusUnprocessableEntity, "Invalid", "spec.replicas: Invalid value: must be greater than or equal to 0")
			return
		}
		scaled := clone(cur)
		scaled["spec"].(map[string]any)["replicas"] = float64(n)
		obj = scaled
	}
	if n, ok := obj["spec"].(map[string]any)["replicas"]; ok && n != nil {
		if v, ok := intOf(fmt.Sprint(n), true); !ok || v < 0 {
			status(w, http.StatusUnprocessableEntity, "Invalid", "spec.replicas: Invalid value: must be greater than or equal to 0")
			return
		}
	}
	if !dry {
		s.rv++
		md := obj["metadata"].(map[string]any)
		md["resourceVersion"] = strconv.Itoa(s.rv)
		if !reflect.DeepEqual(cur["spec"], obj["spec"]) {
			g, _ := md["generation"].(float64)
			md["generation"] = g + 1
		}
		s.objects[t.key()][t.ns+"/"+t.name] = obj
	}
	if t.sub == "scale" {
		writeJSON(w, http.StatusOK, scaleOf(obj))
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func intOf(s string, ok bool) (int, bool) {
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// scaleOf maakt de Scale van een app, zoals GET .../scale.
func scaleOf(o map[string]any) map[string]any {
	md, _ := o["metadata"].(map[string]any)
	spec, _ := o["spec"].(map[string]any)
	st, _ := o["status"].(map[string]any)
	return map[string]any{
		"apiVersion": "autoscaling/v1", "kind": "Scale",
		"metadata": map[string]any{"name": md["name"], "namespace": md["namespace"], "resourceVersion": md["resourceVersion"], "uid": md["uid"]},
		"spec":     map[string]any{"replicas": spec["replicas"]},
		"status":   map[string]any{"replicas": st["replicas"]},
	}
}

// mergePatch past een JSON merge patch toe (RFC 7386).
func mergePatch(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = mergePatch(tm[k], v)
	}
	return tm
}

// applyOp past één bewerking van een JSON patch toe (RFC 6902): add,
// replace, remove en test.
func applyOp(doc, op any) (any, error) {
	m, ok := op.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("bewerking is geen object")
	}
	kind, _ := m["op"].(string)
	ptr, _ := m["path"].(string)
	if ptr == "" || ptr[0] != '/' {
		if ptr == "" && (kind == "replace" || kind == "add") {
			return m["value"], nil
		}
		return nil, fmt.Errorf("ongeldig pad %q", ptr)
	}
	parts := strings.Split(ptr[1:], "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	if kind == "test" {
		got, err := get(doc, parts)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(got, m["value"]) {
			return nil, fmt.Errorf("test van %s faalt", ptr)
		}
		return doc, nil
	}
	return set(doc, parts, kind, m["value"])
}

func get(doc any, parts []string) (any, error) {
	cur := doc
	for _, p := range parts {
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[p]
			if !ok {
				return nil, fmt.Errorf("pad %s bestaat niet", p)
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(c) {
				return nil, fmt.Errorf("index %s bestaat niet", p)
			}
			cur = c[i]
		default:
			return nil, fmt.Errorf("pad %s bestaat niet", p)
		}
	}
	return cur, nil
}

func set(doc any, parts []string, kind string, value any) (any, error) {
	if len(parts) == 0 {
		return value, nil
	}
	p := parts[0]
	switch c := doc.(type) {
	case map[string]any:
		if len(parts) == 1 {
			_, exists := c[p]
			switch {
			case kind == "remove" || kind == "replace":
				if !exists {
					return nil, fmt.Errorf("pad %s bestaat niet", p)
				}
				if kind == "remove" {
					delete(c, p)
					return c, nil
				}
			case kind != "add":
				return nil, fmt.Errorf("onbekende bewerking %s", kind)
			}
			c[p] = value
			return c, nil
		}
		next, ok := c[p]
		if !ok {
			return nil, fmt.Errorf("pad %s bestaat niet", p)
		}
		v, err := set(next, parts[1:], kind, value)
		if err != nil {
			return nil, err
		}
		c[p] = v
		return c, nil
	case []any:
		if len(parts) == 1 && kind == "add" && p == "-" {
			return append(c, value), nil
		}
		i, err := strconv.Atoi(p)
		insert := len(parts) == 1 && kind == "add"
		if err != nil || i < 0 || i > len(c) || (i == len(c) && !insert) {
			return nil, fmt.Errorf("index %s bestaat niet", p)
		}
		if len(parts) == 1 {
			switch kind {
			case "add":
				return append(c[:i], append([]any{value}, c[i:]...)...), nil
			case "replace":
				c[i] = value
				return c, nil
			case "remove":
				return append(c[:i], c[i+1:]...), nil
			}
			return nil, fmt.Errorf("onbekende bewerking %s", kind)
		}
		v, err := set(c[i], parts[1:], kind, value)
		if err != nil {
			return nil, err
		}
		c[i] = v
		return c, nil
	}
	return nil, fmt.Errorf("pad %s bestaat niet", p)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	t, ok := s.target(r.URL.Path)
	if !ok || t.sub != "" {
		status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
		return
	}
	q := r.URL.Query()
	dry := q.Get("dryRun") == "All"
	s.mu.Lock()
	defer s.mu.Unlock()
	obj := s.objects[t.key()][t.ns+"/"+t.name]
	if obj == nil {
		notFound(w, t)
		return
	}
	if t.kind.Kind == "Namespace" && (t.name == "default" || t.name == "kube-system" || t.name == "kube-public") {
		status(w, http.StatusForbidden, "Forbidden", fmt.Sprintf("namespaces %q is forbidden: this namespace may not be deleted", t.name))
		return
	}
	if !dry {
		delete(s.objects[t.key()], t.ns+"/"+t.name)
		switch {
		case t.kind.Kind == "Namespace":
			for key, objs := range s.objects {
				for k := range objs {
					if strings.HasPrefix(k, t.name+"/") {
						delete(s.objects[key], k)
					}
				}
			}
		case q.Get("propagationPolicy") != "Orphan":
			s.collect(t.kind.Kind, t.ns, t.name)
		}
	}
	writeJSON(w, http.StatusOK, obj)
}

// collect haalt weg wat een weggehaald object bezat, zoals de garbage
// collector: de ReplicaSets van een Deployment en hun pods. De aanroeper
// heeft het slot.
func (s *Server) collect(kind, ns, name string) {
	for key, objs := range s.objects {
		for k, o := range objs {
			md, _ := o["metadata"].(map[string]any)
			if md["namespace"] != ns && ns != "" {
				continue
			}
			refs, _ := md["ownerReferences"].([]any)
			for _, ref := range refs {
				rm, _ := ref.(map[string]any)
				if rm["kind"] == kind && rm["name"] == name {
					delete(s.objects[key], k)
					child, _ := md["name"].(string)
					cns, _ := md["namespace"].(string)
					ck, _ := o["kind"].(string)
					if ck == "" {
						ck = kindOfKey(key)
					}
					s.collect(ck, cns, child)
					break
				}
			}
		}
	}
}

func kindOfKey(key string) string {
	for _, k := range Kinds {
		if k.GroupVersion()+"/"+k.Resource == key {
			return k.Kind
		}
	}
	return ""
}

// post kent alleen de eviction van een pod en de TokenRequest van een
// ServiceAccount.
func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	t, ok := s.target(r.URL.Path)
	if ok && t.kind.Kind == "ServiceAccount" && t.sub == "token" {
		s.tokenRequest(w, r, t)
		return
	}
	if !ok || t.kind.Kind != "Pod" || t.sub != "eviction" {
		status(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "kubefake kent POST alleen voor eviction en token")
		return
	}
	var ev struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&ev); err != nil || ev.Kind != "Eviction" ||
		ev.APIVersion != "policy/v1" || ev.Metadata.Name != t.name || ev.Metadata.Namespace != t.ns {
		status(w, http.StatusBadRequest, "BadRequest", "geen geldige Eviction voor deze pod")
		return
	}
	dry := r.URL.Query().Get("dryRun") == "All"
	s.mu.Lock()
	defer s.mu.Unlock()
	pod := s.objects["v1/pods"][t.ns+"/"+t.name]
	if pod == nil {
		notFound(w, t)
		return
	}
	md, _ := pod["metadata"].(map[string]any)
	labels, _ := md["labels"].(map[string]any)
	var budgets []map[string]any
	for k, pdb := range s.objects["policy/v1/poddisruptionbudgets"] {
		if !strings.HasPrefix(k, t.ns+"/") {
			continue
		}
		spec, _ := pdb["spec"].(map[string]any)
		sel, _ := spec["selector"].(map[string]any)
		match, _ := sel["matchLabels"].(map[string]any)
		ok := len(match) > 0
		for lk, lv := range match {
			if labels[lk] != lv {
				ok = false
			}
		}
		if ok {
			budgets = append(budgets, pdb)
		}
	}
	for _, pdb := range budgets {
		st, _ := pdb["status"].(map[string]any)
		if n, _ := st["disruptionsAllowed"].(float64); n < 1 {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{}, "status": "Failure",
				"message": "Cannot evict pod as it would violate the pod's disruption budget.", "reason": "TooManyRequests",
				"details": map[string]any{"causes": []any{map[string]any{
					"reason":  "DisruptionBudget",
					"message": fmt.Sprintf("The disruption budget %s needs %v healthy pods and has %v currently", pdbName(pdb), st["desiredHealthy"], st["currentHealthy"]),
				}}},
				"code": http.StatusTooManyRequests,
			})
			return
		}
	}
	if !dry {
		for _, pdb := range budgets {
			st := pdb["status"].(map[string]any)
			st["disruptionsAllowed"] = st["disruptionsAllowed"].(float64) - 1
		}
		delete(s.objects["v1/pods"], t.ns+"/"+t.name)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{}, "status": "Success", "code": http.StatusCreated,
	})
}

func pdbName(pdb map[string]any) string {
	md, _ := pdb["metadata"].(map[string]any)
	n, _ := md["name"].(string)
	return n
}

// PodDisruptionBudget maakt een budget voor de pods met deze labels; allowed
// is hoeveel er nu weg mogen.
func PodDisruptionBudget(namespace, name string, labels map[string]string, minAvailable, allowed int) map[string]any {
	match := map[string]any{}
	for k, v := range labels {
		match[k] = v
	}
	return map[string]any{
		"apiVersion": "policy/v1", "kind": "PodDisruptionBudget",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     map[string]any{"minAvailable": minAvailable, "selector": map[string]any{"matchLabels": match}},
		"status": map[string]any{
			"disruptionsAllowed": allowed, "desiredHealthy": minAvailable, "currentHealthy": minAvailable + allowed, "expectedPods": minAvailable + allowed,
		},
	}
}
