package httpapi

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// fwNode speelt ufw na: allow en delete veranderen user.rules onder root.
type fwNode struct {
	mu     sync.Mutex
	root   string
	active bool
	calls  []string
}

func (n *fwNode) rulesPath() string { return filepath.Join(n.root, "etc/ufw/user.rules") }

func (n *fwNode) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := strings.Join(append([]string{name}, args...), " ")
	n.calls = append(n.calls, c)
	switch {
	case c == "ufw version":
		return []byte("ufw 0.36.2\n"), nil
	case c == "ufw status":
		if n.active {
			return []byte("Status: active\n"), nil
		}
		return []byte("Status: inactive\n"), nil
	case c == "ufw --force enable":
		n.active = true
		return []byte("Firewall is active\n"), nil
	case name == "ufw" && len(args) > 0 && (args[0] == "allow" || args[0] == "delete"):
		del := args[0] == "delete"
		if del {
			args = args[1:]
		}
		proto, from, port, comment := "any", "any", "any", ""
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "proto":
				proto = args[i+1]
			case "from":
				from = args[i+1]
			case "port":
				port = args[i+1]
			case "comment":
				comment = args[i+1]
			}
		}
		if from == "any" {
			from = "0.0.0.0/0"
		}
		tuple := "### tuple ### allow " + proto + " " + port + " 0.0.0.0/0 any " + from + " in"
		b, _ := os.ReadFile(n.rulesPath())
		var keep []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" && !strings.HasPrefix(l, tuple) {
				keep = append(keep, l)
			}
		}
		if !del {
			if comment != "" {
				tuple += " comment=" + hex.EncodeToString([]byte(comment))
			}
			keep = append(keep, tuple)
		}
		_ = os.WriteFile(n.rulesPath(), []byte(strings.Join(keep, "\n")+"\n"), 0o644)
		return []byte("Rule added\n"), nil
	case strings.HasPrefix(c, "docker"):
		return nil, io.ErrUnexpectedEOF
	}
	return nil, nil
}

func startFirewallNode(t *testing.T, e *testEnv, c *client, nodeID, machineID string) *fwNode {
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
	for f, body := range map[string]string{
		"usr/sbin/ufw": "", "etc/ufw/user.rules": "", "etc/ufw/user6.rules": "", "etc/ufw/ufw.conf": "ENABLED=no\n",
		"etc/default/ufw": "DEFAULT_INPUT_POLICY=\"DROP\"\nDEFAULT_OUTPUT_POLICY=\"ACCEPT\"\n",
	} {
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755)
		if err := os.WriteFile(filepath.Join(root, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n := &fwNode{root: root}
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: fakeCollector(root),
		HeartbeatInterval: 100_000_000, StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"), Root: root,
		Exec:      n.exec,
		OpenNflog: func(context.Context, uint16, func([]byte)) error { return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = ag.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return n
}

func TestFirewallEndToEnd(t *testing.T) {
	e, c := adminClient(t)
	var lab cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "edge", "name": "Edge", "type": "keepalived", "environment": "lab"}, &lab)
	var web01 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": lab.ID}, &web01)
	n := startFirewallNode(t, e, c, web01.ID, strings.Repeat("f", 32))
	e.api.firewall.CheckDelay = 0

	type fwNodeOut struct {
		Online      bool   `json:"online"`
		Supported   bool   `json:"supported"`
		Mode        string `json:"mode"`
		PlanHash    string `json:"plan_hash"`
		AppliedHash string `json:"applied_hash"`
		ApplyError  string `json:"apply_error"`
		Status      *struct {
			Active bool `json:"active"`
		} `json:"status"`
		Diff *struct {
			Add []struct {
				Port string `json:"port"`
			} `json:"add"`
		} `json:"diff"`
	}
	var ov struct {
		Nodes []fwNodeOut `json:"nodes"`
	}
	eventually(t, "node met een agent die de firewall kent", func() bool {
		c.do("GET", "/api/v1/firewall", nil, &ov)
		return len(ov.Nodes) == 1 && ov.Nodes[0].Online && ov.Nodes[0].Supported
	})
	if s := c.do("POST", "/api/v1/firewall/nodes", map[string]any{"mode": "managed"}, nil); s != http.StatusBadRequest {
		t.Fatalf("niets gekozen: %d", s)
	}
	if s := c.do("POST", "/api/v1/firewall/nodes", map[string]any{"mode": "preview", "cluster_id": lab.ID}, nil); s != http.StatusOK {
		t.Fatalf("voorbeeld: %d", s)
	}
	var fresh fwNodeOut
	if s := c.do("POST", "/api/v1/firewall/nodes/"+web01.ID+"/refresh", nil, &fresh); s != http.StatusOK {
		t.Fatalf("vernieuwen: %d", s)
	}
	if fresh.Mode != "preview" || fresh.Diff == nil || len(fresh.Diff.Add) == 0 || fresh.Diff.Add[0].Port != "22" {
		t.Fatalf("voorbeeld: %+v", fresh)
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	if s := c.do("POST", "/api/v1/firewall/nodes/"+web01.ID+"/apply", nil, &started); s != http.StatusAccepted {
		t.Fatalf("toepassen: %d", s)
	}
	if j := waitJob(t, c, started.JobID); j.Status != "succeeded" {
		t.Fatalf("taak %+v", j)
	}
	n.mu.Lock()
	calls := strings.Join(n.calls, "\n")
	n.mu.Unlock()
	for _, want := range []string{"systemd-run --unit=cf-firewall-rollback", "ufw allow proto tcp from 192.168.0.0/16 to any port 22 comment cf:SSH",
		"ufw --force enable", "systemctl stop cf-firewall-rollback.timer"} {
		if !strings.Contains(calls, want) {
			t.Errorf("geen %q in\n%s", want, calls)
		}
	}
	var d struct {
		Node fwNodeOut `json:"node"`
	}
	c.do("GET", "/api/v1/firewall/nodes/"+web01.ID, nil, &d)
	if d.Node.ApplyError != "" || d.Node.AppliedHash == "" || d.Node.AppliedHash != d.Node.PlanHash || d.Node.Status == nil || !d.Node.Status.Active {
		t.Fatalf("na toepassen: %+v", d.Node)
	}

	if s := c.do("PUT", "/api/v1/firewall/settings", map[string]any{"auto_apply": true, "lan": []string{"nee"}, "admin": []string{},
		"ssh_from": "lan", "unknown_ports": "lan", "strict": true, "confirm_seconds": 120, "grace_hours": 24, "monitor": true}, nil); s != http.StatusBadRequest {
		t.Fatalf("ongeldig netwerk: %d", s)
	}
	if s := c.do("POST", "/api/v1/firewall/rules", map[string]any{"action": "open", "proto": "tcp", "port": "8443", "source": "cluster", "cluster_id": lab.ID}, nil); s != http.StatusCreated {
		t.Fatalf("regel: %d", s)
	}
	var traffic struct {
		Items  []any `json:"items"`
		Hourly []any `json:"hourly"`
	}
	if s := c.do("GET", "/api/v1/firewall/traffic?hours=24", nil, &traffic); s != http.StatusOK || len(traffic.Hourly) != 24 {
		t.Fatalf("verkeer: %d %d", s, len(traffic.Hourly))
	}

	e.createUser("viewer", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	viewer.login("viewer", "een-lang-wachtwoord", "")
	if s := viewer.do("POST", "/api/v1/firewall/nodes/"+web01.ID+"/apply", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer past toe: %d", s)
	}
	if s := viewer.do("GET", "/api/v1/firewall", nil, nil); s != http.StatusOK {
		t.Fatalf("viewer leest: %d", s)
	}
}
