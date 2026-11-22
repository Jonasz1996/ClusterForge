package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// pveSim speelt pct, pveam en pvesh na op Proxmox-host pve1, bovenop de
// nep-API: een gemaakte CT staat daarna ook in Proxmox.
type pveSim struct {
	t    *testing.T
	pve  *pvefake.Server
	root string
	// onEnroll krijgt het aanmeldbestand dat in een CT gezet wordt.
	onEnroll func(name string, e agent.EnrollFile)
	// extra beantwoordt commando's voor de rest (fase 26); ok false laat
	// ze aan de gewone naspeling.
	extra func(c string, out io.Writer) (code int, ok bool)
	// console maakt het programma van een console (M7); nil laat de agent
	// het echte maken.
	console func(req protocol.ConsoleRequest, pw string) *exec.Cmd

	mu    sync.Mutex
	calls []string
	ids   map[int]string
}

func (p *pveSim) write(path, body string) {
	full := filepath.Join(p.root, path)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		p.t.Error(err)
	}
}

func (p *pveSim) vmlist() {
	ids := map[string]any{}
	for _, id := range p.pve.Guests() {
		g, _ := p.pve.Guest(id)
		ids[strconv.Itoa(id)] = map[string]any{"node": g.Node, "type": g.Type}
	}
	b, _ := json.Marshal(map[string]any{"ids": ids})
	p.write("/etc/pve/.vmlist", string(b))
}

func (p *pveSim) setup() {
	p.ids = map[int]string{}
	p.write("/usr/bin/pveversion", "")
	p.write("/etc/pve/.members", `{"nodename":"pve1","cluster":{"name":"thuislab"}}`)
	p.write("/root/.ssh/id_rsa.pub", "ssh-rsa AAAAB3NzaC1yc2EHOST root@pve1\n")
	p.write("/sys/class/net/vmbr0/bridge/stp_state", "0")
	p.vmlist()
}

func (p *pveSim) called(prefix string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, c := range p.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func flag(argv []string, name string) string {
	for i, a := range argv {
		if a == name && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// exec voor de leesverzoeken van de agent (pvesh, pveversion).
func (p *pveSim) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	var buf strings.Builder
	code, _ := p.cmd(context.Background(), "", append([]string{name}, args...), nil, &buf)
	if code != 0 {
		return []byte(buf.String()), fmt.Errorf("exit %d", code)
	}
	return []byte(buf.String()), nil
}

func (p *pveSim) cmd(_ context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
	c := strings.Join(argv, " ")
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.mu.Unlock()
	if p.extra != nil {
		if code, ok := p.extra(c, out); ok {
			return code, nil
		}
	}
	tmpl := "debian-13-standard_13.1-2_" + runtime.GOARCH + ".tar.zst"
	id := 0
	if len(argv) > 2 {
		id, _ = strconv.Atoi(argv[2])
	}
	switch {
	case c == "pveversion":
		_, _ = io.WriteString(out, "pve-manager/9.0.10/abc (running kernel: 6.14)\n")
	case strings.HasPrefix(c, "pvesh get /nodes/pve1/lxc ") || strings.HasPrefix(c, "pvesh get /nodes/pve1/qemu "):
		typ := strings.Split(argv[2], "/")[3]
		var list []map[string]any
		for _, vmid := range p.pve.Guests() {
			if g, _ := p.pve.Guest(vmid); g.Node == "pve1" && g.Type == typ {
				list = append(list, map[string]any{"vmid": vmid, "status": g.Status, "name": g.Name})
			}
		}
		b, _ := json.Marshal(list)
		_, _ = out.Write(b)
	case strings.HasPrefix(c, "pvesh get /nodes/pve1/storage/local/content"):
		_, _ = io.WriteString(out, `[{"volid":"local:vztmpl/`+tmpl+`","size":1}]`)
	case strings.HasPrefix(c, "pvesh get /nodes/pve1/storage "):
		_, _ = io.WriteString(out, `[{"storage":"local","type":"dir","content":"vztmpl,rootdir","active":1,"enabled":1,"total":100,"avail":90}]`)
	case c == "pveam list local":
		_, _ = io.WriteString(out, "NAME SIZE\nlocal:vztmpl/"+tmpl+" 120MB\n")
	case strings.HasPrefix(c, "pveam "):
	case strings.HasPrefix(c, "pct status "):
		g, ok := p.pve.Guest(id)
		if !ok {
			_, _ = io.WriteString(out, "Configuration file 'nodes/pve1/lxc/"+argv[2]+".conf' does not exist\n")
			return 2, nil
		}
		_, _ = io.WriteString(out, "status: "+g.Status+"\n")
	case strings.HasPrefix(c, "pct create "):
		name := flag(argv, "--hostname")
		rootfs := flag(argv, "--rootfs")
		st, size, _ := strings.Cut(rootfs, ":")
		cores, _ := strconv.Atoi(flag(argv, "--cores"))
		mem, _ := strconv.Atoi(flag(argv, "--memory"))
		keys, _ := os.ReadFile(flag(argv, "--ssh-public-keys"))
		cfg := map[string]string{
			"hostname": name, "net0": flag(argv, "--net0"), "rootfs": fmt.Sprintf("%s:vm-%d-disk-0,size=%sG", st, id, size),
			"features": flag(argv, "--features"), "onboot": flag(argv, "--onboot"), "nameserver": flag(argv, "--nameserver"),
			"tags": flag(argv, "--tags"), "unprivileged": flag(argv, "--unprivileged"), "ssh-keys": string(keys), "ostemplate": argv[3],
		}
		p.pve.AddGuest(pvefake.Guest{Type: "lxc", VMID: id, Name: name, Node: "pve1", Status: "stopped", MaxCPU: cores, MaxMem: int64(mem) << 20, Config: cfg})
		p.write(fmt.Sprintf("/etc/pve/nodes/pve1/lxc/%d.conf", id), "hostname: "+name+"\n")
		p.mu.Lock()
		p.ids[id] = name
		p.mu.Unlock()
		p.vmlist()
	case strings.HasPrefix(c, "pct start "):
		p.pve.SetGuestStatus(id, "running", "pve1")
	case strings.HasPrefix(c, "pct exec ") && strings.HasSuffix(c, "test -s /etc/clusterforge/agent.json"):
		return 1, nil
	case strings.HasPrefix(c, "pct push ") && argv[4] == "/etc/clusterforge/enroll.json":
		data, err := os.ReadFile(argv[3])
		if err != nil {
			return 1, err
		}
		var e agent.EnrollFile
		if err := json.Unmarshal(data, &e); err != nil {
			return 1, err
		}
		p.mu.Lock()
		name := p.ids[id]
		p.mu.Unlock()
		go p.onEnroll(name, e)
	case strings.HasPrefix(c, "ssh-keyscan"):
		return 1, nil
	}
	return 0, nil
}

// startPVEHost meldt een agent aan op Proxmox-host pve1, met pct en
// pveam nagespeeld.
func startPVEHost(t *testing.T, e *testEnv, c *client, sim *pveSim, bin []byte) string {
	t.Helper()
	var host node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "pve1"}, &host); s != http.StatusCreated {
		t.Fatalf("node pve1: %d", s)
	}
	startAgentWith(t, e, c, host.ID, strings.Repeat("9", 32), &addrs{}, func(ag *agent.Agent, root string) {
		sim.root = root
		sim.setup()
		ag.Root = root
		ag.Exec = sim.exec
		ag.Collector.Run = sim.exec
		ag.ConsoleCommand = sim.console
		ex := &agent.RunExecutor{Root: root, Cmd: sim.cmd, Stderr: io.Discard,
			Sleep: func(context.Context, time.Duration) error { return nil },
			Download: func(_ context.Context, _ string, w io.Writer) error {
				_, err := w.Write(bin)
				return err
			}}
		ag.StartRun = func(_ context.Context, id string) error {
			go ex.Main(context.Background(), id)
			return nil
		}
	})
	return host.ID
}

// TestDeployNewCT: een cluster op nieuwe CT's die de agent op de
// Proxmox-host maakt, met per node een eigen nummer, adres en vorm. De host
// downloadt cf-agent, zet hem met een aanmeldbestand in de CT, en de uitrol
// gaat verder zoals altijd.
func TestDeployNewCT(t *testing.T) {
	e, c := adminClient(t)
	pve, srv, fp := newPVE(t)
	bin := []byte("nep-cf-agent")
	sum := sha256.Sum256(bin)
	e.pvh.AgentSHA256 = func() map[string]string { return map[string]string{runtime.GOARCH: hex.EncodeToString(sum[:])} }

	addrs := map[string]string{"web-01": "10.0.20.11", "web-02": "10.0.20.15"}
	f := newFleet(t, addrs)
	sim := &pveSim{t: t, pve: pve}
	sim.onEnroll = func(name string, in agent.EnrollFile) {
		f.start(name, func(root, _ string) (*agent.Config, error) {
			return agent.Enroll(context.Background(), agent.EnrollOptions{ServerURL: in.Server, Token: in.Token, AllowHTTP: in.AllowHTTP, Version: "test", Root: root})
		})
	}
	hostID := startPVEHost(t, e, c, sim, bin)

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}

	// De host verschijnt met zijn agent bij de koppeling; pve2 heeft er geen.
	var hosts struct {
		Hosts []struct {
			NodeID         string `json:"node_id"`
			PVENode        string `json:"pve_node"`
			Online         bool   `json:"online"`
			Supported      bool   `json:"supported"`
			ConnectionName string `json:"connection_name"`
		} `json:"hosts"`
		Missing []struct {
			PVENode string `json:"pve_node"`
		} `json:"missing"`
	}
	eventually(t, "Proxmox-host met agent", func() bool {
		c.do("GET", "/api/v1/pve-hosts", nil, &hosts)
		return len(hosts.Hosts) == 1 && hosts.Hosts[0].Online && hosts.Hosts[0].ConnectionName == "Thuislab"
	})
	if h := hosts.Hosts[0]; h.NodeID != hostID || h.PVENode != "pve1" || !h.Supported || len(hosts.Missing) != 1 || hosts.Missing[0].PVENode != "pve2" {
		t.Fatalf("hosts: %+v", hosts)
	}
	var inv struct {
		Node      string `json:"node"`
		Templates []struct {
			Volid string `json:"volid"`
		} `json:"templates"`
		SSHKeys []string `json:"ssh_keys"`
	}
	if s := c.do("GET", "/api/v1/pve-hosts/"+hostID+"/inventory", nil, &inv); s != 200 || inv.Node != "pve1" || len(inv.Templates) != 1 || len(inv.SSHKeys) != 1 {
		t.Fatalf("inventaris: %d %+v", s, inv)
	}

	request := func(change func(target map[string]any)) map[string]any {
		target := map[string]any{
			"proxmox_id": conn.ID, "create": "ct", "network": "static", "vlan": 199,
			"first_ip": "10.0.20.11/24", "dns": []string{"10.0.20.1"},
			"ssh_keys": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGJvbmFzIGtleQ jonas@laptop",
			"placement": []map[string]any{
				{"role": "web", "index": 1, "vmid": 150, "cpu": 3},
				{"role": "web", "index": 2, "address": "10.0.20.15", "memory_mib": 3072, "disk_gib": 12},
			},
		}
		if change != nil {
			change(target)
		}
		return map[string]any{
			"template": "keepalived-nginx",
			"cluster":  map[string]any{"name": "Web", "slug": "web", "environment": "lab"},
			"params":   map[string]any{"vip": "10.0.20.100", "auth_pass": "geheim12"},
			"target":   target, "server_url": e.srv.URL,
		}
	}

	// Netwerkregels: nooit op .1-.3, en een vrij nummer.
	for name, tc := range map[string]struct {
		change func(map[string]any)
		field  string
	}{
		"eerste adres .2": {func(tg map[string]any) { tg["first_ip"] = "10.0.20.2/24" }, "target.first_ip"},
		"adres .3": {func(tg map[string]any) {
			tg["placement"] = []map[string]any{{"role": "web", "index": 2, "address": "10.0.20.3"}}
		}, "target.placement"},
		"nummer bezet": {func(tg map[string]any) {
			tg["placement"] = []map[string]any{{"role": "web", "index": 1, "vmid": 101}}
		}, "target.placement"},
		"host zonder agent": {func(tg map[string]any) {
			tg["placement"] = []map[string]any{{"role": "web", "index": 1, "host": "pve2"}}
		}, "target.placement"},
	} {
		var er deployErr
		if s := c.do("POST", "/api/v1/deployments/plan", request(tc.change), &er); s != http.StatusBadRequest || er.Field == nil || *er.Field != tc.field {
			t.Errorf("%s: %d %+v", name, s, er)
		}
	}
	var er deployErr
	if s := c.do("POST", "/api/v1/deployments/plan", map[string]any{
		"template": "keepalived-nginx", "cluster": map[string]any{"name": "Web", "slug": "web", "environment": "lab"},
		"params": map[string]any{"vip": "10.0.20.2", "auth_pass": "geheim12"}, "target": request(nil)["target"], "server_url": e.srv.URL,
	}, &er); s != http.StatusBadRequest || er.Field == nil || *er.Field != "params.vip" {
		t.Errorf("VIP op .2: %d %+v", s, er)
	}

	var plan struct {
		Nodes []struct {
			Hostname   string `json:"hostname"`
			Address    string `json:"address"`
			Host       string `json:"host"`
			Storage    string `json:"storage"`
			CPU        int    `json:"cpu"`
			MemoryMiB  int    `json:"memory_mib"`
			DiskGiB    int    `json:"disk_gib"`
			VMID       int    `json:"vmid"`
			VMIDChosen bool   `json:"vmid_chosen"`
		} `json:"nodes"`
	}
	if s := c.do("POST", "/api/v1/deployments/plan", request(nil), &plan); s != 200 || len(plan.Nodes) != 2 {
		t.Fatalf("plan: %d %+v", s, plan)
	}
	n1, n2 := plan.Nodes[0], plan.Nodes[1]
	if n1.VMID != 150 || !n1.VMIDChosen || n1.CPU != 3 || n1.Host != "pve1" || n1.Address != "10.0.20.11/24" || n1.Storage == "" {
		t.Fatalf("plan web-01: %+v", n1)
	}
	if n2.VMID != 100 || n2.VMIDChosen || n2.Address != "10.0.20.15/24" || n2.MemoryMiB != 3072 || n2.DiskGiB != 12 || n2.Host != "pve1" {
		t.Fatalf("plan web-02: %+v", n2)
	}

	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", request(nil), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	if j := waitJobLong(t, c, res.Job.ID); j.Status != "succeeded" {
		var full struct {
			Steps []struct {
				Name   string   `json:"name"`
				Output []string `json:"output"`
			} `json:"steps"`
		}
		c.do("GET", "/api/v1/jobs/"+res.Job.ID, nil, &full)
		t.Fatalf("uitrol: %s %s\n%+v\n%s", j.Status, j.Error, full, strings.Join(sim.called(""), "\n"))
	}

	// Proxmox kreeg de CT's met de gekozen vorm, en de host zijn sleutel erin.
	for name, want := range map[string]struct {
		vmid  int
		cores int
		mem   int64
		net0  string
		disk  string
	}{
		"web-01": {150, 3, 0, "name=eth0,bridge=vmbr0,tag=199,ip=10.0.20.11/24,gw=10.0.20.1", ""},
		"web-02": {100, 0, 3072 << 20, "name=eth0,bridge=vmbr0,tag=199,ip=10.0.20.15/24,gw=10.0.20.1", "size=12G"},
	} {
		g, ok := pve.Guest(want.vmid)
		if !ok || g.Name != name || g.Status != "running" || g.Config["net0"] != want.net0 || g.Config["onboot"] != "1" ||
			g.Config["unprivileged"] != "1" || g.Config["features"] != "nesting=1" || g.Config["tags"] != "clusterforge;web" ||
			!strings.Contains(g.Config["ssh-keys"], "root@pve1") || !strings.Contains(g.Config["ssh-keys"], "jonas@laptop") ||
			(want.cores > 0 && g.MaxCPU != want.cores) || (want.mem > 0 && g.MaxMem != want.mem) ||
			(want.disk != "" && !strings.Contains(g.Config["rootfs"], want.disk)) {
			t.Fatalf("%s: %+v", name, g)
		}
	}
	if got := sim.called("pct push 150 "); len(got) < 4 {
		t.Fatalf("cf-agent niet in de CT gezet: %v", got)
	}
	// De nodes zijn gekoppeld aan hun CT.
	var vmids []int
	rows, err := e.pool.Query(context.Background(), "SELECT pve_vmid FROM nodes WHERE hostname LIKE 'web-%' ORDER BY hostname")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		_ = rows.Scan(&v)
		vmids = append(vmids, v)
	}
	if !slices.Equal(vmids, []int{150, 100}) {
		t.Fatalf("pve_vmid: %v", vmids)
	}

	// Een actie op de host via de GUI: een snapshot, en verwijderen van
	// een CT van een clusternode wordt geweigerd.
	var j job
	if s := c.do("POST", "/api/v1/pve-hosts/"+hostID+"/actions", map[string]any{
		"op": map[string]any{"action": "snapshot", "vmid": 150, "snapshot": "voor-test"},
	}, &j); s != http.StatusAccepted {
		t.Fatalf("snapshot: %d", s)
	}
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" || len(sim.called("pct snapshot 150 voor-test")) != 1 {
		t.Fatalf("snapshot: %s %s", j.Status, j.Error)
	}
	pve.SetGuestStatus(150, "stopped", "pve1")
	var de deployErr
	if s := c.do("POST", "/api/v1/pve-hosts/"+hostID+"/actions", map[string]any{
		"op": map[string]any{"action": "destroy", "vmid": 150}, "confirm": "web-01",
	}, &de); s != http.StatusBadRequest || !strings.Contains(de.Message, "cluster Web") {
		t.Fatalf("verwijderen: %d %+v", s, de)
	}
}
