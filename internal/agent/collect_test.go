package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeFile(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseKeepalivedVIPs(t *testing.T) {
	conf := `
vrrp_instance VI_1 {
    state MASTER
    interface eth0
    virtual_router_id 51
    virtual_ipaddress {
        10.0.10.100/24 dev eth0 # web
        10.0.10.101
    }
    track_script { chk_nginx }
}
vrrp_instance VI_2 {
    virtual_ipaddress
    {
        2001:db8::10/64
        10.0.10.100
    }
}
`
	got := parseKeepalivedVIPs(conf)
	want := []string{"10.0.10.100", "10.0.10.101", "2001:db8::10"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseAptSimulation(t *testing.T) {
	out := `Reading package lists...
Inst libssl3 [3.0.11-1] (3.0.15-1~deb12u1 Debian-Security:12/stable-security [amd64])
Inst curl [7.88.1-10] (7.88.1-10+deb12u8 Debian:12.8/stable [amd64])
Conf libssl3 (3.0.15-1~deb12u1 Debian-Security:12/stable-security [amd64])
`
	u := parseAptSimulation([]byte(out))
	if u.Total != 2 || u.Security != 1 || !slices.Equal(u.Packages, []string{"libssl3", "curl"}) {
		t.Fatalf("got %+v", u)
	}
}

func TestParseSystemctlShow(t *testing.T) {
	out := "Id=ssh.service\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\n\n" +
		"Id=keepalived.service\nLoadState=not-found\nActiveState=inactive\nUnitFileState=\n\n" +
		"Id=docker.service\nLoadState=loaded\nActiveState=failed\nUnitFileState=enabled\n"
	got := parseSystemctlShow([]byte(out))
	if len(got) != 2 || got[0].Name != "ssh" || got[1].Active != "failed" {
		t.Fatalf("got %+v", got)
	}
}

// De PostgreSQL-instantie komt via een patroon in dezelfde aanroep mee.
func TestServicesWithPattern(t *testing.T) {
	var args []string
	c := &Collector{Run: func(_ context.Context, name string, a ...string) ([]byte, error) {
		args = a
		return []byte("Id=postgresql.service\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\n\n" +
			"Id=postgresql@16-main.service\nLoadState=loaded\nActiveState=failed\nUnitFileState=enabled-runtime\n"), nil
	}}
	got := c.services(context.Background())
	if !slices.Contains(args, "postgresql@*-main.service") || !slices.Contains(args, "redis-server.service") || !slices.Contains(args, "php8.4-fpm.service") {
		t.Fatalf("systemctl %v", args)
	}
	if len(got) != 2 || got[1].Name != "postgresql@16-main" || got[1].Active != "failed" {
		t.Fatalf("got %+v", got)
	}
}

func TestFactsFromRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "/etc/os-release", "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_ID=\"13\"\nVERSION_CODENAME=trixie\n")
	writeFile(t, root, "/etc/machine-id", "0123456789abcdef0123456789ABCDEF\n")
	writeFile(t, root, "/proc/meminfo", "MemTotal:        2048000 kB\nSwapTotal:        512000 kB\n")
	writeFile(t, root, "/proc/stat", "cpu 1 2 3\nbtime 1759600000\n")
	writeFile(t, root, "/proc/sys/kernel/osrelease", "6.12.0-amd64\n")
	writeFile(t, root, "/proc/uptime", "12345.67 100.00\n")
	writeFile(t, root, "/proc/loadavg", "0.10 0.20 0.30 1/100 1234\n")
	writeFile(t, root, "/etc/keepalived/keepalived.conf", "vrrp_instance X {\n virtual_ipaddress {\n  10.1.1.1\n }\n}\n")
	c := &Collector{
		Root: root,
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				return []byte("Id=keepalived.service\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\n"), nil
			case "systemd-detect-virt":
				return []byte("kvm\n"), nil
			}
			return nil, errors.New("niet gevonden")
		},
		Addresses: func() []string { return []string{"10.1.1.1"} },
	}
	f := c.Facts(context.Background())
	if f.OS.ID != "debian" || f.OS.VersionID != "13" || f.OS.PrettyName != "Debian GNU/Linux 13 (trixie)" {
		t.Errorf("os: %+v", f.OS)
	}
	if f.MachineID != "0123456789abcdef0123456789abcdef" || f.Kernel != "6.12.0-amd64" || f.Virtualization != "kvm" {
		t.Errorf("facts: %+v", f)
	}
	if f.MemoryBytes != 2048000*1024 || f.SwapBytes != 512000*1024 || f.BootTime.Unix() != 1759600000 {
		t.Errorf("geheugen of boottijd: %+v", f)
	}
	if f.Keepalived == nil || f.Keepalived.Active != "active" || !slices.Equal(f.Keepalived.VIPs, []string{"10.1.1.1"}) {
		t.Errorf("keepalived: %+v", f.Keepalived)
	}
	if f.Docker != nil || f.Upgrades != nil {
		t.Errorf("docker of apt zonder binaries: %+v %+v", f.Docker, f.Upgrades)
	}
	hb := c.Heartbeat(context.Background(), "1.0")
	if hb.UptimeSeconds != 12345 || hb.Load != [3]float64{0.1, 0.2, 0.3} || hb.Services["keepalived"] != "active" {
		t.Errorf("heartbeat: %+v", hb)
	}
}
