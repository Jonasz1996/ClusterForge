package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type nodeChange struct {
	ID       int64             `json:"id"`
	NodeID   string            `json:"node_id"`
	Hostname string            `json:"hostname"`
	Ts       time.Time         `json:"ts"`
	Source   string            `json:"source"`
	Kind     string            `json:"kind"`
	Subject  string            `json:"subject"`
	Action   string            `json:"action"`
	Summary  string            `json:"summary"`
	Detail   map[string]string `json:"detail"`
	JobID    *string           `json:"job_id"`
}

type nodeChanges struct {
	Changes      []nodeChange `json:"changes"`
	HasMore      bool         `json:"has_more"`
	AgentsTooOld []struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
	} `json:"agents_too_old"`
}

func (c *client) changes(path string, query url.Values) nodeChanges {
	c.t.Helper()
	var out nodeChanges
	if s := c.do("GET", path+"?"+query.Encode(), nil, &out); s != http.StatusOK {
		c.t.Fatalf("GET %s: %d", path, s)
	}
	return out
}

func findChange(cs []nodeChange, kind, action string) *nodeChange {
	for i := range cs {
		if cs[i].Kind == kind && cs[i].Action == action {
			return &cs[i]
		}
	}
	return nil
}

func write(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestJournal(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &cl)
	var n node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web-01", "cluster_id": cl.ID}, &n); s != http.StatusCreated {
		t.Fatalf("node: %d", s)
	}

	// Een apt-run van een uur geleden, een nginx-config en een boot-id.
	start := time.Now().Add(-time.Hour).Local()
	history := "Start-Date: " + start.Format("2006-01-02  15:04:05") + "\n" +
		"Commandline: apt-get install --only-upgrade nginx\n" +
		"Requested-By: jonas (1000)\n" +
		"Upgrade: nginx:amd64 (1.24.0-1, 1.24.0-2)\n" +
		"End-Date: " + start.Add(5*time.Second).Format("2006-01-02  15:04:05") + "\n\n"
	var root string
	stop := startAgentWith(t, e, c, n.ID, strings.Repeat("7", 32), &addrs{}, func(ag *agent.Agent, r string) {
		root = r
		write(t, r, "var/log/apt/history.log", history)
		write(t, r, "etc/nginx/nginx.conf", "worker_processes 1;\n")
		write(t, r, "proc/sys/kernel/random/boot_id", "aaaa\n")
		write(t, r, "proc/sys/kernel/osrelease", "6.1.0-25-amd64\n")
		ag.JournalInterval = 100 * time.Millisecond
	})
	path := "/api/v1/nodes/" + n.ID + "/changes"

	var got nodeChanges
	eventually(t, "pakket in het journaal", func() bool {
		got = c.changes(path, nil)
		return findChange(got.Changes, "package", "upgrade") != nil
	})
	pkg := findChange(got.Changes, "package", "upgrade")
	if pkg.Summary != "nginx bijgewerkt van 1.24.0-1 naar 1.24.0-2 (jonas)" || pkg.Source != "agent" || pkg.Hostname != "web-01" ||
		pkg.Detail["via"] != "apt-get" || pkg.Ts.Sub(start).Abs() > 2*time.Second {
		t.Fatalf("pakket: %+v", pkg)
	}
	if len(got.AgentsTooOld) != 0 {
		t.Fatalf("agent te oud: %+v", got.AgentsTooOld)
	}

	// Met de hand een config aanpassen en herstarten met een nieuwe kernel.
	write(t, root, "etc/nginx/nginx.conf", "worker_processes 4;\n")
	write(t, root, "proc/sys/kernel/osrelease", "6.1.0-26-amd64\n")
	write(t, root, "proc/sys/kernel/random/boot_id", "bbbb\n")
	eventually(t, "bestand en herstart", func() bool {
		got = c.changes(path, nil)
		return findChange(got.Changes, "file", "modified") != nil && findChange(got.Changes, "boot", "boot") != nil
	})
	if f := findChange(got.Changes, "file", "modified"); f.Summary != "/etc/nginx/nginx.conf aangepast" {
		t.Fatalf("bestand: %+v", f)
	}
	if b := findChange(got.Changes, "boot", "boot"); b.Summary != "Herstart, kernel van 6.1.0-25-amd64 naar 6.1.0-26-amd64" {
		t.Fatalf("herstart: %+v", b)
	}

	// Filters en grenzen.
	only := c.changes(path, url.Values{"kind": {"package,boot"}})
	for _, ch := range only.Changes {
		if ch.Kind != "package" && ch.Kind != "boot" {
			t.Fatalf("filter op soort: %+v", ch)
		}
	}
	if len(only.Changes) != 2 {
		t.Fatalf("filter: %d regels", len(only.Changes))
	}
	if one := c.changes(path, url.Values{"limit": {"1"}}); len(one.Changes) != 1 || !one.HasMore {
		t.Fatalf("limit 1: %d %v", len(one.Changes), one.HasMore)
	}
	if none := c.changes(path, url.Values{"to": {start.Add(-time.Minute).UTC().Format(time.RFC3339)}}); len(none.Changes) != 0 {
		t.Fatalf("voor de apt-run: %+v", none.Changes)
	}
	for _, q := range []url.Values{
		{"kind": {"package,onzin"}}, {"limit": {"0"}}, {"limit": {"1001"}},
		{"from": {time.Now().UTC().Format(time.RFC3339)}, "to": {time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}},
	} {
		if s := c.do("GET", path+"?"+q.Encode(), nil, nil); s != http.StatusBadRequest {
			t.Errorf("%v: %d", q, s)
		}
	}
	if s := c.do("GET", "/api/v1/nodes/"+uuid.NewString()+"/changes", nil, nil); s != http.StatusNotFound {
		t.Errorf("onbekende node: %d", s)
	}
	if s := c.do("GET", "/api/v1/clusters/"+uuid.NewString()+"/changes", nil, nil); s != http.StatusNotFound {
		t.Errorf("onbekend cluster: %d", s)
	}

	// Het cluster ziet de regels van zijn nodes.
	byCluster := c.changes("/api/v1/clusters/"+cl.ID+"/changes", url.Values{"kind": {"package"}})
	if len(byCluster.Changes) != 1 || byCluster.Changes[0].Hostname != "web-01" {
		t.Fatalf("cluster: %+v", byCluster.Changes)
	}

	// Een viewer ziet een login zonder IP-adres.
	nodeID := uuid.MustParse(n.ID)
	clusterID := uuid.MustParse(cl.ID)
	if err := journal.Record(ctx, store.New(e.pool), journal.Entry{
		NodeID: nodeID, ClusterID: &clusterID, TS: time.Now().Add(-time.Minute), Source: journal.SourceAgent, Kind: journal.KindLogin,
		Subject: "root", Action: "login", Detail: map[string]string{"ip": "10.0.0.5", "method": "publickey"},
	}); err != nil {
		t.Fatal(err)
	}
	logins := c.changes(path, url.Values{"kind": {"login"}})
	if len(logins.Changes) != 1 || logins.Changes[0].Summary != "SSH-login als root vanaf 10.0.0.5 (sleutel)" || logins.Changes[0].Detail["ip"] != "10.0.0.5" {
		t.Fatalf("login voor admin: %+v", logins.Changes)
	}
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	logins = v.changes(path, url.Values{"kind": {"login"}})
	if len(logins.Changes) != 1 || logins.Changes[0].Summary != "SSH-login als root (sleutel)" || logins.Changes[0].Detail["ip"] != "" ||
		logins.Changes[0].Detail["method"] != "publickey" {
		t.Fatalf("login voor viewer: %+v", logins.Changes)
	}

	// Een agent van voor het journaal staat apart, met de vraag om bij te
	// werken.
	stop()
	if _, err := e.pool.Exec(ctx, "UPDATE agents SET protocol_version = 4 WHERE node_id = $1", nodeID); err != nil {
		t.Fatal(err)
	}
	old := c.changes(path, nil)
	if len(old.AgentsTooOld) != 1 || old.AgentsTooOld[0].Hostname != "web-01" {
		t.Fatalf("agent te oud: %+v", old.AgentsTooOld)
	}
	if old := c.changes("/api/v1/clusters/"+cl.ID+"/changes", nil); len(old.AgentsTooOld) != 1 {
		t.Fatalf("agent te oud in het cluster: %+v", old.AgentsTooOld)
	}
}

func TestJournalDeployAndProxmox(t *testing.T) {
	e, c := adminClient(t)
	_, _, ids, pve, conn := deployWebPVE(t, e, c)
	path := "/api/v1/nodes/" + ids["web-01"] + "/changes"

	// Wat de uitrol schreef, staat bij de node met de taak erbij.
	got := c.changes(path, url.Values{"kind": {"file"}})
	var written []string
	for _, ch := range got.Changes {
		if ch.Source == "clusterforge" && ch.Action == "written" {
			if ch.JobID == nil {
				t.Fatalf("zonder taak: %+v", ch)
			}
			written = append(written, ch.Subject)
		}
	}
	if !slices.Contains(written, "/etc/keepalived/keepalived.conf") {
		t.Fatalf("geschreven bestanden: %v", written)
	}
	if pk := c.changes(path, url.Values{"kind": {"package"}}); findChange(pk.Changes, "package", "install") == nil {
		t.Fatalf("pakketten: %+v", pk.Changes)
	}

	// Iemand stopt de VM in de webinterface van Proxmox.
	var nd pveNode
	c.do("GET", "/api/v1/nodes/"+ids["web-01"], nil, &nd)
	if nd.Proxmox == nil {
		t.Fatalf("niet gekoppeld: %+v", nd)
	}
	pve.UserTask(nd.Proxmox.Host, "qmstop", nd.Proxmox.VMID, "root@pam", "OK", "stopped")
	pve.SetHostOnline("pve2", false)
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != http.StatusOK {
		t.Fatalf("sync: %d", s)
	}
	vm := c.changes(path, url.Values{"kind": {"vm"}})
	stopped := findChange(vm.Changes, "vm", "qmstop")
	if stopped == nil || stopped.Source != "proxmox" || !strings.HasSuffix(stopped.Summary, "hard gestopt in Proxmox door root@pam") {
		t.Fatalf("VM gestopt: %+v", vm.Changes)
	}
	// ClusterForge startte de VM zelf bij de uitrol.
	if started := findChange(vm.Changes, "vm", "qmstart"); started == nil || !strings.HasSuffix(started.Summary, "door ClusterForge") {
		t.Fatalf("VM gestart: %+v", vm.Changes)
	}
	// Een tweede sync schrijft niets dubbel.
	c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil)
	if again := c.changes(path, url.Values{"kind": {"vm"}}); len(again.Changes) != len(vm.Changes) {
		t.Fatalf("dubbel: %d na %d", len(again.Changes), len(vm.Changes))
	}
	if acts := eventActions(t, e, conn.ID); countOf(acts, "proxmox.host_offline") != 1 {
		t.Fatalf("events: %v", acts)
	}
	pve.SetHostOnline("pve2", true)
	c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil)
	if acts := eventActions(t, e, conn.ID); countOf(acts, "proxmox.host_online") != 1 {
		t.Fatalf("events: %v", acts)
	}
}
