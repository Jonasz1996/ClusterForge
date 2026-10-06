package templates

import (
	"strings"
	"testing"
	"testing/fstest"
)

// released zijn de uitgebrachte versies van de ingebouwde templates. Een
// cluster rendert altijd met de versie waarmee het is uitgerold, dus een
// versie die hier staat, mag nooit uit builtin/ verdwijnen. Voeg een nieuwe
// versie toe zodra ze uitgebracht is.
var released = map[string][]string{
	"keepalived-nginx": {"1.0.0", "1.1.0"},
}

func TestReleasedVersionsStay(t *testing.T) {
	for name, versions := range released {
		for _, v := range versions {
			if _, ok := Get(name, v); !ok {
				t.Errorf("%s %s is uitgebracht en kan in gebruik zijn, maar zit niet meer in builtin/%s/%s", name, v, name, v)
			}
		}
	}
	for _, tpl := range Builtin() {
		for _, v := range BuiltinRegistry().Versions(tpl.Name) {
			found := false
			for _, r := range released[tpl.Name] {
				found = found || r == v
			}
			if !found {
				t.Errorf("%s %s staat niet in released; zet ze erbij als ze uitgebracht wordt", tpl.Name, v)
			}
		}
	}
}

func versionYAML(version, port string) string {
	return `name: web
version: ` + version + `
title: Web
cluster_type: generic
params:
  - { name: n, type: int, label: N, default: 1 }
roles:
  - name: app
    count: "{{ .params.n }}"
    vm: { cpu: "1", memory: 1G, disk: 8G }
    steps:
      - file: { path: /etc/web.conf, content: "poort ` + port + `" }
`
}

func TestRegistryVersions(t *testing.T) {
	r, err := Load(fstest.MapFS{
		"web/1.0.0/template.yaml":  {Data: []byte(versionYAML("1.0.0", "80"))},
		"web/1.10.0/template.yaml": {Data: []byte(versionYAML("1.10.0", "8080"))},
		"web/1.2.0/template.yaml":  {Data: []byte(versionYAML("1.2.0", "81"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if latest, _ := r.Latest("web"); latest.Version != "1.10.0" {
		t.Fatalf("nieuwste: %s", latest.Version)
	}
	if got := strings.Join(r.Versions("web"), " "); got != "1.0.0 1.2.0 1.10.0" {
		t.Fatalf("versies: %s", got)
	}
	// Een cluster op 1.0.0 rendert met 1.0.0, ook nu 1.10.0 er is.
	old, ok := r.Get("web", "1.0.0")
	if !ok {
		t.Fatal("1.0.0 ontbreekt")
	}
	c := Context{Params: map[string]any{"n": 1}, Nodes: []NodeInfo{{Hostname: "web-01", Role: "app", Index: 1}}}
	c.Node = &c.Nodes[0]
	steps, err := old.Steps("app", c)
	if err != nil || steps[0].File.Content != "poort 80" {
		t.Fatalf("1.0.0 rendert %+v %v", steps, err)
	}
	if _, ok := r.Get("web", "1.1.0"); ok {
		t.Fatal("1.1.0 bestaat niet")
	}
	if len(r.All()) != 1 || r.All()[0].Version != "1.10.0" {
		t.Fatalf("All: %v", r.All())
	}

	// De map moet bij template.yaml passen.
	for name, fsys := range map[string]fstest.MapFS{
		"versie":     {"web/1.0.1/template.yaml": {Data: []byte(versionYAML("1.0.0", "80"))}},
		"naam":       {"site/1.0.0/template.yaml": {Data: []byte(versionYAML("1.0.0", "80"))}},
		"zonder map": {"web/template.yaml": {Data: []byte(versionYAML("1.0.0", "80"))}},
	} {
		if _, err := Load(fsys); err == nil {
			t.Errorf("%s: geen fout", name)
		}
	}
}

func TestValuesNeedStoredSecrets(t *testing.T) {
	tpl := keepalivedNginx(t)
	params := map[string]any{"vip": "10.0.20.100", "node_count": 2, "vrid": 51, "cpu": 2, "memory": "2G", "disk": "20G"}
	if _, err := tpl.Values(params, nil); err == nil || !strings.Contains(err.Error(), "geheim auth_pass ontbreekt") {
		t.Fatalf("zonder geheim: %v", err)
	}
	v, err := tpl.Values(params, map[string]string{"auth_pass": "geheim12"})
	if err != nil || v["auth_pass"] != "geheim12" || v["vrid"] != 51 {
		t.Fatalf("met geheim: %v %v", v, err)
	}
	// Een nieuwe uitrol maakt het geheim wel.
	if v, err := tpl.Validate(params); err != nil || len(v["auth_pass"].(string)) != 8 {
		t.Fatalf("nieuwe uitrol: %v %v", v, err)
	}
}

func TestUnsafeValuesInCommandsAndPaths(t *testing.T) {
	base := `name: test
version: 1.0.0
title: Test
cluster_type: generic
params:
  - { name: n, type: int, label: N, default: 1 }
  - { name: vip, type: ipv4, label: VIP, default: 10.0.0.1 }
  - { name: naam, type: string, label: Naam, default: x }
  - { name: site, type: string, label: Site, default: x, pattern: "[a-z0-9-]{1,20}" }
  - { name: vrij, type: string, label: Vrij, default: x, pattern: ".+" }
  - { name: pad, type: string, label: Pad, default: x, pattern: "[a-z/]+" }
  - { name: pass, type: secret, label: Wachtwoord }
roles:
  - name: app
    count: "{{ .params.n }}"
    vm: { cpu: "1", memory: 1G, disk: 8G }
    steps:
`
	ok := []string{
		`- command: { run: "ip addr add {{ .params.vip }} dev eth0", unless: "ip addr | grep -q {{ .params.vip }}" }`,
		`- command: { run: "echo {{ .params.site }} {{ .cluster.slug }} {{ .node.hostname }}", creates: "/var/lib/{{ .params.site }}" }`,
		`- command: { run: "echo {{ range .peers }}{{ .address }} {{ end }}" }`,
		`- file: { path: "/etc/{{ .params.site }}.conf", content: "{{ .params.naam }} {{ .params.pass }}" }`,
		`- directory: { path: "/srv/{{ .node.index }}" }`,
		`- command: { run: "echo {{ range $p := .peers }}{{ $p.address }}{{ end }} {{ $.params.n }}" }`,
	}
	bad := map[string]string{
		`- command: { run: "echo {{ .params.naam }}" }`:                          "parameter naam",
		`- command: { run: "echo {{ .params.vrij }}" }`:                          "parameter vrij",
		`- command: { run: "echo {{ .params.pass }}" }`:                          "parameter pass",
		`- command: { run: "echo ok", unless: "test {{ .params.naam }}" }`:       "command.unless",
		`- command: { run: "echo ok", creates: "/x/{{ .params.naam }}" }`:        "command.creates",
		`- file: { path: "/etc/{{ .params.pad }}", content: x }`:                 "parameter pad",
		`- directory: { path: "/srv/{{ .cluster.name }}" }`:                      "slug en environment",
		`- command: { run: "echo {{ .node.interface }}" }`:                       "van een node",
		`- command: { run: "echo {{ range .peers }}{{ .interface }}{{ end }}" }`: ".interface mag hier niet",
		`- command: { run: "echo {{ index .params \"naam\" }}" }`:                "bij naam",
		`- command: { run: "echo {{ $.params.naam }}" }`:                         "parameter naam",
		`- command: { run: "echo {{ range .peers }}{{ . }}{{ end }}" }`:          "mag hier niet",
	}
	for _, step := range ok {
		if _, err := Parse(fstest.MapFS{"template.yaml": {Data: []byte(base + "      " + step + "\n")}}); err != nil {
			t.Errorf("%s: %v", step, err)
		}
	}
	for step, want := range bad {
		_, err := Parse(fstest.MapFS{"template.yaml": {Data: []byte(base + "      " + step + "\n")}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, wil %q", step, err, want)
		}
	}
}
