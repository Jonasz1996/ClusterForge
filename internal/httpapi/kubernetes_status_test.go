package httpapi

import (
	"context"
	"net/http"
	"net/url"
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

// appSubject is een onderwerp van een incident, met de app als het er een
// is.
type appSubject struct {
	Type string `json:"type"`
	Name string `json:"name"`
	App  *struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"app"`
}

// TestKubernetesStatus is de acceptatie van mijlpaal 8 van fase 4: een app
// met een kapotte image maakt het cluster verminderd, geeft een incident,
// en What Broke noemt de uitrol als oorzaak. CPU en geheugen per pod gaan
// naar VictoriaMetrics en staan in een grafiek. Antwoordt de API via geen
// enkele server, dan is het cluster down.
func TestKubernetesStatus(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	q := store.New(e.pool)
	fake := kubefake.New()
	fake.Start()
	t.Cleanup(fake.Close)
	fake.SeedK3s(kubefake.NodeSpec{Name: "leren-server-01", IP: "10.0.40.11", Server: true})
	// Een antwoord van de API geldt twee seconden; de test zet de klok
	// van de sync vooruit om een wijziging meteen te zien.
	clock := new(atomic.Int64)
	e.kube.Now = func() time.Time { return time.Now().Add(time.Duration(clock.Load())) }
	webPod := func(name, hash, image, waiting string, created time.Time) map[string]any {
		return kubefake.Pod(kubefake.PodSpec{
			Namespace: "demo", Name: name, Node: "leren-server-01", Image: image, Waiting: waiting, Created: created,
			OwnerKind: "ReplicaSet", OwnerName: "web-" + hash, Labels: map[string]string{"app": "web", "pod-template-hash": hash},
		})
	}
	start := time.Now().Add(-time.Hour)
	fake.Add(
		kubefake.Namespace("demo"),
		kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Image: "nginx:1.29", Replicas: 2, Ready: 2}),
		webPod("web-5d4-a", "5d4", "nginx:1.29", "", start),
		webPod("web-5d4-b", "5d4", "nginx:1.29", "", start),
		kubefake.PodMetrics("demo", "web-5d4-a", "250m", "64Mi"),
		kubefake.PodMetrics("demo", "web-5d4-b", "1500000n", "32Mi"),
		kubefake.NodeMetrics("leren-server-01", "800m", "1Gi"),
	)

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	var server node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000081", &addrs{}, func(ag *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
		ag.KubeletHealthz = fake.KubeletURL()
	})
	id := uuid.MustParse(cl.ID)
	eventually(t, "eerste sync", func() bool { return e.kube.Sync(ctx, id) == nil })
	clusterIs := func(status, reason string) func() bool {
		return func() bool {
			e.eval.Kick()
			var got statusView
			c.do("GET", "/api/v1/clusters/"+cl.ID, nil, &got)
			return got.Status == status && strings.HasPrefix(got.StatusReason, reason)
		}
	}
	eventually(t, "cluster gezond", clusterIs("healthy", ""))
	apps, err := q.ListKubeApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].Name != "web" || apps[0].Status != "healthy" || apps[0].ShortSince != nil || apps[0].Desired != 2 {
		t.Fatalf("apps: %+v %v", apps, err)
	}

	// CPU en geheugen per pod, met de labels van de node erbij.
	e.ingest.Flush(ctx)
	lines := e.vm.received()
	labels := `{job="clusterforge",instance="leren-server-01",node="leren-server-01",node_id="` + server.ID + `",cluster="leren",cluster_id="` +
		cl.ID + `",env="lab"`
	for _, want := range []string{
		`kube_pod_cpu_usage_cores` + labels + `,namespace="demo",pod="web-5d4-a",workload="web",workload_kind="Deployment"} 0.25 `,
		`kube_pod_cpu_usage_cores` + labels + `,namespace="demo",pod="web-5d4-b",workload="web",workload_kind="Deployment"} 0.0015 `,
		`kube_pod_memory_working_set_bytes` + labels + `,namespace="demo",pod="web-5d4-a",workload="web",workload_kind="Deployment"} 6.7108864e+07 `,
		`kube_node_cpu_usage_cores` + labels + `} 0.8 `,
		`kube_node_memory_working_set_bytes` + labels + `} 1.073741824e+09 `,
	} {
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, want) }) {
			t.Errorf("geen %s in\n%s", want, strings.Join(lines, "\n"))
		}
	}
	// De volgende sync binnen de minuut leest de metrics niet opnieuw.
	kubeLines := func() int {
		e.ingest.Flush(ctx)
		n := 0
		for _, l := range e.vm.received() {
			if strings.HasPrefix(l, "kube_") {
				n++
			}
		}
		return n
	}
	if err := e.kube.Sync(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := kubeLines(); n != 6 {
		t.Fatalf("metrics binnen de minuut: %d regels", n)
	}

	// De grafieken: per namespace voor het cluster, per pod voor een app.
	base := "/api/v1/kubernetes/clusters/" + cl.ID + "/metrics"
	var m struct {
		Range  string `json:"range"`
		Panels []struct {
			ID   string `json:"id"`
			Unit string `json:"unit"`
		} `json:"panels"`
	}
	if s := c.do("GET", base, nil, &m); s != http.StatusOK || len(m.Panels) != 2 || m.Panels[0].Unit != "millicores" || m.Panels[1].Unit != "bytes" {
		t.Fatalf("cluster: %d %+v", s, m)
	}
	if s := c.do("GET", base+"?range=6h&namespace=demo&kind=Deployment&name=web", nil, &m); s != http.StatusOK || m.Range != "6h" ||
		len(m.Panels) != 2 {
		t.Fatalf("app: %d %+v", s, m)
	}
	e.vm.mu.Lock()
	queries := slices.Clone(e.vm.queries)
	e.vm.mu.Unlock()
	if !slices.ContainsFunc(queries, func(s string) bool {
		return strings.Contains(s, `sum by (pod) (kube_pod_cpu_usage_cores{cluster_id="`+cl.ID+`",namespace="demo",workload_kind="Deployment",workload="web"})`)
	}) || !slices.ContainsFunc(queries, func(s string) bool {
		return strings.Contains(s, `sum by (namespace) (kube_pod_memory_working_set_bytes{cluster_id="`+cl.ID+`"})`)
	}) {
		t.Fatalf("queries: %q", queries)
	}
	if s := c.do("GET", base+"?namespace=demo&pod=web-5d4-a", nil, &m); s != http.StatusOK {
		t.Fatalf("pod: %d", s)
	}
	for _, bad := range []string{
		"?kind=Deployment", "?namespace=demo&name=web", "?namespace=demo&kind=Job&name=web", "?kind=Deployment&name=web",
		"?pod=web-5d4-a", "?namespace=demo&kind=Deployment&name=web&pod=web-5d4-a", "?namespace=Demo", `?namespace=demo&pod=a"}`,
	} {
		if s := c.do("GET", base+bad, nil, nil); s != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, s)
		}
	}
	if s := c.do("GET", "/api/v1/kubernetes/clusters/"+uuid.NewString()+"/metrics", nil, nil); s != http.StatusNotFound {
		t.Errorf("onbekend cluster: %d", s)
	}

	// De uitrol van een image die niet bestaat: de oude pods draaien door,
	// de nieuwe kan zijn image niet ophalen.
	rolled := time.Now().Add(-3*time.Minute - 10*time.Second).Truncate(time.Second)
	fake.Add(
		kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Image: "nginx:1.299", Replicas: 2, Ready: 2, Old: 2}),
		webPod("web-7c9-x", "7c9", "nginx:1.299", "ImagePullBackOff", rolled),
		webPod("web-7c9-y", "7c9", "nginx:1.299", "ErrImagePull", rolled.Add(time.Second)),
		map[string]any{
			"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "web-7c9-x.1", "namespace": "demo"},
			"type": "Warning", "reason": "Failed", "message": `Failed to pull image "nginx:1.299": not found`, "count": 3,
			"firstTimestamp": rolled.Add(5 * time.Second).UTC().Format(time.RFC3339), "lastTimestamp": time.Now().UTC().Format(time.RFC3339),
			"source":         map[string]any{"component": "kubelet", "host": "leren-server-01"},
			"involvedObject": map[string]any{"kind": "Pod", "namespace": "demo", "name": "web-7c9-x"},
		},
		map[string]any{
			"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "web.2", "namespace": "demo"},
			"type": "Warning", "reason": "ProgressDeadlineExceeded", "message": "lang bezig", "count": 1,
			"firstTimestamp": time.Now().UTC().Format(time.RFC3339), "lastTimestamp": time.Now().UTC().Format(time.RFC3339),
			"involvedObject": map[string]any{"kind": "Deployment", "namespace": "demo", "name": "web"},
		},
	)
	clock.Add(int64(5 * time.Second))
	if err := e.kube.Sync(ctx, id); err != nil {
		t.Fatal(err)
	}
	kubeLog := func(action string) []auditEntry {
		var out []auditEntry
		for _, it := range c.audit("category=kubernetes&cluster=" + cl.ID).Items {
			if it.Action == action {
				out = append(out, it)
			}
		}
		return out
	}
	out := kubeLog("kube.rolled_out")
	if len(out) != 1 || out[0].Summary != "Uitrol van web in demo: image nginx:1.29 → nginx:1.299" {
		t.Fatalf("uitrol in het logboek: %+v", out)
	}
	warnings, err := q.ListKubeEventsInScope(ctx, store.ListKubeEventsInScopeParams{
		ClusterIds: []uuid.UUID{id}, NodeIds: []uuid.UUID{}, Since: start, Until: time.Now().Add(time.Minute), Lim: 10,
	})
	// Nieuwste eerst.
	if err != nil || len(warnings) != 2 || warnings[1].Reason != "Failed" || warnings[1].AppName != "web" || warnings[1].NodeID == nil ||
		warnings[1].NodeID.String() != server.ID || !warnings[1].Ts.Equal(rolled.Add(5*time.Second)) || warnings[0].AppKind != "Deployment" ||
		warnings[0].NodeID != nil {
		t.Fatalf("waarschuwingen: %+v %v", warnings, err)
	}
	// Binnen de twee minuten is de app nog in orde: een uitrol duurt even.
	e.eval.Kick()
	apps, _ = q.ListKubeApps(ctx)
	if len(apps) != 1 || apps[0].ShortSince == nil || apps[0].Trouble != "ErrImagePull, ImagePullBackOff" {
		t.Fatalf("app na de uitrol: %+v", apps)
	}
	eventually(t, "cluster nog gezond", clusterIs("healthy", ""))

	// Drie minuten later: de app is verminderd, en het cluster ook.
	if _, err := e.pool.Exec(ctx, `UPDATE kube_workloads SET short_since = $1 WHERE name = 'web'`, rolled.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cluster verminderd", clusterIs("degraded", "app web in demo: de uitrol hangt (ErrImagePull, ImagePullBackOff)"))
	apps, _ = q.ListKubeApps(ctx)
	if apps[0].Status != "degraded" || apps[0].StatusReason != "de uitrol hangt (ErrImagePull, ImagePullBackOff)" ||
		apps[0].StatusSince == nil || !apps[0].StatusSince.Equal(rolled.Add(10*time.Second)) {
		t.Fatalf("app verminderd: %+v", apps[0])
	}
	changed := kubeLog("kube.app_status_changed")
	if len(changed) != 1 || changed[0].Summary != "App web in demo van gezond naar verminderd: de uitrol hangt (ErrImagePull, ImagePullBackOff)" {
		t.Fatalf("statuswissel: %+v", changed)
	}

	// Het incident heeft de app en het cluster.
	var inc incidentView
	eventually(t, "incident", func() bool {
		incs := c.incidents("status=open")
		if len(incs) == 1 {
			inc = incs[0]
		}
		return len(incs) == 1
	})
	if inc.Title != "App web in demo is verminderd" || len(inc.Subjects) != 2 {
		t.Fatalf("incident: %+v", inc)
	}
	var d struct {
		Incident struct {
			Subjects []appSubject `json:"subjects"`
		} `json:"incident"`
		History  []auditEntry `json:"history"`
		Analysis *struct {
			Conclusion   string `json:"conclusion"`
			FirstSymptom struct {
				EntryID string `json:"entry_id"`
			} `json:"first_symptom"`
			Causes []struct {
				EntryID string   `json:"entry_id"`
				Level   string   `json:"level"`
				Summary string   `json:"summary"`
				Cluster string   `json:"cluster"`
				Reasons []string `json:"reasons"`
			} `json:"causes"`
		} `json:"analysis"`
		Timeline []timelineEntry `json:"timeline"`
	}
	e.ana.Kick()
	eventually(t, "analyse", func() bool {
		return c.do("GET", "/api/v1/incidents/"+inc.ID, nil, &d) == http.StatusOK && d.Analysis != nil
	})
	subs := d.Incident.Subjects
	i := slices.IndexFunc(subs, func(s appSubject) bool {
		return s.Type == "app"
	})
	if i < 0 || subs[i].Name != "web in demo" || subs[i].App == nil || subs[i].App.Kind != "Deployment" || subs[i].App.Namespace != "demo" ||
		subs[i].App.Name != "web" || !slices.ContainsFunc(subs, func(s appSubject) bool {
		return s.Type == "cluster" && s.App == nil
	}) {
		t.Fatalf("onderwerpen: %+v", subs)
	}
	if !slices.ContainsFunc(d.History, func(a auditEntry) bool { return a.Action == "kube.rolled_out" }) {
		t.Errorf("geschiedenis zonder de uitrol: %+v", actionsOf(d.History))
	}

	// What Broke: het eerste symptoom is de waarschuwing, de oorzaak de
	// uitrol.
	a := d.Analysis
	first := slices.IndexFunc(d.Timeline, func(en timelineEntry) bool { return en.ID == a.FirstSymptom.EntryID })
	if first < 0 || d.Timeline[first].Source != "kube" || d.Timeline[first].Action != "Failed" ||
		d.Timeline[first].Link != "/kubernetes/object?id="+cl.ID+"&g=apps&v=v1&r=deployments&ns=demo&name=web" {
		t.Fatalf("eerste symptoom %q in %q", a.FirstSymptom.EntryID, summaries(d.Timeline))
	}
	link := "web in demo kan zijn image niet ophalen sinds de uitrol van image nginx:1.299 om " + rolled.Local().Format("15:04") + "."
	if len(a.Causes) == 0 || a.Causes[0].Summary != "Uitrol van web in demo: image nginx:1.29 → nginx:1.299" || a.Causes[0].Level != "likely" ||
		a.Causes[0].Cluster != "Leren" || !slices.Contains(a.Causes[0].Reasons, link) {
		t.Fatalf("oorzaken: %+v", a.Causes)
	}
	if !strings.HasPrefix(a.Conclusion, "Waarschijnlijke oorzaak: Uitrol van web in demo") {
		t.Fatalf("conclusie: %q", a.Conclusion)
	}
	tl := c.timeline("/api/v1/clusters/"+cl.ID+"/timeline", url.Values{"from": {start.Format(time.RFC3339)}})
	if !slices.ContainsFunc(tl.Entries, func(en timelineEntry) bool {
		return en.Source == "kube" && en.Summary == `Failed bij pod web-7c9-x in demo: Failed to pull image "nginx:1.299": not found (3 keer)` &&
			en.Hostname == "leren-server-01" && en.Detail["app"] == "web"
	}) {
		t.Fatalf("tijdlijn: %q", summaries(tl.Entries))
	}

	// De API antwoordt niet meer, terwijl de agent er nog is: na 90 s is
	// het cluster down. De apps houden hun laatste status.
	fake.Close()
	clock.Add(int64(5 * time.Second))
	if err := e.kube.Sync(ctx, id); err == nil {
		t.Fatal("sync zonder API")
	}
	eventually(t, "nog verminderd", clusterIs("degraded", "app web in demo"))
	if _, err := e.pool.Exec(ctx, `UPDATE kube_clusters SET ok_at = now() - interval '2 minutes' WHERE cluster_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cluster down", clusterIs("down", "de Kubernetes-API antwoordt via geen enkele server: leren-server-01: "))
}
