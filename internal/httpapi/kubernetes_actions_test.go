package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type kubeActionResult struct {
	Message string `json:"message"`
	Changed bool   `json:"changed"`
}

type drainPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

type drainPlan struct {
	Node     string     `json:"node"`
	Cordoned bool       `json:"cordoned"`
	Evict    []drainPod `json:"evict"`
	Skip     []drainPod `json:"skip"`
	Blocked  []drainPod `json:"blocked"`
}

func podNames(ps []drainPod) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Namespace+"/"+p.Name)
	}
	return out
}

type auditItems struct {
	Items []struct {
		Action  string `json:"action"`
		Summary string `json:"summary"`
	} `json:"items"`
}

// auditHas zoekt een regel in het logboek van Kubernetes.
func auditHas(t *testing.T, c *client, action, summary string) {
	t.Helper()
	var a auditItems
	c.do("GET", "/api/v1/audit?category=kubernetes", nil, &a)
	for _, it := range a.Items {
		if it.Action == action && it.Summary == summary {
			return
		}
	}
	t.Fatalf("geen %s %q in het logboek: %+v", action, summary, a.Items)
}

// Schalen, herstarten, terugdraaien, weghalen, cordon en uncordon werken
// via de agent, staan in het logboek, en kunnen alleen waar ze passen.
func TestKubernetesActions(t *testing.T) {
	e, c := adminClient(t)
	fake := kubeCluster(t)
	ctx := context.Background()
	d := kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Image: "nginx:1.29", Replicas: 3, Ready: 2})
	d["metadata"].(map[string]any)["annotations"] = map[string]any{"deployment.kubernetes.io/revision": "2"}
	fake.Add(
		d,
		kubefake.Revision("demo", "web", "6f8b9", 1, 0, "nginx:1.28"),
		kubefake.Revision("demo", "web", "7c9d4", 2, 3, "nginx:1.29"),
		kubefake.StatefulSet(kubefake.WorkloadSpec{Namespace: "demo", Name: "db", Image: "postgres:18", Replicas: 1, Ready: 1}),
	)

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	var server node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000005", &addrs{}, func(_ *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
	})
	base := "/api/v1/kubernetes/clusters/" + cl.ID
	eventually(t, "overzicht", func() bool { return c.do("GET", base, nil, nil) == http.StatusOK })
	deployment := map[string]any{"group": "apps", "version": "v1", "resource": "deployments", "namespace": "demo", "name": "web"}
	act := func(body map[string]any) (int, kubeActionResult, apiErr) {
		t.Helper()
		var res kubeActionResult
		var ae apiErr
		raw := json.RawMessage{}
		s := c.do("POST", base+"/actions", body, &raw)
		if s == http.StatusOK {
			_ = json.Unmarshal(raw, &res)
		} else {
			_ = json.Unmarshal(raw, &ae)
		}
		return s, res, ae
	}
	spec := func(kind, ns, name string) map[string]any {
		o := fake.Get("apps/v1", kind, ns, name)
		if o == nil {
			t.Fatalf("%s %s/%s is weg", kind, ns, name)
		}
		return o["spec"].(map[string]any)
	}

	// Het object zegt welke acties kunnen.
	var obj struct {
		Actions  []string `json:"actions"`
		Replicas *int     `json:"replicas"`
	}
	c.do("GET", base+"/object?group=apps&version=v1&resource=deployments&namespace=demo&name=web", nil, &obj)
	if !slices.Equal(obj.Actions, []string{"scale", "restart", "rollback", "delete"}) || obj.Replicas == nil || *obj.Replicas != 3 {
		t.Fatalf("acties: %+v", obj)
	}
	c.do("GET", base+"/object?version=v1&resource=nodes&name=leren-worker-01", nil, &obj)
	if !slices.Equal(obj.Actions, []string{"cordon", "drain"}) || obj.Replicas != nil {
		t.Fatalf("acties node: %+v", obj)
	}
	c.do("GET", base+"/object?version=v1&resource=namespaces&name=kube-system", nil, &obj)
	if len(obj.Actions) != 0 {
		t.Fatalf("acties kube-system: %+v", obj)
	}

	// Schalen.
	if s, res, ae := act(map[string]any{"action": "scale", "ref": deployment, "replicas": 5}); s != http.StatusOK || !res.Changed ||
		res.Message != "web geschaald van 3 naar 5 pods" || spec("Deployment", "demo", "web")["replicas"] != 5.0 {
		t.Fatalf("schalen: %d %+v %+v", s, res, ae)
	}
	auditHas(t, c, "kube.scaled", "Deployment demo/web geschaald van 3 naar 5 pods")
	if s, res, _ := act(map[string]any{"action": "scale", "ref": deployment, "replicas": 5}); s != http.StatusOK || res.Changed {
		t.Fatalf("nog eens: %d %+v", s, res)
	}
	for _, body := range []map[string]any{
		{"action": "scale", "ref": deployment},
		{"action": "scale", "ref": deployment, "replicas": 101},
		{"action": "exec", "ref": deployment},
		{"action": "scale", "ref": map[string]any{"group": "apps", "version": "v1", "resource": "deployments", "namespace": "demo", "name": "Web!"}, "replicas": 1},
		{"action": "scale", "ref": map[string]any{"group": "apps", "version": "v1", "resource": "deployments", "namespace": "", "name": "web"}, "replicas": 1},
	} {
		if s, _, ae := act(body); s != http.StatusBadRequest {
			t.Errorf("%v: %d %+v", body, s, ae)
		}
	}
	rs := map[string]any{"group": "apps", "version": "v1", "resource": "replicasets", "namespace": "demo", "name": "web-7c9d4"}
	if s, _, ae := act(map[string]any{"action": "scale", "ref": rs, "replicas": 1}); s != http.StatusConflict || !strings.Contains(ae.Message, "hoort bij Deployment web") {
		t.Fatalf("ReplicaSet schalen: %d %+v", s, ae)
	}

	// Herstarten zet een tijdstip in het podtemplate, zoals kubectl.
	if s, res, ae := act(map[string]any{"action": "restart", "ref": deployment}); s != http.StatusOK || !res.Changed {
		t.Fatalf("herstarten: %d %+v %+v", s, res, ae)
	}
	tpl := spec("Deployment", "demo", "web")["template"].(map[string]any)
	if ann, _ := tpl["metadata"].(map[string]any)["annotations"].(map[string]any); ann["kubectl.kubernetes.io/restartedAt"] == nil {
		t.Fatalf("template na herstarten: %v", tpl)
	}
	auditHas(t, c, "kube.restarted", "Deployment demo/web herstart")

	// De versies, en terug naar de vorige.
	var revs struct {
		Items []struct {
			Revision int64    `json:"revision"`
			Images   []string `json:"images"`
			Current  bool     `json:"current"`
		} `json:"items"`
	}
	if s := c.do("GET", base+"/revisions?namespace=demo&name=web", nil, &revs); s != http.StatusOK || len(revs.Items) != 2 ||
		revs.Items[0].Revision != 2 || !revs.Items[0].Current || revs.Items[1].Images[0] != "nginx:1.28" {
		t.Fatalf("versies: %d %+v", s, revs)
	}
	if s, res, ae := act(map[string]any{"action": "rollback", "ref": deployment, "revision": 2}); s != http.StatusConflict {
		t.Fatalf("naar de huidige: %d %+v %+v", s, res, ae)
	}
	if s, res, ae := act(map[string]any{"action": "rollback", "ref": deployment}); s != http.StatusOK ||
		res.Message != "web gaat terug naar versie 1 (nginx:1.28); Kubernetes vervangt de pods een voor een" {
		t.Fatalf("terugdraaien: %d %+v %+v", s, res, ae)
	}
	b, _ := json.Marshal(spec("Deployment", "demo", "web")["template"])
	if !strings.Contains(string(b), "nginx:1.28") || strings.Contains(string(b), "pod-template-hash") || strings.Contains(string(b), "restartedAt") {
		t.Fatalf("template na terugdraaien: %s", b)
	}
	auditHas(t, c, "kube.rolled_back", "Deployment demo/web teruggedraaid van versie 2 naar versie 1 (nginx:1.28)")
	sts := map[string]any{"group": "apps", "version": "v1", "resource": "statefulsets", "namespace": "demo", "name": "db"}
	if s, _, ae := act(map[string]any{"action": "rollback", "ref": sts}); s != http.StatusConflict || !strings.Contains(ae.Message, "alleen bij een Deployment") {
		t.Fatalf("StatefulSet terugdraaien: %d %+v", s, ae)
	}

	// Een pod weghalen: de ReplicaSet maakt een nieuwe.
	pod := map[string]any{"group": "", "version": "v1", "resource": "pods", "namespace": "demo", "name": "web-7c9-a"}
	if s, res, ae := act(map[string]any{"action": "delete", "ref": pod}); s != http.StatusOK ||
		res.Message != "Pod demo/web-7c9-a weggehaald; ReplicaSet web-7c9 maakt meteen een nieuwe" || fake.Get("v1", "Pod", "demo", "web-7c9-a") != nil {
		t.Fatalf("pod weghalen: %d %+v %+v", s, res, ae)
	}
	auditHas(t, c, "kube.deleted", "Pod demo/web-7c9-a weggehaald")
	if s, _, _ := act(map[string]any{"action": "delete", "ref": pod}); s != http.StatusNotFound {
		t.Fatalf("weg is weg: %d", s)
	}
	// Een node en een namespace van Kubernetes haal je zo niet weg.
	nodeRef := map[string]any{"group": "", "version": "v1", "resource": "nodes", "namespace": "", "name": "leren-worker-01"}
	for _, ref := range []map[string]any{nodeRef, {"group": "", "version": "v1", "resource": "namespaces", "namespace": "", "name": "kube-system"}} {
		if s, _, ae := act(map[string]any{"action": "delete", "ref": ref}); s != http.StatusConflict {
			t.Errorf("%v weghalen: %d %+v", ref, s, ae)
		}
	}
	if fake.Get("v1", "Node", "", "leren-worker-01") == nil || fake.Get("v1", "Namespace", "", "kube-system") == nil {
		t.Fatal("weg")
	}

	// Cordon en uncordon.
	unschedulable := func() any {
		return fake.Get("v1", "Node", "", "leren-worker-01")["spec"].(map[string]any)["unschedulable"]
	}
	if s, res, ae := act(map[string]any{"action": "cordon", "ref": nodeRef}); s != http.StatusOK || !res.Changed || unschedulable() != true {
		t.Fatalf("cordon: %d %+v %+v", s, res, ae)
	}
	if s, res, _ := act(map[string]any{"action": "cordon", "ref": nodeRef}); s != http.StatusOK || res.Changed {
		t.Fatalf("nog eens cordon: %d %+v", s, res)
	}
	if s, res, ae := act(map[string]any{"action": "uncordon", "ref": nodeRef}); s != http.StatusOK || !res.Changed || unschedulable() != nil {
		t.Fatalf("uncordon: %d %+v %+v", s, res, ae)
	}
	auditHas(t, c, "kube.cordoned", "Node leren-worker-01 op cordon gezet in Kubernetes")
	auditHas(t, c, "kube.uncordoned", "Node leren-worker-01 krijgt weer pods in Kubernetes")
	if s, _, ae := act(map[string]any{"action": "cordon", "ref": deployment}); s != http.StatusConflict {
		t.Fatalf("cordon op een Deployment: %d %+v", s, ae)
	}

	// Elke wijziging ging met ClusterForge als field manager.
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r, "PATCH ") && !strings.Contains(r, "fieldManager=clusterforge") {
			t.Errorf("zonder field manager: %s", r)
		}
	}

	// Een viewer leest de versies, maar doet niets.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("GET", base+"/revisions?namespace=demo&name=web", nil, nil); s != http.StatusOK {
		t.Fatalf("viewer versies: %d", s)
	}
	var ae apiErr
	if s := v.do("POST", base+"/actions", map[string]any{"action": "scale", "ref": deployment, "replicas": 1}, &ae); s != http.StatusForbidden {
		t.Fatalf("viewer schaalt: %d %+v", s, ae)
	}
	if s := v.do("POST", base+"/drain", map[string]any{"node": "leren-worker-01", "preview": true}, &ae); s != http.StatusForbidden {
		t.Fatalf("viewer maakt leeg: %d %+v", s, ae)
	}

	// Op prod: tweestapsverificatie en de slug.
	if _, err := e.pool.Exec(ctx, "UPDATE clusters SET environment = 'prod' WHERE id = $1", cl.ID); err != nil {
		t.Fatal(err)
	}
	if s, _, ae := act(map[string]any{"action": "scale", "ref": deployment, "replicas": 2, "confirm": "leren"}); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("prod zonder TOTP: %d %+v", s, ae)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE users SET totp_enabled_at = now() WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	if s, _, ae := act(map[string]any{"action": "scale", "ref": deployment, "replicas": 2}); s != http.StatusConflict || ae.Code != "needs_confirmation" {
		t.Fatalf("prod zonder slug: %d %+v", s, ae)
	}
	if spec("Deployment", "demo", "web")["replicas"] != 5.0 {
		t.Fatal("geschaald zonder bevestiging")
	}
	if s, res, ae := act(map[string]any{"action": "scale", "ref": deployment, "replicas": 2, "confirm": "leren"}); s != http.StatusOK || !res.Changed {
		t.Fatalf("prod met slug: %d %+v %+v", s, res, ae)
	}
	if s := c.do("POST", base+"/drain", map[string]any{"node": "leren-worker-01"}, &ae); s != http.StatusConflict || ae.Code != "needs_confirmation" {
		t.Fatalf("leegmaken op prod: %d %+v", s, ae)
	}
}

// Leegmaken toont eerst wat er gebeurt, stopt bij pods die niet terugkomen
// tenzij je doorgaat, wacht op een PodDisruptionBudget, en laat
// DaemonSets staan.
func TestKubernetesDrain(t *testing.T) {
	e, c := adminClient(t)
	fake := kubeCluster(t)
	db := map[string]string{"app": "db"}
	fake.Add(
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "los", Node: "leren-worker-01"}),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "cache-1", Node: "leren-worker-01", OwnerKind: "ReplicaSet", OwnerName: "cache-5f", EmptyDir: true}),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "db-0", Node: "leren-server-01", OwnerKind: "StatefulSet", OwnerName: "db", Labels: db}),
		kubefake.PodDisruptionBudget("demo", "db", db, 1, 0),
	)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	var server node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000006", &addrs{}, func(_ *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
	})
	base := "/api/v1/kubernetes/clusters/" + cl.ID
	eventually(t, "overzicht", func() bool { return c.do("GET", base, nil, nil) == http.StatusOK })

	var plan drainPlan
	if s := c.do("POST", base+"/drain", map[string]any{"node": "leren-worker-01", "preview": true}, &plan); s != http.StatusOK ||
		plan.Cordoned || !slices.Equal(podNames(plan.Evict), []string{"demo/web-7c9-a", "kube-system/local-path-provisioner-5d9c7b6f4-x1q"}) ||
		!slices.Equal(podNames(plan.Skip), []string{"kube-system/svclb-traefik-2f0b8c41-bl"}) ||
		!slices.Equal(podNames(plan.Blocked), []string{"demo/cache-1", "demo/los"}) {
		t.Fatalf("plan: %d %+v", s, plan)
	}
	if fake.Get("v1", "Node", "", "leren-worker-01")["spec"].(map[string]any)["unschedulable"] != nil {
		t.Fatal("het plan zette een cordon")
	}
	var ae apiErr
	if s := c.do("POST", base+"/drain", map[string]any{"node": "leren-worker-01"}, &ae); s != http.StatusConflict || ae.Code != "needs_force" ||
		!strings.Contains(ae.Message, "demo/los heeft geen eigenaar") {
		t.Fatalf("zonder doorgaan: %d %+v", s, ae)
	}
	if s := c.do("POST", base+"/drain", map[string]any{"node": "leren-niet"}, &ae); s != http.StatusNotFound {
		t.Fatalf("onbekende node: %d %+v", s, ae)
	}

	// Met doorgaan gaan ook de pod zonder eigenaar en de cache weg.
	var j job
	if s := c.do("POST", base+"/drain", map[string]any{"node": "leren-worker-01", "force": true}, &j); s != http.StatusAccepted {
		t.Fatalf("leegmaken: %d %+v", s, j)
	}
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("taak: %+v", j)
	}
	for _, p := range []string{"web-7c9-a", "los", "cache-1"} {
		if fake.Get("v1", "Pod", "demo", p) != nil {
			t.Errorf("%s staat er nog", p)
		}
	}
	if fake.Get("v1", "Pod", "kube-system", "svclb-traefik-2f0b8c41-bl") == nil {
		t.Error("de pod van de DaemonSet is weg")
	}
	if fake.Get("v1", "Node", "", "leren-worker-01")["spec"].(map[string]any)["unschedulable"] != true {
		t.Error("geen cordon")
	}
	log := strings.Join(j.Steps[0].Log, "\n")
	for _, want := range []string{"leren-worker-01 staat op cordon", "demo/los uitgezet; heeft geen eigenaar en komt niet terug", "svclb-traefik-2f0b8c41-bl blijft",
		"leren-worker-01 is leeg: 4 pods uitgezet"} {
		if !strings.Contains(log, want) {
			t.Errorf("log mist %q:\n%s", want, log)
		}
	}
	auditHas(t, c, "kube.drained", "Node leren-worker-01 leeggemaakt in Kubernetes: 4 pods uitgezet, ook zonder eigenaar")

	// Een budget dat nog geen pod laat gaan: leegmaken wacht en probeert
	// opnieuw, tot het budget het toestaat.
	if s := c.do("POST", base+"/drain", map[string]any{"node": "leren-server-01"}, &j); s != http.StatusAccepted {
		t.Fatalf("server leegmaken: %d %+v", s, j)
	}
	eventually(t, "wachten op het budget", func() bool {
		c.do("GET", "/api/v1/jobs/"+j.ID, nil, &j)
		return len(j.Steps) > 0 && strings.Contains(strings.Join(j.Steps[0].Log, "\n"), "demo/db-0 mag nog niet weg")
	})
	if fake.Get("v1", "Pod", "demo", "db-0") == nil {
		t.Fatal("db-0 is weg ondanks het budget")
	}
	fake.Add(kubefake.PodDisruptionBudget("demo", "db", db, 1, 1))
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" || fake.Get("v1", "Pod", "demo", "db-0") != nil {
		t.Fatalf("na het budget: %+v", j)
	}

	// Blijft het budget dicht, dan stopt leegmaken na de time-out en zegt
	// het welke pod bleef.
	fake.Add(
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "db-1", Node: "leren-worker-02", OwnerKind: "StatefulSet", OwnerName: "db", Labels: db}),
		kubefake.PodDisruptionBudget("demo", "db", db, 1, 0),
	)
	if s := c.do("POST", base+"/drain", map[string]any{"node": "leren-worker-02"}, &j); s != http.StatusAccepted {
		t.Fatalf("worker-02 leegmaken: %d %+v", s, j)
	}
	if j = waitJob(t, c, j.ID); j.Status != "failed" || !strings.Contains(j.Error, "staan er nog pods: demo/db-1") {
		t.Fatalf("time-out: %+v", j)
	}
}

// Een herstart of onderhoud van een node van een Kubernetes-cluster maakt
// hem eerst leeg en geeft hem daarna weer pods; een cordon die iemand zelf
// zette, blijft.
func TestKubernetesNodeLifecycle(t *testing.T) {
	e, c := adminClient(t)
	fake := kubeCluster(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	var server, worker node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-worker-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.12"}, &worker)
	stopServer := startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000007", &addrs{}, func(_ *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
	})
	sim := &keepalivedSim{}
	w1 := sim.add("leren-worker-01", "10.0.40.12")
	startSimAgent(t, e, c, worker.ID, "a1000000000000000000000000000008", w1)
	base := "/api/v1/kubernetes/clusters/" + cl.ID
	eventually(t, "overzicht", func() bool { return c.do("GET", base, nil, nil) == http.StatusOK })
	eventually(t, "agent van de worker", func() bool {
		var n struct {
			Agent *struct {
				Commands bool `json:"commands"`
			} `json:"agent"`
		}
		c.do("GET", "/api/v1/nodes/"+worker.ID, nil, &n)
		return n.Agent != nil && n.Agent.Commands
	})
	nodeSpec := func() (unschedulable any, marked bool) {
		n := fake.Get("v1", "Node", "", "leren-worker-01")
		ann, _ := n["metadata"].(map[string]any)["annotations"].(map[string]any)
		_, marked = ann["clusterforge.io/onderhoud"]
		return n["spec"].(map[string]any)["unschedulable"], marked
	}
	run := func(body map[string]any) job {
		t.Helper()
		// Een node is pas terug na een nieuwe herstart: de uptime van de
		// vorige telt niet.
		if err := os.WriteFile(filepath.Join(w1.root, "proc/uptime"), []byte("86400.00 80000.00\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var j job
		var ae apiErr
		raw := json.RawMessage{}
		if s := c.do("POST", "/api/v1/nodes/"+worker.ID+"/actions", body, &raw); s != http.StatusAccepted {
			_ = json.Unmarshal(raw, &ae)
			t.Fatalf("%v: %d %+v", body, s, ae)
		}
		_ = json.Unmarshal(raw, &j)
		return waitJob(t, c, j.ID)
	}
	stepNames := func(j job) []string {
		var out []string
		for _, s := range j.Steps {
			out = append(out, s.Name)
		}
		return out
	}
	stepLog := func(j job, name string) string {
		for _, s := range j.Steps {
			if s.Name == name {
				return strings.Join(s.Log, "\n")
			}
		}
		return ""
	}

	// Herstarten: eerst leeg, na de herstart weer pods zodra hij Ready is.
	j := run(map[string]any{"action": "reboot"})
	want := []string{
		"Kubernetes: node leegmaken", "Keepalived uitzetten", "Node in onderhoud zetten", "Herstarten en wachten tot de node terug is",
		"Keepalived weer aanzetten", "Kubernetes: node weer pods geven", "Node weer actief maken",
	}
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), want) {
		t.Fatalf("reboot: %+v", j)
	}
	if fake.Get("v1", "Pod", "demo", "web-7c9-a") != nil || fake.Get("v1", "Pod", "kube-system", "svclb-traefik-2f0b8c41-bl") == nil {
		t.Fatal("de worker is niet leeggemaakt zoals het hoort")
	}
	if u, marked := nodeSpec(); u != nil || marked {
		t.Fatalf("na de herstart: %v %v", u, marked)
	}
	if log := stepLog(j, "Kubernetes: node weer pods geven"); !strings.Contains(log, "leren-worker-01 krijgt weer nieuwe pods") {
		t.Fatalf("log: %s", log)
	}
	var cordons int
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r, "PATCH /api/v1/nodes/leren-worker-01?") {
			cordons++
		}
	}
	if cordons != 2 {
		t.Fatalf("%d keer de node gewijzigd: %v", cordons, fake.Requests())
	}

	// Onderhoud laat hem leeg en op cordon; beëindigen geeft hem weer pods.
	if j = run(map[string]any{"action": "maintenance"}); j.Status != "succeeded" || j.Steps[0].Name != "Kubernetes: node leegmaken" {
		t.Fatalf("onderhoud: %+v", j)
	}
	if u, marked := nodeSpec(); u != true || !marked {
		t.Fatalf("in onderhoud: %v %v", u, marked)
	}
	if j = run(map[string]any{"action": "activate"}); j.Status != "succeeded" || !slices.Contains(stepNames(j), "Kubernetes: node weer pods geven") {
		t.Fatalf("onderhoud beëindigen: %+v", j)
	}
	if u, marked := nodeSpec(); u != nil || marked {
		t.Fatalf("na onderhoud: %v %v", u, marked)
	}

	// Een cordon die iemand zelf zette, blijft na de herstart.
	nodeRef := map[string]any{"group": "", "version": "v1", "resource": "nodes", "namespace": "", "name": "leren-worker-01"}
	if s := c.do("POST", base+"/actions", map[string]any{"action": "cordon", "ref": nodeRef}, nil); s != http.StatusOK {
		t.Fatalf("cordon: %d", s)
	}
	j = run(map[string]any{"action": "reboot"})
	if j.Status != "succeeded" || !strings.Contains(stepLog(j, "Kubernetes: node weer pods geven"), "blijft op cordon: die zette iemand zelf") {
		t.Fatalf("eigen cordon: %+v", j)
	}
	if u, _ := nodeSpec(); u != true {
		t.Fatal("de eigen cordon is weg")
	}
	c.do("POST", base+"/actions", map[string]any{"action": "uncordon", "ref": nodeRef}, nil)

	// Houdt een budget een pod tegen, dan stopt de herstart voor er iets
	// gebeurt, en krijgt de node weer pods.
	db := map[string]string{"app": "db"}
	fake.Add(
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "db-0", Node: "leren-worker-01", OwnerKind: "StatefulSet", OwnerName: "db", Labels: db}),
		kubefake.PodDisruptionBudget("demo", "db", db, 1, 0),
	)
	// De agent herstart pas na een pauze; wacht tot beide herstarts
	// hierboven echt uitgevoerd zijn, anders telt de laatste hieronder mee.
	eventually(t, "vorige herstarts uitgevoerd", func() bool {
		n := 0
		for _, c := range w1.called() {
			if strings.HasPrefix(c, "reboot ") {
				n++
			}
		}
		return n == 2
	})
	calls := len(w1.called())
	if j = run(map[string]any{"action": "reboot"}); j.Status != "failed" || len(j.Steps) != 1 ||
		!strings.Contains(stepLog(j, "Kubernetes: node leegmaken"), "terugdraaien: leren-worker-01 krijgt weer pods") {
		t.Fatalf("budget: %+v", j)
	}
	if u, marked := nodeSpec(); u != nil || marked || len(w1.called()) != calls {
		t.Fatalf("na het budget: %v %v %q", u, marked, w1.called()[calls:])
	}
	fake.Delete("policy/v1", "PodDisruptionBudget", "demo", "db")

	// Antwoordt Kubernetes niet, dan gaat de herstart toch door, zonder
	// leegmaken.
	stopServer()
	j = run(map[string]any{"action": "reboot"})
	if j.Status != "succeeded" || slices.Contains(stepNames(j), "Kubernetes: node weer pods geven") ||
		!strings.Contains(stepLog(j, "Kubernetes: node leegmaken"), "Kubernetes antwoordde niet") {
		t.Fatalf("zonder Kubernetes: %+v", j)
	}
	if fake.Get("v1", "Pod", "demo", "db-0") == nil {
		t.Fatal("db-0 is weg")
	}
}
