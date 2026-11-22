package agent

// Fase 34: facts, heartbeat en metrics op FreeBSD (OPNsense). Er is geen
// /proc en geen systemd; de waarden komen uit sysctl, mount -p, netstat
// (libxo-JSON) en op OPNsense uit configctl. Namen van de metrics volgen
// node_exporter, zodat dashboards en de gezondheidsscore niets anders
// hoeven te weten.

import (
	"context"
	"encoding/json"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// bsd zegt of de agent op FreeBSD draait. Tests zetten Collector.OS.
func (c *Collector) bsd() bool {
	if c.OS != "" {
		return c.OS == "freebsd"
	}
	return hostOS == "freebsd"
}

// sysctls leest sysctl-waarden in één aanroep. Onbekende namen slaat
// sysctl -i over, zodat een ontbrekende sensor geen fout is.
func (c *Collector) sysctls(ctx context.Context, names ...string) map[string]string {
	out, err := c.run(ctx, "sysctl", append([]string{"-i", "-e"}, names...)...)
	if err != nil && len(out) == 0 {
		return map[string]string{}
	}
	return parseSysctl(out)
}

// parseSysctl leest de uitvoer van sysctl -e: naam=waarde per regel.
func parseSysctl(out []byte) map[string]string {
	res := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		res[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return res
}

// sysctlUint leest een getal; 0 als het er niet is.
func sysctlUint(m map[string]string, k string) uint64 {
	n, _ := strconv.ParseUint(m[k], 10, 64)
	return n
}

// parseBoottime leest kern.boottime: "{ sec = 1731800000, usec = 1 } ...".
func parseBoottime(v string) time.Time {
	_, rest, ok := strings.Cut(v, "sec =")
	if !ok {
		return time.Time{}
	}
	num, _, _ := strings.Cut(strings.TrimSpace(rest), ",")
	n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// parseLoadavg leest vm.loadavg: "{ 0.10 0.20 0.30 }".
func parseLoadavg(v string) ([3]float64, bool) {
	var res [3]float64
	f := strings.Fields(strings.Trim(strings.TrimSpace(v), "{}"))
	if len(f) < 3 {
		return res, false
	}
	for i := range 3 {
		x, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return res, false
		}
		res[i] = x
	}
	return res, true
}

// parseClockrate geeft stathz uit kern.clockrate, de eenheid van
// kern.cp_times.
func parseClockrate(v string) float64 {
	for part := range strings.SplitSeq(strings.Trim(strings.TrimSpace(v), "{}"), ",") {
		k, val, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(k) == "stathz" {
			if n, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && n > 0 {
				return n
			}
		}
	}
	return 128
}

// bsdCPUModes zijn de kolommen van kern.cp_times per cpu.
var bsdCPUModes = []string{"user", "nice", "system", "irq", "idle"}

// bsdHeartbeat vult uptime, load en diensten.
func (c *Collector) bsdHeartbeat(ctx context.Context, hb *protocol.Heartbeat) {
	sc := c.sysctls(ctx, "kern.boottime", "vm.loadavg")
	if bt := parseBoottime(sc["kern.boottime"]); !bt.IsZero() {
		hb.UptimeSeconds = int64(time.Since(bt).Seconds())
	}
	if l, ok := parseLoadavg(sc["vm.loadavg"]); ok {
		hb.Load = l
	}
	for _, s := range c.bsdServices(ctx) {
		hb.Services[s.Name] = s.Active
	}
}

// bsdFacts beschrijft een FreeBSD-machine.
func (c *Collector) bsdFacts(ctx context.Context, f *protocol.Facts) {
	sc := c.sysctls(ctx, "kern.osrelease", "hw.physmem", "vm.swap_total", "kern.boottime", "kern.vm_guest")
	f.Kernel = sc["kern.osrelease"]
	f.MemoryBytes = sysctlUint(sc, "hw.physmem")
	f.SwapBytes = sysctlUint(sc, "vm.swap_total")
	f.BootTime = parseBoottime(sc["kern.boottime"])
	if g := sc["kern.vm_guest"]; g != "" && g != "none" {
		f.Virtualization = g
	}
	if id := strings.ToLower(strings.ReplaceAll(c.read("/etc/hostid"), "-", "")); len(id) == 32 {
		f.MachineID = id
	}
	f.OS = protocol.OSInfo{ID: "freebsd", VersionID: f.Kernel, PrettyName: "FreeBSD " + f.Kernel}
	if out, err := c.run(ctx, "freebsd-version"); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			f.OS.VersionID, f.OS.PrettyName = v, "FreeBSD "+v
		}
	}
	f.Filesystems = c.bsdFilesystems(ctx)
	f.Services = c.bsdServices(ctx)
}

// bsdMount is een regel van mount -p.
type bsdMount struct{ device, path, fstype string }

// bsdMounts leest de echte bestandssystemen uit mount -p. Van ZFS telt per
// pool één dataset, liefst die op /: alle datasets van een pool delen de
// vrije ruimte, en OPNsense maakt er een tiental.
func (c *Collector) bsdMounts(ctx context.Context) []bsdMount {
	out, err := c.run(ctx, "mount", "-p")
	if err != nil {
		return nil
	}
	return parseMountP(out)
}

func parseMountP(out []byte) []bsdMount {
	var res []bsdMount
	pools := map[string]int{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || (f[2] != "ufs" && f[2] != "zfs") {
			continue
		}
		m := bsdMount{device: f[0], path: f[1], fstype: f[2]}
		if m.fstype == "zfs" {
			pool, _, _ := strings.Cut(m.device, "/")
			if i, ok := pools[pool]; ok {
				if m.path == "/" {
					res[i] = m
				}
				continue
			}
			pools[pool] = len(res)
		}
		res = append(res, m)
	}
	return res
}

func (c *Collector) bsdFilesystems(ctx context.Context) []protocol.Filesystem {
	var out []protocol.Filesystem
	for _, m := range c.bsdMounts(ctx) {
		st, _ := c.statfs(c.path(m.path))
		out = append(out, protocol.Filesystem{
			Mount: m.path, Device: m.device, Type: m.fstype, SizeBytes: st.size, UsedBytes: st.size - st.free,
		})
	}
	return out
}

// bsdServices zijn de diensten van OPNsense (configctl service list), met
// active of inactive zoals systemd. Op een kale FreeBSD zijn er geen.
func (c *Collector) bsdServices(ctx context.Context) []protocol.Service {
	if !c.isOPNsense() {
		return nil
	}
	out, err := c.run(ctx, opnConfigctl, "service", "list")
	if err != nil {
		return nil
	}
	return parseOPNServices(out)
}

// parseOPNServices leest configctl service list: een lijst met name,
// eventueel id, en status ("... is running as pid 123.").
func parseOPNServices(out []byte) []protocol.Service {
	var list []struct {
		Name   string `json:"name"`
		ID     any    `json:"id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(out, &list) != nil {
		return nil
	}
	res := []protocol.Service{}
	for _, s := range list {
		if s.Name == "" {
			continue
		}
		name := s.Name
		if s.ID != nil {
			if id := strings.TrimSpace(strings.Trim(jsonScalar(s.ID), `"`)); id != "" {
				name += "/" + id
			}
		}
		active := "inactive"
		if strings.Contains(s.Status, "is running") {
			active = "active"
		}
		res = append(res, protocol.Service{Name: name, Active: active, Enabled: "enabled"})
	}
	slices.SortFunc(res, func(a, b protocol.Service) int { return strings.Compare(a.Name, b.Name) })
	return res
}

// jsonScalar maakt van een JSON-getal of -tekst een string.
func jsonScalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// bsdMemNames zijn de sysctls voor het geheugen.
var bsdMemNames = []string{
	"hw.physmem", "hw.pagesize", "vm.stats.vm.v_free_count", "vm.stats.vm.v_inactive_count",
	"vm.stats.vm.v_laundry_count", "vm.stats.vm.v_cache_count", "vm.stats.vm.v_active_count",
	"vm.stats.vm.v_wire_count", "vm.swap_total", "vfs.bufspace", "kstat.zfs.misc.arcstats.size",
}

// bsdMetrics verzamelt de metrics op FreeBSD.
func (c *Collector) bsdMetrics(ctx context.Context) protocol.Metrics {
	var m metricSet
	names := append([]string{"kern.cp_times", "kern.clockrate", "vm.loadavg", "kern.boottime", "kern.osrelease",
		"kern.version", "kern.hostname", "hw.ncpu", "hw.acpi.thermal.tz0.temperature"}, bsdMemNames...)
	for i := range runtime.NumCPU() {
		names = append(names, "dev.cpu."+strconv.Itoa(i)+".temperature")
	}
	sc := c.sysctls(ctx, names...)
	bsdCPUMetrics(&m, sc["kern.cp_times"], parseClockrate(sc["kern.clockrate"]))
	if l, ok := parseLoadavg(sc["vm.loadavg"]); ok {
		m.add("node_load1", l[0])
		m.add("node_load5", l[1])
		m.add("node_load15", l[2])
	}
	bsdMemMetrics(&m, sc)
	for _, mt := range c.bsdMounts(ctx) {
		st, ok := c.statfs(c.path(mt.path))
		if !ok || st.size == 0 {
			continue
		}
		l := []string{"device", mt.device, "fstype", mt.fstype, "mountpoint", mt.path}
		m.add("node_filesystem_size_bytes", float64(st.size), l...)
		m.add("node_filesystem_free_bytes", float64(st.free), l...)
		m.add("node_filesystem_avail_bytes", float64(st.avail), l...)
	}
	if out, err := c.run(ctx, "netstat", "--libxo", "json", "-i", "-b", "-n", "-W"); err == nil {
		bsdNetMetrics(&m, out)
	}
	bsdTempMetrics(&m, sc)
	if bt := parseBoottime(sc["kern.boottime"]); !bt.IsZero() {
		m.add("node_boot_time_seconds", float64(bt.Unix()))
	}
	m.add("node_uname_info", 1, "sysname", "FreeBSD", "release", sc["kern.osrelease"],
		"version", strings.TrimSpace(strings.SplitN(sc["kern.version"], "\n", 2)[0]), "machine", unameMachine(), "nodename", sc["kern.hostname"])
	if len(m.samples) > protocol.MaxSamples {
		m.samples = m.samples[:protocol.MaxSamples]
	}
	return protocol.Metrics{Samples: m.samples}
}

// bsdCPUMetrics zet kern.cp_times (5 tellers per cpu, in stathz) om naar
// node_cpu_seconds_total.
func bsdCPUMetrics(m *metricSet, cpTimes string, hz float64) {
	f := strings.Fields(cpTimes)
	for i := 0; i+len(bsdCPUModes) <= len(f); i += len(bsdCPUModes) {
		cpu := strconv.Itoa(i / len(bsdCPUModes))
		for j, mode := range bsdCPUModes {
			v, err := strconv.ParseFloat(f[i+j], 64)
			if err != nil {
				continue
			}
			m.add("node_cpu_seconds_total", v/hz, "cpu", cpu, "mode", mode)
		}
	}
}

// bsdMemMetrics geeft de geheugenmetrics met de namen van Linux. Beschikbaar
// is vrij + inactief + laundry + cache, zoals top het telt; de ARC van ZFS
// telt als cache (hij geeft geheugen terug als het nodig is).
func bsdMemMetrics(m *metricSet, sc map[string]string) {
	page := sysctlUint(sc, "hw.pagesize")
	total := sysctlUint(sc, "hw.physmem")
	if page == 0 || total == 0 {
		return
	}
	pages := func(k string) uint64 { return sysctlUint(sc, "vm.stats.vm."+k) * page }
	free := pages("v_free_count")
	cached := pages("v_inactive_count") + pages("v_laundry_count") + pages("v_cache_count")
	arc := sysctlUint(sc, "kstat.zfs.misc.arcstats.size")
	m.add("node_memory_MemTotal_bytes", float64(total))
	m.add("node_memory_MemFree_bytes", float64(free))
	m.add("node_memory_MemAvailable_bytes", float64(min(free+cached+arc, total)))
	m.add("node_memory_Cached_bytes", float64(cached+arc))
	m.add("node_memory_Buffers_bytes", float64(sysctlUint(sc, "vfs.bufspace")))
	if swap := sysctlUint(sc, "vm.swap_total"); swap > 0 {
		m.add("node_memory_SwapTotal_bytes", float64(swap))
	}
}

// bsdNetMetrics leest netstat --libxo json -i -b -n: per interface één
// regel met network <Link#n> en de tellers.
func bsdNetMetrics(m *metricSet, out []byte) {
	var doc struct {
		Statistics struct {
			Interface []map[string]any `json:"interface"`
		} `json:"statistics"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return
	}
	cols := []struct{ key, metric string }{
		{"received-bytes", "node_network_receive_bytes_total"},
		{"received-errors", "node_network_receive_errs_total"},
		{"sent-bytes", "node_network_transmit_bytes_total"},
		{"send-errors", "node_network_transmit_errs_total"},
	}
	seen := map[string]bool{}
	for _, row := range doc.Statistics.Interface {
		name, _ := row["name"].(string)
		network, _ := row["network"].(string)
		name = strings.TrimSuffix(name, "*")
		if name == "" || !strings.HasPrefix(network, "<Link") || seen[name] || ignoredBSDNetDevs(name) {
			continue
		}
		seen[name] = true
		for _, col := range cols {
			if v, ok := row[col.key].(float64); ok {
				m.add(col.metric, v, "device", name)
			}
		}
	}
}

// ignoredBSDNetDevs zijn loopback en pseudo-interfaces zonder eigen verkeer.
func ignoredBSDNetDevs(name string) bool {
	for _, p := range []string{"lo", "pflog", "pfsync", "enc", "ipfw"} {
		if strings.HasPrefix(name, p) {
			if _, err := strconv.Atoi(strings.TrimPrefix(name, p)); err == nil {
				return true
			}
		}
	}
	return false
}

// bsdTempMetrics: dev.cpu.N.temperature (coretemp/amdtemp) en de
// ACPI-zone, als "45.0C".
func bsdTempMetrics(m *metricSet, sc map[string]string) {
	keys := make([]string, 0, len(sc))
	for k := range sc {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		var chip, sensor string
		switch {
		case strings.HasPrefix(k, "dev.cpu.") && strings.HasSuffix(k, ".temperature"):
			chip, sensor = "cpu", "core"+strings.TrimSuffix(strings.TrimPrefix(k, "dev.cpu."), ".temperature")
		case k == "hw.acpi.thermal.tz0.temperature":
			chip, sensor = "acpi", "tz0"
		default:
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSuffix(sc[k], "C"), 64)
		if err != nil {
			continue
		}
		m.add("node_hwmon_temp_celsius", v, "chip", chip, "sensor", sensor)
	}
}
