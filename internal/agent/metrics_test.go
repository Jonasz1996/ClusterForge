package agent

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestMetricsFromRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "/proc/stat", `cpu  300 0 150 2000 10 0 0 0 0 0
cpu0 100 0 50 1000 5 0 0 0 0 0
cpu1 200 0 100 1000 5 1 2 3 0 0
intr 12345
btime 1700000000
`)
	writeFile(t, root, "/proc/loadavg", "0.50 0.40 0.30 1/100 1234\n")
	writeFile(t, root, "/proc/meminfo", "MemTotal:       16000 kB\nMemFree:  2000 kB\nMemAvailable:   8000 kB\nSwapTotal: 0 kB\nShmem: 5 kB\n")
	writeFile(t, root, "/proc/mounts", "/dev/sda1 / ext4 rw 0 0\nproc /proc proc rw 0 0\n")
	writeFile(t, root, "/proc/diskstats", `   8       0 sda 100 0 2000 50 300 0 4000 80 0 1500 130 0 0 0 0
   8       1 sda1 90 0 1800 40 280 0 3800 70 0 1400 110 0 0 0 0
   7       0 loop0 1 0 2 0 0 0 0 0 0 0 0 0 0 0 0
`)
	writeFile(t, root, "/proc/net/dev", `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 500 5 0 0 0 0 0 0 500 5 0 0 0 0 0 0
  eth0: 1000 10 1 2 0 0 0 0 2000 20 3 4 0 0 0 0
vethabc: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0
`)
	writeFile(t, root, "/sys/class/hwmon/hwmon0/name", "coretemp\n")
	writeFile(t, root, "/sys/class/hwmon/hwmon0/temp1_input", "45000\n")
	writeFile(t, root, "/proc/sys/kernel/ostype", "Linux\n")
	writeFile(t, root, "/proc/sys/kernel/hostname", "web01\n")

	m := (&Collector{Root: root}).Metrics()
	got := map[string]float64{}
	for _, s := range m.Samples {
		got[key(s)] = s.Value
	}
	want := map[string]float64{
		`node_cpu_seconds_total{cpu="0",mode="user"}`:  1,
		`node_cpu_seconds_total{cpu="1",mode="idle"}`:  10,
		`node_cpu_seconds_total{cpu="1",mode="steal"}`: 0.03,
		`node_load1`:                                                0.5,
		`node_load15`:                                               0.3,
		`node_memory_MemTotal_bytes`:                                16000 * 1024,
		`node_memory_MemAvailable_bytes`:                            8000 * 1024,
		`node_memory_SwapTotal_bytes`:                               0,
		`node_disk_read_bytes_total{device="sda"}`:                  2000 * 512,
		`node_disk_written_bytes_total{device="sda"}`:               4000 * 512,
		`node_disk_io_time_seconds_total{device="sda"}`:             1.5,
		`node_network_receive_bytes_total{device="eth0"}`:           1000,
		`node_network_transmit_errs_total{device="eth0"}`:           3,
		`node_network_transmit_drop_total{device="eth0"}`:           4,
		`node_hwmon_temp_celsius{chip="hwmon0",sensor="temp1"}`:     45,
		`node_hwmon_chip_names{chip="hwmon0",chip_name="coretemp"}`: 1,
		`node_boot_time_seconds`:                                    1700000000,
	}
	for k, v := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%s ontbreekt", k)
			continue
		}
		if fmt.Sprintf("%.6g", g) != fmt.Sprintf("%.6g", v) {
			t.Errorf("%s = %v, want %v", k, g, v)
		}
	}
	uname := false
	for _, s := range m.Samples {
		uname = uname || (s.Name == "node_uname_info" && s.Labels["nodename"] == "web01" && s.Labels["sysname"] == "Linux")
	}
	if !uname {
		t.Error("node_uname_info ontbreekt")
	}
	if got[`node_filesystem_size_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/"}`] <= 0 {
		t.Error("geen grootte voor /")
	}
	for k := range got {
		for _, bad := range []string{`cpu=""`, `device="sda1"`, "loop0", `"lo"`, "veth", "node_memory_Shmem", `"proc"`} {
			if strings.Contains(k, bad) {
				t.Errorf("onverwachte metric %s", k)
			}
		}
	}
}

func key(s protocol.Sample) string {
	if len(s.Labels) == 0 {
		return s.Name
	}
	parts := make([]string, 0, len(s.Labels))
	for k, v := range s.Labels {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	sort.Strings(parts)
	return s.Name + "{" + strings.Join(parts, ",") + "}"
}
