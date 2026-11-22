package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type componentStatusView struct {
	Health       string  `json:"health"`
	HealthReason string  `json:"health_reason"`
	HealthAt     *string `json:"health_at"`
	DownSince    *string `json:"down_since"`
	DownReason   string  `json:"down_reason"`
}

type metricsPanelsView struct {
	Range  string `json:"range"`
	Panels []struct {
		ID   string `json:"id"`
		Unit string `json:"unit"`
	} `json:"panels"`
}

func (m metricsPanelsView) ids() string {
	var out []string
	for _, p := range m.Panels {
		out = append(out, p.ID+":"+p.Unit)
	}
	return strings.Join(out, " ")
}

// healthHelpers zijn de stappen die beide tests delen: de bewaking een
// ronde laten doen, het onderdeel lezen, op de status van het cluster
// wachten en de gebeurtenissen van de database lezen.
type healthHelpers struct {
	t         *testing.T
	e         *testEnv
	c         *client
	clusterID string
	base      string
}

func (h healthHelpers) check() {
	h.t.Helper()
	if err := h.e.cmps.CheckAll(context.Background()); err != nil {
		h.t.Fatalf("bewaking: %v", err)
	}
}

func (h healthHelpers) comp() componentStatusView {
	h.t.Helper()
	var l list[componentStatusView]
	if s := h.c.do("GET", h.base, nil, &l); s != http.StatusOK || len(l.Items) != 1 {
		h.t.Fatalf("onderdelen: %d", s)
	}
	return l.Items[0]
}

// clusterIs wacht op de status van het cluster, met reason erin.
func (h healthHelpers) clusterIs(status, reason string) {
	h.t.Helper()
	var got statusView
	eventually(h.t, "cluster "+status+": "+reason, func() bool {
		h.e.eval.Kick()
		h.c.do("GET", "/api/v1/clusters/"+h.clusterID, nil, &got)
		return got.Status == status && strings.Contains(got.StatusReason, reason)
	})
}

// log geeft de samenvattingen van een soort gebeurtenis van het cluster.
func (h healthHelpers) log(action string) []string {
	var out []string
	for _, it := range h.c.audit("cluster=" + h.clusterID).Items {
		if it.Action == action {
			out = append(out, it.Summary)
		}
	}
	return out
}

// TestDatabaseHealthGalera is de acceptatie van mijlpaal 12 van fase 5:
// een Galera-node die wegvalt, maakt het cluster verminderd en geeft een
// incident. Een herstart en een state transfer komen in het logboek en in
// de tijdlijn van het incident, en de grafieken lezen de metingen die de
// agents meesturen.
func TestDatabaseHealthGalera(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g := lab.e, lab.c, lab.g
	ctx := context.Background()
	h := healthHelpers{t: t, e: e, c: c, clusterID: lab.cluster.ID, base: lab.base}
	mariadb := func(name string, f func(m *agenttest.MariaDB)) {
		host := g.hosts[name]
		host.Locked(func() { f(host.MariaDB) })
	}

	// Alle drie Synced: gezond.
	h.check()
	if st := h.comp(); st.Health != "healthy" || st.HealthReason != "" || st.HealthAt == nil {
		t.Fatalf("gezond: %+v", st)
	}
	h.clusterIs("healthy", "")

	// MariaDB op nc2 herstart: de uptime begint opnieuw.
	for _, n := range []string{"nc1", "nc2", "nc3"} {
		mariadb(n, func(m *agenttest.MariaDB) { m.Uptime = 86400 })
	}
	h.check()
	mariadb("nc2", func(m *agenttest.MariaDB) { m.Uptime = 4 })
	h.check()
	h.check()
	if l := h.log("cluster.database_restarted"); !slices.Equal(l, []string{"MariaDB op nc2 herstartte (MariaDB Galera van cluster Nextcloud); draait nu 4 seconden"}) {
		t.Fatalf("herstart: %q", l)
	}
	if st := h.comp(); st.Health != "healthy" {
		t.Fatalf("na de herstart: %+v", st)
	}

	// nc3 valt weg: verminderd, in de status van het cluster, met een
	// incident.
	g.leave("nc3", g.hosts["nc3"])
	h.check()
	if st := h.comp(); st.Health != "degraded" || !strings.HasPrefix(st.HealthReason, "MariaDB Galera: nc3 doet niet mee (") || st.DownSince != nil {
		t.Fatalf("zonder nc3: %+v", st)
	}
	h.clusterIs("degraded", "MariaDB Galera: nc3 doet niet mee (")
	var inc incidentView
	eventually(t, "incident", func() bool {
		incs := c.incidents("status=open")
		if len(incs) == 1 {
			inc = incs[0]
		}
		return len(incs) == 1
	})
	if len(inc.Subjects) == 0 || inc.Subjects[0].Type != "cluster" || inc.Subjects[0].Name != "Nextcloud" || inc.Subjects[0].FirstStatus != "degraded" {
		t.Fatalf("incident: %+v", inc)
	}

	// nc3 sluit weer aan en haalt de data bij nc1: een state transfer. Zo
	// lang is het cluster nog verminderd.
	mariadb("nc3", func(m *agenttest.MariaDB) {
		m.Down, m.Uptime = false, 3
		m.Galera = &agenttest.Galera{ClusterName: "nextcloud", UUID: galeraUUID, Status: "Primary", Size: 3, State: 1, Comment: "Joining"}
	})
	mariadb("nc1", func(m *agenttest.MariaDB) { m.Galera.State, m.Galera.Comment = 2, "Donor/Desynced" })
	h.check()
	want := "nc3 sluit opnieuw aan bij MariaDB Galera van cluster Nextcloud en haalt de data die hij mist bij nc1 (state transfer)"
	if l := h.log("cluster.database_state_transfer"); !slices.Equal(l, []string{want}) {
		t.Fatalf("state transfer: %q", l)
	}
	// Een node die weer start, herstartte niet: hij stond stil.
	if l := h.log("cluster.database_restarted"); len(l) != 1 {
		t.Fatalf("herstarts: %q", l)
	}
	if st := h.comp(); st.Health != "degraded" || !strings.Contains(st.HealthReason, "nc1 is niet Synced (Donor/Desynced") ||
		!strings.Contains(st.HealthReason, "nc3 is niet Synced (Joining") {
		t.Fatalf("tijdens de state transfer: %+v", st)
	}

	// De tijdlijn van het incident heeft de gebeurtenissen van de database.
	var d struct {
		incidentDetail
		Timeline []timelineEntry `json:"timeline"`
	}
	if s := c.do("GET", "/api/v1/incidents/"+inc.ID, nil, &d); s != http.StatusOK {
		t.Fatalf("incident: %d", s)
	}
	for _, action := range []string{"cluster.database_restarted", "cluster.database_state_transfer"} {
		if !slices.ContainsFunc(d.Timeline, func(en timelineEntry) bool { return en.Action == action }) {
			t.Errorf("geen %s in de tijdlijn: %q", action, summaries(d.Timeline))
		}
	}

	// Alle drie weer Synced: gezond.
	g.join("nc3", g.hosts["nc3"], false)
	h.check()
	if st := h.comp(); st.Health != "healthy" {
		t.Fatalf("weer Synced: %+v", st)
	}
	h.clusterIs("healthy", "")

	// De agents sturen de metingen van MariaDB mee, met de labels van de
	// node.
	eventually(t, "metingen van MariaDB", func() bool {
		e.ingest.Flush(ctx)
		return slices.ContainsFunc(e.vm.received(), func(l string) bool {
			return strings.HasPrefix(l, "node_db_connections{") && strings.Contains(l, `instance="nc1"`) && strings.Contains(l, `engine="mariadb"`)
		})
	})
	// De grafieken, ook voor een kijker.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	var m metricsPanelsView
	if s := v.do("GET", lab.base+"/"+lab.cid+"/metrics?range=6h", nil, &m); s != http.StatusOK || m.Range != "6h" ||
		m.ids() != "connections:count queries:per_second buffer_pool:percent recv_queue:count flow_control:percent size:bytes" {
		t.Fatalf("grafieken: %d %+v", s, m)
	}
	e.vm.mu.Lock()
	queries := slices.Clone(e.vm.queries)
	e.vm.mu.Unlock()
	sel := `cluster_id="` + lab.cluster.ID + `",engine="mariadb",node_id=~"` + lab.ids["nc1"] + "|" + lab.ids["nc2"] + "|" + lab.ids["nc3"] + `"`
	if !slices.ContainsFunc(queries, func(q string) bool { return strings.Contains(q, "node_db_connections{"+sel+"}") }) {
		t.Fatalf("queries: %q", queries)
	}
	if s := v.do("GET", lab.base+"/"+lab.ids["nc1"]+"/metrics", nil, nil); s != http.StatusNotFound {
		t.Fatalf("onbekend onderdeel: %d", s)
	}
}

// TestDatabaseHealthPatroni: bij PostgreSQL met Patroni maakt een replica
// die achterloopt of wegvalt het cluster verminderd, een failover komt in
// het logboek, en zonder primary is het cluster down.
func TestDatabaseHealthPatroni(t *testing.T) {
	l := newPatroniHosts(t)
	cid := l.deploy(t)
	e, c, g := l.e, l.c, l.g
	base := "/api/v1/clusters/" + l.nc.ID + "/components"
	h := healthHelpers{t: t, e: e, c: c, clusterID: l.nc.ID, base: base}
	postgres := func(name string, f func(p *agenttest.Postgres, pt *agenttest.Patroni)) {
		host := g.hosts[name]
		host.Locked(func() { f(host.Postgres, host.Patroni) })
	}
	owner := func(name string) func() bool {
		return func() bool {
			var d statusView
			c.do("GET", "/api/v1/clusters/"+l.nc.ID, nil, &d)
			for _, v := range d.Vips {
				if v.Address == "10.0.20.190" {
					return v.OwnerHostname != nil && *v.OwnerHostname == name
				}
			}
			return false
		}
	}
	eventually(t, "het VIP op nc1", owner("nc1"))

	h.check()
	h.check()
	if st := h.comp(); st.Health != "healthy" || st.HealthReason != "" {
		t.Fatalf("gezond: %+v", st)
	}
	h.clusterIs("healthy", "")

	// nc2 loopt 45 s achter: verminderd.
	postgres("nc2", func(p *agenttest.Postgres, _ *agenttest.Patroni) {
		p.Source = &agenttest.PGSource{Host: "10.0.20.186", Status: "streaming", Lag: 45}
	})
	h.check()
	if st := h.comp(); st.Health != "degraded" || st.HealthReason != "PostgreSQL met Patroni: nc2 loopt 45 s achter" {
		t.Fatalf("achterstand: %+v", st)
	}
	h.clusterIs("degraded", "PostgreSQL met Patroni: nc2 loopt 45 s achter")
	postgres("nc2", func(p *agenttest.Postgres, _ *agenttest.Patroni) { p.Source.Lag = 0 })

	// De primary valt uit: Patroni maakt nc2 primary, en het VIP volgt.
	g.failover("nc2")
	h.check()
	if l := h.log("cluster.database_failover"); !slices.Equal(l, []string{"Failover in PostgreSQL met Patroni van cluster Nextcloud: nc2 is nu de primary in plaats van nc1"}) {
		t.Fatalf("failover: %q", l)
	}
	eventually(t, "het VIP op nc2", owner("nc2"))
	h.check()
	if st := h.comp(); st.Health != "degraded" || !strings.HasPrefix(st.HealthReason, "PostgreSQL met Patroni: nc1 doet niet mee (") {
		t.Fatalf("na de failover: %+v", st)
	}

	// Ook nc2 stopt: geen primary meer, de database is down.
	postgres("nc2", func(_ *agenttest.Postgres, pt *agenttest.Patroni) { pt.State = "stopped" })
	h.check()
	st := h.comp()
	if st.Health != "down" || st.DownSince == nil || !strings.HasPrefix(st.DownReason, "er is geen primary; nc1: ") ||
		!strings.HasPrefix(st.HealthReason, "PostgreSQL met Patroni: er is geen primary; ") {
		t.Fatalf("zonder primary: %+v", st)
	}
	h.clusterIs("down", "PostgreSQL met Patroni: er is geen primary")
	if l := h.log("cluster.database_down"); len(l) != 1 {
		t.Fatalf("down: %q", l)
	}

	// nc2 start weer: de database draait, nog zonder nc1.
	postgres("nc2", func(_ *agenttest.Postgres, pt *agenttest.Patroni) { pt.State = "running" })
	h.check()
	if st := h.comp(); st.Health != "degraded" || st.DownSince != nil {
		t.Fatalf("weer een primary: %+v", st)
	}
	if l := h.log("cluster.database_up"); len(l) != 1 {
		t.Fatalf("up: %q", l)
	}

	// De grafieken van PostgreSQL.
	var m metricsPanelsView
	if s := c.do("GET", base+"/"+cid+"/metrics", nil, &m); s != http.StatusOK ||
		m.ids() != "connections:count transactions:per_second cache_hits:percent lag:seconds size:bytes" {
		t.Fatalf("grafieken: %d %+v", s, m)
	}
}
