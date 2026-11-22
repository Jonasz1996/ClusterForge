package protocol

import (
	"strings"
	"testing"
)

func TestPVERunValidate(t *testing.T) {
	ok := []PVERun{
		{Action: PVEStart, Type: "lxc", VMID: 101},
		{Action: PVETemplate, Template: &PVETemplateSpec{Storage: "local", Match: PVEDefaultTemplate}},
		{Action: PVECreateCT, Type: "lxc", VMID: 120, Create: &PVECreate{
			Hostname: "web-01", OSTemplate: "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst", Cores: 1, MemoryMiB: 512,
			Storage: "local-lvm", DiskGiB: 8, Bridge: "vmbr0", Address: "10.0.20.11/24", Gateway: "10.0.20.1",
		}},
	}
	for _, r := range ok {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", r.Action, err)
		}
	}
	bad := map[string]PVERun{
		"onbekende actie":      {Action: "exec", Type: "lxc", VMID: 101},
		"vmid":                 {Action: PVEStart, Type: "lxc", VMID: 5},
		"suspend CT":           {Action: PVESuspend, Type: "lxc", VMID: 101},
		"shell in snapshot":    {Action: PVESnapshot, Type: "lxc", VMID: 101, Snapshot: "a;rm -rf /"},
		"vmstate CT":           {Action: PVESnapshot, Type: "lxc", VMID: 101, Snapshot: "test", VMState: true},
		"grootte":              {Action: PVEResize, Type: "lxc", VMID: 101, Disk: "rootfs", Size: "-4G"},
		"features VM":          {Action: PVESet, Type: "qemu", VMID: 200, Set: &PVESetting{Features: new("nesting=1")}},
		"lege set":             {Action: PVESet, Type: "lxc", VMID: 101, Set: &PVESetting{}},
		"template allebei":     {Action: PVETemplate, Template: &PVETemplateSpec{Storage: "local", Match: "x", File: "x.tar.zst"}},
		"bootstrap http":       {Action: PVEBootstrap, Type: "lxc", VMID: 101, Bootstrap: &PVEBootstrapSpec{Server: "http://cf", Token: strings.Repeat("a", 20), SHA256: map[string]string{"amd64": strings.Repeat("0", 64)}}},
		"bootstrap zonder sha": {Action: PVEBootstrap, Type: "lxc", VMID: 101, Bootstrap: &PVEBootstrapSpec{Server: "https://cf", Token: strings.Repeat("a", 20)}},
		"gateway buiten net": {Action: PVECreateCT, Type: "lxc", VMID: 120, Create: &PVECreate{
			Hostname: "web-01", OSTemplate: "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst", Cores: 1, MemoryMiB: 512,
			Storage: "local-lvm", DiskGiB: 8, Bridge: "vmbr0", Address: "10.0.20.11/24", Gateway: "10.0.30.1",
		}},
		"sleutel": {Action: PVECreateCT, Type: "lxc", VMID: 120, Create: &PVECreate{
			Hostname: "web-01", OSTemplate: "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst", Cores: 1, MemoryMiB: 512,
			Storage: "local-lvm", DiskGiB: 8, Bridge: "vmbr0", SSHKeys: []string{"geen sleutel"},
		}},
	}
	for name, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: geen fout", name)
		}
	}
}
