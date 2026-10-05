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
