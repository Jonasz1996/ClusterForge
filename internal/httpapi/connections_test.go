package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// procSockets schrijft /proc/net/tcp en per proces zijn unit en sockets:
// procs geeft per pid de unit en de inodes.
func procSockets(t *testing.T, root string, lines []string, procs map[int]struct {
	unit   string
	inodes []int
}) {
	t.Helper()
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	for i, l := range lines {
		tcp += fmt.Sprintf("   %d: %s\n", i, l)
	}
	writeFile(t, filepath.Join(root, "proc/net/tcp"), tcp, 0o644)
	writeFile(t, filepath.Join(root, "proc/sys/net/ipv4/ip_local_port_range"), "32768\t60999\n", 0o644)
	for pid, p := range procs {
		writeFile(t, filepath.Join(root, fmt.Sprintf("proc/%d/cgroup", pid)), "0::/system.slice/"+p.unit+".service\n", 0o644)
		for i, inode := range p.inodes {
			fd := filepath.Join(root, fmt.Sprintf("proc/%d/fd/%d", pid, 10+i))
			if err := os.MkdirAll(filepath.Dir(fd), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(fmt.Sprintf("socket:[%d]", inode), fd); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// sock is een regel uit /proc/net/tcp.
func sock(local, remote, state string, inode int) string {
	return fmt.Sprintf("%s %s %s 00000000:00000000 00:00000000 00000000     0        0 %d 1 0 100 0 0 10 0", local, remote, state, inode)
}

type observedView struct {
	Count int64  `json:"count"`
	Text  string `json:"text"`
	Lines []struct {
		Hostname string `json:"hostname"`
		Unit     string `json:"unit"`
		Addr     string `json:"addr"`
		Port     int    `json:"port"`
		Count    int64  `json:"count"`
	} `json:"lines"`
}

type connGraph struct {
	Services []depSvcView `json:"services"`
	Groups   []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"groups"`
	Edges []struct {
		ID       string        `json:"id"`
		From     string        `json:"from"`
		To       string        `json:"to"`
		Source   string        `json:"source"`
		State    string        `json:"state"`
		Observed *observedView `json:"observed"`
	} `json:"edges"`
	Suggestions           int `json:"suggestions"`
	SuggestedDependencies int `json:"suggested_dependencies"`
}

func (g connGraph) name(id string) string {
	for _, s := range g.Services {
		if s.ID == id {
			for _, gr := range g.Groups {
				if gr.ID == s.GroupID {
					return gr.Name + "/" + s.Name
				}
			}
			return s.Name
		}
	}
	return "?"
}

// arrows geeft de pijlen als "van -> naar staat bron".
func (g connGraph) arrows() []string {
	var out []string
	for _, e := range g.Edges {
		out = append(out, g.name(e.From)+" -> "+g.name(e.To)+" "+e.State+" "+e.Source)
	}
	slices.Sort(out)
	return out
}

// TestConnections is de acceptatie van mijlpaal 8 van fase 3: php8.2-fpm op
// een node van web-test praat met het VIP van db-test op 3306. Dat wordt een
// voorgestelde afhankelijkheid php8.2-fpm → mariadb met het aantal
// verbindingen als bewijs. Bevestigen maakt haar echt, negeren laat een
// grafsteen, en NATS en de webinterface van ClusterForge worden nooit
// voorgesteld.
func TestConnections(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var web, db cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web-test", "name": "web-test", "type": "nginx", "environment": "test"}, &web)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db-test", "name": "db-test", "type": "mariadb_ha", "environment": "test"}, &db)
	if s := c.do("POST", "/api/v1/clusters/"+db.ID+"/vips", map[string]any{"address": "10.0.30.100"}, nil); s != http.StatusCreated {
		t.Fatalf("VIP: %d", s)
	}
	type proc = struct {
		unit   string
		inodes []int
	}
	// web01 (10.0.20.11): php8.2-fpm naar het VIP en naar db02 op 3306 en
	// naar 203.0.113.7:443, nginx naar de webinterface van ClusterForge,
	// cf-agent naar NATS.
	webSockets := []string{
		sock("0B14000A:D431", "641E000A:0CEA", "01", 2001),
		sock("0B14000A:D432", "641E000A:0CEA", "01", 2002),
		sock("0B14000A:D433", "0C1E000A:0CEA", "01", 2003),
		sock("0B14000A:D434", "077100CB:01BB", "01", 2004),
		sock("0B14000A:D435", "050200C0:1F90", "01", 2005),
		sock("0B14000A:D436", "050200C0:107E", "01", 2006),
	}
	webProcs := map[int]proc{300: {"php8.2-fpm", []int{2001, 2002, 2003, 2004}}, 100: {"nginx", []int{2005}}, 500: {"cf-agent", []int{2006}}}
	// db01 en db02: mariadb luistert op 3306, zonder poort in de dienst.
	dbSockets := []string{sock("00000000:0CEA", "00000000:0000", "0A", 3001)}
	dbProcs := map[int]proc{400: {"mariadb", []int{3001}}}

	hosts := []struct {
		name, cluster, addr string
		units               []string
		sockets             []string
		procs               map[int]proc
	}{
		{"web01", web.ID, "10.0.20.11", []string{"php8.2-fpm=active", "nginx=active"}, webSockets, webProcs},
		{"db01", db.ID, "10.0.30.11", []string{"mariadb=active"}, dbSockets, dbProcs},
		{"db02", db.ID, "10.0.30.12", []string{"mariadb=active"}, dbSockets, dbProcs},
	}
	ids := map[string]string{}
	for i, h := range hosts {
		var n node
		c.do("POST", "/api/v1/nodes", map[string]any{"hostname": h.name, "cluster_id": h.cluster}, &n)
		ids[h.name] = n.ID
		sim := newUnitSim(h.units...)
		startAgentWith(t, e, c, n.ID, fmt.Sprintf("%032x", i+81), &addrs{list: []string{h.addr}}, func(ag *agent.Agent, root string) {
			ag.Collector.Run = sim.run
			ag.ConnsSample, ag.ConnsInterval = 50*time.Millisecond, 200*time.Millisecond
			procSockets(t, root, h.sockets, h.procs)
		})
	}
	eventually(t, "de verbindingen van web01 en de luisterende poort van db01", func() bool {
		var conns, listeners int
		_ = e.pool.QueryRow(ctx, `SELECT count(DISTINCT (unit, peer_addr, peer_port)) FROM observed_connection_hours
			WHERE node_id = $1 AND direction = 'out'`, ids["web01"]).Scan(&conns)
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM node_facts WHERE node_id = $1 AND facts -> 'listeners' @> '[{"port": 3306, "unit": "mariadb"}]'`,
			ids["db01"]).Scan(&listeners)
		return conns == 5 && listeners == 1
	})

	// Eén ronde: de diensten uit de units en de pijl uit de verbindingen.
	// Het onbekende adres is nog geen uur oud; ClusterForge en de agent
	// tellen nooit.
	if err := e.deps.Resolve(ctx); err != nil {
		t.Fatal(err)
	}
	var g connGraph
	if s := c.do("GET", "/api/v1/dependency-graph?include_suggested=true&include_connections=true", nil, &g); s != 200 {
		t.Fatalf("graaf: %d", s)
	}
	if got := g.arrows(); !slices.Equal(got, []string{"web-test/php8.2-fpm -> db-test/mariadb suggested discovered"}) || g.SuggestedDependencies != 1 {
		t.Fatalf("voorstellen: %q %d", got, g.SuggestedDependencies)
	}
	obs := g.Edges[0].Observed
	if obs == nil || obs.Count < 3 || len(obs.Lines) != 2 ||
		!strings.HasPrefix(obs.Text, "php8.2-fpm op web01 → mariadb in db-test: ") ||
		!strings.HasSuffix(obs.Text, " verbindingen gezien naar 10.0.30.100:3306 en 10.0.30.12:3306 in de afgelopen 24 uur") {
		t.Fatalf("bewijs: %+v", obs)
	}
	if obs.Lines[0].Addr != "10.0.30.100" || obs.Lines[0].Unit != "php8.2-fpm" || obs.Lines[0].Hostname != "web01" {
		t.Fatalf("eerste regel: %+v", obs.Lines[0])
	}
	// Zonder include_connections geen bewijs; de teller telt ook zonder
	// voorstellen te tonen.
	var plain connGraph
	c.do("GET", "/api/v1/dependency-graph", nil, &plain)
	if len(plain.Edges) != 0 || plain.SuggestedDependencies != 1 {
		t.Fatalf("zonder voorstellen: %+v", plain)
	}
	c.do("GET", "/api/v1/dependency-graph?include_suggested=true", nil, &plain)
	if len(plain.Edges) != 1 || plain.Edges[0].Observed != nil {
		t.Fatalf("zonder bewijs: %+v", plain.Edges)
	}
	var payload map[string]any
	var raw []byte
	if err := e.pool.QueryRow(ctx, `SELECT payload FROM events WHERE action = 'dependency.created'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &payload)
	if payload["source"] != "discovered" || !strings.HasPrefix(fmt.Sprint(payload["evidence"]), "php8.2-fpm op web01 → mariadb in db-test: ") {
		t.Fatalf("event: %v", payload)
	}

	// De assistent ziet hetzelfde bewijs in de tool kaart.
	for _, tool := range e.asst.Tools {
		if tool.Name != "kaart" {
			continue
		}
		u, err := store.New(e.pool).GetUserByUsername(ctx, "admin")
		if err != nil {
			t.Fatal(err)
		}
		res, err := tool.Run(ctx, assistant.Caller{Principal: auth.Principal{User: u}}, json.RawMessage(`{"laag":"verbindingen","cluster":"web-test"}`))
		b, _ := json.Marshal(res)
		if err != nil || !strings.Contains(string(b), `"staat":"voorstel"`) || !strings.Contains(string(b), "php8.2-fpm op web01 → mariadb in db-test: ") {
			t.Fatalf("tool kaart: %v %s", err, b)
		}
	}

	// Komt 203.0.113.7:443 al twee uur terug, dan wordt het een voorgestelde
	// externe dienst met een pijl.
	if _, err := e.pool.Exec(ctx, `UPDATE observed_connections SET first_seen = first_seen - interval '2 hours' WHERE peer_addr = '203.0.113.7'`); err != nil {
		t.Fatal(err)
	}
	if err := e.deps.Resolve(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/dependency-graph?include_suggested=true&include_connections=true", nil, &g)
	want := []string{"web-test/php8.2-fpm -> Extern/203.0.113.7:443 suggested discovered", "web-test/php8.2-fpm -> db-test/mariadb suggested discovered"}
	if got := g.arrows(); !slices.Equal(got, want) {
		t.Fatalf("met het externe adres: %q", got)
	}
	var ext depSvcView
	for _, s := range g.Services {
		if s.Name == "203.0.113.7:443" {
			ext = s
		}
	}
	if ext.Kind != "web" || ext.Source != "discovered" || ext.State != "suggested" {
		t.Fatalf("externe dienst: %+v", ext)
	}
	for _, s := range g.Services {
		if strings.Contains(s.Name, "192.0.2.5") {
			t.Fatalf("ClusterForge voorgesteld: %+v", s)
		}
	}

	// De verbindingen van web01, alleen voor een admin.
	type connsView struct {
		Supported bool `json:"supported"`
		Out       []struct {
			Unit            string  `json:"unit"`
			Addr            string  `json:"addr"`
			Port            int     `json:"port"`
			Count           int64   `json:"count"`
			Dest            string  `json:"dest"`
			DestLabel       string  `json:"dest_label"`
			DependencyState *string `json:"dependency_state"`
		} `json:"out"`
		In []struct {
			Unit         string  `json:"unit"`
			Port         int     `json:"port"`
			PeerNodeID   *string `json:"peer_node_id"`
			PeerHostname string  `json:"peer_hostname"`
		} `json:"in"`
	}
	var cv connsView
	if s := c.do("GET", "/api/v1/nodes/"+ids["web01"]+"/connections", nil, &cv); s != 200 || !cv.Supported || len(cv.In) != 0 {
		t.Fatalf("verbindingen: %d %+v", s, cv)
	}
	var lines []string
	for _, o := range cv.Out {
		st := ""
		if o.DependencyState != nil {
			st = " " + *o.DependencyState
		}
		lines = append(lines, fmt.Sprintf("%s %s:%d %s %s%s", o.Unit, o.Addr, o.Port, o.Dest, o.DestLabel, st))
		if o.Count < 1 {
			t.Errorf("geen telling: %+v", o)
		}
	}
	slices.Sort(lines)
	want = []string{
		"cf-agent 192.0.2.5:4222 clusterforge ClusterForge",
		"nginx 192.0.2.5:8080 clusterforge ClusterForge",
		"php8.2-fpm 10.0.30.100:3306 service mariadb in db-test suggested",
		"php8.2-fpm 10.0.30.12:3306 service mariadb in db-test suggested",
		"php8.2-fpm 203.0.113.7:443 service 203.0.113.7:443 (extern) suggested",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("uitgaand:\n%s", strings.Join(lines, "\n"))
	}
	if s := c.do("GET", "/api/v1/nodes/00000000-0000-0000-0000-000000000000/connections", nil, nil); s != http.StatusNotFound {
		t.Fatalf("onbekende node: %d", s)
	}
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	viewer.login("kijker", "een-lang-wachtwoord", "")
	if s := viewer.do("GET", "/api/v1/nodes/"+ids["web01"]+"/connections", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer leest verbindingen: %d", s)
	}

	edge := func(to string) string {
		for _, ed := range g.Edges {
			if g.name(ed.To) == to {
				return ed.ID
			}
		}
		t.Fatalf("geen pijl naar %s", to)
		return ""
	}
	toDB, toExt := edge("db-test/mariadb"), edge("Extern/203.0.113.7:443")
	if s := viewer.do("PATCH", "/api/v1/dependencies/"+toDB, map[string]any{"state": "confirmed"}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer bevestigt: %d", s)
	}

	// Bevestigen maakt de pijl echt, en ook php8.2-fpm en mariadb.
	if s := c.do("PATCH", "/api/v1/dependencies/"+toDB, map[string]any{"state": "confirmed"}, nil); s != 200 {
		t.Fatalf("bevestigen: %d", s)
	}
	c.do("GET", "/api/v1/dependency-graph", nil, &g)
	if got := g.arrows(); !slices.Equal(got, []string{"web-test/php8.2-fpm -> db-test/mariadb confirmed discovered"}) || g.SuggestedDependencies != 1 {
		t.Fatalf("na bevestigen: %q %d", got, g.SuggestedDependencies)
	}
	// Negeren laat een grafsteen: een nieuwe ronde stelt niets opnieuw voor.
	if s := c.do("PATCH", "/api/v1/dependencies/"+toExt, map[string]any{"state": "ignored"}, nil); s != 200 {
		t.Fatalf("negeren: %d", s)
	}
	if err := e.deps.Resolve(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/dependency-graph?include_suggested=true&include_ignored=true", nil, &g)
	want = []string{"web-test/php8.2-fpm -> Extern/203.0.113.7:443 ignored discovered", "web-test/php8.2-fpm -> db-test/mariadb confirmed discovered"}
	if got := g.arrows(); !slices.Equal(got, want) || g.SuggestedDependencies != 0 {
		t.Fatalf("na negeren: %q %d", got, g.SuggestedDependencies)
	}
	// Terugzetten als voorstel kan alleen bij een ontdekte pijl.
	if s := c.do("PATCH", "/api/v1/dependencies/"+toExt, map[string]any{"state": "suggested"}, nil); s != 200 {
		t.Fatalf("terugzetten: %d", s)
	}
	var nginx, manual depSvcView
	for _, s := range g.Services {
		if s.Name == "nginx" {
			nginx = s
		}
	}
	var manualEdge struct {
		ID string `json:"id"`
	}
	if s := c.do("POST", "/api/v1/services", map[string]any{"cluster_id": web.ID, "name": "app", "kind": "app"}, &manual); s != 201 {
		t.Fatalf("app: %d", s)
	}
	c.do("POST", "/api/v1/dependencies", map[string]any{"from_service_id": nginx.ID, "to_service_id": manual.ID}, &manualEdge)
	var er deployErr
	if s := c.do("PATCH", "/api/v1/dependencies/"+manualEdge.ID, map[string]any{"state": "suggested"}, &er); s != 400 || er.Field == nil || *er.Field != "state" {
		t.Fatalf("handmatige pijl als voorstel: %d %+v", s, er)
	}
	// Een genegeerde dienst aan het eind houdt bevestigen tegen.
	c.do("PATCH", "/api/v1/services/"+ext.ID, map[string]any{"state": "ignored"}, nil)
	if s := c.do("PATCH", "/api/v1/dependencies/"+toExt, map[string]any{"state": "confirmed"}, nil); s != http.StatusConflict {
		t.Fatalf("bevestigen naar een genegeerde dienst: %d", s)
	}

	// Het logboek.
	var texts []string
	rows, err := e.pool.Query(ctx, `SELECT action FROM events WHERE subject_type IN ('dependency', 'service') ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		texts = append(texts, a)
	}
	rows.Close()
	if n := strings.Count(strings.Join(texts, " "), "dependency.created"); n != 3 {
		t.Fatalf("dependency.created: %d in %v", n, texts)
	}
}
