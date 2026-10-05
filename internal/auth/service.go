// Package auth regelt gebruikers, wachtwoorden, TOTP en sessies.
package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

var (
	ErrInvalidCredentials = errors.New("onjuiste gebruikersnaam of wachtwoord")
	ErrTOTPRequired       = errors.New("TOTP-code vereist")
	ErrInvalidTOTP        = errors.New("onjuiste TOTP-code")
	ErrTOTPAlreadyEnabled = errors.New("TOTP staat al aan")
	ErrTOTPNotPending     = errors.New("start eerst de TOTP-instelling")
	ErrNoSession          = errors.New("geen geldige sessie")
	ErrUsernameTaken      = errors.New("gebruikersnaam bestaat al")
)

// ValidationError is een fout in invoer van de gebruiker.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,64}$`)

// touchInterval bepaalt hoe vaak een actieve sessie in de database wordt
// bijgewerkt; elke request schrijven is onnodig.
const touchInterval = time.Minute

type Service struct {
	q   *store.Queries
	ev  *events.Writer
	ttl time.Duration
	now func() time.Time
	// dummyHash wordt gecontroleerd bij een onbekende gebruiker, zodat de
	// responstijd niet verraadt of een gebruikersnaam bestaat.
	dummyHash string
}

func NewService(q *store.Queries, ev *events.Writer, sessionTTL time.Duration) (*Service, error) {
	h, err := HashPassword("clusterforge-dummy-password")
	if err != nil {
		return nil, err
	}
	return &Service{q: q, ev: ev, ttl: sessionTTL, now: time.Now, dummyHash: h}, nil
}

// Session is een net aangemaakte sessie. Token gaat in de cookie en wordt
// nergens opgeslagen.
type Session struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
	User      store.User
}

// Principal is de ingelogde gebruiker achter een request.
type Principal struct {
	User      store.User
	SessionID []byte
	CSRFToken string
	ExpiresAt time.Time
	// Refreshed is true als de sessie bij deze request verlengd is; de
	// cookie moet dan ook een nieuwe vervaldatum krijgen.
	Refreshed bool
}

type LoginInput struct {
	Username  string
	Password  string
	TOTPCode  string
	IP        string
	UserAgent string
}

func (s *Service) Login(ctx context.Context, in LoginInput) (Session, error) {
	user, err := s.q.GetUserByUsername(ctx, in.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		_, _ = VerifyPassword(in.Password, s.dummyHash)
		s.loginFailed(ctx, nil, in, "unknown_user")
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	ok, err := VerifyPassword(in.Password, user.PasswordHash)
	if err != nil {
		return Session{}, fmt.Errorf("wachtwoord controleren: %w", err)
	}
	if !ok {
		s.loginFailed(ctx, &user, in, "bad_password")
		return Session{}, ErrInvalidCredentials
	}
	if user.DisabledAt != nil {
		s.loginFailed(ctx, &user, in, "disabled")
		return Session{}, ErrInvalidCredentials
	}
	if user.TotpEnabledAt != nil {
		if in.TOTPCode == "" {
			return Session{}, ErrTOTPRequired
		}
		if err := s.useTOTP(ctx, user, in.TOTPCode); err != nil {
			if errors.Is(err, ErrInvalidTOTP) {
				s.loginFailed(ctx, &user, in, "bad_totp")
			}
			return Session{}, err
		}
	}

	token, err := newToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := newToken()
	if err != nil {
		return Session{}, err
	}
	expires := s.now().Add(s.ttl)
	err = s.q.CreateSession(ctx, store.CreateSessionParams{
		ID:        hashToken(token),
		UserID:    user.ID,
		CsrfToken: csrf,
		Ip:        in.IP,
		UserAgent: truncate(in.UserAgent, 512),
		ExpiresAt: expires,
	})
	if err != nil {
		return Session{}, fmt.Errorf("sessie aanmaken: %w", err)
	}
	// De login hoort al bij de nieuwe sessie.
	_ = s.ev.Write(events.WithRequest(ctx, in.IP, hashToken(token)), nil, events.Event{
		Actor:       events.User(user.ID),
		SubjectType: "user",
		SubjectID:   user.ID.String(),
		Action:      "auth.login",
		Payload:     map[string]any{"ip": in.IP, "user_agent": truncate(in.UserAgent, 200)},
	})
	return Session{Token: token, CSRFToken: csrf, ExpiresAt: expires, User: user}, nil
}

// loginFailed legt een mislukte poging vast. Wat er getypt werd, komt er
// nooit in: in het naamveld staat soms per ongeluk een wachtwoord.
func (s *Service) loginFailed(ctx context.Context, user *store.User, in LoginInput, reason string) {
	subject := "onbekend"
	if user != nil {
		subject = user.ID.String()
	}
	_ = s.ev.Write(ctx, nil, events.Event{
		Actor:       events.System(),
		SubjectType: "user",
		SubjectID:   subject,
		Action:      "auth.login_failed",
		Payload:     map[string]any{"ip": in.IP, "reason": reason},
	})
}

// Authenticate zoekt de sessie achter een token op en verlengt hem.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrNoSession
	}
	id := hashToken(token)
	row, err := s.q.GetSessionWithUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrNoSession
	}
	if err != nil {
		return Principal{}, err
	}
	p := Principal{User: row.User, SessionID: id, CSRFToken: row.Session.CsrfToken, ExpiresAt: row.Session.ExpiresAt}
	now := s.now()
	if now.Sub(row.Session.LastSeenAt) > touchInterval {
		p.ExpiresAt = now.Add(s.ttl)
		p.Refreshed = true
		if err := s.q.TouchSession(ctx, store.TouchSessionParams{ID: id, ExpiresAt: p.ExpiresAt}); err != nil {
			return Principal{}, err
		}
	}
	return p, nil
}

func (s *Service) Logout(ctx context.Context, p Principal) error {
	if err := s.q.DeleteSession(ctx, p.SessionID); err != nil {
		return err
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor:       events.User(p.User.ID),
		SubjectType: "user",
		SubjectID:   p.User.ID.String(),
		Action:      "auth.logout",
	})
}

// CreateUser maakt een gebruiker aan. actor is wie het doet; voor de eerste
// beheerder via de CLI is dat het systeem.
func (s *Service) CreateUser(ctx context.Context, actor events.Actor, username, password string, role store.UserRole) (store.User, error) {
	if !usernameRe.MatchString(username) {
		return store.User{}, ValidationError{"gebruikersnaam: 2 tot 64 tekens, letters, cijfers, punt, streepje of underscore"}
	}
	if err := checkPassword(password); err != nil {
		return store.User{}, err
	}
	if role != store.UserRoleAdmin && role != store.UserRoleViewer {
		return store.User{}, ValidationError{"rol moet admin of viewer zijn"}
	}
	hash, err := HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	user, err := s.q.CreateUser(ctx, store.CreateUserParams{Username: username, PasswordHash: hash, Role: role})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return store.User{}, ErrUsernameTaken
	}
	if err != nil {
		return store.User{}, err
	}
	_ = s.ev.Write(ctx, nil, events.Event{
		Actor:       actor,
		SubjectType: "user",
		SubjectID:   user.ID.String(),
		Action:      "user.created",
		Payload:     map[string]any{"username": user.Username, "role": string(user.Role)},
	})
	return user, nil
}

// ChangePassword vervangt het wachtwoord en beëindigt alle andere sessies.
func (s *Service) ChangePassword(ctx context.Context, p Principal, current, next string) error {
	ok, err := VerifyPassword(current, p.User.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		s.reauthFailed(ctx, p.User, "password_change", "bad_password")
		return ErrInvalidCredentials
	}
	if err := checkPassword(next); err != nil {
		return err
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: p.User.ID, PasswordHash: hash}); err != nil {
		return err
	}
	if err := s.q.DeleteUserSessionsExcept(ctx, store.DeleteUserSessionsExceptParams{UserID: p.User.ID, ID: p.SessionID}); err != nil {
		return err
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: events.User(p.User.ID), SubjectType: "user", SubjectID: p.User.ID.String(),
		Action: "auth.password_changed",
	})
}

// BeginTOTPSetup maakt een nieuw geheim aan dat pas actief wordt na
// EnableTOTP met een geldige code.
func (s *Service) BeginTOTPSetup(ctx context.Context, p Principal) (secret, otpauthURL string, err error) {
	if p.User.TotpEnabledAt != nil {
		return "", "", ErrTOTPAlreadyEnabled
	}
	key, err := newTOTPKey(p.User.Username)
	if err != nil {
		return "", "", err
	}
	sec := key.Secret()
	if err := s.q.SetPendingTOTPSecret(ctx, store.SetPendingTOTPSecretParams{ID: p.User.ID, TotpSecret: &sec}); err != nil {
		return "", "", err
	}
	_ = s.ev.Write(ctx, nil, events.Event{
		Actor: events.User(p.User.ID), SubjectType: "user", SubjectID: p.User.ID.String(),
		Action: "auth.totp_setup_started",
	})
	return sec, key.URL(), nil
}

func (s *Service) EnableTOTP(ctx context.Context, p Principal, code string) error {
	user, err := s.q.GetUserByID(ctx, p.User.ID)
	if err != nil {
		return err
	}
	if user.TotpEnabledAt != nil {
		return ErrTOTPAlreadyEnabled
	}
	if user.TotpSecret == nil {
		return ErrTOTPNotPending
	}
	if err := s.useTOTP(ctx, user, code); err != nil {
		if errors.Is(err, ErrInvalidTOTP) {
			s.reauthFailed(ctx, user, "totp_enable", "bad_totp")
		}
		return err
	}
	if err := s.q.EnableTOTP(ctx, user.ID); err != nil {
		return err
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: events.User(user.ID), SubjectType: "user", SubjectID: user.ID.String(),
		Action: "auth.totp_enabled",
	})
}

// DisableTOTP vraagt wachtwoord én een geldige code, zodat een gestolen
// sessie de tweede factor niet kan uitzetten.
func (s *Service) DisableTOTP(ctx context.Context, p Principal, password, code string) error {
	user, err := s.q.GetUserByID(ctx, p.User.ID)
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		s.reauthFailed(ctx, user, "totp_disable", "bad_password")
		return ErrInvalidCredentials
	}
	if user.TotpEnabledAt == nil || user.TotpSecret == nil {
		return nil
	}
	if err := s.useTOTP(ctx, user, code); err != nil {
		if errors.Is(err, ErrInvalidTOTP) {
			s.reauthFailed(ctx, user, "totp_disable", "bad_totp")
		}
		return err
	}
	if err := s.q.DisableTOTP(ctx, user.ID); err != nil {
		return err
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: events.User(user.ID), SubjectType: "user", SubjectID: user.ID.String(),
		Action: "auth.totp_disabled",
	})
}

// reauthFailed legt vast dat een ingelogde gebruiker zijn wachtwoord of code
// fout invulde bij een gevoelige wijziging. what zegt welke.
func (s *Service) reauthFailed(ctx context.Context, user store.User, what, reason string) {
	_ = s.ev.Write(ctx, nil, events.Event{
		Actor: events.User(user.ID), SubjectType: "user", SubjectID: user.ID.String(),
		Action: "auth.reauth_failed", Payload: map[string]any{"what": what, "reason": reason},
	})
}

// useTOTP controleert een code en markeert zijn periode als gebruikt. Een
// code die al eens gebruikt is, of ouder is dan de laatst gebruikte, wordt
// geweigerd.
func (s *Service) useTOTP(ctx context.Context, user store.User, code string) error {
	if user.TotpSecret == nil {
		return ErrInvalidTOTP
	}
	step, ok := matchTOTP(code, *user.TotpSecret, s.now())
	if !ok {
		return ErrInvalidTOTP
	}
	n, err := s.q.UseTOTPStep(ctx, store.UseTOTPStepParams{ID: user.ID, TotpLastStep: &step})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvalidTOTP
	}
	return nil
}

// CleanupSessions verwijdert verlopen sessies.
func (s *Service) CleanupSessions(ctx context.Context) (int64, error) {
	return s.q.DeleteExpiredSessions(ctx)
}

func checkPassword(pw string) error {
	if len([]rune(pw)) < MinPasswordLength {
		return ValidationError{fmt.Sprintf("wachtwoord moet minstens %d tekens hebben", MinPasswordLength)}
	}
	if len(pw) > 1024 {
		return ValidationError{"wachtwoord is te lang"}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
