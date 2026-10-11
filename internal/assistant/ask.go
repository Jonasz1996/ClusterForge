package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Event is een stuk van het antwoord voor de browser: tekst, een tool die
// loopt, het einde met tokens en kosten, of een fout.
type Event struct {
	Type  string
	Text  string
	Name  string
	Label string
	Done  *Done
}

const (
	EventText  = "text"
	EventTool  = "tool"
	EventDone  = "done"
	EventError = "error"
)

// Done is het verbruik van een vraag over alle rondes.
type Done struct {
	Models  []string
	Tokens  Tokens
	CostUSD float64
	Note    string
}

// Turn is één vraag die klaar is om te stellen: de controles zijn gedaan
// voor het antwoord begint te stromen.
type Turn struct {
	s        *Service
	c        Caller
	conv     store.GetConversationRow
	model    Model
	key      string
	history  []anthropic.BetaMessageParam
	question string
}

// Prepare controleert een vraag: toegang, het gesprek, de limieten en de
// geschiedenis. Daarna stuurt Run het antwoord.
func (s *Service) Prepare(ctx context.Context, c Caller, convID uuid.UUID, question string) (*Turn, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, ValidationError{"question", "stel een vraag"}
	}
	if utf8.RuneCountInString(question) > MaxQuestion {
		return nil, ValidationError{"question", fmt.Sprintf("een vraag heeft hoogstens %d tekens", MaxQuestion)}
	}
	cur, key, err := s.access(ctx, c)
	if err != nil {
		return nil, err
	}
	conv, err := s.q.GetConversation(ctx, convID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && conv.UserID != c.userID()) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.checkLimits(ctx, c, cur); err != nil {
		return nil, err
	}
	rows, err := s.q.ListConversationMessages(ctx, convID)
	if err != nil {
		return nil, err
	}
	history := make([]anthropic.BetaMessageParam, 0, len(rows)+1)
	for _, r := range rows {
		var p anthropic.BetaMessageParam
		if err := json.Unmarshal(r.Content, &p); err != nil {
			return nil, fmt.Errorf("bericht %d van gesprek %s: %w", r.Seq, convID, err)
		}
		history = append(history, p)
	}
	model, ok := modelByID(cur.Model)
	if !ok {
		model = Models[0]
	}
	if !s.claim(convID) {
		return nil, ErrBusy
	}
	return &Turn{s: s, c: c, conv: conv, model: model, key: key, history: history, question: question}, nil
}

func (s *Service) claim(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Service) release(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}

func (s *Service) client(key string) anthropic.Client {
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(2)}
	if s.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(s.BaseURL))
	}
	return anthropic.NewClient(opts...)
}

// params bouwt de aanvraag. Systeemprompt en tools zijn bij elke vraag
// gelijk, zodat ze uit de cache komen; de geschiedenis gaat ongewijzigd mee.
func (t *Turn) params(msgs []anthropic.BetaMessageParam) anthropic.BetaMessageNewParams {
	tools := make([]anthropic.BetaToolUnionParam, len(t.s.Tools))
	for i, tl := range t.s.Tools {
		tools[i] = tl.param()
	}
	p := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(t.model.ID),
		MaxTokens: maxTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: systemPrompt, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}},
		Tools:     tools,
		Messages:  msgs,
		// Denken staat altijd aan. Verandert de geschiedenis toch, zoals na
		// een update met nieuwe tools, dan valt het oude denken weg in plaats
		// van dat de vraag mislukt.
		Thinking: anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
			BlockBinding: anthropic.BetaThinkingBlockBindingParam{PrefixMismatchBehavior: "drop_block"},
		}},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
		// De groeiende geschiedenis komt in de cache, zodat een volgende
		// ronde van dezelfde vraag haar niet opnieuw betaalt.
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaThinkingBindingControls2026_08_01},
	}
	if t.model.Fallback {
		// Weigert het model een vraag, dan herhaalt de API haar op het model
		// dat Anthropic voor die soort weigering aanraadt.
		p.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		p.Betas = append(p.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
	}
	return p
}

// Run stelt de vraag en stuurt het antwoord in stukken naar emit, tot het
// model klaar is of de limiet van MaxRounds bereikt is. Elk bericht wordt
// bewaard zoals het verstuurd of ontvangen is.
func (t *Turn) Run(ctx context.Context, emit func(Event)) {
	s := t.s
	defer s.release(t.conv.ID)
	subject := ""
	if len(t.history) == 0 {
		subject = s.subject(ctx, t.conv)
	}
	q := anthropic.NewBetaUserMessage(
		anthropic.NewBetaTextBlock(questionContext(s.Now(), t.c.Principal.User.Username, t.c.admin(), subject)),
		anthropic.NewBetaTextBlock(t.question),
	)
	if err := s.save(ctx, t.conv.ID, "question", q, t.question, t.c.userID(), nil); err != nil {
		s.log.ErrorContext(ctx, "vraag aan de assistent bewaren", "err", err)
		emit(Event{Type: EventError, Text: "interne fout"})
		return
	}
	if err := s.q.TouchConversation(ctx, store.TouchConversationParams{ID: t.conv.ID, Title: title(t.question)}); err != nil {
		s.log.WarnContext(ctx, "gesprek bijwerken", "err", err)
	}
	msgs := append(t.history, q)
	client := s.client(t.key)
	var (
		done  Done
		total int64
		used  []string
		fail  string
	)
	for round := 1; ; round++ {
		msg, err := t.stream(ctx, &client, msgs, emit)
		if err != nil {
			fail = apiError(err)
			break
		}
		u := Tokens{msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheReadInputTokens, msg.Usage.CacheCreationInputTokens}
		c := cost(priceOf(string(msg.Model), t.model.ID), u)
		done.Tokens.add(u)
		total += c
		if m := string(msg.Model); m != "" && !contains(done.Models, m) {
			done.Models = append(done.Models, m)
		}
		p := msg.ToParam()
		if err := s.save(ctx, t.conv.ID, "answer", p, textOf(msg), t.c.userID(), &answerMeta{
			model: string(msg.Model), tokens: u, cost: c, stop: string(msg.StopReason),
		}); err != nil {
			s.log.ErrorContext(ctx, "antwoord van de assistent bewaren", "err", err)
			fail = "interne fout"
			break
		}
		msgs = append(msgs, p)
		if msg.StopReason != anthropic.BetaStopReasonToolUse {
			done.Note = stopNote(string(msg.StopReason))
			break
		}
		var results []anthropic.BetaContentBlockParamUnion
		for _, b := range msg.Content {
			tu, ok := b.AsAny().(anthropic.BetaToolUseBlock)
			if !ok {
				continue
			}
			if round >= MaxRounds {
				results = append(results, anthropic.NewBetaToolResultBlock(tu.ID, limitResult, true))
				continue
			}
			raw := json.RawMessage(tu.JSON.Input.Raw())
			emit(Event{Type: EventTool, Name: tu.Name, Label: s.label(tu.Name, raw)})
			out, isErr := s.runTool(ctx, t.c, tu.Name, raw)
			used = append(used, tu.Name)
			results = append(results, anthropic.NewBetaToolResultBlock(tu.ID, out, isErr))
		}
		tr := anthropic.NewBetaUserMessage(results...)
		if err := s.save(ctx, t.conv.ID, "tool_results", tr, "", t.c.userID(), nil); err != nil {
			s.log.ErrorContext(ctx, "toolresultaten bewaren", "err", err)
			fail = "interne fout"
			break
		}
		msgs = append(msgs, tr)
		if round >= MaxRounds {
			done.Note = limitNote
			break
		}
	}
	done.CostUSD = USD(total)
	if fail != "" {
		emit(Event{Type: EventError, Text: fail})
	}
	emit(Event{Type: EventDone, Done: &done})
	payload := map[string]any{
		"conversation_id": t.conv.ID.String(), "tools": used, "input_tokens": done.Tokens.Input, "output_tokens": done.Tokens.Output,
		"cache_read_tokens": done.Tokens.CacheRead, "cache_write_tokens": done.Tokens.CacheWrite, "cost_usd": done.CostUSD,
		"model": strings.Join(done.Models, ", "),
	}
	if fail != "" {
		payload["error"] = fail
	}
	if err := s.ev.Write(context.WithoutCancel(ctx), nil, events.Event{
		Actor: events.User(t.c.userID()), SubjectType: "assistant_conversation", SubjectID: t.conv.ID.String(),
		Action: "assistant.asked", Payload: payload,
	}); err != nil {
		s.log.WarnContext(ctx, "vraag aan de assistent in het logboek zetten", "err", err)
	}
}

const (
	limitResult = "De limiet van 12 stappen per vraag is bereikt; deze tool liep niet."
	limitNote   = "De assistent had meer dan 12 stappen nodig en stopte. Stel een gerichtere vraag, bijvoorbeeld over één cluster of een kortere periode."
)

func stopNote(reason string) string {
	switch reason {
	case string(anthropic.BetaStopReasonRefusal):
		return "Het model weigerde deze vraag te beantwoorden."
	case string(anthropic.BetaStopReasonMaxTokens):
		return "Het antwoord werd te lang en is afgebroken."
	}
	return ""
}

// stream vraagt één antwoord en stuurt de tekst door terwijl hij binnenkomt.
func (t *Turn) stream(ctx context.Context, client *anthropic.Client, msgs []anthropic.BetaMessageParam, emit func(Event)) (anthropic.BetaMessage, error) {
	st := client.Beta.Messages.NewStreaming(ctx, t.params(msgs))
	defer func() { _ = st.Close() }()
	var msg anthropic.BetaMessage
	for st.Next() {
		ev := st.Current()
		if err := msg.Accumulate(ev); err != nil {
			return msg, err
		}
		if d, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			if td, ok := d.Delta.AsAny().(anthropic.BetaTextDelta); ok && td.Text != "" {
				emit(Event{Type: EventText, Text: td.Text})
			}
		}
	}
	return msg, st.Err()
}

func textOf(m anthropic.BetaMessage) string {
	var parts []string
	for _, b := range m.Content {
		if tb, ok := b.AsAny().(anthropic.BetaTextBlock); ok && strings.TrimSpace(tb.Text) != "" {
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

type answerMeta struct {
	model  string
	tokens Tokens
	cost   int64
	stop   string
}

func (s *Service) save(ctx context.Context, conv uuid.UUID, role string, p anthropic.BetaMessageParam, text string, user uuid.UUID, m *answerMeta) error {
	content, err := json.Marshal(p)
	if err != nil {
		return err
	}
	args := store.InsertAssistantMessageParams{ConversationID: conv, Role: role, Content: content, Text: text, UserID: &user}
	if m != nil {
		args.Model, args.StopReason, args.CostMicrousd = m.model, m.stop, m.cost
		args.InputTokens, args.OutputTokens = int32(m.tokens.Input), int32(m.tokens.Output)
		args.CacheReadTokens, args.CacheWriteTokens = int32(m.tokens.CacheRead), int32(m.tokens.CacheWrite)
	}
	_, err = s.q.InsertAssistantMessage(context.WithoutCancel(ctx), args)
	return err
}

// title is het begin van de eerste vraag.
func title(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if utf8.RuneCountInString(q) <= 80 {
		return q
	}
	r := []rune(q)
	return strings.TrimSpace(string(r[:79])) + "…"
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// apiError zegt in het Nederlands wat er misging bij de Anthropic API. De
// sleutel staat nooit in de tekst.
func apiError(err error) string {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case http.StatusUnauthorized:
			return "De API-sleutel werkt niet (401). Controleer hem bij Instellingen, Assistent."
		case http.StatusForbidden:
			return "De API-sleutel mag dit model niet gebruiken (403)."
		case http.StatusNotFound:
			return "Het model bestaat niet of is niet beschikbaar voor deze sleutel (404)."
		case http.StatusRequestEntityTooLarge:
			return "Het gesprek is te groot geworden; begin een nieuw gesprek."
		case http.StatusTooManyRequests:
			return "De Anthropic API remt af (429); probeer het zo opnieuw."
		case http.StatusServiceUnavailable, 529:
			return "De Anthropic API is overbelast; probeer het zo opnieuw."
		}
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(ae.RawJSON()), &body)
		if msg := strings.TrimSpace(body.Error.Message); msg != "" {
			return fmt.Sprintf("De Anthropic API weigerde de vraag (%d): %s", ae.StatusCode, msg)
		}
		return fmt.Sprintf("De Anthropic API gaf een fout (%d).", ae.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Het antwoord duurde te lang en is afgebroken."
	}
	if errors.Is(err, context.Canceled) {
		return "De vraag is afgebroken."
	}
	return "De Anthropic API is niet bereikbaar: " + err.Error()
}
