package agent

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Een token voor een ServiceAccount van ClusterForge: de agent maakt het
// ServiceAccount met zijn ClusterRoleBinding en vraagt het token. Met een
// admin-token mag alles, met een view-token alleen lezen; na intrekken
// weigert de API het token.
func TestKubeBridgeToken(t *testing.T) {
	a, fake := kubeAgent(t)
	fake.SeedK3s(kubefake.NodeSpec{Name: "leren-server-01", IP: "10.0.40.11", Server: true})
	fake.Add(
		kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Image: "nginx:1.29", Replicas: 2, Ready: 2}),
		kubefake.Secret("demo", "api", "", map[string]string{"token": "geheim"}),
	)
	token := func(tr protocol.KubeTokenRequest) protocol.KubeResponse {
		t.Helper()
		return kubeSend(t, a, protocol.KubeRequest{ID: "t", Op: protocol.KubeToken, Token: &tr})
	}
	issue := func(account, role string) (protocol.KubeTokenResult, func(method, path, body string) (int, string)) {
		t.Helper()
		res := token(protocol.KubeTokenRequest{Account: account, Role: role, Seconds: 7 * 86400, Owner: "jonas"})
		var out protocol.KubeTokenResult
		if res.Status != http.StatusCreated || json.Unmarshal(res.Body, &out) != nil || out.Token == "" {
			t.Fatalf("token %s: %+v", account, res)
		}
		return out, kubefake.TokenClient(fake.URL(), out.CA, out.Token)
	}

	admin, asAdmin := issue("cf-jonas-1a2b3c", protocol.KubeAccessAdmin)
	if d := time.Until(admin.Expires); d < 7*24*time.Hour-time.Minute || d > 7*24*time.Hour+time.Minute || string(admin.CA) != string(fake.CA()) {
		t.Fatalf("token: verloopt %s, CA %q", admin.Expires, admin.CA)
	}
	sa := fake.Get("v1", "ServiceAccount", "clusterforge", "cf-jonas-1a2b3c")
	binding := fake.Get("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", "", "clusterforge:cf-jonas-1a2b3c")
	if sa == nil || binding == nil || fake.Get("v1", "Namespace", "", "clusterforge") == nil {
		t.Fatalf("objecten: %v %v", sa, binding)
	}
	if ann := sa["metadata"].(map[string]any)["annotations"].(map[string]any); ann["clusterforge.io/gebruiker"] != "jonas" {
		t.Fatalf("annotatie: %v", ann)
	}
	if ref := binding["roleRef"].(map[string]any); ref["name"] != "cluster-admin" {
		t.Fatalf("rol: %v", ref)
	}
	if code, body := asAdmin("GET", "/api/v1/namespaces/demo/secrets/api", ""); code != http.StatusOK || !strings.Contains(body, "Z2VoZWlt") {
		t.Fatalf("admin leest: %d %s", code, body)
	}
	if code, body := asAdmin("PATCH", "/apis/apps/v1/namespaces/demo/deployments/web", `{"spec":{"replicas":3}}`); code != http.StatusOK {
		t.Fatalf("admin schaalt: %d %s", code, body)
	}

	_, asViewer := issue("cf-lisa-4d5e6f", protocol.KubeAccessView)
	if code, _ := asViewer("GET", "/apis/apps/v1/namespaces/demo/deployments", ""); code != http.StatusOK {
		t.Fatalf("viewer leest: %d", code)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/namespaces/demo/secrets/api", ""},
		{"PATCH", "/apis/apps/v1/namespaces/demo/deployments/web", `{"spec":{"replicas":1}}`},
		{"DELETE", "/apis/apps/v1/namespaces/demo/deployments/web", ""},
	} {
		if code, body := asViewer(c.method, c.path, c.body); code != http.StatusForbidden || !strings.Contains(body, "system:serviceaccount:clusterforge:cf-lisa-4d5e6f") {
			t.Fatalf("viewer %s %s: %d %s", c.method, c.path, code, body)
		}
	}

	// Wat de agent niet doet, komt niet bij de API.
	before := len(fake.Requests())
	for _, tr := range []protocol.KubeTokenRequest{
		{Account: "default", Role: "admin", Seconds: 3600},
		{Account: "cf-", Role: "admin", Seconds: 3600},
		{Account: "cf-Jonas", Role: "admin", Seconds: 3600},
		{Account: "cf-x/../y", Role: "admin", Seconds: 3600},
		{Account: "cf-x", Role: "edit", Seconds: 3600},
		{Account: "cf-x", Role: "admin", Seconds: 60},
		{Account: "cf-x", Role: "admin", Seconds: 31 * 86400},
		{Account: "default", Revoke: true},
	} {
		if res := token(tr); res.Status != 0 || res.Error == "" {
			t.Fatalf("%+v: %+v", tr, res)
		}
	}
	if res := kubeSend(t, a, protocol.KubeRequest{ID: "t", Op: protocol.KubeToken}); res.Error == "" {
		t.Fatalf("zonder token: %+v", res)
	}
	if reqs := fake.Requests(); len(reqs) != before {
		t.Fatalf("toch naar de API: %v", reqs[before:])
	}

	// Intrekken haalt beide objecten weg, en het token werkt meteen niet
	// meer; nog eens intrekken mag.
	for range 2 {
		if res := token(protocol.KubeTokenRequest{Account: "cf-jonas-1a2b3c", Revoke: true}); res.Status != http.StatusOK || res.Error != "" {
			t.Fatalf("intrekken: %+v", res)
		}
	}
	if fake.Get("v1", "ServiceAccount", "clusterforge", "cf-jonas-1a2b3c") != nil ||
		fake.Get("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", "", "clusterforge:cf-jonas-1a2b3c") != nil {
		t.Fatal("niet weggehaald")
	}
	if code, _ := asAdmin("GET", "/api/v1/namespaces", ""); code != http.StatusUnauthorized {
		t.Fatalf("na intrekken: %d", code)
	}
	if code, _ := asViewer("GET", "/api/v1/namespaces", ""); code != http.StatusOK {
		t.Fatalf("de andere werkt nog: %d", code)
	}
	if !slices.Contains(fake.Requests(), "POST /api/v1/namespaces/clusterforge/serviceaccounts/cf-lisa-4d5e6f/token") {
		t.Fatalf("verzoeken: %v", fake.Requests())
	}
}
