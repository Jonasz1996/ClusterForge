package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// askTimeout is hoe lang één vraag met al zijn rondes mag duren.
const askTimeout = 10 * time.Minute

func caller(r *http.Request) assistant.Caller {
	p, _ := principalFrom(r.Context())
	return assistant.Caller{Principal: p}
}

// assistantError schrijft een passende foutrespons en geeft true terug als
// err niet nil was.
func (s *Server) assistantError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ve assistant.ValidationError
	var le assistant.LimitError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.As(err, &le):
		writeError(w, http.StatusTooManyRequests, "limit", le.Msg)
	case errors.Is(err, assistant.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, assistant.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, assistant.ErrUnavailable):
		writeError(w, http.StatusConflict, "unavailable", err.Error())
	case errors.Is(err, assistant.ErrDisabled):
		writeError(w, http.StatusConflict, "disabled", err.Error())
	case errors.Is(err, assistant.ErrBusy):
		writeError(w, http.StatusConflict, "busy", err.Error())
	default:
		s.internalError(w, r, err)
	}
	return true
}

func (s *Server) assistantSettings(ctx context.Context, c assistant.Caller) (gen.AssistantSettings, error) {
	st, err := s.assistant.Settings(ctx)
	if err != nil {
		return gen.AssistantSettings{}, err
	}
	can, reason, err := s.assistant.CanUse(ctx, c)
	if err != nil {
		return gen.AssistantSettings{}, err
	}
	out := gen.AssistantSettings{
		Available: s.assistant.Available(), CanUse: can, Reason: reason, Enabled: st.Enabled, HasKey: st.HasKey,
		Model: st.Model, MonthlyBudgetUsd: st.MonthlyBudgetUSD, ViewerAccess: st.ViewerAccess,
		Models: make([]gen.AssistantModel, len(assistant.Models)), UpdatedAt: nullableOf(st.UpdatedAt),
		UpdatedBy: nullableOf[string](nil),
	}
	for i, m := range assistant.Models {
		out.Models[i] = gen.AssistantModel{Id: m.ID, Label: m.Label, InputUsd: float32(m.Price.Input), OutputUsd: float32(m.Price.Output)}
	}
	if st.UpdatedBy != nil {
		if u, err := s.q.GetUserByID(ctx, *st.UpdatedBy); err == nil {
			out.UpdatedBy = nullableOf(&u.Username)
		}
	}
	return out, nil
}

func (s *Server) GetAssistantSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.assistantSettings(r.Context(), caller(r))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) SaveAssistantSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.AssistantSettingsInput
	if !decode(w, r, &req) {
		return
	}
	in := assistant.SettingsInput{
		Enabled: req.Enabled, APIKey: deref(req.ApiKey), Model: req.Model, MonthlyBudgetUSD: req.MonthlyBudgetUsd,
		ViewerAccess: req.ViewerAccess,
	}
	if s.assistantError(w, r, s.assistant.Validate(r.Context(), in)) {
		return
	}
	// De sleutel kost geld en leest de hele fleet; een gestolen sessie mag
	// hem niet kunnen vervangen.
	if s.authError(w, r, s.auth.Confirm(r.Context(), p, deref(req.Password), deref(req.TotpCode), "assistant")) {
		return
	}
	if _, err := s.assistant.Save(r.Context(), events.User(p.User.ID), p.User.ID, in); s.assistantError(w, r, err) {
		return
	}
	out, err := s.assistantSettings(r.Context(), caller(r))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) TestAssistantKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var req gen.AssistantKeyTest
	if !decode(w, r, &req) {
		return
	}
	res, err := s.assistant.Test(r.Context(), deref(req.ApiKey), req.Model)
	if s.assistantError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, gen.AssistantKeyTestResult{Ok: res.OK, Model: res.Model, Message: res.Message})
}

func (s *Server) GetAssistantUsage(w http.ResponseWriter, r *http.Request) {
	u, err := s.assistant.MonthUsage(r.Context(), caller(r))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.AssistantUsage{Since: u.Since, SpentUsd: float32(u.SpentUSD), BudgetUsd: u.BudgetUSD}
	out.Users = make([]struct {
		CostUsd   float32 `json:"cost_usd"`
		Questions int     `json:"questions"`
		Username  string  `json:"username"`
	}, len(u.Users))
	for i, uu := range u.Users {
		out.Users[i].CostUsd, out.Users[i].Questions, out.Users[i].Username = float32(uu.CostUSD), uu.Questions, uu.Username
	}
	writeJSON(w, http.StatusOK, out)
}

func toAPIConversation(c assistant.Conversation) gen.AssistantConversation {
	return gen.AssistantConversation{
		Id: c.ID, Title: c.Title, Username: c.Username, SubjectType: c.SubjectType, SubjectId: nullableOf(c.SubjectID),
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, CostUsd: float32(c.CostUSD),
	}
}

func (s *Server) ListAssistantConversations(w http.ResponseWriter, r *http.Request, params gen.ListAssistantConversationsParams) {
	list, err := s.assistant.List(r.Context(), caller(r), deref(params.All))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.AssistantConversationList{Items: make([]gen.AssistantConversation, len(list))}
	for i, c := range list {
		out.Items[i] = toAPIConversation(c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CreateAssistantConversation(w http.ResponseWriter, r *http.Request) {
	var req gen.AssistantConversationInput
	if !decode(w, r, &req) {
		return
	}
	conv, err := s.assistant.Create(r.Context(), caller(r), string(deref(req.SubjectType)), req.SubjectId)
	if s.assistantError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, toAPIConversation(conv))
}

func (s *Server) GetAssistantConversation(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	conv, err := s.assistant.Get(r.Context(), caller(r), id)
	if s.assistantError(w, r, err) {
		return
	}
	out := gen.AssistantConversationDetail{Conversation: toAPIConversation(conv), Turns: make([]gen.AssistantTurn, len(conv.Turns))}
	for i, t := range conv.Turns {
		at := gen.AssistantTurn{
			Question: t.Question, AskedAt: t.AskedAt, Answer: t.Answer, Models: t.Models, Note: t.Note, CostUsd: float32(t.CostUSD),
			InputTokens: int(t.Tokens.Input), OutputTokens: int(t.Tokens.Output), CacheReadTokens: int(t.Tokens.CacheRead),
			CacheWriteTokens: int(t.Tokens.CacheWrite),
		}
		at.Tools = make([]struct {
			Error bool   `json:"error"`
			Label string `json:"label"`
			Name  string `json:"name"`
		}, len(t.Tools))
		for j, tu := range t.Tools {
			at.Tools[j].Error, at.Tools[j].Label, at.Tools[j].Name = tu.Error, tu.Label, tu.Name
		}
		out.Turns[i] = at
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) DeleteAssistantConversation(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if s.assistantError(w, r, s.assistant.Delete(r.Context(), caller(r), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AskAssistant stelt een vraag en stuurt het antwoord als text/event-stream
// terug, terwijl het binnenkomt. Wat misgaat voor het antwoord begint, is een
// gewone foutrespons; daarna een event error.
func (s *Server) AskAssistant(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	var req gen.AssistantQuestion
	if !decode(w, r, &req) {
		return
	}
	turn, err := s.assistant.Prepare(r.Context(), caller(r), id, req.Question)
	if s.assistantError(w, r, err) {
		return
	}
	rc := http.NewResponseController(w)
	// Een antwoord met tools duurt langer dan de schrijftimeout van de server.
	_ = rc.SetWriteDeadline(time.Now().Add(askTimeout + time.Minute))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Zodat een reverse proxy als nginx de stukken meteen doorgeeft.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var (
		mu   sync.Mutex
		gone bool
	)
	write := func(chunk string) {
		mu.Lock()
		defer mu.Unlock()
		if gone {
			return
		}
		if _, err := fmt.Fprint(w, chunk); err != nil || rc.Flush() != nil {
			gone = true
		}
	}
	send := func(ev assistant.Event) {
		var data any
		switch ev.Type {
		case assistant.EventText:
			data = map[string]string{"text": ev.Text}
		case assistant.EventTool:
			data = map[string]string{"name": ev.Name, "label": ev.Label}
		case assistant.EventError:
			data = map[string]string{"message": ev.Text}
		case assistant.EventDone:
			d := ev.Done
			data = map[string]any{
				"models": nonNil(d.Models), "input_tokens": d.Tokens.Input, "output_tokens": d.Tokens.Output,
				"cache_read_tokens": d.Tokens.CacheRead, "cache_write_tokens": d.Tokens.CacheWrite, "cost_usd": d.CostUSD, "note": d.Note,
			}
		}
		b, _ := json.Marshal(data)
		write("event: " + ev.Type + "\ndata: " + string(b) + "\n\n")
	}

	// Gaat de browser weg, dan loopt de vraag door tot het einde, zodat het
	// antwoord en de kosten bewaard worden.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), askTimeout)
	defer cancel()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Een commentaar om de 15 seconden houdt proxy's tussen de browser en
		// ClusterForge wakker terwijl het model denkt of een tool leest.
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				write(": wacht\n\n")
			}
		}
	}()
	turn.Run(ctx, send)
	close(stop)
	wg.Wait()
}
