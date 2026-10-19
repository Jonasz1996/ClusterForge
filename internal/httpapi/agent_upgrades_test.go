package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type upgradeView struct {
	Protocol int      `json:"protocol"`
	Archs    []string `json:"archs"`
	Auto     bool     `json:"auto"`
	Agents   []struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		State    string `json:"state"`
		Why      string `json:"why"`
		Auto     bool   `json:"auto"`
		Online   bool   `json:"online"`
	} `json:"agents"`
}

func (v upgradeView) states() string {
	var s []string
	for _, a := range v.Agents {
		mark := ""
		if a.Auto {
			mark = "*"
		}
		s = append(s, a.Hostname+"="+a.State+mark)
	}
	return strings.Join(s, " ")
}

// upgradeNode is een node met een agent die zichzelf echt bijwerkt: een
// binary op schijf, en een herstart die de agent opnieuw start met wat er
// dan staat. Met rollback praat de nieuwe agent nooit en zet de timer de
// oude terug.
type upgradeNode struct {
	t        *testing.T
	e        *testEnv
	c        *client
	id       string
	bin      string
	root     string
	cfg      *agent.Config
	mu       sync.Mutex
	rollback bool
	stop     func()
	done     chan struct{}
}

func (n *upgradeNode) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	switch {
	case len(args) == 1 && args[0] == "self-check":
		return json.Marshal(protocol.SelfCheck{Version: "nieuw", Protocol: protocol.Version, Arch: runtime.GOARCH})
	case name == "systemd-run", name == "systemctl":
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			return nil, errors.New("exit status 3")
		}
		return nil, nil
	}
	return nil, errors.New("geen commando's in tests: " + name)
}

func (n *upgradeNode) start() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	col := fakeCollector(n.root)
	ag := &agent.Agent{
		Config: n.cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: col,
		HeartbeatInterval: 100_000_000, StatePath: filepath.Join(n.root, "var/lib/clusterforge/agent-state.json"),
		BinaryPath: n.bin, Exec: n.exec, Restart: n.restart,
	}
	go func() { defer close(done); _ = ag.Run(ctx) }()
	n.mu.Lock()
	n.stop, n.done = cancel, done
	n.mu.Unlock()
}

func (n *upgradeNode) halt() {
	n.mu.Lock()
	stop, done := n.stop, n.done
	n.mu.Unlock()
	stop()
	<-done
}

// restart doet wat systemctl restart cf-agent doet; met rollback eerst wat
// de terugvaltimer doet.
func (n *upgradeNode) restart() {
	n.halt()
	n.mu.Lock()
	rb := n.rollback
	n.mu.Unlock()
	if rb {
		state := filepath.Join(n.root, "var/lib/clusterforge/agent-state.json")
		if err := agent.RollbackUpgrade(context.Background(), n.bin, state, slog.New(slog.NewTextHandler(io.Discard, nil)), n.exec); err != nil {
			n.t.Errorf("terugzetten: %v", err)
		}
	}
	n.start()
}

func startUpgradeNode(t *testing.T, e *testEnv, c *client, nodeID, machineID string) *upgradeNode {
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
	n := &upgradeNode{t: t, e: e, c: c, id: nodeID, root: root, cfg: cfg, bin: filepath.Join(root, "usr/local/bin/cf-agent")}
	if err := os.MkdirAll(filepath.Dir(n.bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(n.bin, []byte("oude agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	n.start()
	t.Cleanup(n.halt)
	return n
}

// offerAgent zet een agent klaar zoals de server hem aanbiedt.
func offerAgent(t *testing.T, e *testEnv, content string) {
	t.Helper()
	name := "cf-agent-linux-" + runtime.GOARCH
	sum := sha256.Sum256([]byte(content))
	if err := os.WriteFile(filepath.Join(e.upg.Dir, name), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.upg.Dir, name+".sha256"), []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentUpgrades(t *testing.T) {
	e, c := adminClient(t)
	offerAgent(t, e, "nieuwe agent")

	var lab, prod cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &lab)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "DB", "type": "keepalived", "environment": "prod"}, &prod)
	var web01, web02, db01 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": lab.ID}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": lab.ID}, &web02)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db01", "cluster_id": prod.ID}, &db01)
	n1 := startUpgradeNode(t, e, c, web01.ID, strings.Repeat("1", 32))
	startUpgradeNode(t, e, c, web02.ID, strings.Repeat("2", 32))
	startUpgradeNode(t, e, c, db01.ID, strings.Repeat("3", 32))

	var v upgradeView
	eventually(t, "drie verouderde agents", func() bool {
		c.do("GET", "/api/v1/agent-upgrades", nil, &v)
		return v.states() == "db01=outdated web01=outdated* web02=outdated*"
	})
	if !v.Auto || v.Protocol != protocol.Version || len(v.Archs) != 1 {
		t.Fatalf("overzicht: %+v", v)
	}

	// Alles bijwerken: prod blijft erbuiten, het labcluster krijgt één taak.
	var started struct {
		Jobs    []job    `json:"jobs"`
		Skipped []string `json:"skipped"`
	}
	if s := c.do("POST", "/api/v1/agent-upgrades", map[string]any{"all": true}, &started); s != http.StatusAccepted || len(started.Jobs) != 1 {
		t.Fatalf("bijwerken: %d %+v", s, started)
	}
	j := waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "succeeded" || len(j.Steps) != 2 || j.Steps[0].Name != "web01: cf-agent bijwerken" {
		t.Fatalf("taak: %+v", j)
	}
	if log := strings.Join(j.Steps[1].Log, "\n"); !strings.Contains(log, "terug na") || !strings.Contains(log, "sha256 klopt") {
		t.Fatalf("log: %s", log)
	}
	c.do("GET", "/api/v1/agent-upgrades", nil, &v)
	if got := v.states(); got != "db01=outdated web01=current web02=current" {
		t.Fatalf("na bijwerken: %s", got)
	}

	// Prod vraagt tweestapsverificatie en de slug.
	if s := c.do("POST", "/api/v1/agent-upgrades", map[string]any{"cluster_id": prod.ID}, nil); s != http.StatusForbidden {
		t.Fatalf("prod zonder TOTP: %d", s)
	}
	// Een node die al bij is, of meer dan één keuze, kan niet.
	if s := c.do("POST", "/api/v1/agent-upgrades", map[string]any{"node_ids": []string{web01.ID}}, nil); s != http.StatusConflict {
		t.Fatalf("al bijgewerkt: %d", s)
	}
	if s := c.do("POST", "/api/v1/agent-upgrades", map[string]any{"all": true, "cluster_id": lab.ID}, nil); s != http.StatusBadRequest {
		t.Fatalf("twee keuzes: %d", s)
	}

	// Een nieuwere server; de nieuwe agent op web01 praat nooit en wordt
	// teruggezet.
	offerAgent(t, e, "nog nieuwere agent")
	n1.mu.Lock()
	n1.rollback = true
	n1.mu.Unlock()
	if s := c.do("POST", "/api/v1/agent-upgrades", map[string]any{"node_ids": []string{web01.ID}}, &started); s != http.StatusAccepted {
		t.Fatalf("web01: %d", s)
	}
	j = waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "failed" || !strings.Contains(j.Error, "zette de oude terug") {
		t.Fatalf("teruggezet: %+v", j)
	}
	eventually(t, "web01 teruggezet", func() bool {
		c.do("GET", "/api/v1/agent-upgrades", nil, &v)
		return v.states() == "db01=outdated web01=failed web02=outdated*"
	})

	// Vanzelf: uit op het cluster doet niets, aan werkt web02 bij en laat
	// web01 met rust.
	if s := c.do("PUT", "/api/v1/clusters/"+lab.ID+"/agent-upgrade-auto", map[string]any{"enabled": false}, &v); s != http.StatusOK || v.states() != "db01=outdated web01=failed web02=outdated" {
		t.Fatalf("uit: %d %s", s, v.states())
	}
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	var jobs struct {
		Items []job `json:"items"`
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	if len(jobs.Items) != 2 {
		t.Fatalf("taken na vanzelf uit: %d", len(jobs.Items))
	}
	c.do("PUT", "/api/v1/clusters/"+lab.ID+"/agent-upgrade-auto", map[string]any{"enabled": true}, nil)
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	if len(jobs.Items) != 3 || !strings.HasSuffix(jobs.Items[0].Title, "(vanzelf)") || !strings.Contains(jobs.Items[0].Title, "web02") {
		t.Fatalf("vanzelf: %+v", jobs.Items)
	}
	if j := waitJob(t, c, jobs.Items[0].ID); j.Status != "succeeded" {
		t.Fatalf("vanzelf: %+v", j)
	}
	c.do("GET", "/api/v1/agent-upgrades", nil, &v)
	if got := v.states(); got != "db01=outdated web01=failed web02=current" {
		t.Fatalf("na vanzelf: %s", got)
	}

	// Helemaal uit.
	if s := c.do("PUT", "/api/v1/agent-upgrades/auto", map[string]any{"enabled": false}, &v); s != http.StatusOK || v.Auto {
		t.Fatalf("uit: %d %+v", s, v)
	}

	var evs struct {
		Items []struct {
			Action  string `json:"action"`
			Summary string `json:"summary"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/events?limit=100", nil, &evs)
	seen := map[string]int{}
	for _, ev := range evs.Items {
		seen[ev.Action]++
	}
	if seen["node.agent_upgraded"] != 3 || seen["node.agent_upgrade_failed"] != 1 || seen["cluster.agent_upgrade_auto"] != 2 ||
		seen["settings.agent_upgrade_auto"] != 1 {
		t.Fatalf("events: %v", seen)
	}
}
