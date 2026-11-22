package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Conversation is een gesprek, met de vragen en antwoorden als het
// gesprek zelf gevraagd is.
type Conversation struct {
	ID uuid.UUID
	// UserID is nil voor een gesprek van ClusterForge zelf.
	UserID      *uuid.UUID
	Username    string
	Title       string
	Kind        string
	SubjectType string
	SubjectID   *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CostUSD     float64
	// CanAsk is true als wie het gesprek opvraagt er verder in mag vragen:
	// alleen in zijn eigen gesprekken.
	CanAsk bool
	Turns  []TurnView
}

// De soorten gesprekken: een vraag van een gebruiker, of een vaste opdracht.
const (
	KindChat     = "chat"
	KindAnalysis = "analysis"
	KindExplain  = "explain"
	KindSummary  = "summary"
)

// TurnView is één vraag met wat erop volgde, zoals de webinterface hem
// toont.
type TurnView struct {
	Question string
	AskedAt  time.Time
	Answer   string
	Tools    []ToolUse
	Models   []string
	Tokens   Tokens
	CostUSD  float64
	Note     string
}

// ToolUse is een tool die liep, met zijn zin.
type ToolUse struct {
	Name  string
	Label string
	Error bool
}

// Subjecten waarop een gesprek kan beginnen, met de vraagknop.
const (
	SubjectCluster  = "cluster"
	SubjectNode     = "node"
	SubjectIncident = "incident"
)

// Create begint een gesprek, eventueel over een cluster, node of incident.
func (s *Service) Create(ctx context.Context, c Caller, subjectType string, subjectID *uuid.UUID) (Conversation, error) {
	if _, _, err := s.access(ctx, c); err != nil {
		return Conversation{}, err
	}
	args := store.CreateConversationParams{UserID: c.user(), SubjectType: subjectType, SubjectID: subjectID, Kind: KindChat}
	switch subjectType {
	case "":
		args.SubjectID = nil
	case SubjectCluster, SubjectNode, SubjectIncident:
		if subjectID == nil {
			return Conversation{}, ValidationError{"subject_id", "kies een onderwerp"}
		}
		if s.subjectName(ctx, subjectType, *subjectID) == "" {
			return Conversation{}, ValidationError{"subject_id", "dat onderwerp bestaat niet"}
		}
		if subjectType == SubjectIncident {
			args.IncidentID = subjectID
		}
	default:
		return Conversation{}, ValidationError{"subject_type", "het onderwerp is een cluster, node of incident"}
	}
	r, err := s.q.CreateConversation(ctx, args)
	if err != nil {
		return Conversation{}, err
	}
	return Conversation{
		ID: r.ID, UserID: r.UserID, Username: c.Principal.User.Username, Title: r.Title, Kind: r.Kind, SubjectType: r.SubjectType,
		SubjectID: r.SubjectID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CanAsk: true,
	}, nil
}

// subjectName is het onderwerp in woorden, of leeg als het niet bestaat.
func (s *Service) subjectName(ctx context.Context, typ string, id uuid.UUID) string {
	switch typ {
	case SubjectCluster:
		if cl, err := s.q.GetCluster(ctx, id); err == nil {
			return fmt.Sprintf("cluster %s (id %s)", cl.Name, id)
		}
	case SubjectNode:
		if n, err := s.q.GetNode(ctx, id); err == nil {
			return fmt.Sprintf("node %s (id %s)", n.Node.Hostname, id)
		}
	case SubjectIncident:
		if inc, err := s.q.GetIncident(ctx, id); err == nil {
			return fmt.Sprintf("incident #%d, %q (id %s)", inc.Number, inc.Title, id)
		}
	}
	return ""
}

func (s *Service) subject(ctx context.Context, conv store.GetConversationRow) string {
	if conv.SubjectType == "" || conv.SubjectID == nil {
		return ""
	}
	return s.subjectName(ctx, conv.SubjectType, *conv.SubjectID)
}

// List geeft de gesprekken van c, nieuwste eerst; een admin kan die van
// iedereen vragen.
func (s *Service) List(ctx context.Context, c Caller, everyone bool) ([]Conversation, error) {
	rows, err := s.q.ListConversations(ctx, store.ListConversationsParams{
		Everyone: everyone && c.admin(), UserID: c.user(), Lim: 200,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Conversation, len(rows))
	for i, r := range rows {
		out[i] = Conversation{
			ID: r.ID, UserID: r.UserID, Username: r.Username, Title: r.Title, Kind: r.Kind, SubjectType: r.SubjectType,
			SubjectID: r.SubjectID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CostUSD: USD(r.CostMicrousd),
			CanAsk: c.owns(r.UserID),
		}
	}
	return out, nil
}

// own geeft een gesprek dat c mag zien: zijn eigen, of elk voor een admin.
func (s *Service) own(ctx context.Context, c Caller, id uuid.UUID) (store.GetConversationRow, error) {
	r, err := s.q.GetConversation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !c.owns(r.UserID) && !c.admin()) {
		return r, ErrNotFound
	}
	return r, err
}

// Get geeft een gesprek met zijn vragen en antwoorden.
func (s *Service) Get(ctx context.Context, c Caller, id uuid.UUID) (Conversation, error) {
	r, err := s.own(ctx, c, id)
	if err != nil {
		return Conversation{}, err
	}
	rows, err := s.q.ListConversationMessages(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	conv := Conversation{
		ID: r.ID, UserID: r.UserID, Username: r.Username, Title: r.Title, Kind: r.Kind, SubjectType: r.SubjectType,
		SubjectID: r.SubjectID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CanAsk: c.owns(r.UserID), Turns: s.turns(rows),
	}
	for _, t := range conv.Turns {
		conv.CostUSD += t.CostUSD
	}
	return conv, nil
}

// Delete wist een gesprek.
func (s *Service) Delete(ctx context.Context, c Caller, id uuid.UUID) error {
	if _, err := s.own(ctx, c, id); err != nil {
		return err
	}
	_, err := s.q.DeleteConversation(ctx, id)
	return err
}

// blocks is wat turns uit de bewaarde inhoud leest.
type blocks struct {
	Content []struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
	} `json:"content"`
}

// turns zet de berichten om in vragen met hun antwoord.
func (s *Service) turns(rows []store.AssistantMessage) []TurnView {
	out := []TurnView{}
	var cur *TurnView
	var micro int64
	answers, last := 0, ""
	flush := func() {
		if cur == nil {
			return
		}
		switch {
		case cur.Note != "":
		case last == "question":
			cur.Note = "Er kwam geen antwoord; stel de vraag opnieuw."
		case last == "tool_results" && answers >= MaxRounds:
			cur.Note = limitNote
		case last == "tool_results":
			cur.Note = "Het antwoord werd onderbroken; stel de vraag opnieuw."
		}
		cur.CostUSD = USD(micro)
		out = append(out, *cur)
	}
	for _, r := range rows {
		var b blocks
		_ = json.Unmarshal(r.Content, &b)
		switch r.Role {
		case "question":
			flush()
			cur, micro, answers = &TurnView{Question: r.Text, AskedAt: r.CreatedAt, Tools: []ToolUse{}, Models: []string{}}, 0, 0
		case "answer":
			if cur == nil {
				continue
			}
			answers++
			if strings.TrimSpace(r.Text) != "" {
				if cur.Answer != "" {
					cur.Answer += "\n\n"
				}
				cur.Answer += r.Text
			}
			cur.Tokens.add(Tokens{int64(r.InputTokens), int64(r.OutputTokens), int64(r.CacheReadTokens), int64(r.CacheWriteTokens)})
			micro += r.CostMicrousd
			if r.Model != "" && !contains(cur.Models, r.Model) {
				cur.Models = append(cur.Models, r.Model)
			}
			cur.Note = stopNote(r.StopReason)
			for _, c := range b.Content {
				if c.Type == "tool_use" {
					cur.Tools = append(cur.Tools, ToolUse{Name: c.Name, Label: s.label(c.Name, c.Input)})
				}
			}
		case "tool_results":
			if cur == nil {
				continue
			}
			// De resultaten staan in de volgorde van de aanroepen; de
			// laatste aanroepen horen bij dit bericht.
			start := len(cur.Tools) - len(b.Content)
			for i, c := range b.Content {
				if start+i >= 0 && c.IsError {
					cur.Tools[start+i].Error = true
				}
			}
		}
		last = r.Role
	}
	flush()
	return out
}

// TestResult is de uitkomst van een proefvraag met de sleutel: welk model
// antwoordde, of wat er misging.
type TestResult struct {
	OK      bool
	Model   string
	Message string
}

// Test stelt één kleine vraag met de sleutel en het model, zonder iets op
// te slaan. Een lege sleutel test de opgeslagen.
func (s *Service) Test(ctx context.Context, key, model string) (TestResult, error) {
	if !s.Available() {
		return TestResult{}, ErrUnavailable
	}
	key = strings.TrimSpace(key)
	if key == "" {
		cur, err := s.current(ctx)
		if err != nil {
			return TestResult{}, err
		}
		if key, err = s.key(cur); err != nil {
			return TestResult{}, err
		}
	} else if err := validateKey(key); err != nil {
		return TestResult{}, err
	}
	if _, ok := modelByID(model); !ok {
		return TestResult{}, ValidationError{"model", "kies een van de modellen in de lijst"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := s.client(key)
	msg, err := client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model: anthropic.Model(model), MaxTokens: 512,
		Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("Antwoord alleen met OK."))},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow},
	})
	if err != nil {
		return TestResult{Message: apiError(err)}, nil
	}
	return TestResult{OK: true, Model: string(msg.Model)}, nil
}
