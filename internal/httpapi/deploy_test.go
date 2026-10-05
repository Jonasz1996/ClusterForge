package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fleet speelt de nieuwe VM's: zodra ClusterForge het aanmeldbestand via de
// guest agent schrijft, start er een agent op een nagespeelde Debian-machine.
type fleet struct {
	t     *testing.T
	group agenttest.Group
	// addrs is het adres per hostname, zoals cloud-init het zou instellen.
	addrs map[string]string
	// setup past een machine aan voor zijn agent start.
	setup func(hostname string, h *agenttest.Host)

	mu    sync.Mutex
	seq   int
	hosts map[string]*agenttest.Host
	stops []func()
}

func newFleet(t *testing.T, addrs map[string]string) *fleet {
	f := &fleet{t: t, addrs: addrs, hosts: map[string]*agenttest.Host{}}
	t.Cleanup(func() {
		f.mu.Lock()
		stops := slices.Clone(f.stops)
		f.mu.Unlock()
		for _, stop := range stops {
			stop()
		}
	})
	return f
}

func (f *fleet) host(name string) *agenttest.Host {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hosts[name]
}

func (f *fleet) boot(_ int, name, file, content string) {
	if file != "/etc/clusterforge/enroll.json" {
		f.t.Errorf("onverwacht bestand %s", file)
		return
	}
	var in struct{ Server, Token string }
	if err := json.Unmarshal([]byte(content), &in); err != nil {
		f.t.Errorf("aanmeldbestand: %v", err)
		return
	}
	f.mu.Lock()
	f.seq++
	machineID := fmt.Sprintf("%032x", 0xd0+f.seq)
	addr := f.addrs[name]
	f.mu.Unlock()

	root := f.t.TempDir()
	for p, c := range map[string]string{
		"etc/machine-id": machineID + "\n",
		"proc/uptime":    "120.00 100.00\n",
		"proc/meminfo":   "MemTotal: 2000000 kB\nMemAvailable: 1500000 kB\n",
		"proc/loadavg":   "0.10 0.05 0.01 1/100 1234\n",
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			f.t.Error(err)
			return
		}
	}
	cfg, err := agent.Enroll(context.Background(), agent.EnrollOptions{ServerURL: in.Server, Token: in.Token, Version: "test", Root: root})
	if err != nil {
		f.t.Errorf("aanmelden %s: %v", name, err)
		return
	}
	h := agenttest.NewHost(root, addr)
	if f.setup != nil {
		f.setup(name, h)
	}
	f.group.Join(h)
	col := fakeCollector(root)
	col.Run = h.Exec
	col.Addresses = h.Addresses
	col.PrimaryAddress = func() string { return addr }
	col.Interfaces = func() []protocol.Interface {
		return []protocol.Interface{{Name: "eth0", Up: true, Addresses: []string{addr + "/24"}}}
	}
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: col,
		HeartbeatInterval: 100 * time.Millisecond, MetricsInterval: time.Hour, FactsInterval: time.Hour,
		StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"), Exec: h.Exec, Root: root,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = ag.Run(ctx) }()
	f.mu.Lock()
	f.hosts[name] = h
	f.stops = append(f.stops, func() { cancel(); <-done })
	f.mu.Unlock()
}

type template struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	ClusterType string `json:"cluster_type"`
	Params      []struct {
		Name     string  `json:"name"`
		Type     string  `json:"type"`
		Optional bool    `json:"optional"`
		Default  *string `json:"default"`
	} `json:"params"`
	Roles []struct {
		Name       string  `json:"name"`
		CountParam *string `json:"count_param"`
	} `json:"roles"`
}

type deployErr struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Field   *string `json:"field"`
}

type retryJob struct {
	job
	Retryable bool `json:"retryable"`
}

func TestDeploy(t *testing.T) {
	e, c := adminClient(t)
	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "debian-13-cf", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"scsi0": "ceph:base-9001-disk-0,size=3G", "ide2": "ceph:vm-9001-cloudinit,media=cdrom",
			"boot": "order=scsi0", "agent": "enabled=1", "name": "debian-13-cf",
		},
	})
	pve.SetAgentDelay(200 * time.Millisecond)
	f := newFleet(t, map[string]string{"web-01": "10.0.20.11", "web-02": "10.0.20.12"})
	pve.OnFileWrite(f.boot)

	var tpls struct{ Items []template }
	if s := c.do("GET", "/api/v1/templates", nil, &tpls); s != 200 || len(tpls.Items) == 0 {
		t.Fatalf("templates: %d %+v", s, tpls)
	}
	kn := tpls.Items[slices.IndexFunc(tpls.Items, func(x template) bool { return x.Name == "keepalived-nginx" })]
	if kn.ClusterType != "keepalived" || len(kn.Roles) != 1 || kn.Roles[0].CountParam == nil || *kn.Roles[0].CountParam != "node_count" {
		t.Fatalf("keepalived-nginx: %+v", kn)
	}

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}

	request := func(change func(in map[string]any)) map[string]any {
		in := map[string]any{
			"template": "keepalived-nginx",
			"cluster":  map[string]any{"name": "Web", "slug": "web", "environment": "prod"},
			"params":   map[string]any{"vip": "10.0.20.100", "auth_pass": "geheim12"},
			"target": map[string]any{
				"proxmox_id": conn.ID, "image_vmid": 9001, "network": "static",
				"first_ip": "10.0.20.11/24", "gateway": "10.0.20.1", "dns": []string{"10.0.20.1"},
				"ssh_keys": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGJvbmFzIGtleQ jonas@laptop",
			},
			"server_url": e.srv.URL,
		}
		if change != nil {
			change(in)
		}
		return in
	}
	target := func(in map[string]any) map[string]any { return in["target"].(map[string]any) }
	params := func(in map[string]any) map[string]any { return in["params"].(map[string]any) }

	for name, tc := range map[string]struct {
		change func(map[string]any)
		field  string
	}{
		"geen vip":          {func(in map[string]any) { delete(params(in), "vip") }, "params.vip"},
		"vip buiten subnet": {func(in map[string]any) { params(in)["vip"] = "10.0.30.100" }, "params.vip"},
		"vip is node":       {func(in map[string]any) { params(in)["vip"] = "10.0.20.12" }, "params.vip"},
		"geen golden image": {func(in map[string]any) { target(in)["image_vmid"] = 9000 }, "target.image_vmid"},
		"gewone VM":         {func(in map[string]any) { target(in)["image_vmid"] = 101 }, "target.image_vmid"},
		"eerste adres":      {func(in map[string]any) { target(in)["first_ip"] = "10.0.20.11" }, "target.first_ip"},
		"te weinig adressen": {func(in map[string]any) {
			target(in)["first_ip"] = "10.0.20.254/24"
		}, "target.first_ip"},
		"gateway":          {func(in map[string]any) { target(in)["gateway"] = "10.0.30.1" }, "target.gateway"},
		"ssh-sleutel":      {func(in map[string]any) { target(in)["ssh_keys"] = "geen sleutel" }, "target.ssh_keys"},
		"storage":          {func(in map[string]any) { target(in)["storage"] = "nergens" }, "target.storage"},
		"server":           {func(in map[string]any) { in["server_url"] = "" }, "server_url"},
		"template":         {func(in map[string]any) { in["template"] = "bestaat-niet" }, "template"},
		"te veel geheugen": {func(in map[string]any) { params(in)["memory"] = "30G" }, "params.memory"},
		"slug": {func(in map[string]any) {
			in["cluster"] = map[string]any{"name": "Web", "slug": "Web 1", "environment": "prod"}
		}, "cluster.slug"},
		"omgeving": {func(in map[string]any) {
			in["cluster"] = map[string]any{"name": "Web", "slug": "web", "environment": "productie"}
		}, "cluster.environment"},
	} {
		var er deployErr
		if s := c.do("POST", "/api/v1/deployments", request(tc.change), &er); s != http.StatusBadRequest || er.Field == nil || *er.Field != tc.field {
			field := "<nil>"
			if er.Field != nil {
				field = *er.Field
			}
			t.Errorf("%s: %d %s %q", name, s, field, er.Message)
		}
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM clusters").Scan(&n)
	if n != 0 {
		t.Fatalf("%d clusters na mislukte aanvragen", n)
	}

	var plan struct {
		Nodes []struct {
			Hostname  string `json:"hostname"`
			Address   string `json:"address"`
			Host      string `json:"host"`
			Cpu       int    `json:"cpu"`
			MemoryMib int    `json:"memory_mib"`
			DiskGib   int    `json:"disk_gib"`
		} `json:"nodes"`
		Vip  string `json:"vip"`
		Vrid *int   `json:"vrid"`
	}
	if s := c.do("POST", "/api/v1/deployments/plan", request(nil), &plan); s != 200 || len(plan.Nodes) != 2 ||
		plan.Nodes[0].Hostname != "web-01" || plan.Nodes[1].Address != "10.0.20.12/24" || plan.Nodes[0].Host == plan.Nodes[1].Host ||
		plan.Nodes[0].MemoryMib != 2048 || plan.Nodes[0].DiskGib != 20 || plan.Vip != "10.0.20.100" || plan.Vrid == nil || *plan.Vrid != 51 {
		t.Fatalf("plan: %d %+v", s, plan)
	}
	var er deployErr
	if s := c.do("POST", "/api/v1/deployments/plan", request(func(in map[string]any) { params(in)["node_count"] = 9 }), &er); s != 400 || er.Field == nil || *er.Field != "params.node_count" {
		t.Fatalf("plan met fout: %d %+v", s, er)
	}

	e.createUser("viewer", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("viewer", "een-lang-wachtwoord", "")
	if s := v.do("POST", "/api/v1/deployments", request(nil), nil); s != http.StatusForbidden {
		t.Fatalf("viewer: %d", s)
	}
	if s := v.do("POST", "/api/v1/deployments/plan", request(nil), nil); s != http.StatusForbidden {
		t.Fatalf("viewer plan: %d", s)
	}
	if s := v.do("GET", "/api/v1/templates", nil, nil); s != http.StatusOK {
		t.Fatalf("viewer templates: %d", s)
	}

	// De eerste uitrol loopt vast op apt op web-02.
	f.setup = func(name string, h *agenttest.Host) {
		if name == "web-02" {
			h.Fail("apt-get install", "E: Unable to fetch some archives\n")
		}
	}
	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", request(nil), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	if res.Job.Title != "Uitrollen: Web" {
		t.Fatalf("taak: %+v", res.Job)
	}
	var cl struct {
		cluster
		Type            string  `json:"type"`
		TemplateName    *string `json:"template_name"`
		TemplateVersion *string `json:"template_version"`
		SpecRevision    int     `json:"spec_revision"`
		Status          string  `json:"status"`
		Nodes           []struct {
			ID        string  `json:"id"`
			Hostname  string  `json:"hostname"`
			Lifecycle string  `json:"lifecycle"`
			PrimaryIP *string `json:"primary_ip"`
		} `json:"nodes"`
		Vips []struct {
			Address       string  `json:"address"`
			Interface     string  `json:"interface"`
			Vrid          *int    `json:"vrid"`
			OwnerHostname *string `json:"owner_hostname"`
		} `json:"vips"`
	}
	c.do("GET", "/api/v1/clusters/"+res.ClusterID, nil, &cl)
	if cl.Type != "keepalived" || cl.TemplateName == nil || *cl.TemplateName != "keepalived-nginx" || cl.SpecRevision != 1 ||
		len(cl.Nodes) != 2 || cl.Nodes[0].Lifecycle != "provisioning" || len(cl.Vips) != 1 || cl.Vips[0].Vrid == nil || *cl.Vips[0].Vrid != 51 {
		t.Fatalf("cluster tijdens de uitrol: %+v", cl)
	}

	var j retryJob
	eventually(t, "uitrol mislukt", func() bool {
		c.do("GET", "/api/v1/jobs/"+res.Job.ID, nil, &j)
		return j.Status != "queued" && j.Status != "running"
	})
	if j.Status != "failed" || !j.Retryable || !strings.Contains(j.Error, "apt-get install: Unable to fetch some archives") {
		t.Fatalf("eerste uitrol: %+v", j)
	}
	if f.host("web-01") == nil || !f.host("web-01").Installed("nginx") || f.host("web-02").Installed("nginx") {
		t.Fatal("web-01 zou klaar zijn en web-02 niet")
	}
	if s := v.do("POST", "/api/v1/jobs/"+j.ID+"/retry", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer opnieuw: %d", s)
	}
	if s := c.do("POST", "/api/v1/jobs/"+j.ID+"/retry", nil, &j); s != http.StatusAccepted || j.Status != "queued" {
		t.Fatalf("opnieuw: %d %+v", s, j)
	}
	j.job = waitJob(t, c, j.ID)
	if j.Status != "succeeded" {
		t.Fatalf("tweede poging: %+v", j)
	}
	var names []string
	for _, s := range j.Steps {
		names = append(names, s.Name+": "+s.Status)
	}
	want := []string{
		"VM web-01 maken: succeeded", "VM web-02 maken: succeeded", "Agents aanmelden: succeeded",
		"Software op web-01: succeeded", "Software op web-02: succeeded",
		"Controleren of het cluster werkt: succeeded", "Cluster in gebruik nemen: succeeded",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("stappen: %v", names)
	}
	logs := ""
	for _, s := range j.Steps {
		logs += strings.Join(s.Log, "\n") + "\n"
	}
	for _, w := range []string{
		"klonen uit debian-13-cf", "cloud-init: ip=10.0.20.11/24,gw=10.0.20.1", "schijf scsi0 van 3 naar 20 GB",
		"guest agent antwoordt", "agent aangemeld", "bestand /etc/keepalived/keepalived.conf: aangepast",
		"10.0.20.100 staat op web-01", "http://10.0.20.100/ geeft 200",
	} {
		if !strings.Contains(logs, w) {
			t.Errorf("log mist %q:\n%s", w, logs)
		}
	}
	if s := c.do("POST", "/api/v1/jobs/"+j.ID+"/retry", nil, nil); s != http.StatusConflict {
		t.Fatalf("geslaagde taak opnieuw: %d", s)
	}

	// De VM's staan verdeeld over de hosts, met de gevraagde vorm.
	vms := map[string]pvefake.Guest{}
	for id := 100; id < 110; id++ {
		if g, ok := pve.Guest(id); ok && strings.HasPrefix(g.Name, "web-") {
			vms[g.Name] = g
		}
	}
	w1, w2 := vms["web-01"], vms["web-02"]
	if len(vms) != 2 || w1.Node == w2.Node || w1.Status != "running" || w1.Config["cores"] != "2" || w1.Config["memory"] != "2048" ||
		w1.Config["scsi0"] != "ceph:base-9001-disk-0,size=20G" || w2.Config["ipconfig0"] != "ip=10.0.20.12/24,gw=10.0.20.1" ||
		w1.Config["nameserver"] != "10.0.20.1" || !strings.HasPrefix(w1.Config["sshkeys"], "ssh-ed25519 ") ||
		w1.Config["tags"] != "clusterforge;web" || w1.Config["net0"] != "virtio,bridge=vmbr0" {
		t.Fatalf("VM's: %+v", vms)
	}

	conf, err := os.ReadFile(filepath.Join(f.host("web-02").Root, "etc/keepalived/keepalived.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"priority 140", "virtual_router_id 51", "unicast_src_ip 10.0.20.12", "10.0.20.11", "auth_pass geheim12", "10.0.20.100/24 dev eth0"} {
		if !strings.Contains(string(conf), w) {
			t.Errorf("keepalived.conf mist %q:\n%s", w, conf)
		}
	}
	if u, _ := f.host("web-02").Unit("keepalived"); !u.Enabled || !u.Active {
		t.Fatalf("keepalived op web-02: %+v", u)
	}
	if o := f.group.Owner("10.0.20.100"); o != f.host("web-01") {
		t.Fatal("VIP niet op web-01")
	}

	eventually(t, "cluster gezond", func() bool {
		c.do("GET", "/api/v1/clusters/"+res.ClusterID, nil, &cl)
		return cl.Status == "healthy"
	})
	if cl.Nodes[0].Lifecycle != "active" || cl.Nodes[1].Lifecycle != "active" || cl.Vips[0].OwnerHostname == nil || *cl.Vips[0].OwnerHostname != "web-01" ||
		cl.Vips[0].Interface != "eth0" {
		t.Fatalf("cluster na de uitrol: %+v", cl)
	}
	var pn pveNode
	c.do("GET", "/api/v1/nodes/"+cl.Nodes[0].ID, nil, &pn)
	if pn.Proxmox == nil || pn.Proxmox.VMID != w1.VMID {
		t.Fatalf("Proxmox-koppeling van web-01: %+v", pn.Proxmox)
	}
	if acts := eventActions(t, e, res.ClusterID); !slices.Contains(acts, "cluster.deployed") || !slices.Contains(acts, "cluster.spec_changed") ||
		!slices.Contains(acts, "secret.created") {
		t.Fatalf("events: %v", acts)
	}
	// Wat de agents moesten doen, staat in het logboek; een bestand alleen
	// met zijn vingerafdruk.
	var files int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM events, jsonb_array_elements(payload->'steps') st
		WHERE action = 'agent.command' AND cluster_id = $1 AND st->>'kind' = 'file' AND length(st->>'fingerprint') = 64`, res.ClusterID).Scan(&files); err != nil || files == 0 {
		t.Fatalf("bestanden in agent.command: %d %v", files, err)
	}

	// Het geheim staat nergens leesbaar.
	var leaks int
	_ = e.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM jobs WHERE params::text LIKE '%geheim12%') +
		(SELECT count(*) FROM clusters WHERE spec::text LIKE '%geheim12%') +
		(SELECT count(*) FROM cluster_spec_revisions WHERE spec::text LIKE '%geheim12%') +
		(SELECT count(*) FROM events WHERE payload::text LIKE '%geheim12%') +
		(SELECT count(*) FROM jobs WHERE error LIKE '%geheim12%') +
		(SELECT count(*) FROM job_steps WHERE state::text LIKE '%geheim12%' OR array_to_string(log, ' ') LIKE '%geheim12%' OR error LIKE '%geheim12%') +
		(SELECT count(*) FROM events WHERE payload::text LIKE '%geheim-secret%')`).Scan(&leaks)
	if leaks != 0 || strings.Contains(logs, "geheim12") {
		t.Fatalf("auth_pass staat %d keer in de database of in het log", leaks)
	}

	// Dezelfde VIP of slug kan niet nog eens.
	if s := c.do("POST", "/api/v1/deployments", request(func(in map[string]any) {
		in["cluster"] = map[string]any{"name": "Web 2", "slug": "web2", "environment": "prod"}
		target(in)["first_ip"] = "10.0.20.21/24"
	}), &er); s != http.StatusBadRequest || er.Field == nil || *er.Field != "params.vip" {
		t.Fatalf("zelfde VIP: %d %+v", s, er)
	}
	if s := c.do("POST", "/api/v1/deployments", request(func(in map[string]any) {
		params(in)["vip"] = "10.0.20.101"
		target(in)["first_ip"] = "10.0.20.21/24"
	}), &er); s != http.StatusBadRequest || er.Field == nil || *er.Field != "cluster.slug" {
		t.Fatalf("zelfde slug: %d %+v", s, er)
	}
	if s := c.do("POST", "/api/v1/deployments", request(func(in map[string]any) {
		in["cluster"] = map[string]any{"name": "Web 2", "slug": "web2", "environment": "prod"}
		params(in)["vip"] = "10.0.20.101"
	}), &er); s != http.StatusBadRequest || er.Field == nil || *er.Field != "target.first_ip" {
		t.Fatalf("zelfde adressen: %d %+v", s, er)
	}
}

func TestDeployGuestAgentTimeout(t *testing.T) {
	e, c := adminClient(t)
	e.deploy.GuestAgentTimeout = 300 * time.Millisecond
	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "kaal", Node: "pve1", Status: "stopped", Template: true, NoAgent: true,
		Config: map[string]string{"scsi0": "local:base-9001-disk-0,size=10G", "ide2": "local:vm-9001-cloudinit,media=cdrom"},
	})
	var conn pveConn
	c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Lab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn)
	var res struct {
		Job job `json:"job"`
	}
	if s := c.do("POST", "/api/v1/deployments", map[string]any{
		"template":   "keepalived-nginx",
		"cluster":    map[string]any{"name": "Lab", "slug": "lab", "environment": "lab"},
		"params":     map[string]any{"vip": "10.0.20.100", "disk": "8G"},
		"target":     map[string]any{"proxmox_id": conn.ID, "image_vmid": 9001, "network": "dhcp"},
		"server_url": e.srv.URL,
	}, &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	j := waitJob(t, c, res.Job.ID)
	if j.Status != "failed" || !strings.Contains(j.Error, "guest agent antwoordt niet") {
		t.Fatalf("taak: %+v", j)
	}
	// Zonder gedeelde storage komen beide VM's op de host van de template,
	// en de schijf blijft groter dan gevraagd.
	logs := ""
	for _, s := range j.Steps {
		logs += strings.Join(s.Log, "\n") + "\n"
	}
	for _, w := range []string{"naar pve1", "cloud-init: ip=dhcp", "schijf scsi0 is 10 GB; groter dan gevraagd"} {
		if !strings.Contains(logs, w) {
			t.Errorf("log mist %q:\n%s", w, logs)
		}
	}
	if strings.Contains(logs, "naar pve2") {
		t.Errorf("kloon naar pve2 zonder gedeelde storage:\n%s", logs)
	}
}
