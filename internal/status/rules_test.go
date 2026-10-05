package status

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNode(t *testing.T) {
	fresh := NodeInput{HasAgent: true, HeartbeatAge: 5 * time.Second}
	with := func(f func(*NodeInput)) NodeInput {
		in := fresh
		f(&in)
		return in
	}
	tests := []struct {
		name string
		in   NodeInput
		want Result
	}{
		{"zonder agent", NodeInput{}, Result{Unknown, "geen agent"}},
		{"nog geen heartbeat", with(func(in *NodeInput) { in.HeartbeatAge = -1 }), Result{Unknown, "nog geen heartbeat ontvangen"}},
		{"gezond", fresh, Result{Healthy, ""}},
		{"vertraagd", with(func(in *NodeInput) { in.HeartbeatAge = 45 * time.Second }), Result{Degraded, "heartbeat vertraagd"}},
		{"down", with(func(in *NodeInput) { in.HeartbeatAge = 2 * time.Minute }), Result{Down, "geen heartbeat meer"}},
		{"schijf vol", with(func(in *NodeInput) { in.DiskUsedRatio, in.DiskUsedMount = 0.934, "/var" }), Result{Degraded, "schijf /var is 93 % vol"}},
		{"schijf bijna vol", with(func(in *NodeInput) { in.DiskUsedRatio = 0.89 }), Result{Healthy, ""}},
		{"service gefaald", with(func(in *NodeInput) {
			in.Services = map[string]string{"nginx": "failed", "keepalived": "active"}
		}), Result{Degraded, "service nginx is gefaald"}},
		{"enabled service draait niet", with(func(in *NodeInput) {
			in.Services = map[string]string{"nginx": "inactive", "ssh": "inactive", "docker": "inactive"}
			in.EnabledServices = []string{"nginx", "ssh"}
		}), Result{Degraded, "service nginx draait niet"}},
		{"VM uit zonder agent", NodeInput{VMStatus: "stopped"}, Result{Down, "VM staat uit in Proxmox"}},
		{"VM draait zonder agent", NodeInput{VMStatus: "running"}, Result{Unknown, "geen agent"}},
		{"VM uit en geen heartbeat", with(func(in *NodeInput) { in.HeartbeatAge, in.VMStatus = 2*time.Minute, "stopped" }), Result{Down, "VM staat uit in Proxmox"}},
		{"VM gepauzeerd", with(func(in *NodeInput) { in.HeartbeatAge, in.VMStatus = 2*time.Minute, "paused" }), Result{Down, "VM is gepauzeerd in Proxmox"}},
		// Een verse heartbeat wint van een verouderde sync.
		{"VM uit maar heartbeat vers", with(func(in *NodeInput) { in.VMStatus = "stopped" }), Result{Healthy, ""}},
		{"meerdere problemen", with(func(in *NodeInput) {
			in.HeartbeatAge = 40 * time.Second
			in.Services = map[string]string{"nginx": "failed"}
		}), Result{Degraded, "heartbeat vertraagd; service nginx is gefaald"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Node(tt.in); got != tt.want {
				t.Errorf("Node() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCluster(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	vip := ClusterVIP{ID: uuid.New(), Address: "10.0.0.10"}
	node := func(id uuid.UUID, host string, st Status, addrs ...string) ClusterNode {
		return ClusterNode{ID: id, Hostname: host, Counts: true, Result: Result{Status: st, Reason: "r-" + host}, Addresses: addrs}
	}
	tests := []struct {
		name    string
		nodes   []ClusterNode
		vips    []ClusterVIP
		want    Result
		holders int
	}{
		{"leeg", nil, nil, Result{Unknown, "geen actieve nodes"}, 0},
		{"geen agents", []ClusterNode{node(a, "web01", Unknown)}, []ClusterVIP{vip}, Result{Unknown, "geen agent op de actieve nodes"}, 0},
		{"gezond", []ClusterNode{node(a, "web01", Healthy, "10.0.0.10"), node(b, "web02", Healthy)}, []ClusterVIP{vip},
			Result{Healthy, ""}, 1},
		{"node zonder agent telt niet mee", []ClusterNode{node(a, "web01", Healthy, "10.0.0.10"), node(c, "web03", Unknown)}, []ClusterVIP{vip},
			Result{Healthy, ""}, 1},
		{"één node down", []ClusterNode{node(a, "web01", Healthy, "10.0.0.10"), node(b, "web02", Down)}, []ClusterVIP{vip},
			Result{Degraded, "web02: r-web02"}, 1},
		{"VIP zonder eigenaar", []ClusterNode{node(a, "web01", Healthy), node(b, "web02", Healthy)}, []ClusterVIP{vip},
			Result{Down, "VIP 10.0.0.10 heeft geen eigenaar"}, 0},
		{"alles down", []ClusterNode{node(a, "web01", Down), node(b, "web02", Down)}, []ClusterVIP{vip},
			Result{Down, "alle nodes zijn down"}, 0},
		{"split-brain", []ClusterNode{node(b, "web02", Healthy, "10.0.0.10"), node(a, "web01", Healthy, "10.0.0.10")}, []ClusterVIP{vip},
			Result{SplitBrain, "VIP 10.0.0.10 op web01 en web02"}, 2},
		{"node in onderhoud telt niet mee maar mag het VIP hebben", []ClusterNode{
			node(a, "web01", Healthy),
			{ID: b, Hostname: "web02", Counts: false, Result: Result{Down, "x"}, Addresses: []string{"10.0.0.10"}},
		}, []ClusterVIP{vip}, Result{Healthy, ""}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Cluster(tt.nodes, tt.vips)
			if got.Result != tt.want {
				t.Errorf("Cluster() = %+v, want %+v", got.Result, tt.want)
			}
			for _, v := range tt.vips {
				if len(got.Holders[v.ID]) != tt.holders {
					t.Errorf("holders = %v, want %d", got.Holders[v.ID], tt.holders)
				}
			}
		})
	}
}

func TestHoldersPreferFreshClaims(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	vip := ClusterVIP{ID: uuid.New(), Address: "10.0.0.10"}
	node := func(id uuid.UUID, age time.Duration) ClusterNode {
		return ClusterNode{ID: id, Counts: true, Addresses: []string{"10.0.0.10"}, HeartbeatAge: age}
	}
	// web01 liep vast met het VIP in zijn laatste heartbeat; web02 nam het
	// over en meldt het vers.
	got := Holders([]ClusterNode{node(a, 40*time.Second), node(b, 2*time.Second)}, []ClusterVIP{vip}, VIPGrace)
	if len(got[vip.ID]) != 1 || got[vip.ID][0] != b {
		t.Fatalf("houders %v, wil alleen de verse", got[vip.ID])
	}
	// Twee verse meldingen: echt split-brain.
	if got := Holders([]ClusterNode{node(a, time.Second), node(b, 2*time.Second)}, []ClusterVIP{vip}, VIPGrace); len(got[vip.ID]) != 2 {
		t.Fatalf("houders %v, wil beide", got[vip.ID])
	}
	// Eén oude melding is nog altijd de houder.
	if got := Holders([]ClusterNode{node(a, 60*time.Second)}, []ClusterVIP{vip}, VIPGrace); len(got[vip.ID]) != 1 {
		t.Fatalf("houders %v, wil de enige", got[vip.ID])
	}
	if got := Holders(nil, []ClusterVIP{vip}, VIPGrace); got[vip.ID] == nil || len(got[vip.ID]) != 0 {
		t.Fatalf("zonder houders: %#v", got[vip.ID])
	}
}

func TestSettle(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	tests := []struct {
		name  string
		raw   []uuid.UUID
		owner *uuid.UUID
		for_  time.Duration
		want  []uuid.UUID
	}{
		{"precies één houder", []uuid.UUID{b}, &a, 0, []uuid.UUID{b}},
		{"wissel: even op twee", []uuid.UUID{a, b}, &a, 5 * time.Second, []uuid.UUID{a}},
		{"wissel: even op geen", []uuid.UUID{}, &a, 5 * time.Second, []uuid.UUID{a}},
		{"te lang op twee", []uuid.UUID{a, b}, &a, VIPGrace, []uuid.UUID{a, b}},
		{"te lang op geen", []uuid.UUID{}, &a, 20 * time.Second, []uuid.UUID{}},
		{"zonder vorige eigenaar", []uuid.UUID{a, b}, nil, 0, []uuid.UUID{a, b}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Settle(tt.raw, tt.owner, tt.for_, VIPGrace)
			if len(got) != len(tt.want) {
				t.Fatalf("Settle = %v, wil %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Settle = %v, wil %v", got, tt.want)
				}
			}
		})
	}
	// Tijdens de wissel blijft het cluster gezond.
	nodes := []ClusterNode{
		{ID: a, Hostname: "web01", Counts: true, Result: Result{Status: Healthy}, Addresses: []string{"10.0.0.10"}},
		{ID: b, Hostname: "web02", Counts: true, Result: Result{Status: Healthy}, Addresses: []string{"10.0.0.10"}},
	}
	vip := ClusterVIP{ID: uuid.New(), Address: "10.0.0.10"}
	h := Holders(nodes, []ClusterVIP{vip}, VIPGrace)
	h[vip.ID] = Settle(h[vip.ID], &a, time.Second, VIPGrace)
	if res := ClusterWith(nodes, []ClusterVIP{vip}, h); res.Status != Healthy {
		t.Fatalf("tijdens de wissel: %+v", res.Result)
	}
}
