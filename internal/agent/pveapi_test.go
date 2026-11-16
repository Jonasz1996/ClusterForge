package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const testUPID = "UPID:pve1:0000ABCD:0001E240:6738A1B2:vzdump:101:root@pam:"

func TestPVEAPIWaitsForTaskAndMasksSecrets(t *testing.T) {
	root := pveRoot(t)
	h := &fakeHost{reply: func(c string) (string, int) {
		switch {
		case strings.HasPrefix(c, "pvesh create /nodes/pve1/vzdump"):
			return `"` + testUPID + `"` + "\n", 0
		case strings.HasPrefix(c, "pvesh get /nodes/pve1/tasks/"+testUPID+"/status"):
			return `{"status":"stopped","exitstatus":"OK"}`, 0
		case strings.HasPrefix(c, "pvesh get /nodes/pve1/tasks/"+testUPID+"/log"):
			return `[{"n":1,"t":"INFO: starting new backup job"},{"n":2,"t":"INFO: Finished Backup of VM 101"}]`, 0
		case strings.HasPrefix(c, "pvesh create /storage"):
			return "null\n", 0
		}
		return "", 0
	}}
	out, st := pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEAPI, API: &protocol.PVEAPICall{
		Method: "create", Path: "/nodes/pve1/vzdump", Params: map[string]string{"vmid": "101", "storage": "pbs", "mode": "snapshot"},
	}})
	if st.State != protocol.RunOK {
		t.Fatalf("stand %s: %s\n%s", st.State, st.Error, out)
	}
	if got := h.calls[0]; got != "pvesh create /nodes/pve1/vzdump --output-format json --mode=snapshot --storage=pbs --vmid=101" {
		t.Errorf("argv = %q", got)
	}
	if !strings.Contains(out, "Finished Backup of VM 101") {
		t.Errorf("het takenlog ontbreekt:\n%s", out)
	}
	if st.PVE == nil || st.PVE.UPID != testUPID {
		t.Errorf("UPID = %+v", st.PVE)
	}

	// Een geheim komt niet in de uitvoer en verdwijnt uit spec.json.
	h.calls = nil
	out, st = pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEAPI, API: &protocol.PVEAPICall{
		Method: "create", Path: "/storage", Params: map[string]string{"storage": "pbs", "type": "pbs", "password": "Zeer-Geheim-123"},
		Secret: []string{"password"},
	}})
	if st.State != protocol.RunOK {
		t.Fatalf("stand %s: %s", st.State, st.Error)
	}
	if strings.Contains(out, "Zeer-Geheim-123") {
		t.Errorf("het wachtwoord staat in de uitvoer:\n%s", out)
	}
	if !strings.Contains(h.calls[0], "--password=Zeer-Geheim-123") {
		t.Errorf("pvesh kreeg het wachtwoord niet: %q", h.calls[0])
	}
	spec, _ := os.ReadFile(filepath.Join(runDir(root, "pve-1"), "spec.json"))
	if strings.Contains(string(spec), "Zeer-Geheim-123") {
		t.Errorf("het wachtwoord bleef in spec.json")
	}
}

func TestPVEAPIFailedTask(t *testing.T) {
	root := pveRoot(t)
	h := &fakeHost{reply: func(c string) (string, int) {
		switch {
		case strings.HasPrefix(c, "pvesh create"):
			return "starting\n\"" + testUPID + "\"\n", 0
		case strings.HasSuffix(c, "/status --output-format json"):
			return `{"status":"stopped","exitstatus":"job errors"}`, 0
		}
		return "", 0
	}}
	_, st := pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEAPI, API: &protocol.PVEAPICall{
		Method: "create", Path: "/nodes/pve1/vzdump", Params: map[string]string{"vmid": "101"},
	}})
	if st.State == protocol.RunOK || !strings.Contains(st.Error, "job errors") {
		t.Fatalf("stand %s, fout %q", st.State, st.Error)
	}
}

func TestPVEAPIValidate(t *testing.T) {
	bad := []protocol.PVEAPICall{
		{Method: "get", Path: "/cluster/backup"},
		{Method: "create", Path: "/nodes/../etc"},
		{Method: "create", Path: "cluster/backup"},
		{Method: "create", Path: "/cluster/backup", Params: map[string]string{"Bad Key": "x"}},
		{Method: "create", Path: "/cluster/backup", Params: map[string]string{"a": "x"}, Secret: []string{"b"}},
	}
	for _, c := range bad {
		if c.Validate() == nil {
			t.Errorf("%+v zou ongeldig moeten zijn", c)
		}
	}
	ok := protocol.PVEAPICall{Method: "delete", Path: "/nodes/pve1/storage/local/content/local:backup/vzdump-lxc-101-2026_11_16-01_00_00.tar.zst"}
	if err := ok.Validate(); err != nil {
		t.Errorf("geldig pad geweigerd: %v", err)
	}
}

func TestPVEGetRequest(t *testing.T) {
	root := pveRoot(t)
	a := &Agent{Root: root}
	h := &fakeHost{reply: func(c string) (string, int) {
		if c == "pvesh get /cluster/status --output-format json" {
			return `[{"type":"cluster","name":"homelab","quorate":1}]`, 0
		}
		return "", 1
	}}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		var b strings.Builder
		code, _ := h.cmd(ctx, "", append([]string{name}, args...), nil, &b)
		if code != 0 {
			return nil, &errStep{name, code}
		}
		return []byte(b.String()), nil
	}
	env, _ := json.Marshal(protocol.PVERequest{Op: protocol.PVEOpGet, Path: "/cluster/status"})
	data, _ := json.Marshal(protocol.Envelope{Type: protocol.TypePVERequest, Body: env})
	res := a.handlePVE(data)
	if res.Error != "" || !strings.Contains(string(res.Data), "homelab") {
		t.Fatalf("antwoord %+v", res)
	}
	env, _ = json.Marshal(protocol.PVERequest{Op: protocol.PVEOpGet, Path: "/nodes/../x"})
	data, _ = json.Marshal(protocol.Envelope{Type: protocol.TypePVERequest, Body: env})
	if res := a.handlePVE(data); res.Error == "" {
		t.Fatal("een pad met .. werd gelezen")
	}
}

func TestEditCmdline(t *testing.T) {
	cases := []struct {
		in   string
		on   bool
		want string
	}{
		{"root=ZFS=rpool/ROOT/pve-1 boot=zfs", true, "root=ZFS=rpool/ROOT/pve-1 boot=zfs intel_iommu=on iommu=pt"},
		{"quiet intel_iommu=on iommu=pt", true, "quiet intel_iommu=on iommu=pt"},
		{"quiet intel_iommu=on iommu=pt", false, "quiet"},
	}
	for _, c := range cases {
		if got := editCmdline(c.in, iommuParams("GenuineIntel"), c.on); got != c.want {
			t.Errorf("editCmdline(%q, %v) = %q, wil %q", c.in, c.on, got, c.want)
		}
	}
	if got := iommuParams("AuthenticAMD"); len(got) != 1 || got[0] != "iommu=pt" {
		t.Errorf("AMD: %v", got)
	}
}

func TestParseLspci(t *testing.T) {
	out := []byte(`0000:01:00.0 "VGA compatible controller [0300]" "NVIDIA Corporation [10de]" "GP104 [GeForce GTX 1080] [1b80]" -ra1 "ASUSTeK Computer Inc. [1043]" "Device [8591]"
0000:00:1f.3 "Audio device [0403]" "Intel Corporation [8086]" "Alder Lake PCH-P High Definition Audio Controller [51c8]" -r01 "" ""`)
	n := parseLspci(out)
	if g := n["0000:01:00.0"]; g[0] != "VGA compatible controller" || g[1] != "NVIDIA Corporation" || g[2] != "GP104 [GeForce GTX 1080]" {
		t.Errorf("GPU: %q", g)
	}
	if a := n["0000:00:1f.3"]; a[1] != "Intel Corporation" {
		t.Errorf("audio: %q", a)
	}
}

func hwRoot(t *testing.T) string {
	t.Helper()
	root := pveRoot(t)
	write := func(p, body string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}
	write("/proc/cpuinfo", "vendor_id\t: GenuineIntel\nmodel name\t: 13th Gen Intel(R) Core(TM) i9-13900H\n")
	write("/proc/cmdline", "BOOT_IMAGE=/boot/vmlinuz root=/dev/mapper/pve-root ro quiet\n")
	write("/proc/modules", "vfio_pci 16384 0 - Live 0x0\nkvm 1 0 - Live 0x0\n")
	write("/etc/default/grub", "GRUB_DEFAULT=0\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet intel_iommu=on iommu=pt\"\nGRUB_CMDLINE_LINUX=\"\"\n")
	write("/etc/modprobe.d/clusterforge-vfio.conf", "options vfio-pci ids=10de:1b80,10de:10f0 disable_vga=1\nblacklist nouveau\n")
	for _, d := range []struct{ addr, vendor, device, class, driver, group string }{
		{"0000:01:00.0", "0x10de", "0x1b80", "0x030000", "nouveau", "12"},
		{"0000:01:00.1", "0x10de", "0x10f0", "0x040300", "snd_hda_intel", "12"},
		{"0000:00:1f.6", "0x8086", "0x15bc", "0x020000", "e1000e", "13"},
	} {
		base := "/sys/bus/pci/devices/" + d.addr
		write(base+"/vendor", d.vendor+"\n")
		write(base+"/device", d.device+"\n")
		write(base+"/class", d.class+"\n")
		link("/sys/bus/pci/drivers/"+d.driver, base+"/driver")
		link("../../../kernel/iommu_groups/"+d.group, base+"/iommu_group")
		write("/sys/kernel/iommu_groups/"+d.group+"/type", "DMA\n")
	}
	// De netwerkkaart van de host.
	write("/sys/devices/pci0000:00/0000:00:1f.6/net/eno1/x", "")
	link(filepath.Join(root, "/sys/devices/pci0000:00/0000:00:1f.6"), "/sys/class/net/eno1/device")
	write("/sys/bus/usb/devices/1-4/idVendor", "0658\n")
	write("/sys/bus/usb/devices/1-4/idProduct", "0200\n")
	write("/sys/bus/usb/devices/1-4/product", "Zigbee stick\n")
	write("/sys/bus/usb/devices/1-4/busnum", "1\n")
	write("/sys/bus/usb/devices/usb1/idVendor", "1d6b\n")
	write("/sys/class/hwmon/hwmon0/name", "coretemp\n")
	write("/sys/class/hwmon/hwmon0/temp1_input", "54000\n")
	write("/sys/class/hwmon/hwmon0/temp1_label", "Package id 0\n")
	write("/sys/class/hwmon/hwmon0/temp1_crit", "100000\n")
	write("/sys/devices/system/edac/mc/mc0/ce_count", "3\n")
	write("/sys/devices/system/edac/mc/mc0/ue_count", "0\n")
	return root
}

func TestPVEHardwareScan(t *testing.T) {
	root := hwRoot(t)
	h := pveHost{root: root, run: func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }}
	hw, err := h.hardware(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hw.CPUVendor != "GenuineIntel" || !hw.IOMMU || hw.IOMMUGroups != 2 || hw.Boot != "grub" {
		t.Errorf("basis: %+v", hw)
	}
	if strings.Join(hw.Configured.VFIOIDs, ",") != "10de:10f0,10de:1b80" || hw.Configured.Blacklist[0] != "nouveau" || !hw.Configured.Managed {
		t.Errorf("configuratie: %+v", hw.Configured)
	}
	if len(hw.PCI) != 3 {
		t.Fatalf("PCI: %+v", hw.PCI)
	}
	gpu := hw.PCI[1]
	if gpu.Address != "0000:01:00.0" || gpu.Vendor != "10de" || gpu.Device != "1b80" || gpu.Class != "0300" || gpu.Driver != "nouveau" || gpu.IOMMUGroup != 12 || gpu.GroupSize != 2 {
		t.Errorf("GPU: %+v", gpu)
	}
	if nic := hw.PCI[0]; nic.HostUse != "netwerkkaart eno1" {
		t.Errorf("netwerkkaart: %+v", nic)
	}
	if len(hw.USB) != 1 || hw.USB[0].Vendor != "0658" || hw.USB[0].Port != "4" {
		t.Errorf("USB: %+v", hw.USB)
	}
	if len(hw.Temps) != 1 || hw.Temps[0].C != 54 || hw.Temps[0].Crit != 100 {
		t.Errorf("temperaturen: %+v", hw.Temps)
	}
	if len(hw.EDAC) != 1 || hw.EDAC[0].Corrected != 3 {
		t.Errorf("ECC: %+v", hw.EDAC)
	}
	// De kernelregel en de drivers gelden pas na een herstart.
	if !hw.RebootRequired || len(hw.RebootReasons) != 4 {
		t.Errorf("herstart: %v %q", hw.RebootRequired, hw.RebootReasons)
	}
}

func TestPVEPassthroughSetup(t *testing.T) {
	root := pveRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "etc/default"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, "proc"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "proc/cpuinfo"), []byte("vendor_id\t: GenuineIntel\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "etc/default/grub"), []byte("GRUB_DEFAULT=0\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "run"), 0o755)
	h := &fakeHost{}
	out, st := pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEPassthrough, Passthrough: &protocol.PVEPassthroughSpec{
		IOMMU: true, VFIOIDs: []string{"10de:1b80", "10de:10f0"}, Blacklist: []string{"nouveau"},
	}})
	if st.State != protocol.RunOK {
		t.Fatalf("stand %s: %s\n%s", st.State, st.Error, out)
	}
	grub, _ := os.ReadFile(filepath.Join(root, "etc/default/grub"))
	if !strings.Contains(string(grub), `GRUB_CMDLINE_LINUX_DEFAULT="quiet intel_iommu=on iommu=pt"`) {
		t.Errorf("grub:\n%s", grub)
	}
	conf, _ := os.ReadFile(filepath.Join(root, vfioModprobe))
	if !strings.Contains(string(conf), "options vfio-pci ids=10de:10f0,10de:1b80 disable_vga=1") || !strings.Contains(string(conf), "blacklist nouveau") {
		t.Errorf("modprobe:\n%s", conf)
	}
	if got := strings.Join(h.calls, " | "); got != "update-initramfs -u -k all | update-grub" {
		t.Errorf("commando's: %s", got)
	}
	if st.PVE == nil || !st.PVE.RebootRequired {
		t.Errorf("geen herstart gemeld: %+v", st.PVE)
	}
	// Nog eens hetzelfde: niets te doen.
	h.calls = nil
	if _, st := pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEPassthrough, Passthrough: &protocol.PVEPassthroughSpec{
		IOMMU: true, VFIOIDs: []string{"10de:1b80", "10de:10f0"}, Blacklist: []string{"nouveau"},
	}}); st.State != protocol.RunOK || len(h.calls) != 0 {
		t.Errorf("tweede keer: %s %v", st.State, h.calls)
	}
	// Uitzetten haalt alles weer weg.
	_, st = pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEPassthrough, Passthrough: &protocol.PVEPassthroughSpec{}})
	if st.State != protocol.RunOK {
		t.Fatal(st.Error)
	}
	grub, _ = os.ReadFile(filepath.Join(root, "etc/default/grub"))
	if !strings.Contains(string(grub), `GRUB_CMDLINE_LINUX_DEFAULT="quiet"`) {
		t.Errorf("grub na uitzetten:\n%s", grub)
	}
	if _, err := os.Stat(filepath.Join(root, vfioModprobe)); !os.IsNotExist(err) {
		t.Error("vfio.conf staat er nog")
	}
}

// Een stille aanroep (een nieuw API-token) zet het antwoord alleen in
// RunStatus, niet in de uitvoer.
func TestPVEAPIQuietHidesAnswer(t *testing.T) {
	root := pveRoot(t)
	const value = "3f1c2a9e-7b6d-4e0f-9a8b-1c2d3e4f5a6b"
	h := &fakeHost{reply: func(c string) (string, int) {
		if strings.HasPrefix(c, "pvesh create /access/users/homepage@pve/token/dashboard") {
			return `{"full-tokenid":"homepage@pve!dashboard","info":{"privsep":1},"value":"` + value + `"}` + "\n", 0
		}
		return "", 0
	}}
	out, st := pveRun(t, root, h, nil, protocol.PVERun{Action: protocol.PVEAPI, API: &protocol.PVEAPICall{
		Method: "create", Path: "/access/users/homepage@pve/token/dashboard", Params: map[string]string{"privsep": "1"}, Quiet: true,
	}})
	if st.State != protocol.RunOK {
		t.Fatalf("stand %s: %s\n%s", st.State, st.Error, out)
	}
	if strings.Contains(out, value) {
		t.Errorf("de tokenwaarde staat in de uitvoer:\n%s", out)
	}
	var got struct {
		Value string `json:"value"`
	}
	if st.PVE == nil || json.Unmarshal(st.PVE.Data, &got) != nil || got.Value != value {
		t.Errorf("de tokenwaarde ontbreekt in RunStatus: %+v", st.PVE)
	}
}
