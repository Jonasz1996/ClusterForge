// Package kubefake is een nagespeelde Kubernetes-API voor tests en de
// ontwikkelomgeving, zoals pvefake voor Proxmox. Hij luistert met TLS en
// eist een clientcertificaat, net als k3s, en schrijft een kubeconfig in het
// formaat van /etc/rancher/k3s/k3s.yaml. Hij kent discovery, /version,
// /readyz, en get en list van de gewone soorten objecten met label- en
// fieldselectors en pagina's, patch, delete en eviction met
// PodDisruptionBudgets. Elk verzoek wordt onthouden, zodat een test kan
// nagaan wat er nooit aankwam.
package kubefake

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind is een soort object die de nep-API kent.
type Kind struct {
	Group      string
	Version    string
	Kind       string
	Resource   string
	Namespaced bool
	// Subresources zoals status, log of exec, voor discovery.
	Subresources []string
}

// GroupVersion is apps/v1, of v1 voor de kerngroep.
func (k Kind) GroupVersion() string {
	if k.Group == "" {
		return k.Version
	}
	return k.Group + "/" + k.Version
}

// builtin is true voor de ingebouwde soorten: hun items in een lijst hebben
// geen kind en apiVersion, die van een CRD wel.
func (k Kind) builtin() bool {
	return k.Group == "" || !strings.Contains(k.Group, ".") || strings.HasSuffix(k.Group, ".k8s.io")
}

// Kinds zijn de soorten die de nep-API kent; een k3s-cluster heeft ze ook.
var Kinds = []Kind{
	{Version: "v1", Kind: "Namespace", Resource: "namespaces", Subresources: []string{"status", "finalize"}},
	{Version: "v1", Kind: "Node", Resource: "nodes", Subresources: []string{"status", "proxy"}},
	{Version: "v1", Kind: "Pod", Resource: "pods", Namespaced: true, Subresources: []string{"status", "log", "exec", "attach", "portforward", "proxy", "eviction", "ephemeralcontainers"}},
	{Version: "v1", Kind: "Service", Resource: "services", Namespaced: true, Subresources: []string{"status", "proxy"}},
	{Version: "v1", Kind: "Endpoints", Resource: "endpoints", Namespaced: true},
	{Version: "v1", Kind: "ConfigMap", Resource: "configmaps", Namespaced: true},
	{Version: "v1", Kind: "Secret", Resource: "secrets", Namespaced: true},
	{Version: "v1", Kind: "Event", Resource: "events", Namespaced: true},
	{Version: "v1", Kind: "ServiceAccount", Resource: "serviceaccounts", Namespaced: true, Subresources: []string{"token"}},
	{Version: "v1", Kind: "PersistentVolumeClaim", Resource: "persistentvolumeclaims", Namespaced: true, Subresources: []string{"status"}},
	{Version: "v1", Kind: "PersistentVolume", Resource: "persistentvolumes", Subresources: []string{"status"}},
	{Group: "apps", Version: "v1", Kind: "Deployment", Resource: "deployments", Namespaced: true, Subresources: []string{"status", "scale"}},
	{Group: "apps", Version: "v1", Kind: "ReplicaSet", Resource: "replicasets", Namespaced: true, Subresources: []string{"status", "scale"}},
	{Group: "apps", Version: "v1", Kind: "StatefulSet", Resource: "statefulsets", Namespaced: true, Subresources: []string{"status", "scale"}},
	{Group: "apps", Version: "v1", Kind: "DaemonSet", Resource: "daemonsets", Namespaced: true, Subresources: []string{"status"}},
	{Group: "batch", Version: "v1", Kind: "Job", Resource: "jobs", Namespaced: true, Subresources: []string{"status"}},
	{Group: "batch", Version: "v1", Kind: "CronJob", Resource: "cronjobs", Namespaced: true, Subresources: []string{"status"}},
	{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress", Resource: "ingresses", Namespaced: true, Subresources: []string{"status"}},
	{Group: "networking.k8s.io", Version: "v1", Kind: "IngressClass", Resource: "ingressclasses"},
	{Group: "storage.k8s.io", Version: "v1", Kind: "StorageClass", Resource: "storageclasses"},
	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget", Resource: "poddisruptionbudgets", Namespaced: true, Subresources: []string{"status"}},
	{Group: "helm.cattle.io", Version: "v1", Kind: "HelmChart", Resource: "helmcharts", Namespaced: true},
	{Group: "k3s.cattle.io", Version: "v1", Kind: "ETCDSnapshotFile", Resource: "etcdsnapshotfiles"},
	{Group: "traefik.io", Version: "v1alpha1", Kind: "IngressRoute", Resource: "ingressroutes", Namespaced: true},
	{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "NodeMetrics", Resource: "nodes"},
	{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetrics", Resource: "pods", Namespaced: true},
}

// KindOf zoekt een soort op apiVersion en kind.
func KindOf(apiVersion, kind string) (Kind, bool) {
	for _, k := range Kinds {
		if k.GroupVersion() == apiVersion && k.Kind == kind {
			return k, true
		}
	}
	return Kind{}, false
}

// Server is de nep-API.
type Server struct {
	mu       sync.Mutex
	objects  map[string]map[string]map[string]any // groupversion/resource → ns/name → object
	requests []string
	// logs per ns/pod/container/vorige.
	logs    map[string][]logLine
	version string
	ready   bool
	kubelet bool
	rv      int

	ca, serverCert, serverKey, clientCert, clientKey []byte
	srv, kubeletSrv                                  *httptest.Server
}

// New maakt een nep-API met eigen certificaten. Start zet hem aan.
func New() *Server {
	s := &Server{
		objects: map[string]map[string]map[string]any{}, logs: map[string][]logLine{}, version: "v1.35.9+k3s1", ready: true, kubelet: true,
	}
	s.makeCerts()
	return s
}

// Start luistert op 127.0.0.1 met TLS en clientcertificaten, en start een
// kubelet-healthz zonder TLS.
func (s *Server) Start() {
	s.srv = httptest.NewUnstartedServer(s.Handler())
	cert, err := tls.X509KeyPair(s.serverCert, s.serverKey)
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(s.ca)
	// Een test met een verkeerde CA hoort geen ruis in de uitvoer te geven.
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	s.srv.StartTLS()
	s.kubeletSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		ok := s.kubelet
		s.mu.Unlock()
		if !ok {
			http.Error(w, "kubelet niet gezond", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
}

// Close stopt de servers.
func (s *Server) Close() {
	if s.srv != nil {
		s.srv.Close()
	}
	if s.kubeletSrv != nil {
		s.kubeletSrv.Close()
	}
}

// URL is het adres van de API.
func (s *Server) URL() string { return s.srv.URL }

// KubeletURL is de healthz van de nagespeelde kubelet.
func (s *Server) KubeletURL() string { return s.kubeletSrv.URL + "/healthz" }

// SetVersion zet de gitVersion van /version.
func (s *Server) SetVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = v
}

// SetReady zet wat /readyz antwoordt.
func (s *Server) SetReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = ready
}

// SetKubelet zet wat de healthz van de kubelet antwoordt.
func (s *Server) SetKubelet(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kubelet = ok
}

// Requests geeft elk verzoek dat binnenkwam, als "GET /pad?query".
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Kubeconfig is de kubeconfig zoals k3s die schrijft.
func (s *Server) Kubeconfig() []byte {
	enc := base64.StdEncoding.EncodeToString
	return []byte(`apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: ` + enc(s.ca) + `
    server: ` + s.srv.URL + `
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
kind: Config
preferences: {}
users:
- name: default
  user:
    client-certificate-data: ` + enc(s.clientCert) + `
    client-key-data: ` + enc(s.clientKey) + `
`)
}

// WriteKubeconfig schrijft de kubeconfig naar root/etc/rancher/k3s/k3s.yaml.
func (s *Server) WriteKubeconfig(root string) error {
	p := filepath.Join(root, "etc/rancher/k3s/k3s.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, s.Kubeconfig(), 0o600)
}

// Add zet objecten in de API, of vervangt ze. Een object is een map of
// iets dat als JSON een Kubernetes-object is. Ontbreekt de namespace van
// een object in een namespace, dan is het default.
func (s *Server) Add(objs ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range objs {
		m := toMap(o)
		apiVersion, _ := m["apiVersion"].(string)
		kind, _ := m["kind"].(string)
		k, ok := KindOf(apiVersion, kind)
		if !ok {
			panic(fmt.Sprintf("kubefake: onbekende soort %s %s", apiVersion, kind))
		}
		md, _ := m["metadata"].(map[string]any)
		if md == nil {
			md = map[string]any{}
			m["metadata"] = md
		}
		if k.Namespaced {
			if ns, _ := md["namespace"].(string); ns == "" {
				md["namespace"] = "default"
			}
		} else {
			delete(md, "namespace")
		}
		name, _ := md["name"].(string)
		if name == "" {
			panic("kubefake: object zonder naam")
		}
		s.rv++
		md["resourceVersion"] = strconv.Itoa(s.rv)
		if _, ok := md["uid"]; !ok {
			md["uid"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", s.rv)
		}
		if _, ok := md["creationTimestamp"]; !ok {
			md["creationTimestamp"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		}
		key := k.GroupVersion() + "/" + k.Resource
		if s.objects[key] == nil {
			s.objects[key] = map[string]map[string]any{}
		}
		ns, _ := md["namespace"].(string)
		s.objects[key][ns+"/"+name] = m
	}
}

// Delete haalt een object weg; ns is leeg bij een object zonder namespace.
func (s *Server) Delete(apiVersion, kind, ns, name string) {
	k, ok := KindOf(apiVersion, kind)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects[k.GroupVersion()+"/"+k.Resource], ns+"/"+name)
}

// Get geeft een kopie van een object, of nil.
func (s *Server) Get(apiVersion, kind, ns, name string) map[string]any {
	k, ok := KindOf(apiVersion, kind)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.objects[k.GroupVersion()+"/"+k.Resource][ns+"/"+name]
	if o == nil {
		return nil
	}
	return clone(o)
}

func toMap(o any) map[string]any {
	var b []byte
	switch v := o.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			panic(err)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		panic(fmt.Sprintf("kubefake: geen object: %v", err))
	}
	return m
}

func clone(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// Handler is de HTTP-kant van de nep-API.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			req += "?" + r.URL.RawQuery
		}
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			s.get(w, r)
		case http.MethodPatch:
			s.patch(w, r)
		case http.MethodDelete:
			s.remove(w, r)
		case http.MethodPost:
			s.post(w, r)
		default:
			status(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "methode "+r.Method+" kent kubefake niet")
		}
	})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch p {
	case "/version":
		s.mu.Lock()
		v := s.version
		s.mu.Unlock()
		minor := ""
		if parts := strings.Split(strings.TrimPrefix(v, "v"), "."); len(parts) > 1 {
			minor = parts[1]
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"major": "1", "minor": minor, "gitVersion": v, "gitCommit": "0123456789abcdef", "gitTreeState": "clean",
			"buildDate": "2026-09-30T00:00:00Z", "goVersion": "go1.25.1", "compiler": "gc", "platform": "linux/amd64",
		})
		return
	case "/readyz", "/livez":
		s.mu.Lock()
		ok := s.ready
		s.mu.Unlock()
		if !ok {
			http.Error(w, "[-]etcd failed: reason withheld", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
		return
	case "/api":
		writeJSON(w, http.StatusOK, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
		return
	case "/apis":
		writeJSON(w, http.StatusOK, s.groupList())
		return
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	var gv string
	var rest []string
	switch {
	case segs[0] == "api" && len(segs) >= 2:
		gv, rest = segs[1], segs[2:]
	case segs[0] == "apis" && len(segs) == 2:
		for _, g := range s.groupList()["groups"].([]map[string]any) {
			if g["name"] == segs[1] {
				writeJSON(w, http.StatusOK, g)
				return
			}
		}
		status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
		return
	case segs[0] == "apis" && len(segs) >= 3:
		gv, rest = segs[1]+"/"+segs[2], segs[3:]
	default:
		status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
		return
	}
	if len(rest) == 0 {
		s.resourceList(w, gv)
		return
	}
	ns := ""
	if rest[0] == "namespaces" && len(rest) > 2 && rest[2] != "status" && rest[2] != "finalize" {
		ns, rest = rest[1], rest[2:]
	}
	var k Kind
	found := false
	for _, kk := range Kinds {
		if kk.GroupVersion() == gv && kk.Resource == rest[0] {
			k, found = kk, true
		}
	}
	if !found || len(rest) > 3 {
		status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
		return
	}
	switch len(rest) {
	case 1:
		s.list(w, r, k, ns)
	case 2:
		s.object(w, k, ns, rest[1])
	default:
		if rest[2] == "status" {
			s.object(w, k, ns, rest[1])
			return
		}
		if rest[2] == "log" && k.Resource == "pods" {
			s.podLog(w, r, ns, rest[1])
			return
		}
		// exec, attach, proxy en dergelijke: kubefake antwoordt, zodat een
		// test ziet dat zo'n verzoek hier nooit had mogen komen.
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("kubefake: " + rest[2]))
	}
}

func (s *Server) object(w http.ResponseWriter, k Kind, ns, name string) {
	s.mu.Lock()
	o := s.objects[k.GroupVersion()+"/"+k.Resource][ns+"/"+name]
	if o != nil {
		o = clone(o)
	}
	s.mu.Unlock()
	if o == nil {
		status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("%s %q not found", k.Resource, name))
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, k Kind, ns string) {
	q := r.URL.Query()
	if q.Get("watch") != "" {
		status(w, http.StatusBadRequest, "BadRequest", "kubefake kent watch niet")
		return
	}
	labels, err := parseSelector(q.Get("labelSelector"))
	if err != nil {
		status(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	fields, err := parseSelector(q.Get("fieldSelector"))
	if err != nil {
		status(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	s.mu.Lock()
	var keys []string
	for key := range s.objects[k.GroupVersion()+"/"+k.Resource] {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var items []any
	for _, key := range keys {
		o := s.objects[k.GroupVersion()+"/"+k.Resource][key]
		md, _ := o["metadata"].(map[string]any)
		if ns != "" && md["namespace"] != ns {
			continue
		}
		lm, _ := md["labels"].(map[string]any)
		if !labels.match(func(f string) (string, bool) { v, ok := lm[f].(string); return v, ok }) ||
			!fields.match(func(f string) (string, bool) { return field(o, f) }) {
			continue
		}
		item := clone(o)
		if k.builtin() {
			delete(item, "kind")
			delete(item, "apiVersion")
		}
		items = append(items, item)
	}
	rv := strconv.Itoa(s.rv)
	s.mu.Unlock()

	start, _ := strconv.Atoi(q.Get("continue"))
	start = min(max(start, 0), len(items))
	end := len(items)
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && start+n < end {
		end = start + n
	}
	meta := map[string]any{"resourceVersion": rv}
	if end < len(items) {
		meta["continue"] = strconv.Itoa(end)
		meta["remainingItemCount"] = len(items) - end
	}
	page := items[start:end]
	if page == nil {
		page = []any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": k.Kind + "List", "apiVersion": k.GroupVersion(), "metadata": meta, "items": page})
}

type logLine struct {
	at   time.Time
	text string
}

// SetLog zet de log van een container, of van de vorige container als
// previous true is. Elke regel is een seconde na de vorige, de laatste nu.
func (s *Server) SetLog(namespace, pod, container string, previous bool, lines ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := time.Now().UTC()
	if previous {
		end = end.Add(-time.Minute)
	}
	out := make([]logLine, len(lines))
	for i, l := range lines {
		out[i] = logLine{at: end.Add(time.Duration(i-len(lines)+1) * time.Second), text: l}
	}
	s.logs[fmt.Sprintf("%s/%s/%s/%t", namespace, pod, container, previous)] = out
}

// podLog antwoordt zoals de API op pods/<naam>/log: platte tekst, met
// container, previous, tailLines, timestamps en limitBytes.
func (s *Server) podLog(w http.ResponseWriter, r *http.Request, ns, name string) {
	q := r.URL.Query()
	s.mu.Lock()
	pod := s.objects["v1/pods"][ns+"/"+name]
	var containers []string
	if pod != nil {
		spec, _ := pod["spec"].(map[string]any)
		cs, _ := spec["containers"].([]any)
		for _, c := range cs {
			if m, ok := c.(map[string]any); ok {
				n, _ := m["name"].(string)
				containers = append(containers, n)
			}
		}
	}
	s.mu.Unlock()
	if pod == nil {
		status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("pods %q not found", name))
		return
	}
	c := q.Get("container")
	switch {
	case c == "" && len(containers) == 1:
		c = containers[0]
	case c == "":
		status(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("a container name must be specified for pod %s, choose one of: %v", name, containers))
		return
	case !slices.Contains(containers, c):
		status(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("container %s is not valid for pod %s", c, name))
		return
	}
	previous := q.Get("previous") == "true"
	s.mu.Lock()
	lines, ok := s.logs[fmt.Sprintf("%s/%s/%s/%t", ns, name, c, previous)]
	s.mu.Unlock()
	if previous && !ok {
		status(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("previous terminated container %q in pod %q not found", c, name))
		return
	}
	if n, err := strconv.Atoi(q.Get("tailLines")); err == nil && n >= 0 && n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	var b strings.Builder
	for _, l := range lines {
		if q.Get("timestamps") == "true" {
			b.WriteString(l.at.Format(time.RFC3339Nano) + " ")
		}
		b.WriteString(l.text + "\n")
	}
	out := b.String()
	if n, err := strconv.Atoi(q.Get("limitBytes")); err == nil && n > 0 && n < len(out) {
		out = out[:n]
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(out))
}

// field leest een veld als metadata.name of spec.nodeName.
func field(o map[string]any, f string) (string, bool) {
	var cur any = o
	for part := range strings.SplitSeq(f, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur = m[part]
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case nil:
		return "", false
	}
	return fmt.Sprint(cur), true
}

type requirement struct {
	key, op, value string
}

type selector []requirement

// parseSelector kent a=b, a==b, a!=b, a en !a, gescheiden door komma's.
func parseSelector(s string) (selector, error) {
	var out selector
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.Contains(part, "!="):
			k, v, _ := strings.Cut(part, "!=")
			out = append(out, requirement{strings.TrimSpace(k), "!=", strings.TrimSpace(v)})
		case strings.Contains(part, "=="):
			k, v, _ := strings.Cut(part, "==")
			out = append(out, requirement{strings.TrimSpace(k), "=", strings.TrimSpace(v)})
		case strings.Contains(part, "="):
			k, v, _ := strings.Cut(part, "=")
			out = append(out, requirement{strings.TrimSpace(k), "=", strings.TrimSpace(v)})
		case strings.HasPrefix(part, "!"):
			out = append(out, requirement{key: strings.TrimPrefix(part, "!"), op: "!"})
		case part != "":
			out = append(out, requirement{key: part, op: "exists"})
		default:
			return nil, fmt.Errorf("ongeldige selector %q", s)
		}
	}
	return out, nil
}

func (sel selector) match(get func(string) (string, bool)) bool {
	for _, r := range sel {
		v, ok := get(r.key)
		switch r.op {
		case "=":
			if !ok || v != r.value {
				return false
			}
		case "!=":
			if ok && v == r.value {
				return false
			}
		case "exists":
			if !ok {
				return false
			}
		case "!":
			if ok {
				return false
			}
		}
	}
	return true
}

func (s *Server) groupList() map[string]any {
	var groups []map[string]any
	seen := map[string]int{}
	for _, k := range Kinds {
		if k.Group == "" {
			continue
		}
		gv := map[string]any{"groupVersion": k.GroupVersion(), "version": k.Version}
		if i, ok := seen[k.Group]; ok {
			vs := groups[i]["versions"].([]map[string]any)
			if !slices.ContainsFunc(vs, func(v map[string]any) bool { return v["version"] == k.Version }) {
				groups[i]["versions"] = append(vs, gv)
			}
			continue
		}
		seen[k.Group] = len(groups)
		groups = append(groups, map[string]any{"name": k.Group, "versions": []map[string]any{gv}, "preferredVersion": gv})
	}
	return map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": groups}
}

func (s *Server) resourceList(w http.ResponseWriter, gv string) {
	var res []map[string]any
	for _, k := range Kinds {
		if k.GroupVersion() != gv {
			continue
		}
		verbs := []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}
		res = append(res, map[string]any{
			"name": k.Resource, "singularName": strings.ToLower(k.Kind), "namespaced": k.Namespaced, "kind": k.Kind, "verbs": verbs,
		})
		for _, sub := range k.Subresources {
			res = append(res, map[string]any{"name": k.Resource + "/" + sub, "singularName": "", "namespaced": k.Namespaced, "kind": k.Kind, "verbs": []string{"get"}})
		}
	}
	if res == nil {
		status(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": gv, "resources": res})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func status(w http.ResponseWriter, code int, reason, msg string) {
	writeJSON(w, code, map[string]any{
		"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{}, "status": "Failure",
		"message": msg, "reason": reason, "code": code,
	})
}

// makeCerts maakt een CA, een servercertificaat voor 127.0.0.1 en een
// clientcertificaat in system:masters, zoals de beheerder van k3s.
func (s *Server) makeCerts() {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	now := time.Now()
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "k3s-server-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		panic(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	s.ca = pemBlock("CERTIFICATE", caDER)
	issue := func(serial int64, tpl *x509.Certificate) (certPEM, keyPEM []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		tpl.SerialNumber, tpl.NotBefore, tpl.NotAfter = big.NewInt(serial), now.Add(-time.Hour), now.Add(24*time.Hour)
		tpl.KeyUsage = x509.KeyUsageDigitalSignature
		der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			panic(err)
		}
		kder, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			panic(err)
		}
		return pemBlock("CERTIFICATE", der), pemBlock("EC PRIVATE KEY", kder)
	}
	s.serverCert, s.serverKey = issue(2, &x509.Certificate{
		Subject: pkix.Name{CommonName: "k3s"}, DNSNames: []string{"localhost", "kubernetes"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	s.clientCert, s.clientKey = issue(3, &x509.Certificate{
		Subject: pkix.Name{CommonName: "system:admin", Organization: []string{"system:masters"}}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
