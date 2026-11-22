package proxmox

import (
	"context"
	"reflect"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

func TestParseGuestConfig(t *testing.T) {
	vm := ParseGuestConfig(map[string]any{
		"boot":      "order=scsi0;net0",
		"scsi0":     "local-lvm:vm-101-disk-0,iothread=1,size=32G",
		"scsi1":     "ceph:vm-101-disk-1,size=100G",
		"ide2":      "none,media=cdrom",
		"ide0":      "local-lvm:vm-101-cloudinit,media=cdrom",
		"sata0":     "local:iso/debian-13.iso,media=cdrom,size=700M",
		"unused0":   "local-lvm:vm-101-disk-9",
		"efidisk0":  "local-lvm:vm-101-disk-2,efitype=4m,size=4M",
		"net0":      "virtio=BC:24:11:AA:BB:01,bridge=vmbr0,firewall=1",
		"net10":     "virtio=BC:24:11:AA:BB:03,bridge=vmbr2,link_down=1",
		"net2":      "e1000=BC:24:11:AA:BB:02,bridge=vmbr1,tag=20",
		"ipconfig0": "ip=192.168.1.10/24,gw=192.168.1.1",
		"ipconfig1": "ip=dhcp",
		"onboot":    float64(1),
		"memory":    "4096",
	})
	wantDisks := []Disk{
		{Key: "efidisk0", Storage: "local-lvm", SizeBytes: 4 << 20},
		{Key: "ide0", Storage: "local-lvm"},
		{Key: "sata0", Storage: "local", SizeBytes: 700 << 20},
		{Key: "scsi0", Storage: "local-lvm", SizeBytes: 32 << 30},
		{Key: "scsi1", Storage: "ceph", SizeBytes: 100 << 30},
	}
	if !reflect.DeepEqual(vm.Disks, wantDisks) {
		t.Errorf("schijven: %+v", vm.Disks)
	}
	wantNICs := []NIC{
		{Key: "net0", Bridge: "vmbr0", MAC: "bc:24:11:aa:bb:01"},
		{Key: "net2", Bridge: "vmbr1", Tag: 20, MAC: "bc:24:11:aa:bb:02"},
		{Key: "net10", Bridge: "vmbr2", MAC: "bc:24:11:aa:bb:03", LinkDown: true},
	}
	if !reflect.DeepEqual(vm.NICs, wantNICs) {
		t.Errorf("netwerkkaarten: %+v", vm.NICs)
	}
	if !vm.OnBoot || !reflect.DeepEqual(vm.Addresses, []string{"192.168.1.10"}) {
		t.Errorf("onboot en adressen: %+v", vm)
	}

	ct := ParseGuestConfig(map[string]any{
		"rootfs": "local-zfs:subvol-200-disk-0,size=8G",
		"mp0":    "/srv/data,mp=/data",
		"mp1":    "nas:subvol-200-disk-1,mp=/backup,size=50G",
		"net0":   "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:CC:00:01,ip=10.0.0.5/24,gw=10.0.0.1,ip6=fd00::5/64,type=veth",
		"net1":   "name=eth1,bridge=vmbr1,hwaddr=BC:24:11:CC:00:02,ip=dhcp,type=veth",
	})
	wantDisks = []Disk{
		{Key: "mp0", Path: "/srv/data"},
		{Key: "mp1", Storage: "nas", SizeBytes: 50 << 30},
		{Key: "rootfs", Storage: "local-zfs", SizeBytes: 8 << 30},
	}
	if !reflect.DeepEqual(ct.Disks, wantDisks) {
		t.Errorf("container-schijven: %+v", ct.Disks)
	}
	if len(ct.NICs) != 2 || ct.NICs[0].MAC != "bc:24:11:cc:00:01" || ct.NICs[1].Bridge != "vmbr1" {
		t.Errorf("container-netwerk: %+v", ct.NICs)
	}
	if !reflect.DeepEqual(ct.Addresses, []string{"10.0.0.5", "fd00::5"}) || ct.OnBoot {
		t.Errorf("container-adressen: %+v", ct.Addresses)
	}
}

func TestInterfaces(t *testing.T) {
	pve, srv, fp := newFake(t)
	pve.AddGuest(pvefake.Guest{
		Type: "lxc", VMID: 300, Name: "clusterforge", Node: "pve1", Status: "running",
		Config: map[string]string{"net0": "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:CC:00:09,ip=dhcp"},
		Inet:   map[string]string{"eth0": "192.168.1.50/24"},
	})
	c, err := NewClient(Config{URL: srv.URL, TokenID: "clusterforge@pve!cf", TokenSecret: "geheim", Fingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	ifs, err := c.Interfaces(context.Background(), Guest{Type: "lxc", Node: "pve1", VMID: 300})
	if err != nil {
		t.Fatal(err)
	}
	if got := interfaceIPs(ifs); !reflect.DeepEqual(got, []string{"192.168.1.50"}) {
		t.Fatalf("adressen: %v uit %+v", got, ifs)
	}
	if ifs[1].HWAddr != "bc:24:11:cc:00:09" {
		t.Fatalf("mac: %+v", ifs)
	}
}
