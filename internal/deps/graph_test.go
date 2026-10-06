package deps

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
)

// world bouwt een wereld uit namen, zodat de tests leesbaar blijven.
type world struct {
	t   *testing.T
	w   World
	ids map[string]uuid.UUID
}

func newWorld(t *testing.T) *world { return &world{t: t, ids: map[string]uuid.UUID{}} }

func (b *world) id(name string) uuid.UUID {
	id, ok := b.ids[name]
	if !ok {
		id = uuid.New()
		b.ids[name] = id
	}
	return id
}

func (b *world) ptr(name string) *uuid.UUID {
	id := b.id(name)
	return &id
}

func (b *world) cluster(name, env string, st status.Status, vips ...VIP) {
	b.w.Clusters = append(b.w.Clusters, Cluster{ID: b.id(name), Name: name, Slug: name, Environment: env, Type: "keepalived", Status: st, VIPs: vips})
}

// node met units als "nginx=active".
func (b *world) node(host, cluster string, units ...string) *Node {
	n := Node{ID: b.id(host), Hostname: host, Active: true, Fresh: true, Units: map[string]string{}, Status: status.Healthy}
	if cluster != "" {
		n.ClusterID = b.ptr(cluster)
	}
	for _, u := range units {
		k, v, _ := strings.Cut(u, "=")
		n.Units[k] = v
	}
	b.w.Nodes = append(b.w.Nodes, n)
	return &b.w.Nodes[len(b.w.Nodes)-1]
}

// svc heet "<cluster>/<naam>" in de ids.
func (b *world) svc(cluster, name, kind, unit string) {
	b.svcState(cluster, name, kind, unit, StateConfirmed)
}

func (b *world) svcState(cluster, name, kind, unit, state string) {
	s := Svc{ID: b.id(cluster + "/" + name), Name: name, Kind: kind, Unit: unit, Source: SourceManual, State: state}
	switch {
	case cluster == "extern":
		s.Address, s.Port = "10.0.0.60", 2049
	case strings.HasPrefix(cluster, "node:"):
		s.NodeID = b.ptr(strings.TrimPrefix(cluster, "node:"))
	default:
		s.ClusterID = b.ptr(cluster)
	}
	b.w.Services = append(b.w.Services, s)
}

// dep: "web-prod/nginx" hangt af van "db-prod/mariadb".
func (b *world) dep(from, to, strength string) {
	b.w.Deps = append(b.w.Deps, Dep{ID: b.id(from + "->" + to), From: b.id(from), To: b.id(to), Strength: strength, Source: SourceManual, State: StateConfirmed})
}

func (b *world) build() *Graph { return Build(b.w) }

// name geeft de naam bij een id.
func (b *world) name(id uuid.UUID) string {
	for k, v := range b.ids {
		if v == id {
			return k
		}
	}
	return id.String()
}

func (b *world) hits(hs []Hit) string {
	var out []string
	for _, h := range hs {
		out = append(out, b.name(h.Service)+" "+string(h.Impact)+": "+h.Reason)
	}
	return strings.Join(out, "\n")
}

// voorbeeld is de wereld uit het ontwerp: web-prod hangt via php-fpm van
// mariadb in db-prod af, app-test zacht.
func voorbeeld(t *testing.T) *world {
	b := newWorld(t)
	b.cluster("db-prod", "prod", status.Healthy)
	b.cluster("web-prod", "prod", status.Healthy, VIP{Address: "10.0.20.10", Owner: b.id("web01")})
	b.cluster("app-test", "test", status.Healthy)
	b.node("db01", "db-prod", "mariadb=active")
	b.node("db02", "db-prod", "mariadb=active")
	b.node("web01", "web-prod", "nginx=active", "keepalived=active", "php8.2-fpm=active")
	b.node("web02", "web-prod", "nginx=active", "keepalived=active", "php8.2-fpm=active")
	b.node("app01", "app-test", "app=active")
	b.svc("db-prod", "mariadb", "database", "mariadb")
	b.svc("web-prod", "nginx", "web", "nginx")
	b.svc("web-prod", "keepalived", "vip", "keepalived")
	b.svc("web-prod", "php8.2-fpm", "app", "php8.2-fpm")
	b.svc("app-test", "app", "app", "app")
	b.dep("web-prod/nginx", "web-prod/keepalived", Hard)
	b.dep("web-prod/nginx", "web-prod/php8.2-fpm", Hard)
	b.dep("web-prod/php8.2-fpm", "db-prod/mariadb", Hard)
	b.dep("app-test/app", "db-prod/mariadb", Soft)
	return b
}

func TestImpactOfService(t *testing.T) {
	b := voorbeeld(t)
	g := b.build()
	got := b.hits(g.Impact(Target{Service: b.ptr("db-prod/mariadb")}))
	// Prod bovenaan, daarbinnen down voor verminderd en dan op naam.
	want := strings.Join([]string{
		"web-prod/nginx down: via php8.2-fpm",
		"web-prod/php8.2-fpm down: hangt hard af van mariadb in db-prod · handmatig",
		"app-test/app degraded: hangt zacht af van mariadb in db-prod · handmatig",
	}, "\n")
	if got != want {
		t.Fatalf("impact:\n%s\nwil\n%s", got, want)
	}
	hs := g.Impact(Target{Service: b.ptr("db-prod/mariadb")})
	for _, h := range hs {
		if h.Service == b.id("web-prod/nginx") {
			if len(h.Path) != 3 || h.Path[0] != b.id("db-prod/mariadb") || h.Path[1] != b.id("web-prod/php8.2-fpm") || h.Edge == nil || h.Edge.To != b.id("web-prod/php8.2-fpm") {
				t.Fatalf("pad van nginx: %v", h.Path)
			}
		}
	}
	// Niets is echt uitgevallen.
	for _, s := range b.w.Services {
		if e := g.Effect(s.ID); e.Impact != ImpactNone || g.Own(s.ID).Status != status.Healthy {
			t.Errorf("%s: %+v %+v", s.Name, g.Own(s.ID), e)
		}
	}
}

func TestRealOutagePropagates(t *testing.T) {
	b := voorbeeld(t)
	for i := range b.w.Nodes {
		if b.w.Nodes[i].Hostname == "db01" || b.w.Nodes[i].Hostname == "db02" {
			b.w.Nodes[i].Units["mariadb"] = "failed"
		}
	}
	g := b.build()
	own := g.Own(b.id("db-prod/mariadb"))
	if own.Status != status.Down || own.Reason != "mariadb draait op geen enkele node (db01: failed, db02: failed)" {
		t.Fatalf("mariadb: %+v", own)
	}
	php, nginx, app := g.Effect(b.id("web-prod/php8.2-fpm")), g.Effect(b.id("web-prod/nginx")), g.Effect(b.id("app-test/app"))
	if php.Impact != ImpactDown || nginx.Impact != ImpactDown || app.Impact != ImpactDegraded || nginx.Cause != b.id("db-prod/mariadb") {
		t.Fatalf("doorgeven: php %+v nginx %+v app %+v", php, nginx, app)
	}
	// De pijl naar de oorzaak kleurt, de pijl naar keepalived niet.
	for _, d := range b.w.Deps {
		want := d.To != b.id("web-prod/keepalived")
		if g.Affected(d) != want {
			t.Errorf("pijl %s: geraakt %v", b.name(d.ID), g.Affected(d))
		}
	}
	if imp, causes := g.GroupImpact("cluster:" + b.id("web-prod").String()); imp != ImpactDown || len(causes) != 1 || causes[0] != "db-prod" {
		t.Fatalf("groep web-prod: %s %v", imp, causes)
	}
	if imp, _ := g.GroupImpact("cluster:" + b.id("db-prod").String()); imp != ImpactNone {
		t.Fatalf("db-prod is zelf de oorzaak, niet geraakt: %s", imp)
	}
}

func TestOwnStatus(t *testing.T) {
	b := newWorld(t)
	b.cluster("web", "lab", status.Degraded, VIP{Address: "10.0.0.10", Owner: uuid.Nil})
	b.cluster("rol", "lab", status.Healthy)
	b.cluster("nieuw", "lab", status.Unknown)
	b.cluster("split", "lab", status.SplitBrain, VIP{Address: "10.0.0.11"})
	b.w.Clusters[3].Reason = "VIP 10.0.0.11 op a en b"
	b.cluster("nieuw-vip", "lab", status.Unknown, VIP{Address: "10.0.0.12"})
	b.w.Clusters[4].Reason = "geen actieve nodes"
	b.node("web01", "web", "nginx=active", "redis-server=inactive")
	b.node("web02", "web", "nginx=active")
	stale := b.node("web03", "web", "nginx=active")
	stale.Fresh = false
	maint := b.node("web04", "web", "nginx=failed")
	maint.Active = false
	b.node("rol01", "rol", "nginx=active", "worker=active")
	b.node("rol02", "rol", "nginx=active")
	b.svc("web", "nginx", "web", "nginx")
	b.svc("web", "redis", "cache", "redis-server")
	b.svc("web", "php", "app", "php8.2-fpm")
	b.svc("web", "site", "web", "")
	b.svc("web", "vip", "vip", "keepalived")
	b.svc("rol", "worker", "app", "worker")
	b.svc("split", "vip", "vip", "")
	b.svc("nieuw-vip", "vip", "vip", "keepalived")
	b.svc("extern", "nfs", "storage", "")
	g := b.build()
	for name, want := range map[string]Own{
		// web03 heeft geen verse heartbeat, web04 is niet actief.
		"web/nginx": {Status: status.Degraded, Reason: "nginx draait op 2 van 3 nodes (web03: geen heartbeat)"},
		"web/redis": {Status: status.Down, Reason: "redis-server draait op geen enkele node (web01: inactive)"},
		// De agent volgt de unit niet of hij staat er niet.
		"web/php":  {Status: status.Unknown, Reason: "geen actieve node meldt php8.2-fpm"},
		"web/site": {Status: status.Unknown, Reason: "geen unit om te volgen"},
		"web/vip":  {Status: status.Down, Reason: "VIP 10.0.0.10 heeft geen eigenaar"},
		// Een dienst die maar op één rol draait, is gewoon gezond.
		"rol/worker":    {Status: status.Healthy},
		"split/vip":     {Status: status.Down, Reason: "split-brain: VIP 10.0.0.11 op a en b"},
		"nieuw-vip/vip": {Status: status.Unknown, Reason: "geen actieve nodes"},
		"extern/nfs":    {Status: status.Unknown, Reason: "extern: ClusterForge bewaakt deze dienst niet"},
	} {
		got := g.Own(b.id(name))
		if got.Status != want.Status || got.Reason != want.Reason {
			t.Errorf("%s: %s %q, wil %s %q", name, got.Status, got.Reason, want.Status, want.Reason)
		}
	}
	if inst := g.Own(b.id("web/nginx")).Instances; len(inst) != 3 || inst[2].Hostname != "web03" || inst[2].Running || inst[2].State != "" {
		t.Fatalf("instanties: %+v", inst)
	}
}

func TestPropagationRules(t *testing.T) {
	b := newWorld(t)
	b.cluster("c", "lab", status.Healthy)
	b.node("n1", "c", "a=failed", "b=active", "x=failed")
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "x", "y"} {
		b.svc("c", n, "app", n)
	}
	b.svcState("c", "voorstel", "app", "voorstel", StateSuggested)
	b.svcState("c", "genegeerd", "app", "genegeerd", StateIgnored)
	// Een cyclus: b en c hangen hard van elkaar af, b ook van a.
	b.dep("c/b", "c/a", Hard)
	b.dep("c/c", "c/b", Hard)
	b.dep("c/b", "c/c", Hard)
	// Zacht stopt: d hangt zacht af van c, e hard van d.
	b.dep("c/d", "c/c", Soft)
	b.dep("c/e", "c/d", Hard)
	// Een voorstel en een genegeerde dienst geven niets door en worden niet
	// gemarkeerd.
	b.dep("c/voorstel", "c/a", Hard)
	b.dep("c/f", "c/voorstel", Hard)
	b.dep("c/g", "c/genegeerd", Hard)
	// Een dienst die zelf al down is, krijgt geen tweede markering.
	b.dep("c/x", "c/a", Hard)
	b.dep("c/y", "c/x", Hard)
	g := b.build()
	want := map[string]Impact{
		"c/a": ImpactNone, "c/b": ImpactDown, "c/c": ImpactDown, "c/d": ImpactDegraded, "c/e": ImpactNone,
		"c/voorstel": ImpactNone, "c/f": ImpactNone, "c/g": ImpactNone, "c/x": ImpactNone, "c/y": ImpactDown,
	}
	for name, w := range want {
		if got := g.Effect(b.id(name)).Impact; got != w {
			t.Errorf("%s: %s, wil %s", name, got, w)
		}
	}
	if p := g.Effect(b.id("c/c")).Path; len(p) != 3 || p[0] != b.id("c/a") || p[1] != b.id("c/b") {
		t.Fatalf("pad van c: %v", p)
	}
	// y hangt van x af, dat zelf down is: x is de oorzaak, niet a.
	if e := g.Effect(b.id("c/y")); e.Cause != b.id("c/x") {
		t.Fatalf("oorzaak van y: %s", b.name(e.Cause))
	}
}

func TestImpactOfNode(t *testing.T) {
	b := voorbeeld(t)
	b.cluster("solo", "lab", status.Healthy)
	b.node("solo01", "solo", "redis-server=active")
	b.svc("solo", "redis", "cache", "redis-server")
	b.svc("solo", "site", "web", "")
	b.node("losse01", "")
	b.svc("node:losse01", "backup", "app", "")
	b.dep("web-prod/php8.2-fpm", "solo/redis", Soft)
	b.dep("node:losse01/backup", "solo/site", Hard)
	g := b.build()

	got := b.hits(g.Impact(Target{Node: b.ptr("web01")}))
	want := strings.Join([]string{
		"web-prod/keepalived degraded: VIP 10.0.20.10 verhuist naar web02",
		"web-prod/nginx degraded: draait nog op web02",
		"web-prod/php8.2-fpm degraded: draait nog op web02",
	}, "\n")
	if got != want {
		t.Fatalf("web01:\n%s\nwil\n%s", got, want)
	}
	got = b.hits(g.Impact(Target{Node: b.ptr("web02")}))
	if !strings.Contains(got, "web-prod/keepalived degraded: minder reserve: keepalived draait nog op web01") {
		t.Fatalf("web02, geen eigenaar:\n%s", got)
	}
	got = b.hits(g.Impact(Target{Node: b.ptr("solo01")}))
	want = strings.Join([]string{
		"web-prod/php8.2-fpm degraded: hangt zacht af van redis in solo · handmatig",
		"solo/redis down: redis-server draait op geen andere node",
		"solo/site down: solo01 is de enige actieve node van solo",
		"losse01/backup down: via site",
	}, "\n")
	got = strings.ReplaceAll(got, "node:losse01", "losse01")
	if got != strings.Replace(want, "losse01/backup down: via site", "losse01/backup down: hangt hard af van site in solo · handmatig", 1) {
		t.Fatalf("solo01:\n%s", got)
	}
	got = strings.ReplaceAll(b.hits(g.Impact(Target{Node: b.ptr("losse01")})), "node:losse01", "losse01")
	if got != "losse01/backup down: draait op losse01" {
		t.Fatalf("losse01:\n%s", got)
	}
}

func TestImpactOfCluster(t *testing.T) {
	b := voorbeeld(t)
	g := b.build()
	got := b.hits(g.Impact(Target{Cluster: b.ptr("db-prod")}))
	want := strings.Join([]string{
		"db-prod/mariadb down: valt uit met het cluster",
		"web-prod/nginx down: via php8.2-fpm",
		"web-prod/php8.2-fpm down: hangt hard af van mariadb in db-prod · handmatig",
		"app-test/app degraded: hangt zacht af van mariadb in db-prod · handmatig",
	}, "\n")
	if got != want {
		t.Fatalf("db-prod:\n%s", got)
	}
}

func TestSelectAndCollapse(t *testing.T) {
	b := voorbeeld(t)
	b.cluster("los", "lab", status.Healthy)
	b.svc("los", "iets", "app", "")
	b.svcState("web-prod", "redis-server", "cache", "redis-server", StateSuggested)
	b.svcState("web-prod", "oud", "cache", "memcached", StateIgnored)
	b.svc("extern", "nfs", "storage", "")
	b.dep("db-prod/mariadb", "extern/nfs", Soft)
	b.dep("web-prod/nginx", "db-prod/mariadb", Soft)
	g := b.build()
	names := func(ss []Svc) string {
		var out []string
		for _, s := range ss {
			out = append(out, b.name(s.ID))
		}
		return strings.Join(out, " ")
	}

	svcs, ds := g.Select(Filter{ClusterID: b.ptr("web-prod")})
	if got := names(svcs); got != "db-prod/mariadb web-prod/nginx web-prod/keepalived web-prod/php8.2-fpm" {
		t.Fatalf("web-prod met buren: %s", got)
	}
	if len(ds) != 4 {
		t.Fatalf("pijlen: %d", len(ds))
	}
	svcs, _ = g.Select(Filter{ClusterID: b.ptr("web-prod"), Suggested: true, Ignored: true})
	if got := names(svcs); !strings.Contains(got, "web-prod/redis-server") || !strings.Contains(got, "web-prod/oud") {
		t.Fatalf("met voorstellen en genegeerde: %s", got)
	}
	svcs, _ = g.Select(Filter{Environment: "test"})
	if got := names(svcs); got != "db-prod/mariadb app-test/app" {
		t.Fatalf("test met buren: %s", got)
	}
	svcs, ds = g.Select(Filter{})
	if strings.Contains(names(svcs), "redis-server") || strings.Contains(names(svcs), "oud") || len(svcs) != 7 || len(ds) != 6 {
		t.Fatalf("alles: %s, %d pijlen", names(svcs), len(ds))
	}

	edges := g.Collapse(ds)
	web, db := "cluster:"+b.id("web-prod").String(), "cluster:"+b.id("db-prod").String()
	var found bool
	for _, e := range edges {
		if e.From == e.To {
			t.Fatalf("pijl binnen een groep: %+v", e)
		}
		if e.From == web && e.To == db {
			found = true
			if e.Count != 2 || e.Strength != Hard {
				t.Fatalf("web-prod naar db-prod: %+v", e)
			}
		}
		if e.To == ExternalKey && (e.From != db || e.Strength != Soft) {
			t.Fatalf("naar extern: %+v", e)
		}
	}
	if !found || len(edges) != 3 {
		t.Fatalf("samengevoegd: %+v", edges)
	}
}
