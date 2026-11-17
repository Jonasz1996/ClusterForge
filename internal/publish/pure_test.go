package publish

import (
	"strings"
	"testing"
	"time"
)

func TestFullName(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"homepage", "homepage.jbogaert.be", true},
		{"Homepage.", "homepage.jbogaert.be", true},
		{"cloud.example.org", "cloud.example.org", true},
		{"", "", false},
		{"under_score", "", false},
		{"-start", "", false},
		{"*.jbogaert.be", "", false},
	} {
		got, err := FullName(c.in, "jbogaert.be")
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("FullName(%q) = %q, %v", c.in, got, err)
		}
	}
}

func TestSuggestLabel(t *testing.T) {
	for in, want := range map[string]string{
		"cf-homepage-1":                  "homepage",
		"ghcr.io/gethomepage/homepage:1": "homepage",
		"Vault_Warden":                   "vault-warden",
		"docker-proxy":                   "",
	} {
		if got := SuggestLabel(in); got != want {
			t.Errorf("SuggestLabel(%q) = %q, wil %q", in, got, want)
		}
	}
	if got := SuggestLabel("docker", "grafana-server"); got != "grafana" {
		t.Errorf("tweede naam: %q", got)
	}
}

func TestSnippet(t *testing.T) {
	own := "client_max_body_size 0;"
	with, err := withSnippet(own, "http://192.168.102.242:9000")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(with, own+"\n\n"+snippetBegin) || !strings.Contains(with, "proxy_pass              http://192.168.102.242:9000/outpost.goauthentik.io;") {
		t.Fatalf("met snippet:\n%s", with)
	}
	// Twee keer zetten geeft één blok.
	again, _ := withSnippet(with, "http://192.168.102.242:9000")
	if strings.Count(again, snippetBegin) != 1 {
		t.Fatal("blok staat er twee keer")
	}
	if got := withoutSnippet(with); got != own {
		t.Fatalf("zonder snippet: %q", got)
	}
	if got := withoutSnippet(AuthentikSnippet("http://x")); got != "" {
		t.Fatalf("alleen snippet: %q", got)
	}
	if _, err := withSnippet("location / {\n  return 200;\n}", "http://x"); err == nil {
		t.Fatal("eigen location / niet geweigerd")
	}
}

func TestPickCert(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	certs := []NPMCert{
		{ID: 1, DomainNames: []string{"*.jbogaert.be"}, ExpiresOn: "2026-11-17 12:00:00"},
		{ID: 2, DomainNames: []string{"*.jbogaert.be", "jbogaert.be"}, ExpiresOn: "2027-01-10 00:00:00"},
		{ID: 3, DomainNames: []string{"cloud.jbogaert.be"}, ExpiresOn: "2027-02-01 00:00:00"},
	}
	if c := pickCert(certs, "homepage.jbogaert.be", now); c == nil || c.ID != 2 {
		t.Fatalf("wildcard: %+v", c)
	}
	if c := pickCert(certs, "cloud.jbogaert.be", now); c == nil || c.ID != 3 {
		t.Fatalf("exact, langst geldig: %+v", c)
	}
	if c := pickCert(certs, "a.b.jbogaert.be", now); c != nil {
		t.Fatalf("wildcard dekt maar één niveau: %+v", c)
	}
}

func TestSkipAuthentik(t *testing.T) {
	d := DefaultDefaults()
	if !skipAuthentik(d, "vaultwarden/server:latest") || !skipAuthentik(d, "x", "Home-Assistant") {
		t.Fatal("eigen login niet herkend")
	}
	if skipAuthentik(d, "homepage", "ghcr.io/gethomepage/homepage") {
		t.Fatal("homepage overgeslagen")
	}
}
