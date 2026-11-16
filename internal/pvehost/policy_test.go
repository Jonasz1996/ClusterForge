package pvehost

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPolicy(t *testing.T) {
	cases := []struct {
		method, path string
		params       map[string]string
		title        string
		confirm      string
		secret       []string
		err          string
	}{
		{method: "create", path: "/cluster/ha/resources", params: map[string]string{"sid": "vm:101", "state": "started"}, title: "vm:101 onder HA zetten"},
		{method: "create", path: "/nodes/pve1/qemu", params: map[string]string{"vmid": "120", "archive": "pbs:backup/vm/120/x", "force": "1"},
			title: "VM 120 terugzetten uit back-up op pve1", confirm: "120"},
		{method: "create", path: "/nodes/pve1/qemu", params: map[string]string{"vmid": "120", "cores": "2", "args": "-x"}, err: "voert code uit"},
		{method: "create", path: "/nodes/pve1/vzdump", params: map[string]string{"vmid": "120", "script": "/tmp/x.sh"}, err: "voert code uit"},
		{method: "create", path: "/cluster/backup", params: map[string]string{"all": "1", "schedule": "02:00", "storage": "pbs", "prune-backups": "keep-daily=7"}, title: "back-upjob maken"},
		{method: "set", path: "/nodes/pve1/qemu/101/config", params: map[string]string{"hostpci0": "0000:01:00,pcie=1", "cipassword": "x"},
			title: "VM 101 configuratie wijzigen", secret: []string{"cipassword"}},
		{method: "delete", path: "/nodes/pve1/storage/pbs/content/pbs:backup/vm/101/2026-11-16T01:00:00Z", title: "pbs:backup/vm/101/2026-11-16T01:00:00Z verwijderen",
			confirm: "pbs:backup/vm/101/2026-11-16T01:00:00Z"},
		{method: "create", path: "/nodes/pve1/status", params: map[string]string{"command": "reboot"}, title: "pve1 herstarten", confirm: "pve1"},
		{method: "create", path: "/storage", params: map[string]string{"storage": "pbs", "type": "pbs", "password": "x"}, title: "storage pbs toevoegen", secret: []string{"password"}},
		{method: "delete", path: "/access/users/root@pam", err: "root@pam"},
		{method: "create", path: "/nodes/pve1/execute", err: "kent"},
		{method: "create", path: "/cluster/ha/resources/vm:101/migrate", params: map[string]string{"node": "pve2", "x": "1"}, err: "hoort niet"},
		{method: "set", path: "/nodes/pve1/lxc/105/config", params: map[string]string{"delete": "mp1,unused0"}, title: "CT 105 configuratie wijzigen", confirm: "105"},
		{method: "create", path: "/nodes/pve1/qemu/101/snapshot/voor-update/rollback", title: "VM 101 terugzetten naar snapshot voor-update", confirm: "voor-update"},
		{method: "set", path: "/cluster/firewall/options", params: map[string]string{"enable": "1"}, title: "firewall van het datacenter wijzigen"},
		{method: "delete", path: "/nodes/pve1/qemu/101/firewall/rules/3", title: "firewall van VM 101 wijzigen"},
	}
	for _, c := range cases {
		a, err := check(c.method, c.path, c.params)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s %s: fout %v, wil %q", c.method, c.path, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %s: %v", c.method, c.path, err)
			continue
		}
		if a.Title != c.title || a.Confirm != c.confirm || strings.Join(a.Secret, ",") != strings.Join(c.secret, ",") {
			t.Errorf("%s %s: %+v", c.method, c.path, a)
		}
	}
	if !readDenied("/access/users") || !readDenied("/cluster/acme/plugins") || readDenied("/cluster/resources") {
		t.Error("readDenied klopt niet")
	}
}

func TestAnalyzeOverview(t *testing.T) {
	var in overviewInput
	in.dc = Datacenter{ID: "homelab", Name: "homelab", Hosts: []Host{{NodeID: uuid.New(), PVENode: "pve1", Online: true, Supported: true, Protocol: 35}}}
	must := func(s string, v any) {
		if err := json.Unmarshal([]byte(s), v); err != nil {
			t.Fatal(err)
		}
	}
	must(`[{"type":"cluster","name":"homelab","quorate":1,"version":7},
	       {"type":"node","name":"pve1","online":1,"ip":"192.168.1.11"},
	       {"type":"node","name":"pve2","online":1,"ip":"192.168.1.12"}]`, &in.status)
	must(`[{"type":"node","node":"pve1","status":"online","cpu":0.1,"maxcpu":8,"mem":20000000000,"maxmem":34359738368},
	       {"type":"node","node":"pve2","status":"online","cpu":0.2,"maxcpu":4,"mem":8000000000,"maxmem":17179869184},
	       {"type":"qemu","node":"pve1","vmid":101,"status":"running","maxcpu":8,"maxmem":17179869184},
	       {"type":"lxc","node":"pve1","vmid":102,"status":"running","maxcpu":2,"maxmem":8589934592},
	       {"type":"lxc","node":"pve2","vmid":103,"status":"running","maxcpu":20,"maxmem":4294967296},
	       {"type":"qemu","node":"pve2","vmid":9000,"template":1,"maxmem":2147483648},
	       {"type":"storage","node":"pve1","storage":"local","plugintype":"dir","disk":96,"maxdisk":100,"status":"available","shared":0},
	       {"type":"storage","node":"pve1","storage":"nfs","plugintype":"nfs","disk":10,"maxdisk":100,"status":"available","shared":1},
	       {"type":"storage","node":"pve2","storage":"nfs","plugintype":"nfs","disk":10,"maxdisk":100,"status":"available","shared":1}]`, &in.resources)
	must(`[{"type":"quorum","id":"quorum","status":"OK"},{"type":"master","node":"pve1","status":"active"},
	       {"type":"lrm","node":"pve2","status":"maintenance mode"},
	       {"type":"service","sid":"vm:101","state":"error"}]`, &in.ha)
	o := analyze(in)
	if o.Name != "homelab" || !o.Quorate || o.VMs != 1 || o.CTs != 2 || o.Templates != 1 || o.Running != 3 {
		t.Errorf("tellingen: %+v", o)
	}
	if len(o.Storages) != 2 {
		t.Errorf("gedeelde storage dubbel: %+v", o.Storages)
	}
	c := o.Capacity
	if c.Failover || c.FailoverHost != "pve1" || c.MemRunning != 28*gib {
		t.Errorf("capaciteit: %+v", c)
	}
	want := []string{"HA: vm:101: error", "storage local op pve1 is 96% vol", "pve2: 20 vCPU's op 4 threads", "pve2 staat in onderhoudsmodus", "Valt pve1 uit", "Twee hosts zonder QDevice"}
	all := ""
	for _, w := range o.Warnings {
		all += w.Level + " " + w.Text + "\n"
	}
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("waarschuwing %q ontbreekt in:\n%s", w, all)
		}
	}
	if o.Warnings[0].Level != "crit" {
		t.Errorf("crit eerst: %+v", o.Warnings)
	}
}
