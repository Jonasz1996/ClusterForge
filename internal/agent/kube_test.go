package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestParseKubePath(t *testing.T) {
	for p, want := range map[string]kubeTarget{
		"/version":                       {Info: "/version"},
		"/api":                           {Discovery: true},
		"/api/v1":                        {Discovery: true, Version: "v1"},
		"/apis/apps":                     {Discovery: true, Group: "apps"},
		"/apis/apps/v1":                  {Discovery: true, Group: "apps", Version: "v1"},
		"/api/v1/nodes":                  {Version: "v1", Resource: "nodes"},
		"/api/v1/nodes/leren-server-01":  {Version: "v1", Resource: "nodes", Name: "leren-server-01"},
		"/api/v1/namespaces":             {Version: "v1", Resource: "namespaces"},
		"/api/v1/namespaces/demo":        {Version: "v1", Resource: "namespaces", Name: "demo"},
		"/api/v1/namespaces/demo/status": {Version: "v1", Resource: "namespaces", Name: "demo", Sub: "status"},
		"/api/v1/namespaces/demo/pods":   {Version: "v1", Namespace: "demo", Resource: "pods"},
		"/api/v1/namespaces/demo/pods/web-1/exec":   {Version: "v1", Namespace: "demo", Resource: "pods", Name: "web-1", Sub: "exec"},
		"/apis/apps/v1/namespaces/demo/deployments": {Group: "apps", Version: "v1", Namespace: "demo", Resource: "deployments"},
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/system:aggregate-to-view": {
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles", Name: "system:aggregate-to-view",
		},
	} {
		got, err := parseKubePath(p)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v, wil %+v", p, got, err, want)
		}
	}
	for _, p := range []string{
		"", "api/v1/pods", "/api/v1/../v1/pods", "/api/v1//pods", "/api/v1/pods/", "/api/v1/namespaces/demo/pods/web-1/proxy/metrics",
		"/api/v1/nodes/n1/proxy/stats", "/api/v1/pods%2Fexec", "/api/v1/pods?watch=true", "/metrics", "/healthz", "/apis/apps/v1/./deployments",
		"/api/v1/namespaces/-demo/pods", "/openapi/v2", "/api/v1/pods/a b",
	} {
		if _, err := parseKubePath(p); err == nil {
			t.Errorf("%q aanvaard", p)
		}
	}
}

// kubeAgent is een agent met een nep-API van k3s achter zijn kubeconfig.
func kubeAgent(t *testing.T) (*Agent, *kubefake.Server) {
	t.Helper()
	a, _ := applyAgent(t)
	fake := kubefake.New()
	fake.Start()
	t.Cleanup(fake.Close)
	if err := fake.WriteKubeconfig(a.Root); err != nil {
		t.Fatal(err)
	}
	a.KubeletHealthz = fake.KubeletURL()
	return a, fake
}

func kubeRead(t *testing.T, a *Agent, path string, query map[string]string) protocol.KubeResponse {
	t.Helper()
	return kubeDo(t, a, protocol.KubeRead, path, query)
}

func kubeDo(t *testing.T, a *Agent, op, path string, query map[string]string) protocol.KubeResponse {
	t.Helper()
	return kubeSend(t, a, protocol.KubeRequest{ID: "k1", Op: op, Path: path, Query: query})
}

func kubeSend(t *testing.T, a *Agent, req protocol.KubeRequest) protocol.KubeResponse {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	env, err := json.Marshal(protocol.Envelope{V: protocol.Version, Type: protocol.TypeKubeRequest, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	res := a.handleKube(env)
	if len(res.Gzip) > 0 {
		zr, err := gzip.NewReader(bytes.NewReader(res.Gzip))
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		res.Body = b
	}
	return res
}

func TestKubeBridgeRead(t *testing.T) {
	a, fake := kubeAgent(t)
	fake.SeedK3s(kubefake.NodeSpec{Name: "leren-server-01", IP: "10.0.40.11", Server: true}, kubefake.NodeSpec{Name: "leren-worker-01", IP: "10.0.40.12"})
	pod := kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "web-1", Node: "leren-worker-01"})
	pod["metadata"].(map[string]any)["managedFields"] = []any{map[string]any{"manager": "kubectl", "operation": "Apply"}}
	fake.Add(kubefake.Namespace("demo"), pod)

	res := kubeRead(t, a, "/version", nil)
	if res.Status != 200 || !strings.Contains(string(res.Body), `"gitVersion":"v1.35.9+k3s1"`) {
		t.Fatalf("version: %+v", res)
	}
	res = kubeRead(t, a, "/api/v1/nodes", nil)
	var nodes struct {
		Kind  string
		Items []struct {
			Metadata struct{ Name string }
		}
	}
	if err := json.Unmarshal(res.Body, &nodes); err != nil || res.Status != 200 || nodes.Kind != "NodeList" || len(nodes.Items) != 2 {
		t.Fatalf("nodes: %+v %s", res, res.Body)
	}
	// Een lijst komt altijd in pagina's.
	if reqs := fake.Requests(); !slices.Contains(reqs, "GET /api/v1/nodes?limit=250") {
		t.Fatalf("verzoeken: %v", reqs)
	}
	res = kubeRead(t, a, "/api/v1/namespaces/demo/pods/web-1", nil)
	if res.Status != 200 || strings.Contains(string(res.Body), "managedFields") || !strings.Contains(string(res.Body), `"nodeName":"leren-worker-01"`) {
		t.Fatalf("pod: %s", res.Body)
	}
	res = kubeRead(t, a, "/api/v1/namespaces/demo/pods/bestaat-niet", nil)
	if res.Status != 404 || !strings.Contains(string(res.Body), `"reason":"NotFound"`) {
		t.Fatalf("niet gevonden: %+v %s", res, res.Body)
	}
	res = kubeRead(t, a, "/api/v1/pods", map[string]string{"fieldSelector": "spec.nodeName=leren-worker-01", "limit": "2"})
	var pods struct {
		Items    []any
		Metadata struct{ Continue string }
	}
	if err := json.Unmarshal(res.Body, &pods); err != nil || len(pods.Items) != 2 || pods.Metadata.Continue == "" {
		t.Fatalf("pagina: %s", res.Body)
	}
	res = kubeRead(t, a, "/api/v1/pods", map[string]string{"fieldSelector": "spec.nodeName=leren-worker-01", "limit": "2", "continue": pods.Metadata.Continue})
	if err := json.Unmarshal(res.Body, &pods); err != nil || len(pods.Items) == 0 {
		t.Fatalf("volgende pagina: %s", res.Body)
	}
	if res := kubeRead(t, a, "/apis/apps/v1", nil); res.Status != 200 || !strings.Contains(string(res.Body), `"name":"deployments"`) {
		t.Fatalf("discovery: %s", res.Body)
	}

	// /readyz geeft alleen de status.
	if res := kubeRead(t, a, "/readyz", nil); res.Status != 200 || len(res.Body) != 0 {
		t.Fatalf("readyz: %+v", res)
	}
	fake.SetReady(false)
	if res := kubeRead(t, a, "/readyz", nil); res.Status != 500 {
		t.Fatalf("readyz niet klaar: %+v", res)
	}
}

// Geen secretwaarde verlaat de agent: niet uit een Secret, niet uit een
// lijst, niet uit de annotatie van kubectl apply.
func TestKubeBridgeHidesSecrets(t *testing.T) {
	a, fake := kubeAgent(t)
	const value, plain = "c3VwZXItZ2VoZWltLXdhY2h0d29vcmQ=", "nog-een-geheim"
	applied := fmt.Sprintf(`{"apiVersion":"v1","kind":"Secret","data":{"wachtwoord":%q}}`, value)
	fake.Add(kubefake.Namespace("demo"), map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]any{
			"name": "db", "namespace": "demo", "annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": applied, "team": "web"},
			"managedFields": []any{map[string]any{"manager": "kubectl", "fieldsV1": map[string]any{"f:data": map[string]any{"f:wachtwoord": map[string]any{}}}}},
		},
		"data":       map[string]any{"wachtwoord": value, "leeg": ""},
		"stringData": map[string]any{"extra": plain},
	})
	for _, p := range []string{"/api/v1/namespaces/demo/secrets/db", "/api/v1/namespaces/demo/secrets", "/api/v1/secrets"} {
		res := kubeRead(t, a, p, nil)
		body := string(res.Body)
		if res.Status != 200 || strings.Contains(body, value) || strings.Contains(body, "super-geheim") || strings.Contains(body, plain) {
			t.Fatalf("%s lekt een geheim: %s", p, body)
		}
		for _, want := range []string{`"wachtwoord":"(verborgen, 23 bytes)"`, `"leeg":"(verborgen, 0 bytes)"`, `"extra":"(verborgen, 14 bytes)"`, `"team":"web"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s mist %s: %s", p, want, body)
			}
		}
		if strings.Contains(body, "managedFields") {
			t.Errorf("%s heeft managedFields: %s", p, body)
		}
	}
	if n, ok := protocol.KubeHiddenLength("(verborgen, 23 bytes)"); !ok || n != 23 {
		t.Fatalf("lengte: %d %v", n, ok)
	}

	// Ook een lijst met een Secret erin, zoals een kind List, en een Secret
	// met data in een vreemde vorm.
	for _, v := range []map[string]any{
		{"kind": "List", "items": []any{map[string]any{"kind": "Secret", "data": map[string]any{"a": value}}}},
		{"kind": "Secret", "data": []any{value}},
		{"kind": "Secret", "stringData": value},
	} {
		scrub(v, false)
		if b, _ := json.Marshal(v); strings.Contains(string(b), value) {
			t.Errorf("scrub lekt: %s", b)
		}
	}
}

// exec, attach, portforward en proxy bestaan niet in de brug: zo'n verzoek
// komt nooit bij de API. Logs en schrijven ook niet, via read.
func TestKubeBridgeRefuses(t *testing.T) {
	a, fake := kubeAgent(t)
	fake.SeedK3s(kubefake.NodeSpec{Name: "leren-server-01", IP: "10.0.40.11", Server: true})
	for _, c := range []struct {
		path  string
		query map[string]string
	}{
		{"/api/v1/namespaces/kube-system/pods/coredns-5d9c7b6f4-x0q/exec", map[string]string{"command": "sh"}},
		{"/api/v1/namespaces/kube-system/pods/coredns-5d9c7b6f4-x0q/attach", nil},
		{"/api/v1/namespaces/kube-system/pods/coredns-5d9c7b6f4-x0q/portforward", nil},
		{"/api/v1/namespaces/kube-system/pods/coredns-5d9c7b6f4-x0q/proxy", nil},
		{"/api/v1/namespaces/kube-system/pods/coredns-5d9c7b6f4-x0q/log", nil},
		{"/api/v1/namespaces/kube-system/pods/coredns-5d9c7b6f4-x0q/eviction", nil},
		{"/api/v1/namespaces/default/serviceaccounts/default/token", nil},
		{"/api/v1/nodes/leren-server-01/proxy", nil},
		{"/api/v1/nodes/leren-server-01/proxy/configz", nil},
		{"/api/v1/namespaces/default/services/kubernetes/proxy/", nil},
		{"/api/v1/pods", map[string]string{"watch": "true"}},
		{"/api/v1/pods", map[string]string{"limit": "100000"}},
		{"/api/v1/nodes/leren-server-01", map[string]string{"limit": "1"}},
		{"/api/v1/pods", map[string]string{"pretty": "true"}},
		{"/metrics", nil},
		{"/debug/pprof/heap", nil},
	} {
		res := kubeRead(t, a, c.path, c.query)
		if res.Error == "" || res.Status != 0 || len(res.Body) != 0 {
			t.Errorf("%s %v: %+v", c.path, c.query, res)
		}
	}
	if reqs := fake.Requests(); len(reqs) != 0 {
		t.Fatalf("verzoeken bij de API: %v", reqs)
	}

	// Een onbekende soort verzoek, en een verlopen verzoek.
	for _, req := range []protocol.KubeRequest{
		{ID: "x", Op: "exec", Path: "/api/v1/namespaces/default/pods/web/exec"},
		{ID: "y", Op: protocol.KubeRead, Path: "/version", Deadline: time.Now().Add(-time.Minute)},
	} {
		body, _ := json.Marshal(req)
		env, _ := json.Marshal(protocol.Envelope{Type: protocol.TypeKubeRequest, Body: body})
		if res := a.handleKube(env); res.Error == "" {
			t.Errorf("%+v: %+v", req, res)
		}
	}
	if reqs := fake.Requests(); len(reqs) != 0 {
		t.Fatalf("verzoeken bij de API: %v", reqs)
	}
}

func TestKubeBridgeLarge(t *testing.T) {
	a, fake := kubeAgent(t)
	fake.Add(kubefake.Namespace("groot"))
	for i := range 400 {
		cm := map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": fmt.Sprintf("cm-%03d", i), "namespace": "groot"},
			"data": map[string]any{"inhoud": strings.Repeat(fmt.Sprintf("regel %d ", i), 40)},
		}
		fake.Add(cm)
	}
	body, _ := json.Marshal(protocol.KubeRequest{ID: "k", Op: protocol.KubeRead, Path: "/api/v1/namespaces/groot/configmaps"})
	env, _ := json.Marshal(protocol.Envelope{Type: protocol.TypeKubeRequest, Body: body})
	raw := a.handleKube(env)
	if raw.Status != 200 || len(raw.Gzip) == 0 || len(raw.Body) != 0 || len(raw.Gzip) > protocol.KubeMaxBody {
		t.Fatalf("groot antwoord: status %d, body %d, gzip %d, %s", raw.Status, len(raw.Body), len(raw.Gzip), raw.Error)
	}
	res := kubeRead(t, a, "/api/v1/namespaces/groot/configmaps", nil)
	var list struct{ Items []any }
	if err := json.Unmarshal(res.Body, &list); err != nil || len(list.Items) != 250 {
		t.Fatalf("uitgepakt: %d %v", len(list.Items), err)
	}

	// Wat ook gecomprimeerd niet past, geeft een fout met de oplossing.
	rnd := rand.New(rand.NewPCG(1, 2))
	big := make([]byte, 3<<20)
	for i := range big {
		big[i] = byte('a' + rnd.IntN(26))
	}
	r := kubeAnswer(kubeTarget{Resource: "configmaps"}, 200, []byte(`{"kind":"ConfigMap","data":{"x":"`+string(big)+`"}}`))
	if r.Error == "" || !strings.Contains(r.Error, "filter op namespace") || len(r.Gzip) != 0 {
		t.Fatalf("te groot: %+v", r.Error)
	}
	// Geen JSON gaat niet terug.
	if r := kubeAnswer(kubeTarget{Resource: "secrets"}, 200, []byte("wachtwoord=geheim")); r.Error == "" || len(r.Body) != 0 {
		t.Fatalf("geen JSON: %+v", r)
	}
}

func TestKubeBridgeKubeconfig(t *testing.T) {
	a, _ := applyAgent(t)
	if res := kubeRead(t, a, "/version", nil); !strings.Contains(res.Error, "k3s.yaml") {
		t.Fatalf("zonder kubeconfig: %+v", res)
	}
	// k3s vernieuwt zijn certificaten; de agent leest de nieuwe kubeconfig.
	first := kubefake.New()
	first.Start()
	defer first.Close()
	second := kubefake.New()
	second.Start()
	defer second.Close()
	second.SetVersion("v1.36.5+k3s1")
	if err := first.WriteKubeconfig(a.Root); err != nil {
		t.Fatal(err)
	}
	if res := kubeRead(t, a, "/version", nil); !strings.Contains(string(res.Body), "v1.35.9+k3s1") {
		t.Fatalf("eerste: %+v", res)
	}
	p := filepath.Join(a.Root, K3sKubeconfig)
	data := second.Kubeconfig()
	if err := os.WriteFile(p, append(data, []byte("# nieuw\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := kubeRead(t, a, "/version", nil); !strings.Contains(string(res.Body), "v1.36.5+k3s1") {
		t.Fatalf("tweede: %+v", res)
	}
	// Een kubeconfig met een andere CA wordt niet vertrouwd.
	mixed := strings.Replace(string(data), "server: "+second.URL(), "server: "+first.URL(), 1)
	if err := os.WriteFile(p, []byte(mixed), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := kubeRead(t, a, "/version", nil); res.Status != 0 || !strings.Contains(res.Error, "antwoordt niet") {
		t.Fatalf("verkeerde CA: %+v", res)
	}
	if _, err := parseKubeconfig([]byte("server: http://127.0.0.1:6443\n")); err == nil {
		t.Fatal("http aanvaard")
	}
}

func TestKubeFacts(t *testing.T) {
	a, fake := kubeAgent(t)
	ctx := context.Background()
	f := a.kubeFacts(ctx, "leren-server-01")
	if f == nil || f.Role != "server" || f.NodeName != "leren-server-01" || !f.APIReady || !f.KubeletReady {
		t.Fatalf("server: %+v", f)
	}
	if err := os.WriteFile(filepath.Join(a.Root, k3sConfig), []byte("token: \"x\"\nnode-name: \"k8s-01\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.SetReady(false)
	fake.SetKubelet(false)
	if f := a.kubeFacts(ctx, "leren-server-01"); f.NodeName != "k8s-01" || f.APIReady || f.KubeletReady {
		t.Fatalf("niet klaar: %+v", f)
	}

	b, _ := applyAgent(t)
	if f := b.kubeFacts(ctx, "web-01"); f != nil {
		t.Fatalf("zonder k3s: %+v", f)
	}
	if err := os.MkdirAll(filepath.Join(b.Root, "etc/systemd/system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Root, k3sAgentUnit), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.KubeletHealthz = fake.KubeletURL()
	fake.SetKubelet(true)
	if f := b.kubeFacts(ctx, "leren-worker-01"); f == nil || f.Role != "agent" || f.APIReady || !f.KubeletReady {
		t.Fatalf("worker: %+v", f)
	}
}

// De log van een container gaat als tekst terug, met hoogstens 5000 regels
// en 2 MB; alleen het pad log van een pod mag.
func TestKubeBridgeLogs(t *testing.T) {
	a, fake := kubeAgent(t)
	fake.Add(kubefake.Namespace("demo"),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "web-1", Node: "leren-worker-01", Waiting: "CrashLoopBackOff", Restarts: 3}),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "web-2", Node: "leren-worker-01"}))
	fake.SetLog("demo", "web-1", "main", false, "start", "verbinden met db", "fout: db onbereikbaar")
	fake.SetLog("demo", "web-1", "main", true, "start", "paniek: geen geheugen")
	const path = "/api/v1/namespaces/demo/pods/web-1/log"

	text := func(res protocol.KubeResponse) string {
		t.Helper()
		var s string
		if res.Status != 200 || json.Unmarshal(res.Body, &s) != nil {
			t.Fatalf("log: %+v %s", res, res.Body)
		}
		return s
	}
	if got := text(kubeDo(t, a, protocol.KubeLogs, path, nil)); got != "start\nverbinden met db\nfout: db onbereikbaar\n" {
		t.Fatalf("log: %q", got)
	}
	if got := text(kubeDo(t, a, protocol.KubeLogs, path, map[string]string{"tailLines": "1", "container": "main"})); got != "fout: db onbereikbaar\n" {
		t.Fatalf("laatste regel: %q", got)
	}
	if got := text(kubeDo(t, a, protocol.KubeLogs, path, map[string]string{"previous": "true"})); got != "start\npaniek: geen geheugen\n" {
		t.Fatalf("vorige: %q", got)
	}
	got := text(kubeDo(t, a, protocol.KubeLogs, path, map[string]string{"timestamps": "true", "tailLines": "1"}))
	if ts, rest, _ := strings.Cut(got, " "); rest != "fout: db onbereikbaar\n" || !strings.HasPrefix(ts, "20") {
		t.Fatalf("met tijden: %q", got)
	}
	// Een fout van de API komt als JSON terug, met de status.
	res := kubeDo(t, a, protocol.KubeLogs, "/api/v1/namespaces/demo/pods/web-2/log", map[string]string{"previous": "true"})
	if res.Status != 400 || !strings.Contains(string(res.Body), "previous terminated container") {
		t.Fatalf("geen vorige: %+v %s", res, res.Body)
	}
	limited := false
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r, "GET "+path) && strings.Contains(r, "tailLines=500") {
			limited = true
		}
	}
	if !limited {
		t.Fatalf("zonder grenzen gevraagd: %v", fake.Requests())
	}

	// Van een te grote log blijven de nieuwste regels over, ook als hij
	// gecomprimeerd niet in een bericht past.
	rnd := rand.New(rand.NewPCG(1, 2))
	for _, c := range []struct {
		name string
		line func(i int) string
	}{
		{"herhalend", func(i int) string { return fmt.Sprintf("regel %04d %s", i, strings.Repeat("x", 990)) }},
		{"willekeurig", func(i int) string {
			b := make([]byte, 750)
			for j := range b {
				b[j] = byte(rnd.Uint32())
			}
			return fmt.Sprintf("regel %04d %s", i, base64.StdEncoding.EncodeToString(b))
		}},
	} {
		lines := make([]string, 3000)
		for i := range lines {
			lines[i] = c.line(i)
		}
		fake.SetLog("demo", "web-2", "main", false, lines...)
		got := text(kubeDo(t, a, protocol.KubeLogs, "/api/v1/namespaces/demo/pods/web-2/log", map[string]string{"tailLines": "5000"}))
		rest, ok := strings.CutPrefix(got, protocol.KubeLogTrimmed)
		if !ok || len(rest) > protocol.KubeMaxLogBytes || !strings.HasSuffix(rest, lines[2999]+"\n") || !strings.HasPrefix(rest, "regel ") {
			t.Fatalf("%s: %d bytes, begint met %.40q", c.name, len(got), got)
		}
	}

	before := len(fake.Requests())
	for _, c := range []struct {
		path  string
		query map[string]string
	}{
		{"/api/v1/namespaces/demo/pods/web-1/exec", nil},
		{"/api/v1/namespaces/demo/pods/web-1/attach", nil},
		{"/api/v1/namespaces/demo/pods/web-1", nil},
		{"/api/v1/namespaces/demo/secrets/db/log", nil},
		{"/api/v1/nodes/leren-worker-01/proxy/logs", nil},
		{"/apis/apps/v1/namespaces/demo/deployments/web/log", nil},
		{path, map[string]string{"follow": "true"}},
		{path, map[string]string{"tailLines": "0"}},
		{path, map[string]string{"tailLines": "5001"}},
		{path, map[string]string{"limitBytes": "999999999"}},
		{path, map[string]string{"container": "../main"}},
		{path, map[string]string{"previous": "ja"}},
	} {
		if res := kubeDo(t, a, protocol.KubeLogs, c.path, c.query); res.Status != 0 || res.Error == "" {
			t.Errorf("%s %v: %+v", c.path, c.query, res)
		}
	}
	if after := fake.Requests()[before:]; len(after) > 0 {
		t.Fatalf("de API kreeg %v", after)
	}
}
