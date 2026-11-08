package recipes

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// De stappen van een recept hebben de vorm van template.yaml: een template
// met die stappen leest en rendert ze zonder fout, met de geheimen ingevuld.
func TestStepsFitTemplates(t *testing.T) {
	r := mustParse(t, `RECIPE app 1.0
FROM debian:13
SECRET pw
REPO_DEB https://repo.example.org/app-release_1.0_all.deb
REPO pgdg https://apt.postgresql.org/pub/repos/apt trixie-pgdg main KEY https://www.postgresql.org/media/keys/ACCC4CF8.asc
APT app
USER app SYSTEM
DIR /var/lib/app MODE 0750 OWNER app
FILE /etc/app/app.env <<EOF
NODE={{ node.name }}
PW={{ secret.pw }}
EOF
SET /etc/app/app.conf password={{ secret.pw }} GROUP app
RUN CREATES /var/lib/app/.init /usr/bin/app init
SERVICE app ENABLE START
`)
	var steps []string
	for _, s := range r.Roles[0].Steps {
		b, err := json.Marshal(s.Spec)
		if err != nil {
			t.Fatal(err)
		}
		steps = append(steps, "      - "+string(b))
	}
	yaml := "name: app\nversion: 1.0.0\ntitle: App\ncluster_type: generic\nparams:\n  - { name: pw, type: secret, label: PW, length: 16 }\nroles:\n  - name: app\n    count: \"1\"\n    vm: { cpu: \"1\", memory: 1G, disk: 10G }\n    steps:\n" +
		strings.Join(steps, "\n") + "\n"
	tpl, err := templates.Parse(fstest.MapFS{"template.yaml": {Data: []byte(yaml)}})
	if err != nil {
		t.Fatalf("%v\n%s", err, yaml)
	}
	params, err := tpl.Validate(map[string]any{"pw": "abcdefgh12345678"})
	if err != nil {
		t.Fatal(err)
	}
	c := templates.Context{Params: params, Cluster: templates.ClusterInfo{Name: "App", Slug: "app"},
		Nodes: []templates.NodeInfo{{Hostname: "app-01", Role: "app", Index: 1, Address: "10.0.0.11"}}}
	c.Node = &c.Nodes[0]
	out, err := tpl.Steps("app", c)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, s := range out {
		kinds = append(kinds, s.Kind())
	}
	if got := strings.Join(kinds, " "); got != "repo_deb repo package user directory file ini_set command service" {
		t.Fatalf("soorten: %s", got)
	}
	if f := out[5].File; f.Content != "NODE=app-01\nPW=abcdefgh12345678\n" || f.Mode != "0600" {
		t.Errorf("bestand: %+v", f)
	}
	if is := out[6].IniSet; is.Value != "abcdefgh12345678" || is.Mode != "0640" || is.Group != "app" || out[6].Title != "password in /etc/app/app.conf" {
		t.Errorf("ini_set: %+v %s", is, out[6].Title)
	}
	for _, i := range []int{5, 6, 7} {
		if len(out[i].Notify) != 1 || out[i].Notify[0].Name != "app" || out[i].Notify[0].State != "restarted" {
			t.Errorf("stap %d notify: %+v", i, out[i].Notify)
		}
	}
	if out[0].Title != "pakketbron uit app-release_1.0_all.deb" {
		t.Errorf("titel: %s", out[0].Title)
	}
}
