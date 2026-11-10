package templates

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestRenderMediaPlatform(t *testing.T) {
	tpl, ok := Latest("media-platform")
	if !ok {
		t.Fatal("geen template media-platform")
	}
	params, err := tpl.Validate(map[string]any{
		"vip": "192.168.10.150", "vrid": 50, "auth_pass": "geheim12", "db_host": "192.168.10.180",
		"platform_ref": "claude/stappenplan-uitwerken-lu4uv3", "seaweedfs_image": "chrislusf/seaweedfs:3.97",
		"db_password": "Wachtwoord-123", "office_jwt_secret": "officegeheim1", "office_link_secret": "linkgeheim12",
		"media_worker_key": "mediasleutel1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if params["replication"] != "001" || params["nodes"] != 3 || params["db_port"] != 5433 {
		t.Fatalf("standaarden: %+v", params)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Platform", Slug: "platform", Environment: "prod"}}
	for i := 1; i <= 3; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("media-%02d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.168.10.%d", 145+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[1]
	steps, err := tpl.Steps("node", c)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	var services []string
	for _, s := range steps {
		if s.File != nil {
			files[s.File.Path] = s.File.Content
		}
		if s.Service != nil {
			services = append(services, s.Service.Name)
		}
	}
	// cf-platform krijgt nooit een service-stap: alleen keepalived start het.
	if strings.Contains(strings.Join(services, ","), "cf-platform,") || strings.HasSuffix(strings.Join(services, ","), "cf-platform") {
		t.Fatalf("services: %v", services)
	}
	peers := "192.168.10.146:9333,192.168.10.147:9333,192.168.10.148:9333"
	for path, want := range map[string][]string{
		"/etc/clusterforge/seaweedfs.yaml": {"image: chrislusf/seaweedfs:3.97", "master -ip=192.168.10.147", "-peers=" + peers,
			"-defaultReplication=001", "-mserver=" + peers, "-port=8081"},
		"/etc/seaweedfs/filer.toml": {"[postgres2]", `hostname = "192.168.10.180"`, "port = 5433", `database = "seaweedfs"`, `password = "Wachtwoord-123"`},
		"/etc/clusterforge/platform.env": {"DATA_ROOT=/srv/platform-data\n", "NODE_NAME=media-02\n",
			"PLATFORM_DATABASE_URL=postgresql+asyncpg://platform:Wachtwoord-123@192.168.10.180:5433/platform\n",
			"PLATFORM_CLUSTER_NODES=media-01=192.168.10.146,media-02=192.168.10.147,media-03=192.168.10.148\n",
			"OFFICE_JWT_SECRET=officegeheim1\n"},
		"/etc/clusterforge/platform-cluster.yaml":     {"PLATFORM_MODE: cluster", `restart: "no"`, "image: cf-platform:local"},
		"/usr/local/lib/clusterforge/platform-src.sh": {"REF='claude/stappenplan-uitwerken-lu4uv3'", "SRC=/opt/cf-platform/src", "-p cf-platform"},
		"/usr/local/lib/clusterforge/platform-ctl.sh": {"docker image inspect -f '{{.Id}}' cf-platform:local", "-f /etc/clusterforge/platform-cluster.yaml",
			"docker compose -p cf-platform", "VIP='192.168.10.150'", "overstap proef|echt", "db-copy \"$url\" --leegmaken"},
		"/etc/systemd/system/cf-platform.service":       {"ConditionPathExists=/srv/platform-data/.cf-platform-active"},
		"/etc/systemd/system/cf-platform-mount.service": {"-dir=/srv/platform-data", "-replication=001"},
		"/etc/keepalived/keepalived.conf":               {"priority 140\n", "nopreempt", "notify_master", "192.168.10.146\n        192.168.10.148\n"},
	} {
		got, ok := files[path]
		if !ok {
			t.Errorf("geen bestand %s", path)
			continue
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s mist %q:\n%s", path, w, got)
			}
		}
	}
	for _, path := range []string{"/usr/local/lib/clusterforge/platform-src.sh", "/usr/local/lib/clusterforge/platform-ctl.sh", "/usr/local/lib/clusterforge/platform-weed.sh"} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(files[path])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", path, err, out)
		}
	}
}
