package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type kubeOverview struct {
	Version    string  `json:"version"`
	Via        string  `json:"via"`
	Vip        *string `json:"vip"`
	NodesTotal int     `json:"nodes_total"`
	NodesReady int     `json:"nodes_ready"`
	Pods       struct {
		Total, Running, Problem int
	} `json:"pods"`
	WorkloadsTotal int      `json:"workloads_total"`
	WorkloadsReady int      `json:"workloads_ready"`
	Namespaces     []string `json:"namespaces"`
	Allocatable    struct {
		CPUMillis   int64 `json:"cpu_millis"`
		MemoryBytes int64 `json:"memory_bytes"`
	} `json:"allocatable"`
	Warnings []struct {
		Name, Reason, Message string
		Count                 int
	} `json:"warnings"`
	Problems []kubePod `json:"problems"`
}

type kubePod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Node      string `json:"node"`
	Status    string `json:"status"`
	Problem   bool   `json:"problem"`
	Ready     int    `json:"ready"`
	Restarts  int    `json:"restarts"`
	OwnerKind string `json:"owner_kind"`
	OwnerName string `json:"owner_name"`
	System    bool   `json:"system"`
}

type kubeNode struct {
	Name        string   `json:"name"`
	Roles       []string `json:"roles"`
	Ready       bool     `json:"ready"`
	ReadyReason string   `json:"ready_reason"`
	Pods        int      `json:"pods"`
	InternalIP  string   `json:"internal_ip"`
	Node        *struct {
		ID            string `json:"id"`
		Hostname      string `json:"hostname"`
		Agent         bool   `json:"agent"`
		AgentProtocol int    `json:"agent_protocol"`
	} `json:"node"`
}

type kubeWorkload struct {
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Desired   int      `json:"desired"`
	Ready     int      `json:"ready"`
	State     string   `json:"state"`
	StateText string   `json:"state_text"`
	Images    []string `json:"images"`
}

// kubeCluster is een cluster Leren in de nep-API: een server, twee workers
// waarvan één niet Ready, en een app demo/web met een pod in
// CrashLoopBackOff.
func kubeCluster(t *testing.T) *kubefake.Server {
	t.Helper()
	fake := kubefake.New()
	fake.Start()
	t.Cleanup(fake.Close)
	fake.SeedK3s(
		kubefake.NodeSpec{Name: "leren-server-01", IP: "10.0.40.11", Server: true},
		kubefake.NodeSpec{Name: "leren-worker-01", IP: "10.0.40.12"},
		kubefake.NodeSpec{Name: "leren-worker-02", IP: "10.0.40.13", NotReady: true},
	)
	fake.Add(
		kubefake.Namespace("demo"),
		kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Image: "nginx:1.29", Replicas: 3, Ready: 2}),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "web-7c9-a", Node: "leren-worker-01", OwnerKind: "ReplicaSet", OwnerName: "web-7c9"}),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "web-7c9-b", Node: "leren-server-01", OwnerKind: "ReplicaSet", OwnerName: "web-7c9"}),
		kubefake.Pod(kubefake.PodSpec{
			Namespace: "demo", Name: "web-7c9-c", Node: "leren-worker-02", Waiting: "CrashLoopBackOff", Restarts: 7,
			OwnerKind: "ReplicaSet", OwnerName: "web-7c9",
		}),
		map[string]any{
			"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "db", "namespace": "demo"},
			"data": map[string]any{"wachtwoord": "Z2VoZWltLXdhY2h0d29vcmQ="},
		},
		map[string]any{
			"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "web-7c9-c.17f", "namespace": "demo"},
			"type": "Warning", "reason": "BackOff", "message": "Back-off restarting failed container main", "count": 12,
			"lastTimestamp":  time.Now().UTC().Format(time.RFC3339),
			"involvedObject": map[string]any{"kind": "Pod", "namespace": "demo", "name": "web-7c9-c"},
		},
	)
	return fake
}

// Het dashboard leest een cluster via de agent op de server: overzicht,
// nodes gekoppeld aan de ClusterForge-nodes, apps en pods. Een cluster dat
// met de hand gebouwd is, met alleen cf-agent op de nodes, werkt net zo.
func TestKubernetesDashboard(t *testing.T) {
	e, c := adminClient(t)
	fake := kubeCluster(t)
	ctx := context.Background()

	var cl cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl); s != 201 {
		t.Fatalf("cluster: %d", s)
	}
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.40.100"}, nil)
	var server, worker node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-worker-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.12"}, &worker)
	base := "/api/v1/kubernetes/clusters/" + cl.ID

	var ae apiErr
	if s := c.do("GET", base, nil, &ae); s != http.StatusServiceUnavailable || ae.Code != "kube_unavailable" {
		t.Fatalf("zonder agents: %d %+v", s, ae)
	}

	// De worker heeft alleen k3s-agent: hij kan de API niet bevragen.
	startAgentWith(t, e, c, worker.ID, "a1000000000000000000000000000001", &addrs{}, func(ag *agent.Agent, root string) {
		writeFile(t, filepath.Join(root, "etc/systemd/system/k3s-agent.service"), "[Service]\nExecStart=/usr/local/bin/k3s agent\n", 0o644)
		ag.KubeletHealthz = fake.KubeletURL()
	})
	stopServer := startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000002", &addrs{}, func(ag *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
		ag.KubeletHealthz = fake.KubeletURL()
	})

	var o kubeOverview
	eventually(t, "overzicht", func() bool { return c.do("GET", base, nil, &o) == http.StatusOK })
	if o.Version != "v1.35.9+k3s1" || o.Via != "leren-server-01" || o.Vip == nil || *o.Vip != "10.0.40.100" || o.NodesTotal != 3 ||
		o.NodesReady != 2 || o.Pods.Problem != 1 || !slices.Contains(o.Namespaces, "demo") || o.Allocatable.CPUMillis != 6000 {
		t.Fatalf("overzicht: %+v", o)
	}
	// 4 Deployments van k3s en de DaemonSet van ServiceLB zijn klaar, web niet.
	if o.WorkloadsTotal != 6 || o.WorkloadsReady != 5 {
		t.Fatalf("apps: %d van %d", o.WorkloadsReady, o.WorkloadsTotal)
	}
	if len(o.Problems) != 1 || o.Problems[0].Name != "web-7c9-c" || o.Problems[0].Status != "CrashLoopBackOff" || o.Problems[0].Restarts != 7 {
		t.Fatalf("problemen: %+v", o.Problems)
	}
	if len(o.Warnings) != 1 || o.Warnings[0].Reason != "BackOff" || o.Warnings[0].Count != 12 {
		t.Fatalf("waarschuwingen: %+v", o.Warnings)
	}

	var nodes struct{ Items []kubeNode }
	c.do("GET", base+"/nodes", nil, &nodes)
	if len(nodes.Items) != 3 {
		t.Fatalf("nodes: %+v", nodes)
	}
	s1, w1, w2 := nodes.Items[0], nodes.Items[1], nodes.Items[2]
	if s1.Name != "leren-server-01" || s1.Node == nil || s1.Node.ID != server.ID || !s1.Node.Agent || s1.Node.AgentProtocol != protocol.Version ||
		!slices.Equal(s1.Roles, []string{"control-plane", "etcd", "master"}) || !s1.Ready {
		t.Fatalf("server: %+v %+v", s1, s1.Node)
	}
	if w1.Node == nil || w1.Node.Hostname != "leren-worker-01" || len(w1.Roles) != 0 || w1.InternalIP != "10.0.40.12" {
		t.Fatalf("worker-01: %+v", w1)
	}
	// worker-02 staat niet in ClusterForge en is niet Ready.
	if w2.Node != nil || w2.Ready || w2.ReadyReason != "Kubelet stopped posting node status." || w2.Pods != 3 {
		t.Fatalf("worker-02: %+v", w2)
	}

	var ws struct{ Items []kubeWorkload }
	c.do("GET", base+"/workloads", nil, &ws)
	if len(ws.Items) != 1 || ws.Items[0].Name != "web" || ws.Items[0].Desired != 3 || ws.Items[0].Ready != 2 || ws.Items[0].State != "degraded" ||
		ws.Items[0].Images[0] != "nginx:1.29" {
		t.Fatalf("apps zonder systeem: %+v", ws.Items)
	}
	c.do("GET", base+"/workloads?system=true", nil, &ws)
	if len(ws.Items) != 6 {
		t.Fatalf("apps met systeem: %+v", ws.Items)
	}
	c.do("GET", base+"/workloads?namespace=kube-system", nil, &ws)
	if len(ws.Items) != 5 || slices.ContainsFunc(ws.Items, func(w kubeWorkload) bool { return w.Namespace != "kube-system" }) {
		t.Fatalf("apps in kube-system: %+v", ws.Items)
	}

	var pods struct{ Items []kubePod }
	c.do("GET", base+"/pods", nil, &pods)
	if len(pods.Items) != 3 || pods.Items[0].OwnerKind != "ReplicaSet" || pods.Items[0].Ready != 1 {
		t.Fatalf("pods: %+v", pods.Items)
	}
	c.do("GET", base+"/pods?node=leren-worker-02&system=true", nil, &pods)
	if len(pods.Items) != 3 || pods.Items[0].Name != "web-7c9-c" || !pods.Items[0].Problem || !pods.Items[1].System {
		t.Fatalf("pods op worker-02: %+v", pods.Items)
	}
	for _, bad := range []string{"/pods?namespace=..%2Fsecrets", "/pods?node=a%2Fb", "/workloads?namespace=Demo"} {
		if s := c.do("GET", base+bad, nil, &ae); s != http.StatusBadRequest {
			t.Errorf("%s: %d %+v", bad, s, ae)
		}
	}

	// Een viewer leest mee.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("GET", base, nil, &o); s != http.StatusOK {
		t.Fatalf("viewer: %d", s)
	}

	// Een ander cluster is geen Kubernetes.
	var other cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &other)
	if s := c.do("GET", "/api/v1/kubernetes/clusters/"+other.ID, nil, &ae); s != http.StatusNotFound {
		t.Fatalf("geen Kubernetes: %d", s)
	}

	// De samenvatting voor de lijst, met de nodes gekoppeld.
	id := uuid.MustParse(cl.ID)
	if err := e.kube.Sync(ctx, id); err != nil {
		t.Fatal(err)
	}
	type summary struct {
		Name        string  `json:"name"`
		Synced      bool    `json:"synced"`
		APIOk       bool    `json:"api_ok"`
		Error       string  `json:"error"`
		Version     string  `json:"version"`
		Vip         *string `json:"vip"`
		NodesTotal  int     `json:"nodes_total"`
		NodesReady  int     `json:"nodes_ready"`
		PodsTotal   int     `json:"pods_total"`
		PodsProblem int     `json:"pods_problem"`
	}
	var sums struct{ Items []summary }
	c.do("GET", "/api/v1/kubernetes/clusters", nil, &sums)
	if len(sums.Items) != 1 || !sums.Items[0].Synced || !sums.Items[0].APIOk || sums.Items[0].NodesTotal != 3 || sums.Items[0].NodesReady != 2 ||
		sums.Items[0].PodsTotal != 12 || sums.Items[0].PodsProblem != 1 || sums.Items[0].Version != "v1.35.9+k3s1" {
		t.Fatalf("samenvatting: %+v", sums.Items)
	}
	kn, err := store.New(e.pool).ListKubeNodes(ctx, id)
	if err != nil || len(kn) != 3 || kn[0].NodeID == nil || kn[0].NodeID.String() != server.ID || kn[2].NodeID != nil || kn[0].Pods != 4 {
		t.Fatalf("kube_nodes: %+v %v", kn, err)
	}

	// Valt de server weg, dan blijft de laatste samenvatting staan, met
	// de fout erbij.
	stopServer()
	e.kube.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if err := e.kube.Sync(ctx, id); err == nil {
		t.Fatal("sync zonder agent")
	}
	c.do("GET", "/api/v1/kubernetes/clusters", nil, &sums)
	if sums.Items[0].APIOk || sums.Items[0].Error == "" || sums.Items[0].NodesTotal != 3 {
		t.Fatalf("na de uitval: %+v", sums.Items)
	}
	if s := c.do("GET", base, nil, &ae); s != http.StatusServiceUnavailable || ae.Code != "kube_unavailable" || !strings.Contains(ae.Message, "leren-server-01") {
		t.Fatalf("overzicht na de uitval: %d %+v", s, ae)
	}
}

// Geen secretwaarde verlaat de agent, ook niet via NATS, en exec,
// attach, portforward en proxy bestaan niet in de brug.
func TestKubeBridgeNATS(t *testing.T) {
	e, c := adminClient(t)
	fake := kubeCluster(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	var server node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID}, &server)
	startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000003", &addrs{}, func(ag *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	id := uuid.MustParse(cl.ID)
	var body []byte
	eventually(t, "secret via de brug", func() bool {
		var err error
		body, err = e.kube.Get(ctx, id, "/api/v1/namespaces/demo/secrets/db", nil)
		return err == nil
	})
	if strings.Contains(string(body), "Z2VoZWltLXdhY2h0d29vcmQ=") || !strings.Contains(string(body), `"wachtwoord":"(verborgen, 17 bytes)"`) {
		t.Fatalf("secret: %s", body)
	}
	all, err := e.kube.Get(ctx, id, "/api/v1/secrets", nil)
	if err != nil || strings.Contains(string(all), "Z2VoZWltLXdhY2h0d29vcmQ=") || strings.Contains(string(all), "LS0tLS1CRUdJTi") {
		t.Fatalf("alle secrets: %v %s", err, all)
	}

	nodeID := uuid.MustParse(server.ID)
	for _, p := range []string{
		"/api/v1/namespaces/demo/pods/web-7c9-a/exec", "/api/v1/namespaces/demo/pods/web-7c9-a/attach",
		"/api/v1/namespaces/demo/pods/web-7c9-a/portforward", "/api/v1/namespaces/demo/pods/web-7c9-a/proxy",
		"/api/v1/nodes/leren-server-01/proxy/configz",
	} {
		res, err := e.bus.Kube(ctx, nodeID, protocol.KubeRequest{Op: protocol.KubeRead, Path: p, Query: map[string]string{"command": "sh"}})
		if err != nil || res.Status != 0 || res.Error == "" {
			t.Errorf("%s: %+v %v", p, res, err)
		}
		res, err = e.bus.Kube(ctx, nodeID, protocol.KubeRequest{Op: "exec", Path: p})
		if err != nil || res.Error != "onbekend kube-verzoek exec" {
			t.Errorf("exec %s: %+v %v", p, res, err)
		}
	}
	for _, r := range fake.Requests() {
		for _, sub := range []string{"/exec", "/attach", "/portforward", "/proxy"} {
			if strings.Contains(r, sub) {
				t.Fatalf("de API kreeg %s", r)
			}
		}
	}
}

// Een agent van voor protocol 8 kan de brug niet; het dashboard zegt dan
// wat er moet gebeuren.
func TestKubernetesAgentTooOld(t *testing.T) {
	e, c := adminClient(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "oud", "name": "Oud", "type": "kubernetes", "environment": "lab"}, &cl)
	var n node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "oud-server-01", "cluster_id": cl.ID}, &n)
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO agents (id, node_id, nkey_public, machine_id, version, protocol_version)
		VALUES (gen_random_uuid(), $1, 'UOUD0000000000000000000000000000000000000000000000000000', 'b0000000000000000000000000000001', '0.7.0', 7)`, n.ID); err != nil {
		t.Fatal(err)
	}
	var ae apiErr
	if s := c.do("GET", "/api/v1/kubernetes/clusters/"+cl.ID, nil, &ae); s != http.StatusServiceUnavailable || ae.Code != "agent_too_old" ||
		!strings.Contains(ae.Message, "oud-server-01") || !strings.Contains(ae.Message, "--upgrade") {
		t.Fatalf("te oud: %d %+v", s, ae)
	}
}

func TestPreferences(t *testing.T) {
	_, c := adminClient(t)
	type meKube struct {
		User struct {
			KubeLearning bool `json:"kube_learning"`
		} `json:"user"`
	}
	var m meKube
	c.do("GET", "/api/v1/auth/me", nil, &m)
	if !m.User.KubeLearning {
		t.Fatal("de leermodus staat standaard aan")
	}
	if s := c.do("PUT", "/api/v1/auth/preferences", map[string]any{"kube_learning": false}, nil); s != http.StatusOK {
		t.Fatalf("voorkeuren: %d", s)
	}
	c.do("GET", "/api/v1/auth/me", nil, &m)
	if m.User.KubeLearning {
		t.Fatal("de leermodus staat nog aan")
	}
}
