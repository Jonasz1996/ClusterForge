package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
)

// pbsSim speelt een Proxmox Backup Server na: de bestanden waaraan de
// agent hem herkent en de antwoorden van proxmox-backup-debug api.
type pbsSim struct {
	mu    sync.Mutex
	calls []string
	gcRan bool
}

const pbsUPID = `UPID:pbs1:000012AB:0001A2B3:00000003:65F0A1B2:garbage_collection:store1:root@pam:`

func (p *pbsSim) cmd(_ context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
	c := strings.Join(argv, " ")
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.mu.Unlock()
	switch {
	case strings.HasPrefix(c, "dpkg-query"):
		_, _ = io.WriteString(out, "4.0.14-1")
	case strings.HasPrefix(c, "proxmox-backup-debug api get /nodes/localhost/status "):
		_, _ = io.WriteString(out, `{"cpu":0.05,"wait":0.01,"loadavg":[0.1,0.2,0.3],"memory":{"total":8000,"used":2000,"free":6000},"swap":{"total":0,"used":0},"root":{"total":100,"used":40,"avail":60},"uptime":3600,"kversion":"Linux 6.14","cpuinfo":{"model":"Intel","cpus":4,"sockets":1},"info":{"fingerprint":"aa:bb"}}`)
	case strings.HasPrefix(c, "proxmox-backup-debug api get /status/datastore-usage "):
		_, _ = io.WriteString(out, `[{"store":"store1","total":1000,"used":970,"avail":30,"history":[0.9,null,0.97],"estimated-full-date":-1}]`)
	case strings.HasPrefix(c, "proxmox-backup-debug api get /admin/verify "):
		_, _ = io.WriteString(out, `[{"id":"v-1","store":"store1","schedule":"daily","last-run-state":"error: 2 snapshots kapot","last-run-endtime":1}]`)
	case strings.HasPrefix(c, "proxmox-backup-debug api get /nodes/localhost/tasks/"+pbsUPID+"/status"):
		_, _ = io.WriteString(out, `{"status":"stopped","exitstatus":"OK"}`)
	case strings.HasPrefix(c, "proxmox-backup-debug api get /nodes/localhost/tasks/"+pbsUPID+"/log"):
		_, _ = io.WriteString(out, `[{"n":1,"t":"starting garbage collection on store store1"},{"n":2,"t":"TASK OK"}]`)
	case strings.HasPrefix(c, "proxmox-backup-debug api get "):
		_, _ = io.WriteString(out, `[]`)
	case strings.HasPrefix(c, "proxmox-backup-debug api create /admin/datastore/store1/gc "):
		p.mu.Lock()
		p.gcRan = true
		p.mu.Unlock()
		_, _ = io.WriteString(out, `"`+pbsUPID+`"`+"\n")
	case strings.HasPrefix(c, "proxmox-backup-debug api create /config/remote "):
		_, _ = io.WriteString(out, "null\n")
	default:
		return 1, fmt.Errorf("onbekend commando %q", c)
	}
	return 0, nil
}

func (p *pbsSim) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	var buf strings.Builder
	code, err := p.cmd(ctx, "", append([]string{name}, args...), nil, &buf)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d", code)
	}
	return []byte(buf.String()), err
}

// TestPBS: fase 32. De agent herkent de PBS, het overzicht met
// waarschuwingen, lezen van de API, een taak die op de UPID wacht, een
// geheim dat niet in de uitvoer komt en een aanroep die niet mag.
func TestPBS(t *testing.T) {
	e, c := adminClient(t)
	sim := &pbsSim{}
	var host node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "pbs1"}, &host); s != http.StatusCreated {
		t.Fatalf("node pbs1: %d", s)
	}
	startAgentWith(t, e, c, host.ID, strings.Repeat("8", 32), &addrs{}, func(ag *agent.Agent, root string) {
		for _, f := range []string{"usr/sbin/proxmox-backup-manager", "etc/proxmox-backup/datastore.cfg"} {
			_ = os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755)
		}
		_ = os.WriteFile(filepath.Join(root, "usr/sbin/proxmox-backup-manager"), nil, 0o755)
		_ = os.WriteFile(filepath.Join(root, "etc/proxmox-backup/datastore.cfg"), []byte("datastore: store1\n\tpath /mnt/store1\n"), 0o644)
		ag.Root = root
		ag.Exec = sim.exec
		ag.Collector.Run = sim.exec
		ex := &agent.RunExecutor{Root: root, Cmd: sim.cmd, Stderr: io.Discard, Sleep: func(context.Context, time.Duration) error { return nil }}
		ag.StartRun = func(_ context.Context, id string) error {
			go ex.Main(context.Background(), id)
			return nil
		}
	})

	var list struct {
		Items []struct {
			NodeID     string   `json:"node_id"`
			Version    string   `json:"version"`
			Datastores []string `json:"datastores"`
			Why        string   `json:"why"`
		} `json:"items"`
	}
	eventually(t, "PBS herkend", func() bool {
		c.do("GET", "/api/v1/pbs/servers", nil, &list)
		return len(list.Items) == 1 && list.Items[0].Why == ""
	})
	if list.Items[0].Version != "4.0.14-1" || len(list.Items[0].Datastores) != 1 {
		t.Fatalf("servers: %+v", list)
	}
	base := "/api/v1/pbs/servers/" + host.ID

	var o struct {
		Version  string `json:"version"`
		Warnings []struct {
			Text string `json:"text"`
		} `json:"warnings"`
		Jobs []struct {
			Kind string `json:"kind"`
		} `json:"jobs"`
	}
	if s := c.do("GET", base+"/overview", nil, &o); s != 200 || len(o.Jobs) != 1 || o.Jobs[0].Kind != "verify" {
		t.Fatalf("overzicht: %d %+v", s, o)
	}
	var all []string
	for _, w := range o.Warnings {
		all = append(all, w.Text)
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{"Datastore store1 is 97% vol", "Verify-job v-1 (store1) mislukte"} {
		if !strings.Contains(text, want) {
			t.Errorf("waarschuwing %q ontbreekt in %s", want, text)
		}
	}

	var got struct {
		Data []any `json:"data"`
	}
	if s := c.do("GET", base+"/api?path=/admin/datastore", nil, &got); s != 200 {
		t.Fatalf("lezen: %d", s)
	}
	if s := c.do("POST", base+"/api", map[string]any{"method": "create", "path": "/nodes/localhost/apt/upgrade"}, nil); s != 400 {
		t.Fatalf("niet toegelaten aanroep: %d", s)
	}
	if s := c.do("POST", base+"/api", map[string]any{"method": "delete", "path": "/config/datastore/store1"}, nil); s != 400 {
		t.Fatalf("verwijderen zonder bevestiging: %d", s)
	}

	var j job
	if s := c.do("POST", base+"/api", map[string]any{"method": "create", "path": "/admin/datastore/store1/gc"}, &j); s != 202 {
		t.Fatalf("gc: %d", s)
	}
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("gc-taak: %+v", j)
	}
	if !sim.gcRan {
		t.Fatal("gc niet gestart")
	}

	if s := c.do("POST", base+"/api", map[string]any{"method": "create", "path": "/config/remote",
		"params": map[string]string{"name": "offsite", "host": "10.0.0.9", "auth-id": "sync@pbs", "password": "zeer-geheim-123"}}, &j); s != 202 {
		t.Fatalf("remote: %d", s)
	}
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("remote-taak: %+v", j)
	}
	var raw map[string]any
	c.do("GET", "/api/v1/jobs/"+j.ID, nil, &raw)
	if b := fmt.Sprint(raw); strings.Contains(b, "zeer-geheim-123") {
		t.Fatal("wachtwoord staat in de taak")
	}
	sim.mu.Lock()
	defer sim.mu.Unlock()
	found := false
	for _, call := range sim.calls {
		if strings.HasPrefix(call, "proxmox-backup-debug api create /config/remote ") {
			found = strings.Contains(call, "--password=zeer-geheim-123")
		}
	}
	if !found {
		t.Fatalf("de agent kreeg het wachtwoord niet: %v", sim.calls)
	}
}
