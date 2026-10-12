package whatbroke

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// kubeWarning maakt een waarschuwing uit Kubernetes zoals de database hem
// teruggeeft.
func (f *fixture) kubeWarning(cluster uuid.UUID, n *Node, ts time.Time, reason, pod, message string, count int32) Entry {
	f.nextID++
	r := store.ListKubeEventsInScopeRow{
		ID: f.nextID, ClusterID: cluster, Key: "demo/" + pod + ".1", Ts: ts, LastTs: ts, Count: count, Reason: reason,
		Namespace: "demo", ObjectKind: "Pod", ObjectName: pod, AppKind: "Deployment", AppName: "web", Message: message,
		ClusterName: f.world.Clusters[cluster],
	}
	if n != nil {
		r.NodeID, r.Hostname, r.NodeName = &n.ID, n.Hostname, n.Hostname
	}
	return FromKubeEvent(r)
}

func (f *fixture) rollout(cluster uuid.UUID, seen time.Time, at string, from, to string) Entry {
	p := map[string]any{"kind": "Deployment", "namespace": "demo", "name": "web", "from": from, "to": to}
	if at != "" {
		p["at"] = at
	}
	return f.event(seen, "kube.rolled_out", "Uitrol van web in demo: image "+from+" → "+to,
		audit.Subject{Type: "cluster", ID: cluster.String()}, &cluster, nil, nil, p)
}

func (f *fixture) appStatus(cluster uuid.UUID, ts time.Time, from, to, reason string) Entry {
	return f.event(ts, "kube.app_status_changed", "App web in demo van "+from+" naar "+to,
		audit.Subject{Type: "cluster", ID: cluster.String()}, &cluster, nil, nil,
		map[string]any{"kind": "Deployment", "namespace": "demo", "name": "web", "from": from, "to": to, "reason": reason})
}

// Een uitrol met een image die niet bestaat: de eerste waarschuwing van
// Kubernetes is het eerste symptoom, en de uitrol de oorzaak, met het
// verband in een zin.
func TestAnalyzeKubeRollout(t *testing.T) {
	f := newFixture()
	at := t0.Add(-2 * time.Minute)
	rolled := f.rollout(f.web, t0.Add(-100*time.Second), at.Format(time.RFC3339), "nginx:1.29", "nginx:1.299")
	pull := f.kubeWarning(f.web, &f.web1, at.Add(2*time.Second), "Failed", "web-5d4-x", `Failed to pull image "nginx:1.299": not found`, 1)
	backoff := f.kubeWarning(f.web, &f.web1, at.Add(10*time.Second), "BackOff", "web-5d4-x", `Back-off pulling image "nginx:1.299"`, 4)
	info := f.kubeWarning(f.web, nil, at.Add(20*time.Second), "FailedToUpdateEndpoint", "web-5d4-x", "conflict", 1)
	scaled := f.event(t0.Add(-40*time.Minute), "kube.scaled", "Deployment demo/api geschaald van 1 naar 2 pods",
		audit.Subject{Type: "cluster", ID: f.web.String()}, &f.web, nil, nil, map[string]any{"kind": "Deployment", "namespace": "demo", "name": "api"})
	started := t0.Add(-100 * time.Second)
	in := Input{
		Started: started,
		Subjects: []Subject{
			{Type: "app", ID: uuid.New(), Name: "web in demo", ClusterID: &f.web, Status: "degraded", Since: started},
			clusterSubject(f, f.web, "degraded", t0),
		},
		World: f.world,
		Entries: []Entry{
			rolled, pull, backoff, info, scaled,
			f.appStatus(f.web, t0, "healthy", "degraded", "de uitrol hangt (ImagePullBackOff)"),
			f.clusterStatus(f.web, t0, "healthy", "degraded"),
		},
		MetricsUnknown: true,
	}
	if pull.Class != events.ClassSymptom || info.Class != events.ClassInfo || pull.Source != SourceKube ||
		pull.Link != "/kubernetes/object?id="+f.web.String()+"&g=apps&v=v1&r=deployments&ns=demo&name=web" {
		t.Fatalf("waarschuwing = %+v", pull)
	}
	if backoff.Summary != `BackOff bij pod web-5d4-x in demo: Back-off pulling image "nginx:1.299" (4 keer)` {
		t.Fatalf("samenvatting = %q", backoff.Summary)
	}
	if !rolled.Ts.Equal(at) || rolled.Delay != "" {
		t.Fatalf("uitrol op het begin = %+v", rolled)
	}
	a := Analyze(in)
	if a.FirstSymptom.EntryID != pull.ID {
		t.Fatalf("eerste symptoom = %+v", a.FirstSymptom)
	}
	if len(a.Causes) == 0 || a.Causes[0].EntryID != rolled.ID || a.Causes[0].Level != LevelLikely {
		t.Fatalf("oorzaken = %+v", a.Causes)
	}
	link := "web in demo kan zijn image niet ophalen sinds de uitrol van image nginx:1.299 om " + at.Local().Format("15:04") + "."
	if !slices.Contains(a.Causes[0].Reasons, link) || a.Causes[0].Cluster != "Web" {
		t.Fatalf("redenen = %q", a.Causes[0].Reasons)
	}
	want := "Waarschijnlijke oorzaak: Uitrol van web in demo: image nginx:1.29 → nginx:1.299, tegelijk met het eerste symptoom."
	if a.Conclusion != want {
		t.Fatalf("conclusie = %q", a.Conclusion)
	}
	for _, c := range a.Causes[1:] {
		if c.EntryID == scaled.ID && c.Level == LevelLikely {
			t.Fatalf("schalen van een andere app 40 min ervoor = %+v", c)
		}
	}

	// Zonder het begin ligt de uitrol in de 30 s voor de sync hem zag.
	late := f.rollout(f.web, t0, "", "nginx:1.29", "nginx:1.299")
	if !late.Ts.Equal(t0) || !late.From.Equal(t0.Add(-30*time.Second)) || late.Delay == "" {
		t.Fatalf("uitrol zonder begin = %+v", late)
	}
}

// Een app die crasht na een uitrol, zonder waarschuwing in het venster: dan
// is de status het symptoom.
func TestAnalyzeKubeRolloutStatusOnly(t *testing.T) {
	f := newFixture()
	at := t0.Add(-3 * time.Minute)
	rolled := f.rollout(f.web, at.Add(20*time.Second), at.Format(time.RFC3339), "app:1", "app:2")
	in := Input{
		Started:        t0,
		Subjects:       []Subject{{Type: "app", ID: uuid.New(), Name: "web in demo", ClusterID: &f.web, Status: "down", Since: t0}},
		World:          f.world,
		Entries:        []Entry{rolled, f.appStatus(f.web, t0, "healthy", "down", "0 van 2 pods klaar (CrashLoopBackOff)")},
		MetricsUnknown: true,
	}
	a := Analyze(in)
	if len(a.Causes) == 0 || a.Causes[0].EntryID != rolled.ID {
		t.Fatalf("oorzaken = %+v", a.Causes)
	}
	link := "web in demo is down sinds de uitrol van image app:2 om " + at.Local().Format("15:04") + "."
	if !slices.Contains(a.Causes[0].Reasons, link) {
		t.Fatalf("redenen = %q", a.Causes[0].Reasons)
	}
}
