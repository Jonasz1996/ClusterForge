package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// TempPasswordTTL is hoe lang een tijdelijk wachtwoord uit `user reset`
// standaard geldig is.
const TempPasswordTTL = time.Hour

// MaxTempPasswordTTL is de langste geldigheid die de beheerder kan kiezen.
const MaxTempPasswordTTL = 24 * time.Hour

var (
	ErrUnknownUser       = errors.New("gebruiker bestaat niet")
	ErrNoForcedChange    = errors.New("er staat geen verplichte wachtwoordwijziging open")
	ErrSameAsTemporary   = ValidationError{Msg: "kies een ander wachtwoord dan het tijdelijke"}
	ErrSetupRequired     = errors.New("stel eerst een nieuw wachtwoord en tweestapsverificatie in")
	errTempPasswordStale = errors.New("tijdelijk wachtwoord verlopen of al gebruikt")
)

// ValidUsername zegt of s als gebruikersnaam kan bestaan. Alleen zo'n naam
// komt in een commando dat iemand in een shell plakt.
func ValidUsername(s string) bool { return usernameRe.MatchString(s) }

// ResetResult is wat de beheerder na een reset te zien krijgt.
type ResetResult struct {
	User         store.User
	TempPassword string
	ExpiresAt    time.Time
	Sessions     int64
}

// ResetUser zet de TOTP van een gebruiker uit, geeft hem een tijdelijk
// wachtwoord dat één keer werkt en na ttl verloopt, beëindigt al zijn
// sessies en dwingt bij de volgende login een nieuw wachtwoord en een nieuwe
// TOTP af. Alleen voor de CLI: wie een shell op de server heeft, mag dit.
func (s *Service) ResetUser(ctx context.Context, username string, ttl time.Duration) (ResetResult, error) {
	if ttl <= 0 {
		ttl = TempPasswordTTL
	}
	if ttl > MaxTempPasswordTTL {
		return ResetResult{}, ValidationError{Msg: fmt.Sprintf("geldigheid mag hoogstens %s zijn", MaxTempPasswordTTL)}
	}
	user, err := s.q.GetUserByUsername(ctx, strings.TrimSpace(username))
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetResult{}, ErrUnknownUser
	}
	if err != nil {
		return ResetResult{}, err
	}
	if user.DisabledAt != nil {
		return ResetResult{}, ValidationError{Msg: "dit account is uitgeschakeld; een reset zet het niet terug aan"}
	}
	temp, err := newTempPassword()
	if err != nil {
		return ResetResult{}, err
	}
	hash, err := HashPassword(ctx, temp)
	if err != nil {
		return ResetResult{}, err
	}
	expires := s.now().Add(ttl)
	if err := s.q.ResetUserByCLI(ctx, store.ResetUserByCLIParams{ID: user.ID, PasswordHash: hash, TempPasswordExpiresAt: &expires}); err != nil {
		return ResetResult{}, err
	}
	n, err := s.q.DeleteAllUserSessions(ctx, user.ID)
	if err != nil {
		return ResetResult{}, err
	}
	host, _ := os.Hostname()
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "user", SubjectID: user.ID.String(),
		Action: "user.reset",
		Payload: map[string]any{
			"username": user.Username, "via": "cli", "host": host,
			"totp_was_on": user.TotpEnabledAt != nil, "expires_at": expires.UTC().Format(time.RFC3339),
			"sessies_beeindigd": n,
		},
	}); err != nil {
		return ResetResult{}, fmt.Errorf("reset is gebeurd, maar het logboek schrijven mislukte: %w", err)
	}
	return ResetResult{User: user, TempPassword: temp, ExpiresAt: expires, Sessions: n}, nil
}

// useTempPassword controleert bij het inloggen of het tijdelijke wachtwoord
// nog geldig is en markeert het als gebruikt.
func (s *Service) useTempPassword(ctx context.Context, user store.User) error {
	if user.TempPasswordExpiresAt == nil || !s.now().Before(*user.TempPasswordExpiresAt) || user.TempPasswordUsedAt != nil {
		return errTempPasswordStale
	}
	n, err := s.q.UseTempPassword(ctx, user.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return errTempPasswordStale
	}
	return nil
}

// SetupPending zegt of de gebruiker na een reset nog een wachtwoord of TOTP
// moet instellen voor hij verder mag.
func SetupPending(u store.User) bool { return u.MustChangePassword || u.MustSetupTotp }

// CompleteForcedPassword zet na een reset het nieuwe wachtwoord. Het
// tijdelijke wachtwoord is bij het inloggen al gecontroleerd en opgebruikt,
// dus het wordt niet opnieuw gevraagd.
func (s *Service) CompleteForcedPassword(ctx context.Context, p Principal, next string) error {
	user, err := s.q.GetUserByID(ctx, p.User.ID)
	if err != nil {
		return err
	}
	if !user.MustChangePassword {
		return ErrNoForcedChange
	}
	if err := checkPassword(next); err != nil {
		return err
	}
	same, err := VerifyPassword(ctx, next, user.PasswordHash)
	if err != nil {
		return err
	}
	if same {
		return ErrSameAsTemporary
	}
	hash, err := HashPassword(ctx, next)
	if err != nil {
		return err
	}
	if err := s.q.CompleteForcedPassword(ctx, store.CompleteForcedPasswordParams{ID: user.ID, PasswordHash: hash}); err != nil {
		return err
	}
	if err := s.q.DeleteUserSessionsExcept(ctx, store.DeleteUserSessionsExceptParams{UserID: user.ID, ID: p.SessionID}); err != nil {
		return err
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: events.User(user.ID), SubjectType: "user", SubjectID: user.ID.String(),
		Action: "auth.password_changed", Payload: map[string]any{"na_reset": true},
	})
}

// tempAlphabet laat tekens weg die op elkaar lijken (0/O, 1/l/I).
const tempAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// newTempPassword geeft 20 willekeurige tekens in groepjes van vijf, zo'n
// 115 bits: makkelijk over te typen, onmogelijk te raden.
func newTempPassword() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(tempAlphabet)))
	for i := 0; i < 20; i++ {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(tempAlphabet[n.Int64()])
	}
	return b.String(), nil
}
