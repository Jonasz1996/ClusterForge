package status

import (
	"testing"
	"time"
)

func TestAppStatus(t *testing.T) {
	now := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	for _, tc := range []struct {
		name string
		app  App
		want Result
	}{
		{"in orde", App{Desired: 2, Ready: 2}, Result{Healthy, ""}},
		{"net te weinig", App{Desired: 2, Ready: 1, ShortSince: ago(time.Minute)}, Result{Healthy, ""}},
		{"verminderd", App{Desired: 2, Ready: 1, Trouble: "CrashLoopBackOff", ShortSince: ago(AppGrace)},
			Result{Degraded, "1 van 2 pods klaar (CrashLoopBackOff)"}},
		{"down", App{Desired: 1, Ready: 0, ShortSince: ago(5 * time.Minute)}, Result{Down, "0 van 1 pods klaar"}},
		{"uitrol hangt", App{Desired: 1, Ready: 1, Trouble: "ImagePullBackOff", ShortSince: ago(3 * time.Minute)},
			Result{Degraded, "de uitrol hangt (ImagePullBackOff)"}},
	} {
		if got := AppStatus(tc.app, now); got != tc.want {
			t.Errorf("%s: %+v, verwacht %+v", tc.name, got, tc.want)
		}
	}
}

func TestKube(t *testing.T) {
	now := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	healthy := Result{Healthy, ""}
	nodes := []KubeNode{{Name: "s1", Roles: []string{"control-plane", "etcd", "master"}, Ready: true}, {Name: "w1", Ready: true}}
	broken := AppResult{App{Namespace: "demo", Name: "web"}, Result{Degraded, "de uitrol hangt (ImagePullBackOff)"}}
	for _, tc := range []struct {
		name string
		base Result
		in   KubeInput
		want Result
	}{
		{"nooit gelezen", healthy, KubeInput{}, healthy},
		{"gezond", healthy, KubeInput{Synced: true, APIOk: true, Nodes: nodes}, healthy},
		{"API net weg", healthy, KubeInput{Synced: true, ErrorKind: "api", Error: "time-out", OKAt: ago(time.Minute)}, healthy},
		{"API weg", healthy, KubeInput{Synced: true, ErrorKind: "api", Error: "s1: connection refused", OKAt: ago(KubeAPIDown)},
			Result{Down, "de Kubernetes-API antwoordt via geen enkele server: s1: connection refused"}},
		{"API nooit gezien", healthy, KubeInput{Synced: true, ErrorKind: "api", Error: "x"}, Result{Down, "de Kubernetes-API antwoordt via geen enkele server: x"}},
		{"agent te oud", healthy, KubeInput{Synced: true, ErrorKind: "old_agent", Error: "te oud"}, healthy},
		{"geen agent", healthy, KubeInput{Synced: true, ErrorKind: "no_agent", Error: "geen k3s"}, healthy},
		{"nodes al down", Result{Down, "alle nodes zijn down"}, KubeInput{Synced: true, ErrorKind: "api"}, Result{Down, "alle nodes zijn down"}},
		{"server NotReady", healthy, KubeInput{Synced: true, APIOk: true, Nodes: []KubeNode{{Name: "s2", Roles: []string{"control-plane", "etcd"}}}},
			Result{Degraded, "server s2 is NotReady, etcd mist een lid"}},
		{"worker NotReady", Result{Degraded, "w2: geen heartbeat"}, KubeInput{Synced: true, APIOk: true, Nodes: []KubeNode{{Name: "w2"}}},
			Result{Degraded, "w2: geen heartbeat; node w2 is NotReady"}},
		{"app", healthy, KubeInput{Synced: true, APIOk: true, Nodes: nodes, Apps: []AppResult{broken, {App{Name: "ok"}, healthy}}},
			Result{Degraded, "app web in demo: de uitrol hangt (ImagePullBackOff)"}},
		{"veel apps", healthy, KubeInput{Synced: true, APIOk: true, Apps: []AppResult{broken, broken, broken, broken, broken}},
			Result{Degraded, "app web in demo: de uitrol hangt (ImagePullBackOff); app web in demo: de uitrol hangt (ImagePullBackOff); " +
				"app web in demo: de uitrol hangt (ImagePullBackOff); en nog 2"}},
	} {
		if got := Kube(tc.base, tc.in, now); got != tc.want {
			t.Errorf("%s: %+v, verwacht %+v", tc.name, got, tc.want)
		}
	}
}
