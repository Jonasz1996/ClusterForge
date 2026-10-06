package backups

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNotIsolatable(t *testing.T) {
	ok := map[string]string{
		"name": "web01", "cores": "2", "memory": "2048", "agent": "1", "ostype": "l26", "scsihw": "virtio-scsi-single",
		"boot": "order=scsi0", "scsi0": "local-lvm:vm-101-disk-0,iothread=1,size=32G",
		"ide2": "local-lvm:vm-101-cloudinit,media=cdrom", "ide0": "none,media=cdrom", "efidisk0": "local-lvm:vm-101-disk-1,size=4M",
		"net0": "virtio=BC:24:11:00:00:01,bridge=vmbr0,firewall=1", "ipconfig0": "ip=dhcp", "ciuser": "debian",
		"sshkeys": "ssh-ed25519%20AAAA", "serial0": "socket", "vga": "serial0", "smbios1": "uuid=1234", "vmgenid": "abcd",
		"tags": "web", "description": "x", "numa0": "cpus=0-1,memory=2048", "unused0": "local-lvm:vm-101-disk-2",
	}
	if bad := notIsolatable(ok); len(bad) != 0 {
		t.Fatalf("een gewone VM is niet te isoleren: %v", bad)
	}
	for key, value := range map[string]string{
		"virtiofs0":  "share,cache=auto",
		"hostpci0":   "0000:01:00.0",
		"usb0":       "host=1234:5678",
		"args":       "-device foo",
		"hookscript": "local:snippets/hook.pl",
		"parallel0":  "/dev/parport0",
		"serial1":    "/dev/ttyS0",
		"scsi1":      "/dev/disk/by-id/ata-SAMSUNG,size=100G",
	} {
		cfg := map[string]string{"name": "x", key: value}
		if bad := notIsolatable(cfg); len(bad) != 1 || !strings.HasPrefix(bad[0], key) {
			t.Errorf("%s=%s: %v", key, value, bad)
		}
	}
}

func TestIsolation(t *testing.T) {
	cfg := map[string]string{
		"name": "web01", "net0": "virtio=BC:24:11:00:00:01,bridge=vmbr0", "net1": "virtio=BC:24:11:00:00:02,bridge=vmbr1,link_down=0",
		"onboot": "1", "protection": "1", "tags": "web;prod", "smbios1": "uuid=5f1d,serial=b2xk,base64=1",
		"ide2": "local:iso/debian-13.iso,media=cdrom", "ide3": "local-lvm:vm-101-cloudinit,media=cdrom",
		"scsi0": "local-lvm:vm-131-disk-0,size=32G",
	}
	got := isolation(cfg, "notitie")
	want := map[string]string{
		"net0": "virtio=BC:24:11:00:00:01,bridge=vmbr0,link_down=1", "net1": "virtio=BC:24:11:00:00:02,bridge=vmbr1,link_down=1",
		"onboot": "0", "protection": "0", "tags": "web;prod;cf-sandbox", "description": "notitie",
		"smbios1": "uuid=5f1d,serial=Y2Ytc2FuZGJveA==,base64=1", "ide2": "none,media=cdrom",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, wilde %q", k, got[k], v)
		}
	}
	if _, touched := got["ide3"]; touched {
		t.Error("de cloud-init-schijf mag blijven")
	}
	if _, touched := got["scsi0"]; touched {
		t.Error("de schijf mag blijven")
	}

	// Teruggelezen na de wijziging klopt alles.
	after := map[string]string{}
	for k, v := range cfg {
		after[k] = v
	}
	for k, v := range got {
		after[k] = v
	}
	if p := isolationProblems(after); len(p) != 0 {
		t.Fatalf("na isoleren: %v", p)
	}
	if serialOf(after["smbios1"]) != sandboxSerial {
		t.Fatal("serienummer")
	}
	// Elke afwijking blokkeert het starten.
	for name, change := range map[string]func(map[string]string){
		"net1 heeft geen link_down=1": func(c map[string]string) { c["net1"] = "virtio=BC:24:11:00:00:02,bridge=vmbr1" },
		"onboot staat aan":            func(c map[string]string) { c["onboot"] = "1" },
		"de tag cf-sandbox ontbreekt": func(c map[string]string) { c["tags"] = "web" },
		"SMBIOS-serienummer":          func(c map[string]string) { c["smbios1"] = "uuid=5f1d" },
		"allowlist":                   func(c map[string]string) { c["virtiofs0"] = "share" },
	} {
		c := map[string]string{}
		for k, v := range after {
			c[k] = v
		}
		change(c)
		if p := isolationProblems(c); !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, name) }) {
			t.Errorf("%s: %v", name, p)
		}
	}
	if withSerial("", sandboxSerial) != "serial=cf-sandbox" || withSerial("uuid=1", sandboxSerial) != "uuid=1,serial=cf-sandbox" {
		t.Fatal("withSerial zonder base64")
	}
}

func TestSizes(t *testing.T) {
	cfg := map[string]string{
		"scsi0": "ceph:vm-1-disk-0,size=32G", "virtio1": "ceph:vm-1-disk-1,size=512M", "efidisk0": "ceph:vm-1-disk-2,size=4M",
		"ide2": "ceph:vm-1-cloudinit,media=cdrom,size=4M", "memory": "current=4096",
	}
	if got, want := diskBytes(cfg), int64(32<<30+512<<20+4<<20); got != want {
		t.Fatalf("schijven: %d, wilde %d", got, want)
	}
	if memoryBytes(cfg) != 4096<<20 || memoryBytes(map[string]string{"memory": "1024"}) != 1024<<20 ||
		memoryBytes(map[string]string{}) != defaultMemoryMB<<20 {
		t.Fatal("geheugen")
	}
	if !hasAgent(map[string]string{"agent": "1"}) || !hasAgent(map[string]string{"agent": "enabled=1,fstrim_cloned_disks=1"}) ||
		hasAgent(map[string]string{"agent": "0"}) || hasAgent(map[string]string{}) {
		t.Fatal("agent")
	}
	if duration(41*time.Second) != "41 s" || duration(192*time.Second) != "3 min 12 s" || duration(2*time.Hour) != "2 u" ||
		duration(3*time.Minute) != "3 min" {
		t.Fatal("duration")
	}
	if !sameHost("web01", "web01.lab.example") || !sameHost("WEB01", "web01") || sameHost("web02", "web01") {
		t.Fatal("sameHost")
	}
}
