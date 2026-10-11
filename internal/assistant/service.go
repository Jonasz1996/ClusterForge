// Package assistant is de AI-assistent van ClusterForge. Hij beantwoordt
// vragen over de fleet in het Nederlands met de Anthropic API, met vaste
// tools die alleen lezen. De HTTP-server levert die tools aan, met dezelfde
// weergaven en rechten als de API. De assistent voert nooit iets uit.
package assistant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Grenzen per vraag en per gebruiker.
const (
	// MaxRounds is het aantal keer dat het model per vraag tools mag vragen.
	MaxRounds = 12
	// MaxQuestionsPerHour remt één gebruiker.
	MaxQuestionsPerHour = 30
	// MaxQuestion is de langste vraag in tekens.
	MaxQuestion = 4000
	// MaxToolResult is het grootste resultaat van een tool in bytes; de
	// rest wordt afgekapt.
	MaxToolResult = 20 * 1024
	// Retention is hoe lang een gesprek na de laatste vraag blijft staan.
	Retention = 365 * 24 * time.Hour
	// DefaultBudget is het maandbudget in dollar zolang niemand het kiest.
	DefaultBudget = 20
	// maxTokens is het meeste dat één antwoord van het model mag gebruiken,
	// denken inbegrepen.
	maxTokens = 16000
)

var (
	ErrUnavailable = errors.New("de assistent kan niet aan zonder CF_MASTER_KEY; die versleutelt de API-sleutel")
	ErrDisabled    = errors.New("de assistent staat uit; een beheerder zet hem aan bij Instellingen, Assistent")
	ErrForbidden   = errors.New("de assistent is alleen voor beheerders; een beheerder kan viewers toelaten bij Instellingen, Assistent")
	ErrNotFound    = errors.New("gesprek niet gevonden")
	ErrBusy        = errors.New("de assistent beantwoordt in dit gesprek nog een vraag")
)

// ValidationError is foute invoer, met het veld.
type ValidationError struct{ Field, Msg string }

func (e ValidationError) Error() string { return e.Msg }

// LimitError zegt dat het budget of de limiet per uur op is.
type LimitError struct{ Msg string }

func (e LimitError) Error() string { return e.Msg }

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	box  *secrets.Box
	// BaseURL is het adres van de Anthropic API; leeg is de echte. Een test
	// zet hier een nagespeelde API.
	BaseURL string
	// Tools zijn de tools van het model. De HTTP-server zet ze.
	Tools []Tool
	Now   func() time.Time

	mu      sync.Mutex
	running map[uuid.UUID]bool
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, box *secrets.Box) *Service {
	return &Service{pool: pool, q: store.New(pool), ev: ev, log: log, box: box, Now: time.Now, running: map[uuid.UUID]bool{}}
}

// Available is false zonder masterkey: dan kan de sleutel niet versleuteld
// worden en kan de assistent niet aan.
func (s *Service) Available() bool { return s.box != nil }

// Caller is wie de vraag stelt. Principal gaat mee naar de tools, die met
// zijn rechten lezen.
type Caller struct {
	Principal auth.Principal
}

func (c Caller) userID() uuid.UUID { return c.Principal.User.ID }
func (c Caller) admin() bool       { return c.Principal.User.Role == store.UserRoleAdmin }

// Settings zijn de instellingen zonder de sleutel.
type Settings struct {
	Enabled          bool
	HasKey           bool
	Model            string
	MonthlyBudgetUSD int
	ViewerAccess     bool
	UpdatedAt        *time.Time
	UpdatedBy        *uuid.UUID
}

// SettingsInput is een wijziging. Een lege sleutel houdt de oude.
type SettingsInput struct {
	Enabled          bool
	APIKey           string
	Model            string
	MonthlyBudgetUSD int
	ViewerAccess     bool
}

func (s *Service) current(ctx context.Context) (*store.AssistantSetting, error) {
	r, err := s.q.GetAssistantSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Settings geeft de instellingen, of de standaard zolang er geen zijn.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	cur, err := s.current(ctx)
	if err != nil {
		return Settings{}, err
	}
	if cur == nil {
		return Settings{Model: DefaultModel, MonthlyBudgetUSD: DefaultBudget}, nil
	}
	return Settings{
		Enabled: cur.Enabled, HasKey: len(cur.ApiKeyEnc) > 0, Model: cur.Model, MonthlyBudgetUSD: int(cur.MonthlyBudgetUsd),
		ViewerAccess: cur.ViewerAccess, UpdatedAt: &cur.UpdatedAt, UpdatedBy: cur.UpdatedBy,
	}, nil
}

func aad(id uuid.UUID) []byte { return []byte("assistant:" + id.String()) }

func validateKey(key string) error {
	if len(key) > 300 || strings.ContainsAny(key, " \t\r\n") || !strings.HasPrefix(key, "sk-ant-") {
		return ValidationError{"api_key", "dat is geen API-sleutel van Anthropic; die begint met sk-ant-"}
	}
	return nil
}

func validate(in *SettingsInput) error {
	in.APIKey = strings.TrimSpace(in.APIKey)
	if in.APIKey != "" {
		if err := validateKey(in.APIKey); err != nil {
			return err
		}
	}
	if _, ok := modelByID(in.Model); !ok {
		return ValidationError{"model", "kies een van de modellen in de lijst"}
	}
	if in.MonthlyBudgetUSD < 1 || in.MonthlyBudgetUSD > 10000 {
		return ValidationError{"monthly_budget_usd", "het maandbudget ligt tussen $1 en $10.000"}
	}
	return nil
}

// Validate controleert een wijziging voor iemand zijn wachtwoord of code
// invult, zodat een fout in het formulier geen code opmaakt.
func (s *Service) Validate(ctx context.Context, in SettingsInput) error {
	if !s.Available() {
		return ErrUnavailable
	}
	if err := validate(&in); err != nil {
		return err
	}
	if in.Enabled && in.APIKey == "" {
		cur, err := s.current(ctx)
		if err != nil {
			return err
		}
		if cur == nil || len(cur.ApiKeyEnc) == 0 {
			return ValidationError{"api_key", "vul een API-sleutel in om de assistent aan te zetten"}
		}
	}
	return nil
}

// Save bewaart de instellingen. De sleutel wordt versleuteld met een AAD
// gebonden aan de rij; het event zegt alleen dat hij veranderde.
func (s *Service) Save(ctx context.Context, actor events.Actor, by uuid.UUID, in SettingsInput) (Settings, error) {
	if !s.Available() {
		return Settings{}, ErrUnavailable
	}
	if err := validate(&in); err != nil {
		return Settings{}, err
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		cur, err := q.GetAssistantSettings(ctx)
		isNew := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !isNew {
			return err
		}
		if isNew {
			cur = store.AssistantSetting{ID: uuid.New(), Model: DefaultModel, MonthlyBudgetUsd: DefaultBudget}
		}
		enc, keyID := cur.ApiKeyEnc, cur.KeyID
		if in.APIKey != "" {
			enc, keyID = s.box.Seal([]byte(in.APIKey), aad(cur.ID)), s.box.KeyID
		}
		if in.Enabled && len(enc) == 0 {
			return ValidationError{"api_key", "vul een API-sleutel in om de assistent aan te zetten"}
		}
		diff := map[string]any{}
		change := func(field string, from, to any) {
			if from != to {
				diff[field] = map[string]any{"from": from, "to": to}
			}
		}
		change("enabled", cur.Enabled, in.Enabled)
		change("model", cur.Model, in.Model)
		change("monthly_budget_usd", int(cur.MonthlyBudgetUsd), in.MonthlyBudgetUSD)
		change("viewer_access", cur.ViewerAccess, in.ViewerAccess)
		if in.APIKey != "" {
			diff["key_changed"] = true
		}
		if isNew {
			_, err = q.CreateAssistantSettings(ctx, store.CreateAssistantSettingsParams{
				ID: cur.ID, Enabled: in.Enabled, ApiKeyEnc: enc, KeyID: keyID, Model: in.Model,
				MonthlyBudgetUsd: int32(in.MonthlyBudgetUSD), ViewerAccess: in.ViewerAccess, UpdatedBy: &by,
			})
		} else {
			_, err = q.UpdateAssistantSettings(ctx, store.UpdateAssistantSettingsParams{
				ID: cur.ID, Enabled: in.Enabled, ApiKeyEnc: enc, KeyID: keyID, Model: in.Model,
				MonthlyBudgetUsd: int32(in.MonthlyBudgetUSD), ViewerAccess: in.ViewerAccess, UpdatedBy: &by,
			})
		}
		if err != nil || len(diff) == 0 {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "assistant", SubjectID: cur.ID.String(), Action: "assistant.settings_updated", Payload: diff,
		})
	})
	if err != nil {
		return Settings{}, err
	}
	return s.Settings(ctx)
}

// key geeft de opgeslagen sleutel.
func (s *Service) key(cur *store.AssistantSetting) (string, error) {
	if cur == nil || len(cur.ApiKeyEnc) == 0 {
		return "", ValidationError{"api_key", "er is nog geen API-sleutel"}
	}
	b, err := s.box.Open(cur.ApiKeyEnc, aad(cur.ID), cur.KeyID)
	if err != nil {
		return "", ValidationError{"api_key", "de opgeslagen sleutel is niet te ontsleutelen (is CF_MASTER_KEY veranderd?); vul hem opnieuw in"}
	}
	return string(b), nil
}

// access controleert of c de assistent mag gebruiken en geeft de
// instellingen en de sleutel.
func (s *Service) access(ctx context.Context, c Caller) (*store.AssistantSetting, string, error) {
	if !s.Available() {
		return nil, "", ErrUnavailable
	}
	cur, err := s.current(ctx)
	if err != nil {
		return nil, "", err
	}
	if cur == nil || !cur.Enabled {
		return nil, "", ErrDisabled
	}
	if !c.admin() && !cur.ViewerAccess {
		return nil, "", ErrForbidden
	}
	key, err := s.key(cur)
	if err != nil {
		return nil, "", err
	}
	return cur, key, nil
}

// CanUse zegt of c de assistent nu kan gebruiken, en anders waarom niet.
func (s *Service) CanUse(ctx context.Context, c Caller) (bool, string, error) {
	_, _, err := s.access(ctx, c)
	var ve ValidationError
	switch {
	case err == nil:
		return true, "", nil
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrDisabled), errors.Is(err, ErrForbidden), errors.As(err, &ve):
		return false, err.Error(), nil
	}
	return false, "", err
}

// monthStart is het begin van de maand in de tijdzone van de server.
func monthStart(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

// Usage is het verbruik van deze maand.
type Usage struct {
	Since     time.Time
	SpentUSD  float64
	BudgetUSD int
	Users     []UserUsage
}

type UserUsage struct {
	UserID    *uuid.UUID
	Username  string
	Questions int
	CostUSD   float64
}

// MonthUsage geeft het verbruik van deze maand; per gebruiker alleen voor
// een admin.
func (s *Service) MonthUsage(ctx context.Context, c Caller) (Usage, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return Usage{}, err
	}
	since := monthStart(s.Now())
	spent, err := s.q.AssistantCostSince(ctx, since)
	if err != nil {
		return Usage{}, err
	}
	u := Usage{Since: since, SpentUSD: USD(spent), BudgetUSD: st.MonthlyBudgetUSD, Users: []UserUsage{}}
	if !c.admin() {
		return u, nil
	}
	rows, err := s.q.AssistantUsageByUser(ctx, since)
	if err != nil {
		return Usage{}, err
	}
	for _, r := range rows {
		u.Users = append(u.Users, UserUsage{UserID: r.UserID, Username: r.Username, Questions: int(r.Questions), CostUSD: USD(r.CostMicrousd)})
	}
	return u, nil
}

// checkLimits weigert een vraag als het budget van de maand of de limiet
// per uur op is. Het eerste keer per maand dat het budget op is, komt in het
// logboek.
func (s *Service) checkLimits(ctx context.Context, c Caller, cur *store.AssistantSetting) error {
	now := s.Now()
	n, err := s.q.CountQuestionsSince(ctx, store.CountQuestionsSinceParams{UserID: ptr(c.userID()), Since: now.Add(-time.Hour)})
	if err != nil {
		return err
	}
	if n >= MaxQuestionsPerHour {
		return LimitError{fmt.Sprintf("Je stelde het afgelopen uur al %d vragen; dat is het maximum. Probeer het straks opnieuw.", MaxQuestionsPerHour)}
	}
	since := monthStart(now)
	spent, err := s.q.AssistantCostSince(ctx, since)
	if err != nil {
		return err
	}
	budget := int64(cur.MonthlyBudgetUsd) * 1_000_000
	if spent < budget {
		return nil
	}
	logged, err := s.q.EventSince(ctx, store.EventSinceParams{Action: "assistant.budget_reached", Since: since})
	if err != nil {
		return err
	}
	if !logged {
		_ = s.ev.Write(ctx, nil, events.Event{
			Actor: events.System(), SubjectType: "assistant", SubjectID: cur.ID.String(), Action: "assistant.budget_reached",
			Payload: map[string]any{"spent_usd": USD(spent), "budget_usd": cur.MonthlyBudgetUsd},
		})
	}
	next := since.AddDate(0, 1, 0)
	return LimitError{fmt.Sprintf("Het maandbudget van de assistent ($%d) is op. Vanaf %d %s kan het weer, of een beheerder verhoogt het budget bij Instellingen, Assistent.",
		cur.MonthlyBudgetUsd, next.Day(), monthNames[next.Month()-1])}
}

var monthNames = []string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}

func ptr[T any](v T) *T { return &v }

// Prune ruimt gesprekken op die langer dan een jaar stilliggen. Het is een
// taak van de planner.
func (s *Service) Prune(ctx context.Context, now time.Time) error {
	_, err := s.q.PruneConversations(ctx, now.Add(-Retention))
	return err
}
