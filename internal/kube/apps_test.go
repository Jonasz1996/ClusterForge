package kube

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
)

// decode leest een object van de nep-API zoals de sync het krijgt.
func decode[T any](t *testing.T, o map[string]any) T {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func workload(t *testing.T, kind string, o map[string]any) Workload {
	w := decode[Workload](t, o)
	w.Kind = kind
	return w
}

func TestWorkloadOf(t *testing.T) {
	for _, tc := range []struct {
		pod        kubefake.PodSpec
		kind, name string
	}{
		{kubefake.PodSpec{OwnerKind: "ReplicaSet", OwnerName: "web-api-5d9c7b6f4", Labels: map[string]string{"pod-template-hash": "5d9c7b6f4"}},
			"Deployment", "web-api"},
		{kubefake.PodSpec{OwnerKind: "ReplicaSet", OwnerName: "web-api-5d9c7b6f4"}, "Deployment", "web-api"},
		{kubefake.PodSpec{OwnerKind: "StatefulSet", OwnerName: "db"}, "StatefulSet", "db"},
		{kubefake.PodSpec{OwnerKind: "DaemonSet", OwnerName: "logs"}, "DaemonSet", "logs"},
		{kubefake.PodSpec{OwnerKind: "Job", OwnerName: "backup-29"}, "Job", "backup-29"},
		{kubefake.PodSpec{OwnerKind: "Node", OwnerName: "s1"}, "", ""},
		{kubefake.PodSpec{}, "", ""},
	} {
		tc.pod.Namespace, tc.pod.Name = "demo", "p"
		p := decode[Pod](t, kubefake.Pod(tc.pod))
		if k, n := WorkloadOf(p.Metadata); k != tc.kind || n != tc.name {
			t.Errorf("%s %s: %s %s, verwacht %s %s", tc.pod.OwnerKind, tc.pod.OwnerName, k, n, tc.kind, tc.name)
		}
	}
}

func TestHealth(t *testing.T) {
	pod := func(name, waiting string) Pod {
		return decode[Pod](t, kubefake.Pod(kubefake.PodSpec{
			Namespace: "demo", Name: name, Waiting: waiting, OwnerKind: "ReplicaSet", OwnerName: "web-5d4",
			Labels: map[string]string{"pod-template-hash": "5d4"},
		}))
	}
	other := decode[Pod](t, kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "api-1", Waiting: "CrashLoopBackOff", OwnerKind: "ReplicaSet", OwnerName: "api-7f"}))
	elsewhere := decode[Pod](t, kubefake.Pod(kubefake.PodSpec{Namespace: "prod", Name: "web-1", Waiting: "Error", OwnerKind: "ReplicaSet", OwnerName: "web-5d4"}))
	for _, tc := range []struct {
		name    string
		w       kubefake.WorkloadSpec
		pods    []Pod
		short   bool
		trouble string
		state   string
	}{
		{"in orde", kubefake.WorkloadSpec{Replicas: 2, Ready: 2}, []Pod{pod("web-a", ""), pod("web-b", ""), other, elsewhere}, false, "", WorkloadReady},
		{"te weinig klaar", kubefake.WorkloadSpec{Replicas: 3, Ready: 1},
			[]Pod{pod("web-a", ""), pod("web-b", "CrashLoopBackOff"), pod("web-c", "CrashLoopBackOff"), other}, true, "CrashLoopBackOff", WorkloadDegraded},
		// De oude pods draaien nog, de nieuwe kan zijn image niet ophalen.
		{"uitrol hangt", kubefake.WorkloadSpec{Replicas: 2, Ready: 2, Old: 2},
			[]Pod{pod("web-a", ""), pod("web-b", ""), pod("web-n", "ImagePullBackOff"), pod("web-m", "ErrImagePull")},
			true, "ErrImagePull, ImagePullBackOff", WorkloadRolling},
		// Een gewone uitrol: de nieuwe pod start nog, zonder probleem.
		{"uitrol loopt", kubefake.WorkloadSpec{Replicas: 2, Ready: 2, Old: 1}, []Pod{pod("web-a", ""), pod("web-b", "")}, false, "", WorkloadRolling},
		{"op nul", kubefake.WorkloadSpec{Replicas: 0, Ready: 0}, []Pod{pod("web-a", "Error")}, false, "Error", WorkloadStopped},
	} {
		tc.w.Namespace, tc.w.Name = "demo", "web"
		w := workload(t, "Deployment", kubefake.Deployment(tc.w))
		short, trouble := w.Health(tc.pods)
		if st, _ := w.State(); short != tc.short || trouble != tc.trouble || st != tc.state {
			t.Errorf("%s: %v %q %s, verwacht %v %q %s", tc.name, short, trouble, st, tc.short, tc.trouble, tc.state)
		}
	}
	w := workload(t, "Deployment", kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Replicas: 2, Ready: 2, Old: 1}))
	if _, text := w.State(); text != "nog 1 oude pod naast de nieuwe" {
		t.Errorf("uitleg = %q", text)
	}
}

func TestImagesChanged(t *testing.T) {
	for _, tc := range []struct {
		before, after, from, to []string
		ok                      bool
	}{
		{[]string{"nginx:1.29"}, []string{"nginx:1.29"}, nil, nil, false},
		{[]string{"nginx:1.29", "sidecar:1"}, []string{"nginx:1.30", "sidecar:1"}, []string{"nginx:1.29"}, []string{"nginx:1.30"}, true},
		{[]string{"app:1"}, []string{"app:1", "proxy:2"}, []string{""}, []string{"proxy:2"}, true},
		{nil, []string{"app:1"}, []string{""}, []string{"app:1"}, true},
	} {
		from, to, ok := imagesChanged(tc.before, tc.after)
		if ok != tc.ok || !slices.Equal(from, tc.from) || !slices.Equal(to, tc.to) {
			t.Errorf("%v → %v: %v %v %v", tc.before, tc.after, from, to, ok)
		}
	}
}

func TestRolloutStart(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 14, 2, 0, 0, time.UTC)
	pod := func(name, image string, created time.Time, owner string) Pod {
		return decode[Pod](t, kubefake.Pod(kubefake.PodSpec{
			Namespace: "demo", Name: name, Image: image, Created: created, OwnerKind: "ReplicaSet", OwnerName: owner,
		}))
	}
	w := workload(t, "Deployment", kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Image: "nginx:1.30", Replicas: 2}))
	pods := []Pod{
		pod("web-old", "nginx:1.29", t0.Add(-time.Hour), "web-5d4"),
		pod("web-new-b", "nginx:1.30", t0.Add(20*time.Second), "web-7c9"),
		pod("web-new-a", "nginx:1.30", t0, "web-7c9"),
		pod("api-x", "nginx:1.30", t0.Add(-time.Minute), "api-7c9"),
		pod("web-nodate", "nginx:1.30", time.Time{}, "web-7c9"),
	}
	if at, ok := rolloutStart(w, pods, []string{"nginx:1.30"}); !ok || !at.Equal(t0) {
		t.Errorf("begin = %v %v", at, ok)
	}
	if _, ok := rolloutStart(w, pods, []string{"nginx:1.31"}); ok {
		t.Error("begin zonder pod met de image")
	}
}
