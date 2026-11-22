package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

// leren is een Proxmox met een golden image, machines die opstarten als
// ClusterForge een VM maakt, en een nep-API van Kubernetes waarin elke
// machine die opstart als node Ready is.
type leren struct {
	pve    *pvefake.Server
	f      *fleet
	fake   *kubefake.Server
	connID string
	// request is de aanvraag voor het cluster Leren met params erbij.
	request func(params map[string]any) map[string]any

	mu sync.Mutex
	// broken zijn de machines waar k3s na een (her)start niet opkomt.
	broken map[string]bool
}

var installVersionRe = regexp.MustCompile(`(?m)^VERSION='([^']+)'$`)

// simK3s speelt k3s na op een machine: het installatiescript zet het
// merkteken met de versie uit het script, en een start of herstart van k3s
// of k3s-agent laat de node in Kubernetes die versie melden, zoals de
// kubelet na een herstart. Op een kapotte machine faalt de unit dan en
// meldt de node de oude versie.
func (l *leren) simK3s(t *testing.T, name string, h *agenttest.Host) {
	var mu sync.Mutex
	installed := ""
	h.SetAfter(func(call string, err error) {
		if err != nil {
			return
		}
		if call == "sh -c /usr/local/lib/clusterforge/install-k3s.sh" {
			b, err := os.ReadFile(filepath.Join(h.Root, "usr/local/lib/clusterforge/install-k3s.sh"))
			m := installVersionRe.FindSubmatch(b)
			if err != nil || m == nil {
				t.Errorf("install-k3s.sh op %s zonder versie: %v", name, err)
				return
			}
			mu.Lock()
			installed = string(m[1])
			mu.Unlock()
			if err := os.WriteFile(filepath.Join(h.Root, "usr/local/lib/clusterforge/k3s-"+string(m[1])), nil, 0o644); err != nil {
				t.Error(err)
			}
			return
		}
		verb, unit, ok := strings.Cut(strings.TrimPrefix(call, "systemctl "), " ")
		if !ok || !strings.HasPrefix(call, "systemctl ") || (verb != "start" && verb != "restart") || (unit != "k3s" && unit != "k3s-agent") {
			return
		}
		l.mu.Lock()
		broken := l.broken[name]
		l.mu.Unlock()
		if broken {
			h.SetUnit(unit, agenttest.Unit{Enabled: true, Failed: true})
			return
		}
		mu.Lock()
		v := installed
		mu.Unlock()
		if v != "" {
			l.fake.SetNodeVersion(name, v)
		}
	})
}

// breakK3s laat k3s op een machine na een (her)start falen, of niet meer.
func (l *leren) breakK3s(name string, broken bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.broken[name] = broken
}

func newLeren(t *testing.T, e *testEnv, c *client) *leren {
	t.Helper()
	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "debian-13-cf", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"scsi0": "ceph:base-9001-disk-0,size=3G", "ide2": "ceph:vm-9001-cloudinit,media=cdrom",
			"boot": "order=scsi0", "agent": "enabled=1", "name": "debian-13-cf",
		},
	})
	pve.SetAgentDelay(100 * time.Millisecond)
	f := newFleet(t, map[string]string{"leren-server-01": "10.0.40.11", "leren-worker-01": "10.0.40.12", "leren-worker-02": "10.0.40.13"})
	// k3s schrijft k3s.yaml op de server; hier wijst die naar de nep-API,
	// waar de drie nodes Ready zijn.
	fake := kubefake.New()
	fake.Start()
	t.Cleanup(fake.Close)
	fake.SeedK3s(
		kubefake.NodeSpec{Name: "leren-server-01", IP: "10.0.40.11", Server: true},
		kubefake.NodeSpec{Name: "leren-worker-01", IP: "10.0.40.12"},
		kubefake.NodeSpec{Name: "leren-worker-02", IP: "10.0.40.13"},
	)
	l := &leren{pve: pve, f: f, fake: fake, broken: map[string]bool{}}
	// Een node die er later bij komt, meldt zich Ready zodra hij opstart.
	f.setup = func(name string, h *agenttest.Host) {
		l.simK3s(t, name, h)
		if name == "leren-server-01" {
			if err := fake.WriteKubeconfig(h.Root); err != nil {
				t.Error(err)
			}
		}
		if fake.Get("v1", "Node", "", name) == nil {
			fake.Add(kubefake.Node(kubefake.NodeSpec{Name: name, IP: h.Address, Server: strings.Contains(name, "-server-")}))
		}
	}
	pve.OnFileWrite(f.boot)
	// Traefik antwoordt 404 op het VIP.
	e.deploy.HTTPGet = func(context.Context, string) (int, error) { return http.StatusNotFound, nil }

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	l.connID = conn.ID
	l.request = func(params map[string]any) map[string]any {
		p := map[string]any{"vip": "10.0.40.100"}
		for k, v := range params {
			p[k] = v
		}
		return map[string]any{
			"template": "kubernetes",
			"cluster":  map[string]any{"name": "Leren", "slug": "leren", "environment": "lab"},
			"params":   p,
			"target": map[string]any{
				"proxmox_id": conn.ID, "image_vmid": 9001, "network": "static",
				"first_ip": "10.0.40.11/24", "gateway": "10.0.40.1", "dns": []string{"10.0.40.1"},
			},
			"server_url": e.srv.URL,
		}
	}
	return l
}

// deploy rolt Leren uit en geeft het cluster en de taak.
func (l *leren) deploy(t *testing.T, c *client, params map[string]any) (string, job) {
	t.Helper()
	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", l.request(params), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	return res.ClusterID, waitJob(t, c, res.Job.ID)
}

// Een Kubernetes-cluster in de vorm Leren: één server en twee workers. De
// server begint het cluster en krijgt het VIP, de workers melden zich aan
// via het VIP, en Traefik antwoordt 404 op het VIP.
func TestDeployKubernetes(t *testing.T) {
	e, c := adminClient(t)
	l := newLeren(t, e, c)
	f, request := l.f, l.request
	var mu sync.Mutex
	var asked []string
	e.deploy.HTTPGet = func(_ context.Context, u string) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, u)
		return http.StatusNotFound, nil
	}

	type choiceParam struct {
		Name    string `json:"name"`
		Choices []int  `json:"choices"`
	}
	type kubeTemplate struct {
		template
		Params []choiceParam `json:"params"`
	}
	var tpls struct{ Items []kubeTemplate }
	c.do("GET", "/api/v1/templates", nil, &tpls)
	i := slices.IndexFunc(tpls.Items, func(x kubeTemplate) bool { return x.Name == "kubernetes" })
	if i < 0 {
		t.Fatal("geen template kubernetes")
	}
	kt := tpls.Items[i]
	servers := slices.IndexFunc(kt.Params, func(p choiceParam) bool { return p.Name == "servers" })
	if kt.ClusterType != "kubernetes" || len(kt.Roles) != 2 || *kt.Roles[0].CountParam != "servers" || *kt.Roles[1].CountParam != "workers" ||
		servers < 0 || !slices.Equal(kt.Params[servers].Choices, []int{1, 3, 5}) {
		t.Fatalf("template kubernetes: %+v", kt)
	}

	var er deployErr
	if s := c.do("POST", "/api/v1/deployments/plan", request(map[string]any{"servers": 2}), &er); s != 400 || er.Field == nil ||
		*er.Field != "params.servers" || !strings.Contains(er.Message, "kies 1, 3 of 5") {
		t.Fatalf("twee servers: %d %+v", s, er)
	}
	type plan struct {
		Nodes []struct {
			Hostname  string `json:"hostname"`
			Address   string `json:"address"`
			MemoryMib int    `json:"memory_mib"`
			DiskGib   int    `json:"disk_gib"`
		} `json:"nodes"`
	}
	var p plan
	if s := c.do("POST", "/api/v1/deployments/plan", request(map[string]any{"workers": 0}), &p); s != 200 || len(p.Nodes) != 1 || p.Nodes[0].Hostname != "leren-server-01" {
		t.Fatalf("zonder workers: %d %+v", s, p)
	}
	if s := c.do("POST", "/api/v1/deployments/plan", request(nil), &p); s != 200 || len(p.Nodes) != 3 ||
		p.Nodes[0].Hostname != "leren-server-01" || p.Nodes[0].DiskGib != 30 || p.Nodes[1].Hostname != "leren-worker-01" ||
		p.Nodes[2].Address != "10.0.40.13/24" || p.Nodes[2].MemoryMib != 4096 || p.Nodes[2].DiskGib != 40 {
		t.Fatalf("plan: %d %+v", s, p)
	}

	clusterID, j := l.deploy(t, c, nil)
	var names []string
	logs := ""
	for _, s := range j.Steps {
		names = append(names, s.Name)
		logs += strings.Join(s.Log, "\n") + "\n"
	}
	if j.Status != "succeeded" {
		t.Fatalf("uitrol: %+v\n%s", j, logs)
	}
	if want := []string{
		"VM leren-server-01 maken", "VM leren-worker-01 maken", "VM leren-worker-02 maken", "Agents aanmelden",
		"Software op leren-server-01", "Software op leren-worker-01", "Software op leren-worker-02",
		"Controleren of het cluster werkt", "Cluster in gebruik nemen",
	}; !slices.Equal(names, want) {
		t.Fatalf("stappen: %v", names)
	}
	for _, w := range []string{"bestand /etc/rancher/k3s/config.yaml: aangepast", "service k3s: aangepast", "service k3s-agent: aangepast",
		"10.0.40.100 staat op leren-server-01", "alle 3 nodes zijn Ready", "http://10.0.40.100/ geeft 404"} {
		if !strings.Contains(logs, w) {
			t.Errorf("log mist %q:\n%s", w, logs)
		}
	}
	mu.Lock()
	if !slices.Contains(asked, "http://10.0.40.100/") {
		t.Fatalf("gevraagd: %v", asked)
	}
	mu.Unlock()

	// Het token staat in config.yaml op elke node, en nergens in de logs.
	server, worker := f.host("leren-server-01"), f.host("leren-worker-02")
	cfg, err := os.ReadFile(filepath.Join(worker.Root, "etc/rancher/k3s/config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "server: https://10.0.40.100:6443") || !strings.Contains(string(cfg), "node-ip: 10.0.40.13") {
		t.Fatalf("config van worker-02:\n%s", cfg)
	}
	line := strings.Split(string(cfg), "token: \"")[1]
	token := line[:strings.Index(line, "\"")]
	if len(token) != 48 || strings.Contains(logs, token) {
		t.Fatalf("token %q in de logs of met de verkeerde lengte", token)
	}
	scfg, _ := os.ReadFile(filepath.Join(server.Root, "etc/rancher/k3s/config.yaml"))
	if !strings.Contains(string(scfg), "cluster-init: true") || !strings.Contains(string(scfg), token) {
		t.Fatalf("config van de server:\n%s", scfg)
	}
	if u, _ := server.Unit("k3s"); !u.Enabled || !u.Active {
		t.Fatalf("k3s op de server: %+v", u)
	}
	if u, _ := worker.Unit("k3s-agent"); !u.Enabled || !u.Active {
		t.Fatalf("k3s-agent op worker-02: %+v", u)
	}
	if _, ok := worker.Unit("keepalived"); ok {
		t.Fatal("keepalived op een worker")
	}
	if o := f.group.Owner("10.0.40.100"); o != server {
		t.Fatal("VIP niet op de server")
	}

	var cl struct {
		cluster
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	eventually(t, "cluster gezond", func() bool {
		c.do("GET", "/api/v1/clusters/"+clusterID, nil, &cl)
		return cl.Status == "healthy"
	})
	if cl.Type != "kubernetes" {
		t.Fatalf("type: %+v", cl)
	}
	var dg depGraph
	c.do("GET", "/api/v1/dependency-graph?cluster_id="+clusterID, nil, &dg)
	if got := strings.Join(dg.names(), " "); got != "Leren/k3s Leren/k3s-agent Leren/keepalived" || dg.svc(t, "Leren", "k3s").Status != "healthy" ||
		dg.svc(t, "Leren", "k3s-agent").Status != "healthy" {
		t.Fatalf("diensten: %s %+v", got, dg)
	}
}
