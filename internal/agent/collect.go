package agent

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// WatchedServices zijn de systemd-units die de agent volgt. Units die niet
// bestaan, verschijnen niet in de facts.
var WatchedServices = []string{
	"ssh", "keepalived", "nginx", "apache2", "haproxy", "docker", "containerd", "postgresql", "mariadb", "mysql",
	"redis-server", "memcached", "rabbitmq-server", "php8.1-fpm", "php8.2-fpm", "php8.3-fpm", "php8.4-fpm",
	"cron", "qemu-guest-agent", "pve-cluster", "pveproxy", "cf-agent",
}

// WatchedPatterns zijn units met een variabel deel. postgresql.service is op
// Debian en Ubuntu een overkoepelende unit die altijd active staat; de
// instantie, zoals postgresql@16-main, zegt of de database draait. systemctl
// show vindt via een patroon alleen geladen units, en dat is een instantie
// zodra ze enabled is of draait.
var WatchedPatterns = []string{"postgresql@*-main.service"}

// realFilesystems zijn de bestandssysteemtypes die in de facts komen.
var realFilesystems = []string{"ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "vfat", "ntfs3", "bcachefs"}

// Collector leest facts en heartbeatgegevens van het systeem.
type Collector struct {
	// Root is de bestandssysteemwortel; leeg is "/". Tests gebruiken een map.
	Root string
	// Run voert een commando uit; nil gebruikt exec. Tests vervangen het.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// PrimaryAddress geeft het adres waarmee de node naar buiten gaat.
	PrimaryAddress func() string
	// Addresses geeft de IP-adressen voor de heartbeat; nil leest ze van de
	// interfaces.
	Addresses func() []string
	// Interfaces geeft de netwerkinterfaces voor de facts; nil leest ze van
	// het systeem.
	Interfaces func() []protocol.Interface
}

func (c *Collector) path(p string) string {
	if c.Root == "" {
		return p
	}
	return filepath.Join(c.Root, p)
}

func (c *Collector) read(p string) string {
	b, err := os.ReadFile(c.path(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (c *Collector) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, name, args...)
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Output()
}

func (c *Collector) machineID() string {
	id := c.read("/etc/machine-id")
	if len(id) != 32 {
		return ""
	}
	return strings.ToLower(id)
}

// Heartbeat verzamelt de snelle gegevens voor elke heartbeat.
func (c *Collector) Heartbeat(ctx context.Context, version string) protocol.Heartbeat {
	hb := protocol.Heartbeat{
		AgentVersion: version, ProtocolVersion: protocol.Version,
		Services: map[string]string{},
	}
	if c.Addresses != nil {
		hb.Addresses = c.Addresses()
	} else {
		hb.Addresses = hostAddresses()
	}
	if f := strings.Fields(c.read("/proc/uptime")); len(f) > 0 {
		up, _ := strconv.ParseFloat(f[0], 64)
		hb.UptimeSeconds = int64(up)
	}
	if f := strings.Fields(c.read("/proc/loadavg")); len(f) >= 3 {
		for i := range 3 {
			hb.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	for _, s := range c.services(ctx) {
		hb.Services[s.Name] = s.Active
	}
	return hb
}

// Facts verzamelt de volledige beschrijving van de node.
func (c *Collector) Facts(ctx context.Context) protocol.Facts {
	hostname, _ := os.Hostname()
	f := protocol.Facts{
		Hostname:  hostname,
		MachineID: c.machineID(),
		OS:        c.osRelease(),
		Kernel:    c.read("/proc/sys/kernel/osrelease"),
		Arch:      runtime.GOARCH,
		CPUs:      runtime.NumCPU(),
	}
	if c.Interfaces != nil {
		f.Interfaces = c.Interfaces()
	} else {
		f.Interfaces = interfaces()
	}
	mem := c.meminfo()
	f.MemoryBytes, f.SwapBytes = mem["MemTotal"]*1024, mem["SwapTotal"]*1024
	f.BootTime = c.bootTime()
	if out, err := c.run(ctx, "systemd-detect-virt"); err == nil || len(out) > 0 {
		f.Virtualization = strings.TrimSpace(string(out))
	}
	if c.PrimaryAddress != nil {
		f.PrimaryAddress = c.PrimaryAddress()
	}
	f.Filesystems = c.filesystems()
	f.Services = c.services(ctx)
	f.Docker = c.docker(ctx)
	f.Keepalived = c.keepalived(f.Services)
	f.Upgrades = c.upgrades(ctx)
	return f
}

func (c *Collector) osRelease() protocol.OSInfo {
	s := c.read("/etc/os-release")
	if s == "" {
		s = c.read("/usr/lib/os-release")
	}
	kv := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if u, err := strconv.Unquote(v); err == nil {
			v = u
		} else {
			v = strings.Trim(v, `'"`)
		}
		kv[k] = v
	}
	return protocol.OSInfo{ID: kv["ID"], VersionID: kv["VERSION_ID"], Codename: kv["VERSION_CODENAME"], PrettyName: kv["PRETTY_NAME"]}
}

// meminfo geeft de waarden uit /proc/meminfo in kB.
func (c *Collector) meminfo() map[string]uint64 {
	out := map[string]uint64{}
	for line := range strings.SplitSeq(c.read("/proc/meminfo"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err == nil {
			out[k] = n
		}
	}
	return out
}

func (c *Collector) bootTime() time.Time {
	for line := range strings.SplitSeq(c.read("/proc/stat"), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err == nil {
				return time.Unix(n, 0).UTC()
			}
		}
	}
	return time.Time{}
}

type fsStat struct{ size, free, avail uint64 }

type mount struct{ device, path, fstype string }

// mounts geeft de echte bestandssystemen uit /proc/mounts.
func (c *Collector) mounts() []mount {
	var out []mount
	seen := map[string]bool{}
	for line := range strings.SplitSeq(c.read("/proc/mounts"), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !slices.Contains(realFilesystems, f[2]) {
			continue
		}
		m := mount{device: f[0], path: unescapeMount(f[1]), fstype: f[2]}
		// Dezelfde schijf op meerdere plekken (bind mounts) één keer tellen.
		if seen[m.device] && m.fstype != "zfs" {
			continue
		}
		seen[m.device] = true
		out = append(out, m)
	}
	return out
}

func (c *Collector) filesystems() []protocol.Filesystem {
	var out []protocol.Filesystem
	for _, m := range c.mounts() {
		st, _ := statfs(c.path(m.path))
		out = append(out, protocol.Filesystem{
			Mount: m.path, Device: m.device, Type: m.fstype, SizeBytes: st.size, UsedBytes: st.size - st.free,
		})
	}
	return out
}

// unescapeMount zet \040 en dergelijke in /proc/mounts terug.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// services leest de toestand van WatchedServices in één systemctl-aanroep.
func (c *Collector) services(ctx context.Context) []protocol.Service {
	args := append([]string{"show", "--property=Id,LoadState,ActiveState,UnitFileState"}, unitNames()...)
	out, err := c.run(ctx, "systemctl", args...)
	if err != nil && len(out) == 0 {
		return nil
	}
	return parseSystemctlShow(out)
}

func unitNames() []string {
	out := make([]string, 0, len(WatchedServices)+len(WatchedPatterns))
	for _, s := range WatchedServices {
		out = append(out, s+".service")
	}
	return append(out, WatchedPatterns...)
}

func parseSystemctlShow(out []byte) []protocol.Service {
	var res []protocol.Service
	for block := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n\n") {
		kv := map[string]string{}
		for line := range strings.SplitSeq(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				kv[k] = v
			}
		}
		if kv["Id"] == "" || kv["LoadState"] == "not-found" {
			continue
		}
		res = append(res, protocol.Service{
			Name: strings.TrimSuffix(kv["Id"], ".service"), Active: kv["ActiveState"], Enabled: kv["UnitFileState"],
		})
	}
	return res
}

func (c *Collector) docker(ctx context.Context) *protocol.Docker {
	out, err := c.run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return nil
	}
	d := &protocol.Docker{Version: strings.TrimSpace(string(out))}
	if ps, err := c.run(ctx, "docker", "ps", "-q"); err == nil {
		d.Containers = len(strings.Fields(string(ps)))
	}
	return d
}

func (c *Collector) keepalived(services []protocol.Service) *protocol.Keepalived {
	conf := c.read("/etc/keepalived/keepalived.conf")
	i := slices.IndexFunc(services, func(s protocol.Service) bool { return s.Name == "keepalived" })
	if conf == "" && i < 0 {
		return nil
	}
	k := &protocol.Keepalived{VIPs: parseKeepalivedVIPs(conf)}
	if i >= 0 {
		k.Active = services[i].Active
	}
	return k
}

// parseKeepalivedVIPs haalt de adressen uit de virtual_ipaddress-blokken.
// Includes worden niet gevolgd.
func parseKeepalivedVIPs(conf string) []string {
	var vips []string
	// in is de diepte van het virtual_ipaddress-blok waarin we zitten, of -1.
	depth, in, entered := 0, -1, false
	sc := bufio.NewScanner(strings.NewReader(conf))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexAny(line, "#!"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if entered && depth == in && len(fields) > 0 && fields[0] != "}" && fields[0] != "{" {
			addr, _, _ := strings.Cut(fields[0], "/")
			if net.ParseIP(addr) != nil && !slices.Contains(vips, addr) {
				vips = append(vips, addr)
			}
		}
		if len(fields) > 0 && (fields[0] == "virtual_ipaddress" || fields[0] == "virtual_ipaddress_excluded") {
			in, entered = depth+1, false
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		switch {
		case in >= 0 && !entered && depth >= in:
			entered = true
		case entered && depth < in:
			in, entered = -1, false
		}
	}
	return vips
}

const maxUpgradePackages = 200

// upgrades telt de beschikbare upgrades volgens de laatste apt update. De
// agent draait zelf geen apt update; dat verandert het systeem.
func (c *Collector) upgrades(ctx context.Context) *protocol.Upgrades {
	out, err := c.run(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=1", "dist-upgrade")
	if err != nil {
		return nil
	}
	return parseAptSimulation(out)
}

func parseAptSimulation(out []byte) *protocol.Upgrades {
	u := &protocol.Upgrades{Packages: []string{}}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || f[0] != "Inst" {
			continue
		}
		u.Total++
		if strings.Contains(sc.Text(), "-security") {
			u.Security++
		}
		if len(u.Packages) < maxUpgradePackages {
			u.Packages = append(u.Packages, f[1])
		}
	}
	return u
}

// interfaces geeft de netwerkinterfaces behalve loopback.
func interfaces() []protocol.Interface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []protocol.Interface
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		pi := protocol.Interface{Name: i.Name, MAC: i.HardwareAddr.String(), Up: i.Flags&net.FlagUp != 0, Addresses: []string{}}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			pi.Addresses = append(pi.Addresses, a.String())
		}
		out = append(out, pi)
	}
	return out
}

// hostAddresses geeft alle bruikbare IP-adressen zonder prefix, ook op lo
// (LVS-DR zet VIP's soms daar), behalve loopback en link-local.
func hostAddresses() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ipn.IP.String())
	}
	return out
}
