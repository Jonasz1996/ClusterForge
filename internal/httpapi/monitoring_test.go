package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
)

// addrs is een lijst adressen die de test tijdens het draaien van een agent
// kan wijzigen, bijvoorbeeld om een VIP te laten verhuizen.
type addrs struct {
	mu   sync.Mutex
	list []string
}

func (a *addrs) set(l ...string) {
	a.mu.Lock()
	a.list = l
	a.mu.Unlock()
}

func (a *addrs) get() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.list)
}

// procRoot is een bestandssysteemwortel met genoeg in /proc voor metrics.
func procRoot(t *testing.T, machineID string) string {
	t.Helper()
	root := fakeRoot(t, machineID)
	files := map[string]string{
		"proc/loadavg": "0.50 0.40 0.30 1/100 1234\n",
		"proc/meminfo": "MemTotal: 16000 kB\nMemAvailable: 8000 kB\n",
		"proc/stat":    "cpu0 100 0 50 1000 5 0 0 0\n",
		"proc/mounts":  "/dev/sda1 / ext4 rw 0 0\n",
	}
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// startAgent meldt een agent aan voor een bestaande node en laat hem draaien
// tot de test stopt of stop wordt aangeroepen.
func startAgent(t *testing.T, e *testEnv, c *client, nodeID, machineID string, a *addrs) (stop func()) {
	t.Helper()
	return startAgentWith(t, e, c, nodeID, machineID, a, nil)
}

// startAgentWith is startAgent met een kans om de agent aan te passen voor
// hij start; root is zijn bestandssysteemwortel.
func startAgentWith(t *testing.T, e *testEnv, c *client, nodeID, machineID string, a *addrs, setup func(ag *agent.Agent, root string)) (stop func()) {
	t.Helper()
	var tok newToken
	if status := c.do("POST", "/api/v1/enrollment-tokens", map[string]any{"node_id": nodeID}, &tok); status != http.StatusCreated {
		t.Fatalf("token: %d", status)
	}
	root := procRoot(t, machineID)
	cfg, err := agent.Enroll(context.Background(), agent.EnrollOptions{ServerURL: e.srv.URL, Token: tok.Token, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	col := fakeCollector(root)
	col.Addresses = a.get
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: col,
		HeartbeatInterval: 100 * time.Millisecond, MetricsInterval: 100 * time.Millisecond,
		StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"),
	}
	if setup != nil {
		setup(ag, root)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = ag.Run(ctx) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

type statusView struct {
	Status       string `json:"status"`
	StatusReason string `json:"status_reason"`
	Vips         []struct {
		Address       string  `json:"address"`
		OwnerHostname *string `json:"owner_hostname"`
	} `json:"vips"`
	Nodes []struct {
		Hostname     string `json:"hostname"`
		Status       string `json:"status"`
		StatusReason string `json:"status_reason"`
	} `json:"nodes"`
}

func TestMonitoring(t *testing.T) {
	e, c := adminClient(t)

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "prod"}, &cl)
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.99.0.20"}, nil)
	var web01, web02, web03 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": cl.ID}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": cl.ID}, &web02)
	// Een node zonder agent telt niet mee.
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web03", "cluster_id": cl.ID}, &web03)

	var d statusView
	clusterIs := func(what, status string, check func() bool) {
		t.Helper()
		eventually(t, what, func() bool {
			c.do("GET", "/api/v1/clusters/"+cl.ID, nil, &d)
			return d.Status == status && (check == nil || check())
		})
	}
	owner := func(host string) func() bool {
		return func() bool { return d.Vips[0].OwnerHostname != nil && *d.Vips[0].OwnerHostname == host }
	}

	clusterIs("cluster zonder agents", "unknown", nil)

	a1, a2 := &addrs{}, &addrs{}
	a1.set("10.99.0.20", "192.0.2.11")
	a2.set("192.0.2.12")
	stop1 := startAgent(t, e, c, web01.ID, strings.Repeat("1", 32), a1)
	startAgent(t, e, c, web02.ID, strings.Repeat("2", 32), a2)

	clusterIs("gezond cluster", "healthy", owner("web01"))
	var list struct {
		Items []struct {
			Status string `json:"status"`
			Vips   []struct {
				Address       string  `json:"address"`
				OwnerHostname *string `json:"owner_hostname"`
			} `json:"vips"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/clusters", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Status != "healthy" || len(list.Items[0].Vips) != 1 ||
		list.Items[0].Vips[0].OwnerHostname == nil || *list.Items[0].Vips[0].OwnerHostname != "web01" {
		t.Errorf("clusterlijst: %+v", list.Items)
	}
	for _, n := range d.Nodes {
		want := map[string]string{"web01": "healthy", "web02": "healthy", "web03": "unknown"}[n.Hostname]
		if n.Status != want {
			t.Errorf("%s: %s (%s), want %s", n.Hostname, n.Status, n.StatusReason, want)
		}
	}

	// Metrics komen met de labels van de server in VictoriaMetrics.
	eventually(t, "metrics in VictoriaMetrics", func() bool {
		for _, l := range e.vm.received() {
			if strings.HasPrefix(l, `node_load1{job="clusterforge",instance="web01",node="web01",node_id="`+web01.ID+`",cluster="lb",cluster_id="`+cl.ID+`",env="prod"} 0.5 `) {
				return true
			}
		}
		return false
	})

	// Beide nodes hebben het VIP: split-brain, en het VIP springt niet heen en weer.
	a2.set("192.0.2.12", "10.99.0.20")
	clusterIs("split-brain", "split_brain", owner("web01"))
	if d.StatusReason != "VIP 10.99.0.20 op web01 en web02" {
		t.Errorf("reden: %q", d.StatusReason)
	}

	// Failover naar web02.
	a1.set("192.0.2.11")
	clusterIs("failover", "healthy", owner("web02"))

	// web01 valt weg: de node is down, het cluster degraded.
	stop1()
	if _, err := e.pool.Exec(context.Background(), "UPDATE node_status SET heartbeat_at = now() - interval '5 minutes' WHERE node_id = $1", web01.ID); err != nil {
		t.Fatal(err)
	}
	e.eval.Kick()
	clusterIs("node down", "degraded", nil)
	if d.StatusReason != "web01: geen heartbeat meer" {
		t.Errorf("reden: %q", d.StatusReason)
	}

	var evs struct {
		Items []struct {
			Action  string         `json:"action"`
			Payload map[string]any `json:"payload"`
		} `json:"items"`
	}
	seen := map[string]bool{}
	eventually(t, "events", func() bool {
		c.do("GET", "/api/v1/events?limit=200", nil, &evs)
		for _, ev := range evs.Items {
			seen[ev.Action+" "+str(ev.Payload["to"])] = true
		}
		return seen["node.status_changed down"] && seen["cluster.status_changed split_brain"] && seen["vip.owner_changed "+web02.ID]
	})
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func TestMetricsAPI(t *testing.T) {
	e, c := adminClient(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "DB", "type": "generic", "environment": "lab"}, &cl)
	var n node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db01", "cluster_id": cl.ID}, &n)

	var m struct {
		Range       string  `json:"range"`
		StepSeconds int     `json:"step_seconds"`
		Timestamps  []int64 `json:"timestamps"`
		Panels      []struct {
			ID     string `json:"id"`
			Unit   string `json:"unit"`
			Series []struct {
				Label  string     `json:"label"`
				Values []*float64 `json:"values"`
			} `json:"series"`
		} `json:"panels"`
	}
	if status := c.do("GET", "/api/v1/nodes/"+n.ID+"/metrics?range=6h", nil, &m); status != http.StatusOK {
		t.Fatalf("node metrics: %d", status)
	}
	if m.Range != "6h" || m.StepSeconds != 90 || len(m.Timestamps) != 241 || len(m.Panels) != 7 {
		t.Fatalf("antwoord: %s %d %d %d", m.Range, m.StepSeconds, len(m.Timestamps), len(m.Panels))
	}
	cpu := m.Panels[0]
	if cpu.ID != "cpu" || cpu.Unit != "percent" || len(cpu.Series) != 2 || cpu.Series[0].Label != "gebruik" {
		t.Fatalf("cpu: %+v", cpu)
	}
	if v := cpu.Series[0].Values[0]; v == nil || *v != 1.5 || cpu.Series[0].Values[1] != nil {
		t.Errorf("waarden: %v %v", cpu.Series[0].Values[0], cpu.Series[0].Values[1])
	}
	e.vm.mu.Lock()
	q := slices.Clone(e.vm.queries)
	e.vm.mu.Unlock()
	if !slices.ContainsFunc(q, func(s string) bool { return strings.Contains(s, `node_id="`+n.ID+`"`) }) {
		t.Errorf("queries: %q", q)
	}

	if status := c.do("GET", "/api/v1/clusters/"+cl.ID+"/metrics", nil, &m); status != http.StatusOK {
		t.Fatalf("cluster metrics: %d", status)
	}
	if m.Range != "1h" || len(m.Panels) != 4 || m.Panels[0].Series[0].Label != "web01" {
		t.Fatalf("cluster: %+v", m.Panels[0])
	}

	var ae apiErr
	if status := c.do("GET", "/api/v1/nodes/"+n.ID+"/metrics?range=2y", nil, &ae); status != http.StatusBadRequest {
		t.Errorf("onbekende periode: %d", status)
	}
	if status := c.do("GET", "/api/v1/nodes/00000000-0000-0000-0000-000000000000/metrics", nil, nil); status != http.StatusNotFound {
		t.Errorf("onbekende node: %d", status)
	}

	var info struct {
		MetricsEnabled bool   `json:"metrics_enabled"`
		GrafanaNodeURL string `json:"grafana_node_url"`
	}
	c.do("GET", "/api/v1/info", nil, &info)
	if !info.MetricsEnabled || info.GrafanaNodeURL == "" {
		t.Errorf("info: %+v", info)
	}
}

func TestStream(t *testing.T) {
	e, c := adminClient(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/stream", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	// Wachten tot de stream luistert, dan iets wijzigen.
	time.Sleep(300 * time.Millisecond)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "live", "name": "Live", "type": "generic", "environment": "lab"}, nil)
	timeout := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream gesloten")
			}
			var ev struct {
				Action string `json:"action"`
			}
			if data, ok := strings.CutPrefix(l, "data: "); ok && json.Unmarshal([]byte(data), &ev) == nil && ev.Action == "cluster.created" {
				return
			}
		case <-timeout:
			t.Fatal("geen change-event ontvangen")
		}
	}
}

func TestStreamNeedsSession(t *testing.T) {
	e := newTestEnv(t)
	resp, err := http.Get(e.srv.URL + "/api/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d", resp.StatusCode)
	}
}
