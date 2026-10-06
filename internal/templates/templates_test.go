package templates

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func keepalivedNginx(t *testing.T) *Template {
	t.Helper()
	tpl, ok := Latest("keepalived-nginx")
	if !ok {
		t.Fatal("keepalived-nginx ontbreekt")
	}
	return tpl
}

func TestValidate(t *testing.T) {
	tpl := keepalivedNginx(t)
	p, err := tpl.Validate(map[string]any{"vip": " 10.0.20.100 ", "memory": "1024M", "node_count": float64(3)})
	if err != nil {
		t.Fatal(err)
	}
	if p["vip"] != "10.0.20.100" || p["node_count"] != 3 || p["memory"] != "1G" || p["disk"] != "20G" || p["cpu"] != 2 {
		t.Fatalf("genormaliseerd: %v", p)
	}
	if p["vrid"] != nil {
		t.Fatalf("vrid zou leeg blijven: %v", p["vrid"])
	}
	if s, _ := p["auth_pass"].(string); len(s) != 8 {
		t.Fatalf("auth_pass niet gegenereerd: %q", p["auth_pass"])
	}
	if got := tpl.Secrets(); len(got) != 1 || got[0] != "auth_pass" {
		t.Fatalf("secrets: %v", got)
	}

	for name, tc := range map[string]struct {
		in    map[string]any
		field string
	}{
		"vip ontbreekt": {map[string]any{}, "vip"},
		"geen ip":       {map[string]any{"vip": "web"}, "vip"},
		"te weinig":     {map[string]any{"vip": "10.0.20.100", "node_count": 1}, "node_count"},
		"kommagetal":    {map[string]any{"vip": "10.0.20.100", "node_count": 2.5}, "node_count"},
		"te klein":      {map[string]any{"vip": "10.0.20.100", "memory": "256M"}, "memory"},
		"geen grootte":  {map[string]any{"vip": "10.0.20.100", "disk": "veel"}, "disk"},
		"vrid":          {map[string]any{"vip": "10.0.20.100", "vrid": 300}, "vrid"},
		"onbekend":      {map[string]any{"vip": "10.0.20.100", "kleur": "rood"}, "kleur"},
		"secret":        {map[string]any{"vip": "10.0.20.100", "auth_pass": "a b c d e"}, "auth_pass"},
		"secret lang":   {map[string]any{"vip": "10.0.20.100", "auth_pass": "123456789"}, "auth_pass"},
	} {
		_, err := tpl.Validate(tc.in)
		var fe FieldError
		if !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRenderKeepalivedNginx(t *testing.T) {
	tpl := keepalivedNginx(t)
	params, err := tpl.Validate(map[string]any{"vip": "10.0.20.100", "vrid": 42, "auth_pass": "geheim12"})
	if err != nil {
		t.Fatal(err)
	}
	c := Context{
		Params:  params,
		Cluster: ClusterInfo{Name: "Web", Slug: "web", Environment: "prod"},
		Nodes: []NodeInfo{
			{Hostname: "web-01", Role: "web", Index: 1, Address: "10.0.20.11", Prefix: 24, Interface: "eth0"},
			{Hostname: "web-02", Role: "web", Index: 2, Address: "10.0.20.12", Prefix: 24, Interface: "ens18"},
		},
	}
	counts, err := tpl.Counts(c)
	if err != nil || len(counts) != 1 || counts[0] != 2 {
		t.Fatalf("counts: %v %v", counts, err)
	}
	vm, err := tpl.VM("web", c)
	if err != nil || vm != (VMShape{CPU: 2, MemoryMiB: 2048, DiskGiB: 20}) {
		t.Fatalf("vm: %+v %v", vm, err)
	}

	c.Node = &c.Nodes[1]
	steps, err := tpl.Steps("web", c)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 || steps[0].Package == nil || steps[3].File == nil {
		t.Fatalf("stappen: %+v", steps)
	}
	conf := steps[3].File.Content
	for _, want := range []string{
		"router_id web-02", "interface ens18", "virtual_router_id 42", "priority 140",
		"unicast_src_ip 10.0.20.12", "unicast_peer {\n        10.0.20.11\n    }",
		"auth_pass geheim12", "10.0.20.100/24 dev ens18",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("keepalived.conf mist %q:\n%s", want, conf)
		}
	}
	if steps[3].File.Mode != "0640" || len(steps[3].Notify) != 1 || steps[3].Notify[0].State != "reloaded" {
		t.Fatalf("notify: %+v", steps[3])
	}
	if steps[3].Title != "bestand /etc/keepalived/keepalived.conf" || !strings.Contains(steps[1].File.Content, "Dit is web-02") {
		t.Fatalf("titel of index: %+v", steps[1])
	}

	params["vrid"] = nil
	if _, err := tpl.Steps("web", c); err == nil || !strings.Contains(err.Error(), "leeg") {
		t.Fatalf("lege vrid: %v", err)
	}
	params["vrid"] = 42

	c.Node = nil
	checks, err := tpl.RenderChecks(c)
	if err != nil || len(checks) != 2 || checks[0].VIPOwned.VIP != "10.0.20.100" || checks[1].HTTP.URL != "http://10.0.20.100/" {
		t.Fatalf("checks: %+v %v", checks, err)
	}
}

func TestParseErrors(t *testing.T) {
	base := `name: test
version: 1.0.0
title: Test
cluster_type: generic
params:
  - { name: n, type: int, label: N, default: 1 }
roles:
  - name: app
    count: "{{ .params.n }}"
    vm: { cpu: "1", memory: 1G, disk: 8G }
    steps:
`
	for name, tc := range map[string]struct {
		yaml  string
		files map[string]string
		err   string
	}{
		"onbekend veld":    {base + "      - package: { names: [x], versie: 2 }\n", nil, "versie"},
		"twee soorten":     {base + "      - { package: { names: [x] }, service: { name: x } }\n", nil, "precies één"},
		"sjabloon weg":     {base + "      - file: { path: /x, template: x.tmpl }\n", nil, "bestaat niet"},
		"onbekende waarde": {base + "      - file: { path: /x, content: \"{{ .params.m }}\" }\n", nil, "map has no entry"},
		"sjabloonfout":     {base + "      - file: { path: /x, template: x.tmpl }\n", map[string]string{"files/x.tmpl": "{{ .node.ip }}"}, "map has no entry"},
		"notify":           {base + "      - file: { path: /x, content: x, notify: [nginx] }\n", nil, "notify"},
		"parametertype":    {strings.Replace(base, "type: int", "type: getal", 1) + "      - package: { names: [x] }\n", nil, "onbekend type"},
		"default te groot": {strings.Replace(base, "default: 1", "default: 1, max: 0", 1) + "      - package: { names: [x] }\n", nil, ""},
		"yaml-veld":        {base + "      - package: { names: [x] }\nkleur: rood\n", nil, "kleur"},
		"depends_on weg":   {base + "      - package: { names: [x] }\nservices:\n  - { name: a, kind: web, depends_on: [b] }\n", nil, "staat niet in de template"},
		"op zichzelf":      {base + "      - package: { names: [x] }\nservices:\n  - { name: a, kind: web, depends_on: [a] }\n", nil, "van zichzelf"},
		"dubbele dienst":   {base + "      - package: { names: [x] }\nservices:\n  - { name: a, kind: web }\n  - { name: a, kind: vip }\n", nil, "twee keer"},
		"sterkte":          {base + "      - package: { names: [x] }\nservices:\n  - { name: a, kind: web, depends_on: [b], strength: sterk }\n  - { name: b, kind: vip }\n", nil, "hard of soft"},
		"zonder soort":     {base + "      - package: { names: [x] }\nservices:\n  - { name: a }\n", nil, "kind"},
		"poort te groot":   {base + "      - package: { names: [x] }\nservices:\n  - { name: a, kind: web, port: 70000 }\n", nil, "port"},
		"poort onbekend":   {base + "      - package: { names: [x] }\nservices:\n  - { name: a, kind: web, port: \"{{ .params.p }}\" }\n", nil, "port"},
		"poort als tekst":  {strings.Replace(base, "type: int, label: N, default: 1", "type: string, label: N, default: \"1\"", 1) + "      - package: { names: [x] }\nservices:\n  - { name: a, kind: web, port: \"{{ .params.n }}\" }\n", nil, "port"},
		"poort met tekst":  {base + "      - package: { names: [x] }\nservices:\n  - { name: a, kind: web, port: \"80{{ .params.n }}\" }\n", nil, "port"},
	} {
		fsys := fstest.MapFS{"template.yaml": {Data: []byte(tc.yaml)}}
		for p, c := range tc.files {
			fsys[p] = &fstest.MapFile{Data: []byte(c)}
		}
		_, err := Parse(fsys)
		if tc.err == "" {
			// Een default buiten min/max valt pas bij het uitrollen op.
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSize(t *testing.T) {
	for in, want := range map[string]int64{"512M": 512 << 20, "2G": 2 << 30, "2GiB": 2 << 30, "1t": 1 << 40, "64 KB": 64 << 10} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "2", "1.5G", "-1G", "2X"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("%q aanvaard", bad)
		}
	}
	if FormatSize(1536<<20) != "1536M" || FormatSize(2<<30) != "2G" {
		t.Fatal(FormatSize(1536<<20), FormatSize(2<<30))
	}
}

// render rendert de stappen van de eerste rol voor de eerste van twee nodes.
func render(t *testing.T, tpl *Template, in map[string]any) ([]Step, Context) {
	t.Helper()
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	role := tpl.Roles[0].Name
	c := Context{
		Params:  params,
		Cluster: ClusterInfo{Name: "Taken", Slug: "taken", Environment: "prod"},
		Nodes: []NodeInfo{
			{Hostname: "taken-01", Role: role, Index: 1, Address: "10.0.30.11", Prefix: 24, Interface: "eth0"},
			{Hostname: "taken-02", Role: role, Index: 2, Address: "10.0.30.12", Prefix: 24, Interface: "eth0"},
		},
	}
	c.Node = &c.Nodes[0]
	steps, err := tpl.Steps(role, c)
	if err != nil {
		t.Fatal(err)
	}
	return steps, c
}

func titles(steps []Step) string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Title)
	}
	return strings.Join(out, "; ")
}

func TestBuiltinList(t *testing.T) {
	var names []string
	for _, tpl := range Builtin() {
		names = append(names, tpl.Name+" "+tpl.Version)
	}
	if got := strings.Join(names, ", "); got != "cron 1.0.0, docker 1.0.0, generic 1.0.0, keepalived-nginx 1.1.0" {
		t.Fatalf("ingebouwde templates: %s", got)
	}
}

func TestRenderDocker(t *testing.T) {
	tpl, _ := Latest("docker")
	steps, c := render(t, tpl, map[string]any{"log_max_size": "100M", "node_count": 1})
	if got := titles(steps); got != "pakketten: docker.io, docker-compose; bestand /etc/docker/daemon.json; service docker; map /opt/stacks" {
		t.Fatalf("stappen: %s", got)
	}
	var daemon struct {
		LogDriver   string            `json:"log-driver"`
		LogOpts     map[string]string `json:"log-opts"`
		LiveRestore bool              `json:"live-restore"`
	}
	if err := json.Unmarshal([]byte(steps[1].File.Content), &daemon); err != nil {
		t.Fatalf("daemon.json: %v\n%s", err, steps[1].File.Content)
	}
	if daemon.LogDriver != "json-file" || daemon.LogOpts["max-size"] != "100M" || daemon.LogOpts["max-file"] != "3" || !daemon.LiveRestore {
		t.Fatalf("daemon.json: %+v", daemon)
	}
	if len(steps[1].Notify) != 1 || steps[1].Notify[0] != (protocol.ServiceStep{Name: "docker", State: "restarted"}) {
		t.Fatalf("notify: %+v", steps[1].Notify)
	}
	if counts, _ := tpl.Counts(c); counts[0] != 1 || tpl.Failback {
		t.Fatalf("één node zonder VIP: %v %v", counts, tpl.Failback)
	}
	svcs, err := tpl.RenderServices(c.Params)
	if err != nil || len(svcs) != 1 || !reflect.DeepEqual(svcs[0], Service{Name: "docker", Kind: "container", Unit: "docker"}) {
		t.Fatalf("diensten: %+v %v", svcs, err)
	}
}

func TestRenderCron(t *testing.T) {
	tpl, _ := Latest("cron")
	steps, c := render(t, tpl, map[string]any{"vip": "10.0.30.100", "vrid": 61, "auth_pass": "geheim12"})
	if got := titles(steps); got != "pakketten: cron, keepalived; bestand /usr/local/bin/cf-leader; service cron; bestand /etc/keepalived/keepalived.conf; service keepalived" {
		t.Fatalf("stappen: %s", got)
	}
	leader := steps[1].File
	if leader.Mode != "0755" || !strings.HasPrefix(leader.Content, "#!/bin/sh\n") ||
		!strings.Contains(leader.Content, `grep -qF " inet 10.0.30.100/"`) || !strings.Contains(leader.Content, `exec "$@"`) {
		t.Fatalf("cf-leader:\n%+v", leader)
	}
	conf := steps[3].File.Content
	for _, want := range []string{"chk_cron", "is-active --quiet cron", "virtual_router_id 61", "priority 150", "unicast_peer {\n        10.0.30.12\n    }", "10.0.30.100/24 dev eth0"} {
		if !strings.Contains(conf, want) {
			t.Errorf("keepalived.conf mist %q:\n%s", want, conf)
		}
	}
	c.Node = nil
	checks, err := tpl.RenderChecks(c)
	if err != nil || len(checks) != 1 || checks[0].VIPOwned == nil || checks[0].VIPOwned.VIP != "10.0.30.100" || !tpl.Failback {
		t.Fatalf("checks: %+v %v", checks, err)
	}
	svcs, _ := tpl.RenderServices(c.Params)
	if len(svcs) != 2 || svcs[0].Unit != "cron" || svcs[0].Kind != "cron" || svcs[0].DependsOn[0] != "keepalived" || svcs[0].Strength != "hard" {
		t.Fatalf("diensten: %+v", svcs)
	}
}

func TestRenderGeneric(t *testing.T) {
	tpl, _ := Latest("generic")
	steps, c := render(t, tpl, map[string]any{
		"vip": "10.0.30.100", "vrid": 62, "auth_pass": "geheim12", "image": "ghcr.io/jonas/app:1.4.2", "port": 8080, "container_port": 3000,
		"health_path": "/health",
	})
	if got := titles(steps); got != "pakketten: docker.io, keepalived; bestand /etc/docker/daemon.json; service docker; map /etc/cf-app; "+
		"bestand /etc/systemd/system/cf-app.service; service cf-app; bestand /etc/keepalived/keepalived.conf; service keepalived" {
		t.Fatalf("stappen: %s", got)
	}
	unit := steps[4]
	for _, want := range []string{
		"Requires=docker.service",
		"ExecStartPre=-/usr/bin/docker pull ghcr.io/jonas/app:1.4.2\n",
		"ExecStart=/usr/bin/docker run --rm --name cf-app --env-file /etc/cf-app/app.env -p 8080:3000 ghcr.io/jonas/app:1.4.2\n",
		"Restart=always",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit.File.Content, want) {
			t.Errorf("cf-app.service mist %q:\n%s", want, unit.File.Content)
		}
	}
	if len(unit.Notify) != 1 || unit.Notify[0] != (protocol.ServiceStep{Name: "cf-app", State: "restarted"}) {
		t.Fatalf("notify: %+v", unit.Notify)
	}
	if conf := steps[6].File.Content; !strings.Contains(conf, "is-active --quiet cf-app") || !strings.Contains(conf, "chk_app\n    }") {
		t.Fatalf("keepalived.conf:\n%s", conf)
	}
	c.Node = nil
	checks, err := tpl.RenderChecks(c)
	if err != nil || len(checks) != 2 || checks[1].HTTP.URL != "http://10.0.30.100:8080/health" || checks[1].Within() != 2*time.Minute {
		t.Fatalf("checks: %+v %v", checks, err)
	}
	svcs, err := tpl.RenderServices(c.Params)
	if err != nil || len(svcs) != 3 || svcs[0].Name != "app" || svcs[0].Unit != "cf-app" || svcs[0].Port != 8080 ||
		strings.Join(svcs[0].DependsOn, ",") != "docker,keepalived" {
		t.Fatalf("diensten: %+v %v", svcs, err)
	}
	if tpl.Services[0].PortParam() != "port" || tpl.Services[0].FixedPort() != 0 {
		t.Fatalf("poort van app: %+v", tpl.Services[0])
	}

	// Standaard: nginx:stable op poort 80.
	steps, _ = render(t, tpl, map[string]any{"vip": "10.0.30.100", "vrid": 62})
	if !strings.Contains(steps[4].File.Content, "-p 80:80 nginx:stable\n") {
		t.Fatalf("standaard:\n%s", steps[4].File.Content)
	}

	// Een image of pad dat iets anders in de unit of de controle zet, komt
	// niet door de validatie.
	for _, bad := range []map[string]any{
		{"image": "nginx:stable --privileged"},
		{"image": "nginx:stable\nExecStartPre=/bin/sh -c id"},
		{"image": "nginx:$(id)"},
		{"image": "nginx:`id`"},
		{"image": "nginx;id"},
		{"image": "Nginx"},
		{"image": "-v/:/host nginx"},
		{"health_path": "/ok?x=1"},
		{"health_path": "ok"},
		{"health_path": "/a b"},
	} {
		bad["vip"] = "10.0.30.100"
		var fe FieldError
		if _, err := tpl.Validate(bad); !errors.As(err, &fe) || (fe.Field != "image" && fe.Field != "health_path") {
			t.Errorf("%v aanvaard: %v", bad, err)
		}
	}
	for _, good := range []string{"nginx", "registry.local:5000/team/app:v2", "ghcr.io/jonas/app@sha256:" + strings.Repeat("ab", 32)} {
		if _, err := tpl.Validate(map[string]any{"vip": "10.0.30.100", "image": good}); err != nil {
			t.Errorf("%s geweigerd: %v", good, err)
		}
	}
}
