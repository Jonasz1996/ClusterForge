package homeapps

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/publish"
)

func TestMatch(t *testing.T) {
	cases := []struct{ image, unit, container, want string }{
		{"plexinc/pms-docker:1.41.2", "", "", "plex"},
		{"lscr.io/linuxserver/sonarr:latest", "", "", "sonarr"},
		{"ghcr.io/goauthentik/server:2025.10.1", "", "", "authentik"},
		{"ghcr.io/example/server:1", "", "", ""},
		{"jc21/nginx-proxy-manager:2", "", "", "npm"},
		{"", "plexmediaserver.service", "", "plex"},
		{"", "home-assistant@homeassistant", "", "home-assistant"},
		{"postgres:17", "", "nextcloud-db", ""},
		{"", "", "jellyfin", "jellyfin"},
	}
	for _, c := range cases {
		e, ok := match(c.image, c.unit, c.container)
		if got := e.Slug; ok != (c.want != "") || got != c.want {
			t.Errorf("match(%q,%q,%q) = %q,%v; wil %q", c.image, c.unit, c.container, got, ok, c.want)
		}
	}
	if ImageTag("plexinc/pms-docker:1.41.2") != "1.41.2" || ImageTag("nginx:latest") != "" || ImageTag("reg:5000/app") != "" {
		t.Error("ImageTag")
	}
}

func TestBuild(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	cl := uuid.New()
	n1, n2, n3, p1, p2 := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	in := Input{
		Now: now,
		Nodes: []Node{
			{ID: n1, Hostname: "hp1", ClusterID: &cl, Online: true},
			{ID: n2, Hostname: "hp2", ClusterID: &cl, Online: false},
			{ID: n3, Hostname: "hp3", ClusterID: &cl, Online: true},
			{ID: p1, Hostname: "plex1", Online: true},
			{ID: p2, Hostname: "plex2", Online: true},
		},
		Clusters: []Cluster{{ID: cl, Slug: "homepage", Name: "Homepage", Template: "homepage", VIP: "192.168.113.159", VIPOwner: "hp1"}},
		Services: []publish.ServiceRow{
			// Homepage: nginx op 80 op alle nodes, via het template herkend.
			{Key: "c80", ClusterID: &cl, Nodes: []publish.NodeShort{{ID: n1, Hostname: "hp1"}, {ID: n2, Hostname: "hp2"}, {ID: n3, Hostname: "hp3"}},
				Proto: "tcp", Port: 80, Kind: "http", Status: 200, Unit: "nginx", Target: "192.168.113.159", OnAll: true},
			// Patroni-REST op hetzelfde cluster: hoort er ook bij, geen eigen app.
			{Key: "c8008", ClusterID: &cl, Nodes: []publish.NodeShort{{ID: n1, Hostname: "hp1"}}, Proto: "tcp", Port: 8008, Kind: "http", Unit: "patroni"},
			// Twee losse Plex-servers.
			{Key: "p1", Nodes: []publish.NodeShort{{ID: p1, Hostname: "plex1", IP: "10.0.0.1"}}, Port: 32400, Kind: "http", Status: 401,
				Container: "plex", Image: "plexinc/pms-docker:1.41.2", Target: "10.0.0.1"},
			{Key: "p2", Nodes: []publish.NodeShort{{ID: p2, Hostname: "plex2"}}, Port: 32400, Kind: "http", Status: 502,
				Unit: "plexmediaserver", Target: "10.0.0.2"},
			// Onbekende webdienst, niet gepubliceerd: twijfel.
			{Key: "x", Nodes: []publish.NodeShort{{ID: p1, Hostname: "plex1"}}, Port: 9999, Kind: "http", Container: "iets"},
			// Onbekend maar gepubliceerd: gewone app.
			{Key: "y", Nodes: []publish.NodeShort{{ID: p2, Hostname: "plex2"}}, Port: 8443, Kind: "https", Container: "mijn-tool", Target: "10.0.0.2"},
			// sshd en tcp: nooit.
			{Key: "s", Nodes: []publish.NodeShort{{ID: p2, Hostname: "plex2"}}, Port: 22, Kind: "tcp", Unit: "ssh"},
		},
		Hosts: []publish.HostView{
			{ID: 1, Domains: []string{"homepage.jbogaert.be"}, ServiceKey: "c80", Authentik: publish.AKProtected},
			{ID: 2, Domains: []string{"tool.jbogaert.be"}, ServiceKey: "y"},
		},
		Known: []Known{
			{Key: "plex@plex1", FirstSeen: now.Add(-48 * time.Hour), LastSeen: now.Add(-time.Minute), Override: Override{Group: "Films", Fields: []string{"status"}}},
			{Key: "sonarr@dl", Name: "Sonarr", Group: GroupDownloads, LastSeen: now.Add(-2 * time.Hour), FirstSeen: now.Add(-72 * time.Hour)},
			{Key: "radarr@dl", Name: "Radarr", LastSeen: now.Add(-25 * time.Hour)},
		},
	}
	res := Build(in)
	by := map[string]App{}
	for _, a := range res.Apps {
		by[a.Key] = a
	}
	hp := by["homepage@homepage"]
	if hp.Slug != "homepage" || hp.URL != "https://homepage.jbogaert.be" || hp.ActiveNode != "hp1" || hp.Status != StatusWarn ||
		len(hp.Instances) != 3 || hp.Authentik != publish.AKProtected || len(hp.Ports) != 2 || hp.InternalURL != "http://192.168.113.159:80" {
		t.Errorf("homepage: %+v", hp)
	}
	p := by["plex@plex1"]
	if p.Slug != "plex-plex1" || p.Name != "Plex (plex1)" || p.Group != "Films" || p.Status != StatusOK || p.Version != "1.41.2" ||
		p.FirstSeen == nil || !p.FirstSeen.Equal(now.Add(-48*time.Hour)) || p.URL != "http://10.0.0.1:32400" {
		t.Errorf("plex1: %+v", p)
	}
	if q := by["plex@plex2"]; q.Status != StatusDown || q.Slug != "plex-plex2" {
		t.Errorf("plex2: %+v", q)
	}
	if y := by["mijn-tool@plex2"]; y.Auto != "npm" || y.Catalog != "" || y.Name != "Mijn tool" || y.URL != "https://tool.jbogaert.be" {
		t.Errorf("mijn-tool: %+v", y)
	}
	if s := by["sonarr@dl"]; !s.Gone || s.Status != StatusDown || s.Slug != "sonarr" {
		t.Errorf("sonarr weg: %+v", s)
	}
	if _, ok := by["radarr@dl"]; ok || len(res.Expired) != 1 || res.Expired[0] != "radarr@dl" {
		t.Errorf("radarr moet opgeruimd: %v", res.Expired)
	}
	if len(res.Doubts) != 1 || res.Doubts[0].Key != "x" {
		t.Errorf("twijfel: %+v", res.Doubts)
	}
	if len(res.Apps) != 5 || len(res.Seen) != 4 {
		t.Errorf("apps %d, seen %d", len(res.Apps), len(res.Seen))
	}
	// Twijfel als app gekozen, of genegeerd.
	in.Decisions = map[string]Decision{"x": {Choice: "app", Name: "Mijn dashboard"}}
	res = Build(in)
	found := false
	for _, a := range res.Apps {
		if a.Key == "mijn-dashboard@plex1" && a.Auto == "gekozen" && a.Name == "Mijn dashboard" {
			found = true
		}
	}
	if !found || len(res.Doubts) != 0 {
		t.Errorf("gekozen app ontbreekt: %+v", res.Apps)
	}
	in.Decisions = map[string]Decision{"x": {Choice: "ignore"}}
	if res = Build(in); len(res.Doubts) != 0 || len(res.Apps) != 5 {
		t.Errorf("genegeerd: %d twijfel, %d apps", len(res.Doubts), len(res.Apps))
	}
}

// Interne poorten (HAProxy-frontends, statistieken, Patroni) worden geen
// tegel en geen twijfel, ook niet via het template van het cluster.
func TestBuildSkipsInternal(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	cl := uuid.New()
	n1 := uuid.New()
	in := Input{
		Now:      now,
		Nodes:    []Node{{ID: n1, Hostname: "hp1", ClusterID: &cl, Online: true}},
		Clusters: []Cluster{{ID: cl, Slug: "homepage", Name: "Homepage", Template: "homepage", VIP: "192.168.113.159", VIPOwner: "hp1"}},
		Services: []publish.ServiceRow{
			{Key: "c80", ClusterID: &cl, Nodes: []publish.NodeShort{{ID: n1, Hostname: "hp1"}}, Port: 80, Kind: "http", Status: 200, Unit: "nginx", Target: "192.168.113.159"},
			{Key: "c8008", ClusterID: &cl, Nodes: []publish.NodeShort{{ID: n1, Hostname: "hp1"}}, Port: 8008, Kind: "http", Unit: "patroni", Internal: true},
			{Key: "h1", Nodes: []publish.NodeShort{{ID: n1, Hostname: "hp1"}}, Port: 13100, Kind: "http", Status: 503, Unit: "haproxy", Internal: true},
			{Key: "h2", Nodes: []publish.NodeShort{{ID: n1, Hostname: "hp1"}}, Port: 7000, Kind: "http", Status: 200, Unit: "haproxy", Internal: true},
		},
	}
	res := Build(in)
	if len(res.Apps) != 1 || len(res.Doubts) != 0 {
		t.Fatalf("apps %+v, twijfel %+v", res.Apps, res.Doubts)
	}
	if hp := res.Apps[0]; hp.Slug != "homepage" || len(hp.Ports) != 1 {
		t.Errorf("homepage: %+v", hp)
	}
}
