package templates

import (
	"fmt"
	"strings"
	"testing"
)

// kubeContext rendert de Kubernetes-template voor het aantal servers en
// workers in params.
func kubeContext(t *testing.T, in map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("kubernetes")
	if !ok {
		t.Fatal("geen template kubernetes")
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Leren", Slug: "leren", Environment: "lab"}}
	counts, err := tpl.Counts(c)
	if err != nil {
		t.Fatal(err)
	}
	addr := 11
	for i, r := range tpl.Roles {
		for n := 1; n <= counts[i]; n++ {
			c.Nodes = append(c.Nodes, NodeInfo{
				Hostname: fmt.Sprintf("leren-%s-%02d", r.Name, n), Role: r.Name, Index: n,
				Address: fmt.Sprintf("10.0.40.%d", addr), Prefix: 24, Interface: "ens18",
			})
			addr++
		}
	}
	return tpl, c
}

// kubeFiles rendert één node en geeft de inhoud per pad en de stappen.
func kubeFiles(t *testing.T, tpl *Template, c Context, hostname string) (map[string]string, []Step) {
	t.Helper()
	for i := range c.Nodes {
		if c.Nodes[i].Hostname == hostname {
			c.Node = &c.Nodes[i]
		}
	}
	if c.Node == nil {
		t.Fatalf("geen node %s", hostname)
	}
	steps, err := tpl.Steps(c.Node.Role, c)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, s := range steps {
		if s.File != nil {
			files[s.File.Path] = s.File.Content
		}
	}
	return files, steps
}

func TestRenderKubernetes(t *testing.T) {
	tpl, c := kubeContext(t, map[string]any{"vip": "10.0.40.100", "servers": 3, "workers": 2, "vrid": 61})
	if len(c.Nodes) != 5 || c.Nodes[0].Hostname != "leren-server-01" || c.Nodes[3].Hostname != "leren-worker-01" {
		t.Fatalf("nodes: %+v", c.Nodes)
	}
	token, _ := c.Params["token"].(string)
	if len(token) != 48 {
		t.Fatalf("token: %q", token)
	}

	// server-01 begint het cluster.
	files, steps := kubeFiles(t, tpl, c, "leren-server-01")
	cfg := files["/etc/rancher/k3s/config.yaml"]
	for _, want := range []string{"token: \"" + token + "\"", "node-ip: 10.0.40.11", "flannel-iface: ens18", "  - 10.0.40.100", "cluster-init: true", "etcd-snapshot-retention: 5"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config van server-01 mist %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "server: https://") {
		t.Errorf("server-01 meldt zich ergens aan:\n%s", cfg)
	}
	if got := titles(steps); got != "pakketten: curl, ca-certificates; map /usr/local/lib/clusterforge; bestand /usr/local/lib/clusterforge/install-k3s.sh; commando; "+
		"map /etc/rancher/k3s; bestand /etc/rancher/k3s/config.yaml; bestand /etc/systemd/system/k3s.service; service k3s; commando; "+
		"pakketten: keepalived; bestand /etc/keepalived/check-k3s.sh; bestand /etc/keepalived/keepalived.conf; service keepalived" {
		t.Fatalf("stappen van een server: %s", got)
	}
	for _, s := range steps {
		if s.File != nil && s.File.Path == "/etc/rancher/k3s/config.yaml" && s.File.Mode != "0600" {
			t.Errorf("config.yaml met het token heeft modus %q", s.File.Mode)
		}
		// Het token staat alleen in config.yaml, nooit in een commando dat
		// in een staplog of foutmelding kan komen.
		if s.Command != nil && (strings.Contains(s.Command.Run, token) || strings.Contains(s.Command.Unless, token)) {
			t.Errorf("token in een commando: %+v", s.Command)
		}
		if s.File != nil && s.File.Path != "/etc/rancher/k3s/config.yaml" && strings.Contains(s.File.Content, token) {
			t.Errorf("token in %s", s.File.Path)
		}
	}
	install := files["/usr/local/lib/clusterforge/install-k3s.sh"]
	for _, want := range []string{"VERSION='v1.35.9+k3s1'", "/v1.35.9%2Bk3s1'", "sha256sum -c -", "mv -f /usr/local/bin/k3s.new /usr/local/bin/k3s",
		`touch "/usr/local/lib/clusterforge/k3s-$VERSION"`} {
		if !strings.Contains(install, want) {
			t.Errorf("install-k3s.sh mist %q", want)
		}
	}
	// Het merkteken in creates hoort bij de versie in het script; anders
	// installeert elke uitrol opnieuw of nooit.
	for _, s := range steps {
		if s.Command != nil && s.Command.Run == "/usr/local/lib/clusterforge/install-k3s.sh" && s.Command.Creates != "/usr/local/lib/clusterforge/k3s-v1.35.9+k3s1" {
			t.Errorf("creates van de installatie: %q", s.Command.Creates)
		}
	}
	ka := files["/etc/keepalived/keepalived.conf"]
	for _, want := range []string{"virtual_router_id 61", "priority 150", "unicast_src_ip 10.0.40.11", "        10.0.40.12\n        10.0.40.13\n", "10.0.40.100/24 dev ens18", "chk_k3s"} {
		if !strings.Contains(ka, want) {
			t.Errorf("keepalived.conf van server-01 mist %q:\n%s", want, ka)
		}
	}

	// De andere servers melden zich aan via het VIP.
	files, _ = kubeFiles(t, tpl, c, "leren-server-02")
	cfg = files["/etc/rancher/k3s/config.yaml"]
	if !strings.Contains(cfg, "server: https://10.0.40.100:6443") || strings.Contains(cfg, "cluster-init") {
		t.Errorf("config van server-02:\n%s", cfg)
	}
	if ka := files["/etc/keepalived/keepalived.conf"]; !strings.Contains(ka, "priority 140") || strings.Contains(ka, "        10.0.40.12\n") {
		t.Errorf("keepalived.conf van server-02:\n%s", ka)
	}

	// Een worker: alleen k3s agent, geen keepalived.
	files, steps = kubeFiles(t, tpl, c, "leren-worker-02")
	cfg = files["/etc/rancher/k3s/config.yaml"]
	if !strings.Contains(cfg, "server: https://10.0.40.100:6443") || !strings.Contains(cfg, "node-ip: 10.0.40.15") || strings.Contains(cfg, "cluster-init") || strings.Contains(cfg, "tls-san") {
		t.Errorf("config van een worker:\n%s", cfg)
	}
	if got := titles(steps); strings.Contains(got, "keepalived") || !strings.Contains(got, "service k3s-agent") {
		t.Errorf("stappen van een worker: %s", got)
	}
	if !strings.Contains(files["/etc/systemd/system/k3s-agent.service"], "ExecStart=/usr/local/bin/k3s agent") {
		t.Error("unit van de worker")
	}

	checks, err := tpl.RenderChecks(Context{Params: c.Params, Cluster: c.Cluster, Nodes: c.Nodes})
	if err != nil || len(checks) != 3 || checks[0].VIPOwned.VIP != "10.0.40.100" || checks[1].KubeReady == nil ||
		checks[2].HTTP.URL != "http://10.0.40.100/" || checks[2].HTTP.Expect != 404 {
		t.Fatalf("controles: %+v %v", checks, err)
	}
}

// Met één server staat er geen unicast-blok in keepalived, en zonder workers
// is het cluster alleen die server.
func TestRenderKubernetesOneServer(t *testing.T) {
	tpl, c := kubeContext(t, map[string]any{"vip": "10.0.40.100", "workers": 0, "vrid": 61})
	if len(c.Nodes) != 1 || c.Nodes[0].Role != "server" {
		t.Fatalf("nodes: %+v", c.Nodes)
	}
	files, _ := kubeFiles(t, tpl, c, "leren-server-01")
	if ka := files["/etc/keepalived/keepalived.conf"]; strings.Contains(ka, "unicast") {
		t.Errorf("keepalived.conf met één server:\n%s", ka)
	}
	if !strings.Contains(files["/etc/rancher/k3s/config.yaml"], "cluster-init: true") {
		t.Error("één server zonder etcd")
	}
}

func TestKubernetesCounts(t *testing.T) {
	tpl, _ := Latest("kubernetes")
	if _, err := tpl.Validate(map[string]any{"vip": "10.0.40.100", "servers": 2}); err == nil || !strings.Contains(err.Error(), "kies 1, 3 of 5") {
		t.Errorf("twee servers: %v", err)
	}
	if _, err := tpl.Validate(map[string]any{"vip": "10.0.40.100", "workers": 11}); err == nil {
		t.Error("elf workers aanvaard")
	}
	if _, err := tpl.Validate(map[string]any{"servers": 1}); err == nil || !strings.Contains(err.Error(), "VIP is verplicht") {
		t.Errorf("zonder VIP: %v", err)
	}
	// Een te klein aantal uit een sjabloon, zoals een cluster.yaml in Git
	// met workers: -1, valt bij Counts op.
	if _, err := tpl.Counts(Context{Params: map[string]any{"servers": 1, "workers": -1}}); err == nil || !strings.Contains(err.Error(), "0 tot 20") {
		t.Errorf("min bij Counts: %v", err)
	}
	params, err := tpl.Validate(map[string]any{"vip": "10.0.40.100"})
	if err != nil {
		t.Fatal(err)
	}
	counts, err := tpl.Counts(Context{Params: params})
	if err != nil || counts[0] != 1 || counts[1] != 2 {
		t.Fatalf("standaard: %v %v", counts, err)
	}
}
