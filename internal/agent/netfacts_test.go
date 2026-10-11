package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func symlink(t *testing.T, root, target, p string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, full); err != nil {
		t.Fatal(err)
	}
}

func TestNetFacts(t *testing.T) {
	root := t.TempDir()
	// eth0 fysiek in bond0? Nee: eth0 fysiek, vmbr0 bridge met eth1, eth0.20 VLAN.
	writeFile(t, root, "/sys/class/net/eth0/mtu", "1500\n")
	writeFile(t, root, "/sys/class/net/eth0/uevent", "INTERFACE=eth0\nIFINDEX=2\n")
	writeFile(t, root, "/sys/class/net/eth0/device/vendor", "0x1af4\n")
	writeFile(t, root, "/sys/class/net/eth1/mtu", "9000\n")
	writeFile(t, root, "/sys/class/net/eth1/device/vendor", "0x1af4\n")
	symlink(t, root, "../vmbr0", "/sys/class/net/eth1/master")
	writeFile(t, root, "/sys/class/net/vmbr0/uevent", "DEVTYPE=bridge\nINTERFACE=vmbr0\n")
	writeFile(t, root, "/sys/class/net/vmbr0/mtu", "9000\n")
	writeFile(t, root, "/sys/class/net/eth0.20/uevent", "DEVTYPE=vlan\n")
	writeFile(t, root, "/proc/net/vlan/config", "VLAN Dev name	 | VLAN ID\nName-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD\neth0.20        | 20  | eth0\n")
	writeFile(t, root, "/proc/net/route", "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"+
		"eth0\t00000000\t01140A0A\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"+
		"eth0\t00140A0A\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"+
		"eth0.20\t0000A8C0\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n")
	writeFile(t, root, "/proc/net/ipv6_route",
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00000003 eth0\n"+
			"20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 eth0\n"+
			"fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 eth0\n"+
			"00000000000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000002 00000000 80200001 lo\n")
	writeFile(t, root, "/etc/resolv.conf", "nameserver 127.0.0.53\noptions edns0\n")
	writeFile(t, root, "/run/systemd/resolve/resolv.conf", "nameserver 10.10.20.1\nnameserver 1.1.1.1\n")
	writeFile(t, root, "/proc/sys/net/ipv4/ip_local_port_range", "32768\t60999\n")
	// nginx luistert op 80 (pid 100), sshd op 22 (pid 200, in een sessie
	// zonder unit), en een tijdelijke UDP-poort telt niet mee.
	writeFile(t, root, "/proc/net/tcp", "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
		"   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0 100 0 0 10 0\n"+
		"   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1002 1 0 100 0 0 10 0\n"+
		"   2: 0B140A0A:0050 05000A0A:C350 01 00000000:00000000 00:00000000 00000000     0        0 1003 1 0 100 0 0 10 0\n"+
		"   3: 0B140A0A:D431 641E0A0A:0CEA 01 00000000:00000000 00:00000000 00000000     0        0 1004 1 0 100 0 0 10 0\n"+
		"   4: 0B140A0A:0050 0971000B:1F90 01 00000000:00000000 00:00000000 00000000     0        0 1005 1 0 100 0 0 10 0\n")
	writeFile(t, root, "/proc/net/tcp6", "  sl  local_address rem_address st\n"+
		"   0: 00000000000000000000000000000000:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1006 1 0\n")
	writeFile(t, root, "/proc/net/udp", "  sl  local_address rem_address st\n"+
		"   0: 00000000:0202 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1007 2 0\n"+
		"   1: 00000000:8235 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1008 2 0\n")
	writeFile(t, root, "/proc/100/cgroup", "0::/system.slice/nginx.service\n")
	symlink(t, root, "socket:[1001]", "/proc/100/fd/6")
	symlink(t, root, "socket:[1003]", "/proc/100/fd/7")
	symlink(t, root, "socket:[1005]", "/proc/100/fd/8")
	symlink(t, root, "socket:[1006]", "/proc/100/fd/9")
	writeFile(t, root, "/proc/200/cgroup", "0::/user.slice/user-0.slice/session-3.scope\n")
	symlink(t, root, "socket:[1002]", "/proc/200/fd/3")
	writeFile(t, root, "/proc/300/cgroup", "12:pids:/system.slice/php8.2-fpm.service\n1:name=systemd:/system.slice/php8.2-fpm.service\n")
	symlink(t, root, "socket:[1004]", "/proc/300/fd/5")

	c := &Collector{Root: root}
	f := protocol.Facts{Interfaces: []protocol.Interface{{Name: "eth0"}, {Name: "eth1"}, {Name: "vmbr0"}, {Name: "eth0.20"}}}
	c.netFacts(&f)
	kinds := map[string]protocol.Interface{}
	for _, i := range f.Interfaces {
		kinds[i.Name] = i
	}
	if i := kinds["eth0"]; i.Kind != "physical" || i.MTU != 1500 {
		t.Errorf("eth0: %+v", i)
	}
	if i := kinds["eth1"]; i.Kind != "physical" || i.Master != "vmbr0" || i.MTU != 9000 {
		t.Errorf("eth1: %+v", i)
	}
	if i := kinds["vmbr0"]; i.Kind != "bridge" {
		t.Errorf("vmbr0: %+v", i)
	}
	if i := kinds["eth0.20"]; i.Kind != "vlan" || i.VLANID != 20 || i.Parent != "eth0" {
		t.Errorf("eth0.20: %+v", i)
	}
	wantRoutes := []protocol.Route{
		{Dest: "0.0.0.0/0", Gateway: "10.10.20.1", Interface: "eth0", Metric: 100},
		{Dest: "10.10.20.0/24", Interface: "eth0", Metric: 100},
		{Dest: "192.168.0.0/16", Interface: "eth0.20"},
		{Dest: "::/0", Gateway: "fe80::1", Interface: "eth0", Metric: 1024},
		{Dest: "2001:db8::/64", Interface: "eth0", Metric: 256},
	}
	if !slices.Equal(f.Routes, wantRoutes) {
		t.Errorf("routes: %+v", f.Routes)
	}
	if f.DefaultGateway != "10.10.20.1" {
		t.Errorf("gateway: %q", f.DefaultGateway)
	}
	if !slices.Equal(f.DNS, []string{"10.10.20.1", "1.1.1.1"}) {
		t.Errorf("dns: %v", f.DNS)
	}
	wantListeners := []protocol.Listener{
		{Proto: "tcp", Addr: "0.0.0.0", Port: 22},
		{Proto: "tcp", Addr: "0.0.0.0", Port: 80, Unit: "nginx"},
		{Proto: "tcp", Addr: "::", Port: 80, Unit: "nginx"},
		{Proto: "udp", Addr: "0.0.0.0", Port: 514},
	}
	if !slices.Equal(f.Listeners, wantListeners) {
		t.Errorf("listeners: %+v", f.Listeners)
	}

	// Verbindingen: inkomend op 80 van een privé-adres met afzender, van
	// een publiek adres zonder; uitgaand van php-fpm naar 3306.
	var tr ConnTracker
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	c.SampleConns(&tr, start)
	c.SampleConns(&tr, start.Add(time.Minute))
	got, ok := tr.Take(start.Add(5 * time.Minute))
	if !ok || !got.Since.Equal(start) {
		t.Fatalf("conns: %+v %v", got, ok)
	}
	wantOut := []protocol.ConnOut{{Unit: "php8.2-fpm", Addr: "10.10.30.100", Port: 3306, Count: 2}}
	wantIn := []protocol.ConnIn{{Unit: "nginx", Port: 80, Count: 2}, {Unit: "nginx", Port: 80, Peer: "10.10.0.5", Count: 2}}
	if !slices.Equal(got.Out, wantOut) || !slices.Equal(got.In, wantIn) {
		t.Fatalf("conns: out %+v in %+v", got.Out, got.In)
	}
	if _, ok := tr.Take(start.Add(10 * time.Minute)); ok {
		t.Fatal("na Take moet de tracker leeg zijn")
	}
}

func TestParseKeepalivedInstances(t *testing.T) {
	conf := `
global_defs { router_id web01 }
vrrp_instance VI_WEB {
    state MASTER   # begint als master
    interface eth0
    virtual_router_id 51
    priority 150
    unicast_src_ip 10.0.10.11
    unicast_peer {
        10.0.10.12
        10.0.10.13
    }
    virtual_ipaddress {
        10.0.10.100/24 dev eth0 label eth0:1
    }
    track_script { chk_nginx }
}
vrrp_instance VI_DB
{
    state backup
    interface vmbr0.30
    virtual_router_id 52
    virtual_ipaddress
    {
        10.0.30.100
    }
}
`
	got := parseKeepalivedInstances(conf)
	if len(got) != 2 {
		t.Fatalf("instanties: %+v", got)
	}
	a, b := got[0], got[1]
	if a.Name != "VI_WEB" || a.Interface != "eth0" || a.VRID != 51 || a.State != "MASTER" || a.Priority != 150 ||
		!slices.Equal(a.Peers, []string{"10.0.10.12", "10.0.10.13"}) || !slices.Equal(a.VIPs, []string{"10.0.10.100"}) {
		t.Errorf("VI_WEB: %+v", a)
	}
	if b.Name != "VI_DB" || b.Interface != "vmbr0.30" || b.VRID != 52 || b.State != "BACKUP" || b.Peers != nil ||
		!slices.Equal(b.VIPs, []string{"10.0.30.100"}) {
		t.Errorf("VI_DB: %+v", b)
	}
}

func TestMaintenanceFacts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "/boot/vmlinuz-6.12.9-amd64", "")
	writeFile(t, root, "/boot/vmlinuz-6.12.48-amd64", "")
	writeFile(t, root, "/boot/vmlinuz-6.12.10-amd64", "")
	stamp := time.Date(2026, 10, 9, 6, 0, 0, 0, time.UTC)
	writeFile(t, root, "/var/lib/apt/periodic/update-success-stamp", "")
	_ = os.Chtimes(filepath.Join(root, "var/lib/apt/periodic/update-success-stamp"), stamp, stamp)
	_ = os.MkdirAll(filepath.Join(root, "var/lib/apt/lists"), 0o755)
	_ = os.Chtimes(filepath.Join(root, "var/lib/apt/lists"), stamp.Add(-time.Hour), stamp.Add(-time.Hour))
	c := &Collector{Root: root}
	f := protocol.Facts{Kernel: "6.12.10-amd64"}
	c.maintenanceFacts(&f)
	if f.KernelInstalled != "6.12.48-amd64" || !f.RebootRequired || !f.AptUpdatedAt.Equal(stamp) {
		t.Fatalf("facts: %+v", f)
	}
	f = protocol.Facts{Kernel: "6.12.48-amd64"}
	c.maintenanceFacts(&f)
	if f.RebootRequired {
		t.Fatal("zelfde kernel, geen reboot-required: toch nodig")
	}
	writeFile(t, root, "/run/reboot-required", "*** System restart required ***\n")
	c.maintenanceFacts(&f)
	if !f.RebootRequired {
		t.Fatal("/run/reboot-required genegeerd")
	}
	// De apt-tijd telt niet mee in de hash.
	if !f.Stable().AptUpdatedAt.IsZero() {
		t.Fatal("AptUpdatedAt in Stable")
	}
}

func TestCertificates(t *testing.T) {
	root := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour), KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	notAfter := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "web.example.be"}, DNSNames: []string{"web.example.be"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caTmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	pemOf := func(typ string, der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
	}
	// fullchain met de leaf en de CA, en een haproxy-bestand met sleutel.
	writeFile(t, root, "/etc/letsencrypt/archive/web/fullchain1.pem", pemOf("CERTIFICATE", leafDER)+pemOf("CERTIFICATE", caDER))
	symlink(t, root, "../../archive/web/fullchain1.pem", "/etc/letsencrypt/live/web/fullchain.pem")
	writeFile(t, root, "/etc/haproxy/certs/web.pem", pemOf("CERTIFICATE", leafDER)+pemOf("EC PRIVATE KEY", keyDER))
	writeFile(t, root, "/etc/nginx/ca.crt", pemOf("CERTIFICATE", caDER))
	c := &Collector{Root: root}
	got := c.certificates()
	if len(got) != 1 {
		t.Fatalf("certificaten: %+v", got)
	}
	if got[0].Path != "/etc/letsencrypt/live/web/fullchain.pem" || got[0].Subject != "web.example.be" || got[0].Issuer != "Test CA" ||
		!got[0].NotAfter.Equal(notAfter) || !slices.Equal(got[0].DNSNames, []string{"web.example.be"}) {
		t.Fatalf("certificaat: %+v", got[0])
	}
}

func TestVersionLess(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"6.1.0-9-amd64", "6.1.0-25-amd64", true},
		{"6.12.48+deb13-amd64", "6.12.9+deb13-amd64", false},
		{"6.8.12-4-pve", "6.8.12-10-pve", true},
		{"6.1.0", "6.1.0", false},
	} {
		if got := versionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("versionLess(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}
