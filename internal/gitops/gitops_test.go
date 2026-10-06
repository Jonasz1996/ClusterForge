package gitops

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/gitops/ghfake"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

const webYAML = `clusterforge: 1
cluster:
  name: Webcluster
  slug: web
  environment: prod
  tags: [web]
template:
  name: keepalived-nginx
  version: 1.0.0
params:
  vip: 10.0.20.100
  vrid: 51
  node_count: 2
  cpu: 2
  memory: 2G
  disk: 20G
target:
  proxmox: pve-thuis
  image_vmid: 9001
  bridge: vmbr0
  first_ip: 10.0.20.11/24
  gateway: 10.0.20.1
`

// web is het cluster dat bij webYAML hoort.
func web() *Cluster {
	return &Cluster{
		ID: uuid.New(), Slug: "web", Name: "Webcluster", Environment: "prod", Type: "keepalived", Tags: []string{"web"},
		Linked: true, Revision: 1,
		Spec: &deploy.Spec{
			Template: deploy.SpecTemplate{Name: "keepalived-nginx", Version: "1.0.0"},
			Cluster:  templates.ClusterInfo{Name: "Webcluster", Slug: "web", Environment: "prod"},
			Params:   map[string]any{"vip": "10.0.20.100", "vrid": 51, "node_count": 2, "cpu": 2, "memory": "2G", "disk": "20G"},
			Secrets:  []string{"auth_pass"},
			Target:   deploy.Target{Network: "static", FirstIP: "10.0.20.11/24", Gateway: "10.0.20.1", ImageVMID: 9001, Bridge: "vmbr0"},
			Nodes: []deploy.SpecNode{
				{NodeID: uuid.New(), Hostname: "web-01", Role: "web", Index: 1},
				{NodeID: uuid.New(), Hostname: "web-02", Role: "web", Index: 2},
			},
		},
	}
}

func check(t *testing.T, src string, c *Cluster) (*Checked, []FieldError, bool) {
	t.Helper()
	f, errs := Parse([]byte(src))
	if len(errs) > 0 {
		return nil, errs, false
	}
	return Check(f, "web", c, templates.BuiltinRegistry())
}

func errText(errs []FieldError) string {
	var out []string
	for _, e := range errs {
		out = append(out, e.String())
	}
	return strings.Join(out, "\n")
}

func TestParseAndCheck(t *testing.T) {
	ch, errs, _ := check(t, webYAML, web())
	if len(errs) > 0 {
		t.Fatalf("geldig bestand gaf fouten:\n%s", errText(errs))
	}
	if !ch.Same(web()) {
		t.Fatal("bestand gelijk aan het cluster, maar Same zegt nee")
	}
	if ch.Params["node_count"] != 2 || ch.Params["memory"] != "2G" {
		t.Fatalf("genormaliseerd: %v", ch.Params)
	}

	for _, tc := range []struct {
		name, from, to string
		cluster        *Cluster
		want           string
	}{
		{"yaml", "  slug: web\n", "  slug: web: www\n", web(), "regel 4, bestand: geen geldige YAML: hier mag geen tweede dubbele punt staan"},
		{"onbekend veld", "  tags: [web]\n", "  tags: [web]\n  owner: jonas\n", web(), "regel 7, cluster.owner: onbekend veld"},
		{"dubbel", "  slug: web\n", "  slug: web\n  slug: www\n", web(), "regel 5, cluster.slug: staat twee keer in het bestand (ook op regel 4)"},
		{"geen getal", "  node_count: 2\n", "  node_count: twee\n", web(), "regel 13, params.node_count: Aantal nodes: moet een geheel getal zijn"},
		{"lijst als parameter", "  node_count: 2\n", "  node_count: [2]\n", web(), "regel 13, params.node_count: een parameter is één waarde"},
		{"slug", "  slug: web\n", "  slug: www\n", web(), "regel 4, cluster.slug: de slug moet gelijk zijn aan de mapnaam web"},
		{"omgeving", "  environment: prod\n", "  environment: productie\n", web(), "regel 5, cluster.environment: kies lab, test of prod"},
		{"versie", "  version: 1.0.0\n", "  version: 9.9.9\n", web(), "regel 9, template.version: onbekende versie 9.9.9; deze server kent 1.0.0, 1.1.0"},
		{"template", "  name: keepalived-nginx\n", "  name: docker\n", web(), "regel 8, template.name: de template ligt vast na de uitrol; dit cluster gebruikt keepalived-nginx"},
		{"ontbreekt", "  node_count: 2\n", "", web(), "regel 10, params.node_count: node_count ontbreekt; in Git is elke parameter verplicht"},
		{"vrid ontbreekt", "  vrid: 51\n", "", web(), "regel 10, params.vrid: vrid ontbreekt; alleen bij een nieuw cluster mag hij ontbreken"},
		{"vip vast", "  vip: 10.0.20.100\n", "  vip: 10.0.20.101\n", web(), "regel 11, params.vip: VIP ligt vast na de uitrol (nu 10.0.20.100)"},
		{"vrid vast", "  vrid: 51\n", "  vrid: 52\n", web(), "regel 12, params.vrid: VRRP-id ligt vast na de uitrol (nu 51)"},
		{"cpu vast", "  cpu: 2\n", "  cpu: 4\n", web(), "regel 14, params.cpu: vCPU per node ligt vast na de uitrol (nu 2): een bestaande VM groter of kleiner maken gaat niet via Git"},
		{"omlaag", "  node_count: 2\n", "  node_count: 1\n", web(), "params.node_count: Aantal nodes: minstens 2"},
		{"onbekende parameter", "  disk: 20G\n", "  disk: 20G\n  kleur: blauw\n", web(), "regel 17, params.kleur: onbekende parameter voor keepalived-nginx 1.0.0"},
		{"formaat", "clusterforge: 1\n", "clusterforge: 2\n", web(), "regel 1, clusterforge: zet clusterforge: 1 bovenaan"},
		{"nieuw zonder target", "target:\n  proxmox: pve-thuis\n  image_vmid: 9001\n  bridge: vmbr0\n  first_ip: 10.0.20.11/24\n  gateway: 10.0.20.1\n", "", nil,
			"regel 1, target: een nieuw cluster heeft een target nodig"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(webYAML, tc.from, tc.to, 1)
			_, errs, _ := check(t, src, tc.cluster)
			if !strings.Contains(errText(errs), tc.want) {
				t.Fatalf("verwacht %q, kreeg:\n%s", tc.want, errText(errs))
			}
		})
	}
}

func TestCheckSecret(t *testing.T) {
	src := strings.Replace(webYAML, "  vip: 10.0.20.100\n", "  vip: 10.0.20.100\n  auth_pass: geheim12\n", 1)
	_, errs, secret := check(t, src, web())
	if !secret || !strings.Contains(errText(errs), "regel 12, params.auth_pass: zet geen geheimen in Git") {
		t.Fatalf("geheim niet geweigerd: %v %s", secret, errText(errs))
	}
	if strings.Contains(errText(errs), "geheim12") {
		t.Fatal("de fout noemt het geheim")
	}
	// Ook onder een onbekende naam die op een geheim lijkt.
	src = strings.Replace(webYAML, "  vip: 10.0.20.100\n", "  vip: 10.0.20.100\n  db_password: x\n", 1)
	if _, _, secret := check(t, src, web()); !secret {
		t.Fatal("db_password niet als geheim herkend")
	}
}

func TestCheckNewCluster(t *testing.T) {
	src := strings.Replace(webYAML, "  vrid: 51\n", "", 1)
	ch, errs, _ := check(t, src, nil)
	if len(errs) > 0 {
		t.Fatalf("nieuw cluster zonder vrid: %s", errText(errs))
	}
	if v, ok := ch.Params["vrid"]; !ok || v != nil {
		t.Fatalf("vrid: %v %v", v, ok)
	}
	// Een hogere node_count is bij een bestaand cluster geen fout.
	src = strings.Replace(webYAML, "  node_count: 2\n", "  node_count: 3\n", 1)
	if _, errs, _ := check(t, src, web()); len(errs) > 0 {
		t.Fatalf("omhoog schalen: %s", errText(errs))
	}
}

func TestDiff(t *testing.T) {
	a := "een\ntwee\ndrie\nvier\nvijf\nzes\nzeven\nacht\nnegen\ntien\nelf\n"
	b := "een\ntwee\nDRIE\nvier\nvijf\nzes\nzeven\nacht\nnegen\ntien\nELF\ntwaalf\n"
	d, n := Diff(a, b, "oud", "nieuw")
	if n != 3 {
		t.Fatalf("anders: %d", n)
	}
	want := `--- oud
+++ nieuw
@@ -1,6 +1,6 @@
 een
 twee
-drie
+DRIE
 vier
 vijf
 zes
@@ -8,4 +8,5 @@
 acht
 negen
 tien
-elf
+ELF
+twaalf
`
	if d != want {
		t.Fatalf("diff:\n%s", d)
	}
	if d, n := Diff("x\n", "x\n", "a", "b"); d != "" || n != 0 {
		t.Fatal("gelijke bestanden geven een diff")
	}
	if d, _ := Diff("", "a\nb\n", "/dev/null", "b"); !strings.Contains(d, "@@ -0,0 +1,2 @@\n+a\n+b\n") {
		t.Fatalf("nieuw bestand:\n%s", d)
	}
}

func TestExportRoundTrip(t *testing.T) {
	c := web()
	c.Description = "De website: met dubbele punt"
	c.Spec.Target.SSHKeys = "ssh-ed25519 AAAAC3Nza jonas@laptop"
	tpl, _ := templates.Get("keepalived-nginx", "1.0.0")
	data, err := exportYAML(c, tpl, "pve-thuis")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Beheerd door ClusterForge GitOps", "  vip: 10.0.20.100 # ligt vast\n", "  tags: [web]\n",
		"  description: 'De website: met dubbele punt'\n", "  proxmox: pve-thuis\n", "    - ssh-ed25519 AAAAC3Nza jonas@laptop\n"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("export mist %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "auth_pass") {
		t.Fatalf("export bevat een geheim:\n%s", data)
	}
	f, errs := Parse(data)
	if len(errs) > 0 {
		t.Fatalf("export is niet te lezen: %s\n%s", errText(errs), data)
	}
	ch, errs, _ := Check(f, "web", c, templates.BuiltinRegistry())
	if len(errs) > 0 || !ch.Same(c) {
		t.Fatalf("export is niet gelijk aan het cluster: %s", errText(errs))
	}
	if !sameAsExport(data, data, c, tpl, templates.BuiltinRegistry()) {
		t.Fatal("export niet gelijk aan zichzelf")
	}
	// Andere opmaak, zelfde inhoud: gelijk. Een andere waarde: niet.
	other := strings.Replace(string(data), "  tags: [web]\n", "  tags:\n    - web\n", 1)
	if !sameAsExport([]byte(other), data, c, tpl, templates.BuiltinRegistry()) {
		t.Fatal("andere opmaak telt als verschil")
	}
	other = strings.Replace(string(data), "  bridge: vmbr0\n", "  bridge: vmbr1\n", 1)
	if sameAsExport([]byte(other), data, c, tpl, templates.BuiltinRegistry()) {
		t.Fatal("andere bridge telt niet als verschil")
	}
}

func TestGitHubClient(t *testing.T) {
	gh := ghfake.New("jonas", "infra", "tok-123")
	srv := httptest.NewServer(gh)
	defer srv.Close()
	ctx := context.Background()

	if _, err := NewGitHub(Config{APIURL: "http://192.0.2.1", Owner: "jonas", Name: "infra", Branch: "main", Token: "x"}); err == nil {
		t.Fatal("http naar een ander toestel toegestaan")
	}
	bad, err := NewGitHub(Config{APIURL: srv.URL, Owner: "jonas", Name: "infra", Branch: "main", Token: "fout"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := bad.Head(ctx, ""); err == nil || !strings.Contains(err.Error(), "ongeldig of verlopen") {
		t.Fatalf("fout token: %v", err)
	}

	sha := gh.Commit("Eerste versie", "Jonas", map[string]string{"clusters/web/cluster.yaml": webYAML, "README.md": "hoi\n"})
	g, err := NewGitHub(Config{APIURL: srv.URL, Owner: "jonas", Name: "infra", Branch: "main", Token: "tok-123"})
	if err != nil {
		t.Fatal(err)
	}
	head, etag, err := g.Head(ctx, "")
	if err != nil || head != sha || etag == "" {
		t.Fatalf("head: %s %s %v", head, etag, err)
	}
	if _, _, err := g.Head(ctx, etag); !errors.Is(err, ErrNotModified) {
		t.Fatalf("zelfde ETag: %v", err)
	}
	c, err := g.Commit(ctx, sha)
	if err != nil || c.Title() != "Eerste versie" || c.Author != "Jonas" || c.Tree == "" {
		t.Fatalf("commit: %+v %v", c, err)
	}
	tree, err := g.Tree(ctx, c.Tree)
	if err != nil {
		t.Fatal(err)
	}
	files := clusterFiles(tree, "clusters")
	if len(files) != 1 || files[0].Path != "clusters/web/cluster.yaml" {
		t.Fatalf("clusterbestanden: %+v", files)
	}
	b, err := g.Blob(ctx, files[0].SHA)
	if err != nil || string(b) != webYAML {
		t.Fatalf("blob: %v", err)
	}
	next := gh.Commit("Naam", "Jonas", map[string]string{"clusters/web/cluster.yaml": strings.Replace(webYAML, "Webcluster", "Website", 1)})
	if head, _, err := g.Head(ctx, etag); err != nil || head != next {
		t.Fatalf("nieuwe kop: %s %v", head, err)
	}
	for _, r := range gh.Requests() {
		if strings.Contains(r, "tok-123") {
			t.Fatal("token in een URL")
		}
	}
	if !slices.ContainsFunc(gh.Requests(), func(r string) bool { return strings.HasSuffix(r, " 304") }) {
		t.Fatal("geen 304 gezien")
	}
}

func TestNormalizeAPIURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "https://api.github.com",
		"https://api.github.com/":        "https://api.github.com",
		"http://127.0.0.1:8098":          "http://127.0.0.1:8098",
		"https://git.example.org/api/v3": "https://git.example.org/api/v3",
	} {
		if got, err := NormalizeAPIURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"http://api.github.com", "ftp://x", "https://jonas:tok@api.github.com"} {
		if _, err := NormalizeAPIURL(in); err == nil {
			t.Errorf("%q toegestaan", in)
		}
	}
	if got := FileURL("https://api.github.com", "jonas", "infra", "main", "clusters/web/cluster.yaml"); got != "https://github.com/jonas/infra/blob/main/clusters/web/cluster.yaml" {
		t.Fatal(got)
	}
}
