package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// csNode is een testagent met nagespeelde CrowdSec: na het script van
// ClusterForge staat cscli er en geeft de node alerts en bans.
type csNode struct {
	mu      sync.Mutex
	root    string
	script  string
	decided []string
}

var planHash = regexp.MustCompile(`plan ([0-9a-f]{16})`)

func (n *csNode) cmd(_ context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
	if !strings.HasSuffix(argv[0], "/script") {
		return 0, nil
	}
	body, err := os.ReadFile(argv[0])
	if err != nil {
		return 1, err
	}
	n.mu.Lock()
	n.script = string(body)
	n.mu.Unlock()
	if !strings.Contains(string(body), "CrowdSec toepassen") {
		return 0, nil
	}
	m := planHash.FindStringSubmatch(string(body))
	for _, f := range []string{"usr/bin/cscli", "usr/bin/crowdsec-firewall-bouncer"} {
		_ = os.MkdirAll(filepath.Join(n.root, filepath.Dir(f)), 0o755)
		_ = os.WriteFile(filepath.Join(n.root, f), nil, 0o755)
	}
	_ = os.MkdirAll(filepath.Join(n.root, "var/lib/clusterforge"), 0o755)
	_ = os.WriteFile(filepath.Join(n.root, "var/lib/clusterforge/crowdsec.json"),
		[]byte(`{"hash":"`+m[1]+`","collections":["crowdsecurity/linux"],"applied_at":"2026-11-15T20:00:00Z"}`), 0o644)
	_, _ = io.WriteString(out, "Klaar: plan "+m[1]+" toegepast.\n")
	return 0, nil
}

func (n *csNode) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	c := strings.Join(append([]string{name}, args...), " ")
	now := time.Now().UTC()
	switch {
	case c == "cscli version":
		return []byte("version: v1.6.4-debian-pragmatic-amd64\n"), nil
	case c == "cscli collections list -o json":
		return []byte(`{"collections":[{"name":"crowdsecurity/linux","status":"enabled","local_version":"0.4"}]}`), nil
	case c == "cscli bouncers list -o json":
		return []byte(`[{"name":"cs-firewall-bouncer","ip_address":"127.0.0.1","valid":true,"type":"crowdsec-firewall-bouncer"}]`), nil
	case c == "cscli metrics -o json":
		return []byte(`{"acquisition":{"journalctl:ssh":{"reads":10,"parsed":10}},"decisions":{"x":{"CAPI":{"ban":12000}}}}`), nil
	case strings.HasPrefix(c, "cscli alerts list"):
		return fmt.Appendf(nil, `[{"id":3,"scenario":"crowdsecurity/ssh-bf","events_count":6,"start_at":%q,"stop_at":%q,
			"source":{"scope":"Ip","value":"203.0.113.50","cn":"RU"},"decisions":[{"id":9,"origin":"crowdsec"}]}]`,
			now.Add(-time.Minute).Format(time.RFC3339), now.Format(time.RFC3339)), nil
	case c == "cscli decisions list -o json":
		return []byte(`[{"id":3,"source":{"scope":"Ip","value":"203.0.113.50","cn":"RU"},
			"decisions":[{"id":9,"origin":"crowdsec","type":"ban","scope":"Ip","value":"203.0.113.50","duration":"3h50m","scenario":"crowdsecurity/ssh-bf"}]}]`), nil
	case strings.HasPrefix(c, "cscli decisions"):
		n.mu.Lock()
		n.decided = append(n.decided, c)
		n.mu.Unlock()
		return []byte("1 decision(s) deleted\n"), nil
	case strings.HasPrefix(c, "systemctl is-active"):
		return []byte("active\n"), nil
	case strings.HasPrefix(c, "docker"):
		return nil, fmt.Errorf("geen docker")
	}
	return []byte("inactive"), nil
}

func startCrowdSecNode(t *testing.T, e *testEnv, c *client, nodeID, machineID string) *csNode {
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
	n := &csNode{root: root}
	ex := &agent.RunExecutor{Root: root, Cmd: n.cmd, Stderr: io.Discard}
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: fakeCollector(root),
		HeartbeatInterval: 100_000_000, StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"), Root: root,
		Exec: n.exec,
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

func TestCrowdSecEndToEnd(t *testing.T) {
	e, c := adminClient(t)
	var lab cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "edge", "name": "Edge", "type": "keepalived", "environment": "lab"}, &lab)
	var web01 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": lab.ID}, &web01)
	n := startCrowdSecNode(t, e, c, web01.ID, strings.Repeat("c", 32))

	type csNodeOut struct {
		Hostname    string `json:"hostname"`
		Online      bool   `json:"online"`
		Supported   bool   `json:"supported"`
		Enabled     bool   `json:"enabled"`
		PlanHash    string `json:"plan_hash"`
		AppliedHash string `json:"applied_hash"`
		Status      *struct {
			Installed bool   `json:"installed"`
			Version   string `json:"version"`
		} `json:"status"`
	}
	var ov struct {
		Stats map[string]int `json:"stats"`
		Nodes []csNodeOut    `json:"nodes"`
	}
	eventually(t, "node met een agent die CrowdSec kent", func() bool {
		c.do("GET", "/api/v1/crowdsec", nil, &ov)
		return len(ov.Nodes) == 1 && ov.Nodes[0].Online && ov.Nodes[0].Supported
	})

	if s := c.do("POST", "/api/v1/crowdsec/nodes", map[string]any{"managed": true}, nil); s != http.StatusBadRequest {
		t.Fatalf("niets gekozen: %d", s)
	}
	if s := c.do("POST", "/api/v1/crowdsec/nodes", map[string]any{"managed": true, "cluster_id": lab.ID}, nil); s != http.StatusOK {
		t.Fatalf("aanzetten: %d", s)
	}
	// Aanzetten draait meteen een ronde: stand, plan, toepassen.
	eventually(t, "CrowdSec toegepast", func() bool {
		var d struct {
			Node csNodeOut `json:"node"`
		}
		c.do("GET", "/api/v1/crowdsec/nodes/"+web01.ID, nil, &d)
		if d.Node.PlanHash != "" && d.Node.PlanHash != d.Node.AppliedHash {
			_ = e.api.crowdsec.Tick(context.Background())
		}
		return d.Node.Enabled && d.Node.PlanHash != "" && d.Node.PlanHash == d.Node.AppliedHash
	})
	n.mu.Lock()
	script := n.script
	n.mu.Unlock()
	if !strings.Contains(script, "crowdsecurity/linux") || !strings.Contains(script, "crowdsec-firewall-bouncer-nftables") {
		t.Fatalf("script:\n%s", script)
	}

	// Volgende ronde: alerts en de ban komen binnen.
	if _, err := e.pool.Exec(context.Background(), `UPDATE crowdsec_nodes SET status_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := e.api.crowdsec.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var alerts struct {
		Items []struct {
			Value    string `json:"value"`
			Hostname string `json:"hostname"`
			Country  string `json:"country"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/crowdsec/alerts?cluster_id="+lab.ID, nil, &alerts)
	if len(alerts.Items) != 1 || alerts.Items[0].Value != "203.0.113.50" || alerts.Items[0].Country != "RU" {
		t.Fatalf("alerts %+v", alerts)
	}
	var bans struct {
		Items []struct {
			Value string   `json:"value"`
			Nodes []string `json:"nodes"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/crowdsec/decisions", nil, &bans)
	if len(bans.Items) != 1 || bans.Items[0].Nodes[0] != "web01" {
		t.Fatalf("bans %+v", bans)
	}
	c.do("GET", "/api/v1/crowdsec", nil, &ov)
	if ov.Stats["alerts_24h"] != 1 || ov.Stats["blocklist"] != 12000 || ov.Stats["running"] != 1 {
		t.Fatalf("stats %+v", ov.Stats)
	}

	// Deblokkeren en blokkeren via de pagina; viewers mogen het niet.
	var res struct {
		Nodes  []string `json:"nodes"`
		Errors []string `json:"errors"`
	}
	if s := c.do("POST", "/api/v1/crowdsec/decisions", map[string]any{"delete": true, "scope": "ip", "value": "203.0.113.50"}, &res); s != http.StatusOK || len(res.Nodes) != 1 {
		t.Fatalf("deblokkeren: %d %+v", s, res)
	}
	if s := c.do("POST", "/api/v1/crowdsec/decisions", map[string]any{"scope": "ip", "value": "203.0.113.51", "duration": "2h"}, &res); s != http.StatusOK {
		t.Fatalf("blokkeren: %d", s)
	}
	if s := c.do("POST", "/api/v1/crowdsec/decisions", map[string]any{"scope": "ip", "value": "203.0.113.51", "duration": "2 uur"}, nil); s != http.StatusBadRequest {
		t.Fatalf("ongeldige duur: %d", s)
	}
	n.mu.Lock()
	decided := strings.Join(n.decided, "\n")
	n.mu.Unlock()
	if !strings.Contains(decided, "cscli decisions delete --ip 203.0.113.50") ||
		!strings.Contains(decided, "cscli decisions add --ip 203.0.113.51 --duration 2h --type ban --reason handmatig via ClusterForge") {
		t.Fatalf("cscli: %s", decided)
	}

	// Profiel en instellingen.
	if s := c.do("PUT", "/api/v1/crowdsec/nodes/"+web01.ID, map[string]any{"bouncer": false, "extra_profiles": []string{"nginx"}, "disabled_profiles": []string{}}, nil); s != http.StatusOK {
		t.Fatalf("configureren: %d", s)
	}
	if s := c.do("PUT", "/api/v1/crowdsec/settings", map[string]any{"auto_apply": false, "bouncer_default": true, "share_bans": "cluster",
		"whitelist": []string{"192.168.0.0/16", "nee"}}, nil); s != http.StatusBadRequest {
		t.Fatalf("ongeldige whitelist: %d", s)
	}
	var set struct {
		ShareBans string `json:"share_bans"`
	}
	if s := c.do("PUT", "/api/v1/crowdsec/settings", map[string]any{"auto_apply": false, "bouncer_default": true, "share_bans": "cluster",
		"whitelist": []string{"192.168.0.0/16"}}, &set); s != http.StatusOK || set.ShareBans != "cluster" {
		t.Fatalf("instellingen: %d %+v", s, set)
	}
	// Configureren startte (met automatisch toepassen nog aan) misschien
	// al een taak; wacht tot die klaar is.
	eventually(t, "geen lopende CrowdSec-taak", func() bool {
		var d struct {
			Node struct {
				ApplyRunning bool `json:"apply_running"`
			} `json:"node"`
		}
		c.do("GET", "/api/v1/crowdsec/nodes/"+web01.ID, nil, &d)
		return !d.Node.ApplyRunning
	})
	var started struct {
		JobID string `json:"job_id"`
	}
	// Een tik van de lus kan net nog een taak gestart hebben (automatisch
	// toepassen stond aan tot de PUT hierboven); dan antwoordt de API 400
	// "loopt al een CrowdSec-taak". Probeer tot de node vrij is.
	eventually(t, "toepassen gestart", func() bool {
		return c.do("POST", "/api/v1/crowdsec/nodes/"+web01.ID+"/apply", map[string]any{}, &started) == http.StatusAccepted
	})
	if j := waitJob(t, c, started.JobID); j.Status != "succeeded" {
		t.Fatalf("taak %+v", j)
	}
	n.mu.Lock()
	script = n.script
	n.mu.Unlock()
	if !strings.Contains(script, "crowdsecurity/nginx") || strings.Contains(script, "apt-get install -y -q crowdsec-firewall-bouncer-nftables") {
		t.Fatalf("script na configureren:\n%s", script)
	}

	e.createUser("viewer", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	viewer.login("viewer", "een-lang-wachtwoord", "")
	if s := viewer.do("POST", "/api/v1/crowdsec/decisions", map[string]any{"scope": "ip", "value": "203.0.113.52", "duration": "2h"}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer blokkeert: %d", s)
	}
	if s := viewer.do("GET", "/api/v1/crowdsec", nil, nil); s != http.StatusOK {
		t.Fatalf("viewer leest: %d", s)
	}
}
