package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
)

type endpointsView struct {
	ServerURL    string `json:"server_url"`
	ServerSource string `json:"server_source"`
	NatsURL      string `json:"nats_url"`
	NatsSource   string `json:"nats_source"`
	Agents       []struct {
		Hostname  string `json:"hostname"`
		NatsURL   string `json:"nats_url"`
		ServerURL string `json:"server_url"`
		State     string `json:"state"`
		Why       string `json:"why"`
	} `json:"agents"`
}

func (v endpointsView) states() string {
	var s []string
	for _, a := range v.Agents {
		s = append(s, a.Hostname+"="+a.State)
	}
	return strings.Join(s, " ")
}

// confNode is een agent die agent.json echt herschrijft: een herstart start
// hem opnieuw met wat er dan in agent.json staat, en met rollback eerst wat
// de terugvaltimer doet.
type confNode struct {
	t        *testing.T
	root     string
	cfgPath  string
	mu       sync.Mutex
	rollback bool
	stop     func()
	done     chan struct{}
}

func (n *confNode) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		return nil, os.ErrNotExist
	}
	return nil, nil
}

func (n *confNode) start() {
	cfg, err := agent.LoadConfig(n.cfgPath)
	if err != nil {
		n.t.Errorf("agent.json: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: fakeCollector(n.root),
		HeartbeatInterval: 100_000_000, StatePath: filepath.Join(n.root, "var/lib/clusterforge/agent-state.json"),
		ConfigPath: n.cfgPath, BinaryPath: "/bin/true", Exec: n.exec, Restart: n.restart,
	}
	go func() { defer close(done); _ = ag.Run(ctx) }()
	n.mu.Lock()
	n.stop, n.done = cancel, done
	n.mu.Unlock()
}

func (n *confNode) halt() {
	n.mu.Lock()
	stop, done := n.stop, n.done
	n.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

func (n *confNode) restart() {
	n.halt()
	n.mu.Lock()
	rb := n.rollback
	n.mu.Unlock()
	if rb {
		state := filepath.Join(n.root, "var/lib/clusterforge/agent-state.json")
		if err := agent.RollbackReconfigure(context.Background(), n.cfgPath, state, slog.New(slog.NewTextHandler(io.Discard, nil)), n.exec); err != nil {
			n.t.Errorf("terugzetten: %v", err)
		}
	}
	n.start()
}

func startConfNode(t *testing.T, e *testEnv, c *client, nodeID, machineID string) *confNode {
	t.Helper()
	var tok newToken
	if status := c.do("POST", "/api/v1/enrollment-tokens", map[string]any{"node_id": nodeID}, &tok); status != http.StatusCreated {
		t.Fatalf("token: %d", status)
	}
	root := procRoot(t, machineID)
	cfg, err := agent.Enroll(context.Background(), agent.EnrollOptions{ServerURL: e.srv.URL, AllowHTTP: true, Token: tok.Token, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	n := &confNode{t: t, root: root, cfgPath: filepath.Join(root, "etc/clusterforge/agent.json")}
	if err := cfg.Save(n.cfgPath); err != nil {
		t.Fatal(err)
	}
	n.start()
	t.Cleanup(n.halt)
	return n
}

func TestAgentEndpoints(t *testing.T) {
	e, c := adminClient(t)
	var lab cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &lab)
	var web01, web02 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": lab.ID}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": lab.ID}, &web02)
	n1 := startConfNode(t, e, c, web01.ID, strings.Repeat("1", 32))
	startConfNode(t, e, c, web02.ID, strings.Repeat("2", 32))

	// Zonder instelling en zonder CF_NATS_ADVERTISE: het adres van de
	// request; de agents wijzen daar al naar.
	var v endpointsView
	eventually(t, "agents melden hun adres", func() bool {
		c.do("GET", "/api/v1/agent-endpoints", nil, &v)
		return v.states() == "web01=current web02=current"
	})
	if v.ServerSource != "request" || v.ServerURL != "" || v.NatsSource != "request" {
		t.Fatalf("standaard: %+v", v)
	}
	u, _ := url.Parse(e.srv.URL)
	_, natsPort, _ := net.SplitHostPort(strings.TrimPrefix(v.NatsURL, "tls://"))

	// Een ander adres voor dezelfde server: localhost in plaats van 127.0.0.1.
	newServer := "http://localhost:" + u.Port()
	if s := c.do("PUT", "/api/v1/agent-endpoints", map[string]any{"server_url": newServer + "/", "nats_url": "localhost:" + natsPort}, &v); s != http.StatusOK {
		t.Fatalf("instellen: %d", s)
	}
	if v.ServerURL != newServer || v.NatsURL != "tls://localhost:"+natsPort || v.ServerSource != "setting" || v.states() != "web01=differs web02=differs" {
		t.Fatalf("na instellen: %+v", v)
	}
	if s := c.do("PUT", "/api/v1/agent-endpoints", map[string]any{"server_url": "ftp://x", "nats_url": ""}, nil); s != http.StatusBadRequest {
		t.Fatalf("ftp: %d", s)
	}

	// Alles omzetten: één taak voor het cluster, één node tegelijk.
	var started struct {
		Jobs []job `json:"jobs"`
	}
	if s := c.do("POST", "/api/v1/agent-endpoints/apply", map[string]any{"all": true}, &started); s != http.StatusAccepted || len(started.Jobs) != 1 {
		t.Fatalf("omzetten: %d %+v", s, started)
	}
	j := waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "succeeded" || len(j.Steps) != 2 {
		t.Fatalf("taak: %+v", j)
	}
	if log := strings.Join(j.Steps[0].Log, "\n"); !strings.Contains(log, "NATS op tls://localhost:"+natsPort+" getest") || !strings.Contains(log, "terug na") {
		t.Fatalf("log: %s", log)
	}
	c.do("GET", "/api/v1/agent-endpoints", nil, &v)
	if v.states() != "web01=current web02=current" {
		t.Fatalf("na omzetten: %+v", v)
	}
	if cfg, _ := agent.LoadConfig(n1.cfgPath); cfg.NatsURL != "tls://localhost:"+natsPort || cfg.ServerURL != newServer {
		t.Fatalf("agent.json web01: %+v", cfg)
	}
	if s := c.do("POST", "/api/v1/agent-endpoints/apply", map[string]any{"node_ids": []string{web01.ID}}, nil); s != http.StatusConflict {
		t.Fatalf("al omgezet: %d", s)
	}

	// Een adres dat niet werkt: de agent test het, weigert, en blijft.
	if s := c.do("PUT", "/api/v1/agent-endpoints", map[string]any{"server_url": newServer, "nats_url": "127.0.0.1:1"}, &v); s != http.StatusOK {
		t.Fatalf("instellen: %d", s)
	}
	if s := c.do("POST", "/api/v1/agent-endpoints/apply", map[string]any{"all": true}, &started); s != http.StatusAccepted {
		t.Fatalf("omzetten: %d", s)
	}
	j = waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "failed" || !strings.Contains(j.Error, "werkt niet vanaf deze node") || !strings.Contains(j.Error, "oude adres") {
		t.Fatalf("onbereikbaar: %+v", j)
	}
	if cfg, _ := agent.LoadConfig(n1.cfgPath); cfg.NatsURL != "tls://localhost:"+natsPort {
		t.Fatalf("agent.json toch gewijzigd: %+v", cfg)
	}

	// Een adres dat de test doorstaat maar na de herstart niet werkt: de
	// timer zet het oude agent.json terug.
	if s := c.do("PUT", "/api/v1/agent-endpoints", map[string]any{"server_url": e.srv.URL, "nats_url": "127.0.0.1:" + natsPort}, &v); s != http.StatusOK {
		t.Fatalf("instellen: %d", s)
	}
	n1.mu.Lock()
	n1.rollback = true
	n1.mu.Unlock()
	if s := c.do("POST", "/api/v1/agent-endpoints/apply", map[string]any{"node_ids": []string{web01.ID}}, &started); s != http.StatusAccepted {
		t.Fatalf("web01: %d", s)
	}
	j = waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "failed" || !strings.Contains(j.Error, "zette het oude agent.json terug") {
		t.Fatalf("teruggezet: %+v", j)
	}
	eventually(t, "web01 teruggezet", func() bool {
		c.do("GET", "/api/v1/agent-endpoints", nil, &v)
		return v.states() == "web01=failed web02=differs"
	})
	var evs struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/events?limit=200", nil, &evs)
	var acts []string
	for _, ev := range evs.Items {
		acts = append(acts, ev.Action)
	}
	all := strings.Join(acts, " ")
	for _, want := range []string{"settings.agent_endpoints", "node.agent_reconfigured", "node.agent_reconfigure_failed"} {
		if !strings.Contains(all, want) {
			t.Errorf("logboek mist %s: %s", want, all)
		}
	}
}
