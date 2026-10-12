package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type kubeLessonStep struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`
	State    string   `json:"state"`
	Text     []string `json:"text"`
	Web      string   `json:"web"`
	Kubectl  []string `json:"kubectl"`
	Yaml     string   `json:"yaml"`
	Question string   `json:"question"`
	Detail   string   `json:"detail"`
	Answer   string   `json:"answer"`
	Wrong    bool     `json:"wrong"`
	Link     *struct {
		Label     string `json:"label"`
		Tab       string `json:"tab"`
		Namespace string `json:"namespace"`
		Ref       *struct {
			Resource string `json:"resource"`
			Name     string `json:"name"`
		} `json:"ref"`
		URL string `json:"url"`
	} `json:"link"`
}

type kubeLesson struct {
	ID          string           `json:"id"`
	Intro       []string         `json:"intro"`
	Outro       []string         `json:"outro"`
	Steps       []kubeLessonStep `json:"steps"`
	Done        int              `json:"done"`
	Unavailable string           `json:"unavailable"`
	StartedAt   *time.Time       `json:"started_at"`
	FinishedAt  *time.Time       `json:"finished_at"`
	Problem     string           `json:"problem"`
}

// states geeft per opdracht de toestand, zoals "nodes:done".
func (l kubeLesson) states() []string {
	out := make([]string, 0, len(l.Steps))
	for _, s := range l.Steps {
		out = append(out, s.ID+":"+s.State)
	}
	return out
}

// current is de opdracht die aan de beurt is.
func (l kubeLesson) current() kubeLessonStep {
	for _, s := range l.Steps {
		if s.State == "current" {
			return s
		}
	}
	return kubeLessonStep{}
}

// webDeployment is web in leren, met de annotatie van de versie.
func webDeployment(image string, replicas, ready, generation, observed int, revision string) map[string]any {
	d := kubefake.Deployment(kubefake.WorkloadSpec{
		Namespace: "leren", Name: "web", Image: image, Replicas: replicas, Ready: ready, Generation: generation, ObservedGeneration: observed,
	})
	if revision != "" {
		d["metadata"].(map[string]any)["annotations"] = map[string]any{"deployment.kubernetes.io/revision": revision}
	}
	return d
}

func webPod(name, node string) map[string]any {
	return kubefake.Pod(kubefake.PodSpec{
		Namespace: "leren", Name: name, Node: node, Labels: map[string]string{"app": "web"}, OwnerKind: "ReplicaSet", OwnerName: "web-5d9c",
	})
}

// De lessen 1 tot en met 8 zijn te volgen: elke opdracht wordt op het
// cluster gecontroleerd via de agent, de voortgang blijft per gebruiker
// bewaard, en een les kan opnieuw beginnen.
func TestKubernetesLessons(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	// Elke verandering in de nep-API zet de klok verder, zodat de cache van
	// twee seconden niet in de weg zit.
	var shift atomic.Int64
	e.kube.Now = func() time.Time { return time.Now().Add(time.Duration(shift.Load())) }
	changed := func() { shift.Add(int64(3 * time.Second)) }

	fake := kubefake.New()
	fake.Start()
	t.Cleanup(fake.Close)
	const server, worker = "leren-server-01", "leren-worker-01"
	fake.SeedK3s(
		kubefake.NodeSpec{Name: server, IP: "10.0.40.11", Server: true},
		kubefake.NodeSpec{Name: worker, IP: "10.0.40.12"},
	)

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.40.100"}, nil)
	var srv node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": server, "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &srv)
	startAgentWith(t, e, c, srv.ID, "a1000000000000000000000000000006", &addrs{}, func(_ *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
	})
	base := "/api/v1/kubernetes/clusters/" + cl.ID
	eventually(t, "overzicht", func() bool { return c.do("GET", base, nil, nil) == http.StatusOK })
	if err := e.kube.Sync(ctx, uuid.MustParse(cl.ID)); err != nil {
		t.Fatal(err)
	}

	type summary struct {
		ID          string     `json:"id"`
		Number      int        `json:"number"`
		Steps       int        `json:"steps"`
		Done        int        `json:"done"`
		Unavailable string     `json:"unavailable"`
		FinishedAt  *time.Time `json:"finished_at"`
	}
	list := func(cc *client) map[string]summary {
		t.Helper()
		var res struct{ Items []summary }
		if s := cc.do("GET", base+"/lessons", nil, &res); s != http.StatusOK {
			t.Fatalf("lessen: %d", s)
		}
		out := map[string]summary{}
		for _, it := range res.Items {
			out[it.ID] = it
		}
		return out
	}
	sums := list(c)
	if len(sums) != 10 || sums["verkennen"].Number != 1 || sums["verkennen"].Steps != 4 || sums["onderhoud"].Unavailable != "" ||
		sums["node-erbij"].Unavailable != "" || !strings.Contains(sums["server-valt-uit"].Unavailable, "minstens 3 servers nodig; dit cluster heeft er 1") {
		t.Fatalf("lessen: %+v", sums)
	}

	check := func(lesson string, answers map[string]string) kubeLesson {
		t.Helper()
		var l kubeLesson
		if s := c.do("POST", base+"/lessons/"+lesson+"/check", map[string]any{"answers": answers}, &l); s != http.StatusOK {
			t.Fatalf("%s controleren: %d", lesson, s)
		}
		return l
	}
	want := func(l kubeLesson, states ...string) {
		t.Helper()
		if !slices.Equal(l.states(), states) {
			t.Fatalf("%s: %v, verwacht %v; %+v", l.ID, l.states(), states, l.current())
		}
	}
	detail := func(l kubeLesson, sub string) {
		t.Helper()
		if !strings.Contains(l.current().Detail, sub) {
			t.Fatalf("%s %s: %q bevat geen %q", l.ID, l.current().ID, l.current().Detail, sub)
		}
	}

	// Les 1: vragen over wat er staat, met fouten en hints.
	l := check("verkennen", nil)
	want(l, "nodes:current", "server:locked", "dns:locked", "traefik:locked")
	if len(l.Intro) == 0 || l.current().Question == "" || l.current().Kubectl[0] != "sudo kubectl get nodes" || l.current().Link.Tab != "nodes" {
		t.Fatalf("les 1: %+v", l)
	}
	l = check("verkennen", map[string]string{"nodes": "3"})
	if !l.current().Wrong || !strings.Contains(l.current().Detail, "klopt niet") {
		t.Fatalf("fout antwoord: %+v", l.current())
	}
	l = check("verkennen", map[string]string{"nodes": " Twee. "})
	want(l, "nodes:done", "server:current", "dns:locked", "traefik:locked")
	if l.Steps[0].Answer != "Twee." || !strings.Contains(l.Steps[0].Detail, "2 nodes, waarvan 1 server") {
		t.Fatalf("goed antwoord: %+v", l.Steps[0])
	}
	l = check("verkennen", map[string]string{"server": worker})
	detail(l, "control-plane")
	l = check("verkennen", map[string]string{"server": strings.ToUpper(server)})
	l = check("verkennen", map[string]string{"dns": "kube-system"})
	// Traefik staat in de nep-API op de tweede node, niet waar svclb staat.
	l = check("verkennen", map[string]string{"traefik": server})
	detail(l, "klopt niet")
	l = check("verkennen", map[string]string{"traefik": worker})
	want(l, "nodes:done", "server:done", "dns:done", "traefik:done")
	if l.Done != 4 || l.FinishedAt == nil || len(l.Outro) == 0 {
		t.Fatalf("les 1 af: %+v", l)
	}

	// Les 2: de namespace, de Deployment, en de pod die ontstaat.
	l = check("eerste-app", nil)
	want(l, "namespace:current", "deployment:locked", "pod:locked")
	detail(l, "nog geen namespace")
	if !strings.Contains(l.current().Yaml, "kind: Namespace") {
		t.Fatalf("yaml: %q", l.current().Yaml)
	}
	fake.Add(kubefake.Namespace("leren"))
	changed()
	l = check("eerste-app", nil)
	want(l, "namespace:done", "deployment:current", "pod:locked")
	if !strings.Contains(l.current().Yaml, "image: nginx:1.28") || l.current().Kubectl[0] != "sudo kubectl create deployment web --image=nginx:1.28 -n leren" {
		t.Fatalf("deployment: %+v", l.current())
	}
	fake.Add(
		webDeployment("nginx:1.28", 1, 0, 1, 1, "1"),
		kubefake.Pod(kubefake.PodSpec{
			Namespace: "leren", Name: "web-5d9c-a", Node: worker, Waiting: "ImagePullBackOff", Labels: map[string]string{"app": "web"},
			OwnerKind: "ReplicaSet", OwnerName: "web-5d9c",
		}),
	)
	changed()
	l = check("eerste-app", nil)
	want(l, "namespace:done", "deployment:done", "pod:current")
	detail(l, "web-5d9c-a: ImagePullBackOff")
	detail(l, "internet")
	fake.Add(webDeployment("nginx:1.28", 1, 1, 1, 1, "1"), webPod("web-5d9c-a", worker))
	changed()
	l = check("eerste-app", nil)
	want(l, "namespace:done", "deployment:done", "pod:done")
	if l.Steps[2].Detail != "web-5d9c-a draait op leren-worker-01." {
		t.Fatalf("pod: %q", l.Steps[2].Detail)
	}

	// Les 3: schalen, de spreiding, en een pod die terugkomt.
	l = check("schalen", nil)
	detail(l, "web staat op 1 pod; zet hem op 3.")
	fake.Add(webDeployment("nginx:1.28", 3, 2, 2, 2, "1"), webPod("web-5d9c-b", server), webPod("web-5d9c-c", worker))
	changed()
	l = check("schalen", nil)
	detail(l, "2 van 3 pods klaar")
	fake.Add(webDeployment("nginx:1.28", 3, 3, 2, 2, "1"))
	changed()
	l = check("schalen", map[string]string{"spreiding": "2"})
	want(l, "drie:done", "spreiding:done", "zelfherstel:current")
	cur := l.current()
	if !slices.Contains(cur.Kubectl, "sudo kubectl delete pod web-5d9c-a -n leren") || !strings.Contains(cur.Text[0], "`web-5d9c-a`") ||
		cur.Link.Ref == nil || cur.Link.Ref.Name != "web-5d9c-a" {
		t.Fatalf("zelfherstel: %+v", cur)
	}
	detail(l, "Alle pods van daarnet draaien nog")
	fake.Delete("v1", "Pod", "leren", "web-5d9c-a")
	fake.Add(webDeployment("nginx:1.28", 3, 2, 2, 2, "1"))
	changed()
	l = check("schalen", nil)
	detail(l, "web-5d9c-a is weg; wachten op de nieuwe: 2 van 3 klaar.")
	fake.Add(webDeployment("nginx:1.28", 3, 3, 2, 2, "1"), webPod("web-5d9c-d", server))
	changed()
	l = check("schalen", nil)
	want(l, "drie:done", "spreiding:done", "zelfherstel:done")
	if l.Steps[2].Detail != "web-5d9c-a is weg, en web-5d9c-d kwam ervoor in de plaats." {
		t.Fatalf("zelfherstel: %q", l.Steps[2].Detail)
	}

	// Les 4: een Service, een Ingress onder nip.io, en de app openen.
	l = check("bereikbaar", nil)
	detail(l, "nog geen Service")
	fake.Add(kubefake.Service(kubefake.ServiceSpec{Namespace: "leren", Name: "andere", Selector: map[string]string{"app": "db"}, Ports: []kubefake.ServicePort{{Port: 80}}}))
	changed()
	l = check("bereikbaar", nil)
	detail(l, "De Service andere kiest de pods van web niet")
	fake.Add(kubefake.Service(kubefake.ServiceSpec{Namespace: "leren", Name: "web", Selector: map[string]string{"app": "web"}, Ports: []kubefake.ServicePort{{Port: 80}}}))
	changed()
	l = check("bereikbaar", nil)
	want(l, "service:done", "ingress:current", "openen:locked")
	if !strings.Contains(l.current().Yaml, "host: web.10.0.40.100.nip.io") || !strings.Contains(l.current().Yaml, "name: web\n") ||
		!strings.Contains(l.current().Kubectl[0], "--rule='web.10.0.40.100.nip.io/*=web:80'") {
		t.Fatalf("ingress: %+v", l.current())
	}
	fake.Add(kubefake.Ingress(kubefake.IngressSpec{Namespace: "leren", Name: "web", Host: "web.10.0.40.100.nip.io", Service: "web", Port: 80}))
	changed()
	l = check("bereikbaar", nil)
	want(l, "service:done", "ingress:done", "openen:current")
	if l.current().Link.URL != "http://web.10.0.40.100.nip.io/" || !strings.Contains(l.current().Text[0], "http://web.10.0.40.100.nip.io/") {
		t.Fatalf("openen: %+v", l.current())
	}
	l = check("bereikbaar", map[string]string{"openen": "ok"})
	want(l, "service:done", "ingress:done", "openen:done")

	// Les 5: een nieuwe versie, het versienummer, en terugdraaien.
	l = check("nieuwe-versie", nil)
	detail(l, "web draait nog nginx:1.28.")
	if !strings.Contains(l.current().Text[0], "`nginx:1.28`") || !strings.Contains(l.current().Yaml, "replicas: 3") {
		t.Fatalf("bijwerken: %+v", l.current())
	}
	fake.Add(webDeployment("nginx:1.29", 3, 3, 3, 2, "2"))
	changed()
	l = check("nieuwe-versie", nil)
	detail(l, "De uitrol naar nginx:1.29 loopt: de wijziging is nog niet opgepikt.")
	fake.Add(webDeployment("nginx:1.29", 3, 3, 3, 3, "2"))
	changed()
	l = check("nieuwe-versie", map[string]string{"versie": "1"})
	detail(l, "klopt niet")
	l = check("nieuwe-versie", map[string]string{"versie": "2"})
	want(l, "bijwerken:done", "versie:done", "terugdraaien:current")
	detail(l, "web draait nginx:1.29; terug naar nginx:1.28.")
	fake.Add(webDeployment("nginx:1.28", 3, 3, 4, 4, "3"))
	changed()
	l = check("nieuwe-versie", nil)
	want(l, "bijwerken:done", "versie:done", "terugdraaien:done")
	if l.Steps[2].Detail != "web draait weer nginx:1.28, nu als versie 3." {
		t.Fatalf("terugdraaien: %q", l.Steps[2].Detail)
	}

	// Les 6: een ConfigMap, een Secret, en web die ze leest: met envFrom
	// of per sleutel, zoals kubectl set env doet.
	l = check("configuratie", nil)
	detail(l, "nog geen ConfigMap instellingen")
	fake.Add(kubefake.ConfigMap("leren", "instellingen", map[string]string{"TAAL": "nl", "KLEUR": "blauw"}))
	changed()
	l = check("configuratie", nil)
	want(l, "configmap:done", "secret:current", "gebruiken:locked")
	if l.Steps[0].Detail != "instellingen heeft KLEUR en TAAL." {
		t.Fatalf("configmap: %q", l.Steps[0].Detail)
	}
	fake.Add(kubefake.Secret("leren", "geheim", "Opaque", map[string]string{"WACHTWOORD": "verander-mij"}))
	changed()
	l = check("configuratie", nil)
	want(l, "configmap:done", "secret:done", "gebruiken:current")
	detail(l, "web leest de ConfigMap instellingen en het Secret geheim nog niet.")
	if strings.Contains(l.Steps[1].Detail, "verander-mij") {
		t.Fatalf("secretwaarde in de les: %q", l.Steps[1].Detail)
	}
	d := webDeployment("nginx:1.28", 3, 3, 5, 5, "4")
	ctr := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	ctr["envFrom"] = []any{map[string]any{"configMapRef": map[string]any{"name": "instellingen"}}}
	ctr["env"] = []any{map[string]any{"name": "WACHTWOORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "geheim", "key": "WACHTWOORD"}}}}
	d["status"].(map[string]any)["replicas"] = 4
	fake.Add(d)
	changed()
	l = check("configuratie", nil)
	detail(l, "de oude pods stoppen nog: 4 pods voor 3")
	d["status"].(map[string]any)["replicas"] = 3
	fake.Add(d)
	changed()
	l = check("configuratie", nil)
	want(l, "configmap:done", "secret:done", "gebruiken:done")

	// Les 7: een claim, een app die schrijft, herstarten, en de node van
	// het volume.
	l = check("opslag", nil)
	detail(l, "nog geen claim data")
	fake.Add(kubefake.Volume(kubefake.VolumeSpec{Namespace: "leren", Claim: "data", Size: "1Gi", Pending: true})...)
	changed()
	l = check("opslag", nil)
	want(l, "claim:done", "notities:current", "herstarten:locked", "node:locked")
	if l.Steps[0].Detail != "De claim data bestaat en is Pending." {
		t.Fatalf("claim: %q", l.Steps[0].Detail)
	}
	notities := func(name string) map[string]any {
		return kubefake.Pod(kubefake.PodSpec{
			Namespace: "leren", Name: name, Node: worker, Image: "busybox:1.37", Labels: map[string]string{"app": "notities"},
			OwnerKind: "ReplicaSet", OwnerName: "notities-7f8", Claim: "data",
		})
	}
	fake.Add(kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "leren", Name: "notities", Image: "busybox:1.37", Replicas: 1, Ready: 1}), notities("notities-7f8-a"))
	changed()
	l = check("opslag", nil)
	detail(l, "De claim data is Pending.")
	fake.Add(kubefake.Volume(kubefake.VolumeSpec{Namespace: "leren", Claim: "data", Volume: "pvc-3f1c", Size: "1Gi", Node: worker})...)
	changed()
	l = check("opslag", nil)
	want(l, "claim:done", "notities:done", "herstarten:current", "node:locked")
	detail(l, "notities draait nog met dezelfde pod.")
	fake.Delete("v1", "Pod", "leren", "notities-7f8-a")
	fake.Add(notities("notities-7f8-b"))
	fake.SetLog("leren", "notities-7f8-b", "main", false, "Sat Oct 11 20:00:00 UTC 2026")
	changed()
	l = check("opslag", nil)
	detail(l, "ziet maar één start")
	fake.SetLog("leren", "notities-7f8-b", "main", false, "Sat Oct 11 20:00:00 UTC 2026", "Sat Oct 11 20:05:00 UTC 2026")
	changed()
	l = check("opslag", map[string]string{"node": worker})
	want(l, "claim:done", "notities:done", "herstarten:done", "node:done")
	if !strings.Contains(l.Steps[2].Detail, "20:00:00 UTC 2026; Sat Oct 11 20:05:00") || !strings.Contains(l.Steps[3].Detail, "/var/lib/rancher/k3s/storage/") {
		t.Fatalf("opslag: %+v", l.Steps)
	}

	// Les 8: cordon op een node met pods van web, leegmaken, en terug.
	l = check("onderhoud", nil)
	want(l, "cordon:current", "leegmaken:locked", "terugzetten:locked")
	detail(l, "nog geen node op cordon")
	if !slices.Contains(l.current().Kubectl, "sudo kubectl cordon "+server) {
		t.Fatalf("cordon: %+v", l.current())
	}
	fake.Add(kubefake.Node(kubefake.NodeSpec{Name: worker, IP: "10.0.40.12", Unschedulable: true}))
	changed()
	l = check("onderhoud", nil)
	want(l, "cordon:done", "leegmaken:current", "terugzetten:locked")
	if l.current().Title != "Maak leren-worker-01 leeg" || l.current().Kubectl[0] != "sudo kubectl drain leren-worker-01 --ignore-daemonsets --delete-emptydir-data" {
		t.Fatalf("leegmaken: %+v", l.current())
	}
	detail(l, "Er draait nog 1 pod van web op leren-worker-01.")
	fake.Delete("v1", "Pod", "leren", "web-5d9c-c")
	fake.Add(webPod("web-5d9c-e", server))
	changed()
	l = check("onderhoud", nil)
	want(l, "cordon:done", "leegmaken:done", "terugzetten:current")
	fake.Add(kubefake.Node(kubefake.NodeSpec{Name: worker, IP: "10.0.40.12"}))
	changed()
	l = check("onderhoud", nil)
	want(l, "cordon:done", "leegmaken:done", "terugzetten:done")

	sums = list(c)
	for _, id := range []string{"verkennen", "eerste-app", "schalen", "bereikbaar", "nieuwe-versie", "configuratie", "opslag", "onderhoud"} {
		if sums[id].Done != sums[id].Steps || sums[id].FinishedAt == nil {
			t.Fatalf("%s niet af: %+v", id, sums[id])
		}
	}

	// Opnieuw beginnen haalt alleen de voortgang weg.
	if s := c.do("DELETE", base+"/lessons/onderhoud", nil, nil); s != http.StatusNoContent {
		t.Fatalf("opnieuw: %d", s)
	}
	l = check("onderhoud", nil)
	want(l, "cordon:current", "leegmaken:locked", "terugzetten:locked")
	if fake.Get("apps/v1", "Deployment", "leren", "web") == nil {
		t.Fatal("web is weg")
	}

	// Een onbekende les, en geen Kubernetes.
	if s := c.do("POST", base+"/lessons/bestaat-niet/check", map[string]any{}, nil); s != http.StatusNotFound {
		t.Fatalf("onbekende les: %d", s)
	}
	var other cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &other)
	if s := c.do("GET", "/api/v1/kubernetes/clusters/"+other.ID+"/lessons", nil, nil); s != http.StatusNotFound {
		t.Fatalf("geen Kubernetes: %d", s)
	}

	// Een viewer volgt de lessen ook, met zijn eigen voortgang.
	e.createUser("viewer", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("viewer", "een-lang-wachtwoord", "")
	if vs := list(v); vs["verkennen"].Done != 0 || vs["onderhoud"].Done != 0 {
		t.Fatalf("viewer: %+v", vs)
	}
	var vl kubeLesson
	if s := v.do("POST", base+"/lessons/verkennen/check", map[string]any{"answers": map[string]string{"nodes": "2"}}, &vl); s != http.StatusOK || vl.Done != 1 {
		t.Fatalf("viewer controleren: %d %+v", s, vl)
	}
	if s := v.do("DELETE", base+"/lessons/verkennen", nil, nil); s != http.StatusNoContent {
		t.Fatalf("viewer opnieuw: %d", s)
	}
	if list(c)["verkennen"].Done != 4 {
		t.Fatal("de viewer haalde de voortgang van de admin weg")
	}

	// Les 9: een node erbij, en pods die erop komen.
	l = check("node-erbij", nil)
	want(l, "toevoegen:current", "pods:locked", "weghalen:locked")
	detail(l, "Er is nog geen node bij gekomen; je cluster heeft er 2.")
	const extra = "leren-worker-02"
	fake.Add(kubefake.Node(kubefake.NodeSpec{Name: extra, IP: "10.0.40.13", NotReady: true}))
	changed()
	l = check("node-erbij", nil)
	detail(l, extra+" is erbij, maar nog niet Ready")
	fake.Add(kubefake.Node(kubefake.NodeSpec{Name: extra, IP: "10.0.40.13"}))
	changed()
	l = check("node-erbij", nil)
	want(l, "toevoegen:done", "pods:current", "weghalen:locked")
	if l.Steps[0].Detail != extra+" is erbij en Ready. Je cluster heeft nu 3 nodes." || l.current().Title != "Zet pods op "+extra {
		t.Fatalf("les 9: %+v", l.Steps[:2])
	}
	detail(l, "Nog geen pod van web op "+extra)
	fake.Add(webPod("web-5d9c-f", extra))
	changed()
	l = check("node-erbij", nil)
	want(l, "toevoegen:done", "pods:done", "weghalen:current")
	if !strings.HasPrefix(l.Steps[1].Detail, "1 pod van web draait op "+extra+".") ||
		!slices.Equal(l.current().Kubectl, []string{"sudo kubectl drain " + extra + " --ignore-daemonsets --delete-emptydir-data", "sudo kubectl delete node " + extra}) {
		t.Fatalf("les 9: %+v", l.Steps[1:])
	}
	l = check("node-erbij", map[string]string{"weghalen": "Gelezen"})
	want(l, "toevoegen:done", "pods:done", "weghalen:done")

	// Les 10 kan pas met drie servers: de meerderheid, een server uit, en
	// weer aan.
	server2, server3 := "leren-server-02", "leren-server-03"
	fake.Add(kubefake.Node(kubefake.NodeSpec{Name: server2, IP: "10.0.40.14", Server: true}), kubefake.Node(kubefake.NodeSpec{Name: server3, IP: "10.0.40.15", Server: true}))
	changed()
	if err := e.kube.Sync(ctx, uuid.MustParse(cl.ID)); err != nil {
		t.Fatal(err)
	}
	if u := list(c)["server-valt-uit"].Unavailable; u != "" {
		t.Fatalf("les 10 met drie servers: %q", u)
	}
	l = check("server-valt-uit", map[string]string{"meerderheid": "2"})
	want(l, "meerderheid:current", "uitzetten:locked", "api:locked", "aanzetten:locked")
	detail(l, "klopt niet")
	l = check("server-valt-uit", map[string]string{"meerderheid": "één"})
	want(l, "meerderheid:done", "uitzetten:current", "api:locked", "aanzetten:locked")
	if l.Steps[0].Detail != "Juist: met 3 servers is de meerderheid 2, dus er mag er 1 uitvallen." ||
		!strings.Contains(l.current().Text[0], "bijvoorbeeld `"+server+"`") || !strings.Contains(l.current().Text[1], "Stond het VIP 10.0.40.100 op") {
		t.Fatalf("les 10: %+v", l.Steps[:2])
	}
	detail(l, "Alle servers zijn nog Ready.")
	fake.Add(kubefake.Node(kubefake.NodeSpec{Name: server2, IP: "10.0.40.14", Server: true, NotReady: true}))
	changed()
	l = check("server-valt-uit", map[string]string{"api": "3"})
	want(l, "meerderheid:done", "uitzetten:done", "api:current", "aanzetten:locked")
	detail(l, "klopt niet")
	if l.Steps[1].Detail != server2+" is NotReady, en de API antwoordt nog: 2 van de 3 servers draaien." || !strings.Contains(l.current().Text[1], "De pods op "+server2) {
		t.Fatalf("les 10: %+v", l.Steps[1:3])
	}
	l = check("server-valt-uit", map[string]string{"api": "twee"})
	want(l, "meerderheid:done", "uitzetten:done", "api:done", "aanzetten:current")
	detail(l, server2+" is nog NotReady.")
	if l.current().Title != "Zet "+server2+" weer aan" {
		t.Fatalf("aanzetten: %+v", l.current())
	}
	fake.Add(kubefake.Node(kubefake.NodeSpec{Name: server2, IP: "10.0.40.14", Server: true}))
	changed()
	l = check("server-valt-uit", nil)
	want(l, "meerderheid:done", "uitzetten:done", "api:done", "aanzetten:done")
	if l.FinishedAt == nil || l.Steps[3].Detail != server2+" is weer Ready." {
		t.Fatalf("les 10 af: %+v", l)
	}

	// Zonder agent kan het cluster niet gelezen worden: de les blijft
	// staan, met het probleem erbij. Met één node kan les 8 niet.
	var lone cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "los", "name": "Los", "type": "kubernetes", "environment": "lab"}, &lone)
	lb := "/api/v1/kubernetes/clusters/" + lone.ID
	if s := c.do("POST", lb+"/lessons/eerste-app/check", map[string]any{}, &l); s != http.StatusOK || l.Problem == "" ||
		!slices.Equal(l.states(), []string{"namespace:current", "deployment:locked", "pod:locked"}) {
		t.Fatalf("zonder agent: %d %+v", s, l)
	}
	var ls struct{ Items []summary }
	c.do("GET", lb+"/lessons", nil, &ls)
	if ls.Items[7].ID != "onderhoud" || !strings.Contains(ls.Items[7].Unavailable, "minstens 2 nodes") {
		t.Fatalf("les 8 met 0 nodes: %+v", ls.Items[7])
	}
}
