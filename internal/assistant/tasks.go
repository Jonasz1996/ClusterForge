package assistant

import (
	"context"
	"errors"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// ErrRunning zegt dat de assistent al met dezelfde opdracht bezig is, zoals
// een tweede analyse van hetzelfde incident.
var ErrRunning = errors.New("de assistent is hier al mee bezig; wacht tot het antwoord er is")

// Effort is hoe grondig het model nadenkt.
type Effort = anthropic.BetaOutputConfigEffort

const (
	EffortLow    = anthropic.BetaOutputConfigEffortLow
	EffortMedium = anthropic.BetaOutputConfigEffortMedium
	EffortHigh   = anthropic.BetaOutputConfigEffortHigh
)

// Task is een vaste opdracht aan de assistent: een analyse van een incident,
// de uitleg van een bevinding of een samenvatting. Ze krijgt een eigen
// gesprek, met dezelfde tools, limieten en kosten als een gewone vraag, zodat
// je er daarna in de assistent op kunt doorvragen.
type Task struct {
	Kind  string
	Title string
	// Question is wat het gesprek toont; Prompt is de opdracht aan het
	// model, met de gegevens.
	Question string
	Prompt   string
	Effort   Effort
	// Het onderwerp van het gesprek, zoals een cluster of incident.
	SubjectType string
	SubjectID   *uuid.UUID
	IncidentID  *uuid.UUID
	// Claim houdt een tweede opdracht over hetzelfde tegen zolang de eerste
	// loopt.
	Claim *uuid.UUID
	// Finish bewaart het resultaat als het model klaar was.
	Finish func(ctx context.Context, r Result) error
}

// Start controleert een opdracht (toegang, budget, limiet per uur), begint
// het gesprek en geeft de vraag die Run stelt.
func (s *Service) Start(ctx context.Context, c Caller, t Task) (*Turn, error) {
	cur, key, err := s.access(ctx, c)
	if err != nil {
		return nil, err
	}
	if err := s.checkLimits(ctx, c, cur); err != nil {
		return nil, err
	}
	if t.Claim != nil && !s.claim(*t.Claim) {
		return nil, ErrRunning
	}
	claims := []uuid.UUID{}
	if t.Claim != nil {
		claims = append(claims, *t.Claim)
	}
	release := func() {
		for _, id := range claims {
			s.release(id)
		}
	}
	r, err := s.q.CreateConversation(ctx, store.CreateConversationParams{
		UserID: c.user(), Title: title(t.Title), SubjectType: t.SubjectType, SubjectID: t.SubjectID, IncidentID: t.IncidentID, Kind: t.Kind,
	})
	if err != nil {
		release()
		return nil, err
	}
	s.claim(r.ID)
	claims = append(claims, r.ID)
	model, ok := modelByID(cur.Model)
	if !ok {
		model = Models[0]
	}
	effort := t.Effort
	if effort == "" {
		effort = EffortMedium
	}
	conv := store.GetConversationRow{
		ID: r.ID, UserID: r.UserID, Title: r.Title, SubjectType: r.SubjectType, SubjectID: r.SubjectID, IncidentID: r.IncidentID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Kind: r.Kind, Username: c.Principal.User.Username,
	}
	return &Turn{
		s: s, c: c, conv: conv, model: model, key: key, question: t.Question, prompt: t.Prompt, effort: effort,
		claims: claims, finish: t.Finish,
	}, nil
}
