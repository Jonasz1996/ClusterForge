package agent

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Schalen, cordon, terugdraaien, weghalen en uitzetten gaan door naar de
// API, met ClusterForge als field manager; een weggehaald Secret komt
// zonder waarden terug.
func TestKubeBridgeWrite(t *testing.T) {
	a, fake := kubeAgent(t)
	fake.SeedK3s(kubefake.NodeSpec{Name: "leren-server-01", IP: "10.0.40.11", Server: true})
	web := map[string]string{"app": "web"}
	fake.Add(
		kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Image: "nginx:1.29", Replicas: 3, Ready: 3}),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "web-1", Node: "leren-server-01", Labels: web, OwnerKind: "ReplicaSet", OwnerName: "web-7c9"}),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "web-2", Node: "leren-server-01", Labels: web, OwnerKind: "ReplicaSet", OwnerName: "web-7c9"}),
		kubefake.Secret("demo", "api", "", map[string]string{"token": "geheim-token-123"}),
		kubefake.PodDisruptionBudget("demo", "web", web, 2, 1),
	)
	patch := func(path, typ, body string, query map[string]string) protocol.KubeResponse {
		t.Helper()
		return kubeSend(t, a, protocol.KubeRequest{ID: "p", Op: protocol.KubePatch, Path: path, PatchType: typ, Body: json.RawMessage(body), Query: query})
	}
	replicas := func() any {
		return fake.Get("apps/v1", "Deployment", "demo", "web")["spec"].(map[string]any)["replicas"]
	}

	// Schalen via de subresource scale; dryRun verandert niets.
	if res := patch("/apis/apps/v1/namespaces/demo/deployments/web/scale", protocol.KubeMergePatch, `{"spec":{"replicas":5}}`,
		map[string]string{"dryRun": "All"}); res.Status != http.StatusOK || replicas() != 3.0 {
		t.Fatalf("dry-run: %+v %v", res, replicas())
	}
	if res := patch("/apis/apps/v1/namespaces/demo/deployments/web/scale", protocol.KubeMergePatch, `{"spec":{"replicas":5}}`, nil); res.Status != http.StatusOK || replicas() != 5.0 {
		t.Fatalf("schalen: %+v %v", res, replicas())
	}
	if reqs := fake.Requests(); !slices.Contains(reqs, "PATCH /apis/apps/v1/namespaces/demo/deployments/web/scale?fieldManager=clusterforge") {
		t.Fatalf("verzoeken: %v", reqs)
	}
	// Cordon op een node.
	if res := patch("/api/v1/nodes/leren-server-01", protocol.KubeMergePatch, `{"spec":{"unschedulable":true}}`, nil); res.Status != http.StatusOK ||
		fake.Get("v1", "Node", "", "leren-server-01")["spec"].(map[string]any)["unschedulable"] != true {
		t.Fatalf("cordon: %+v", res)
	}
	// Terugdraaien vervangt het hele podtemplate.
	tpl := `[{"op":"replace","path":"/spec/template","value":{"metadata":{"labels":{"app":"web"}},"spec":{"containers":[{"name":"web","image":"nginx:1.28"}]}}}]`
	if res := patch("/apis/apps/v1/namespaces/demo/deployments/web", protocol.KubeJSONPatch, tpl, nil); res.Status != http.StatusOK {
		t.Fatalf("terugdraaien: %+v", res)
	}
	got, _ := json.Marshal(fake.Get("apps/v1", "Deployment", "demo", "web")["spec"].(map[string]any)["template"])
	if !strings.Contains(string(got), "nginx:1.28") {
		t.Fatalf("template: %s", got)
	}

	// Uitzetten: het budget laat er één gaan, de tweede moet wachten.
	evict := func(pod string) protocol.KubeResponse {
		t.Helper()
		return kubeDo(t, a, protocol.KubeEvict, "/api/v1/namespaces/demo/pods/"+pod+"/eviction", nil)
	}
	if res := evict("web-1"); res.Status != http.StatusCreated || fake.Get("v1", "Pod", "demo", "web-1") != nil {
		t.Fatalf("uitzetten: %+v", res)
	}
	if res := evict("web-2"); res.Status != http.StatusTooManyRequests || !strings.Contains(string(res.Body), "disruption budget") ||
		fake.Get("v1", "Pod", "demo", "web-2") == nil {
		t.Fatalf("budget: %+v", res)
	}

	// Weghalen; het Secret komt terug zonder zijn waarde.
	res := kubeDo(t, a, protocol.KubeDelete, "/api/v1/namespaces/demo/secrets/api", map[string]string{"propagationPolicy": "Background"})
	if res.Status != http.StatusOK || fake.Get("v1", "Secret", "demo", "api") != nil {
		t.Fatalf("weghalen: %+v", res)
	}
	if strings.Contains(string(res.Body), "Z2VoZWlt") || strings.Contains(string(res.Body), "geheim-token") || !strings.Contains(string(res.Body), "verborgen") {
		t.Fatalf("secret in het antwoord: %s", res.Body)
	}
	if res := kubeDo(t, a, protocol.KubeDelete, "/apis/apps/v1/namespaces/demo/deployments/web", nil); res.Status != http.StatusOK {
		t.Fatalf("deployment weghalen: %+v", res)
	}
	if res := kubeDo(t, a, protocol.KubeDelete, "/apis/apps/v1/namespaces/demo/deployments/web", nil); res.Status != http.StatusNotFound {
		t.Fatalf("tweede keer: %+v", res)
	}
}

// Wat de brug niet kent, komt nooit bij de API: patch op andere soorten,
// subresources of met vreemde parameters, delete van een lijst of een
// namespace van Kubernetes, en eviction van iets anders dan een pod.
func TestKubeBridgeWriteRefuses(t *testing.T) {
	a, fake := kubeAgent(t)
	merge := func(path, body string, query map[string]string) protocol.KubeRequest {
		return protocol.KubeRequest{ID: "p", Op: protocol.KubePatch, Path: path, PatchType: protocol.KubeMergePatch, Body: json.RawMessage(body), Query: query}
	}
	big := `{"metadata":{"annotations":{"x":"` + strings.Repeat("a", protocol.KubeMaxPatch) + `"}}}`
	for _, req := range []protocol.KubeRequest{
		merge("/api/v1/namespaces/demo/secrets/api", `{"data":{"token":"eA=="}}`, nil),
		merge("/api/v1/namespaces/demo/configmaps/web", `{"data":{"a":"b"}}`, nil),
		merge("/api/v1/namespaces/demo/pods/web-1", `{"metadata":{"labels":{"a":"b"}}}`, nil),
		merge("/apis/apps/v1/namespaces/demo/deployments/web/status", `{"status":{"replicas":9}}`, nil),
		merge("/apis/apps/v1/namespaces/demo/daemonsets/svclb/scale", `{"spec":{"replicas":9}}`, nil),
		merge("/apis/apps/v1/namespaces/demo/deployments", `{"spec":{"replicas":9}}`, nil),
		merge("/apis/apps/v1/deployments/web", `{"spec":{"replicas":9}}`, nil),
		merge("/api/v1/namespaces/demo/nodes/leren-server-01", `{"spec":{"unschedulable":true}}`, nil),
		merge("/api/v1/nodes/leren-server-01/proxy", `{}`, nil),
		merge("/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/admin", `{"subjects":[]}`, nil),
		merge("/apis/apps/v1beta1/namespaces/demo/deployments/web", `{"spec":{"replicas":9}}`, nil),
		merge("/apis/apps/v1/namespaces/demo/deployments/web", `[{"op":"remove","path":"/spec"}]`, nil),
		merge("/apis/apps/v1/namespaces/demo/deployments/web", big, nil),
		merge("/apis/apps/v1/namespaces/demo/deployments/web", `{}`, map[string]string{"fieldManager": "kubectl"}),
		merge("/apis/apps/v1/namespaces/demo/deployments/web", `{}`, map[string]string{"force": "true"}),
		merge("/apis/apps/v1/namespaces/demo/deployments/web", `{}`, map[string]string{"dryRun": "None"}),
		{ID: "j", Op: protocol.KubePatch, Path: "/apis/apps/v1/namespaces/demo/deployments/web", PatchType: protocol.KubeJSONPatch, Body: json.RawMessage(`{}`)},
		{ID: "s", Op: protocol.KubePatch, Path: "/apis/apps/v1/namespaces/demo/deployments/web", PatchType: "strategic", Body: json.RawMessage(`{}`)},
		{ID: "d1", Op: protocol.KubeDelete, Path: "/api/v1/namespaces/demo/pods"},
		{ID: "d2", Op: protocol.KubeDelete, Path: "/api/v1/namespaces/kube-system"},
		{ID: "d3", Op: protocol.KubeDelete, Path: "/api/v1/namespaces/default"},
		{ID: "d4", Op: protocol.KubeDelete, Path: "/api/v1/namespaces/kube-node-lease"},
		{ID: "d5", Op: protocol.KubeDelete, Path: "/api/v1/namespaces/demo/pods/web-1/exec"},
		{ID: "d6", Op: protocol.KubeDelete, Path: "/api/v1/namespaces/demo/pods/web-1", Query: map[string]string{"propagationPolicy": "Alles"}},
		{ID: "d7", Op: protocol.KubeDelete, Path: "/api/v1/namespaces/demo/pods/web-1", Query: map[string]string{"gracePeriodSeconds": "-1"}},
		{ID: "d8", Op: protocol.KubeDelete, Path: "/api/v1/namespaces/demo/pods/web-1", Query: map[string]string{"labelSelector": "app=web"}},
		{ID: "d9", Op: protocol.KubeDelete, Path: "/apis"},
		{ID: "e1", Op: protocol.KubeEvict, Path: "/api/v1/namespaces/demo/pods/web-1"},
		{ID: "e2", Op: protocol.KubeEvict, Path: "/apis/apps/v1/namespaces/demo/deployments/web/eviction"},
		{ID: "e3", Op: protocol.KubeEvict, Path: "/api/v1/namespaces/demo/pods/web-1/eviction", Query: map[string]string{"gracePeriodSeconds": "0"}},
		{ID: "e4", Op: protocol.KubeEvict, Path: "/api/v1/nodes/leren-server-01/eviction"},
	} {
		if res := kubeSend(t, a, req); res.Error == "" || res.Status != 0 {
			t.Errorf("%s %s %v: %+v", req.Op, req.Path, req.Query, res)
		}
	}
	if reqs := fake.Requests(); len(reqs) != 0 {
		t.Fatalf("verzoeken bij de API: %v", reqs)
	}
}
