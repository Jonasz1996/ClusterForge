package deps

import (
	"slices"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/status"
)

// connWorld is web-test met php8.2-fpm en nginx, db-test met mariadb
// achter VIP 10.0.30.100, een losse node met redis en een externe NFS.
func connWorld(t *testing.T) (*world, Nets) {
	b := newWorld(t)
	b.cluster("web-test", "test", status.Healthy)
	b.cluster("db-test", "test", status.Healthy, VIP{Address: "10.0.30.100", Owner: b.id("db-01")})
	b.node("web-01", "web-test")
	b.node("web-02", "web-test")
	b.node("db-01", "db-test")
	b.node("db-02", "db-test")
	b.node("solo", "")
	b.svc("web-test", "php8.2-fpm", "app", "php8.2-fpm")
	b.svc("web-test", "nginx", "web", "nginx")
	b.svc("db-test", "mariadb", "database", "mariadb")
	b.svc("node:solo", "redis", "cache", "redis-server")
	b.w.Services[len(b.w.Services)-1].Port = 6379
	b.svc("extern", "nfs", "storage", "")
	nets := NewNets([]Net{
		NetOf(b.id("web-01"), "", []string{"10.0.20.11"}, []byte(`[{"proto":"tcp","addr":"127.0.0.1","port":9000,"unit":"php8.2-fpm"}]`)),
		NetOf(b.id("web-02"), "10.0.20.12", nil, nil),
		NetOf(b.id("db-01"), "10.0.30.11", []string{"10.0.30.100", "fe80::1"},
			[]byte(`[{"proto":"tcp","addr":"0.0.0.0","port":3306,"unit":"mariadb"},{"proto":"tcp","addr":"0.0.0.0","port":9100}]`)),
		NetOf(b.id("db-02"), "10.0.30.12", nil, []byte(`[{"proto":"tcp","addr":"0.0.0.0","port":3306,"unit":"mariadb"}]`)),
		NetOf(b.id("solo"), "10.0.20.50", nil, nil),
	})
	return b, nets
}

func TestDest(t *testing.T) {
	b, nets := connWorld(t)
	g := b.build()
	skip := SelfSkip([]string{"10.0.0.5"}, Ports(":8080", "0.0.0.0:4222"))
	web1 := b.id("web-01")
	for _, tc := range []struct {
		addr string
		port int
		kind string
		svc  string
		text string
	}{
		{"10.0.30.100", 3306, DestService, "db-test/mariadb", "mariadb in db-test"},
		{"::ffff:10.0.30.100", 3306, DestService, "db-test/mariadb", "mariadb in db-test"},
		{"10.0.30.12", 3306, DestService, "db-test/mariadb", "mariadb in db-test"},
		{"10.0.20.50", 6379, DestService, "node:solo/redis", "redis in solo"},
		{"10.0.0.60", 2049, DestService, "extern/nfs", "nfs (extern)"},
		{"127.0.0.1", 9000, DestService, "web-test/php8.2-fpm", "php8.2-fpm in web-test"},
		{"10.0.30.11", 9100, DestNode, "", "db-01"},
		{"10.0.30.100", 22, DestVIP, "", "VIP van db-test"},
		{"10.0.0.5", 4222, DestClusterForge, "", "ClusterForge"},
		{"10.0.0.5", 8080, DestClusterForge, "", "ClusterForge"},
		{"10.0.0.5", 5432, DestUnknown, "", ""},
		{"93.184.216.34", 443, DestUnknown, "", ""},
		{"fe80::1", 22, DestUnknown, "", ""},
	} {
		d := g.Dest(nets, Conn{NodeID: web1, Unit: "php8.2-fpm", Addr: tc.addr, Port: tc.port}, skip)
		got := ""
		if d.Service != nil {
			got = b.name(d.Service.ID)
		}
		if d.Kind != tc.kind || got != tc.svc || g.DestLabel(d) != tc.text {
			t.Errorf("%s:%d: %s %q %q, verwacht %s %q %q", tc.addr, tc.port, d.Kind, got, g.DestLabel(d), tc.kind, tc.svc, tc.text)
		}
	}
	if id, ok := nets.NodeAt("10.0.20.11"); !ok || id != web1 {
		t.Errorf("NodeAt: %v %v", id, ok)
	}
}

func TestDestPrefersPortAndOwner(t *testing.T) {
	b, _ := connWorld(t)
	// Een tweede dienst in db-test met poort 3306 wint van de unit, en een
	// bevestigde van een voorstel.
	b.svcState("db-test", "galera", "database", "", StateSuggested)
	b.w.Services[len(b.w.Services)-1].Port = 3306
	b.svc("db-test", "proxysql", "database", "")
	b.w.Services[len(b.w.Services)-1].Port = 3306
	g := b.build()
	nets := NewNets([]Net{NetOf(b.id("db-01"), "10.0.30.11", nil, nil)})
	d := g.Dest(nets, Conn{NodeID: b.id("web-01"), Addr: "10.0.30.100", Port: 3306}, nil)
	if d.Service == nil || d.Service.Name != "proxysql" {
		t.Fatalf("verwacht proxysql, kreeg %+v", d)
	}
}

func TestOrigin(t *testing.T) {
	b, _ := connWorld(t)
	g := b.build()
	for _, tc := range []struct {
		node, unit, want string
	}{
		{"web-01", "php8.2-fpm", "web-test/php8.2-fpm"},
		{"solo", "redis-server", "node:solo/redis"},
		{"web-01", "apt-daily", ""},
		{"web-01", "", ""},
		{"web-01", "cf-agent", ""},
		// Een unit in een ander cluster telt niet.
		{"db-01", "php8.2-fpm", ""},
	} {
		s, ok := g.Origin(Conn{NodeID: b.id(tc.node), Unit: tc.unit})
		got := ""
		if ok {
			got = b.name(s.ID)
		}
		if got != tc.want {
			t.Errorf("%s %s: %q, verwacht %q", tc.node, tc.unit, got, tc.want)
		}
	}
}

func TestObserve(t *testing.T) {
	b, nets := connWorld(t)
	g := b.build()
	t0 := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	conns := []Conn{
		{NodeID: b.id("web-01"), Unit: "php8.2-fpm", Addr: "10.0.30.100", Port: 3306, FirstSeen: t0, LastSeen: t0.Add(20 * time.Hour), Recent: 1000},
		{NodeID: b.id("web-02"), Unit: "php8.2-fpm", Addr: "10.0.30.12", Port: 3306, FirstSeen: t0.Add(-time.Hour), LastSeen: t0.Add(19 * time.Hour), Recent: 240},
		{NodeID: b.id("web-01"), Unit: "php8.2-fpm", Addr: "203.0.113.7", Port: 443, FirstSeen: t0, LastSeen: t0.Add(2 * time.Hour), Recent: 96},
		{NodeID: b.id("web-02"), Unit: "php8.2-fpm", Addr: "203.0.113.7", Port: 443, FirstSeen: t0, LastSeen: t0.Add(time.Hour), Recent: 4},
		// Weg: geen dienst, de agent zelf, ClusterForge, dezelfde dienst, een
		// node zonder dienst op die poort.
		{NodeID: b.id("web-01"), Unit: "apt-daily", Addr: "151.101.2.132", Port: 80, Recent: 9},
		{NodeID: b.id("web-01"), Unit: "cf-agent", Addr: "10.0.0.5", Port: 4222, Recent: 1440},
		{NodeID: b.id("web-01"), Unit: "nginx", Addr: "10.0.0.5", Port: 8080, Recent: 30},
		{NodeID: b.id("db-01"), Unit: "mariadb", Addr: "10.0.30.12", Port: 3306, Recent: 1440},
		{NodeID: b.id("web-01"), Unit: "php8.2-fpm", Addr: "10.0.30.11", Port: 9100, Recent: 10},
	}
	seen := g.Observe(nets, conns, SelfSkip([]string{"10.0.0.5"}, []int{4222, 8080}))
	if len(seen) != 2 {
		t.Fatalf("verwacht 2 paren, kreeg %d: %+v", len(seen), seen)
	}
	db := seen[0]
	if b.name(db.From.ID) != "web-test/php8.2-fpm" || db.To == nil || b.name(db.To.ID) != "db-test/mariadb" {
		t.Fatalf("eerste paar: %+v", db)
	}
	if db.Recent != 1240 || !db.FirstSeen.Equal(t0.Add(-time.Hour)) || !db.LastSeen.Equal(t0.Add(20*time.Hour)) || len(db.Lines) != 2 {
		t.Errorf("totalen: %+v", db)
	}
	want := "php8.2-fpm op web-01 en web-02 → mariadb in db-test: 1.240 verbindingen gezien naar 10.0.30.100:3306 en 10.0.30.12:3306 in de afgelopen 24 uur"
	if got := g.Text(db); got != want {
		t.Errorf("tekst:\n%s\nverwacht\n%s", got, want)
	}
	ext := seen[1]
	if ext.To != nil || ext.Addr != "203.0.113.7" || ext.Port != 443 || ext.Recent != 100 {
		t.Errorf("onbekende bestemming: %+v", ext)
	}
	if got := g.Text(ext); got != "php8.2-fpm op web-01 en web-02 → 203.0.113.7:443: 100 verbindingen gezien in de afgelopen 24 uur" {
		t.Errorf("tekst: %s", got)
	}
	ext.Recent = 0
	if got := g.Text(ext); got != "php8.2-fpm op web-01 en web-02 → 203.0.113.7:443: geen verbindingen meer gezien in de afgelopen 24 uur" {
		t.Errorf("tekst zonder verbindingen: %s", got)
	}
}

func TestPortsAndNames(t *testing.T) {
	if got := Ports(":8080", "0.0.0.0:4222", "cf.lan:4222", "https://cf.lan", "http://10.0.0.5:8081/", "", "cf.lan"); !slices.Equal(got, []int{8080, 4222, 443, 8081}) {
		t.Errorf("Ports: %v", got)
	}
	for in, want := range map[string]string{"10.0.40.5": "10.0.40.5:5432", "2001:db8::1": "2001:db8::1.5432"} {
		if got := externalName(in, 5432); got != want || !nameRe.MatchString(got) {
			t.Errorf("externalName(%s) = %s", in, got)
		}
	}
	if HostPort("2001:db8::1", 443) != "[2001:db8::1]:443" || HostPort("10.0.0.1", 80) != "10.0.0.1:80" {
		t.Error("HostPort")
	}
	if portKind(5432) != "database" || portKind(12345) != "external" {
		t.Error("portKind")
	}
	for _, k := range []int{80, 3306, 6379, 5672, 53, 2049, 1} {
		if !KnownKind(portKind(k)) {
			t.Errorf("portKind(%d) is geen bekende soort", k)
		}
	}
	if got := listed([]string{"a", "b", "c", "d"}, 3); got != "a, b en 2 andere" {
		t.Errorf("listed: %s", got)
	}
	if Count(1, "verbinding", "verbindingen") != "1 verbinding" || Count(1234567, "x", "xen") != "1.234.567 xen" {
		t.Error("Count")
	}
}
