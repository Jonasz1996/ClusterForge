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
	return s.resetUser(ctx, username, ttl, map[string]any{"via": "cli"})
}

func (s *Service) resetUser(ctx context.Context, username string, ttl time.Duration, extra map[string]any) (ResetResult, error) {
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
		Payload: withExtra(map[string]any{
			"username": user.Username, "host": host,
			"totp_was_on": user.TotpEnabledAt != nil, "expires_at": expires.UTC().Format(time.RFC3339),
			"sessies_beeindigd": n,
		}, extra),
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

func withExtra(m, extra map[string]any) map[string]any {
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// ResetRequestTTL is hoe lang een herstelcode van de loginpagina geldig is.
const ResetRequestTTL = 24 * time.Hour

// ErrUnknownCode: de code bestaat niet, is verlopen of al gebruikt.
var ErrUnknownCode = errors.New("onbekende, verlopen of al gebruikte herstelcode")

// codeAlphabet: hoofdletters en cijfers zonder 0/O en 1/I.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// codeLabel is het leesbare deel van de code: wat de bezoeker invulde, in
// kleine letters en alleen tekens die veilig in een shell staan.
func codeLabel(input string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(input) {
		if b.Len() >= 32 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "account"
	}
	return b.String()
}

// normalizeCode maakt een geplakte code gelijk aan de uitgegeven vorm: het
// willekeurige deel na de laatste punt in hoofdletters, spaties weg.
func normalizeCode(code string) string {
	code = strings.Join(strings.Fields(code), "")
	i := strings.LastIndexByte(code, '.')
	if i < 0 {
		return strings.ToUpper(code)
	}
	return strings.ToLower(code[:i]) + "." + strings.ToUpper(code[i+1:])
}

// LooksLikeCode zegt of de CLI een herstelcode kreeg in plaats van een naam.
func LooksLikeCode(s string) bool {
	s = normalizeCode(s)
	i := strings.LastIndexByte(s, '.')
	if i < 0 || len(s)-i-1 != 19 {
		return false
	}
	for j, r := range s[i+1:] {
		if (j+1)%5 == 0 {
			if r != '-' {
				return false
			}
		} else if !strings.ContainsRune(codeAlphabet, r) {
			return false
		}
	}
	return true
}

// RequestReset geeft een herstelcode voor "Wachtwoord vergeten of kan niet
// inloggen?". Er wordt niet gekeken of het account bestaat: elk verzoek
// krijgt een code, zodat het antwoord niets verraadt. Pas de CLI zoekt de
// gebruiker op. De code draagt de ingevulde naam leesbaar in zich, maar
// telt alleen als de server hem uitgaf: in de database staat de hash.
func (s *Service) RequestReset(ctx context.Context, input, ip string) (code string, expires time.Time, err error) {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 128 {
		return "", time.Time{}, ValidationError{Msg: "vul je gebruikersnaam of e-mailadres in"}
	}
	var b strings.Builder
	b.WriteString(codeLabel(input))
	b.WriteByte('.')
	max := big.NewInt(int64(len(codeAlphabet)))
	for i := 0; i < 16; i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", time.Time{}, err
		}
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	code = b.String()
	expires = s.now().Add(ResetRequestTTL)
	if err := s.q.CreateResetRequest(ctx, store.CreateResetRequestParams{
		CodeHash: hashToken(code), Username: input, Ip: ip, ExpiresAt: expires,
	}); err != nil {
		return "", time.Time{}, err
	}
	return code, expires, nil
}

// ResetByCode is `user reset <code>`: het verzoek achter de code geeft de
// gebruikersnaam. De code werkt één keer, ook als het account niet bestaat.
func (s *Service) ResetByCode(ctx context.Context, code string, ttl time.Duration) (ResetResult, string, error) {
	if ttl > MaxTempPasswordTTL {
		return ResetResult{}, "", ValidationError{Msg: fmt.Sprintf("geldigheid mag hoogstens %s zijn", MaxTempPasswordTTL)}
	}
	req, err := s.q.ClaimResetRequest(ctx, hashToken(normalizeCode(code)))
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetResult{}, "", ErrUnknownCode
	}
	if err != nil {
		return ResetResult{}, "", err
	}
	res, err := s.resetUser(ctx, req.Username, ttl, map[string]any{
		"via": "code", "verzoek": req.ID.String(), "verzoek_ip": req.Ip,
	})
	return res, req.Username, err
}
