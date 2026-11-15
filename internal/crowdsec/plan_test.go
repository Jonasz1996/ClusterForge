package crowdsec

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func keys(p Plan) []string {
	var k []string
	for _, pp := range p.Profiles {
		k = append(k, pp.Key+":"+pp.Origin)
	}
	return k
}

func TestBuildStack(t *testing.T) {
	cases := []struct {
		name  string
		s     Signals
		cfg   NodeConfig
		want  []string
		files map[string][]string
		cols  []string
	}{
		{
			name: "NPM-node van ClusterForge (Debian 13, journald)",
			s: Signals{Units: map[string]bool{"ssh": true, "keepalived": true, "docker": true}, Template: "nginx-proxy-manager", Docker: true,
				Containers: []protocol.CrowdSecContainer{{Name: "cf-npm", Image: "jc21/nginx-proxy-manager:latest", Mounts: map[string]string{"/data": "/opt/cf-npm/data"}}}},
			cfg:  NodeConfig{Bouncer: true},
			want: []string{"linux:auto", "nginx-proxy-manager:auto"},
			files: map[string][]string{
				"/etc/crowdsec/acquis.d/cf-linux.yaml":               {"source: journalctl", `"_SYSTEMD_UNIT=ssh.service"`, `type: "syslog"`},
				"/etc/crowdsec/acquis.d/cf-nginx-proxy-manager.yaml": {"source: file", `"/opt/cf-npm/data/logs/*.log"`, `type: "nginx-proxy-manager"`},
				bouncerLocal:  {"- input", "- forward"},
				whitelistFile: {`"192.168.0.0/16"`},
			},
			cols: []string{"crowdsecurity/linux", "crowdsecurity/nginx-proxy-manager"},
		},
		{
			name: "bestaande Authentik in /root (compose-namen), Patroni",
			s: Signals{Units: map[string]bool{"ssh": true, "patroni": true, "docker": true},
				Containers: []protocol.CrowdSecContainer{
					{Name: "root-server-1", Image: "ghcr.io/goauthentik/server:2025.6.2"},
					{Name: "root-worker-1", Image: "ghcr.io/goauthentik/server:2025.6.2"},
					{Name: "root-redis-1", Image: "docker.io/library/redis:alpine"},
				}},
			want: []string{"linux:auto", "postgresql:auto", "authentik:auto"},
			files: map[string][]string{
				"/etc/crowdsec/acquis.d/cf-authentik.yaml": {"source: docker", `"root-server-1"`, `"root-worker-1"`, `type: "authentik"`},
			},
			cols: []string{"crowdsecurity/linux", "crowdsecurity/pgsql", "firix/authentik"},
		},
		{
			name: "Galera + Zabbix-web op Apache, auth.log aanwezig, nginx uitgezet",
			s: Signals{Units: map[string]bool{"ssh": true, "mariadb": true, "apache2": true, "nginx": true, "cf-zabbix-server": true},
				Probes: map[string][]string{authLog: {authLog}}, Template: "zabbix"},
			cfg:  NodeConfig{Disabled: []string{"nginx", "linux"}, Extra: []string{"grafana"}},
			want: []string{"linux:auto", "nginx:off", "apache2:auto", "mariadb:auto", "grafana:extra"},
			files: map[string][]string{
				"/etc/crowdsec/acquis.d/cf-linux.yaml":   {`"/var/log/auth.log"`},
				"/etc/crowdsec/acquis.d/cf-apache2.yaml": {`"/var/log/apache2/*.log"`},
				"/etc/crowdsec/acquis.d/cf-mariadb.yaml": {`"_SYSTEMD_UNIT=mariadb.service"`},
			},
			cols: []string{"LePresidente/grafana", "crowdsecurity/apache2", "crowdsecurity/linux", "crowdsecurity/mariadb"},
		},
		{
			name: "Home Assistant-cluster zonder container op deze node, Nextcloud",
			s: Signals{Units: map[string]bool{"ssh": true}, Template: "home-assistant",
				Probes: map[string][]string{nextcloudA: {"/var/www/nextcloud/data/nextcloud.log"}, nextcloudB: {"/var/www/nextcloud/data/nextcloud.log"}}},
			want: []string{"linux:auto", "home-assistant:auto", "nextcloud:auto"},
			files: map[string][]string{
				"/etc/crowdsec/acquis.d/cf-home-assistant.yaml": {`"/opt/cf-homeassistant/config/home-assistant.log"`},
				"/etc/crowdsec/acquis.d/cf-nextcloud.yaml":      {`"/var/www/nextcloud/data/nextcloud.log"`, `type: "Nextcloud"`},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Build(c.s, c.cfg, DefaultWhitelist)
			if got := keys(p); !slices.Equal(got, c.want) {
				t.Errorf("profielen %v, wil %v", got, c.want)
			}
			if c.cols != nil && !slices.Equal(p.Collections, c.cols) {
				t.Errorf("collections %v, wil %v", p.Collections, c.cols)
			}
			for f, subs := range c.files {
				body, ok := p.Files[f]
				if !ok {
					t.Errorf("%s ontbreekt (%v)", f, p.Files)
					continue
				}
				for _, s := range subs {
					if !strings.Contains(body, s) {
						t.Errorf("%s mist %q:\n%s", f, s, body)
					}
				}
			}
			if !c.cfg.Bouncer {
				if _, ok := p.Files[bouncerLocal]; ok {
					t.Error("bouncerconfig zonder bouncer")
				}
			}
			// Elk bestand is geldige YAML; elke bron heeft labels.type.
			for f, body := range p.Files {
				dec := yaml.NewDecoder(strings.NewReader(body))
				for {
					var doc map[string]any
					if err := dec.Decode(&doc); err != nil {
						if err.Error() != "EOF" {
							t.Errorf("%s: %v", f, err)
						}
						break
					}
					if strings.HasPrefix(f, acquisDir) {
						if l, _ := doc["labels"].(map[string]any); l["type"] == "" || l == nil {
							t.Errorf("%s zonder labels.type", f)
						}
					}
				}
			}
			if p2 := Build(c.s, c.cfg, DefaultWhitelist); p2.Hash != p.Hash {
				t.Error("hash niet stabiel")
			}
		})
	}
}

func TestHashChanges(t *testing.T) {
	s := Signals{Units: map[string]bool{"ssh": true}}
	a := Build(s, NodeConfig{}, DefaultWhitelist)
	s.Units["nginx"] = true
	b := Build(s, NodeConfig{}, DefaultWhitelist)
	c := Build(s, NodeConfig{}, []string{"10.0.0.0/8"})
	d := Build(s, NodeConfig{Bouncer: true}, []string{"10.0.0.0/8"})
	if a.Hash == b.Hash || b.Hash == c.Hash || c.Hash == d.Hash {
		t.Errorf("hashes %s %s %s %s", a.Hash, b.Hash, c.Hash, d.Hash)
	}
}

func TestValidWhitelist(t *testing.T) {
	if err := ValidWhitelist([]string{"192.168.90.0/24", "203.0.113.4", "::1/128"}); err != nil {
		t.Error(err)
	}
	if err := ValidWhitelist([]string{"192.168.90.0/24\n  - 0.0.0.0/0"}); err == nil {
		t.Error("ongeldige whitelist aanvaard")
	}
}

// TestScriptRuns draait het script met nep-cscli, -crowdsec en -systemctl
// in een eigen map in plaats van /etc en /var/lib.
func TestScriptRuns(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("geen bash")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	log := filepath.Join(root, "calls.log")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\necho \""+name+" $*\" >> "+log+"\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake("cscli", "exit 0")
	fake("crowdsec", `[ -f "$CFTEST_FAIL" ] && { echo "bad acquis"; exit 1; }; exit 0`)
	fake("crowdsec-firewall-bouncer", "exit 0")
	fake("systemctl", "exit 0")
	fake("journalctl", "exit 0")
	fake("apt-get", "exit 0")
	fake("sleep", "exit 0")

	run := func(p Plan) ([]byte, error) {
		script := Script(p)
		script = strings.ReplaceAll(script, "/etc/crowdsec", root+"/etc/crowdsec")
		script = strings.ReplaceAll(script, "/var/lib/clusterforge", root+"/var/lib/clusterforge")
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CFTEST_FAIL="+root+"/fail")
		return cmd.CombinedOutput()
	}
	s := Signals{Units: map[string]bool{"ssh": true, "nginx": true}, Docker: true}
	p := Build(s, NodeConfig{Bouncer: true}, DefaultWhitelist)
	if out, err := run(p); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/crowdsec/acquis.d/cf-nginx.yaml")); !strings.Contains(string(b), "/var/log/nginx/*.log") {
		t.Errorf("cf-nginx.yaml: %s", b)
	}
	st, _ := os.ReadFile(filepath.Join(root, "var/lib/clusterforge/crowdsec.json"))
	if !strings.Contains(string(st), `"hash":"`+p.Hash+`"`) || !strings.Contains(string(st), `"crowdsecurity/nginx"`) {
		t.Errorf("state: %s", st)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "cscli collections install crowdsecurity/nginx") {
		t.Errorf("aanroepen: %s", calls)
	}

	// nginx weg: de collection die ClusterForge zette, gaat weg, het bestand ook.
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	delete(s.Units, "nginx")
	p2 := Build(s, NodeConfig{Bouncer: true}, DefaultWhitelist)
	if out, err := run(p2); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	calls, _ = os.ReadFile(log)
	if !strings.Contains(string(calls), "cscli collections remove crowdsecurity/nginx") || strings.Contains(string(calls), "remove crowdsecurity/linux") {
		t.Errorf("aanroepen: %s", calls)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/crowdsec/acquis.d/cf-nginx.yaml")); err == nil {
		t.Error("cf-nginx.yaml is blijven staan")
	}

	// Configuratietest faalt: vorige bestanden terug, script faalt.
	if err := os.WriteFile(filepath.Join(root, "fail"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s.Units["apache2"] = true
	p3 := Build(s, NodeConfig{Bouncer: true}, DefaultWhitelist)
	out, err := run(p3)
	if err == nil || !strings.Contains(string(out), "vorige gaat terug") {
		t.Fatalf("script moest falen: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/crowdsec/acquis.d/cf-apache2.yaml")); err == nil {
		t.Error("cf-apache2.yaml bleef staan na de mislukte test")
	}
	if _, err := os.Stat(filepath.Join(root, "etc/crowdsec/acquis.d/cf-linux.yaml")); err != nil {
		t.Error("cf-linux.yaml niet teruggezet")
	}
	if st2, _ := os.ReadFile(filepath.Join(root, "var/lib/clusterforge/crowdsec.json")); !strings.Contains(string(st2), p2.Hash) {
		t.Errorf("state na mislukking: %s", st2)
	}
	if len(Script(p3)) > protocol.MaxRunScript {
		t.Error("script te lang voor een run")
	}
}
