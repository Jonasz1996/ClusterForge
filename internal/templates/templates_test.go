package templates

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func keepalivedNginx(t *testing.T) *Template {
	t.Helper()
	tpl, ok := Get("keepalived-nginx")
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
