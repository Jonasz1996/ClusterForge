package agent

import (
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// userHZ is de eenheid van de tellers in /proc/stat. Op Linux is die in de
// praktijk altijd 100.
const userHZ = 100

// cpuModes zijn de kolommen van een cpu-regel in /proc/stat, in volgorde.
var cpuModes = []string{"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal"}

// memFields zijn de regels uit /proc/meminfo die als metric meegaan.
var memFields = []string{"MemTotal", "MemFree", "MemAvailable", "Buffers", "Cached", "SwapTotal", "SwapFree"}

// ignoredDisks zijn partities en virtuele apparaten, zoals bij node_exporter.
var ignoredDisks = regexp.MustCompile(`^(z?ram|loop|fd|(h|s|v|xv)d[a-z]|nvme\d+n\d+p)\d+$`)

// ignoredNetDevs zijn interfaces per container of VM; op een Docker- of
// Proxmox-host zijn het er te veel om allemaal te volgen.
var ignoredNetDevs = regexp.MustCompile(`^(lo|veth.*|tap\d+i\d+|fwbr\d+i\d+|fwpr\d+p\d+|fwln\d+i\d+)$`)

// netColumns koppelt kolommen uit /proc/net/dev aan metricnamen.
var netColumns = map[int]string{
	0: "node_network_receive_bytes_total", 2: "node_network_receive_errs_total", 3: "node_network_receive_drop_total",
	8: "node_network_transmit_bytes_total", 10: "node_network_transmit_errs_total", 11: "node_network_transmit_drop_total",
}

// Metrics verzamelt de basismetrics van de node. Namen en labels volgen
// node_exporter.
func (c *Collector) Metrics() protocol.Metrics {
	var m metricSet
	c.cpuMetrics(&m)
	if f := strings.Fields(c.read("/proc/loadavg")); len(f) >= 3 {
		for i, name := range []string{"node_load1", "node_load5", "node_load15"} {
			if v, err := strconv.ParseFloat(f[i], 64); err == nil {
				m.add(name, v)
			}
		}
	}
	mem := c.meminfo()
	for _, k := range memFields {
		if v, ok := mem[k]; ok {
			m.add("node_memory_"+k+"_bytes", float64(v*1024))
		}
	}
	for _, mt := range c.mounts() {
		st, ok := statfs(c.path(mt.path))
		if !ok || st.size == 0 {
			continue
		}
		l := []string{"device", mt.device, "fstype", mt.fstype, "mountpoint", mt.path}
		m.add("node_filesystem_size_bytes", float64(st.size), l...)
		m.add("node_filesystem_free_bytes", float64(st.free), l...)
		m.add("node_filesystem_avail_bytes", float64(st.avail), l...)
	}
	c.diskMetrics(&m)
	c.netMetrics(&m)
	c.hwmonMetrics(&m)
	if bt := c.bootTime(); !bt.IsZero() {
		m.add("node_boot_time_seconds", float64(bt.Unix()))
	}
	// Grafana-dashboards voor node_exporter kiezen hun nodes via deze metric.
	m.add("node_uname_info", 1,
		"sysname", c.read("/proc/sys/kernel/ostype"), "release", c.read("/proc/sys/kernel/osrelease"),
		"version", c.read("/proc/sys/kernel/version"), "machine", unameMachine(),
		"nodename", c.read("/proc/sys/kernel/hostname"))
	if len(m.samples) > protocol.MaxSamples {
		m.samples = m.samples[:protocol.MaxSamples]
	}
	return protocol.Metrics{Samples: m.samples}
}

type metricSet struct{ samples []protocol.Sample }

// add voegt een meting toe; labels gaan als paren naam, waarde.
func (m *metricSet) add(name string, v float64, labels ...string) {
	s := protocol.Sample{Name: name, Value: v}
	if len(labels) > 0 {
		s.Labels = make(map[string]string, len(labels)/2)
		for i := 0; i+1 < len(labels); i += 2 {
			s.Labels[labels[i]] = labels[i+1]
		}
	}
	m.samples = append(m.samples, s)
}

func (c *Collector) cpuMetrics(m *metricSet) {
	for line := range strings.SplitSeq(c.read("/proc/stat"), "\n") {
		f := strings.Fields(line)
		// "cpu" zonder nummer is het totaal; node_exporter geeft alleen per cpu.
		if len(f) < 2 || !strings.HasPrefix(f[0], "cpu") || f[0] == "cpu" {
			continue
		}
		cpu := strings.TrimPrefix(f[0], "cpu")
		for i, mode := range cpuModes {
			if i+1 >= len(f) {
				break
			}
			v, err := strconv.ParseFloat(f[i+1], 64)
			if err != nil {
				continue
			}
			m.add("node_cpu_seconds_total", v/userHZ, "cpu", cpu, "mode", mode)
		}
	}
}

func (c *Collector) diskMetrics(m *metricSet) {
	for line := range strings.SplitSeq(c.read("/proc/diskstats"), "\n") {
		f := strings.Fields(line)
		if len(f) < 14 || ignoredDisks.MatchString(f[2]) {
			continue
		}
		dev := f[2]
		num := func(i int) float64 {
			v, _ := strconv.ParseFloat(f[i], 64)
			return v
		}
		m.add("node_disk_reads_completed_total", num(3), "device", dev)
		m.add("node_disk_read_bytes_total", num(5)*512, "device", dev)
		m.add("node_disk_writes_completed_total", num(7), "device", dev)
		m.add("node_disk_written_bytes_total", num(9)*512, "device", dev)
		m.add("node_disk_io_time_seconds_total", num(12)/1000, "device", dev)
	}
}

func (c *Collector) netMetrics(m *metricSet) {
	for line := range strings.SplitSeq(c.read("/proc/net/dev"), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		dev := strings.TrimSpace(name)
		f := strings.Fields(rest)
		if len(f) < 16 || ignoredNetDevs.MatchString(dev) {
			continue
		}
		for col := range 16 {
			metric, ok := netColumns[col]
			if !ok {
				continue
			}
			if v, err := strconv.ParseFloat(f[col], 64); err == nil {
				m.add(metric, v, "device", dev)
			}
		}
	}
}

// hwmonMetrics leest temperaturen uit /sys/class/hwmon, als die er zijn.
func (c *Collector) hwmonMetrics(m *metricSet) {
	dirs, _ := filepath.Glob(c.path("/sys/class/hwmon/hwmon*"))
	for _, dir := range dirs {
		chip := filepath.Base(dir)
		rel := "/sys/class/hwmon/" + chip
		if name := c.read(rel + "/name"); name != "" {
			m.add("node_hwmon_chip_names", 1, "chip", chip, "chip_name", name)
		}
		inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
		for _, in := range inputs {
			sensor := strings.TrimSuffix(filepath.Base(in), "_input")
			v, err := strconv.ParseFloat(c.read(rel+"/"+filepath.Base(in)), 64)
			if err != nil {
				continue
			}
			m.add("node_hwmon_temp_celsius", v/1000, "chip", chip, "sensor", sensor)
		}
	}
}

// unameMachine geeft de architectuur zoals uname -m.
func unameMachine() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}
