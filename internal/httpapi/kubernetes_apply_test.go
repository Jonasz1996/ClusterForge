package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type applyObject struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ref       *struct {
		Resource string `json:"resource"`
	} `json:"ref"`
	Action string `json:"action"`
	Diff   string `json:"diff"`
	Errors []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
	Conflicts []struct {
		Field   string `json:"field"`
		Manager string `json:"manager"`
	} `json:"conflicts"`
	Note string `json:"note"`
}

type applyResult struct {
	OK      bool          `json:"ok"`
	Changed int           `json:"changed"`
	Applied bool          `json:"applied"`
	Objects []applyObject `json:"objects"`
}

// actions zet de objecten op een rij als "Deployment demo/whoami create".
func (r applyResult) actions() []string {
	var out []string
	for _, o := range r.Objects {
		name := o.Name
		if o.Namespace != "" {
			name = o.Namespace + "/" + o.Name
		}
		out = append(out, o.Kind+" "+name+" "+o.Action)
	}
	return out
}

const whoami = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: whoami
  labels: {app: whoami}
spec:
  replicas: 1
  selector:
    matchLabels: {app: whoami}
  template:
    metadata:
      labels: {app: whoami}
    spec:
      containers:
        - name: whoami
          image: traefik/whoami:v1.11
          ports:
            - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: whoami
spec:
  selector: {app: whoami}
  ports:
    - port: 80
      targetPort: 80
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: whoami
spec:
  rules:
    - host: whoami.10.0.40.100.nip.io
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: whoami
                port: {number: 80}
`

// Toepassen controleert eerst met een dry-run en toont het verschil, past
// daarna toe als ClusterForge, en zegt de tweede keer dat er niets
// verandert. Fouten komen per veld terug, een conflict met kubectl noemt
// kubectl, en een Secret laat zijn waarden nergens zien.
func TestKubernetesApply(t *testing.T) {
	e, c := adminClient(t)
	fake := kubeCluster(t)
	ctx := context.Background()

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
	apply := func(body map[string]any) (int, applyResult, apiErr) {
		t.Helper()
		var res applyResult
		var ae apiErr
		raw := json.RawMessage{}
		s := c.do("POST", base+"/apply", body, &raw)
		if s == http.StatusOK {
			_ = json.Unmarshal(raw, &res)
		} else {
			_ = json.Unmarshal(raw, &ae)
		}
		return s, res, ae
	}
	want := func(what string, res applyResult, actions ...string) {
		t.Helper()
		if got := res.actions(); !slices.Equal(got, actions) {
			t.Fatalf("%s: %v, verwacht %v\n%+v", what, got, actions, res)
		}
	}

	// Controleren verandert niets en toont wat er bij komt.
	s, res, ae := apply(map[string]any{"yaml": whoami, "namespace": "demo", "preview": true})
	if s != http.StatusOK || !res.OK || res.Applied || res.Changed != 3 {
		t.Fatalf("controleren: %d %+v %+v", s, res, ae)
	}
	want("controleren", res, "Deployment demo/whoami create", "Service demo/whoami create", "Ingress demo/whoami create")
	if d := res.Objects[0].Diff; !strings.Contains(d, "+++ Deployment demo/whoami\n") || !strings.Contains(d, "+        - image: traefik/whoami:v1.11\n") ||
		strings.Contains(d, "resourceVersion") || strings.Contains(d, "uid:") {
		t.Fatalf("verschil:\n%s", d)
	}
	if fake.Get("apps/v1", "Deployment", "demo", "whoami") != nil {
		t.Fatal("controleren maakte de Deployment")
	}

	// Toepassen.
	s, res, ae = apply(map[string]any{"yaml": whoami, "namespace": "demo"})
	if s != http.StatusOK || !res.OK || !res.Applied || res.Changed != 3 {
		t.Fatalf("toepassen: %d %+v %+v", s, res, ae)
	}
	if fake.Get("networking.k8s.io/v1", "Ingress", "demo", "whoami") == nil || res.Objects[0].Ref == nil || res.Objects[0].Ref.Resource != "deployments" {
		t.Fatalf("niet toegepast: %+v", res)
	}
	auditHas(t, c, "kube.applied", "Toegepast in Kubernetes: nieuw Deployment demo/whoami, Service demo/whoami, Ingress demo/whoami")
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r, "PATCH ") && !strings.Contains(r, "fieldManager=clusterforge") {
			t.Errorf("zonder field manager: %s", r)
		}
	}

	// Een tweede keer: geen verschil.
	_, res, _ = apply(map[string]any{"yaml": whoami, "namespace": "demo", "preview": true})
	want("nog eens", res, "Deployment demo/whoami unchanged", "Service demo/whoami unchanged", "Ingress demo/whoami unchanged")
	if res.Changed != 0 || res.Objects[0].Diff != "" {
		t.Fatalf("nog eens: %+v", res)
	}

	// Een nieuwe image: alleen de Deployment verandert, met die regel.
	next := strings.Replace(whoami, "whoami:v1.11", "whoami:v1.10", 1)
	_, res, _ = apply(map[string]any{"yaml": next, "namespace": "demo", "preview": true})
	want("nieuwe image", res, "Deployment demo/whoami update", "Service demo/whoami unchanged", "Ingress demo/whoami unchanged")
	if d := res.Objects[0].Diff; !strings.Contains(d, "-        - image: traefik/whoami:v1.11\n+        - image: traefik/whoami:v1.10\n") {
		t.Fatalf("verschil image:\n%s", d)
	}

	// Een nieuwe namespace komt eerst, ook als hij later in de YAML staat.
	ns := `apiVersion: v1
kind: ConfigMap
metadata: {name: instellingen, namespace: winkel}
data: {LOG_LEVEL: info}
---
apiVersion: v1
kind: Namespace
metadata: {name: winkel}
`
	_, res, _ = apply(map[string]any{"yaml": ns, "preview": true})
	want("namespace", res, "Namespace winkel create", "ConfigMap winkel/instellingen create")
	if !res.OK || !strings.Contains(res.Objects[1].Note, "pas bij Toepassen") || !strings.Contains(res.Objects[1].Diff, "+  LOG_LEVEL: info\n") {
		t.Fatalf("namespace controleren: %+v", res)
	}
	_, res, _ = apply(map[string]any{"yaml": ns})
	if !res.OK || fake.Get("v1", "ConfigMap", "winkel", "instellingen") == nil {
		t.Fatalf("namespace toepassen: %+v", res)
	}

	// Fouten komen bij het veld.
	bad := `apiVersion: apps/v1
kind: Deployment
metadata: {name: kapot}
spec:
  selector: {matchLabels: {app: kapot}}
  template:
    metadata: {labels: {app: kapot}}
    spec:
      containers: [{name: kapot, ports: [{containerPort: 0}]}]
---
apiVersion: v1
kind: Service
metadata: {name: Hoofdletters}
spec:
  ports: [{port: 80}]
---
apiVersion: example.com/v1
kind: Taart
metadata: {name: appel}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: ergens, namespace: bestaat-niet}
`
	_, res, _ = apply(map[string]any{"yaml": bad, "namespace": "demo", "preview": true})
	want("fouten", res, "Deployment demo/kapot failed", "Service demo/Hoofdletters failed", "Taart appel failed", "ConfigMap bestaat-niet/ergens failed")
	if res.OK {
		t.Fatal("fouten maar ok")
	}
	fields := func(o applyObject) []string {
		var out []string
		for _, e := range o.Errors {
			out = append(out, e.Field+": "+e.Message)
		}
		return out
	}
	if f := fields(res.Objects[0]); !slices.Contains(f, "spec.template.spec.containers[0].image: Required value") ||
		!slices.ContainsFunc(f, func(s string) bool {
			return strings.HasPrefix(s, "spec.template.spec.containers[0].ports[0].containerPort: Invalid value: 0")
		}) {
		t.Fatalf("fouten Deployment: %v", f)
	}
	if f := fields(res.Objects[1]); len(f) != 1 || !strings.HasPrefix(f[0], "metadata.name: Invalid value") {
		t.Fatalf("fout naam: %v", f)
	}
	if f := fields(res.Objects[2]); len(f) != 1 || f[0] != "kind: het cluster kent de soort Taart (example.com/v1) niet" || res.Objects[2].Ref != nil {
		t.Fatalf("fout soort: %v %+v", f, res.Objects[2])
	}
	if f := fields(res.Objects[3]); len(f) != 1 || !strings.HasPrefix(f[0], "metadata.namespace: namespace bestaat-niet bestaat niet") {
		t.Fatalf("fout namespace: %v", f)
	}

	// Echt toepassen stopt bij het eerste object dat faalt.
	half := `apiVersion: v1
kind: ConfigMap
metadata: {name: eerst}
data: {a: b}
---
` + strings.SplitN(bad, "---", 2)[0] + `---
apiVersion: v1
kind: Service
metadata: {name: daarna}
spec:
  ports: [{port: 80}]
`
	_, res, _ = apply(map[string]any{"yaml": half, "namespace": "demo"})
	want("half", res, "ConfigMap demo/eerst create", "Deployment demo/kapot failed", "Service demo/daarna skipped")
	if fake.Get("v1", "ConfigMap", "demo", "eerst") == nil || fake.Get("v1", "Service", "demo", "daarna") != nil {
		t.Fatal("half toegepast klopt niet")
	}

	// Fouten in de YAML zelf.
	for _, y := range []string{"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n data: {}\n", "kind: ConfigMap\nmetadata: {name: x}\n", "apiVersion: v1\nkind: Pod\nmetadata: {generateName: x-}\n", "# leeg\n"} {
		if s, _, ae := apply(map[string]any{"yaml": y, "preview": true}); s != http.StatusBadRequest || ae.Code != "yaml" {
			t.Errorf("%q: %d %+v", y, s, ae)
		}
	}
	if _, _, ae := apply(map[string]any{"yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n data: {}\n", "preview": true}); !strings.HasPrefix(ae.Message, "regel 4: ") {
		t.Errorf("regel: %+v", ae)
	}

	// Een veld dat kubectl beheert: een conflict, en met overnemen gaat het.
	fake.Own("apps/v1", "Deployment", "demo", "whoami", "kubectl-client-side-apply", ".spec.replicas")
	scaled := strings.Replace(whoami, "replicas: 1", "replicas: 2", 1)
	_, res, _ = apply(map[string]any{"yaml": scaled, "namespace": "demo", "preview": true})
	if o := res.Objects[0]; o.Action != "failed" || len(o.Conflicts) != 1 || o.Conflicts[0].Field != "spec.replicas" || o.Conflicts[0].Manager != "kubectl-client-side-apply" {
		t.Fatalf("conflict: %+v", res.Objects[0])
	}
	_, res, _ = apply(map[string]any{"yaml": scaled, "namespace": "demo", "force": true})
	if !res.OK || res.Objects[0].Action != "update" || fake.Get("apps/v1", "Deployment", "demo", "whoami")["spec"].(map[string]any)["replicas"] != 2.0 {
		t.Fatalf("overnemen: %+v", res)
	}
	auditHas(t, c, "kube.applied", "Toegepast in Kubernetes: anders Deployment demo/whoami, velden van een ander programma overgenomen")

	// Een Secret: de waarde gaat naar de API, maar komt nergens terug.
	secret := func(pw string) string {
		return "apiVersion: v1\nkind: Secret\nmetadata: {name: login}\nstringData: {wachtwoord: " + pw + "}\n"
	}
	_, res, _ = apply(map[string]any{"yaml": secret("zeer-geheim-1"), "namespace": "demo"})
	if !res.OK || res.Objects[0].Action != "create" || strings.Contains(res.Objects[0].Diff, "zeer-geheim") || !strings.Contains(res.Objects[0].Diff, "(verborgen, 13 bytes)") {
		t.Fatalf("secret: %+v", res)
	}
	_, res, _ = apply(map[string]any{"yaml": secret("zeer-geheim-2"), "namespace": "demo", "preview": true})
	if o := res.Objects[0]; o.Action != "update" || o.Diff != "" || !strings.Contains(o.Note, "waarden") {
		t.Fatalf("secret anders: %+v", o)
	}
	_, res, _ = apply(map[string]any{"yaml": secret("zeer-geheim-1"), "namespace": "demo", "preview": true})
	if res.Objects[0].Action != "unchanged" {
		t.Fatalf("secret hetzelfde: %+v", res.Objects[0])
	}
	var a auditItems
	c.do("GET", "/api/v1/audit?category=kubernetes", nil, &a)
	if raw, _ := json.Marshal(a); strings.Contains(string(raw), "zeer-geheim") {
		t.Fatal("secretwaarde in het logboek")
	}

	// Een Helm-chart is een object voor de Helm-controller van k3s.
	chart := `apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata: {name: uptime-kuma, namespace: kube-system}
spec:
  repo: https://helm.irsanp.com
  chart: uptime-kuma
  version: 2.21.2
  targetNamespace: monitoring
  createNamespace: true
`
	if _, res, _ = apply(map[string]any{"yaml": chart}); !res.OK || fake.Get("helm.cattle.io/v1", "HelmChart", "kube-system", "uptime-kuma") == nil {
		t.Fatalf("helm: %+v", res)
	}
	// Fouten in de values komen er bij Controleren uit, niet pas in de
	// logs van de installatie.
	badValues := chart + "  valuesContent: |-\n    service:\n      type: [ClusterIP\n"
	if _, res, _ = apply(map[string]any{"yaml": badValues, "preview": true}); res.OK || res.Objects[0].Errors[0].Field != "spec.valuesContent" ||
		!strings.HasPrefix(res.Objects[0].Errors[0].Message, "regel ") {
		t.Fatalf("helm values: %+v", res)
	}

	// Een viewer controleert of past niets toe.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("POST", base+"/apply", map[string]any{"yaml": whoami, "preview": true}, &ae); s != http.StatusForbidden {
		t.Fatalf("viewer: %d %+v", s, ae)
	}

	// Op prod: controleren mag, toepassen vraagt TOTP en de slug.
	if _, err := e.pool.Exec(ctx, "UPDATE clusters SET environment = 'prod' WHERE id = $1", cl.ID); err != nil {
		t.Fatal(err)
	}
	if s, _, ae := apply(map[string]any{"yaml": next, "namespace": "demo", "preview": true}); s != http.StatusOK {
		t.Fatalf("prod controleren: %d %+v", s, ae)
	}
	if s, _, ae := apply(map[string]any{"yaml": next, "namespace": "demo", "confirm": "leren"}); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("prod zonder TOTP: %d %+v", s, ae)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE users SET totp_enabled_at = now() WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	if s, _, ae := apply(map[string]any{"yaml": next, "namespace": "demo"}); s != http.StatusConflict || ae.Code != "needs_confirmation" {
		t.Fatalf("prod zonder slug: %d %+v", s, ae)
	}
	if s, res, ae := apply(map[string]any{"yaml": next, "namespace": "demo", "confirm": "leren"}); s != http.StatusOK || res.Changed != 1 {
		t.Fatalf("prod met slug: %d %+v %+v", s, res, ae)
	}
}
