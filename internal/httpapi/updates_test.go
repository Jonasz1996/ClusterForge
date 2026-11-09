package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// runNode is een testagent die runs (fase 10) in zijn eigen proces doet,
// met nagespeelde apt, docker en scripts.
type runNode struct {
	mu    sync.Mutex
	calls []string
	// fail laat een script met deze tekst mislukken.
	fail string
}

func (n *runNode) cmd(_ context.Context, user string, argv, _ []string, out io.Writer) (int, error) {
	c := strings.Join(argv, " ")
	n.mu.Lock()
	n.calls = append(n.calls, c)
	fail := n.fail
	n.mu.Unlock()
	switch {
	case strings.HasPrefix(c, "apt-get upgrade"):
		_, _ = io.WriteString(out, "Setting up openssl (3.5.4-1~deb13u1) ...\n")
	case argv[0] == "/bin/sh":
		_, _ = io.WriteString(out, "draait als "+user+"\nexport PGPASSWORD=geheim\n")
		if fail != "" {
			return 4, nil
		}
	}
	return 0, nil
}

func (n *runNode) got(prefix string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, x := range n.calls {
		if strings.HasPrefix(x, prefix) {
			c++
		}
	}
	return c
}

func startRunNode(t *testing.T, e *testEnv, c *client, nodeID, machineID string, addrs ...string) *runNode {
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
	n := &runNode{}
	ex := &agent.RunExecutor{Root: root, Cmd: n.cmd, Stderr: io.Discard}
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: fakeCollector(root, addrs...),
		HeartbeatInterval: 100_000_000, StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"), Root: root,
		Exec: func(context.Context, string, ...string) ([]byte, error) { return []byte("inactive"), nil },
		StartRun: func(_ context.Context, id string) error {
			go ex.Main(context.Background(), id)
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = ag.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return n
}

func TestUpdatesAndCommands(t *testing.T) {
	e, c := adminClient(t)
	var lab cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &lab)
	c.do("POST", "/api/v1/clusters/"+lab.ID+"/vips", map[string]any{"address": "10.0.10.100"}, nil)
	var web01, web02 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": lab.ID}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": lab.ID}, &web02)
	// web01 heeft het VIP en komt dus als laatste.
	n1 := startRunNode(t, e, c, web01.ID, strings.Repeat("1", 32), "192.0.2.11", "10.0.10.100")
	n2 := startRunNode(t, e, c, web02.ID, strings.Repeat("2", 32), "192.0.2.12")

	var ov struct {
		RunProtocol int `json:"run_protocol"`
		Nodes       []struct {
			Hostname  string `json:"hostname"`
			Supported bool   `json:"supported"`
			Online    bool   `json:"online"`
			VIPHolder bool   `json:"vip_holder"`
		} `json:"nodes"`
	}
	eventually(t, "twee nodes die kunnen bijwerken", func() bool {
		c.do("GET", "/api/v1/updates", nil, &ov)
		return len(ov.Nodes) == 2 && ov.Nodes[0].Online && ov.Nodes[1].Online && ov.Nodes[0].Supported && ov.Nodes[0].VIPHolder
	})
	if ov.RunProtocol != protocol.RunSince {
		t.Fatalf("%+v", ov)
	}

	// Niets gekozen, of nodes en cluster samen, kan niet.
	if s := c.do("POST", "/api/v1/updates", map[string]any{"cluster_id": lab.ID}, nil); s != http.StatusBadRequest {
		t.Fatalf("niets gekozen: %d", s)
	}
	var started struct {
		Jobs    []job    `json:"jobs"`
		Skipped []string `json:"skipped"`
	}
	if s := c.do("POST", "/api/v1/updates", map[string]any{"cluster_id": lab.ID, "apt": map[string]any{"autoremove": true}}, &started); s != http.StatusAccepted || len(started.Jobs) != 1 {
		t.Fatalf("bijwerken: %d %+v", s, started)
	}
	j := waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "succeeded" {
		t.Fatalf("taak: %+v", j)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	want := "web02: voorbereiden|web02: pakketten (apt)|web02: gezond terug|web01 (VIP-houder): voorbereiden|web01 (VIP-houder): pakketten (apt)|web01 (VIP-houder): gezond terug"
	if got := strings.Join(names, "|"); got != want {
		t.Fatalf("stappen:\n%s\nwil\n%s", got, want)
	}
	if log := strings.Join(j.Steps[1].Log, "\n"); !strings.Contains(log, "Setting up openssl") || !strings.Contains(log, "$ apt-get autoremove -y -q") {
		t.Fatalf("log: %s", log)
	}
	if n1.got("apt-get upgrade") != 1 || n2.got("apt-get upgrade") != 1 || n1.got("apt-mark hold") != 0 {
		t.Fatalf("apt: %v %v", n1.calls, n2.calls)
	}

	// Een eigen commando: maken, wijzigen (nieuwe revisie), draaien.
	script := "grep -v -e '^HANodeName' /etc/zabbix/cf-zabbix_server.conf > /root/zbx-upgrade.conf && zabbix_server -f -c /root/zbx-upgrade.conf"
	var cmd struct {
		ID        string `json:"id"`
		Revision  int    `json:"revision"`
		Revisions []struct {
			Revision int    `json:"revision"`
			Sha256   string `json:"sha256"`
		} `json:"revisions"`
		Runs []job `json:"runs"`
	}
	if s := c.do("POST", "/api/v1/commands", map[string]any{"name": "Zabbix-database omzetten", "script": script}, &cmd); s != http.StatusCreated || cmd.Revision != 1 {
		t.Fatalf("maken: %d %+v", s, cmd)
	}
	if s := c.do("POST", "/api/v1/commands", map[string]any{"name": "zabbix-database omzetten", "script": "true"}, nil); s != http.StatusBadRequest {
		t.Fatalf("dubbele naam: %d", s)
	}
	if s := c.do("POST", "/api/v1/commands", map[string]any{"name": "x", "script": "true", "run_as": "root; rm"}, nil); s != http.StatusBadRequest {
		t.Fatalf("gebruiker: %d", s)
	}
	if s := c.do("PUT", "/api/v1/commands/"+cmd.ID, map[string]any{"name": "Zabbix-database omzetten", "script": script + "\n", "description": "eenmalig"}, &cmd); s != http.StatusOK || cmd.Revision != 2 || len(cmd.Revisions) != 2 {
		t.Fatalf("wijzigen: %d %+v", s, cmd)
	}
	var run job
	if s := c.do("POST", "/api/v1/commands/"+cmd.ID+"/run", map[string]any{"node_ids": []string{web01.ID, web02.ID}}, &run); s != http.StatusAccepted {
		t.Fatalf("draaien: %d", s)
	}
	j = waitJob(t, c, run.ID)
	if j.Status != "succeeded" || len(j.Steps) != 2 || j.Steps[0].Name != "web02" || j.Steps[1].Name != "web01 (VIP-houder)" {
		t.Fatalf("run: %+v", j)
	}
	if log := strings.Join(j.Steps[0].Log, "\n"); !strings.Contains(log, "draait als root") || !strings.Contains(log, "revisie 2") || strings.Contains(log, "geheim") {
		t.Fatalf("log: %s", log)
	}

	// Mislukt op de eerste node: de tweede draait niet.
	n2.mu.Lock()
	n2.fail = "ja"
	n2.mu.Unlock()
	before := n1.got("/bin/sh")
	c.do("POST", "/api/v1/commands/"+cmd.ID+"/run", map[string]any{"cluster_id": lab.ID}, &run)
	j = waitJob(t, c, run.ID)
	if j.Status != "failed" || !strings.Contains(j.Error, "exitcode 4") || !strings.Contains(j.Error, "niet gedraaid") || n1.got("/bin/sh") != before {
		t.Fatalf("mislukt: %+v", j)
	}
	// Met verder bij een fout draait web01 wel.
	c.do("POST", "/api/v1/commands/"+cmd.ID+"/run", map[string]any{"cluster_id": lab.ID, "continue_on_error": true}, &run)
	j = waitJob(t, c, run.ID)
	if j.Status != "failed" || !strings.Contains(j.Error, "mislukt op web02") || n1.got("/bin/sh") != before+1 {
		t.Fatalf("verder: %+v", j)
	}
	c.do("GET", "/api/v1/commands/"+cmd.ID, nil, &cmd)
	if len(cmd.Runs) != 3 {
		t.Fatalf("runs: %+v", cmd.Runs)
	}
	var list struct {
		Items []struct {
			Name    string `json:"name"`
			LastRun *job   `json:"last_run"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/commands", nil, &list)
	if len(list.Items) != 1 || list.Items[0].LastRun == nil || list.Items[0].LastRun.ID != run.ID {
		t.Fatalf("lijst: %+v", list)
	}
	if s := c.do("DELETE", "/api/v1/commands/"+cmd.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("weghalen: %d", s)
	}
	// Een viewer mag niets draaien.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("POST", "/api/v1/updates", map[string]any{"cluster_id": lab.ID, "apt": map[string]any{}}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer: %d", s)
	}
}
