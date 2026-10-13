package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// stream doet een POST die een stroom van de assistent teruggeeft, zoals
// een analyse of een uitleg, en leest de hele stroom.
func (c *client) streamPost(path string, body any) (int, []streamEvent) {
	c.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.base+path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var e apiErr
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return resp.StatusCode, []streamEvent{{Type: "http", Data: map[string]any{"code": e.Code, "message": e.Message}}}
	}
	var out []streamEvent
	sc := bufio.NewScanner(resp.Body)
	var typ string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			typ = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev := streamEvent{Type: typ}
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.Data)
			out = append(out, ev)
		}
	}
	return resp.StatusCode, out
}

type incidentAIView struct {
	AI *struct {
		Text           *string `json:"text"`
		Hidden         bool    `json:"hidden"`
		Model          string  `json:"model"`
		At             string  `json:"at"`
		By             *string `json:"by"`
		Automatic      bool    `json:"automatic"`
		CostUsd        float64 `json:"cost_usd"`
		ConversationID *string `json:"conversation_id"`
	} `json:"ai"`
}

// firstPrompt is de tekst van de eerste vraag aan het model: de context en
// de opdracht.
func firstPrompt(t *testing.T, r claudeRequest) (context, prompt, effort string) {
	t.Helper()
	var body struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	if err := json.Unmarshal([]byte(r.Raw), &body); err != nil || len(body.Messages) == 0 || len(body.Messages[0].Content) != 2 {
		t.Fatalf("aanvraag: %v %s", err, r.Raw[:min(len(r.Raw), 300)])
	}
	return body.Messages[0].Content[0].Text, body.Messages[0].Content[1].Text, body.OutputConfig.Effort
}

const analysisAnswer = "## In het kort\nWeb was verminderd doordat nginx op web01 faalde.\n\n" +
	"## Waarschijnlijke oorzaak\nDe upgrade van nginx naar 1.26.3, twee minuten voor het eerste symptoom.\n\n" +
	"## Nog te controleren\n- de foutmelding van nginx\n\n## Wat je kunt doen\n- [web01 bekijken](/nodes/detail?id=x)"

func TestAssistantAnalyze(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login viewer: %d", s)
	}
	_, _, _, incID := whatBrokeFleet(t, e, c)
	path := "/api/v1/incidents/" + incID.String() + "/analyze"

	// Zolang de assistent uit staat, kan het niet.
	if s, evs := c.streamPost(path, nil); s != http.StatusConflict || evs[0].Data["code"] != "disabled" {
		t.Fatalf("uit: %d %+v", s, evs)
	}
	fc := enableAssistant(t, e, c, false)
	if s, _ := v.streamPost(path, nil); s != http.StatusForbidden {
		t.Fatalf("viewer: %d", s)
	}
	if s, _ := c.streamPost("/api/v1/incidents/"+uuid.NewString()+"/analyze", nil); s != http.StatusNotFound {
		t.Fatalf("onbekend incident: %d", s)
	}

	// Ronde 1 kijkt naar het overzicht, ronde 2 schrijft de analyse.
	fc.then(
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "tool_use", 4000, 150,
				block{text: "Ik kijk eerst naar het overzicht."},
				block{id: "toolu_1", name: "overzicht", input: `{}`})
		},
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "end_turn", 6000, 400, block{text: analysisAnswer})
		},
	)
	s, evs := c.streamPost(path, nil)
	if s != http.StatusOK || len(streamOf(evs, "error")) != 0 {
		t.Fatalf("analyse: %d %+v", s, evs)
	}
	if tools := streamOf(evs, "tool"); len(tools) != 1 || tools[0]["label"] != "Overzicht van de fleet lezen" {
		t.Fatalf("tools = %+v", tools)
	}
	done := streamOf(evs, "done")
	convID, _ := done[0]["conversation_id"].(string)
	if len(done) != 1 || convID == "" || done[0]["note"] != "" {
		t.Fatalf("done = %+v", done)
	}

	// De opdracht: grondig, met de gegevens van de tool incident erbij en
	// de vier kopjes.
	reqs := fc.requests()
	ctxText, prompt, effort := firstPrompt(t, reqs[0])
	if effort != "high" || !strings.Contains(ctxText, "De vraag komt van admin (beheerder)") || strings.Contains(ctxText, "begon op de pagina") {
		t.Fatalf("context %q, effort %q", ctxText, effort)
	}
	for _, want := range []string{
		`Analyseer incident #1, "Cluster Web is verminderd"`, "## In het kort", "## Waarschijnlijke oorzaak", "## Nog te controleren",
		"## Wat je kunt doen", "Gegevens van ClusterForge (tool incident):", `"analyse":`, "1.26.3", `"tijdlijn":`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("opdracht mist %q", want)
		}
	}

	// Het resultaat staat bij het incident, zonder de inleiding.
	var d incidentAIView
	if s := c.do("GET", "/api/v1/incidents/"+incID.String(), nil, &d); s != http.StatusOK || d.AI == nil {
		t.Fatalf("incident: %d %+v", s, d)
	}
	wantCost := (4000.0*4 + 150*20 + 6000*4 + 400*20) / 1e6
	if ai := d.AI; ai.Text == nil || *ai.Text != analysisAnswer || ai.Hidden || ai.Model != "claude-opus-5-5" || ai.By == nil || *ai.By != "admin" ||
		ai.Automatic || ai.ConversationID == nil || *ai.ConversationID != convID || ai.CostUsd < wantCost-1e-6 || ai.CostUsd > wantCost+1e-6 {
		t.Fatalf("ai = %+v", *ai)
	}
	// Een viewer ziet dat er een analyse is, maar niet wat erin staat: de
	// tools lazen met de rechten van een beheerder.
	var dv incidentAIView
	if s := v.do("GET", "/api/v1/incidents/"+incID.String(), nil, &dv); s != http.StatusOK || dv.AI == nil || !dv.AI.Hidden ||
		dv.AI.Text != nil || dv.AI.ConversationID != nil {
		t.Fatalf("viewer: %d %+v", s, dv.AI)
	}

	// Het gesprek staat bij de gesprekken, en je kunt erin doorvragen.
	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Title  string `json:"title"`
			CanAsk bool   `json:"can_ask"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/assistant/conversations", nil, &list)
	if len(list.Items) != 1 || list.Items[0].ID != convID || list.Items[0].Kind != "analysis" || !list.Items[0].CanAsk ||
		list.Items[0].Title != "Analyse van incident #1: Cluster Web is verminderd" {
		t.Fatalf("gesprekken = %+v", list.Items)
	}
	_, conv, turns := c.conversation(convID)
	if conv.SubjectType != "incident" || len(turns) != 1 || turns[0].Question != "Analyseer incident #1: Cluster Web is verminderd." ||
		!strings.Contains(turns[0].Answer, "## In het kort") {
		t.Fatalf("gesprek %+v, beurten %+v", conv, turns)
	}

	// Het logboek.
	var audit struct {
		Items []struct {
			Action  string `json:"action"`
			Summary string `json:"summary"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/audit?category=incidents", nil, &audit)
	if len(audit.Items) == 0 || audit.Items[0].Action != "incident.analyzed" ||
		audit.Items[0].Summary != "Incident #1 geanalyseerd door de assistent, $0,05 (claude-opus-5-5)" {
		t.Fatalf("logboek incident = %+v", audit.Items)
	}
	c.do("GET", "/api/v1/audit?category=assistant", nil, &audit)
	if len(audit.Items) == 0 || audit.Items[0].Summary != "Analyse door de assistent met 1 tool (overzicht), $0,05 (claude-opus-5-5)" {
		t.Fatalf("logboek assistent = %+v", audit.Items)
	}

	// Weigert het model, dan blijft de vorige analyse staan.
	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-opus-5-5", "refusal", 100, 5, block{text: "Nee."})
	})
	s, evs = c.streamPost(path, nil)
	if done := streamOf(evs, "done"); s != http.StatusOK || len(done) != 1 || done[0]["note"] != "Het model weigerde deze vraag te beantwoorden." {
		t.Fatalf("weigering: %d %+v", s, evs)
	}
	c.do("GET", "/api/v1/incidents/"+incID.String(), nil, &d)
	if *d.AI.Text != analysisAnswer || *d.AI.ConversationID != convID {
		t.Fatalf("na weigering: %+v", d.AI)
	}

	// Mag een viewer de assistent gebruiken, dan vervangt hij de analyse van
	// een beheerder nog niet door een met minder gegevens.
	fc = enableAssistant(t, e, c, true)
	if s, evs := v.streamPost(path, nil); s != http.StatusForbidden || evs[0].Data["code"] != "forbidden" {
		t.Fatalf("viewer vervangt de analyse van een beheerder: %d %+v", s, evs)
	}
	// Zonder analyse leest de zijne met zijn rechten, en ziet hij haar.
	if _, err := e.pool.Exec(ctx, `UPDATE incidents SET ai_analysis = NULL, ai_at = NULL, ai_scope = '' WHERE id = $1`, incID); err != nil {
		t.Fatal(err)
	}
	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-opus-5-5", "end_turn", 100, 50, block{text: analysisAnswer})
	})
	if s, evs := v.streamPost(path, nil); s != http.StatusOK || len(streamOf(evs, "error")) != 0 {
		t.Fatalf("viewer analyseert: %d %+v", s, evs)
	}
	if ctxText, _, _ := firstPrompt(t, fc.requests()[len(fc.requests())-1]); !strings.Contains(ctxText, "kijker (viewer)") {
		t.Fatalf("context viewer: %q", ctxText)
	}
	v.do("GET", "/api/v1/incidents/"+incID.String(), nil, &dv)
	if dv.AI == nil || dv.AI.Hidden || dv.AI.Text == nil || *dv.AI.Text != analysisAnswer || dv.AI.By == nil || *dv.AI.By != "kijker" ||
		dv.AI.ConversationID == nil {
		t.Fatalf("viewer na eigen analyse: %+v", dv.AI)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM assistant_conversations WHERE kind = 'analysis'`).Scan(&n)
	if n != 3 {
		t.Fatalf("%d gesprekken met een analyse", n)
	}
}

func TestAssistantAutoAnalyze(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	cl, _, _, incID := whatBrokeFleet(t, e, c)
	q := store.New(e.pool)
	fc := enableAssistant(t, e, c, false)

	// Andere incidenten die niet automatisch gaan: in test, door een test,
	// met ernst laag, of te oud.
	var testCl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web-test", "name": "Web test", "type": "nginx", "environment": "test"}, &testCl)
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		cluster  string
		severity string
		byTest   bool
		at       time.Time
	}{
		{testCl.ID, "critical", false, now.Add(-10 * time.Minute)},
		{cl.ID, "high", true, now.Add(-10 * time.Minute)},
		{cl.ID, "low", false, now.Add(-10 * time.Minute)},
		{cl.ID, "high", false, now.Add(-3 * time.Hour)},
	} {
		inc, err := q.CreateIncident(ctx, store.CreateIncidentParams{Title: "Ander incident", Severity: tc.severity, StartedAt: tc.at, DetectedAt: tc.at,
			CausedByTest: tc.byTest})
		if err != nil {
			t.Fatal(err)
		}
		clusterID := uuid.MustParse(tc.cluster)
		if err := q.UpsertIncidentSubject(ctx, store.UpsertIncidentSubjectParams{IncidentID: inc.ID, SubjectType: "cluster", SubjectID: clusterID,
			Name: "x", ClusterID: &clusterID, Since: tc.at, FirstStatus: "down", WorstStatus: "down", LastStatus: "down"}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE incidents SET analysis = '{}'::jsonb WHERE id = $1`, inc.ID); err != nil {
			t.Fatal(err)
		}
	}

	// Standaard staat automatisch analyseren uit.
	if err := e.asst.AutoAnalyze(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.asst.Wait()
	if n := len(fc.requests()); n != 0 {
		t.Fatalf("uit, toch %d aanvragen", n)
	}
	var st assistantSettings
	if s := c.do("PUT", "/api/v1/assistant/settings", map[string]any{
		"enabled": true, "model": "claude-opus-5-5", "monthly_budget_usd": 20, "viewer_access": false, "auto_analyze": true,
		"password": "een-lang-wachtwoord",
	}, &st); s != http.StatusOK {
		t.Fatalf("aanzetten: %d", s)
	}
	var raw map[string]any
	c.do("GET", "/api/v1/assistant/settings", nil, &raw)
	if raw["auto_analyze"] != true {
		t.Fatalf("instellingen: %+v", raw)
	}

	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-opus-5-5", "end_turn", 1000, 100, block{text: analysisAnswer})
	})
	if err := e.asst.AutoAnalyze(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.asst.Wait()
	reqs := fc.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d aanvragen", len(reqs))
	}
	if ctxText, prompt, _ := firstPrompt(t, reqs[0]); !strings.Contains(ctxText, "De vraag komt van ClusterForge (beheerder)") ||
		!strings.Contains(prompt, "Analyseer incident #1,") {
		t.Fatalf("context %q", ctxText)
	}
	var d incidentAIView
	c.do("GET", "/api/v1/incidents/"+incID.String(), nil, &d)
	if d.AI == nil || !d.AI.Automatic || d.AI.By != nil || d.AI.Text == nil || *d.AI.Text != analysisAnswer || d.AI.ConversationID == nil {
		t.Fatalf("automatisch: %+v", d.AI)
	}
	// Het gesprek is van ClusterForge zelf: een beheerder ziet het bij
	// iedereen, maar kan er niet in doorvragen.
	var list struct {
		Items []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Kind     string `json:"kind"`
			CanAsk   bool   `json:"can_ask"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/assistant/conversations?all=true", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Username != "" || list.Items[0].Kind != "analysis" || list.Items[0].CanAsk {
		t.Fatalf("gesprekken = %+v", list.Items)
	}
	if s, evs := c.ask(list.Items[0].ID, "En nu?"); s != http.StatusNotFound {
		t.Fatalf("doorvragen in een automatisch gesprek: %d %+v", s, evs)
	}
	var audit struct {
		Items []struct {
			Summary string `json:"summary"`
			Actor   struct {
				Type string `json:"type"`
			} `json:"actor"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/audit?category=incidents", nil, &audit)
	if len(audit.Items) == 0 || audit.Items[0].Summary != "Incident #1 automatisch geanalyseerd door de assistent, $0,01 (claude-opus-5-5)" ||
		audit.Items[0].Actor.Type != "system" {
		t.Fatalf("logboek = %+v", audit.Items)
	}

	// Elk incident één keer.
	if err := e.asst.AutoAnalyze(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.asst.Wait()
	if n := len(fc.requests()); n != 1 {
		t.Fatalf("tweede ronde: %d aanvragen", n)
	}

	// Hoogstens tien per dag: met tien analyses vandaag wacht een nieuw
	// incident tot morgen.
	inc, err := q.CreateIncident(ctx, store.CreateIncidentParams{Title: "Nog een", Severity: "critical", StartedAt: now, DetectedAt: now.Add(-5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	clusterID := uuid.MustParse(cl.ID)
	_ = q.UpsertIncidentSubject(ctx, store.UpsertIncidentSubjectParams{IncidentID: inc.ID, SubjectType: "cluster", SubjectID: clusterID, Name: "Web",
		ClusterID: &clusterID, Since: now, FirstStatus: "down", WorstStatus: "down", LastStatus: "down"})
	_, _ = e.pool.Exec(ctx, `UPDATE incidents SET analysis = '{}'::jsonb WHERE id = $1`, inc.ID)
	for i := 0; i < assistant.MaxAutoPerDay-1; i++ {
		if _, err := e.pool.Exec(ctx, `INSERT INTO assistant_conversations (kind, title) VALUES ('analysis', 'oud')`); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.asst.AutoAnalyze(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.asst.Wait()
	if n := len(fc.requests()); n != 1 {
		t.Fatalf("boven het maximum: %d aanvragen", n)
	}
	// Morgen wel.
	if err := e.asst.AutoAnalyze(ctx, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.asst.Wait()
	if n := len(fc.requests()); n != 1 {
		t.Fatalf("morgen is het incident te oud: %d aanvragen", n)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE assistant_conversations SET created_at = now() - interval '2 days' WHERE title = 'oud'`); err != nil {
		t.Fatal(err)
	}
	if err := e.asst.AutoAnalyze(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.asst.Wait()
	if n := len(fc.requests()); n != 2 {
		t.Fatalf("na de dag: %d aanvragen", n)
	}
}

// spofFleet zet Web neer met web01 en web02 allebei op pve1, op lokale
// storage, en rekent de health score.
func spofFleet(t *testing.T, e *testEnv, c *client) (cl cluster) {
	t.Helper()
	ctx := context.Background()
	pve, srv, fp := newPVE(t)
	pve.SetGuestStatus(101, "running", "pve1")
	pve.SetGuestStatus(102, "running", "pve1")
	for _, vmid := range []int{101, 102} {
		pve.SetGuestConfig(vmid, "scsi0", "local:vm-"+strconv.Itoa(vmid)+"-disk-0,size=32G")
		pve.SetGuestConfig(vmid, "net0", "virtio=BC:24:11:00:01:0"+strconv.Itoa(vmid-100)+",bridge=vmbr0")
	}
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != http.StatusCreated {
		t.Fatalf("koppelen: %d", s)
	}
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	for _, n := range []struct {
		name string
		vmid int
	}{{"web01", 101}, {"web02", 102}} {
		if s := c.do("POST", "/api/v1/nodes", map[string]any{
			"hostname": n.name, "cluster_id": cl.ID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": n.vmid},
		}, nil); s != http.StatusCreated {
			t.Fatalf("node %s: %d", n.name, s)
		}
	}
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != http.StatusOK {
		t.Fatalf("sync: %d", s)
	}
	if err := e.pve.ReadConfigs(ctx, uuid.MustParse(conn.ID)); err != nil {
		t.Fatal(err)
	}
	e.risk.Warmup = 0
	if err := e.risk.Compute(ctx); err != nil {
		t.Fatal(err)
	}
	return cl
}

func TestAssistantExplain(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login viewer: %d", s)
	}
	cl := spofFleet(t, e, c)
	var view clusterRiskView
	c.do("GET", "/api/v1/clusters/"+cl.ID+"/risk", nil, &view)
	same := view.findings("redundancy.same_host")
	if len(same) != 1 {
		t.Fatalf("redundantie: %+v", view.card(t, "redundancy"))
	}
	f := same[0]
	finding := map[string]any{"check": f.Check, "subject_type": f.Subject.Type, "subject_id": f.Subject.ID}

	if s, _ := c.streamPost("/api/v1/assistant/explain", finding); s != http.StatusForbidden {
		t.Fatalf("uit: %d", s)
	}
	fc := enableAssistant(t, e, c, true)
	for _, bad := range []map[string]any{
		{},
		{"check": f.Check},
		{"check": f.Check, "subject_type": f.Subject.Type, "subject_id": f.Subject.ID, "spof": "host:x"},
	} {
		if s, _ := c.streamPost("/api/v1/assistant/explain", bad); s != http.StatusBadRequest {
			t.Errorf("%v: %d", bad, s)
		}
	}
	if s, _ := c.streamPost("/api/v1/assistant/explain", map[string]any{"check": "redundancy.same_host", "subject_type": "cluster",
		"subject_id": uuid.NewString()}); s != http.StatusNotFound {
		t.Fatalf("onbekende bevinding: %d", s)
	}
	if s, _ := c.streamPost("/api/v1/assistant/explain", map[string]any{"spof": "host:onbekend"}); s != http.StatusNotFound {
		t.Fatalf("onbekende SPOF: %d", s)
	}
	if n := len(fc.requests()); n != 0 {
		t.Fatalf("%d aanvragen voor foute invoer", n)
	}

	// Een bevinding.
	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-opus-5-5", "end_turn", 2000, 200, block{text: "Beide nodes staan op pve1."})
	})
	s, evs := c.streamPost("/api/v1/assistant/explain", finding)
	done := streamOf(evs, "done")
	if s != http.StatusOK || streamText(evs) != "Beide nodes staan op pve1." || len(done) != 1 || done[0]["conversation_id"] == "" {
		t.Fatalf("uitleg: %d %+v", s, evs)
	}
	_, prompt, effort := firstPrompt(t, fc.requests()[0])
	if effort != "medium" || !strings.Contains(prompt, "op de kaart Redundantie van cluster Web (prod): "+strconv.Quote(f.Text)) ||
		!strings.Contains(prompt, `"check":"redundancy.same_host"`) || !strings.Contains(prompt, `"gain":`) {
		t.Fatalf("opdracht:\n%s", prompt)
	}
	_, conv, turns := c.conversation(done[0]["conversation_id"].(string))
	if conv.SubjectType != "cluster" || len(turns) != 1 || turns[0].Question != "Leg de bevinding op de kaart Redundantie van cluster Web (prod) uit: "+f.Text ||
		conv.Title != title80("Uitleg: "+f.Text) {
		t.Fatalf("gesprek %+v %+v", conv, turns)
	}

	// Een SPOF.
	type spofItem struct {
		Component struct{ Key, Kind, Name string }
	}
	var spofs struct {
		Spofs []spofItem `json:"spofs"`
	}
	c.do("GET", "/api/v1/risk/spof", nil, &spofs)
	i := slices.IndexFunc(spofs.Spofs, func(sp spofItem) bool { return sp.Component.Kind == "host" })
	if i < 0 {
		t.Fatalf("SPOF's: %+v", spofs)
	}
	s, evs = c.streamPost("/api/v1/assistant/explain", map[string]any{"spof": spofs.Spofs[i].Component.Key})
	if s != http.StatusOK || len(streamOf(evs, "error")) != 0 {
		t.Fatalf("SPOF: %d %+v", s, evs)
	}
	_, prompt, _ = firstPrompt(t, fc.requests()[1])
	if !strings.Contains(prompt, "Leg uit waarom Proxmox-host pve1 (Thuislab) een single point of failure is.") || !strings.Contains(prompt, `"name":"Web"`) {
		t.Fatalf("opdracht SPOF:\n%s", prompt)
	}

	// Een viewer krijgt de bevinding over tweestapsverificatie zonder de naam
	// van de beheerder.
	var ov riskOverview
	v.do("GET", "/api/v1/risk", nil, &ov)
	j := slices.IndexFunc(ov.Platform.Findings, func(f riskFinding) bool { return f.Check == "platform.admin_totp" })
	if j < 0 {
		t.Fatalf("platform: %+v", ov.Platform.Findings)
	}
	totp := ov.Platform.Findings[j]
	if s, evs := v.streamPost("/api/v1/assistant/explain", map[string]any{"check": totp.Check, "subject_type": totp.Subject.Type,
		"subject_id": totp.Subject.ID}); s != http.StatusOK || len(streamOf(evs, "error")) != 0 {
		t.Fatalf("viewer: %d %+v", s, evs)
	}
	_, prompt, _ = firstPrompt(t, fc.requests()[2])
	if strings.Contains(prompt, `"name":"admin"`) || strings.Contains(prompt, "admin heeft") || !strings.Contains(prompt, `"name":""`) ||
		!strings.Contains(prompt, "op de kaart Platform") {
		t.Fatalf("opdracht viewer noemt de beheerder:\n%s", prompt)
	}
}

// title80 is een titel zoals de assistent hem inkort.
func title80(s string) string {
	r := []rune(s)
	if len(r) <= 80 {
		return s
	}
	return strings.TrimSpace(string(r[:79])) + "…"
}

func TestDocsSummary(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login viewer: %d", s)
	}
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod",
		"description": "De website"}, &cl)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": cl.ID}, nil)
	path := "/api/v1/docs/summary?cluster=" + cl.ID
	arch := func() testDoc {
		t.Helper()
		var d testDoc
		if s := c.do("GET", "/api/v1/docs/cluster/architectuur?cluster="+cl.ID, nil, &d); s != http.StatusOK {
			t.Fatalf("architectuur: %d", s)
		}
		return d
	}

	fc := enableAssistant(t, e, c, true)
	if s, _ := v.streamPost(path, nil); s != http.StatusForbidden {
		t.Fatalf("viewer: %d", s)
	}
	if s, _ := c.streamPost("/api/v1/docs/summary?cluster="+uuid.NewString(), nil); s != http.StatusNotFound {
		t.Fatalf("onbekend cluster: %d", s)
	}

	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-sonnet-5-5", "end_turn", 9000, 300, block{text: "Web is de website, met [één node](https://evil.example/a).\n\n" +
			"- Draait op web01 ![x](https://evil.example/p.png)\n- Geen back-ups bekend"})
	})
	s, evs := c.streamPost(path, nil)
	if s != http.StatusOK || len(streamOf(evs, "error")) != 0 {
		t.Fatalf("samenvatting: %d %+v", s, evs)
	}
	_, prompt, effort := firstPrompt(t, fc.requests()[0])
	if effort != "medium" || !strings.Contains(prompt, "documentatie van cluster Web (omgeving prod)") {
		t.Fatalf("opdracht: %q %s", effort, prompt[:min(len(prompt), 300)])
	}
	for _, want := range []string{"=== Architectuur ===", "=== Netwerk ===", "=== Configuratie ===", "=== Herstel ===", "=== Runbooks ===", "De website"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("opdracht mist %q", want)
		}
	}
	d := arch()
	want := "## In het kort\n\nWeb is de website, met één node.\n\n- Draait op web01\n- Geen back-ups bekend\n\n" +
		"*Samengevat door de assistent (Claude Sonnet 5.5) op "
	if !strings.Contains(d.Content, want) || strings.Contains(d.Content, "evil") || d.CauseText != "Samenvatting van de assistent gewijzigd" ||
		d.Version == nil {
		t.Fatalf("architectuur (%s):\n%s", d.CauseText, d.Content)
	}
	var idx struct {
		Clusters []struct {
			ID      string `json:"id"`
			Summary *struct {
				Model string `json:"model"`
				By    string `json:"by"`
			} `json:"summary"`
		} `json:"clusters"`
	}
	v.do("GET", "/api/v1/docs", nil, &idx)
	if len(idx.Clusters) != 1 || idx.Clusters[0].Summary == nil || idx.Clusters[0].Summary.By != "admin" ||
		idx.Clusters[0].Summary.Model != "claude-sonnet-5-5" {
		t.Fatalf("index: %+v", idx.Clusters)
	}

	// Een nieuwe samenvatting leest de vorige niet.
	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-opus-5-5", "end_turn", 9000, 300, block{text: "Web is de website."})
	})
	if s, evs := c.streamPost(path, nil); s != http.StatusOK || len(streamOf(evs, "error")) != 0 {
		t.Fatalf("tweede: %d %+v", s, evs)
	}
	if _, prompt, _ := firstPrompt(t, fc.requests()[1]); strings.Contains(prompt, "In het kort") && strings.Contains(prompt, "Draait op web01") {
		t.Fatalf("de vorige samenvatting ging mee:\n%s", prompt)
	}
	if d := arch(); !strings.Contains(d.Content, "## In het kort\n\nWeb is de website.\n\n*Samengevat door de assistent (Claude Opus 5.5)") {
		t.Fatalf("tweede samenvatting:\n%s", d.Content)
	}

	// Een leeg antwoord bewaart niets.
	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-opus-5-5", "end_turn", 100, 10, block{text: "![alleen](https://evil.example/p.png)"})
	})
	if s, evs := c.streamPost(path, nil); s != http.StatusOK || len(streamOf(evs, "error")) != 1 {
		t.Fatalf("leeg: %d %+v", s, evs)
	}
	if d := arch(); !strings.Contains(d.Content, "Web is de website.") {
		t.Fatalf("na leeg antwoord:\n%s", d.Content)
	}

	// Weghalen.
	if s := v.do("DELETE", path, nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer haalt weg: %d", s)
	}
	if s := c.do("DELETE", path, nil, nil); s != http.StatusNoContent {
		t.Fatalf("weghalen: %d", s)
	}
	if d := arch(); strings.Contains(d.Content, "In het kort") {
		t.Fatalf("na weghalen:\n%s", d.Content)
	}
	if s := c.do("DELETE", path, nil, nil); s != http.StatusNotFound {
		t.Fatalf("tweede keer weghalen: %d", s)
	}
	var audit struct {
		Items []struct {
			Summary string `json:"summary"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/audit?category=docs", nil, &audit)
	var sums []string
	for _, it := range audit.Items {
		sums = append(sums, it.Summary)
	}
	if len(sums) != 3 || sums[0] != "Samenvatting 'In het kort' van Web weggehaald" ||
		sums[2] != "Samenvatting 'In het kort' van Web geschreven door de assistent, $0,02 (claude-sonnet-5-5)" {
		t.Fatalf("logboek = %q", sums)
	}
}

// TestCardNames: de assistent kent elke kaart bij naam, zodat de uitleg
// van een bevinding zegt op welke kaart ze staat.
func TestCardNames(t *testing.T) {
	for card := range risk.CardLabels {
		if cardNames[card] == "" {
			t.Errorf("kaart %s heeft geen naam voor de assistent", card)
		}
	}
}
