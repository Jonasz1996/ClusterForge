package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type timelineEntry struct {
	ID       string            `json:"id"`
	Ts       time.Time         `json:"ts"`
	From     time.Time         `json:"from"`
	Delay    string            `json:"delay"`
	Class    string            `json:"class"`
	Source   string            `json:"source"`
	Kind     string            `json:"kind"`
	Action   string            `json:"action"`
	Summary  string            `json:"summary"`
	Hostname string            `json:"hostname"`
	Cluster  string            `json:"cluster"`
	Link     string            `json:"link"`
	Detail   map[string]string `json:"detail"`
}

type timeline struct {
	Entries []timelineEntry `json:"entries"`
	HasMore bool            `json:"has_more"`
}

func (c *client) timeline(path string, query url.Values) timeline {
	c.t.Helper()
	var out timeline
	if s := c.do("GET", path+"?"+query.Encode(), nil, &out); s != http.StatusOK {
		c.t.Fatalf("GET %s: %d", path, s)
	}
	return out
}

func summaries(entries []timelineEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Class+" "+e.Summary)
	}
	return out
}

// TestWhatBroke is de acceptatie van mijlpaal 3 van fase 3 in de API: de
// tijdlijn van een cluster en een node, met filters en zonder IP-adres voor
// een viewer, de analyse bij een incident en de oorzaak in de lijst, en de
// grafieken over een vast venster.
func TestWhatBroke(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login viewer: %d", s)
	}
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	var web01, web02 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": cl.ID}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": cl.ID}, &web02)
	clusterID, web01ID := uuid.MustParse(cl.ID), uuid.MustParse(web01.ID)

	q := store.New(e.pool)
	t0 := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	record := func(n string, ts time.Time, je journal.Entry) {
		t.Helper()
		je.NodeID, je.ClusterID, je.TS, je.Source = uuid.MustParse(n), &clusterID, ts, journal.SourceAgent
		if err := journal.Record(ctx, q, je); err != nil {
			t.Fatal(err)
		}
	}
	record(web01.ID, t0.Add(-5*time.Minute), journal.Entry{Kind: journal.KindLogin, Subject: "root", Action: "login",
		Detail: map[string]string{"ip": "192.0.2.44", "method": "publickey"}})
	record(web01.ID, t0.Add(-160*time.Second), journal.Entry{Kind: journal.KindPackage, Subject: "nginx", Action: "upgrade",
		From: "1.26.0", To: "1.26.3"})
	record(web01.ID, t0.Add(-120*time.Second), journal.Entry{Kind: journal.KindService, Subject: "nginx.service", Action: "failed"})
	record(web02.ID, t0.Add(-60*time.Second), journal.Entry{Kind: journal.KindFile, Subject: "/etc/hosts", Action: "modified"})
	payload, _ := json.Marshal(map[string]any{"hostname": "web01", "from": "healthy", "to": "degraded", "reason": "service nginx is gefaald"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO events (ts, actor_type, subject_type, subject_id, cluster_id, action, payload)
		VALUES ($1, 'system', 'node', $2, $3, 'node.status_changed', $4)`, t0, web01.ID, cl.ID, payload); err != nil {
		t.Fatal(err)
	}

	// De tijdlijn van het cluster: oudste eerst, het logboek en het journaal
	// door elkaar, met het venster bij de statusovergang.
	from := url.Values{"from": {t0.Add(-time.Hour).Format(time.RFC3339)}}
	tl := c.timeline("/api/v1/clusters/"+cl.ID+"/timeline", from)
	got := summaries(tl.Entries)
	want := []string{
		"change SSH-login als root vanaf 192.0.2.44 (sleutel)",
		"change nginx bijgewerkt van 1.26.0 naar 1.26.3",
		"symptom nginx.service mislukt",
		"change /etc/hosts aangepast",
	}
	if len(got) < 5 || !slices.Equal(got[:4], want) || tl.Entries[4].Action != "node.status_changed" || tl.HasMore {
		t.Fatalf("tijdlijn = %q", got)
	}
	if st := tl.Entries[4]; st.Class != "symptom" || st.Delay == "" || !st.From.Before(st.Ts) || st.Cluster != "Web" ||
		st.Link != "/nodes/detail?id="+web01.ID {
		t.Fatalf("statusovergang = %+v", st)
	}
	// De nodes en het cluster zelf zijn net aangemaakt: info.
	if last := tl.Entries[len(tl.Entries)-1]; last.Class != "info" || last.Source != "log" {
		t.Fatalf("laatste = %+v", last)
	}
	// Een viewer ziet de login zonder IP-adres.
	vtl := v.timeline("/api/v1/clusters/"+cl.ID+"/timeline", from)
	if s := vtl.Entries[0]; s.Summary != "SSH-login als root (sleutel)" || s.Detail["ip"] != "" {
		t.Fatalf("viewer: %+v", s)
	}
	// Alleen veranderingen, en de laatste twee.
	only := url.Values{"from": from["from"], "class": {"change"}, "limit": {"2"}}
	if tl := c.timeline("/api/v1/clusters/"+cl.ID+"/timeline", only); !tl.HasMore ||
		!slices.Equal(summaries(tl.Entries), []string{"change nginx bijgewerkt van 1.26.0 naar 1.26.3", "change /etc/hosts aangepast"}) {
		t.Fatalf("alleen veranderingen = %q, meer %v", summaries(tl.Entries), tl.HasMore)
	}
	// De node ziet alleen zichzelf.
	ntl := c.timeline("/api/v1/nodes/"+web01.ID+"/timeline", url.Values{"from": from["from"], "class": {"change,symptom"}})
	for _, en := range ntl.Entries {
		if en.Hostname != "web01" {
			t.Fatalf("node-tijdlijn heeft %+v", en)
		}
	}
	if len(ntl.Entries) != 4 {
		t.Fatalf("node-tijdlijn = %q", summaries(ntl.Entries))
	}
	for _, bad := range []string{
		"/api/v1/clusters/" + cl.ID + "/timeline?class=oorzaak",
		"/api/v1/clusters/" + cl.ID + "/timeline?limit=0",
		"/api/v1/nodes/" + web01.ID + "/timeline?from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z",
		"/api/v1/nodes/" + web01.ID + "/timeline?from=2026-01-01T00:00:00Z&to=2026-10-01T00:00:00Z",
	} {
		if s := c.do("GET", bad, nil, nil); s != http.StatusBadRequest {
			t.Errorf("GET %s: %d", bad, s)
		}
	}
	if s := c.do("GET", "/api/v1/nodes/"+uuid.NewString()+"/timeline", nil, nil); s != http.StatusNotFound {
		t.Errorf("onbekende node: %d", s)
	}

	// Een incident: de analyse loopt op de achtergrond.
	inc, err := q.CreateIncident(ctx, store.CreateIncidentParams{Title: "Cluster Web is verminderd", Severity: "high", StartedAt: t0, DetectedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []store.UpsertIncidentSubjectParams{
		{SubjectType: "cluster", SubjectID: clusterID, Name: "Web"},
		{SubjectType: "node", SubjectID: web01ID, Name: "web01"},
	} {
		sub.IncidentID, sub.ClusterID, sub.Since = inc.ID, &clusterID, t0
		sub.FirstStatus, sub.WorstStatus, sub.LastStatus = "degraded", "degraded", "degraded"
		if err := q.UpsertIncidentSubject(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	e.ana.Kick()
	var d struct {
		incidentDetail
		Analysis *struct {
			Version      int    `json:"version"`
			Conclusion   string `json:"conclusion"`
			FirstSymptom struct {
				EntryID string `json:"entry_id"`
				Summary string `json:"summary"`
			} `json:"first_symptom"`
			Causes []struct {
				EntryID  string   `json:"entry_id"`
				Level    string   `json:"level"`
				Summary  string   `json:"summary"`
				Hostname string   `json:"hostname"`
				Reasons  []string `json:"reasons"`
			} `json:"causes"`
			MetricsUnknown bool       `json:"metrics_unknown"`
			AnalyzedAt     *time.Time `json:"analyzed_at"`
		} `json:"analysis"`
		Timeline     []timelineEntry `json:"timeline"`
		TimelineMore bool            `json:"timeline_more"`
	}
	eventually(t, "analyse van het incident", func() bool {
		return v.do("GET", "/api/v1/incidents/"+inc.ID.String(), nil, &d) == http.StatusOK && d.Analysis != nil
	})
	a := d.Analysis
	if a.Version != 1 || a.AnalyzedAt == nil || len(a.Causes) == 0 || !strings.HasPrefix(a.FirstSymptom.Summary, "nginx.service mislukt") {
		t.Fatalf("analyse = %+v", a)
	}
	top := a.Causes[0]
	if top.Level != "likely" || top.Summary != "nginx bijgewerkt van 1.26.0 naar 1.26.3" || top.Hostname != "web01" ||
		!slices.Contains(top.Reasons, "nginx op web01 faalde 40 s na deze update van nginx.") {
		t.Fatalf("oorzaak = %+v", top)
	}
	// De fake VictoriaMetrics geeft één punt: te weinig voor een afwijking,
	// maar wel te lezen.
	if a.MetricsUnknown {
		t.Fatal("metrics onbekend")
	}
	if !slices.ContainsFunc(d.Timeline, func(en timelineEntry) bool { return en.ID == a.FirstSymptom.EntryID }) {
		t.Fatalf("het eerste symptoom staat niet in de tijdlijn: %q", summaries(d.Timeline))
	}
	for _, en := range d.Timeline {
		if strings.Contains(en.Summary, "192.0.2.44") {
			t.Fatalf("een viewer ziet een IP-adres in de tijdlijn: %+v", en)
		}
	}
	list := v.incidents("")
	var cause struct {
		Items []struct {
			Cause *struct {
				Summary string `json:"summary"`
				Level   string `json:"level"`
			} `json:"cause"`
		} `json:"items"`
	}
	v.do("GET", "/api/v1/incidents", nil, &cause)
	if len(list) != 1 || cause.Items[0].Cause == nil || cause.Items[0].Cause.Summary != "nginx bijgewerkt van 1.26.0 naar 1.26.3 op web01" ||
		cause.Items[0].Cause.Level != "likely" {
		t.Fatalf("lijst = %+v", cause.Items)
	}

	// De grafieken over het venster van het incident.
	var m struct {
		Range       string `json:"range"`
		StepSeconds int    `json:"step_seconds"`
	}
	window := url.Values{"from": {t0.Add(-15 * time.Minute).Format(time.RFC3339)}, "to": {t0.Add(15 * time.Minute).Format(time.RFC3339)}}
	if s := c.do("GET", "/api/v1/nodes/"+web01.ID+"/metrics?"+window.Encode(), nil, &m); s != http.StatusOK || m.Range != "1h" || m.StepSeconds != 15 {
		t.Fatalf("venster: %d %+v", s, m)
	}
	window.Set("from", t0.Add(-5*time.Hour).Format(time.RFC3339))
	if s := c.do("GET", "/api/v1/clusters/"+cl.ID+"/metrics?"+window.Encode(), nil, &m); s != http.StatusOK || m.Range != "6h" {
		t.Fatalf("venster cluster: %d %+v", s, m)
	}
	for _, bad := range []url.Values{
		{"from": window["from"]},
		{"from": {t0.Format(time.RFC3339)}, "to": {t0.Add(-time.Minute).Format(time.RFC3339)}},
		{"from": {t0.Add(-8 * 24 * time.Hour).Format(time.RFC3339)}, "to": {t0.Format(time.RFC3339)}},
	} {
		if s := c.do("GET", "/api/v1/nodes/"+web01.ID+"/metrics?"+bad.Encode(), nil, nil); s != http.StatusBadRequest {
			t.Errorf("venster %v: %d", bad, s)
		}
	}
}
