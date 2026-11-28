package publish

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDecorateOpenPathsEnTerugval(t *testing.T) {
	adv := AuthentikSnippet("http://192.168.103.244:9000")
	fb := &Fallback{Scheme: "http", Host: "192.168.10.5", Port: 8080, Cluster: "oud"}
	got := decorate(adv, []string{"/api", "/guide.xml"}, fb)
	for _, want := range []string{
		"location ^~ /api { # open", "location = /guide.xml { # open", "location @cf_fallback",
		"proxy_pass          http://192.168.10.5:8080;", "error_page 502 504 = @cf_fallback;", fallbackMark,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mist %q in\n%s", want, got)
		}
	}
	if p := extrasPaths(got); strings.Join(p, ",") != "/api,/guide.xml" {
		t.Errorf("paden %v", p)
	}
	if !hasFallback(got) {
		t.Error("geen terugval gezien")
	}
	// Opnieuw toepassen is hetzelfde; zonder extra's is het origineel terug.
	if again := decorate(got, []string{"/api", "/guide.xml"}, fb); again != got {
		t.Errorf("niet idempotent:\n%s\n---\n%s", got, again)
	}
	if back := decorate(got, nil, nil); back != adv {
		t.Errorf("niet terug naar het origineel:\n%s", back)
	}
	// Zonder forward auth geen open paden (er is niets om open te zetten).
	if plain := decorate("", []string{"/api"}, nil); plain != "" {
		t.Errorf("open pad zonder forward auth: %q", plain)
	}
	// keepSnippet houdt de extra's bij het bewerken van de eigen Advanced.
	kept, err := keepSnippet(got, "client_max_body_size 0;")
	if err != nil || !strings.Contains(kept, extrasBegin) || !strings.HasPrefix(kept, "client_max_body_size 0;") {
		t.Errorf("keepSnippet: %v\n%s", err, kept)
	}
	if own := ownAdvanced(kept); own != "client_max_body_size 0;" {
		t.Errorf("ownAdvanced %q", own)
	}
}

func TestPolicies(t *testing.T) {
	if _, err := cleanPolicy(Policy{Name: "Matomo", Mode: PolicyExceptPaths, Paths: []string{"/matomo.php", " /matomo.js", "/matomo.php"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Policy{
		{Name: "x", Mode: PolicyExceptPaths},
		{Name: "x", Mode: PolicyExceptPaths, Paths: []string{"/a;b"}},
		{Name: "x", Mode: PolicyExceptPaths, Paths: []string{"/"}},
		{Name: "x", Mode: "raar"},
		{Name: "", Mode: PolicyNever},
	} {
		if _, err := cleanPolicy(bad); err == nil {
			t.Errorf("%+v aanvaard", bad)
		}
	}
	pols := map[string]Policy{"proxmox": {Name: "proxmox", Mode: PolicyNever}, "matomo": {Name: "matomo", Mode: PolicyExceptPaths, Paths: []string{"/matomo.js"}}}
	if p, ok := policyFor(pols, "proxmox50.jbogaert.be"); !ok || p.Mode != PolicyNever {
		t.Errorf("proxmox50: %+v %v", p, ok)
	}
	d := DefaultDefaults()
	if p := effectivePolicy(pols, d, "plex.jbogaert.be"); p.Mode != PolicyOwnLogin || !p.Implicit {
		t.Errorf("plex: %+v", p)
	}
	if p := effectivePolicy(pols, d, "sonarr.jbogaert.be"); !p.WantsAuthentik() {
		t.Errorf("sonarr: %+v", p)
	}
	if n := defaultName(ServiceRow{Label: "app", Environment: "test"}, "jbogaert.be"); n != "app.test.jbogaert.be" {
		t.Errorf("test: %s", n)
	}
	if n := defaultName(ServiceRow{Label: "media", Environment: "lab"}, "jbogaert.be"); n != "media.jbogaert.be" {
		t.Errorf("lab: %s", n)
	}
	if n := defaultName(ServiceRow{Label: "app", Environment: "prod"}, "jbogaert.be"); n != "app.jbogaert.be" {
		t.Errorf("prod: %s", n)
	}
}

func TestBuildScan(t *testing.T) {
	prod, test := uuid.New(), uuid.New()
	n1 := uuid.New()
	now := time.Now()
	st := Settings{Domain: "jbogaert.be", Defaults: DefaultDefaults(), AuthentikClusterID: &prod, AuthentikHasToken: true}
	svc := func(key, label, cl, env string, id uuid.UUID) ServiceRow {
		r := ServiceRow{Key: key, ClusterID: &id, ClusterName: cl, Environment: env, Port: 80, Kind: "http", Web: true, Label: label,
			Target: "192.168.10.50", TargetVIP: true, Nodes: []NodeShort{{ID: n1}}}
		r.DefaultName = defaultName(r, st.Domain)
		return r
	}
	pols := map[string]Policy{
		"vaultx": {Name: "vaultx", Mode: PolicyOwnLogin},
		"matomo": {Name: "matomo", Mode: PolicyExceptPaths, Paths: []string{"/matomo.js"}},
	}
	snippet := AuthentikSnippet("http://192.168.103.244:9000")
	dead := &Probe{Error: "connection refused", At: now}
	ov := &Overview{
		known: map[string]knownAddr{
			"192.168.10.145": {What: "VIP van media"},
			"192.168.30.20":  {What: "node guest1", Ready: "agent offline"},
		},
		Settings:  st,
		Authentik: ReadState{Linked: true, ReadAt: &now},
		Services: []ServiceRow{
			svc("new", "sonarr", "media", "prod", prod),
			svc("vx", "vaultx", "vault", "prod", prod),
			svc("t", "wiki", "wiki-test", "test", test),
			svc("mv", "grafana", "grafana2", "prod", prod),
		},
		Hosts: []HostView{
			{ID: 1, Domains: []string{"grafana.jbogaert.be"}, Enabled: true, ForwardScheme: "http", ForwardHost: "192.168.20.9", ForwardPort: 3000, Authentik: AKProtected, ClusterName: "grafana"},
			{ID: 2, Domains: []string{"gimp.jbogaert.be"}, Enabled: true, ForwardHost: "192.168.0.7", ForwardPort: 3000, Cleanup: true, Authentik: AKOpen, TargetProbe: dead},
			// Scan 28/11: onbereikbaar maar bekend, gateway of niet bekeken: nooit verwijderen.
			{ID: 6, Domains: []string{"media.jbogaert.be"}, Enabled: true, ForwardHost: "192.168.10.145", ForwardPort: 80, Cleanup: true, Authentik: AKOpen, TargetProbe: dead},
			{ID: 7, Domains: []string{"firewall.jbogaert.be"}, Enabled: true, ForwardHost: "192.168.10.1", ForwardPort: 443, Cleanup: true, Authentik: AKOpen, TargetProbe: dead},
			{ID: 8, Domains: []string{"adguard.jbogaert.be"}, Enabled: true, ForwardHost: "192.168.0.8", ForwardPort: 3000, Cleanup: true, Authentik: AKOpen},
			{ID: 9, Domains: []string{"guest.jbogaert.be"}, Enabled: true, ForwardHost: "192.168.30.20", ForwardPort: 8080, Cleanup: true, Authentik: AKOpen, TargetProbe: dead},
			{ID: 3, Domains: []string{"radarr.jbogaert.be"}, Enabled: true, ForwardHost: "192.168.10.60", ForwardPort: 7878, Authentik: AKOpen, ServiceKey: "x"},
			{ID: 4, Domains: []string{"matomo.jbogaert.be"}, Enabled: true, ForwardHost: "192.168.10.61", ForwardPort: 80, Authentik: AKProtected, ServiceKey: "y"},
			{ID: 5, Domains: []string{"proxmox50.jbogaert.be"}, Enabled: true, ForwardHost: "192.168.10.62", ForwardPort: 8006, Authentik: AKProtected, ServiceKey: "z",
				AdvancedFix: []AdvFix{{Old: "http://192.168.0.244:9000", New: "http://192.168.103.244:9000", Why: "outpost van Authentik uit de instellingen"}}},
		},
		rawHosts: []NPMHost{
			{ID: 1, DomainNames: []string{"grafana.jbogaert.be"}, AdvancedConfig: snippet},
			{ID: 4, DomainNames: []string{"matomo.jbogaert.be"}, AdvancedConfig: snippet},
			{ID: 5, DomainNames: []string{"proxmox50.jbogaert.be"}, AdvancedConfig: snippet},
		},
		Apps: []AKAppView{{Name: "Tautulli", Slug: "tautulli", Mode: "forward_single", External: "https://tautulli.jbogaert.be"}},
		rawAK: AKRead{
			Applications: []AKApplication{{Slug: "tautulli", Name: "Tautulli", Provider: intPtr(7)}},
			Providers:    []AKProxyProvider{{PK: 7, ExternalHost: "https://tautulli.jbogaert.be", Mode: "forward_single"}},
		},
	}
	items, notes := buildScan(st, ov, pols, map[string]Owner{}, map[string]bool{})
	for _, id := range []string{"disable:6", "disable:7", "disable:8", "disable:9"} {
		for _, it := range items {
			if it.ID == id {
				t.Errorf("%s voorgesteld om te verwijderen", id)
			}
		}
	}
	for _, want := range []string{"media.jbogaert.be", "firewall.jbogaert.be", "adguard.jbogaert.be", "guest.jbogaert.be"} {
		if !strings.Contains(strings.Join(notes, "\n"), want) {
			t.Errorf("geen opmerking voor %s in %v", want, notes)
		}
	}
	got := map[string]ScanItem{}
	for _, it := range items {
		got[it.ID] = it
	}
	check := func(id, kind, what string) ScanItem {
		t.Helper()
		it, ok := got[id]
		if !ok {
			t.Fatalf("%s ontbreekt; wel: %v", id, keysOf(got))
		}
		if it.Kind != kind || it.What != what {
			t.Errorf("%s: %s/%s, wil %s/%s", id, it.Kind, it.What, kind, what)
		}
		return it
	}
	if it := check("add:new", ScanAdd, "publish"); it.Domain != "sonarr.jbogaert.be" || it.Action.Authentik == nil || !*it.Action.Authentik {
		t.Errorf("sonarr: %+v", it)
	}
	if it := check("add:vx", ScanAdd, "publish"); *it.Action.Authentik {
		t.Error("vaultx hoort zonder Authentik (eigen login)")
	}
	if it := check("add:t", ScanAdd, "publish"); it.Domain != "wiki.test.jbogaert.be" {
		t.Errorf("test: %s", it.Domain)
	}
	if it := check("move:mv", ScanUpdate, "move"); it.Action.HostID != 1 {
		t.Errorf("verhuizen: %+v", it)
	}
	check("disable:2", ScanRemove, "disable")
	check("protect:3", ScanUpdate, "protect")
	check("paths:4", ScanUpdate, "paths")
	check("outpost:5", ScanUpdate, "outpost")
	check("ak:tautulli", ScanRemove, "ak_disable")
	// Al uitgeschakeld: niet opnieuw.
	items, _ = buildScan(st, ov, pols, map[string]Owner{}, map[string]bool{"gimp.jbogaert.be": true, "ak:tautulli": true})
	for _, it := range items {
		if it.ID == "disable:2" || it.ID == "ak:tautulli" {
			t.Errorf("%s opnieuw voorgesteld", it.ID)
		}
	}
}

func TestPrimaryVIPSkipsDatabase(t *testing.T) {
	c := clusterRow{VIPs: []vipRow{
		{Address: "192.168.103.240", DB: true},
		{Address: "192.168.103.244"},
	}}
	if got := primaryVIP(c, nil); got != "192.168.103.244" {
		t.Errorf("outpost: %s, wil .244 (niet de database-VIP .240)", got)
	}
	if !isDBVIP("Patroni-primary; staat op de node waar de database schrijft (ClusterForge)") || isDBVIP("Authentik") {
		t.Error("isDBVIP")
	}
	if got := primaryVIP(clusterRow{VIPs: []vipRow{{Address: "192.168.10.180", DB: true}}}, nil); got != "" {
		t.Errorf("alleen db-VIP: %s", got)
	}
}

func intPtr(i int) *int { return &i }

func keysOf(m map[string]ScanItem) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
