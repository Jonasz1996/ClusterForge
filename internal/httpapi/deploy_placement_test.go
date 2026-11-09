package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

// TestDeployPlacement kiest per node de Proxmox-host en de storage. De
// golden image staat op lokale storage van pve1, dus een VM voor pve2 wordt
// daar gekloond en daarna verhuisd.
func TestDeployPlacement(t *testing.T) {
	e, c := adminClient(t)
	pve, srv, fp := newPVE(t)
	pve.AddHost(pvefake.Host{Name: "pve3", Online: false, MaxCPU: 8, MaxMem: 32 << 30})
	pve.AddStorage(pvefake.Storage{Name: "local-lvm", Node: "pve1", Disk: 10 << 30, MaxDisk: 200 << 30, Content: "images,rootdir"})
	pve.AddStorage(pvefake.Storage{Name: "local-lvm", Node: "pve2", Disk: 10 << 30, MaxDisk: 200 << 30, Content: "images,rootdir"})
	pve.AddStorage(pvefake.Storage{Name: "fast", Node: "pve2", Disk: 0, MaxDisk: 30 << 30, Content: "images"})
	pve.AddStorage(pvefake.Storage{Name: "iso", Node: "pve2", Disk: 0, MaxDisk: 30 << 30, Content: "iso,vztmpl"})
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "debian-13-cf", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"scsi0": "local-lvm:base-9001-disk-0,size=3G", "ide2": "local-lvm:vm-9001-cloudinit,media=cdrom",
			"boot": "order=scsi0", "agent": "enabled=1", "name": "debian-13-cf",
		},
	})
	pve.SetAgentDelay(100 * time.Millisecond)
	f := newFleet(t, map[string]string{"web-01": "10.0.20.11", "web-02": "10.0.20.12"})
	pve.OnFileWrite(f.boot)

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	request := func(placement []map[string]any) map[string]any {
		return map[string]any{
			"template": "keepalived-nginx",
			"cluster":  map[string]any{"name": "Web", "slug": "web", "environment": "lab"},
			"params":   map[string]any{"vip": "10.0.20.100", "auth_pass": "geheim12"},
			"target": map[string]any{
				"proxmox_id": conn.ID, "image_vmid": 9001, "network": "static",
				"first_ip": "10.0.20.11/24", "gateway": "10.0.20.1", "placement": placement,
			},
			"server_url": e.srv.URL,
		}
	}
	type planNode struct {
		Hostname      string `json:"hostname"`
		Index         int    `json:"index"`
		Host          string `json:"host"`
		Storage       string `json:"storage"`
		HostChosen    bool   `json:"host_chosen"`
		StorageChosen bool   `json:"storage_chosen"`
	}
	var plan struct{ Nodes []planNode }

	// Zonder keuze spreidt ClusterForge, ook met een template op lokale
	// storage: local-lvm bestaat op beide hosts.
	if s := c.do("POST", "/api/v1/deployments/plan", request(nil), &plan); s != 200 || len(plan.Nodes) != 2 ||
		plan.Nodes[0].Host == plan.Nodes[1].Host || plan.Nodes[0].Storage != "local-lvm" || plan.Nodes[0].HostChosen || plan.Nodes[0].Index != 1 {
		t.Fatalf("automatisch: %d %+v", s, plan)
	}

	for name, pl := range map[string]map[string]any{
		"offline host":          {"role": "web", "index": 1, "host": "pve3"},
		"onbekende host":        {"role": "web", "index": 1, "host": "pve9"},
		"storage niet op host":  {"role": "web", "index": 1, "host": "pve1", "storage": "fast"},
		"storage zonder images": {"role": "web", "index": 1, "host": "pve2", "storage": "iso"},
		"storage te klein":      {"role": "web", "index": 1, "host": "pve2", "storage": "fast", "disk": true},
	} {
		big := pl["disk"] != nil
		delete(pl, "disk")
		in := request([]map[string]any{pl})
		if big {
			in["params"].(map[string]any)["disk"] = "40G"
		}
		var er deployErr
		if s := c.do("POST", "/api/v1/deployments/plan", in, &er); s != http.StatusBadRequest || er.Field == nil || *er.Field != "target.placement" {
			t.Errorf("%s: %d %+v", name, s, er)
		}
	}

	choice := []map[string]any{
		{"role": "web", "index": 1, "host": "pve2", "storage": "fast"},
		{"role": "web", "index": 2, "host": "pve2"},
	}
	if s := c.do("POST", "/api/v1/deployments/plan", request(choice), &plan); s != 200 ||
		plan.Nodes[0].Host != "pve2" || plan.Nodes[0].Storage != "fast" || !plan.Nodes[0].HostChosen || !plan.Nodes[0].StorageChosen ||
		plan.Nodes[1].Host != "pve2" || plan.Nodes[1].Storage != "local-lvm" || plan.Nodes[1].StorageChosen {
		t.Fatalf("gekozen: %d %+v", s, plan)
	}

	var res struct {
		Job job `json:"job"`
	}
	if s := c.do("POST", "/api/v1/deployments", request(choice), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	j := waitJob(t, c, res.Job.ID)
	logs := ""
	for _, s := range j.Steps {
		logs += strings.Join(s.Log, "\n") + "\n"
	}
	if j.Status != "succeeded" {
		t.Fatalf("uitrol: %+v\n%s", j, logs)
	}
	for _, w := range []string{"klonen uit debian-13-cf naar pve1", "verhuizen van pve1 naar pve2", "draait op pve2"} {
		if !strings.Contains(logs, w) {
			t.Errorf("log mist %q:\n%s", w, logs)
		}
	}
	vms := map[string]pvefake.Guest{}
	for id := 100; id < 120; id++ {
		if g, ok := pve.Guest(id); ok && strings.HasPrefix(g.Name, "web-") {
			vms[g.Name] = g
		}
	}
	if w1, w2 := vms["web-01"], vms["web-02"]; w1.Node != "pve2" || w2.Node != "pve2" ||
		!strings.HasPrefix(w1.Config["scsi0"], "fast:") || !strings.HasPrefix(w2.Config["scsi0"], "local-lvm:") || w1.Status != "running" {
		t.Fatalf("VM's: %+v", vms)
	}
}
