package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSeaweedCmdline(t *testing.T) {
	p, ok := parseSeaweedCmdline([]string{"/usr/bin/weed", "-logtostderr=true", "-v", "1", "server",
		"-dir=/data", "-ip", "10.0.0.5", "-filer", "-s3", "-s3.port=9000", "-master.peers=10.0.0.5:9333,10.0.0.6:9333",
		"-master.defaultReplication=001", "-filer.password", "geheim", "-s3.secretKey=abc"})
	if !ok || p.Role != "server" {
		t.Fatalf("niet herkend: %+v", p)
	}
	if p.Ports["master"] != 9333 || p.Ports["volume"] != 8080 || p.Ports["filer"] != 8888 || p.Ports["s3"] != 9000 {
		t.Errorf("poorten: %v", p.Ports)
	}
	if p.IP != "10.0.0.5" || p.DefaultReplication != "001" || len(p.Peers) != 2 || p.Dirs[0] != "/data" {
		t.Errorf("vlaggen: %+v", p)
	}
	joined := strings.Join(p.Args, " ")
	if strings.Contains(joined, "geheim") || strings.Contains(joined, "abc") || !strings.Contains(joined, "-s3.secretKey=***") {
		t.Errorf("geheimen niet gemaskeerd: %s", joined)
	}

	if _, ok := parseSeaweedCmdline([]string{"weed", "shell"}); ok {
		t.Error("weed shell is geen dienst")
	}
	if _, ok := parseSeaweedCmdline([]string{"/bin/sh", "-c", "weed master"}); ok {
		t.Error("een shell is geen weed")
	}
	v, _ := parseSeaweedCmdline([]string{"weed", "volume", "-mserver=a:9333,b:9333", "-dir", "/srv/v1,/srv/v2", "-port=8081"})
	if v.Ports["volume"] != 8081 || len(v.Masters) != 2 || len(v.Dirs) != 2 {
		t.Errorf("volume: %+v", v)
	}
	m, _ := parseSeaweedCmdline([]string{"weed", "master", "-mdir=/meta"})
	if m.DefaultReplication != "000" || m.Ports["master"] != 9333 || m.MetaDir != "/meta" {
		t.Errorf("master: %+v", m)
	}
}

func TestCgroupOwner(t *testing.T) {
	id := strings.Repeat("ab12", 16)
	if u, c := cgroupOwner("0::/system.slice/docker-" + id + ".scope"); u != "" || c != id[:12] {
		t.Errorf("docker: %q %q", u, c)
	}
	if u, c := cgroupOwner("0::/system.slice/seaweedfs-master.service"); u != "seaweedfs-master.service" || c != "" {
		t.Errorf("systemd: %q %q", u, c)
	}
}

func TestParseSeaweedToml(t *testing.T) {
	cfg := parseSeaweedToml([]byte(`
[leveldb2]
enabled = false
dir = "./filerldb2"

[mysql2]  
enabled = true # de gedeelde store
password = "nooit"

[jwt.signing]
key = "zeergeheim"
`))
	if len(cfg.Enabled) != 1 || cfg.Enabled[0] != "mysql2" || !cfg.JWT {
		t.Errorf("toml: %+v", cfg)
	}
}

func TestSeaweedFacts(t *testing.T) {
	root := t.TempDir()
	cmd := func(args ...string) string { return strings.Join(args, "\x00") + "\x00" }
	writeFile(t, root, "/proc/101/cmdline", cmd("/usr/bin/weed", "master", "-mdir=/data/m", "-defaultReplication=000"))
	writeFile(t, root, "/proc/101/cgroup", "0::/system.slice/docker-"+strings.Repeat("c", 64)+".scope\n")
	writeFile(t, root, "/proc/102/cmdline", cmd("weed", "volume", "-dir=/srv/weed", "-mserver=localhost:9333"))
	writeFile(t, root, "/proc/102/cgroup", "0::/system.slice/weed-volume.service\n")
	writeFile(t, root, "/proc/103/cmdline", cmd("nginx", "-g", "daemon off;"))
	writeFile(t, root, "/etc/seaweedfs/filer.toml", "[leveldb2]\nenabled = true\n")
	// De container ziet zijn eigen /etc.
	if err := os.MkdirAll(filepath.Join(root, "/proc/101/root/etc/seaweedfs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "/proc/101/root/etc/seaweedfs/master.toml", "[master.maintenance]\n")

	pages := map[string]string{
		"http://127.0.0.1:9333/cluster/status": `{"IsLeader":true,"Leader":"10.0.0.5:9333","Peers":[]}`,
		"http://127.0.0.1:9333/dir/status": `{"Version":"30GB 3.80","Topology":{"Max":16,"Free":4,"DataCenters":[{"Id":"DefaultDataCenter","Racks":[{"Id":"DefaultRack","DataNodes":[{"Url":"10.0.0.5:8080","Volumes":12,"Max":16}]}]}],
			"Layouts":[{"replication":"000","collection":"","writables":[1,2,3]}]}}`,
		"http://127.0.0.1:8080/status": `{"Version":"30GB 3.80","Volumes":[
			{"Id":1,"Size":1000,"FileCount":10,"ReplicaPlacement":{},"Collection":""},
			{"Id":2,"Size":500,"FileCount":5,"ReplicaPlacement":{"node":2},"Collection":"media","ReadOnly":true}]}`,
	}
	c := &Collector{
		Root:   root,
		Statfs: func(string) (uint64, uint64, uint64, bool) { return 1000, 400, 300, true },
		HTTPGet: func(_ context.Context, url string) ([]byte, error) {
			if p, ok := pages[url]; ok {
				return []byte(p), nil
			}
			return nil, errors.New("geen antwoord")
		},
	}
	f := c.seaweed(context.Background())
	if f == nil || len(f.Processes) != 2 {
		t.Fatalf("processen: %+v", f)
	}
	if f.Processes[0].Container != strings.Repeat("c", 12) || f.Processes[1].Unit != "weed-volume.service" {
		t.Errorf("eigenaars: %+v", f.Processes)
	}
	if len(f.Processes[1].Disks) != 1 || f.Processes[1].Disks[0].Free != 300 {
		t.Errorf("schijven: %+v", f.Processes[1].Disks)
	}
	if len(f.Configs) != 2 {
		t.Errorf("configs: %+v", f.Configs)
	}
	if f.Version != "30GB 3.80" || f.Master == nil || !f.Master.IsLeader || len(f.Master.DataNodes) != 1 || f.Master.Layouts[0].Writable != 3 {
		t.Errorf("master: %+v", f.Master)
	}
	if f.Volume == nil || f.Volume.Volumes != 2 || f.Volume.Bytes != 1500 || f.Volume.ReadOnly != 1 ||
		f.Volume.Replications["000"] != 1 || f.Volume.Replications["002"] != 1 || f.Volume.Collections[0] != "media" {
		t.Errorf("volume: %+v", f.Volume)
	}
	if s := f.Stable(); s.Master != nil || s.Processes[0].PID != 0 || s.Processes[1].Disks != nil {
		t.Errorf("stable: %+v", s)
	}

	if (&Collector{Root: t.TempDir()}).seaweed(context.Background()) != nil {
		t.Error("zonder weed hoort het nil")
	}
}
