package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

// Een Kubernetes-cluster in de vorm Leren: één server en twee workers. De
// server begint het cluster en krijgt het VIP, de workers melden zich aan
// via het VIP, en Traefik antwoordt 404 op het VIP.
func TestDeployKubernetes(t *testing.T) {
	e, c := adminClient(t)
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
	pve.OnFileWrite(f.boot)
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

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	request := func(params map[string]any) map[string]any {
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

	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", request(nil), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	j := waitJob(t, c, res.Job.ID)
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
		"10.0.40.100 staat op leren-server-01", "http://10.0.40.100/ geeft 404"} {
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
		c.do("GET", "/api/v1/clusters/"+res.ClusterID, nil, &cl)
		return cl.Status == "healthy"
	})
	if cl.Type != "kubernetes" {
		t.Fatalf("type: %+v", cl)
	}
	var dg depGraph
	c.do("GET", "/api/v1/dependency-graph?cluster_id="+res.ClusterID, nil, &dg)
	if got := strings.Join(dg.names(), " "); got != "Leren/k3s Leren/k3s-agent Leren/keepalived" || dg.svc(t, "Leren", "k3s").Status != "healthy" ||
		dg.svc(t, "Leren", "k3s-agent").Status != "healthy" {
		t.Fatalf("diensten: %s %+v", got, dg)
	}
}
