package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestCost(t *testing.T) {
	// Opus 5.5: $4 in, $20 uit, $0,20 uit de cache, $5 naar de cache.
	u := Tokens{Input: 1_000_000, Output: 100_000, CacheRead: 2_000_000, CacheWrite: 200_000}
	if got := cost(priceOf("claude-opus-5-5", "claude-opus-5-5"), u); got != 4_000_000+2_000_000+400_000+1_000_000 {
		t.Fatalf("opus = %d", got)
	}
	// Een antwoord van een fallbackmodel kost zijn eigen prijs.
	if got := priceOf("claude-opus-4-8", "claude-opus-5-5"); got.Input != 5 || got.Output != 25 {
		t.Fatalf("fallback = %+v", got)
	}
	// Een gedateerde naam hoort bij zijn model; een onbekend model rekent met
	// het gekozen model.
	if got := priceOf("claude-haiku-5-5-20261001", "claude-opus-5-5"); got.Input != 0.10 {
		t.Fatalf("gedateerd = %+v", got)
	}
	if got := priceOf("claude-iets-nieuws", "claude-sonnet-5-5"); got.Input != 2 {
		t.Fatalf("onbekend = %+v", got)
	}
	if USD(1_234_500) != 1.2345 {
		t.Fatal(USD(1_234_500))
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("kort"); got != "kort" {
		t.Fatal(got)
	}
	long := strings.Repeat("é", MaxToolResult)
	got := truncate(long)
	if len(got) > MaxToolResult || !utf8.ValidString(got) || !strings.Contains(got, "[afgekapt: het resultaat was 40 KB") {
		t.Fatalf("afgekapt: %d bytes, geldig %v, einde %q", len(got), utf8.ValidString(got), got[len(got)-120:])
	}
}

func TestTitleAndContext(t *testing.T) {
	if got := title("  Waarom\nwas web-prod   down?  "); got != "Waarom was web-prod down?" {
		t.Fatal(got)
	}
	if got := title(strings.Repeat("a", 100)); utf8.RuneCountInString(got) != 80 || !strings.HasSuffix(got, "…") {
		t.Fatal(got)
	}
	loc, _ := time.LoadLocation("Europe/Brussels")
	now := time.Date(2026, 10, 11, 14, 5, 0, 0, loc)
	got := questionContext(now, "jonas", true, "cluster Web (id 1)")
	want := "Context van ClusterForge: nu is het zondag 11 oktober 2026, 14:05 (UTC+02:00). De vraag komt van jonas (beheerder). " +
		"Het gesprek begon op de pagina van cluster Web (id 1)."
	if got != want {
		t.Fatalf("context = %q", got)
	}
	if got := questionContext(now.AddDate(0, 1, 0), "piet", false, ""); !strings.Contains(got, "(UTC+01:00)") || !strings.Contains(got, "piet (viewer)") {
		t.Fatal(got)
	}
}

func TestValidate(t *testing.T) {
	in := SettingsInput{Model: "claude-sonnet-5-5", MonthlyBudgetUSD: 20, APIKey: " sk-ant-abc "}
	if err := validate(&in); err != nil || in.APIKey != "sk-ant-abc" {
		t.Fatalf("%v %q", err, in.APIKey)
	}
	for _, bad := range []SettingsInput{
		{Model: "claude-sonnet-5-5", MonthlyBudgetUSD: 20, APIKey: "sk-ant-a b"},
		{Model: "claude-sonnet-5-5", MonthlyBudgetUSD: 20, APIKey: "sk-openai"},
		{Model: "claude-sonnet-4", MonthlyBudgetUSD: 20},
		{Model: "claude-sonnet-5-5", MonthlyBudgetUSD: 0},
		{Model: "claude-sonnet-5-5", MonthlyBudgetUSD: 10001},
	} {
		if err := validate(&bad); err == nil {
			t.Errorf("%+v is goedgekeurd", bad)
		}
	}
}

func TestTurns(t *testing.T) {
	s := &Service{Tools: []Tool{{Name: "node", Label: func(in map[string]any) string { return "Node " + in["node"].(string) + " bekijken" }}}}
	msg := func(role, text string, content any, extra func(*store.AssistantMessage)) store.AssistantMessage {
		b, _ := json.Marshal(map[string]any{"content": content})
		m := store.AssistantMessage{ID: int64(len(text)), ConversationID: uuid.Nil, Role: role, Text: text, Content: b}
		if extra != nil {
			extra(&m)
		}
		return m
	}
	toolUse := []any{map[string]any{"type": "tool_use", "id": "t1", "name": "node", "input": map[string]any{"node": "web01"}}}
	toolErr := []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "is_error": true}}
	priced := func(m *store.AssistantMessage) {
		m.Model, m.CostMicrousd, m.InputTokens, m.OutputTokens = "claude-opus-5-5", 1500, 100, 20
	}
	rows := []store.AssistantMessage{
		// Een gewone beurt met een tool die faalde.
		msg("question", "Hoe gaat het met web01?", nil, nil),
		msg("answer", "", toolUse, priced),
		msg("tool_results", "", toolErr, nil),
		msg("answer", "Slecht.", nil, priced),
		// Een vraag zonder antwoord.
		msg("question", "En nu?", nil, nil),
		// Een geweigerde vraag.
		msg("question", "Iets anders?", nil, nil),
		msg("answer", "", nil, func(m *store.AssistantMessage) { m.StopReason = "refusal" }),
		// Afgebroken na een tool.
		msg("question", "Kijk eens", nil, nil),
		msg("answer", "", toolUse, nil),
		msg("tool_results", "", toolErr, nil),
	}
	got := s.turns(rows)
	if len(got) != 4 {
		t.Fatalf("%d beurten", len(got))
	}
	first := got[0]
	if first.Answer != "Slecht." || len(first.Tools) != 1 || first.Tools[0].Label != "Node web01 bekijken" || !first.Tools[0].Error ||
		first.CostUSD != 0.003 || first.Tokens.Input != 200 || len(first.Models) != 1 || first.Note != "" {
		t.Fatalf("eerste = %+v", first)
	}
	if !strings.Contains(got[1].Note, "geen antwoord") || !strings.Contains(got[2].Note, "weigerde") || !strings.Contains(got[3].Note, "onderbroken") {
		t.Fatalf("opmerkingen = %q, %q, %q", got[1].Note, got[2].Note, got[3].Note)
	}
}

func TestAnalysisText(t *testing.T) {
	full := "## In het kort\nWeb was 4 minuten verminderd.\n\n## Waarschijnlijke oorzaak\nDe upgrade.\n\n" +
		"## Nog te controleren\n- de logs\n\n## Wat je kunt doen\n- [terugdraaien](/nodes/detail?id=1)"
	if got := AnalysisText("Ik kijk eerst naar de metrics.\n\n" + full); got != full {
		t.Fatalf("zonder inleiding: %q", got)
	}
	if got := AnalysisText("  " + full + "\n"); got != full {
		t.Fatalf("getrimd: %q", got)
	}
	if got := AnalysisText("Geen kopjes."); got != "Geen kopjes." {
		t.Fatal(got)
	}
	if m := MissingHeadings(full); len(m) != 0 {
		t.Fatal(m)
	}
	if m := MissingHeadings("## In het kort\nx\n## Wat je kunt doen\ny"); strings.Join(m, ",") != "Waarschijnlijke oorzaak,Nog te controleren" {
		t.Fatal(m)
	}
}
