package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func repoServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestApplyRepoDeb(t *testing.T) {
	a, h := applyAgent(t)
	srv := repoServer(t, map[string]string{"/zabbix-release_latest_8.0+debian13_all.deb": "Package: zabbix-release\nVersion: 1:8.0-1+debian13\n"})
	a.HTTPClient = srv.Client()
	step := []protocol.Step{{RepoDeb: &protocol.RepoDebStep{URL: srv.URL + "/zabbix-release_latest_8.0+debian13_all.deb"}}}
	res := a.apply(context.Background(), step)
	if !res.OK || !res.Steps[0].Changed || !h.Installed("zabbix-release") {
		t.Fatalf("eerste keer: %+v %v", res, h.Calls())
	}
	calls := strings.Join(h.Calls(), "\n")
	if !strings.Contains(calls, "dpkg -i --force-confdef --force-confold") || !strings.Contains(calls, "apt-get update") {
		t.Fatalf("calls: %s", calls)
	}
	// Het gedownloade pakket blijft niet liggen.
	if entries, _ := os.ReadDir(filepath.Join(a.Root, "var/lib/clusterforge/repo")); len(entries) != 0 {
		t.Errorf("achtergebleven: %v", entries)
	}
	n := len(h.Calls())
	res = a.apply(context.Background(), step)
	if !res.OK || res.Steps[0].Changed || slices.ContainsFunc(h.Calls()[n:], func(c string) bool { return strings.HasPrefix(c, "dpkg -i") }) {
		t.Fatalf("tweede keer: %+v %v", res, h.Calls()[n:])
	}

	for _, bad := range []string{srv.URL + "/bestaat-niet.deb", srv.URL + "/x.txt", "ftp://x/y.deb"} {
		if res := a.apply(context.Background(), []protocol.Step{{RepoDeb: &protocol.RepoDebStep{URL: bad}}}); res.OK {
			t.Errorf("%s: zou moeten falen", bad)
		}
	}
}

func TestApplyRepo(t *testing.T) {
	a, h := applyAgent(t)
	key := "-----BEGIN PGP PUBLIC KEY BLOCK-----\nabc\n-----END PGP PUBLIC KEY BLOCK-----\n"
	srv := repoServer(t, map[string]string{"/key.asc": key})
	a.HTTPClient = srv.Client()
	step := []protocol.Step{{Repo: &protocol.RepoStep{Name: "pgdg", URL: "https://apt.postgresql.org/pub/repos/apt", Suite: "trixie-pgdg", Components: []string{"main"}, Key: srv.URL + "/key.asc"}}}
	res := a.apply(context.Background(), step)
	if !res.OK || !res.Steps[0].Changed {
		t.Fatalf("eerste keer: %+v", res)
	}
	src, _ := os.ReadFile(filepath.Join(a.Root, "etc/apt/sources.list.d/cf-pgdg.sources"))
	if string(src) != "Types: deb\nURIs: https://apt.postgresql.org/pub/repos/apt\nSuites: trixie-pgdg\nComponents: main\nSigned-By: /etc/apt/keyrings/cf-pgdg.asc\n" {
		t.Errorf("bron: %q", src)
	}
	if k, _ := os.ReadFile(filepath.Join(a.Root, "etc/apt/keyrings/cf-pgdg.asc")); string(k) != key {
		t.Errorf("sleutel: %q", k)
	}
	n := len(h.Calls())
	if res := a.apply(context.Background(), step); !res.OK || res.Steps[0].Changed || slices.Contains(h.Calls()[n:], "apt-get update -q") {
		t.Fatalf("tweede keer: %+v %v", res, h.Calls()[n:])
	}
	// Een sleutel over http weigert de agent.
	step[0].Repo.Key = strings.Replace(srv.URL, "https", "http", 1) + "/key.asc"
	if res := a.apply(context.Background(), step); res.OK || !strings.Contains(res.Error, "https") {
		t.Fatalf("http-sleutel: %+v", res)
	}
}

func TestApplyIniSet(t *testing.T) {
	a, _ := applyAgent(t)
	p := filepath.Join(a.Root, "etc/zabbix/zabbix_server.conf")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "# Zabbix\n### Option: DBHost\n# DBHost=localhost\n\nDBName=zabbix\n\n### Option: DBPassword\n# DBPassword=\n"
	if err := os.WriteFile(p, []byte(orig), 0o640); err != nil {
		t.Fatal(err)
	}
	set := func(k, v, mode string) protocol.Result {
		return a.apply(context.Background(), []protocol.Step{{IniSet: &protocol.IniSetStep{Path: "/etc/zabbix/zabbix_server.conf", Key: k, Value: v, Mode: mode}}})
	}
	if res := set("DBPassword", "geheim-wachtwoord", ""); !res.OK || !res.Steps[0].Changed || strings.Contains(strings.Join(res.Steps[0].Output, " "), "geheim") {
		t.Fatalf("wachtwoord: %+v", res)
	}
	if res := set("DBHost", "10.0.0.5", ""); !res.OK || !res.Steps[0].Changed {
		t.Fatalf("host: %+v", res)
	}
	if res := set("DBName", "zabbix", ""); !res.OK || res.Steps[0].Changed {
		t.Fatalf("zelfde waarde: %+v", res)
	}
	got, _ := os.ReadFile(p)
	want := "# Zabbix\n### Option: DBHost\n# DBHost=localhost\nDBHost=10.0.0.5\n\nDBName=zabbix\n\n### Option: DBPassword\n# DBPassword=\nDBPassword=geheim-wachtwoord\n"
	if string(got) != want {
		t.Errorf("bestand:\n%s\nwant\n%s", got, want)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %o", fi.Mode().Perm())
	}
	// Een nieuw bestand, met mode.
	if res := a.apply(context.Background(), []protocol.Step{{IniSet: &protocol.IniSetStep{Path: "/etc/app.env", Key: "A", Value: "1", Mode: "0600"}}}); !res.OK {
		t.Fatalf("nieuw: %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(a.Root, "etc/app.env")); string(b) != "A=1\n" {
		t.Errorf("nieuw: %q", b)
	}
	if res := set("bad key", "x", ""); res.OK {
		t.Error("ongeldige sleutel")
	}
	if res := set("A", "x\ny", ""); res.OK {
		t.Error("twee regels")
	}
}

func TestIniSet(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "k=v\n"},
		{"a = 1\nk = old\n", "a = 1\nk = v\n"},
		{"k=1\nk=2\n", "k=v\n"},
		{"  k  =  1", "  k  =  v\n"},
		{"; k = x\nother=1", "; k = x\nk=v\nother=1\n"},
		{"kk=1\n", "kk=1\nk=v\n"},
	}
	for _, c := range cases {
		if got := iniSet(c.in, "k", "v"); got != c.want {
			t.Errorf("%q: %q, wil %q", c.in, got, c.want)
		}
	}
}
