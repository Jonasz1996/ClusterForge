package agent

import (
	"bufio"
	"encoding/hex"
	"math/bits"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De netwerkfacts komen uit /sys, /proc en /etc, zonder externe commando's.

const (
	maxRoutes    = 200
	maxListeners = 200
)

// netFacts vult soort, ouder, master, VLAN en MTU van de interfaces aan, en
// zet routes, gateway, DNS en luisterende poorten in f.
func (c *Collector) netFacts(f *protocol.Facts) {
	vlans := c.vlanConfig()
	for i := range f.Interfaces {
		c.interfaceDetails(&f.Interfaces[i], vlans)
	}
	f.Routes = c.routes()
	f.DefaultGateway = defaultGateway(f.Routes)
	f.DNS = c.nameservers()
	f.Listeners = c.listeners(c.socketUnits())
}

type vlanInfo struct {
	id     int
	parent string
}

// vlanConfig leest /proc/net/vlan/config: "eth0.20 | 20 | eth0".
func (c *Collector) vlanConfig() map[string]vlanInfo {
	out := map[string]vlanInfo{}
	for line := range strings.SplitSeq(c.read("/proc/net/vlan/config"), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			continue
		}
		out[strings.TrimSpace(parts[0])] = vlanInfo{id: id, parent: strings.TrimSpace(parts[2])}
	}
	return out
}

func (c *Collector) interfaceDetails(ifc *protocol.Interface, vlans map[string]vlanInfo) {
	dir := "/sys/class/net/" + ifc.Name
	if strings.ContainsAny(ifc.Name, "/") || ifc.Name == "." || ifc.Name == ".." {
		return
	}
	ifc.MTU, _ = strconv.Atoi(c.read(dir + "/mtu"))
	if target, err := os.Readlink(c.path(dir + "/master")); err == nil {
		ifc.Master = filepath.Base(target)
	}
	devtype := ""
	for line := range strings.SplitSeq(c.read(dir+"/uevent"), "\n") {
		if v, ok := strings.CutPrefix(line, "DEVTYPE="); ok {
			devtype = v
		}
	}
	exists := func(p string) bool { _, err := os.Lstat(c.path(dir + "/" + p)); return err == nil }
	switch {
	case devtype == "vlan" || vlans[ifc.Name].id > 0:
		ifc.Kind = "vlan"
		if v, ok := vlans[ifc.Name]; ok {
			ifc.VLANID, ifc.Parent = v.id, v.parent
		} else if parent, id, ok := strings.Cut(ifc.Name, "."); ok {
			ifc.Parent = parent
			ifc.VLANID, _ = strconv.Atoi(id)
		}
	case devtype == "bridge" || exists("bridge"):
		ifc.Kind = "bridge"
	case devtype == "bond" || exists("bonding"):
		ifc.Kind = "bond"
	case devtype == "wireguard":
		ifc.Kind = "wireguard"
	case exists("tun_flags"):
		ifc.Kind = "tun"
	case exists("device"):
		ifc.Kind = "physical"
	case c.read(dir+"/type") == "1" && c.read(dir+"/iflink") != c.read(dir+"/ifindex"):
		ifc.Kind = "veth"
	case strings.HasPrefix(ifc.Name, "dummy"):
		ifc.Kind = "dummy"
	default:
		ifc.Kind = "other"
	}
}

// routes leest de routes uit /proc/net/route en /proc/net/ipv6_route.
// Lokale routes, multicast en link-local blijven weg.
func (c *Collector) routes() []protocol.Route {
	var out []protocol.Route
	for i, line := range strings.Split(c.read("/proc/net/route"), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 8 || f[0] == "lo" {
			continue
		}
		flags, _ := strconv.ParseUint(f[3], 16, 32)
		if flags&0x1 == 0 {
			continue
		}
		dest, ok1 := procIPv4(f[1])
		gw, ok2 := procIPv4(f[2])
		mask, ok3 := procIPv4(f[7])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		ones := bits.OnesCount32(uint32(mask[0])<<24 | uint32(mask[1])<<16 | uint32(mask[2])<<8 | uint32(mask[3]))
		r := protocol.Route{Dest: dest.String() + "/" + strconv.Itoa(ones), Interface: f[0]}
		r.Metric, _ = strconv.Atoi(f[6])
		if flags&0x2 != 0 {
			r.Gateway = gw.String()
		}
		out = append(out, r)
	}
	for line := range strings.SplitSeq(c.read("/proc/net/ipv6_route"), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[9] == "lo" {
			continue
		}
		flags, _ := strconv.ParseUint(f[8], 16, 32)
		plen, _ := strconv.ParseUint(f[1], 16, 8)
		dest, ok1 := procIPv6(f[0])
		gw, ok2 := procIPv6(f[4])
		if flags&0x1 == 0 || !ok1 || !ok2 || plen == 128 || dest.IsMulticast() || dest.IsLinkLocalUnicast() {
			continue
		}
		r := protocol.Route{Dest: dest.String() + "/" + strconv.FormatUint(plen, 10), Interface: f[9]}
		m, _ := strconv.ParseUint(f[5], 16, 32)
		r.Metric = int(m)
		if flags&0x2 != 0 && !gw.IsUnspecified() {
			r.Gateway = gw.String()
		}
		out = append(out, r)
	}
	if len(out) > maxRoutes {
		out = out[:maxRoutes]
	}
	return out
}

// defaultGateway is de gateway van de IPv4-standaardroute met de laagste
// metric.
func defaultGateway(routes []protocol.Route) string {
	best, metric := "", -1
	for _, r := range routes {
		if r.Dest == "0.0.0.0/0" && r.Gateway != "" && (metric < 0 || r.Metric < metric) {
			best, metric = r.Gateway, r.Metric
		}
	}
	return best
}

// procIPv4 leest een adres uit /proc/net/route: hex in de bytevolgorde van
// de kernel.
func procIPv4(s string) (net.IP, bool) {
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 4 {
		return nil, false
	}
	return net.IPv4(raw[3], raw[2], raw[1], raw[0]).To4(), true
}

// procIPv6 leest een adres uit /proc/net/ipv6_route, in netwerkvolgorde.
func procIPv6(s string) (net.IP, bool) {
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 16 {
		return nil, false
	}
	return net.IP(raw), true
}

// nameservers leest /etc/resolv.conf. Staat daar alleen de stub van
// systemd-resolved, dan komen de echte servers uit diens resolv.conf.
func (c *Collector) nameservers() []string {
	parse := func(p string) []string {
		var out []string
		for line := range strings.SplitSeq(c.read(p), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil && !slices.Contains(out, f[1]) {
				out = append(out, f[1])
			}
		}
		return out
	}
	ns := parse("/etc/resolv.conf")
	if len(ns) == 1 && ns[0] == "127.0.0.53" {
		if up := parse("/run/systemd/resolve/resolv.conf"); len(up) > 0 {
			return up
		}
	}
	return ns
}

// procSocket is één regel uit /proc/net/tcp, tcp6, udp of udp6.
type procSocket struct {
	proto        string
	local, rem   net.IP
	lport, rport int
	state        string
	inode        string
}

// sockets leest de sockets van één protocol (tcp of udp), IPv4 en IPv6.
func (c *Collector) sockets(proto string) []procSocket {
	var out []procSocket
	for _, name := range []string{proto, proto + "6"} {
		file, err := os.Open(c.path("/proc/net/" + name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(file)
		first := true
		for sc.Scan() {
			if first {
				first = false
				continue
			}
			// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
			f := strings.Fields(sc.Text())
			if len(f) < 10 {
				continue
			}
			lip, lport, ok1 := parseProcAddr(f[1])
			rip, rport, ok2 := parseProcAddr(f[2])
			if !ok1 || !ok2 {
				continue
			}
			out = append(out, procSocket{proto: proto, local: unmap(lip), rem: unmap(rip), lport: lport, rport: rport, state: f[3], inode: f[9]})
		}
		_ = file.Close()
	}
	return out
}

// unmap maakt van ::ffff:10.0.0.1 weer 10.0.0.1.
func unmap(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

// ephemeral geeft de reeks tijdelijke poorten van de kernel.
func (c *Collector) ephemeral() (int, int) {
	f := strings.Fields(c.read("/proc/sys/net/ipv4/ip_local_port_range"))
	if len(f) == 2 {
		lo, err1 := strconv.Atoi(f[0])
		hi, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil && lo <= hi {
			return lo, hi
		}
	}
	return 32768, 60999
}

// listeners geeft de luisterende TCP-poorten en de ongebonden UDP-poorten,
// zonder loopback-duplicaten en zonder tijdelijke poorten.
func (c *Collector) listeners(units map[string]string) []protocol.Listener {
	lo, hi := c.ephemeral()
	seen := map[string]bool{}
	var out []protocol.Listener
	for _, proto := range []string{"tcp", "udp"} {
		for _, s := range c.sockets(proto) {
			// TCP 0A is LISTEN; UDP 07 zonder afzender is een gebonden poort.
			if proto == "tcp" && s.state != "0A" || proto == "udp" && (s.state != "07" || s.rport != 0) {
				continue
			}
			if s.lport >= lo && s.lport <= hi {
				continue
			}
			l := protocol.Listener{Proto: proto, Addr: s.local.String(), Port: s.lport, Unit: units[s.inode]}
			key := proto + " " + net.JoinHostPort(l.Addr, strconv.Itoa(l.Port))
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		return out[i].Addr < out[j].Addr
	})
	if len(out) > maxListeners {
		out = out[:maxListeners]
	}
	return out
}

// socketUnits koppelt het inode van elke socket aan de systemd-unit van
// het proces dat hem open heeft, via /proc/<pid>/fd en /proc/<pid>/cgroup.
func (c *Collector) socketUnits() map[string]string {
	out := map[string]string{}
	procs, err := os.ReadDir(c.path("/proc"))
	if err != nil {
		return out
	}
	for _, p := range procs {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		unit := unitFromCgroup(c.read("/proc/" + p.Name() + "/cgroup"))
		if unit == "" {
			continue
		}
		fds, err := os.ReadDir(c.path("/proc/" + p.Name() + "/fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(c.path("/proc/" + p.Name() + "/fd/" + fd.Name()))
			if err != nil {
				continue
			}
			if inode, ok := strings.CutPrefix(target, "socket:["); ok {
				inode = strings.TrimSuffix(inode, "]")
				if _, taken := out[inode]; !taken {
					out[inode] = unit
				}
			}
		}
	}
	return out
}

// unitFromCgroup haalt de service uit /proc/<pid>/cgroup, zoals nginx uit
// 0::/system.slice/nginx.service. Een proces in een sessie of scope heeft
// geen unit.
func unitFromCgroup(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || (parts[0] != "0" && !strings.Contains(parts[1], "name=systemd")) {
			continue
		}
		for _, el := range slices.Backward(strings.Split(parts[2], "/")) {
			if name, ok := strings.CutSuffix(el, ".service"); ok && name != "" {
				return name
			}
		}
	}
	return ""
}
