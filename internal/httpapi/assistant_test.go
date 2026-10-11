package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// fakeClaude speelt de Messages API na: elke aanvraag krijgt het volgende
// antwoord uit het script, als stroom zoals de echte API die stuurt.
type fakeClaude struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []claudeRequest
	script []func(claudeRequest) (int, string)
}

type claudeRequest struct {
	Header http.Header
	Body   map[string]any
	Raw    string
}

func newFakeClaude(t *testing.T) *fakeClaude {
	f := &fakeClaude{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		req := claudeRequest{Header: r.Header.Clone(), Raw: string(raw)}
		_ = json.Unmarshal(raw, &req.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		var step func(claudeRequest) (int, string)
		if len(f.script) > 0 {
			step, f.script = f.script[0], f.script[1:]
		}
		f.mu.Unlock()
		status, body := http.StatusOK, sse("claude-opus-5-5", "end_turn", 100, 10, block{text: "OK"})
		if req.Body["stream"] != true {
			// Zonder stroom, zoals de test van de sleutel.
			b, _ := json.Marshal(map[string]any{
				"id": "msg_test", "type": "message", "role": "assistant", "model": req.Body["model"], "stop_reason": "end_turn",
				"content": []any{map[string]any{"type": "text", "text": "OK"}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2},
			})
			status, body = http.StatusOK, string(b)
		}
		if step != nil {
			status, body = step(req)
		}
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		if req.Body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeClaude) then(steps ...func(claudeRequest) (int, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, steps...)
}

func (f *fakeClaude) requests() []claudeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// block is een inhoudsblok van een nagespeeld antwoord: tekst, denken of een
// toolaanroep.
type block struct {
	text, thinking  string
	id, name, input string
}

func sse(model, stop string, in, out int, blocks ...block) string {
	var b strings.Builder
	ev := func(typ string, v any) {
		j, _ := json.Marshal(v)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", typ, j)
	}
	ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_" + uuid.NewString()[:8], "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": in, "output_tokens": 1, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
	}})
	for i, bl := range blocks {
		switch {
		case bl.thinking != "":
			ev("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
			ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "thinking_delta", "thinking": bl.thinking}})
			ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "signature_delta", "signature": "sig-" + bl.thinking}})
		case bl.name != "":
			ev("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "tool_use", "id": bl.id, "name": bl.name, "input": map[string]any{}}})
			ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": bl.input}})
		default:
			ev("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "text", "text": ""}})
			ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "text_delta", "text": bl.text}})
		}
		ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	ev("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": out}})
	ev("message_stop", map[string]any{"type": "message_stop"})
	return b.String()
}

type streamEvent struct {
	Type string
	Data map[string]any
}

// ask stelt een vraag en leest de hele stroom.
func (c *client) ask(conv, question string) (int, []streamEvent) {
	c.t.Helper()
	body, _ := json.Marshal(map[string]string{"question": question})
	req, _ := http.NewRequest("POST", c.base+"/api/v1/assistant/conversations/"+conv+"/messages", strings.NewReader(string(body)))
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
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		c.t.Fatalf("content-type %q", ct)
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

func streamText(evs []streamEvent) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Type == "text" {
			b.WriteString(ev.Data["text"].(string))
		}
	}
	return b.String()
}

func streamOf(evs []streamEvent, typ string) []map[string]any {
	var out []map[string]any
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev.Data)
		}
	}
	return out
}

type assistantSettings struct {
	Available        bool   `json:"available"`
	CanUse           bool   `json:"can_use"`
	Reason           string `json:"reason"`
	Enabled          bool   `json:"enabled"`
	HasKey           bool   `json:"has_key"`
	Model            string `json:"model"`
	MonthlyBudgetUsd int    `json:"monthly_budget_usd"`
	ViewerAccess     bool   `json:"viewer_access"`
	UpdatedBy        string `json:"updated_by"`
	Models           []struct {
		ID string `json:"id"`
	} `json:"models"`
}

type assistantConversation struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Username    string  `json:"username"`
	SubjectType string  `json:"subject_type"`
	CostUsd     float64 `json:"cost_usd"`
}

type assistantTurn struct {
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Models   []string `json:"models"`
	Note     string   `json:"note"`
	CostUsd  float64  `json:"cost_usd"`
	Input    int      `json:"input_tokens"`
	Output   int      `json:"output_tokens"`
	Tools    []struct {
		Name  string `json:"name"`
		Label string `json:"label"`
		Error bool   `json:"error"`
	} `json:"tools"`
}

func (c *client) conversation(id string) (int, assistantConversation, []assistantTurn) {
	c.t.Helper()
	var d struct {
		Conversation assistantConversation `json:"conversation"`
		Turns        []assistantTurn       `json:"turns"`
	}
	s := c.do("GET", "/api/v1/assistant/conversations/"+id, nil, &d)
	return s, d.Conversation, d.Turns
}

// whatBrokeFleet zet een cluster Web met web01 en web02 neer, met een
// nginx-upgrade die nginx liet falen, en een incident met zijn analyse.
func whatBrokeFleet(t *testing.T, e *testEnv, c *client) (cl cluster, web01, web02 node, incident uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": cl.ID}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": cl.ID}, &web02)
	clusterID := uuid.MustParse(cl.ID)
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
	payload, _ := json.Marshal(map[string]any{"hostname": "web01", "from": "healthy", "to": "degraded", "reason": "service nginx is gefaald"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO events (ts, actor_type, subject_type, subject_id, cluster_id, action, payload)
		VALUES ($1, 'system', 'node', $2, $3, 'node.status_changed', $4)`, t0, web01.ID, cl.ID, payload); err != nil {
		t.Fatal(err)
	}
	inc, err := q.CreateIncident(ctx, store.CreateIncidentParams{Title: "Cluster Web is verminderd", Severity: "high", StartedAt: t0, DetectedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []store.UpsertIncidentSubjectParams{
		{SubjectType: "cluster", SubjectID: clusterID, Name: "Web"},
		{SubjectType: "node", SubjectID: uuid.MustParse(web01.ID), Name: "web01"},
	} {
		sub.IncidentID, sub.ClusterID, sub.Since = inc.ID, &clusterID, t0
		sub.FirstStatus, sub.WorstStatus, sub.LastStatus = "degraded", "degraded", "degraded"
		if err := q.UpsertIncidentSubject(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	e.ana.Kick()
	eventually(t, "analyse van het incident", func() bool {
		var d struct {
			Analysis *json.RawMessage `json:"analysis"`
		}
		return c.do("GET", "/api/v1/incidents/"+inc.ID.String(), nil, &d) == http.StatusOK && d.Analysis != nil
	})
	return cl, web01, web02, inc.ID
}

// enableAssistant zet de assistent aan met een testsleutel tegen de
// nagespeelde API.
func enableAssistant(t *testing.T, e *testEnv, c *client, viewers bool) *fakeClaude {
	t.Helper()
	fc := newFakeClaude(t)
	e.asst.BaseURL = fc.srv.URL
	var st assistantSettings
	if s := c.do("PUT", "/api/v1/assistant/settings", map[string]any{
		"enabled": true, "api_key": "sk-ant-test-0123456789", "model": "claude-opus-5-5", "monthly_budget_usd": 20,
		"viewer_access": viewers, "password": "een-lang-wachtwoord",
	}, &st); s != http.StatusOK || !st.Enabled || !st.HasKey || !st.CanUse {
		t.Fatalf("aanzetten: %d %+v", s, st)
	}
	return fc
}

func TestAssistantSettings(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login viewer: %d", s)
	}
	var st assistantSettings
	if s := c.do("GET", "/api/v1/assistant/settings", nil, &st); s != http.StatusOK || !st.Available || st.Enabled || st.CanUse ||
		st.Model != "claude-opus-5-5" || st.MonthlyBudgetUsd != 20 || len(st.Models) != 3 || !strings.Contains(st.Reason, "staat uit") {
		t.Fatalf("standaard: %d %+v", s, st)
	}
	put := func(cl *client, body map[string]any) (int, apiErr) {
		var er apiErr
		s := cl.do("PUT", "/api/v1/assistant/settings", body, &er)
		return s, er
	}
	base := func(extra map[string]any) map[string]any {
		in := map[string]any{"enabled": true, "model": "claude-opus-5-5", "monthly_budget_usd": 20, "viewer_access": false,
			"api_key": "sk-ant-test-0123456789", "password": "een-lang-wachtwoord"}
		for k, val := range extra {
			if val == nil {
				delete(in, k)
			} else {
				in[k] = val
			}
		}
		return in
	}
	for _, tc := range []struct {
		name string
		body map[string]any
		code string
	}{
		{"zonder sleutel aan", base(map[string]any{"api_key": nil}), "validation"},
		{"geen sleutel van Anthropic", base(map[string]any{"api_key": "abc"}), "validation"},
		{"onbekend model", base(map[string]any{"model": "gpt"}), "validation"},
		{"budget 0", base(map[string]any{"monthly_budget_usd": 0}), "validation"},
		{"fout wachtwoord", base(map[string]any{"password": "fout"}), "invalid_credentials"},
		{"geen wachtwoord", base(map[string]any{"password": nil}), "invalid_credentials"},
	} {
		if s, er := put(c, tc.body); s != http.StatusBadRequest || er.Code != tc.code {
			t.Errorf("%s: %d %+v", tc.name, s, er)
		}
	}
	if s, _ := put(v, base(nil)); s != http.StatusForbidden {
		t.Fatalf("viewer slaat op: %d", s)
	}
	// Twee mislukte bevestigingen staan in het logboek.
	var failed int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE action = 'auth.reauth_failed' AND payload->>'what' = 'assistant'`).Scan(&failed)
	if failed != 2 {
		t.Fatalf("reauth_failed = %d", failed)
	}

	var raw json.RawMessage
	if s := c.do("PUT", "/api/v1/assistant/settings", base(nil), &raw); s != http.StatusOK || strings.Contains(string(raw), "sk-ant") {
		t.Fatalf("opslaan: %d %s", s, raw)
	}
	_ = json.Unmarshal(raw, &st)
	if !st.Enabled || !st.HasKey || !st.CanUse || st.UpdatedBy != "admin" {
		t.Fatalf("na opslaan: %+v", st)
	}
	// Een lege sleutel houdt de oude.
	if s, er := put(c, base(map[string]any{"api_key": nil, "monthly_budget_usd": 35})); s != http.StatusOK {
		t.Fatalf("zonder sleutel: %d %+v", s, er)
	}
	// De sleutel staat nergens leesbaar: niet in de API, de database of het
	// logboek.
	c.do("GET", "/api/v1/assistant/settings", nil, &raw)
	var leaks int
	_ = e.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM assistant_settings WHERE encode(api_key_enc, 'escape') LIKE '%sk-ant%') +
		(SELECT count(*) FROM events WHERE payload::text LIKE '%sk-ant%')`).Scan(&leaks)
	if leaks != 0 || strings.Contains(string(raw), "sk-ant") {
		t.Fatalf("sleutel lekt: %d %s", leaks, raw)
	}
	var audit struct {
		Items []struct {
			Action  string `json:"action"`
			Summary string `json:"summary"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/audit?category=assistant", nil, &audit)
	var sums []string
	for _, it := range audit.Items {
		sums = append(sums, it.Summary)
	}
	if !slices.Equal(sums, []string{
		"Instellingen van de assistent gewijzigd: maandbudget in dollar van 20 naar 35",
		"Instellingen van de assistent gewijzigd: aan, api-sleutel",
	}) {
		t.Fatalf("logboek = %q", sums)
	}

	// Een viewer ziet de instellingen, maar mag de assistent niet gebruiken.
	if s := v.do("GET", "/api/v1/assistant/settings", nil, &st); s != http.StatusOK || st.CanUse || !strings.Contains(st.Reason, "alleen voor beheerders") {
		t.Fatalf("viewer: %d %+v", s, st)
	}
	if s := v.do("POST", "/api/v1/assistant/conversations", map[string]any{}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer begint gesprek: %d", s)
	}

	// De sleutel testen tegen de nagespeelde API: eerst goed, dan geweigerd.
	fc := newFakeClaude(t)
	e.asst.BaseURL = fc.srv.URL
	fc.then(nil, func(claudeRequest) (int, string) {
		return http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	})
	var res struct {
		Ok      bool   `json:"ok"`
		Model   string `json:"model"`
		Message string `json:"message"`
	}
	if s := c.do("POST", "/api/v1/assistant/settings/test", map[string]any{"model": "claude-haiku-5-5"}, &res); s != http.StatusOK || !res.Ok {
		t.Fatalf("test opgeslagen sleutel: %d %+v", s, res)
	}
	if s := c.do("POST", "/api/v1/assistant/settings/test", map[string]any{"model": "claude-haiku-5-5", "api_key": "sk-ant-ander"}, &res); s != http.StatusOK ||
		res.Ok || !strings.Contains(res.Message, "401") {
		t.Fatalf("test foute sleutel: %d %+v", s, res)
	}
	reqs := fc.requests()
	if len(reqs) != 2 || reqs[0].Header.Get("X-Api-Key") != "sk-ant-test-0123456789" || reqs[1].Header.Get("X-Api-Key") != "sk-ant-ander" ||
		reqs[0].Body["model"] != "claude-haiku-5-5" {
		t.Fatalf("testaanvragen: %+v", reqs)
	}
	if s := v.do("POST", "/api/v1/assistant/settings/test", map[string]any{"model": "claude-haiku-5-5"}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer test: %d", s)
	}
}

func TestAssistantAsk(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login viewer: %d", s)
	}
	cl, web01, _, incID := whatBrokeFleet(t, e, c)

	// Zolang de assistent uit staat, kan niemand iets vragen.
	if s := c.do("POST", "/api/v1/assistant/conversations", map[string]any{}, nil); s != http.StatusConflict {
		t.Fatalf("uit: %d", s)
	}
	fc := enableAssistant(t, e, c, false)

	var conv assistantConversation
	for _, bad := range []map[string]any{
		{"subject_type": "cluster"},
		{"subject_type": "cluster", "subject_id": uuid.NewString()},
		{"subject_type": "dienst", "subject_id": cl.ID},
	} {
		if s := c.do("POST", "/api/v1/assistant/conversations", bad, nil); s != http.StatusBadRequest {
			t.Errorf("gesprek %v: %d", bad, s)
		}
	}
	if s := c.do("POST", "/api/v1/assistant/conversations", map[string]any{"subject_type": "cluster", "subject_id": cl.ID}, &conv); s != http.StatusCreated ||
		conv.SubjectType != "cluster" || conv.Username != "admin" {
		t.Fatalf("gesprek: %d %+v", s, conv)
	}

	// Ronde 1: denken, tekst en twee tools. Ronde 2: het antwoord.
	fc.then(
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "tool_use", 3000, 200,
				block{thinking: "eerst de incidenten"},
				block{text: "Ik kijk naar de incidenten."},
				block{id: "toolu_1", name: "incidenten", input: `{"status":"alle","cluster":"Web","van":"","tot":"","tests":false}`},
				block{id: "toolu_2", name: "incident", input: `{"incident":"#1"}`})
		},
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "end_turn", 5000, 300,
				block{text: "De oorzaak is de upgrade van nginx op [web01](/nodes/detail?id=" + web01.ID + ")."})
		},
	)
	s, evs := c.ask(conv.ID, "  Waarom was Web verminderd?  ")
	if s != http.StatusOK {
		t.Fatalf("vraag: %d %+v", s, evs)
	}
	tools := streamOf(evs, "tool")
	if len(tools) != 2 || tools[0]["label"] != "Incidenten van Web zoeken" || tools[1]["label"] != "Incident 1 lezen" {
		t.Fatalf("tools = %+v", tools)
	}
	if txt := streamText(evs); txt != "Ik kijk naar de incidenten.De oorzaak is de upgrade van nginx op [web01](/nodes/detail?id="+web01.ID+")." {
		t.Fatalf("tekst = %q", txt)
	}
	done := streamOf(evs, "done")
	// Opus 5.5: $4 per miljoen tokens in, $20 uit.
	wantCost := (3000.0*4 + 200*20 + 5000*4 + 300*20) / 1e6
	if len(done) != 1 || len(streamOf(evs, "error")) != 0 || done[0]["cost_usd"] != wantCost || done[0]["input_tokens"] != 8000.0 ||
		done[0]["output_tokens"] != 500.0 || done[0]["note"] != "" {
		t.Fatalf("done = %+v, fouten %+v", done, streamOf(evs, "error"))
	}

	reqs := fc.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d aanvragen", len(reqs))
	}
	first := reqs[0]
	// De vorm van de aanvraag.
	if first.Header.Get("X-Api-Key") != "sk-ant-test-0123456789" || first.Body["model"] != "claude-opus-5-5" || first.Body["max_tokens"] != 16000.0 ||
		first.Body["fallbacks"] != "default" || !strings.Contains(first.Header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") ||
		!strings.Contains(first.Header.Get("Anthropic-Beta"), "thinking-binding-controls-2026-08-01") {
		t.Fatalf("aanvraag: %v %s", first.Header, first.Raw[:min(len(first.Raw), 400)])
	}
	var shape struct {
		System []struct {
			Text         string `json:"text"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"system"`
		CacheControl *struct {
			Type string `json:"type"`
		} `json:"cache_control"`
		Thinking struct {
			Type         string `json:"type"`
			BlockBinding struct {
				PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
			} `json:"block_binding"`
		} `json:"thinking"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		Tools []struct {
			Name                string `json:"name"`
			Strict              bool   `json:"strict"`
			EagerInputStreaming bool   `json:"eager_input_streaming"`
			InputSchema         struct {
				Properties           map[string]any `json:"properties"`
				Required             []string       `json:"required"`
				AdditionalProperties *bool          `json:"additionalProperties"`
			} `json:"input_schema"`
		} `json:"tools"`
		ToolChoice any `json:"tool_choice"`
		Messages   []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(first.Raw), &shape); err != nil {
		t.Fatal(err)
	}
	if len(shape.System) != 1 || shape.System[0].CacheControl == nil || shape.CacheControl == nil || shape.Thinking.Type != "adaptive" ||
		shape.Thinking.BlockBinding.PrefixMismatchBehavior != "drop_block" || shape.OutputConfig.Effort != "medium" || shape.ToolChoice != nil {
		t.Fatalf("vorm: %+v", shape)
	}
	if len(shape.Tools) != len(e.asst.Tools) || len(shape.Tools) != 17 {
		t.Fatalf("%d tools", len(shape.Tools))
	}
	for _, tl := range shape.Tools {
		props := make([]string, 0, len(tl.InputSchema.Properties))
		for k := range tl.InputSchema.Properties {
			props = append(props, k)
		}
		slices.Sort(props)
		req := slices.Sorted(slices.Values(tl.InputSchema.Required))
		if !tl.Strict || !tl.EagerInputStreaming || tl.InputSchema.AdditionalProperties == nil || *tl.InputSchema.AdditionalProperties ||
			!slices.Equal(props, req) {
			t.Errorf("tool %s: %+v", tl.Name, tl)
		}
	}
	// De vraag: eerst de context met het onderwerp, dan de vraag zelf.
	if m := shape.Messages; len(m) != 1 || len(m[0].Content) != 2 || !strings.Contains(m[0].Content[0]["text"].(string), "cluster Web (id "+cl.ID+")") ||
		!strings.Contains(m[0].Content[0]["text"].(string), "De vraag komt van admin (beheerder)") || m[0].Content[1]["text"] != "Waarom was Web verminderd?" {
		t.Fatalf("vraag = %+v", m)
	}
	// Ronde 2 krijgt het denken met zijn handtekening en de twee resultaten
	// terug, in de volgorde van de aanroepen.
	var second struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				Signature string `json:"signature"`
				ToolUseID string `json:"tool_use_id"`
				IsError   bool   `json:"is_error"`
				Content   any    `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal([]byte(reqs[1].Raw), &second)
	m := second.Messages
	if len(m) != 3 || m[1].Role != "assistant" || m[1].Content[0].Type != "thinking" || m[1].Content[0].Signature != "sig-eerst de incidenten" ||
		m[2].Role != "user" || len(m[2].Content) != 2 || m[2].Content[0].ToolUseID != "toolu_1" || m[2].Content[1].ToolUseID != "toolu_2" ||
		m[2].Content[0].IsError || m[2].Content[1].IsError {
		t.Fatalf("ronde 2 = %s", reqs[1].Raw)
	}
	results := fmt.Sprint(m[2].Content[0].Content, m[2].Content[1].Content)
	for _, want := range []string{"Cluster Web is verminderd", "nginx bijgewerkt van 1.26.0 naar 1.26.3", `"likely"`, incID.String()} {
		if !strings.Contains(results, want) {
			t.Errorf("resultaten missen %q: %s", want, results)
		}
	}

	// Het gesprek is bewaard met de titel, de tools en de kosten.
	s, got, turns := c.conversation(conv.ID)
	if s != http.StatusOK || got.Title != "Waarom was Web verminderd?" || len(turns) != 1 || got.CostUsd != wantCost {
		t.Fatalf("gesprek: %d %+v %+v", s, got, turns)
	}
	tr := turns[0]
	if tr.Question != "Waarom was Web verminderd?" || !strings.HasPrefix(tr.Answer, "Ik kijk naar de incidenten.\n\nDe oorzaak") || len(tr.Tools) != 2 ||
		tr.Tools[1].Label != "Incident 1 lezen" || tr.Tools[0].Error || !slices.Equal(tr.Models, []string{"claude-opus-5-5"}) || tr.Input != 8000 ||
		tr.CostUsd != wantCost || tr.Note != "" {
		t.Fatalf("beurt = %+v", tr)
	}

	// Een tweede vraag stuurt de geschiedenis ongewijzigd mee, zodat de cache
	// en het denken blijven gelden.
	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-opus-5-5", "end_turn", 9000, 50, block{text: "Nee."})
	})
	if s, evs := c.ask(conv.ID, "Nog iets?"); s != http.StatusOK || streamText(evs) != "Nee." {
		t.Fatalf("tweede vraag: %d %+v", s, evs)
	}
	reqs = fc.requests()
	var r2, r3 struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal([]byte(reqs[1].Raw), &r2)
	_ = json.Unmarshal([]byte(reqs[2].Raw), &r3)
	if len(r3.Messages) != 5 {
		t.Fatalf("%d berichten", len(r3.Messages))
	}
	for i := range r2.Messages {
		if string(r2.Messages[i]) != string(r3.Messages[i]) {
			t.Fatalf("bericht %d veranderde:\n%s\n%s", i, r2.Messages[i], r3.Messages[i])
		}
	}
	if !strings.Contains(string(r3.Messages[4]), "Nog iets?") || strings.Contains(string(r3.Messages[4]), "begon op de pagina") {
		t.Fatalf("vraag 2 = %s", r3.Messages[4])
	}

	// In het logboek: welke tools en wat het kostte, niet de vraag.
	var payload string
	if err := e.pool.QueryRow(ctx, `SELECT payload::text FROM events WHERE action = 'assistant.asked' ORDER BY id LIMIT 1`).Scan(&payload); err != nil ||
		!strings.Contains(payload, `"tools": ["incidenten", "incident"]`) || strings.Contains(payload, "verminderd") {
		t.Fatalf("assistant.asked = %s %v", payload, err)
	}

	// Het verbruik van deze maand.
	var usage struct {
		SpentUsd  float64 `json:"spent_usd"`
		BudgetUsd int     `json:"budget_usd"`
		Users     []struct {
			Username  string `json:"username"`
			Questions int    `json:"questions"`
		} `json:"users"`
	}
	if s := c.do("GET", "/api/v1/assistant/usage", nil, &usage); s != http.StatusOK || usage.SpentUsd <= wantCost || usage.BudgetUsd != 20 ||
		len(usage.Users) != 1 || usage.Users[0].Username != "admin" || usage.Users[0].Questions != 2 {
		t.Fatalf("verbruik: %d %+v", s, usage)
	}

	// Een fout van de API komt als event en het gesprek zegt dat er geen
	// antwoord kwam.
	fc.then(func(claudeRequest) (int, string) {
		return http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	})
	if s, evs := c.ask(conv.ID, "En nu?"); s != http.StatusOK || len(streamOf(evs, "error")) != 1 ||
		!strings.Contains(streamOf(evs, "error")[0]["message"].(string), "(401)") || len(streamOf(evs, "done")) != 1 {
		t.Fatalf("fout: %d %+v", s, evs)
	}
	if _, _, turns := c.conversation(conv.ID); len(turns) != 3 || !strings.Contains(turns[2].Note, "geen antwoord") {
		t.Fatalf("beurten = %+v", turns)
	}

	// Een tool met foute of onvolledige invoer: het model krijgt de fout.
	fc.then(
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "tool_use", 100, 10,
				block{id: "toolu_3", name: "cluster", input: `{"cluster":"Bestaat-niet"}`},
				block{id: "toolu_4", name: "tijdlijn", input: `{"cluster":"Web"}`},
				block{id: "toolu_5", name: "rm_rf", input: `{}`})
		},
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "end_turn", 100, 10, block{text: "Sorry."})
		},
	)
	conv2 := assistantConversation{}
	c.do("POST", "/api/v1/assistant/conversations", map[string]any{}, &conv2)
	if s, _ := c.ask(conv2.ID, "Hoe gaat het met Bestaat-niet?"); s != http.StatusOK {
		t.Fatalf("foute tools: %d", s)
	}
	reqs = fc.requests()
	last := reqs[len(reqs)-1].Raw
	for _, want := range []string{`geen cluster \"Bestaat-niet\"; de clusters zijn: Web`, "onvolledige invoer: node ontbreekt", "onbekende tool rm_rf"} {
		if !strings.Contains(last, want) {
			t.Errorf("toolfout mist %q: %s", want, last)
		}
	}
	if _, _, turns := c.conversation(conv2.ID); len(turns) != 1 || len(turns[0].Tools) != 3 || !turns[0].Tools[0].Error || !turns[0].Tools[2].Error {
		t.Fatalf("toolfouten = %+v", turns)
	}

	// De limiet van 12 rondes: daarna stopt de lus met een opmerking.
	for range assistant.MaxRounds {
		fc.then(func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "tool_use", 10, 10, block{id: "toolu_" + uuid.NewString()[:6], name: "overzicht", input: `{}`})
		})
	}
	before := len(fc.requests())
	if s, evs := c.ask(conv2.ID, "Blijf zoeken"); s != http.StatusOK || !strings.Contains(streamOf(evs, "done")[0]["note"].(string), "12 stappen") ||
		len(streamOf(evs, "tool")) != assistant.MaxRounds-1 {
		t.Fatalf("limiet: %d %+v", s, streamOf(evs, "done"))
	}
	if n := len(fc.requests()) - before; n != assistant.MaxRounds {
		t.Fatalf("%d aanvragen in plaats van %d", n, assistant.MaxRounds)
	}
	if _, _, turns := c.conversation(conv2.ID); !strings.Contains(turns[1].Note, "12 stappen") {
		t.Fatalf("limiet in gesprek: %+v", turns[1])
	}

	// Haiku kent geen fallback.
	if _, err := e.pool.Exec(ctx, `UPDATE assistant_settings SET model = 'claude-haiku-5-5'`); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.ask(conv2.ID, "Kort?"); s != http.StatusOK {
		t.Fatalf("haiku: %d", s)
	}
	reqs = fc.requests()
	if h := reqs[len(reqs)-1]; h.Body["model"] != "claude-haiku-5-5" || h.Body["fallbacks"] != nil ||
		strings.Contains(h.Header.Get("Anthropic-Beta"), "fallback") {
		t.Fatalf("haiku: %v %v", h.Body["fallbacks"], h.Header.Get("Anthropic-Beta"))
	}

	// Gesprekken: de viewer ziet ze niet, een admin ziet alles met all.
	var list struct {
		Items []assistantConversation `json:"items"`
	}
	if s := c.do("GET", "/api/v1/assistant/conversations", nil, &list); s != http.StatusOK || len(list.Items) != 2 || list.Items[0].ID != conv2.ID {
		t.Fatalf("lijst: %d %+v", s, list)
	}
	if s, _, _ := v.conversation(conv.ID); s != http.StatusNotFound {
		t.Fatalf("viewer leest gesprek van admin: %d", s)
	}
	if s := v.do("DELETE", "/api/v1/assistant/conversations/"+conv.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("viewer wist gesprek van admin: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/assistant/conversations/"+conv2.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("wissen: %d", s)
	}
	if s, _, _ := c.conversation(conv2.ID); s != http.StatusNotFound {
		t.Fatalf("na wissen: %d", s)
	}
}

func TestAssistantLimitsAndViewers(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	if s, _, _ := v.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login viewer: %d", s)
	}
	cl, _, _, _ := whatBrokeFleet(t, e, c)
	fc := enableAssistant(t, e, c, true)

	// Een viewer gebruikt de tools met zijn eigen rechten: de tijdlijn zonder
	// IP-adres van de login.
	timeline := func(cl *client) string {
		var conv assistantConversation
		if s := cl.do("POST", "/api/v1/assistant/conversations", map[string]any{}, &conv); s != http.StatusCreated {
			t.Fatalf("gesprek: %d", s)
		}
		fc.then(
			func(claudeRequest) (int, string) {
				return 200, sse("claude-opus-5-5", "tool_use", 10, 10,
					block{id: "toolu_1", name: "tijdlijn", input: `{"cluster":"web","node":"","van":"","tot":"","klasse":"wijzigingen"}`})
			},
			nil,
		)
		if s, _ := cl.ask(conv.ID, "Wat veranderde er?"); s != http.StatusOK {
			t.Fatalf("vraag: %d", s)
		}
		reqs := fc.requests()
		return reqs[len(reqs)-1].Raw
	}
	if got := timeline(c); !strings.Contains(got, "192.0.2.44") || !strings.Contains(got, "nginx bijgewerkt") {
		t.Fatalf("admin-tijdlijn: %s", got)
	}
	if got := timeline(v); strings.Contains(got, "192.0.2.44") || !strings.Contains(got, "SSH-login als root (sleutel)") {
		t.Fatalf("viewer-tijdlijn: %s", got)
	}
	// Een viewer ziet zijn eigen gesprekken en niet die van iedereen.
	var list struct {
		Items []assistantConversation `json:"items"`
	}
	v.do("GET", "/api/v1/assistant/conversations?all=true", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Username != "kijker" {
		t.Fatalf("viewer-lijst: %+v", list)
	}
	c.do("GET", "/api/v1/assistant/conversations?all=true", nil, &list)
	if len(list.Items) != 2 {
		t.Fatalf("admin-lijst: %+v", list)
	}

	// De limiet per uur telt vragen uit de database.
	var conv assistantConversation
	v.do("POST", "/api/v1/assistant/conversations", map[string]any{"subject_type": "cluster", "subject_id": cl.ID}, &conv)
	if _, err := e.pool.Exec(ctx, `INSERT INTO assistant_messages (conversation_id, seq, role, content, user_id)
		SELECT $1, 100 + g, 'question', '{}', (SELECT id FROM users WHERE username = 'kijker') FROM generate_series(1, 29) g`, conv.ID); err != nil {
		t.Fatal(err)
	}
	if s, evs := v.ask(conv.ID, "Nog een?"); s != http.StatusTooManyRequests || evs[0].Data["code"] != "limit" ||
		!strings.Contains(evs[0].Data["message"].(string), "30 vragen") {
		t.Fatalf("limiet per uur: %d %+v", s, evs)
	}
	// Een admin heeft zijn eigen limiet.
	var own assistantConversation
	c.do("POST", "/api/v1/assistant/conversations", map[string]any{}, &own)
	if s, _ := c.ask(own.ID, "Werkt het nog?"); s != http.StatusOK {
		t.Fatalf("admin na limiet viewer: %d", s)
	}

	// Het maandbudget: is het op, dan weigert de assistent, en het logboek
	// zegt het één keer.
	if _, err := e.pool.Exec(ctx, `UPDATE assistant_messages SET cost_microusd = 25000000 WHERE id = (SELECT max(id) FROM assistant_messages WHERE role = 'answer')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if s, evs := c.ask(own.ID, "En nu?"); s != http.StatusTooManyRequests || !strings.Contains(evs[0].Data["message"].(string), "maandbudget") {
			t.Fatalf("budget: %d %+v", s, evs)
		}
	}
	var reached int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = 'assistant.budget_reached'`).Scan(&reached)
	if reached != 1 {
		t.Fatalf("budget_reached = %d", reached)
	}
	// Viewers toelaten intrekken: dan niet meer.
	if s := c.do("PUT", "/api/v1/assistant/settings", map[string]any{
		"enabled": true, "model": "claude-opus-5-5", "monthly_budget_usd": 100, "viewer_access": false, "password": "een-lang-wachtwoord",
	}, nil); s != http.StatusOK {
		t.Fatalf("viewers uit: %d", s)
	}
	if s, evs := v.ask(conv.ID, "Mag het nog?"); s != http.StatusForbidden {
		t.Fatalf("viewer zonder toegang: %d %+v", s, evs)
	}
	if s, _ := c.ask(own.ID, "Met meer budget?"); s != http.StatusOK {
		t.Fatalf("hoger budget: %d", s)
	}
	// Opruimen na een jaar stilte.
	if err := e.asst.Prune(ctx, time.Now().Add(assistant.Retention+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := c.conversation(own.ID); s != http.StatusNotFound {
		t.Fatalf("na opruimen: %d", s)
	}
}

// toolInputs geeft voor elke tool geldige invoer over cluster en node; job
// is het id van een taak.
func toolInputs(cluster, node, job string) map[string][]string {
	tgt := func(extra string) []string {
		return []string{
			fmt.Sprintf(`{"cluster":%q,"node":""%s}`, cluster, extra),
			fmt.Sprintf(`{"cluster":"","node":%q%s}`, node, extra),
		}
	}
	return map[string][]string{
		"overzicht":   {`{}`},
		"cluster":     {fmt.Sprintf(`{"cluster":%q}`, cluster)},
		"node":        {fmt.Sprintf(`{"node":%q}`, node)},
		"tijdlijn":    tgt(`,"van":"","tot":"","klasse":"alles"`),
		"wijzigingen": tgt(`,"van":"","tot":"","soort":""`),
		"incidenten":  {`{"status":"alle","cluster":"","van":"","tot":"","tests":true}`, fmt.Sprintf(`{"status":"open","cluster":%q,"van":"","tot":"","tests":false}`, cluster)},
		"incident":    {`{"incident":"1"}`},
		"metrics":     tgt(`,"venster":"1h","van":"","tot":""`),
		"drift":       {fmt.Sprintf(`{"cluster":%q}`, cluster)},
		"back_ups":    {`{"node":""}`, fmt.Sprintf(`{"node":%q}`, node)},
		"testen":      append(tgt(`,"soort":"alle","uitkomst":"alle"`), `{"cluster":"","node":"","soort":"failover","uitkomst":"fail"}`),
		"taken":       append(tgt(""), `{"cluster":"","node":""}`),
		"taak":        {fmt.Sprintf(`{"id":%q,"log":true}`, job)},
		"git":         {fmt.Sprintf(`{"cluster":%q,"status":"alle"}`, cluster), `{"cluster":"","status":"pending"}`},
		"gezondheid":  {`{"cluster":""}`, fmt.Sprintf(`{"cluster":%q}`, cluster)},
		"wat_als":     {fmt.Sprintf(`{"soort":"node","naam":%q}`, node), fmt.Sprintf(`{"soort":"cluster","naam":%q}`, cluster), `{"soort":"spof","naam":""}`},
		"impact": {
			fmt.Sprintf(`{"dienst":"","cluster":%q,"node":""}`, cluster), fmt.Sprintf(`{"dienst":"","cluster":"","node":%q}`, node),
			`{"dienst":"nginx","cluster":"","node":""}`,
		},
	}
}

// runTools roept elke tool van de assistent aan als username, met de invoer
// uit toolInputs. Een fout die het model zou zien mag; een interne fout niet.
func (e *testEnv) runTools(t *testing.T, username string, inputs map[string][]string) map[string]string {
	t.Helper()
	ctx := context.Background()
	u, err := store.New(e.pool).GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	c := assistant.Caller{Principal: auth.Principal{User: u}}
	out := map[string]string{}
	for _, tool := range e.asst.Tools {
		ins, ok := inputs[tool.Name]
		if !ok {
			t.Fatalf("geen testinvoer voor tool %s", tool.Name)
		}
		for _, in := range ins {
			// Elk verplicht veld staat in de invoer, zoals een strikt schema eist.
			var fields map[string]any
			_ = json.Unmarshal([]byte(in), &fields)
			for _, k := range tool.Required {
				if _, ok := fields[k]; !ok {
					t.Fatalf("testinvoer %s mist %s", tool.Name, k)
				}
			}
			res, err := tool.Run(ctx, c, json.RawMessage(in))
			var te assistant.ToolError
			switch {
			case errors.As(err, &te):
				out[tool.Name] += "FOUT " + te.Msg + "\n"
			case err != nil:
				t.Fatalf("%s %s: %v", tool.Name, in, err)
			default:
				b, _ := json.Marshal(res)
				if len(b) > assistant.MaxToolResult {
					t.Errorf("%s %s: %d bytes", tool.Name, in, len(b))
				}
				out[tool.Name] += string(b) + "\n"
				if tool.Label != nil && tool.Label(fields) == "" {
					t.Errorf("%s heeft geen label", tool.Name)
				}
			}
		}
	}
	return out
}

// TestAssistantToolsReadOnly loopt alle tools af met een trigger op elke
// tabel: geen enkele tool mag iets schrijven.
func TestAssistantToolsReadOnly(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	cl, web01, _, _ := whatBrokeFleet(t, e, c)
	_ = cl
	job := uuid.New()
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (id, kind, title, status, cluster_id) VALUES ($1, 'cluster.apply', 'Toepassen', 'succeeded', $2)`, job, cl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO job_steps (job_id, seq, name, status, log) VALUES ($1, 1, 'nginx', 'succeeded', $2)`,
		job, []string{"regel 1", "regel 2"}); err != nil {
		t.Fatal(err)
	}
	e.stopRunner()
	if _, err := e.pool.Exec(ctx, `
		CREATE TABLE tool_writes (tbl text, op text);
		CREATE FUNCTION log_tool_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO tool_writes VALUES (TG_TABLE_NAME, TG_OP); RETURN NULL; END $$;
		DO $$ DECLARE r record; BEGIN
		FOR r IN SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relname <> 'tool_writes' AND NOT c.relispartition LOOP
			EXECUTE format('CREATE TRIGGER tool_write AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON %I
				FOR EACH STATEMENT EXECUTE FUNCTION log_tool_write()', r.relname);
		END LOOP; END $$;`); err != nil {
		t.Fatal(err)
	}
	out := e.runTools(t, "admin", toolInputs("Web", web01.Hostname, job.String()))
	var triggers int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname = 'tool_write'`).Scan(&triggers); err != nil || triggers < 30 {
		t.Fatalf("%d triggers: %v", triggers, err)
	}
	rows, err := e.pool.Query(ctx, `SELECT tbl, op FROM tool_writes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var tbl, op string
		_ = rows.Scan(&tbl, &op)
		t.Errorf("een tool schreef: %s op %s", op, tbl)
	}
	// Een paar resultaten die het model nodig heeft.
	for tool, want := range map[string]string{
		"overzicht":   `"hostname":"web01"`,
		"tijdlijn":    "nginx bijgewerkt van 1.26.0 naar 1.26.3",
		"wijzigingen": "nginx.service",
		"incident":    "Cluster Web is verminderd",
		"metrics":     `"panelen"`,
		"taak":        "regel 2",
	} {
		if !strings.Contains(out[tool], want) {
			t.Errorf("%s mist %q: %s", tool, want, out[tool])
		}
	}
}
