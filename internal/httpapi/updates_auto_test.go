package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/updates"
)

// aptNode speelt apt na voor een node met keepalived (keepalivedSim); fail
// laat de upgrade mislukken.
type aptNode struct {
	mu    sync.Mutex
	fail  bool
	count int
}

func (a *aptNode) cmd(_ context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
	c := strings.Join(argv, " ")
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.HasPrefix(c, "apt-get full-upgrade") || strings.HasPrefix(c, "apt-get upgrade") {
		a.count++
		if a.fail {
			_, _ = io.WriteString(out, "E: Kan niet\n")
			return 100, nil
		}
		_, _ = io.WriteString(out, "Setting up openssl ...\n")
	}
	return 0, nil
}

func (a *aptNode) setFail(v bool) {
	a.mu.Lock()
	a.fail = v
	a.mu.Unlock()
}

func startUpdateSimAgent(t *testing.T, e *testEnv, c *client, nodeID, machineID string, n *simNode) *aptNode {
	t.Helper()
	apt := &aptNode{}
	n.stop = startAgentWith(t, e, c, nodeID, machineID, n.addrs, func(ag *agent.Agent, root string) {
		n.root = root
		if err := os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("86400.00 80000.00\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ag.Exec = n.exec
		ag.Collector.Run = n.exec
		ag.Root = root
		ex := &agent.RunExecutor{Root: root, Cmd: apt.cmd, Stderr: io.Discard}
		ag.StartRun = func(_ context.Context, id string) error {
			go ex.Main(context.Background(), id)
			return nil
		}
	})
	return apt
}

func TestUpdatesFailoverMaster(t *testing.T) {
	e, c := adminClient(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "lab"}, &cl)
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.99.0.40"}, nil)
	var lb01, lb02 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb01", "cluster_id": cl.ID}, &lb01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb02", "cluster_id": cl.ID}, &lb02)
	sim := &keepalivedSim{vip: "10.99.0.40"}
	k1, k2 := sim.add("lb01", "192.0.2.41"), sim.add("lb02", "192.0.2.42")
	a1 := startUpdateSimAgent(t, e, c, lb01.ID, strings.Repeat("1", 32), k1)
	a2 := startUpdateSimAgent(t, e, c, lb02.ID, strings.Repeat("2", 32), k2)

	var d statusView
	ownerIs := func(what, owner string) {
		t.Helper()
		eventually(t, what, func() bool {
			c.do("GET", "/api/v1/clusters/"+cl.ID, nil, &d)
			return d.Vips[0].OwnerHostname != nil && *d.Vips[0].OwnerHostname == owner
		})
	}
	ownerIs("VIP op lb01", "lb01")
	var ov struct {
		Nodes []struct {
			Smart  bool `json:"smart"`
			Online bool `json:"online"`
		} `json:"nodes"`
	}
	eventually(t, "agents met protocol 29", func() bool {
		c.do("GET", "/api/v1/updates", nil, &ov)
		return len(ov.Nodes) == 2 && ov.Nodes[0].Smart && ov.Nodes[1].Smart && ov.Nodes[0].Online && ov.Nodes[1].Online
	})

	var started struct {
		Jobs []job `json:"jobs"`
	}
	if s := c.do("POST", "/api/v1/updates", map[string]any{"cluster_id": cl.ID, "apt": map[string]any{"full": true}}, &started); s != http.StatusAccepted {
		t.Fatalf("bijwerken: %d", s)
	}
	j := waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "succeeded" {
		t.Fatalf("taak: %+v", j)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	want := "lb02: voorbereiden|lb02: pakketten (apt)|lb02: gezond terug|" +
		"lb01 (VIP-houder): failover, VIP's naar een bijgewerkte node|lb01 (VIP-houder): voorbereiden|lb01 (VIP-houder): pakketten (apt)|" +
		"lb01 (VIP-houder): keepalived weer aan|lb01 (VIP-houder): gezond terug"
	if got := strings.Join(names, "|"); got != want {
		t.Fatalf("stappen:\n%s\nwil\n%s", got, want)
	}
	if log := strings.Join(j.Steps[3].Log, "\n"); !strings.Contains(log, "10.99.0.40 staat nu op lb02") {
		t.Fatalf("failover: %s", log)
	}
	if en, ac := k1.keepalived(); !en || !ac {
		t.Fatalf("keepalived op lb01 na de update: enabled %v active %v", en, ac)
	}
	ownerIs("VIP terug op lb01", "lb01")
	if a1.count != 1 || a2.count != 1 {
		t.Fatalf("upgrades %d %d", a1.count, a2.count)
	}

	// De master faalt: keepalived blijft uit, het VIP blijft op lb02 en de
	// node staat op onderhoud.
	a1.setFail(true)
	c.do("POST", "/api/v1/updates", map[string]any{"cluster_id": cl.ID, "apt": map[string]any{}}, &started)
	j = waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "failed" || !strings.Contains(j.Error, "lb01") {
		t.Fatalf("taak: %+v", j)
	}
	if _, ac := k1.keepalived(); ac {
		t.Fatal("keepalived draait weer op de mislukte master")
	}
	ownerIs("VIP blijft op lb02", "lb02")
	var n1 struct {
		Lifecycle string `json:"lifecycle"`
	}
	c.do("GET", "/api/v1/nodes/"+lb01.ID, nil, &n1)
	if n1.Lifecycle != "maintenance" {
		t.Fatalf("lifecycle %q", n1.Lifecycle)
	}

	// Mislukt een back-upnode, dan komt de master niet aan de beurt.
	a1.setFail(false)
	c.do("POST", "/api/v1/nodes/"+lb01.ID+"/actions", map[string]any{"action": "activate"}, nil)
	ownerIs("VIP weer op lb01", "lb01")
	a2.setFail(true)
	before := a1.count
	c.do("POST", "/api/v1/updates", map[string]any{"cluster_id": cl.ID, "apt": map[string]any{}}, &started)
	j = waitJob(t, c, started.Jobs[0].ID)
	if j.Status != "failed" || a1.count != before {
		t.Fatalf("back-upnode mislukt maar master bijgewerkt: %+v (%d → %d)", j, before, a1.count)
	}
	if en, ac := k1.keepalived(); !en || !ac {
		t.Fatal("master aangeraakt")
	}
}

func TestUpdatePoliciesAuto(t *testing.T) {
	e, c := adminClient(t)
	var lab, prod cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &lab)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "DB", "type": "keepalived", "environment": "prod"}, &prod)
	var web01 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": lab.ID}, &web01)
	n := startRunNode(t, e, c, web01.ID, strings.Repeat("3", 32), "192.0.2.51")

	now := time.Now()
	window := fmt.Sprintf("dagelijks %s-%s", now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04"))
	pol := map[string]any{"enabled": true, "apt": true, "apt_full": true, "docker": false, "apt_autoremove": false, "docker_prune": false, "reboot": false, "window": window}
	if s := c.do("PUT", "/api/v1/updates/policies/environments/lab", map[string]any{"enabled": true, "apt": true, "window": "morgen"}, nil); s != http.StatusBadRequest {
		t.Fatalf("slecht venster: %d", s)
	}
	if s := c.do("PUT", "/api/v1/updates/policies/environments/lab", pol, nil); s != http.StatusOK {
		t.Fatalf("lab: %d", s)
	}
	// Prod vraagt tweestapsverificatie.
	if s := c.do("PUT", "/api/v1/updates/policies/environments/prod", pol, nil); s != http.StatusForbidden {
		t.Fatalf("prod zonder TOTP: %d", s)
	}
	if s := c.do("PUT", "/api/v1/updates/policies/clusters/"+prod.ID, map[string]any{"mode": "off"}, nil); s != http.StatusForbidden {
		t.Fatalf("prodcluster zonder TOTP: %d", s)
	}
	eventually(t, "web01 met protocol 29", func() bool {
		var ov struct {
			Nodes []struct {
				Smart  bool `json:"smart"`
				Online bool `json:"online"`
			} `json:"nodes"`
		}
		c.do("GET", "/api/v1/updates", nil, &ov)
		return len(ov.Nodes) == 1 && ov.Nodes[0].Smart && ov.Nodes[0].Online
	})
	ctx := context.Background()
	if err := e.upd.AutoTick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var pv struct {
		Clusters []struct {
			Name      string  `json:"name"`
			Mode      string  `json:"mode"`
			LastJobID *string `json:"last_job_id"`
			LastNote  string  `json:"last_note"`
			Effective struct {
				Enabled bool `json:"enabled"`
			} `json:"effective"`
		} `json:"clusters"`
	}
	c.do("GET", "/api/v1/updates/policies", nil, &pv)
	var jobID string
	for _, cl := range pv.Clusters {
		if cl.Name == "Web" {
			if cl.LastJobID == nil || !cl.Effective.Enabled || cl.Mode != "inherit" {
				t.Fatalf("web: %+v", cl)
			}
			jobID = *cl.LastJobID
		}
		if cl.Name == "DB" && (cl.LastJobID != nil || cl.Effective.Enabled) {
			t.Fatalf("prod zonder beleid kreeg een taak: %+v", cl)
		}
	}
	j := waitJob(t, c, jobID)
	if j.Status != "succeeded" || !strings.HasPrefix(j.Title, "Automatisch: ") || n.got("apt-get full-upgrade") != 1 {
		t.Fatalf("taak %+v, aanroepen %v", j, n.calls)
	}
	// Hetzelfde venster telt maar één keer.
	if err := e.upd.AutoTick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n.got("apt-get full-upgrade") != 1 {
		t.Fatal("tweede taak in hetzelfde venster")
	}
	// Uit op het cluster wint van de omgeving.
	if s := c.do("PUT", "/api/v1/updates/policies/clusters/"+lab.ID, map[string]any{"mode": "off"}, &pv); s != http.StatusOK {
		t.Fatalf("uit: %d", s)
	}
	for _, cl := range pv.Clusters {
		if cl.Name == "Web" && (cl.Mode != "off" || cl.Effective.Enabled) {
			t.Fatalf("uit: %+v", cl)
		}
	}
	// Zonder rapport is er niets om een fix voor voor te stellen.
	if s := c.do("POST", "/api/v1/updates/fix", map[string]any{"node_id": web01.ID, "project": "web"}, nil); s != http.StatusNotFound && s != http.StatusForbidden {
		t.Fatalf("fix zonder rapport: %d", s)
	}
}

func TestReleaseNotesSources(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/app/releases":
			_, _ = io.WriteString(w, `[{"tag_name":"v2.0","name":"2.0","body":"BREAKING: APP_PORT heet nu APP_LISTEN"}]`)
		case "/v2/repositories/library/nginx/":
			_, _ = io.WriteString(w, `{"description":"Official build of Nginx.","full_description":"NGINX_ENTRYPOINT_QUIET_LOGS=1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	src := &updates.Sources{GitHubAPI: gh.URL, DockerHubAPI: gh.URL}
	notes, errs := src.Fetch(context.Background(), "ghcr.io/acme/app:2.0", "")
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "APP_LISTEN") || notes[0].URL != "https://github.com/acme/app/releases" || len(errs) != 0 {
		t.Fatalf("%+v %v", notes, errs)
	}
	notes, _ = src.Fetch(context.Background(), "nginx:1.29", "")
	if len(notes) != 1 || notes[0].Where != "Docker Hub" || notes[0].URL != "https://hub.docker.com/_/nginx" {
		t.Fatalf("%+v", notes)
	}
	// Een Docker Hub-image met het label source gaat naar beide bronnen.
	notes, _ = src.Fetch(context.Background(), "acme/app:2.0", "https://github.com/acme/app.git")
	if len(notes) != 1 || notes[0].Where != "GitHub-releases" {
		t.Fatalf("%+v", notes)
	}
	for in, want := range map[string]string{
		"nginx": "docker.io library/nginx", "nginx:1.29@sha256:abc": "docker.io library/nginx", "ghcr.io/a/b:1": "ghcr.io a/b",
		"localhost:5000/x:1": "localhost:5000 x", "lscr.io/linuxserver/plex:latest": "lscr.io linuxserver/plex",
	} {
		reg, repo := updates.ImageRepo(in)
		if reg+" "+repo != want {
			t.Errorf("%s: %s %s", in, reg, repo)
		}
	}
}
