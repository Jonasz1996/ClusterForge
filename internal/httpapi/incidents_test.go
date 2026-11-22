package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type incidentView struct {
	ID           string                `json:"id"`
	Number       int                   `json:"number"`
	Title        string                `json:"title"`
	Severity     string                `json:"severity"`
	Status       string                `json:"status"`
	ResolvedAt   *string               `json:"resolved_at"`
	CausedByTest bool                  `json:"caused_by_test"`
	Note         string                `json:"note"`
	Subjects     []incidentSubjectView `json:"subjects"`
}

type incidentSubjectView struct {
	Type        string  `json:"type"`
	Name        string  `json:"name"`
	FirstStatus string  `json:"first_status"`
	WorstStatus string  `json:"worst_status"`
	LastStatus  string  `json:"last_status"`
	Until       *string `json:"until"`
	Present     bool    `json:"present"`
}

type incidentDetail struct {
	Incident    incidentView `json:"incident"`
	History     []auditEntry `json:"history"`
	HistoryMore bool         `json:"history_more"`
}

func (c *client) incidents(query string) []incidentView {
	c.t.Helper()
	var l struct {
		Items []incidentView `json:"items"`
	}
	if s := c.do("GET", "/api/v1/incidents?"+query, nil, &l); s != http.StatusOK {
		c.t.Fatalf("incidenten: %d", s)
	}
	return l.Items
}

func titles(incs []incidentView) []string {
	var out []string
	for _, inc := range incs {
		out = append(out, inc.Status+" "+inc.Title)
	}
	return out
}

// TestIncidents is de acceptatie van mijlpaal 2 van fase 3: een split-brain
// op prod opent één kritiek incident met het cluster erin, een viewer ziet
// het met de geschiedenis maar zonder IP-adressen en mag geen notitie
// schrijven, en als het VIP weer op één node staat, sluit het incident na de
// wachttijd. Een losse node die down gaat, is een eigen incident.
func TestIncidents(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login viewer: %d", s)
	}

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "prod"}, &cl)
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.99.0.20"}, nil)
	var web01, web02, solo node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": cl.ID}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": cl.ID}, &web02)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "solo"}, &solo)
	a1, a2, a3 := &addrs{}, &addrs{}, &addrs{}
	a1.set("10.99.0.20", "192.0.2.11")
	a2.set("192.0.2.12")
	a3.set("192.0.2.13")
	startAgent(t, e, c, web01.ID, strings.Repeat("1", 32), a1)
	startAgent(t, e, c, web02.ID, strings.Repeat("2", 32), a2)
	stopSolo := startAgent(t, e, c, solo.ID, strings.Repeat("3", 32), a3)

	var d statusView
	eventually(t, "gezond cluster", func() bool {
		c.do("GET", "/api/v1/clusters/"+cl.ID, nil, &d)
		return d.Status == "healthy"
	})
	if got := c.incidents(""); len(got) != 0 {
		t.Fatalf("incidenten bij een gezond cluster: %v", titles(got))
	}

	// Beide nodes hebben het VIP: één kritiek incident.
	a2.set("192.0.2.12", "10.99.0.20")
	var inc incidentView
	eventually(t, "incident geopend", func() bool {
		got := c.incidents("")
		if len(got) == 1 {
			inc = got[0]
		}
		return len(got) == 1 && slices.ContainsFunc(inc.Subjects, func(s incidentSubjectView) bool { return s.Type == "cluster" })
	})
	if inc.Title != "Cluster LB is in split-brain" || inc.Severity != "critical" || inc.Status != "open" || inc.Number != 1 ||
		inc.CausedByTest || inc.ResolvedAt != nil {
		t.Fatalf("incident: %+v", inc)
	}
	sub := inc.Subjects[0]
	if sub.Name != "LB" || sub.FirstStatus != "split_brain" || sub.Until != nil || !sub.Present {
		t.Fatalf("onderwerp: %+v", sub)
	}
	if got := find(t, c.audit("category=incidents").Items, "incident.opened"); got.Summary != "Incident #1 geopend: Cluster LB is in split-brain (kritiek)" ||
		got.Cluster == nil || got.Cluster.Name != "LB" {
		t.Fatalf("logboek: %+v", got)
	}

	// Een viewer ziet het incident, maar mag geen notitie schrijven.
	if got := v.incidents(""); len(got) != 1 || got[0].ID != inc.ID {
		t.Fatalf("viewer: %v", titles(got))
	}
	if s := v.do("PUT", "/api/v1/incidents/"+inc.ID+"/note", map[string]any{"note": "kijk naar keepalived"}, nil); s != http.StatusForbidden {
		t.Fatalf("notitie van viewer: %d", s)
	}
	var withNote incidentView
	if s := c.do("PUT", "/api/v1/incidents/"+inc.ID+"/note", map[string]any{"note": "  VRRP-pakketten kwamen niet aan  "}, &withNote); s != http.StatusOK ||
		withNote.Note != "VRRP-pakketten kwamen niet aan" {
		t.Fatalf("notitie: %d %+v", s, withNote)
	}
	if s := c.do("PUT", "/api/v1/incidents/"+inc.ID+"/note", map[string]any{"note": strings.Repeat("x", 10001)}, nil); s != http.StatusBadRequest {
		t.Fatalf("te lange notitie: %d", s)
	}
	if s := c.do("PUT", "/api/v1/incidents/00000000-0000-0000-0000-000000000000/note", map[string]any{"note": "x"}, nil); s != http.StatusNotFound {
		t.Fatalf("onbekend incident: %d", s)
	}
	if got := find(t, c.audit("category=incidents").Items, "incident.note_updated"); got.Summary != "Notitie bij incident #1 bijgewerkt" ||
		got.IP == nil {
		t.Fatalf("notitie in het logboek: %+v", got)
	}

	// Het VIP staat weer alleen op web02: het incident sluit na de wachttijd.
	a1.set("192.0.2.11")
	eventually(t, "incident gesloten", func() bool {
		e.eval.Kick()
		got := c.incidents("status=resolved")
		if len(got) == 1 {
			inc = got[0]
		}
		return len(got) == 1
	})
	if inc.ResolvedAt == nil || inc.Subjects[0].Until == nil || inc.Subjects[0].LastStatus != "healthy" || inc.Severity != "critical" {
		t.Fatalf("gesloten: %+v", inc)
	}
	if got := find(t, c.audit("category=incidents").Items, "incident.resolved"); !strings.HasPrefix(got.Summary, "Incident #1 opgelost na ") {
		t.Fatalf("opgelost: %q", got.Summary)
	}

	// De geschiedenis: de overgangen van het cluster, het incident en de
	// VIP-wissel, oudste eerst; een viewer ziet geen IP-adres.
	var det incidentDetail
	if s := v.do("GET", "/api/v1/incidents/"+inc.ID, nil, &det); s != http.StatusOK {
		t.Fatalf("detail: %d", s)
	}
	var actions []string
	for _, h := range det.History {
		actions = append(actions, h.Action)
		if h.IP != nil || h.Session != nil {
			t.Fatalf("viewer ziet herkomst: %+v", h)
		}
		if h.Category == "security" {
			t.Fatalf("beveiliging in de geschiedenis: %+v", h)
		}
	}
	for _, want := range []string{"cluster.status_changed", "incident.opened", "incident.note_updated", "vip.owner_changed", "incident.resolved"} {
		if !slices.Contains(actions, want) {
			t.Fatalf("%s ontbreekt in %v", want, actions)
		}
	}
	if slices.Index(actions, "incident.opened") > slices.Index(actions, "incident.resolved") {
		t.Fatalf("niet oudste eerst: %v", actions)
	}
	if s := v.do("GET", "/api/v1/incidents/00000000-0000-0000-0000-000000000000", nil, nil); s != http.StatusNotFound {
		t.Fatalf("onbekend incident: %d", s)
	}

	// De VIP-wissel staat nu ook bij de nodes zelf in het logboek.
	find(t, c.audit("node="+web02.ID+"&category=status").Items, "vip.owner_changed")
	find(t, c.audit("node="+web01.ID+"&category=status").Items, "vip.owner_changed")

	// Een losse node die down gaat, is een eigen incident, met lage ernst.
	stopSolo()
	eventually(t, "solo down", func() bool {
		// Een heartbeat die nog onderweg was, maakt de node weer vers; zet
		// hem daarom elke ronde opnieuw terug.
		if _, err := e.pool.Exec(ctx, "UPDATE node_status SET heartbeat_at = now() - interval '5 minutes' WHERE node_id = $1", solo.ID); err != nil {
			t.Fatal(err)
		}
		e.eval.Kick()
		got := c.incidents("status=open")
		return len(got) == 1 && got[0].Title == "solo is down" && got[0].Severity == "low"
	})
	// Open eerst; het filter op cluster laat solo weg.
	if got := titles(c.incidents("")); !slices.Equal(got, []string{"open solo is down", "resolved Cluster LB is in split-brain"}) {
		t.Fatalf("lijst: %v", got)
	}
	if got := titles(c.incidents("cluster=" + cl.ID)); !slices.Equal(got, []string{"resolved Cluster LB is in split-brain"}) {
		t.Fatalf("op cluster: %v", got)
	}
	for _, q := range []string{"status=dicht", "limit=0", "from=2026-10-11T10:00:00Z&to=2026-10-11T09:00:00Z"} {
		if s := c.do("GET", "/api/v1/incidents?"+q, nil, nil); s != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, s)
		}
	}
}
