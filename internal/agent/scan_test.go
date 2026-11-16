package agent

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const nmapSample = `<?xml version="1.0"?>
<nmaprun scanner="nmap" args="nmap" version="7.95">
<host><status state="up"/><address addr="192.168.10.191" addrtype="ipv4"/><address addr="BC:24:11:00:00:01" addrtype="mac" vendor="Proxmox"/>
<hostnames><hostname name="db1" type="PTR"/></hostnames>
<ports>
<port protocol="tcp" portid="22"><state state="open"/><service name="ssh" product="OpenSSH" version="10.0p2 Debian 7" extrainfo="protocol 2.0"><cpe>cpe:/a:openbsd:openssh:10.0p2</cpe></service>
<script id="ssh2-enum-algos" output="kex_algorithms: (2)&#xa;  curve25519-sha256"/><script id="vuln-iets" output="mag niet mee"/></port>
<port protocol="tcp" portid="23"><state state="filtered"/></port>
<port protocol="tcp" portid="443"><state state="open"/><service name="http" tunnel="ssl" product="nginx"/></port>
</ports></host>
<host><status state="down"/><address addr="192.168.10.192" addrtype="ipv4"/></host>
<runstats><hosts up="1" down="253" total="254"/></runstats>
</nmaprun>`

func TestParseNmapXML(t *testing.T) {
	res, err := parseNmapXML([]byte(nmapSample))
	if err != nil {
		t.Fatal(err)
	}
	if res.HostsUp != 1 || res.HostsSeen != 254 || len(res.Hosts) != 1 {
		t.Fatalf("resultaat %+v", res)
	}
	h := res.Hosts[0]
	if h.Addr != "192.168.10.191" || h.Vendor != "Proxmox" || h.Hostname != "db1" || len(h.Ports) != 2 {
		t.Fatalf("host %+v", h)
	}
	if p := h.Ports[0]; p.Product != "OpenSSH" || len(p.Scripts) != 1 || !strings.Contains(p.Scripts["ssh2-enum-algos"], "curve25519") {
		t.Errorf("poort 22 %+v", p)
	}
	if h.Ports[1].Tunnel != "ssl" {
		t.Errorf("poort 443 %+v", h.Ports[1])
	}
}

func TestCheckScanSpec(t *testing.T) {
	ok := &protocol.ScanSpec{Targets: []string{"192.168.10.0/24", "192.168.10.191"}, Allowed: []string{"192.168.10.0/24"}, Exclude: []string{"192.168.10.1"}}
	if _, _, err := checkScanSpec(ok); err != nil {
		t.Fatal(err)
	}
	for name, sp := range map[string]*protocol.ScanSpec{
		"buiten scope":  {Targets: []string{"192.168.11.5"}, Allowed: []string{"192.168.10.0/24"}},
		"groter":        {Targets: []string{"192.168.0.0/16"}, Allowed: []string{"192.168.10.0/24"}},
		"hostnaam":      {Targets: []string{"example.com"}, Allowed: []string{"192.168.10.0/24"}},
		"optie":         {Targets: []string{"--script=exploit"}, Allowed: []string{"192.168.10.0/24"}},
		"geen scope":    {Targets: []string{"192.168.10.5"}},
		"te groot doel": {Targets: []string{"10.0.0.0/8"}, Allowed: []string{"10.0.0.0/8"}},
	} {
		if _, _, err := checkScanSpec(sp); err == nil {
			t.Errorf("%s: geen fout", name)
		}
	}
}

func TestScanRunsNmap(t *testing.T) {
	var got []string
	a := &Agent{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if slices.Equal(args, []string{"--version"}) {
			return []byte("Nmap version 7.95 ( https://nmap.org )\n"), nil
		}
		got = args
		return nil, os.WriteFile(args[slices.Index(args, "-oX")+1], []byte(nmapSample), 0o600)
	}}
	res, err := a.scan(context.Background(), &protocol.ScanSpec{Targets: []string{"192.168.10.0/24"}, Allowed: []string{"192.168.10.0/24"}, Profile: "standaard"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Nmap != "7.95" || len(res.Hosts) != 1 {
		t.Fatalf("resultaat %+v", res)
	}
	args := strings.Join(got, " ")
	for _, want := range []string{"--top-ports 1000", "-sV", "--script ssl-enum-ciphers,ssl-cert,ssh2-enum-algos,ssh-hostkey,http-title,http-server-header", "-n", "192.168.10.0/24"} {
		if !strings.Contains(args, want) {
			t.Errorf("geen %q in %s", want, args)
		}
	}
}
