package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// patroniHosts speelt Debian 13-nodes na met etcd en Patroni: de eerste
// Patroni die start, maakt het cluster en is de primary, de volgende worden
// replica's die bijhouden, en de eerste replica is synchroon.
type patroniHosts struct {
	mu      sync.Mutex
	hosts   map[string]*agenttest.Host
	primary string
	sync    string
	// params zijn de instellingen van PostgreSQL in etcd; restarts de
	// nodes die Patroni herstartte, op volgorde.
	params   map[string]any
	restarts []string
}

func (g *patroniHosts) after(name string, h *agenttest.Host) func(call string, err error) {
	return func(call string, err error) {
		if err != nil {
			return
		}
		switch {
		case call == "sh -c /usr/local/lib/clusterforge/install-etcd.sh":
			touchFile(h, "/usr/local/lib/clusterforge/etcd-v3.5.34")
		case strings.HasPrefix(call, "sh -c systemctl enable etcd && systemctl start --no-block etcd && touch "):
			h.SetUnit("etcd", agenttest.Unit{Enabled: true, Active: true})
			touchFile(h, components.EtcdMarker("nextcloud"))
		case call == "systemctl start patroni":
			g.start(name, h)
		case call == "systemctl reload-or-restart patroni":
			// Een nieuwe configuratie, zoals shared_buffers: Patroni leest
			// haar, en PostgreSQL wacht op een herstart.
			h.Locked(func() {
				if h.Patroni != nil {
					h.Patroni.PendingRestart = true
				}
			})
		case strings.HasPrefix(call, "systemctl reboot"):
			g.reboot(h)
		}
	}
}

// reboot herstart een replica: PostgreSQL is even weg, de uptime begint
// opnieuw, en na het opstarten houdt de replica weer bij.
func (g *patroniHosts) reboot(h *agenttest.Host) {
	h.Locked(func() { h.Patroni.State, h.Patroni.Replication = "stopped", "" })
	_ = os.WriteFile(filepath.Join(h.Root, "proc/uptime"), []byte("1.00 1.00\n"), 0o644)
	time.AfterFunc(300*time.Millisecond, func() {
		h.Locked(func() { h.Patroni.State, h.Patroni.Replication, h.Postgres.Uptime = "running", "streaming", 0 })
	})
}

// api speelt de acties van Patroni na: een switchover kan alleen van de
// primary naar de synchrone replica, opnieuw opbouwen niet op de primary.
func (g *patroniHosts) api(name string) agenttest.PatroniAPI {
	return func(method, path string, body []byte) (int, string) {
		switch {
		case method == http.MethodPost && path == "/switchover":
			var in struct{ Leader, Candidate string }
			_ = json.Unmarshal(body, &in)
			g.mu.Lock()
			defer g.mu.Unlock()
			switch {
			case in.Leader != g.primary:
				return http.StatusPreconditionFailed, "leader name does not match"
			case in.Candidate != g.sync:
				return http.StatusPreconditionFailed, "candidate name does not match with sync_standby"
			}
			// Eerst de oude primary weg, dan de nieuwe: nooit twee tegelijk.
			old, next := g.hosts[in.Leader], g.hosts[in.Candidate]
			old.Locked(func() {
				old.Patroni.Role, old.Patroni.Replication, old.Patroni.Sync = "replica", "streaming", true
				old.Postgres.Recovery = true
			})
			next.Locked(func() {
				next.Patroni.Role, next.Patroni.Replication, next.Patroni.Sync = "primary", "", false
				next.Postgres.Recovery = false
			})
			for _, h := range g.hosts {
				h.Locked(func() {
					if h.Patroni != nil {
						h.Patroni.Timeline++
					}
				})
			}
			g.primary, g.sync = in.Candidate, in.Leader
			return http.StatusOK, `Successfully switched over to "` + in.Candidate + `"`
		case method == http.MethodPost && path == "/reinitialize":
			g.mu.Lock()
			primary := g.primary
			g.mu.Unlock()
			if name == primary {
				return http.StatusServiceUnavailable, "I am the leader, can not reinitialize"
			}
			h := g.hosts[name]
			h.Locked(func() { h.Patroni.State, h.Patroni.Replication = "creating replica", "" })
			time.AfterFunc(200*time.Millisecond, func() {
				h.Locked(func() { h.Patroni.State, h.Patroni.Replication, h.Postgres.Uptime = "running", "streaming", 0 })
			})
			return http.StatusOK, "reinitialize started"
		case method == http.MethodPost && path == "/restart":
			// Patroni antwoordt pas als PostgreSQL opnieuw draait, met de
			// instellingen uit etcd.
			g.mu.Lock()
			g.restarts = append(g.restarts, name)
			maxConn := 100
			if v, ok := g.params["max_connections"]; ok {
				maxConn = atoiAny(v)
			}
			g.mu.Unlock()
			h := g.hosts[name]
			h.Locked(func() { h.Patroni.PendingRestart, h.Postgres.Uptime, h.Postgres.MaxConnections = false, 0, maxConn })
			return http.StatusOK, "restarted successfully"
		case path == "/config":
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.params == nil {
				g.params = map[string]any{"max_connections": 100}
			}
			if method == http.MethodPatch {
				var in struct {
					PostgreSQL struct {
						Parameters map[string]any `json:"parameters"`
					} `json:"postgresql"`
				}
				_ = json.Unmarshal(body, &in)
				changed := false
				for k, v := range in.PostgreSQL.Parameters {
					changed = changed || fmt.Sprint(g.params[k]) != fmt.Sprint(v)
				}
				if changed {
					for _, h := range g.hosts {
						h.Locked(func() {
							if h.Patroni != nil {
								h.Patroni.PendingRestart = true
							}
						})
					}
				}
				maps.Copy(g.params, in.PostgreSQL.Parameters)
			}
			b, _ := json.Marshal(map[string]any{"postgresql": map[string]any{"parameters": g.params}})
			return http.StatusOK, string(b)
		}
		return http.StatusNotFound, "not found"
	}
}

// atoiAny leest een getal uit JSON, als getal of als tekst.
func atoiAny(v any) int {
	n, _ := strconv.Atoi(fmt.Sprint(v))
	return n
}

func touchFile(h *agenttest.Host, p string) {
	p = filepath.Join(h.Root, p)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, nil, 0o644)
}

// start laat Patroni op een node meedoen.
func (g *patroniHosts) start(name string, h *agenttest.Host) {
	g.mu.Lock()
	role, repl, sync := "replica", "streaming", false
	if g.primary == "" {
		g.primary, role, repl = name, "primary", ""
	} else if g.sync == "" {
		g.sync, sync = name, true
	}
	g.mu.Unlock()
	h.SetUnit("patroni", agenttest.Unit{Enabled: true, Active: true})
	h.Locked(func() {
		h.Patroni = &agenttest.Patroni{Scope: "nextcloud", Role: role, State: "running", Timeline: 1, Replication: repl, Sync: sync, Name: name}
		h.PatroniAPI = g.api(name)
		h.Postgres = &agenttest.Postgres{Version: "17.6 (Debian 17.6-1.pgdg13+1)", Recovery: role == "replica", MaxConnections: 100, Uptime: 5,
			Databases: []agenttest.Database{{Name: "postgres", Size: 7 << 20}}, Roles: agenttest.PatroniRoles()}
	})
}

// replicate speelt de replicatie na: de andere nodes met PostgreSQL krijgen
// de gebruikers en databases van from.
func (g *patroniHosts) replicate(from string) {
	src := g.hosts[from]
	var roles []agenttest.PGRole
	var dbs []agenttest.Database
	src.Locked(func() { roles, dbs = slices.Clone(src.Postgres.Roles), slices.Clone(src.Postgres.Databases) })
	for name, h := range g.hosts {
		if name != from {
			h.Locked(func() {
				if h.Postgres != nil {
					h.Postgres.Roles, h.Postgres.Databases = slices.Clone(roles), slices.Clone(dbs)
				}
			})
		}
	}
}

// failover laat de primary hard uitvallen en maakt to de nieuwe primary,
// zoals Patroni na de ttl doet.
func (g *patroniHosts) failover(to string) {
	g.mu.Lock()
	old := g.hosts[g.primary]
	g.primary, g.sync = to, ""
	g.mu.Unlock()
	old.SetUnit("patroni", agenttest.Unit{Enabled: true, Failed: true})
	old.Locked(func() { old.Patroni, old.Postgres = nil, nil })
	h := g.hosts[to]
	h.Locked(func() {
		h.Patroni.Role, h.Patroni.Timeline, h.Patroni.Replication, h.Patroni.Sync = "primary", 2, "", false
		h.Postgres.Recovery = false
	})
}

type componentOwnVIPPlan struct {
	componentPlanView
	VIP *struct {
		Address string `json:"address"`
		Own     bool   `json:"own"`
		Vrid    int    `json:"vrid"`
	} `json:"vip"`
}

// patroniLab is Nextcloud op nc1 tot nc3 (10.0.20.186-188) met agents die
// Patroni naspelen. nc1 en nc2 hebben het VIP van de app al in keepalived,
// nc3 nog geen keepalived. Extra nodes, zoals nc4, zijn als nc3.
type patroniLab struct {
	e   *testEnv
	c   *client
	nc  cluster
	g   *patroniHosts
	ids map[string]string
}

func newPatroniHosts(t *testing.T, extra ...string) patroniLab {
	t.Helper()
	e, c := adminClient(t)
	ctx := context.Background()
	group := &agenttest.Group{Takeover: 200 * time.Millisecond}

	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "lab"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	g := &patroniHosts{hosts: map[string]*agenttest.Host{}}
	ids := map[string]string{}
	names := append([]string{"nc1", "nc2", "nc3"}, extra...)
	for i, name := range names {
		ip := fmt.Sprintf("10.0.20.%d", 186+i)
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": nc.ID, "primary_ip": ip}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
			writeFile(t, filepath.Join(root, "proc/meminfo"), "MemTotal: 4194304 kB\nMemAvailable: 2097152 kB\n", 0o644)
			// nc1 en nc2 hebben het VIP van de app al in keepalived; nc3 heeft
			// nog geen keepalived.
			h := agenttest.NewHost(root, ip)
			if name == "nc1" || name == "nc2" {
				writeFile(t, filepath.Join(root, "etc/keepalived/keepalived.conf"), ncKeepalived(name, 150-10*i), 0o640)
				if _, err := h.Exec(ctx, "apt-get", "install", "-y", "keepalived"); err != nil {
					t.Fatal(err)
				}
			}
			h.SetUnit("apache2", agenttest.Unit{Enabled: true, Active: true})
			g.hosts[name] = h
			h.SetAfter(g.after(name, h))
			ag.Exec, ag.Root, ag.Collector.Run, ag.Collector.Addresses = h.Exec, root, h.Exec, h.Addresses
			ag.ExecStdin, ag.Stream, ag.DumpDir = h.ExecStdin, h.Stream, filepath.Join(root, "var/backups/clusterforge-db")
			ag.Collector.Interfaces, ag.Collector.PrimaryAddress = h.Interfaces, func() string { return ip }
			ag.PatroniURL, ag.Dial = h.ServePatroni(t), h.Dial
			group.Join(h)
		})
	}
	// PostgreSQL op de andere nodes is bereikbaar op poort 5432.
	network := func(addr string) *agenttest.Host {
		for i, name := range names {
			if addr == fmt.Sprintf("10.0.20.%d:5432", 186+i) {
				return g.hosts[name]
			}
		}
		return nil
	}
	for _, h := range g.hosts {
		h.Locked(func() { h.Network = network })
	}
	eventually(t, "facts van de nodes", func() bool {
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM node_facts f JOIN nodes n ON n.id = f.node_id WHERE n.cluster_id = $1 AND f.facts->'os'->>'id' = 'debian'", nc.ID).Scan(&n)
		return n == len(names)
	})
	return patroniLab{e: e, c: c, nc: nc, g: g, ids: ids}
}

// deploy rolt PostgreSQL met Patroni uit op de drie nodes, met het eigen
// VIP 10.0.20.190, en geeft het id van het onderdeel. nc1 is de primary.
func (l patroniLab) deploy(t *testing.T) string {
	t.Helper()
	base := "/api/v1/clusters/" + l.nc.ID + "/components"
	in := map[string]any{"engine": "postgresql", "nodes": []string{l.ids["nc1"], l.ids["nc2"], l.ids["nc3"]}, "vip_address": "10.0.20.190"}
	var p componentPlanView
	if s := l.c.do("POST", base+"/plan", in, &p); s != http.StatusOK || p.Blocked {
		t.Fatalf("plan: %d %+v", s, p)
	}
	in["hash"] = p.Hash
	var j job
	if s := l.c.do("POST", base, in, &j); s != http.StatusAccepted {
		t.Fatalf("toevoegen: %d", s)
	}
	if j = waitJobLong(t, l.c, j.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	var comps list[componentView]
	l.c.do("GET", base, nil, &comps)
	return comps.Items[0].ID
}

// TestClusterComponentPatroni is de acceptatie van het eerste deel van
// mijlpaal 10 van fase 5: Nextcloud krijgt PostgreSQL met Patroni op nc1 tot
// nc3, met een eigen VIP dat de primary volgt. De uitrol zet eerst etcd op
// alle nodes en dan Patroni node voor node; het VIP staat daarna op de
// primary, en gaat mee als de primary uitvalt.
func TestClusterComponentPatroni(t *testing.T) {
	lab := newPatroniHosts(t)
	e, c, g, nc, ids := lab.e, lab.c, lab.g, lab.nc, lab.ids
	ctx := context.Background()

	// PostgreSQL staat nu bij de onderdelen, zonder het VIP als instelling.
	var tpls list[struct {
		Engine string `json:"engine"`
		Title  string `json:"title"`
		Params []struct {
			Name string `json:"name"`
		} `json:"params"`
	}]
	c.do("GET", "/api/v1/component-templates", nil, &tpls)
	if len(tpls.Items) != 2 || tpls.Items[1].Engine != "postgresql" || tpls.Items[1].Title != "PostgreSQL met Patroni" {
		t.Fatalf("onderdelen: %+v", tpls)
	}
	var params []string
	for _, p := range tpls.Items[1].Params {
		params = append(params, p.Name)
	}
	if !slices.Equal(params, []string{"shared_buffers", "max_connections", "replication_password", "rewind_password", "rest_password"}) {
		t.Fatalf("instellingen van PostgreSQL: %v", params)
	}

	base := "/api/v1/clusters/" + nc.ID + "/components"
	nodes := []string{ids["nc1"], ids["nc2"], ids["nc3"]}
	plan := func(body map[string]any) componentOwnVIPPlan {
		t.Helper()
		var p componentOwnVIPPlan
		if s := c.do("POST", base+"/plan", body, &p); s != http.StatusOK {
			t.Fatalf("plan: %d", s)
		}
		return p
	}

	// Zonder eigen VIP kan het niet, met het VIP van de app ook niet, en een
	// adres moet vrij zijn en in het netwerk van de nodes liggen.
	if p := plan(map[string]any{"engine": "postgresql", "nodes": nodes}); !p.Blocked || !slices.Contains(p.codes(), "blocked:own_vip_required") {
		t.Fatalf("zonder VIP: %+v", p)
	}
	var vip struct {
		ID string `json:"id"`
	}
	c.do("POST", "/api/v1/clusters/"+nc.ID+"/vips", map[string]any{"address": ncVIP}, &vip)
	var ae apiErr
	if s := c.do("POST", base+"/plan", map[string]any{"engine": "postgresql", "nodes": nodes, "vip_id": vip.ID}, &ae); s != http.StatusBadRequest ||
		!strings.Contains(ae.Message, "eigen VIP") {
		t.Fatalf("VIP van de app: %d %+v", s, ae)
	}
	if s := c.do("POST", base+"/plan", map[string]any{"engine": "postgresql", "nodes": nodes, "vip_address": "nc-db"}, &ae); s != http.StatusBadRequest {
		t.Fatalf("geen adres: %d %+v", s, ae)
	}
	if s := c.do("POST", base+"/plan", map[string]any{"engine": "postgresql", "nodes": nodes, "vip_address": "10.0.20.190",
		"settings": map[string]any{"vip": "10.0.20.191"}}, &ae); s != http.StatusBadRequest {
		t.Fatalf("VIP als instelling: %d %+v", s, ae)
	}
	for addr, code := range map[string]string{ncVIP: "blocked:vip_in_use", "10.0.20.187": "blocked:vip_in_use", "10.0.30.5": "blocked:vip_subnet"} {
		if p := plan(map[string]any{"engine": "postgresql", "nodes": nodes, "vip_address": addr}); !p.Blocked || !slices.Contains(p.codes(), code) {
			t.Errorf("%s: %+v", addr, p.codes())
		}
	}

	// MariaDB kan ook een eigen VIP krijgen in plaats van dat van de app.
	if p := plan(map[string]any{"engine": "mariadb", "nodes": nodes, "vip_address": "10.0.20.191"}); p.Blocked || p.VIP == nil || !p.VIP.Own ||
		!slices.Contains(p.Nodes[0].Steps, "bestand "+protocol.DBTrackFile) || p.Nodes[0].VIPInstance != "cf-nextcloud-mariadb" {
		t.Fatalf("MariaDB met een eigen VIP: %+v", p)
	}

	in := map[string]any{"engine": "postgresql", "nodes": nodes, "vip_address": "10.0.20.190", "settings": map[string]any{"shared_buffers": "512M"}}
	p := plan(in)
	if p.Blocked || !slices.Equal(p.codes(), []string{"info:own_vip"}) || p.Title != "PostgreSQL met Patroni" || p.VIP == nil || !p.VIP.Own ||
		p.VIP.Address != "10.0.20.190" || p.VIP.Vrid != 50 || len(p.Nodes) != 3 {
		t.Fatalf("plan: %+v %+v", p, p.VIP)
	}
	for i, n := range p.Nodes {
		// Ook nc3, zonder keepalived.conf: de agent maakt er een met de
		// include.
		include := []string{"keepalived.conf: include van /etc/keepalived/conf.d"}
		want := append([]string{"pakketten: curl, ca-certificates", "map /usr/local/lib/clusterforge", "map /var/lib/clusterforge",
			"map /etc/postgresql-common/createcluster.d", "bestand /etc/postgresql-common/createcluster.d/clusterforge.conf", "pakketten: postgresql, patroni, python3-etcd",
			"bestand /usr/local/lib/clusterforge/drop-main.sh", "het lege cluster main van Debian weghalen", "gebruiker etcd",
			"bestand /usr/local/lib/clusterforge/install-etcd.sh", "etcd v3.5.34 installeren", "map /etc/etcd", "bestand /etc/etcd/clusterforge.yml",
			"bestand /etc/systemd/system/etcd.service", "etcd starten", "service etcd", "map /etc/patroni", "bestand /etc/patroni/clusterforge.env",
			"bestand /etc/patroni/clusterforge.yml",
			"bestand /etc/systemd/system/patroni.service", "service patroni", "pakket keepalived", "map /etc/keepalived/conf.d",
			"bestand /etc/keepalived/conf.d/clusterforge-postgresql.conf"}, append(include, "keepalived starten of herladen")...)
		if n.Role != "data" || n.Index != i+1 || n.NeedBytes != 912<<20 || n.VIPInstance != "cf-nextcloud-postgresql" || !slices.Equal(n.Steps, want) {
			t.Errorf("plan voor %s: %+v\n%q", n.Hostname, n, n.Steps)
		}
	}
	if d := p.Nodes[0].VIPDiff; !strings.Contains(d, "+include /etc/keepalived/conf.d/*.conf") || strings.Contains(d, "geheim12") {
		t.Errorf("diff op nc1:\n%s", d)
	}

	// Uitrollen: eerst etcd op alle drie, dan Patroni node voor node, dan
	// het VIP; het staat daarna op de primary.
	in["hash"] = p.Hash
	var j job
	if s := c.do("POST", base, in, &j); s != http.StatusAccepted || j.Title != "Database toevoegen: PostgreSQL met Patroni op Nextcloud" {
		t.Fatalf("toevoegen: %d %+v", s, j)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" {
		t.Fatalf("taak: %s %s\n%+v", j.Status, j.Error, j.Steps)
	}
	var names []string
	for _, st := range j.Steps {
		names = append(names, st.Name)
	}
	if !slices.Equal(names, []string{"nc1: etcd", "nc2: etcd", "nc3: etcd", "nc1: PostgreSQL", "nc2: PostgreSQL", "nc3: PostgreSQL",
		"nc1: VIP 10.0.20.190", "nc2: VIP 10.0.20.190", "nc3: VIP 10.0.20.190", "Controleren"}) {
		t.Fatalf("stappen: %v", names)
	}
	if log := strings.Join(j.Steps[4].Log, "\n"); !strings.Contains(log, "nc2: replica, running, streaming, synchroon") {
		t.Errorf("nc2:\n%s", log)
	}
	check := strings.Join(j.Steps[9].Log, "\n")
	if !strings.Contains(check, "nc1 is de primary, de andere 2 datanodes zijn replica's die bijhouden") || !strings.Contains(check, "synchrone replica: nc2") ||
		!strings.Contains(check, "het VIP 10.0.20.190 staat op nc1") {
		t.Errorf("controleren:\n%s", check)
	}
	// Patroni startte pas na etcd: de eerste stap zette op geen enkele node
	// Patroni.
	if log := strings.Join(j.Steps[0].Log, "\n"); strings.Contains(log, "service patroni") || strings.Contains(log, "/etc/patroni/") {
		t.Errorf("etcd-stap op nc1 deed Patroni:\n%s", log)
	}

	// De configuratie op nc2.
	read := func(name, p string) string {
		b, err := os.ReadFile(filepath.Join(g.hosts[name].Root, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	pat := read("nc2", "etc/patroni/clusterforge.yml")
	for _, want := range []string{"scope: nextcloud\n", "name: nc2\n", "connect_address: 10.0.20.187:5432\n", "    - 10.0.20.186:2379\n",
		"    - host replication cf_replicatie 10.0.20.188/32 scram-sha-256\n", "    - host all all 10.0.20.190/32 scram-sha-256\n", "shared_buffers: 512MB\n",
		"data_dir: /var/lib/postgresql/17/clusterforge\n"} {
		if !strings.Contains(pat, want) {
			t.Errorf("patroni.yml zonder %q:\n%s", want, pat)
		}
	}
	ka := read("nc2", strings.TrimPrefix(protocol.DBTrackPGFile, "/"))
	pass := regexp.MustCompile(`auth_pass (\S+)`).FindStringSubmatch(ka)
	if pass == nil || len(pass[1]) != 8 || !strings.Contains(ka, "vrrp_instance cf-nextcloud-postgresql {") || !strings.Contains(ka, "virtual_router_id 50\n") ||
		!strings.Contains(ka, "unicast_src_ip 10.0.20.187\n") || !strings.Contains(ka, "        10.0.20.186\n        10.0.20.188\n") ||
		!strings.Contains(ka, "10.0.20.190/24 dev eth0\n") || !strings.Contains(ka, "track_script {\n        cf_pg\n") {
		t.Fatalf("conf.d op nc2:\n%s", ka)
	}
	if conf := read("nc2", "etc/keepalived/keepalived.conf"); !strings.Contains(conf, "include /etc/keepalived/conf.d/*.conf\n") || !strings.HasSuffix(conf, ncKeepalived("nc2", 140)) {
		t.Errorf("keepalived.conf op nc2:\n%s", conf)
	}
	if conf := read("nc3", "etc/keepalived/keepalived.conf"); !strings.Contains(conf, "include /etc/keepalived/conf.d/*.conf\n") {
		t.Errorf("keepalived.conf op nc3:\n%s", conf)
	}
	// Geen wachtwoord in het logboek van de taak.
	for _, st := range j.Steps {
		if log := strings.Join(st.Log, "\n"); strings.Contains(log, pass[1]) {
			t.Fatalf("het wachtwoord van VRRP staat in het logboek van %s", st.Name)
		}
	}

	// Het VIP staat in de inventory, met nc1 als eigenaar, en het onderdeel
	// toont het.
	owner := func(name string) bool {
		var d statusView
		c.do("GET", "/api/v1/clusters/"+nc.ID, nil, &d)
		for _, v := range d.Vips {
			if v.Address == "10.0.20.190" {
				return v.OwnerHostname != nil && *v.OwnerHostname == name
			}
		}
		return false
	}
	eventually(t, "het VIP op nc1", func() bool { return owner("nc1") })
	var comps list[struct {
		componentView
		VIP *struct {
			Address string `json:"address"`
			Own     bool   `json:"own"`
			Vrid    int    `json:"vrid"`
			Owner   string `json:"owner"`
		} `json:"vip"`
	}]
	c.do("GET", base, nil, &comps)
	if len(comps.Items) != 1 || comps.Items[0].Engine != "postgresql" || comps.Items[0].State != "ready" || comps.Items[0].VIP == nil ||
		!comps.Items[0].VIP.Own || comps.Items[0].VIP.Vrid != 50 || comps.Items[0].VIP.Owner != "nc1" {
		t.Fatalf("onderdeel: %+v", comps)
	}
	compID := comps.Items[0].ID
	var kind string
	if err := e.pool.QueryRow(ctx, "SELECT kind FROM services WHERE cluster_id = $1 AND name = 'postgresql'", nc.ID).Scan(&kind); err != nil || kind != "database" {
		t.Fatalf("dienst postgresql: %q %v", kind, err)
	}

	// Databases en gebruikers: ClusterForge leest en schrijft op de primary
	// nc1, en een app verbindt met het eigen VIP op poort 5432. Gebruikers
	// hebben geen hosts; pg_hba laat de nodes en het VIP binnen.
	dbURL := base + "/" + compID + "/databases"
	var dl componentDatabasesView
	if s := c.do("GET", dbURL, nil, &dl); s != http.StatusOK || dl.Engine != "postgresql" || dl.Node != "nc1" || dl.Address != "10.0.20.190" ||
		dl.Port != 5432 || len(dl.Hosts) != 0 || len(dl.Databases) != 0 {
		t.Fatalf("databases: %d %+v", s, dl)
	}
	if _, users := dl.names(); !slices.Equal(users, []string{"cf_replicatie*", "cf_rewind*", "postgres*"}) {
		t.Fatalf("gebruikers: %v", users)
	}
	for name, want := range map[string]string{"postgres": "van PostgreSQL zelf", "template1": "van PostgreSQL zelf"} {
		if s := c.do("POST", dbURL, map[string]any{"name": name}, &ae); s != http.StatusBadRequest || !strings.Contains(ae.Message, want) {
			t.Errorf("%s: %d %+v", name, s, ae)
		}
	}
	if s := c.do("POST", dbURL, map[string]any{"name": "nextcloud", "user": "pg_nextcloud"}, &ae); s != http.StatusBadRequest || !strings.Contains(ae.Message, "pg_") {
		t.Errorf("pg_nextcloud: %d %+v", s, ae)
	}
	var created struct {
		User     string   `json:"user"`
		Password string   `json:"password"`
		Hosts    []string `json:"hosts"`
		Address  string   `json:"address"`
		Port     int      `json:"port"`
		Node     string   `json:"node"`
	}
	if s := c.do("POST", dbURL, map[string]any{"name": "nextcloud"}, &created); s != http.StatusCreated || created.User != "nextcloud" || len(created.Password) < 16 ||
		len(created.Hosts) != 0 || created.Address != "10.0.20.190" || created.Port != 5432 || created.Node != "nc1" {
		t.Fatalf("nieuwe database: %d %+v", s, created)
	}
	nc1 := g.hosts["nc1"]
	nc1.Locked(func() {
		p := nc1.Postgres
		i := slices.IndexFunc(p.Roles, func(r agenttest.PGRole) bool { return r.Name == "nextcloud" })
		j := slices.IndexFunc(p.Databases, func(d agenttest.Database) bool { return d.Name == "nextcloud" })
		if i < 0 || j < 0 || !p.Roles[i].LogsIn(created.Password, time.Now()) || p.Roles[i].Super || p.Databases[j].Owner != "nextcloud" {
			t.Fatalf("op nc1: %+v", p)
		}
		// Wat Nextcloud bij de installatie maakt.
		p.Databases[j].Tables, p.Databases[j].Size = 12, 40<<20
	})
	c.do("GET", dbURL, nil, &dl)
	if dbs, users := dl.names(); !slices.Equal(dbs, []string{"nextcloud(nextcloud)"}) || !slices.Equal(users, []string{"nextcloud", "cf_replicatie*", "cf_rewind*", "postgres*"}) ||
		!dl.Users[0].Databases[0].All {
		t.Fatalf("na het aanmaken: %v %v %+v", dbs, users, dl.Users[0])
	}
	if s := c.do("POST", base+"/"+compID+"/users/postgres/password", nil, &ae); s != http.StatusBadRequest ||
		!strings.Contains(ae.Message, "Patroni zelf") {
		t.Errorf("wachtwoord van postgres: %d %+v", s, ae)
	}

	// Een dump komt op de primary.
	dumpsURL := base + "/" + compID + "/dumps"
	if s := c.do("POST", dumpsURL, map[string]any{"database": "nextcloud"}, &j); s != http.StatusAccepted {
		t.Fatalf("dump: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "succeeded" || !strings.Contains(strings.Join(j.Steps[0].Log, "\n"), "nc1 is de primary") {
		t.Fatalf("dump: %s %s %+v", j.Status, j.Error, j.Steps)
	}
	var dv componentDumpsView
	c.do("GET", dumpsURL, nil, &dv)
	if len(dv.Dumps) != 1 || dv.Dumps[0].Node != "nc1" || dv.Dumps[0].Status != "ok" || dv.Dumps[0].Tables != 12 || !strings.HasSuffix(dv.Dumps[0].File, ".pgsql.zst") ||
		dv.freshness() != "nextcloud=ok" {
		t.Fatalf("dumps: %+v", dv)
	}
	g.replicate("nc1")

	// De drift ziet een met de hand aangepast bestand in conf.d.
	driftURL := "/api/v1/clusters/" + nc.ID + "/drift"
	var rep baselineReport
	c.do("POST", driftURL+"/check", nil, &rep)
	if rep.status("nc1") != "in_sync" || rep.status("nc2") != "in_sync" || rep.status("nc3") != "in_sync" {
		t.Fatalf("drift na de uitrol: %+v", rep)
	}
	writeFile(t, filepath.Join(g.hosts["nc3"].Root, protocol.DBTrackPGFile), strings.Replace(read("nc3", strings.TrimPrefix(protocol.DBTrackPGFile, "/")), "priority 100", "priority 200", 1), 0o600)
	c.do("POST", driftURL+"/check", nil, &rep)
	if f := rep.findings("nc3"); rep.status("nc3") != "drift" || len(f) != 1 || f[0].Key != "file:"+protocol.DBTrackPGFile+":content" {
		t.Fatalf("drift na de wijziging: %+v", rep)
	}

	// Valt de primary uit, dan maakt Patroni nc2 primary en gaat het VIP
	// mee.
	g.failover("nc2")
	eventually(t, "het VIP op nc2", func() bool { return owner("nc2") })

	// Terugzetten na de failover: de dump staat op nc1, maar schrijven kan
	// alleen op de nieuwe primary nc2. ClusterForge maakt daar een
	// tijdelijke beheerder, de agent op nc1 zet de dump ermee terug over het
	// netwerk, en daarna is de beheerder weer weg.
	var started struct {
		Job    job    `json:"job"`
		Target string `json:"target"`
	}
	if s := c.do("POST", dumpsURL+"/"+dv.Dumps[0].ID+"/restore", nil, &started); s != http.StatusAccepted || !strings.HasPrefix(started.Target, "nextcloud_herstel_") {
		t.Fatalf("terugzetten: %d %+v", s, started)
	}
	j = waitJobLong(t, c, started.Job.ID)
	log := strings.Join(j.Steps[0].Log, "\n")
	if j.Status != "succeeded" || !regexp.MustCompile(`Tijdelijke beheerder cf_herstel_[0-9a-f]{16} gemaakt op de primary nc2`).MatchString(log) ||
		!strings.Contains(log, "weggehaald") || !strings.Contains(log, "runuser -u postgres -- psql "+started.Target) {
		t.Fatalf("terugzetten: %s %s\n%s", j.Status, j.Error, log)
	}
	nc2 := g.hosts["nc2"]
	nc2.Locked(func() {
		p := nc2.Postgres
		i := slices.IndexFunc(p.Databases, func(d agenttest.Database) bool { return d.Name == started.Target })
		if i < 0 || p.Databases[i].Tables != 12 || p.Databases[i].Owner != "nextcloud" {
			t.Fatalf("op nc2: %+v", p.Databases)
		}
		if slices.ContainsFunc(p.Roles, func(r agenttest.PGRole) bool { return strings.HasPrefix(r.Name, "cf_herstel_") }) {
			t.Errorf("de tijdelijke beheerder staat er nog: %+v", p.Roles)
		}
	})
	if !slices.ContainsFunc(nc1.Calls(), func(c string) bool {
		return strings.HasPrefix(c, "psql -X -q -v ON_ERROR_STOP=1 -d host=10.0.20.187 port=5432 dbname="+started.Target)
	}) {
		t.Errorf("aanroepen op nc1: %q", nc1.Calls())
	}
	// Het wachtwoord van de beheerder staat nergens in ClusterForge.
	var state, jobParams string
	if err := e.pool.QueryRow(ctx, "SELECT s.state::text, j.params::text FROM job_steps s JOIN jobs j ON j.id = s.job_id WHERE s.job_id = $1", started.Job.ID).Scan(&state, &jobParams); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\{"at": "[^"]+", "role": "cf_herstel_[0-9a-f]{16}", "primary": "nc2"\}$`).MatchString(state) || strings.Contains(jobParams, "password") {
		t.Errorf("staat van de taak: %s, %s", state, jobParams)
	}
}
