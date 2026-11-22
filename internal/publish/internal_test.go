package publish

import (
	"strings"
	"testing"
)

func TestInternalReason(t *testing.T) {
	cases := []struct {
		r        ServiceRow
		internal bool
	}{
		{ServiceRow{Unit: "haproxy.service", Port: 13100}, true},
		{ServiceRow{Unit: "haproxy", Port: 7000, Title: "Statistics Report for HAProxy"}, true},
		{ServiceRow{Port: 7000, Title: "Statistics Report for HAProxy"}, true},
		{ServiceRow{Unit: "patroni", Port: 8008}, true},
		{ServiceRow{Unit: "spiceproxy", Port: 3128}, true},
		{ServiceRow{Unit: "tailscaled", Port: 61196}, true},
		{ServiceRow{Container: "seaweedfs-volume", Image: "chrislusf/seaweedfs:latest", Port: 8080}, true},
		{ServiceRow{Unit: "crowdsec", Port: 8081}, true},
		{ServiceRow{Container: "npm-app-1", Image: "jc21/nginx-proxy-manager:latest", Port: 443}, true},
		{ServiceRow{Container: "npm-app-1", Image: "jc21/nginx-proxy-manager:latest", Port: 81}, false},
		{ServiceRow{Container: "root-server-1", Image: "ghcr.io/goauthentik/server:2025.6.2", Port: 9300}, true},
		{ServiceRow{Container: "root-server-1", Image: "ghcr.io/goauthentik/server:2025.6.2", Port: 9000}, false},
		{ServiceRow{Unit: "pveproxy", Port: 8006}, false},
		{ServiceRow{Container: "searxng", Image: "searxng/searxng:latest", Port: 8080}, false},
		{ServiceRow{Container: "webserver", Image: "nginx:latest", Port: 9300}, false},
	}
	for _, c := range cases {
		if got := internalReason(c.r) != ""; got != c.internal {
			t.Errorf("%+v: intern %v, verwacht %v", c.r, got, c.internal)
		}
	}
}

func TestDomainTypo(t *testing.T) {
	cases := []struct {
		name, fixed string
		ok          bool
	}{
		{"proxmox50.bogaert.be", "proxmox50.jbogaert.be", true},
		{"nas.jbogeart.be", "nas.jbogaert.be", true},
		{"nas.jbogaert.be", "", false},
		{"jbogaert.be", "", false},
		{"example.org", "", false},
		{"app.jbogaert.nl", "", false},
		{"app.bogaert.com", "", false},
	}
	for _, c := range cases {
		_, fixed, ok := domainTypo(c.name, "jbogaert.be")
		if ok != c.ok || fixed != c.fixed {
			t.Errorf("%s: %q %v, verwacht %q %v", c.name, fixed, ok, c.fixed, c.ok)
		}
	}
}

func TestSuggestTarget(t *testing.T) {
	svcs := []ServiceRow{
		{Key: "a", Proto: "tcp", Web: true, Kind: "http", Port: 5100, Container: "changedetection", Image: "dgtlmoon/changedetection.io", Target: "192.168.106.166", TargetVIP: true, Label: "changedetection"},
		{Key: "b", Proto: "tcp", Web: true, Kind: "http", Port: 15100, Unit: "haproxy", Target: "192.168.106.166", TargetVIP: true, Internal: true, Label: "haproxy"},
		{Key: "c", Proto: "tcp", Web: true, Kind: "https", Port: 5651, Container: "ferdium", Image: "lscr.io/linuxserver/ferdium:latest", Target: "192.168.106.166", TargetVIP: true, Label: "ferdium"},
		{Key: "d", Proto: "tcp", Web: true, Kind: "http", Port: 5650, Container: "ferdium", Image: "lscr.io/linuxserver/ferdium:latest", Target: "192.168.106.166", TargetVIP: true, Label: "ferdium"},
	}
	if s := suggestTarget("changedetection.jbogaert.be", 15100, svcs); s == nil || s.ServiceKey != "a" || !s.Sure {
		t.Errorf("changedetection: %+v", s)
	}
	if s := suggestTarget("ferdium.bogaert.be", 5651, svcs); s == nil || s.ServiceKey != "c" || s.Scheme != "https" {
		t.Errorf("ferdium: %+v", s)
	}
	if s := suggestTarget("iets.jbogaert.be", 15100, svcs); s != nil {
		t.Errorf("haproxy-poort mag geen voorstel geven: %+v", s)
	}
}

func TestKeepSnippet(t *testing.T) {
	cur := "client_max_body_size 0;\n\n" + AuthentikSnippet("http://ak:9000")
	got, err := keepSnippet(cur, "add_header X 1;")
	if err != nil || !strings.HasPrefix(got, "add_header X 1;\n\n"+snippetBegin) || !strings.HasSuffix(got, snippetEnd) {
		t.Fatalf("%v %q", err, got)
	}
	if _, err := keepSnippet(cur, "location / { }"); err == nil {
		t.Fatal("location / naast forward auth")
	}
	if got, _ := keepSnippet("oud", ""); got != "" {
		t.Fatalf("leeg: %q", got)
	}
}
