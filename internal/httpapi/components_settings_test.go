package httpapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
)

type settingsPlanView struct {
	Engine   string `json:"engine"`
	Revision int    `json:"revision"`
	Params   []struct {
		Name  string `json:"name"`
		Label string `json:"label"`
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"params"`
	Changes []struct {
		Name string `json:"name"`
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"changes"`
	Nodes []struct {
		Hostname string `json:"hostname"`
		Healthy  bool   `json:"healthy"`
		Writer   bool   `json:"writer"`
	} `json:"nodes"`
	Effects  []string           `json:"effects"`
	Findings []componentFinding `json:"findings"`
	Blocked  bool               `json:"blocked"`
	Hash     string             `json:"hash"`
}

type upgradePlanView struct {
	Engine string `json:"engine"`
	Nodes  []struct {
		Hostname string    `json:"hostname"`
		Role     string    `json:"role"`
		Writer   bool      `json:"writer"`
		Version  string    `json:"version"`
		Packages *[]string `json:"packages"`
		Skip     bool      `json:"skip"`
	} `json:"nodes"`
	Effects  []string           `json:"effects"`
	Findings []componentFinding `json:"findings"`
	Blocked  bool               `json:"blocked"`
	Hash     string             `json:"hash"`
}

type componentFinding struct {
	Level    string `json:"level"`
	Code     string `json:"code"`
	Hostname string `json:"hostname"`
	Message  string `json:"message"`
}

// codesOf geeft de codes van de bevindingen, zoals "blocked:no_changes".
func codesOf(fs []componentFinding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Level+":"+f.Code)
	}
	return strings.Join(out, " ")
}

func (p settingsPlanView) order() string {
	var out []string
	for _, n := range p.Nodes {
		out = append(out, n.Hostname)
	}
	return strings.Join(out, " ")
}

func (p upgradePlanView) order() string {
	var out []string
	for _, n := range p.Nodes {
		out = append(out, n.Hostname)
	}
	return strings.Join(out, " ")
}

var markerRe = regexp.MustCompile(`touch (/var/lib/clusterforge/db-update-[0-9a-f-]+)\.(checked|done)`)

// updateShell speelt de commando's van Bijwerken na op een host: apt-get
// update en kijken (met newer of er iets nieuws is), bijwerken (install
// zet de nieuwe versie) en herstarten (restart). order onthoudt de nodes
// in de volgorde waarin ze herstartten.
type updateShell struct {
	mu      sync.Mutex
	order   []string
	newer   map[string]bool
	install func(name string, h *agenttest.Host)
	restart func(name string, h *agenttest.Host, unit string)
}

func (u *updateShell) restarted() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.order)
}

func (u *updateShell) shell(name string, h *agenttest.Host) func(cmd string) (bool, []byte, error) {
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(h.Root, p)); return err == nil }
	write := func(p, content string) {
		p = filepath.Join(h.Root, p)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	return func(cmd string) (bool, []byte, error) {
		switch {
		case strings.HasPrefix(cmd, "test ! -e "):
			// Het merkteken van gevonden updates, of van een herstart die
			// nog moet.
			if exists(strings.Fields(cmd)[3]) {
				return true, nil, errors.New("exit status 1")
			}
			return true, nil, nil
		case strings.Contains(cmd, "update && { if apt-get"):
			m := markerRe.FindStringSubmatch(cmd)
			u.mu.Lock()
			newer := u.newer[name]
			u.mu.Unlock()
			out := "alles is al bijgewerkt\n"
			if newer {
				write(m[1]+".found", "")
				out = "Inst mariadb-server [1:11.8.3-0+deb13u1] (1:11.8.4-0+deb13u1 Debian-Security:13/stable-security [amd64])\n"
			}
			write(m[1]+".checked", "")
			return true, []byte(out), nil
		case strings.Contains(cmd, "install -y --only-upgrade"):
			m := markerRe.FindStringSubmatch(cmd)
			u.mu.Lock()
			newer := u.newer[name]
			u.mu.Unlock()
			if newer {
				u.install(name, h)
				write(m[1]+".restart", "1700000000\n")
			}
			write(m[1]+".done", "")
			return true, []byte("nieuw: 11.8.4\n"), nil
		case strings.HasPrefix(cmd, "systemctl restart ") && strings.Contains(cmd, " && rm -f "):
			f := strings.Fields(cmd)
			unit := f[len(f)-5]
			u.mu.Lock()
			u.order = append(u.order, name)
			u.mu.Unlock()
			u.restart(name, h, unit)
			_ = os.Remove(filepath.Join(h.Root, f[len(f)-1]))
			return true, nil, nil
		}
		return false, nil, nil
	}
}

// TestDatabaseSettingsGalera is de acceptatie van de instellingen en het
// bijwerken van MariaDB Galera (mijlpaal 11 van fase 5): nieuwe
// instellingen worden een nieuwe revisie die node voor node toegepast
// wordt, met een herstart en wachten op Synced na elke node; bijwerken
// doet node voor node apt-get update en herstart alleen waar iets
// veranderde.
func TestDatabaseSettingsGalera(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g := lab.e, lab.c, lab.g
	url := lab.base + "/" + lab.cid
	plan := func(in map[string]any) settingsPlanView {
		t.Helper()
		var p settingsPlanView
		if s := c.do("POST", url+"/settings/plan", map[string]any{"settings": in}, &p); s != http.StatusOK {
			t.Fatalf("plan: %d", s)
		}
		return p
	}

	p := plan(nil)
	if p.Engine != "mariadb" || p.Revision != 1 || len(p.Params) != 2 || p.Params[0].Name != "buffer_pool" || p.Params[0].Value != "256M" ||
		p.Params[1].Value != "200" || !p.Blocked || codesOf(p.Findings) != "blocked:no_changes" || p.order() != "nc1 nc2 nc3" {
		t.Fatalf("plan zonder wijziging: %+v", p)
	}
	var ae apiErr
	if s := c.do("POST", url+"/settings/plan", map[string]any{"settings": map[string]any{"max_connections": 5}}, &ae); s != http.StatusBadRequest ||
		!strings.Contains(ae.Message, "minstens 20") {
		t.Fatalf("te weinig verbindingen: %d %+v", s, ae)
	}
	if s := c.do("POST", url+"/settings/plan", map[string]any{"settings": map[string]any{"vip": "10.0.20.185"}}, &ae); s != http.StatusBadRequest ||
		!strings.Contains(ae.Message, "kan na de uitrol niet meer veranderen") {
		t.Fatalf("vip: %d %+v", s, ae)
	}

	in := map[string]any{"buffer_pool": "512M", "max_connections": 300}
	// Zolang nc3 niet meedoet, kan het niet: anders ligt er straks een
	// tweede node stil.
	g.leave("nc3", g.hosts["nc3"])
	if p = plan(in); !p.Blocked || codesOf(p.Findings) != "blocked:not_healthy" ||
		!strings.Contains(p.Findings[0].Message, "nc3 doet nu niet mee (") || !strings.Contains(p.Findings[0].Message, "voordat ClusterForge de instellingen wijzigt") {
		t.Fatalf("met nc3 weg: %+v", p.Findings)
	}
	g.join("nc3", g.hosts["nc3"], false)
	p = plan(in)
	if p.Blocked || len(p.Changes) != 2 || p.Changes[0].From != "256M" || p.Changes[0].To != "512M" || p.Changes[1].To != "300" {
		t.Fatalf("plan: %+v", p)
	}
	if eff := strings.Join(p.Effects, "\n"); !strings.Contains(eff, "herstart MariaDB node voor node: nc1, nc2 en nc3") {
		t.Errorf("gevolgen: %s", eff)
	}
	var j job
	if s := c.do("POST", url+"/settings", map[string]any{"settings": in, "hash": "oud"}, &ae); s != http.StatusConflict || ae.Code != "plan_stale" {
		t.Fatalf("oude hash: %d %+v", s, ae)
	}
	if s := c.do("POST", url+"/settings", map[string]any{"settings": in, "hash": p.Hash}, &j); s != http.StatusAccepted ||
		j.Title != "Instellingen wijzigen: MariaDB Galera op Nextcloud" {
		t.Fatalf("instellingen: %d %+v", s, j)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), []string{"nc1: MariaDB", "nc2: MariaDB", "nc3: MariaDB", "Controleren"}) {
		t.Fatalf("taak: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	for _, name := range []string{"nc1", "nc2", "nc3"} {
		h := g.hosts[name]
		b, _ := os.ReadFile(filepath.Join(h.Root, "etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf"))
		if !strings.Contains(string(b), "innodb_buffer_pool_size = 512M") || !strings.Contains(string(b), "max_connections = 300") {
			t.Errorf("%s: %s", name, b)
		}
		if !slices.Contains(h.Calls(), "systemctl restart mariadb") {
			t.Errorf("%s herstartte niet: %v", name, h.Calls())
		}
	}
	if log := stepLogOf(j, "nc2: MariaDB"); !strings.Contains(log, "nc2 deed al mee; MariaDB herstart om de nieuwe configuratie te lezen") {
		t.Errorf("log:\n%s", log)
	}
	var comps list[componentView]
	c.do("GET", lab.base, nil, &comps)
	if comp := comps.Items[0]; comp.Revision != 2 || comp.AppliedRevision == nil || *comp.AppliedRevision != 2 {
		t.Errorf("revisie: %+v", comps.Items[0])
	}
	if ev := clusterEvents(t, e, lab.cluster.ID, "cluster.database_settings"); len(ev) != 1 || !strings.Contains(ev[0], `"from": "256M"`) ||
		!strings.Contains(ev[0], `"revision": 2`) {
		t.Errorf("events: %v", ev)
	}
	if p = plan(nil); p.Revision != 2 || p.Params[0].Value != "512M" || p.Params[1].Value != "300" {
		t.Errorf("na de wijziging: %+v", p.Params)
	}

	// Bijwerken: nc1 en nc2 hebben een nieuwere MariaDB, nc3 niet.
	u := &updateShell{
		newer: map[string]bool{"nc1": true, "nc2": true},
		install: func(name string, h *agenttest.Host) {
			h.Locked(func() { h.MariaDB.Version = "11.8.4-MariaDB-0+deb13u1" })
		},
		restart: func(name string, h *agenttest.Host, unit string) {
			if unit != "mariadb" {
				t.Errorf("%s: herstart %s", name, unit)
			}
			g.leave(name, h)
			time.AfterFunc(200*time.Millisecond, func() {
				g.join(name, h, false)
				h.Locked(func() { h.MariaDB.Uptime = 0 })
			})
		},
	}
	for name, h := range g.hosts {
		h.Locked(func() { h.Shell = u.shell(name, h) })
	}
	var up upgradePlanView
	if s := c.do("GET", url+"/upgrade", nil, &up); s != http.StatusOK {
		t.Fatalf("plan bijwerken: %d", s)
	}
	if up.Blocked || up.order() != "nc1 nc2 nc3" || up.Nodes[0].Version != "11.8.3" || up.Nodes[0].Packages == nil || codesOf(up.Findings) != "info:no_updates" {
		t.Fatalf("plan bijwerken: %+v", up)
	}
	if eff := strings.Join(up.Effects, "\n"); !strings.Contains(eff, "Node voor node: nc1, nc2 en nc3") || !strings.Contains(eff, "werkt het alleen MariaDB en Galera bij") {
		t.Errorf("gevolgen: %s", eff)
	}
	if s := c.do("POST", url+"/upgrade", map[string]any{"hash": up.Hash}, &j); s != http.StatusAccepted || j.Title != "Bijwerken: MariaDB Galera op Nextcloud" {
		t.Fatalf("bijwerken: %d %+v", s, j)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), []string{"nc1: bijwerken", "nc2: bijwerken", "nc3: bijwerken", "Controleren"}) {
		t.Fatalf("bijwerken: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if got := u.restarted(); !slices.Equal(got, []string{"nc1", "nc2"}) {
		t.Errorf("herstart: %v", got)
	}
	if log := stepLogOf(j, "nc2: bijwerken"); !strings.Contains(log, "nc2 draait nu 11.8.4 in plaats van 11.8.3") || !strings.Contains(log, "nc2: Synced in een cluster van 3") {
		t.Errorf("nc2:\n%s", log)
	}
	if log := stepLogOf(j, "nc3: bijwerken"); !strings.Contains(log, "nc3 heeft geen nieuwere versie; er verandert niets") {
		t.Errorf("nc3:\n%s", log)
	}
	if ev := clusterEvents(t, e, lab.cluster.ID, "cluster.database_updated"); len(ev) != 1 || !strings.Contains(ev[0], `"updated": ["nc1", "nc2"]`) {
		t.Errorf("events: %v", ev)
	}
	// nc1 herstartte één keer: na het bijwerken, door ClusterForge.
	restarts := 0
	for _, call := range g.hosts["nc1"].Calls() {
		if strings.HasPrefix(call, "sh -c systemctl restart --no-block mariadb && rm -f /var/lib/clusterforge/db-update-"+j.ID+".restart") {
			restarts++
		}
	}
	if restarts != 1 {
		t.Errorf("nc1 herstartte %d keer: %v", restarts, g.hosts["nc1"].Calls())
	}
}

// TestDatabaseSettingsPatroni is de acceptatie van de instellingen en het
// bijwerken van PostgreSQL met Patroni (mijlpaal 11 van fase 5):
// max_connections komt in etcd en PostgreSQL herstart node voor node, de
// primary als laatste; bijwerken doet de replica's eerst en schakelt
// over voor het de primary bijwerkt.
func TestDatabaseSettingsPatroni(t *testing.T) {
	lab := newPatroniHosts(t)
	e, c, g, nc := lab.e, lab.c, lab.g, lab.nc
	cid := lab.deploy(t)
	url := "/api/v1/clusters/" + nc.ID + "/components/" + cid
	eventually(t, "het VIP op nc1", func() bool { return slices.Contains(g.hosts["nc1"].Addresses(), "10.0.20.190") })
	plan := func(in map[string]any) settingsPlanView {
		t.Helper()
		var p settingsPlanView
		if s := c.do("POST", url+"/settings/plan", map[string]any{"settings": in}, &p); s != http.StatusOK {
			t.Fatalf("plan: %d", s)
		}
		return p
	}

	in := map[string]any{"shared_buffers": "512M", "max_connections": 200}
	p := plan(in)
	if p.Engine != "postgresql" || p.Blocked || len(p.Changes) != 2 || p.order() != "nc2 nc3 nc1" || !p.Nodes[2].Writer {
		t.Fatalf("plan: %+v", p)
	}
	if eff := strings.Join(p.Effects, "\n"); !strings.Contains(eff, "max_connections komt in etcd") ||
		!strings.Contains(eff, "herstart PostgreSQL node voor node: nc2 en nc3 en als laatste nc1, de primary") ||
		!strings.Contains(eff, "Patroni blijft daar de primary, er is geen failover") {
		t.Errorf("gevolgen: %s", eff)
	}
	var j job
	if s := c.do("POST", url+"/settings", map[string]any{"settings": in, "hash": p.Hash}, &j); s != http.StatusAccepted {
		t.Fatalf("instellingen: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	names := stepNamesOf(j)
	if j.Status != "succeeded" || !slices.Contains(names, "Instellingen in etcd") ||
		slices.Index(names, "Instellingen in etcd")+1 != slices.Index(names, "PostgreSQL herstarten waar nodig") || names[len(names)-1] != "Controleren" {
		t.Fatalf("taak: %s %s %v", j.Status, j.Error, names)
	}
	g.mu.Lock()
	restarts, maxConn := slices.Clone(g.restarts), atoiAny(g.params["max_connections"])
	g.mu.Unlock()
	if !slices.Equal(restarts, []string{"nc2", "nc3", "nc1"}) || maxConn != 200 {
		t.Errorf("herstart %v, max_connections %d", restarts, maxConn)
	}
	for _, name := range []string{"nc1", "nc2", "nc3"} {
		h := g.hosts[name]
		b, _ := os.ReadFile(filepath.Join(h.Root, "etc/patroni/clusterforge.yml"))
		var mc int
		var pending bool
		h.Locked(func() { mc, pending = h.Postgres.MaxConnections, h.Patroni.PendingRestart })
		if !strings.Contains(string(b), "shared_buffers: 512MB") || mc != 200 || pending {
			t.Errorf("%s: max_connections %d, pending %v", name, mc, pending)
		}
	}
	if log := stepLogOf(j, "PostgreSQL herstarten waar nodig"); !strings.Contains(log, "PostgreSQL op nc2 herstart") || !strings.Contains(log, "nc1: primary, running") {
		t.Errorf("log:\n%s", log)
	}

	// Bijwerken: de replica's eerst; voor nc1 schakelt ClusterForge over
	// naar nc2, de synchrone replica.
	u := &updateShell{
		newer: map[string]bool{"nc1": true, "nc2": true, "nc3": true},
		install: func(name string, h *agenttest.Host) {
			h.Locked(func() { h.Postgres.Version = "17.7 (Debian 17.7-1.pgdg13+1)" })
		},
		restart: func(name string, h *agenttest.Host, unit string) {
			if unit != "patroni" {
				t.Errorf("%s: herstart %s", name, unit)
			}
			var repl string
			h.Locked(func() { repl, h.Patroni.State, h.Patroni.Replication = h.Patroni.Replication, "stopped", "" })
			time.AfterFunc(200*time.Millisecond, func() {
				h.Locked(func() { h.Patroni.State, h.Patroni.Replication, h.Postgres.Uptime = "running", repl, 0 })
			})
		},
	}
	for name, h := range g.hosts {
		h.Locked(func() { h.Shell = u.shell(name, h) })
	}
	var up upgradePlanView
	if s := c.do("GET", url+"/upgrade", nil, &up); s != http.StatusOK {
		t.Fatalf("plan bijwerken: %d", s)
	}
	if up.Blocked || up.order() != "nc2 nc3 nc1" || up.Nodes[0].Version != "17.6" {
		t.Fatalf("plan bijwerken: %+v", up)
	}
	if eff := strings.Join(up.Effects, "\n"); !strings.Contains(eff, "Voor nc1 schakelt ClusterForge eerst over naar de synchrone replica") {
		t.Errorf("gevolgen: %s", eff)
	}
	if s := c.do("POST", url+"/upgrade", map[string]any{"hash": up.Hash}, &j); s != http.StatusAccepted {
		t.Fatalf("bijwerken: %d", s)
	}
	j = waitJobLong(t, c, j.ID)
	if j.Status != "succeeded" || !slices.Equal(stepNamesOf(j), []string{"nc2: bijwerken", "nc3: bijwerken", "nc1: bijwerken", "Controleren"}) {
		t.Fatalf("bijwerken: %s %s %v", j.Status, j.Error, stepNamesOf(j))
	}
	if got := u.restarted(); !slices.Equal(got, []string{"nc2", "nc3", "nc1"}) {
		t.Errorf("herstart: %v", got)
	}
	if log := stepLogOf(j, "nc1: bijwerken"); !strings.Contains(log, "nc1 is de primary; Patroni schakelt over naar nc2") ||
		!strings.Contains(log, "nc1 draait nu 17.7 in plaats van 17.6") {
		t.Errorf("nc1:\n%s", log)
	}
	if log := stepLogOf(j, "Controleren"); !strings.Contains(log, "het VIP 10.0.20.190 staat op nc2") {
		t.Errorf("controleren:\n%s", log)
	}
	if ev := clusterEvents(t, e, nc.ID, "cluster.database_switchover"); len(ev) != 1 || !strings.Contains(ev[0], `"update": true`) {
		t.Errorf("switchover: %v", ev)
	}
	if ev := clusterEvents(t, e, nc.ID, "cluster.database_updated"); len(ev) != 1 || !strings.Contains(ev[0], `"updated": ["nc2", "nc3", "nc1"]`) {
		t.Errorf("events: %v", ev)
	}
}
