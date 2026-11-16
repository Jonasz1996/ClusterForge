package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const ctConf = `#Webserver%3A cluster web
arch: amd64
cores: 2
features: nesting=1,keyctl=1
hostname: web-01
memory: 2048
net0: name=eth0,bridge=vmbr0,gw=10.0.20.1,hwaddr=BC:24:11:00:00:01,ip=10.0.20.11/24,tag=20,type=veth
onboot: 1
ostype: debian
parent: voor-update
rootfs: local-lvm:vm-101-disk-0,size=8G
swap: 512
tags: clusterforge;web
unprivileged: 1

[voor-update]
#voor de update
arch: amd64
cores: 2
hostname: web-01
memory: 2048
rootfs: local-lvm:vm-101-disk-0,size=8G
snaptime: 1763200000
`

const vmConf = `boot: order=scsi0
cores: 4
ide2: local:iso/debian.iso,media=cdrom
memory: 8192
name: docker-01
net0: virtio=BC:24:11:00:00:02,bridge=vmbr0
scsi0: ceph-vm:vm-200-disk-0,size=32G
scsihw: virtio-scsi-pci
`

// pveRoot maakt een nagespeelde Proxmox-host pve1.
func pveRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(p, body string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("/usr/bin/pveversion", "")
	write("/etc/pve/.members", `{"nodename":"pve1","version":7,"cluster":{"name":"homelab","version":3,"nodes":2,"quorate":1}}`)
	write("/etc/pve/.vmlist", `{"version":9,"ids":{"101":{"node":"pve1","type":"lxc","version":1},"200":{"node":"pve1","type":"qemu","version":2},"300":{"node":"pve2","type":"lxc","version":3}}}`)
	write("/etc/pve/nodes/pve1/lxc/101.conf", ctConf)
	write("/etc/pve/nodes/pve1/qemu-server/200.conf", vmConf)
	write("/etc/pve/nodes/pve2/lxc/300.conf", "hostname: elders\nmemory: 512\n")
	write("/root/.ssh/id_rsa.pub", "ssh-rsa AAAAB3NzaC1yc2E root@pve1\n")
	write("/sys/class/net/vmbr0/bridge/stp_state", "0")
	write("/sys/class/net/eno1/address", "x")
	return root
}

func pveshReply(c string) (string, int) {
	switch {
	case c == "pveversion":
		return "pve-manager/9.0.10/deadbeef (running kernel: 6.14.8-2-pve)\n", 0
	case strings.HasPrefix(c, "pvesh get /nodes/pve1/lxc "):
		return `[{"vmid":101,"status":"running","name":"web-01","uptime":3600}]`, 0
	case strings.HasPrefix(c, "pvesh get /nodes/pve1/qemu "):
		return `[{"vmid":"200","status":"stopped","name":"docker-01"}]`, 0
	case strings.HasPrefix(c, "pvesh get /nodes/pve1/storage/local/content"):
		return `[{"volid":"local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst","size":123}]`, 0
	case strings.HasPrefix(c, "pvesh get /nodes/pve1/storage "):
		return `[{"storage":"local","type":"dir","content":"iso,vztmpl,backup","active":1,"enabled":1,"total":100,"avail":50},
		         {"storage":"local-lvm","type":"lvmthin","content":"rootdir,images","active":1,"enabled":1,"total":1000,"avail":800},
		         {"storage":"ceph-vm","type":"rbd","content":"images","active":1,"enabled":1,"shared":1,"total":5000,"avail":4000}]`, 0
	}
	return "", 0
}

func TestPVEFactsAndInventory(t *testing.T) {
	root := pveRoot(t)
	h := &fakeHost{reply: pveshReply}
	host := (&RunExecutor{Root: root, Cmd: h.cmd}).host()

	f := host.facts(context.Background())
	if f == nil || f.Node != "pve1" || f.Cluster != "homelab" || f.Version != "9.0.10" {
		t.Fatalf("facts = %+v", f)
	}
	if (pveHost{root: t.TempDir()}).facts(context.Background()) != nil {
		t.Fatal("een machine zonder Proxmox kreeg facts proxmox")
	}

	inv, err := host.inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inv.Node != "pve1" || len(inv.Guests) != 2 {
		t.Fatalf("inventaris = %+v", inv)
	}
	ct := inv.Guests[0]
	if ct.Type != "lxc" || ct.VMID != 101 || ct.Name != "web-01" || ct.Status != "running" || ct.Uptime != 3600 ||
		ct.Cores != 2 || ct.MemoryMiB != 2048 || ct.SwapMiB != 512 || !ct.OnBoot || !ct.Unprivileged ||
		ct.Features != "nesting=1,keyctl=1" || ct.Description != "Webserver: cluster web" {
		t.Fatalf("CT = %+v", ct)
	}
	if len(ct.Disks) != 1 || ct.Disks[0] != (protocol.PVEDisk{Name: "rootfs", Storage: "local-lvm", SizeGiB: 8}) {
		t.Fatalf("schijven = %+v", ct.Disks)
	}
	if !slices.Equal(ct.Tags, []string{"clusterforge", "web"}) || len(ct.Nets) != 1 {
		t.Fatalf("tags %v, netten %v", ct.Tags, ct.Nets)
	}
	if len(ct.Snapshots) != 1 || ct.Snapshots[0].Name != "voor-update" || ct.Snapshots[0].Description != "voor de update" || ct.Snapshots[0].Time.IsZero() {
		t.Fatalf("snapshots = %+v", ct.Snapshots)
	}
	vm := inv.Guests[1]
	if vm.Type != "qemu" || vm.Name != "docker-01" || vm.Status != "stopped" || len(vm.Disks) != 1 || vm.Disks[0].SizeGiB != 32 {
		t.Fatalf("VM = %+v", vm)
	}
	if !slices.Equal(inv.Bridges, []string{"vmbr0"}) || len(inv.SSHKeys) != 1 || !slices.Equal(inv.UsedIDs, []int{101, 200, 300}) {
		t.Fatalf("bridges %v, sleutels %v, ids %v", inv.Bridges, inv.SSHKeys, inv.UsedIDs)
	}
	if len(inv.Storages) != 3 || len(inv.Templates) != 1 || inv.Templates[0].Volid != "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst" {
		t.Fatalf("storages %+v, templates %+v", inv.Storages, inv.Templates)
	}
	// Alleen de storage met vztmpl wordt gelezen.
	if got := h.called("pvesh get /nodes/pve1/storage/"); len(got) != 1 {
		t.Fatalf("inhoud gelezen van %v", got)
	}
}

func TestPVESizeGiB(t *testing.T) {
	for in, want := range map[string]int{"8G": 8, "512M": 1, "1T": 1024, "4194304K": 4, "": 0, "x": 0} {
		if got := pveSizeGiB(in); got != want {
			t.Errorf("pveSizeGiB(%q) = %d, wil %d", in, got, want)
		}
	}
}

// pveRun voert een run pve uit en geeft uitvoer en stand.
func pveRun(t *testing.T, root string, h *fakeHost, ex *RunExecutor, p protocol.PVERun) (string, protocol.RunStatus) {
	t.Helper()
	if ex == nil {
		ex = &RunExecutor{}
	}
	ex.Root, ex.Cmd, ex.Stderr = root, h.cmd, io.Discard
	ex.Sleep = func(context.Context, time.Duration) error { return nil }
	spec := protocol.RunSpec{ID: "pve-1", Kind: protocol.RunPVE, PVE: &p}
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec ongeldig: %v", err)
	}
	dir := runDir(root, spec.ID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ex.Main(context.Background(), spec.ID)
	out, _ := os.ReadFile(filepath.Join(dir, "output.log"))
	st, err := readRunStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), st
}

func TestPVERunActions(t *testing.T) {
	root := pveRoot(t)
	cores, mem, onboot, desc, feat := 4, 4096, false, "nieuw", ""
	tags := []string{"clusterforge", "db"}
	cases := []struct {
		run  protocol.PVERun
		want string
	}{
		{protocol.PVERun{Action: protocol.PVEStart, Type: "lxc", VMID: 101}, "pct start 101"},
		{protocol.PVERun{Action: protocol.PVEShutdown, Type: "qemu", VMID: 200}, "qm shutdown 200 --timeout 180"},
		{protocol.PVERun{Action: protocol.PVESuspend, Type: "qemu", VMID: 200}, "qm suspend 200"},
		{protocol.PVERun{Action: protocol.PVESet, Type: "lxc", VMID: 101, Set: &protocol.PVESetting{
			Cores: &cores, MemoryMiB: &mem, OnBoot: &onboot, Tags: &tags, Description: &desc, Features: &feat,
		}}, "pct set 101 --cores 4 --memory 4096 --onboot 0 --tags clusterforge;db --description nieuw --delete features"},
		{protocol.PVERun{Action: protocol.PVEResize, Type: "lxc", VMID: 101, Disk: "rootfs", Size: "+4G"}, "pct resize 101 rootfs +4G"},
		{protocol.PVERun{Action: protocol.PVESnapshot, Type: "qemu", VMID: 200, Snapshot: "voor-x", Description: "test", VMState: true},
			"qm snapshot 200 voor-x --description test --vmstate 1"},
		{protocol.PVERun{Action: protocol.PVERollback, Type: "lxc", VMID: 101, Snapshot: "voor-update"}, "pct rollback 101 voor-update"},
		{protocol.PVERun{Action: protocol.PVEMigrate, Type: "lxc", VMID: 101, Target: "pve2"}, "pct migrate 101 pve2 --restart 1 --timeout 180"},
		{protocol.PVERun{Action: protocol.PVEMigrate, Type: "qemu", VMID: 200, Target: "pve2", TargetStorage: "local-lvm"},
			"qm migrate 200 pve2 --targetstorage local-lvm --with-local-disks 1"},
	}
	for _, c := range cases {
		h := &fakeHost{reply: func(cmd string) (string, int) {
			switch cmd {
			case "pct status 101":
				return "status: running\n", 0
			case "qm status 200":
				return "status: stopped\n", 0
			}
			return "", 0
		}}
		_, st := pveRun(t, root, h, nil, c.run)
		if st.State != protocol.RunOK {
			t.Fatalf("%s: stand %s (%s)", c.want, st.State, st.Error)
		}
		last := h.calls[len(h.calls)-1]
		if last != c.want {
			t.Errorf("commando = %q, wil %q", last, c.want)
		}
	}
}

func TestPVEDestroyOnlyStopped(t *testing.T) {
	root := pveRoot(t)
	h := &fakeHost{reply: func(cmd string) (string, int) {
		if cmd == "pct status 101" {
			return "status: running\n", 0
		}
		return "", 0
	}}
	_, st := pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEDestroy, Type: "lxc", VMID: 101})
	if st.State != protocol.RunFailed || !strings.Contains(st.Error, "zet hem eerst uit") || len(h.called("pct destroy")) != 0 {
		t.Fatalf("stand %s (%s), calls %v", st.State, st.Error, h.calls)
	}
	h = &fakeHost{reply: func(cmd string) (string, int) { return "status: stopped\n", 0 }}
	_, st = pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEDestroy, Type: "lxc", VMID: 101})
	if st.State != protocol.RunOK || len(h.called("pct destroy 101 --purge 1")) != 1 {
		t.Fatalf("stand %s (%s), calls %v", st.State, st.Error, h.calls)
	}
}

func TestPVECreateCT(t *testing.T) {
	root := pveRoot(t)
	create := &protocol.PVECreate{
		Hostname: "db-01", OSTemplate: "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst",
		Cores: 2, MemoryMiB: 2048, SwapMiB: 512, Storage: "local-lvm", DiskGiB: 16, Bridge: "vmbr0", VLAN: 20,
		Address: "10.0.20.21/24", Gateway: "10.0.20.1", DNS: []string{"10.0.20.1"},
		SSHKeys: []string{"ssh-ed25519 AAAAC3Nza jonas@pc"}, HostKeys: true, Nesting: true, Keyctl: true, OnBoot: true,
		Tags: []string{"clusterforge", "db"}, Description: "Beheerd door ClusterForge",
	}
	h := &fakeHost{}
	_, st := pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVECreateCT, Type: "lxc", VMID: 102, Create: create})
	if st.State != protocol.RunOK {
		t.Fatalf("stand %s (%s)", st.State, st.Error)
	}
	got := h.called("pct create")
	if len(got) != 1 {
		t.Fatalf("calls %v", h.calls)
	}
	keysFile := filepath.Join(runDir(root, "pve-1"), "ssh-keys")
	want := "pct create 102 local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst --hostname db-01 --unprivileged 1 --cores 2 --memory 2048 --swap 512 " +
		"--rootfs local-lvm:16 --net0 name=eth0,bridge=vmbr0,tag=20,ip=10.0.20.21/24,gw=10.0.20.1 --onboot 1 --features nesting=1,keyctl=1 " +
		"--nameserver 10.0.20.1 --tags clusterforge;db --description Beheerd door ClusterForge --ssh-public-keys " + keysFile
	if got[0] != want {
		t.Fatalf("commando\n%s\nwil\n%s", got[0], want)
	}
	keys, _ := os.ReadFile(keysFile)
	if !strings.Contains(string(keys), "jonas@pc") || !strings.Contains(string(keys), "root@pve1") {
		t.Fatalf("sleutels = %q", keys)
	}

	// Een VMID in gebruik door een andere CT: weigeren.
	h = &fakeHost{}
	_, st = pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVECreateCT, Type: "lxc", VMID: 300, Create: create})
	if st.State != protocol.RunFailed || !strings.Contains(st.Error, "elders") || len(h.called("pct create")) != 0 {
		t.Fatalf("stand %s (%s)", st.State, st.Error)
	}
	// Dezelfde CT van een eerdere poging: niets opnieuw.
	again := *create
	again.Hostname = "web-01"
	h = &fakeHost{}
	_, st = pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVECreateCT, Type: "lxc", VMID: 101, Create: &again})
	if st.State != protocol.RunOK || st.PVE == nil || !st.PVE.Existed || len(h.called("pct create")) != 0 {
		t.Fatalf("stand %s (%s) %+v", st.State, st.Error, st.PVE)
	}
}

func TestPVETemplate(t *testing.T) {
	root := pveRoot(t)
	arch := runtime.GOARCH
	h := &fakeHost{reply: func(cmd string) (string, int) {
		switch cmd {
		case "pveam list local":
			return "NAME SIZE\nlocal:vztmpl/debian-12-standard_12.7-1_" + arch + ".tar.zst 120MB\n", 0
		case "pveam available --section system":
			return "system debian-13-standard_13.0-1_" + arch + ".tar.zst\nsystem debian-13-standard_13.1-2_" + arch + ".tar.zst\n" +
				"system debian-13-standard_13.10-1_arm64x.tar.zst\nsystem ubuntu-24.04-standard_24.04-2_" + arch + ".tar.zst\n", 0
		}
		return "", 0
	}}
	_, st := pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVETemplate,
		Template: &protocol.PVETemplateSpec{Storage: "local", Match: protocol.PVEDefaultTemplate}})
	file := "debian-13-standard_13.1-2_" + arch + ".tar.zst"
	if st.State != protocol.RunOK || st.PVE == nil || st.PVE.Volid != "local:vztmpl/"+file || st.PVE.Existed {
		t.Fatalf("stand %s (%s) %+v", st.State, st.Error, st.PVE)
	}
	if got := h.called("pveam download"); len(got) != 1 || got[0] != "pveam download local "+file {
		t.Fatalf("download %v", got)
	}
	if compareVersions("debian-13-standard_13.10-1", "debian-13-standard_13.9-1") <= 0 {
		t.Fatal("13.10 hoort na 13.9")
	}
}

func TestPVEBootstrap(t *testing.T) {
	root := pveRoot(t)
	bin := []byte("nep-agent")
	sum := sha256.Sum256(bin)
	ex := &RunExecutor{Download: func(_ context.Context, url string, w io.Writer) error {
		if !strings.HasSuffix(url, "/downloads/cf-agent-linux-"+runtime.GOARCH) {
			t.Errorf("url = %s", url)
		}
		_, err := w.Write(bin)
		return err
	}}
	var enrolled []byte
	h := &fakeHost{}
	h.reply = func(cmd string) (string, int) {
		switch {
		case cmd == "pct status 102":
			return "status: running\n", 0
		case cmd == "pct exec 102 -- test -s /etc/clusterforge/agent.json":
			return "", 1
		case strings.HasPrefix(cmd, "pct push 102 ") && strings.Contains(cmd, "/etc/clusterforge/enroll.json"):
			enrolled, _ = os.ReadFile(strings.Fields(cmd)[3])
		case strings.HasPrefix(cmd, "ssh-keyscan"):
			return "10.0.20.21 ssh-ed25519 AAAAC3NzaNIEUW\n", 0
		}
		return "", 0
	}
	known := filepath.Join(root, "/root/.ssh/known_hosts")
	if err := os.WriteFile(known, []byte("10.0.20.21 ssh-ed25519 AAAAOUD\npve2 ssh-rsa AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, st := pveRun(t, root, h, ex, protocol.PVERun{Action: protocol.PVEBootstrap, Type: "lxc", VMID: 102, Bootstrap: &protocol.PVEBootstrapSpec{
		Server: "http://cf.lan:8080", AllowHTTP: true, Token: "geheim-token-1234567890",
		SHA256: map[string]string{runtime.GOARCH: hex.EncodeToString(sum[:])}, KnownHost: "10.0.20.21",
	}})
	if st.State != protocol.RunOK {
		t.Fatalf("stand %s (%s)\n%s", st.State, st.Error, out)
	}
	var e EnrollFile
	if err := json.Unmarshal(enrolled, &e); err != nil || e.Token != "geheim-token-1234567890" || e.Server != "http://cf.lan:8080" || !e.AllowHTTP {
		t.Fatalf("aanmeldbestand %q", enrolled)
	}
	if strings.Contains(out, "geheim-token") {
		t.Fatal("het token staat in de uitvoer")
	}
	spec, _ := os.ReadFile(filepath.Join(runDir(root, "pve-1"), "spec.json"))
	if strings.Contains(string(spec), "geheim-token") {
		t.Fatal("het token staat na de run nog in spec.json")
	}
	if _, err := os.Stat(filepath.Join(runDir(root, "pve-1"), "enroll.json")); !os.IsNotExist(err) {
		t.Fatal("enroll.json bleef op de host staan")
	}
	for _, want := range []string{"pct push 102 ", "pct exec 102 -- systemctl enable --now cf-agent.service"} {
		if len(h.called(want)) == 0 {
			t.Errorf("%q niet gedaan: %v", want, h.calls)
		}
	}
	kh, _ := os.ReadFile(known)
	if strings.Contains(string(kh), "AAAAOUD") || !strings.Contains(string(kh), "AAAAC3NzaNIEUW") || !strings.Contains(string(kh), "pve2") {
		t.Fatalf("known_hosts = %q", kh)
	}

	// Een verkeerde sha256: niets in de CT.
	h = &fakeHost{reply: func(cmd string) (string, int) {
		if cmd == "pct status 102" {
			return "status: running\n", 0
		}
		if strings.HasSuffix(cmd, "agent.json") {
			return "", 1
		}
		return "", 0
	}}
	_, st = pveRun(t, root, h, ex, protocol.PVERun{Action: protocol.PVEBootstrap, Type: "lxc", VMID: 102, Bootstrap: &protocol.PVEBootstrapSpec{
		Server: "https://cf.lan", Token: "geheim-token-1234567890", SHA256: map[string]string{runtime.GOARCH: strings.Repeat("0", 64)},
	}})
	if st.State != protocol.RunFailed || !strings.Contains(st.Error, "sha256") || len(h.called("pct push")) != 0 {
		t.Fatalf("stand %s (%s)", st.State, st.Error)
	}
}
