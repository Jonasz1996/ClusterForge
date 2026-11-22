package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// dockerNode speelt docker na op een testagent: één project wordpress in
// /docker/wordpress (alleen op de bronnode bestaat het al).
type dockerNode struct {
	mu      sync.Mutex
	root    string
	running bool
	exists  bool
	calls   []string
}

func (n *dockerNode) container() []map[string]any {
	st := "exited"
	if n.running {
		st = "running"
	}
	return []map[string]any{{
		"Id": "abcdef0123456789", "Name": "/wordpress", "Image": "sha256:1", "RestartCount": 0,
		"State":  map[string]any{"Status": st, "StartedAt": "2026-11-16T03:00:00Z"},
		"Config": map[string]any{"Image": "wordpress:latest", "Labels": map[string]string{"com.docker.compose.project": "wordpress", "com.docker.compose.project.working_dir": "/docker/wordpress", "com.docker.compose.project.config_files": "/docker/wordpress/docker-compose.yaml", "com.docker.compose.service": "wordpress"}},
		"Mounts": []map[string]any{
			{"Type": "bind", "Source": "/docker/wordpress/html", "Destination": "/var/www/html", "RW": true},
			{"Type": "bind", "Source": "/var/run/docker.sock", "Destination": "/var/run/docker.sock", "RW": true},
		},
		"HostConfig":      map[string]any{"NetworkMode": "wordpress_default", "RestartPolicy": map[string]any{"Name": "unless-stopped"}},
		"NetworkSettings": map[string]any{"Ports": map[string]any{"80/tcp": []map[string]any{{"HostIp": "0.0.0.0", "HostPort": "8086"}}}},
	}}
}

func verbOf(argv []string) string {
	if len(argv) < 3 || argv[1] != "compose" {
		return ""
	}
	for i := 2; i < len(argv); i++ {
		switch argv[i] {
		case "-p", "-f", "--env-file", "--project-directory":
			i++
			continue
		}
		return strings.Join(argv[i:], " ")
	}
	return ""
}

func (n *dockerNode) run(argv []string) ([]byte, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := strings.Join(argv, " ")
	n.calls = append(n.calls, c)
	verb := verbOf(argv)
	switch {
	case strings.HasPrefix(c, "docker ps -a --no-trunc --format"):
		if !n.exists {
			return nil, 0
		}
		return []byte("abcdef0123456789\tUp 4 days\n"), 0
	case strings.HasPrefix(c, "docker ps -a -q"):
		if !n.exists {
			return nil, 0
		}
		return []byte("abcdef0123456789\n"), 0
	case strings.HasPrefix(c, "docker inspect"):
		if !n.exists {
			return []byte("[]"), 0
		}
		b, _ := json.Marshal(n.container())
		return b, 0
	case verb == "config --format json":
		return []byte(`{"services":{"wordpress":{"env_file":[{"path":"/docker/wordpress/.env"}]}}}`), 0
	case verb == "config --services":
		return []byte("wordpress\n"), 0
	case verb == "config -q":
		return nil, 0
	case strings.HasPrefix(verb, "up -d"):
		n.exists, n.running = true, true
		return nil, 0
	case strings.HasPrefix(verb, "stop"), strings.HasPrefix(verb, "down"):
		n.running = false
		return nil, 0
	case strings.HasPrefix(verb, "pull"), strings.HasPrefix(verb, "restart"), strings.HasPrefix(verb, "logs"):
		return nil, 0
	case strings.HasPrefix(c, "docker logs"):
		return []byte("2026-11-16T03:00:00Z WORDPRESS_DB_PASSWORD=supergeheim ready\n"), 0
	}
	return nil, 0
}

func (n *dockerNode) did(s string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.calls {
		if strings.Contains(c, s) {
			return true
		}
	}
	return false
}

func startDockerNode(t *testing.T, e *testEnv, c *client, nodeID, machineID string, addrs ...string) *dockerNode {
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
	n := &dockerNode{root: root}
	ex := &agent.RunExecutor{Root: root, Stderr: io.Discard, Stable: time.Millisecond, Poll: 5 * time.Millisecond,
		Cmd: func(_ context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
			data, code := n.run(argv)
			_, _ = out.Write(data)
			return code, nil
		}}
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: fakeCollector(root, addrs...),
		HeartbeatInterval: 100_000_000, StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"), Root: root,
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			data, _ := n.run(append([]string{name}, args...))
			return data, nil
		},
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

func writeStackFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestStacksEndToEnd(t *testing.T) {
	e, c := adminClient(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "docker-dubbel", "name": "DockerDubbel", "type": "keepalived", "environment": "lab"}, &cl)
	if s := c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.106.165"}, nil); s != http.StatusCreated {
		t.Fatalf("vip: %d", s)
	}
	var n1, n2 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "docker-dubbel1", "cluster_id": cl.ID}, &n1)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "docker-dubbel2", "cluster_id": cl.ID}, &n2)
	d1 := startDockerNode(t, e, c, n1.ID, strings.Repeat("d", 32), "10.0.106.165")
	d2 := startDockerNode(t, e, c, n2.ID, strings.Repeat("e", 32))
	d1.exists, d1.running = true, true
	writeStackFile(t, filepath.Join(d1.root, "docker/wordpress/docker-compose.yaml"), "services:\n  wordpress:\n    image: wordpress:latest\n    env_file: .env\n")
	writeStackFile(t, filepath.Join(d1.root, "docker/wordpress/.env"), "WORDPRESS_DB_PASSWORD=supergeheim\nWORDPRESS_DB_NAME=wordpress\n")
	writeStackFile(t, filepath.Join(d1.root, "docker/wordpress/wp.sqlite"), "data")

	type ovT struct {
		Cluster struct {
			VIP string `json:"vip"`
		} `json:"cluster"`
		Nodes []struct {
			Hostname  string `json:"hostname"`
			HoldsVIP  bool   `json:"holds_vip"`
			Docker    bool   `json:"docker"`
			Supported bool   `json:"supported"`
			Error     string `json:"error"`
		} `json:"nodes"`
		Stacks []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Placement string `json:"placement"`
			Nodes     []struct {
				Hostname   string `json:"hostname"`
				Should     bool   `json:"should"`
				Revision   int    `json:"revision"`
				Containers []any  `json:"containers"`
			} `json:"nodes"`
		} `json:"stacks"`
		Unmanaged []struct {
			Hostname  string   `json:"hostname"`
			SyncPaths []string `json:"sync_paths"`
			Project   struct {
				Name string `json:"name"`
			} `json:"project"`
		} `json:"unmanaged"`
		Available bool `json:"available"`
	}
	var ov ovT
	eventually(t, "twee nodes met Docker en het VIP op dubbel1", func() bool {
		c.do("GET", "/api/v1/clusters/"+cl.ID+"/stacks", nil, &ov)
		return len(ov.Nodes) == 2 && ov.Nodes[0].Docker && ov.Nodes[1].Docker && ov.Nodes[0].HoldsVIP && len(ov.Unmanaged) == 1
	})
	if !ov.Available || ov.Unmanaged[0].Project.Name != "wordpress" || strings.Join(ov.Unmanaged[0].SyncPaths, ",") != "/docker/wordpress" {
		t.Fatalf("overzicht %+v", ov)
	}

	// Overnemen: op de node verandert niets.
	var imp struct {
		Results []struct {
			Name    string   `json:"name"`
			StackID *string  `json:"stack_id"`
			Files   []string `json:"files"`
			Skipped []string `json:"skipped"`
			Error   string   `json:"error"`
		} `json:"results"`
	}
	if s := c.do("POST", "/api/v1/clusters/"+cl.ID+"/stacks/import", map[string]any{"node_id": n1.ID, "items": []map[string]any{{"name": "wordpress"}}}, &imp); s != http.StatusOK {
		t.Fatalf("import: %d", s)
	}
	r := imp.Results[0]
	if r.Error != "" || r.StackID == nil || strings.Join(r.Files, ",") != ".env,docker-compose.yaml" || len(r.Skipped) != 1 {
		t.Fatalf("import %+v", r)
	}
	if d1.did("up -d") || d1.did("stop") {
		t.Fatal("overnemen veranderde iets op de node")
	}
	id := *r.StackID

	// De editor toont plaatshouders; opslaan zet de echte waarde terug.
	var det struct {
		Stack struct {
			Placement string `json:"placement"`
			Revision  int    `json:"revision"`
		} `json:"stack"`
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Secrets int    `json:"secrets"`
		} `json:"files"`
	}
	c.do("GET", "/api/v1/stacks/"+id, nil, &det)
	if det.Stack.Placement != "vip" || det.Files[0].Path != ".env" || strings.Contains(det.Files[0].Content, "supergeheim") || det.Files[0].Secrets != 1 {
		t.Fatalf("detail %+v", det)
	}
	files := []map[string]any{
		{"path": ".env", "content": det.Files[0].Content + "WP_DEBUG=0\n"},
		{"path": "docker-compose.yaml", "content": det.Files[1].Content},
	}
	var saved struct {
		Revision int  `json:"revision"`
		Critical bool `json:"critical"`
	}
	if s := c.do("PUT", "/api/v1/stacks/"+id, map[string]any{"files": files, "critical": true, "note": "WP_DEBUG"}, &saved); s != http.StatusOK || saved.Revision != 2 || !saved.Critical {
		t.Fatalf("opslaan: %d %+v", s, saved)
	}
	if s := c.do("PUT", "/api/v1/stacks/"+id, map[string]any{"files": []map[string]any{{"path": ".env", "content": "X=«geheim-7»"}, files[1]}}, nil); s != http.StatusBadRequest {
		t.Fatalf("onbekende plaatshouder: %d", s)
	}

	// Toepassen: dubbel2 (reserve) krijgt de bestanden en stopt, dubbel1
	// (VIP) draait; allebei krijgen de lijst voor keepalived.
	var job struct {
		JobID string `json:"job_id"`
	}
	if s := c.do("POST", "/api/v1/clusters/"+cl.ID+"/stacks/jobs", map[string]any{"action": "apply"}, &job); s != http.StatusAccepted {
		t.Fatalf("toepassen: %d", s)
	}
	waitStackJob(t, c, job.JobID, "succeeded")
	env2, err := os.ReadFile(filepath.Join(d2.root, "docker/wordpress/.env"))
	if err != nil || !strings.Contains(string(env2), "WORDPRESS_DB_PASSWORD=supergeheim") || !strings.Contains(string(env2), "WP_DEBUG=0") {
		t.Fatalf(".env op dubbel2: %q %v", env2, err)
	}
	if d2.did("up -d") || !d2.did("stop") || !d1.did("up -d") {
		t.Fatalf("calls dubbel1 %v / dubbel2 %v", d1.calls, d2.calls)
	}
	for _, d := range []*dockerNode{d1, d2} {
		data, _ := os.ReadFile(filepath.Join(d.root, protocol.StackListFile))
		l := protocol.ParseStackList(string(data))
		if len(l) != 1 || l[0].Placement != "vip" || !l[0].Critical {
			t.Fatalf("lijst %q", data)
		}
	}
	var syncName string
	if err := e.pool.QueryRow(context.Background(), `SELECT name FROM cron_jobs WHERE cluster_id = $1 AND sync_before LIKE 'cf-agent stack-pause wordpress'`, cl.ID).Scan(&syncName); err != nil || syncName != "cf-stack-wordpress" {
		t.Fatalf("sync: %q %v", syncName, err)
	}
	c.do("GET", "/api/v1/clusters/"+cl.ID+"/stacks", nil, &ov)
	if len(ov.Stacks) != 1 || len(ov.Unmanaged) != 0 || ov.Stacks[0].Nodes[0].Revision != 2 || !ov.Stacks[0].Nodes[0].Should || ov.Stacks[0].Nodes[1].Should {
		t.Fatalf("na toepassen %+v", ov.Stacks)
	}

	// Logs met gemaskeerde geheimen.
	var logs struct {
		Logs string `json:"logs"`
	}
	if s := c.do("GET", "/api/v1/clusters/"+cl.ID+"/stacks/logs?node_id="+n1.ID+"&container=wordpress", nil, &logs); s != http.StatusOK || strings.Contains(logs.Logs, "supergeheim") || logs.Logs == "" {
		t.Fatalf("logs %d %q", s, logs.Logs)
	}

	// Loslaten: de lijst wordt leeg en de sync gaat weg; de containers blijven.
	var del struct {
		JobID *string `json:"job_id"`
	}
	if s := c.do("DELETE", "/api/v1/stacks/"+id, nil, &del); s != http.StatusOK || del.JobID == nil {
		t.Fatalf("loslaten: %d", s)
	}
	waitStackJob(t, c, *del.JobID, "succeeded")
	data, _ := os.ReadFile(filepath.Join(d2.root, protocol.StackListFile))
	if len(protocol.ParseStackList(string(data))) != 0 {
		t.Fatalf("lijst na loslaten %q", data)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM cron_jobs WHERE cluster_id = $1 AND name LIKE 'cf-stack-%'`, cl.ID).Scan(&n)
	if n != 0 || !d1.running {
		t.Fatalf("syncs %d, wordpress draait %v", n, d1.running)
	}
}

func waitStackJob(t *testing.T, c *client, id, want string) {
	t.Helper()
	j := waitJobLong(t, c, id)
	if j.Status != want {
		t.Fatalf("taak %s: %s (%s) %+v", id, j.Status, j.Error, j.Steps)
	}
}
