package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type userFlags struct {
	User struct {
		TotpEnabled        bool `json:"totp_enabled"`
		MustChangePassword bool `json:"must_change_password"`
		MustSetupTotp      bool `json:"must_setup_totp"`
	} `json:"user"`
}

// enableTOTP zet TOTP aan voor de ingelogde gebruiker en geeft het geheim.
func enableTOTP(t *testing.T, c *client) string {
	t.Helper()
	var setup struct {
		Secret string `json:"secret"`
	}
	if s := c.do("POST", "/api/v1/auth/totp/setup", nil, &setup); s != 200 {
		t.Fatalf("totp setup: %d", s)
	}
	code, _ := totp.GenerateCode(setup.Secret, time.Now())
	if s := c.do("POST", "/api/v1/auth/totp/enable", map[string]any{"code": code}, nil); s != 204 {
		t.Fatalf("totp enable: %d", s)
	}
	return setup.Secret
}

func TestUserResetFlow(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	old := e.client()
	if s, _, _ := old.login("jonas", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatalf("login: %d", s)
	}
	enableTOTP(t, old)

	res, err := e.auth.ResetUser(ctx, "JONAS", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.TempPassword) < 12 || res.Sessions != 1 || time.Until(res.ExpiresAt) > time.Hour+time.Minute {
		t.Fatalf("reset: %+v", res)
	}
	// Alle sessies zijn weg, het oude wachtwoord werkt niet meer.
	if s := old.do("GET", "/api/v1/auth/me", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("oude sessie na reset: %d", s)
	}
	c := e.client()
	if s, _, _ := c.login("jonas", "een-lang-wachtwoord", ""); s != 401 {
		t.Fatalf("oud wachtwoord: %d", s)
	}
	// Het tijdelijke wachtwoord werkt zonder TOTP.
	if s, _, er := c.login("jonas", res.TempPassword, ""); s != 200 {
		t.Fatalf("tijdelijk wachtwoord: %d %s", s, er.Code)
	}
	var m userFlags
	c.do("GET", "/api/v1/auth/me", nil, &m)
	if !m.User.MustChangePassword || !m.User.MustSetupTotp || m.User.TotpEnabled {
		t.Fatalf("me na reset: %+v", m.User)
	}
	// Maar één keer.
	if s, _, _ := e.client().login("jonas", res.TempPassword, ""); s != 401 {
		t.Fatalf("tijdelijk wachtwoord tweede keer: %d", s)
	}
	// De rest van de API is dicht, ook TOTP vóór het wachtwoord.
	var er apiErr
	if s := c.do("GET", "/api/v1/clusters", nil, &er); s != 403 || er.Code != "account_setup_required" {
		t.Fatalf("clusters tijdens instellen: %d %s", s, er.Code)
	}
	if s := c.do("POST", "/api/v1/auth/totp/setup", nil, nil); s != 403 {
		t.Fatalf("totp vóór wachtwoord: %d", s)
	}
	if s := c.do("POST", "/api/v1/auth/reset/password", map[string]any{"new_password": res.TempPassword}, &er); s != 400 {
		t.Fatalf("zelfde wachtwoord: %d", s)
	}
	if s := c.do("POST", "/api/v1/auth/reset/password", map[string]any{"new_password": "nog-een-lang-wachtwoord"}, nil); s != 204 {
		t.Fatalf("nieuw wachtwoord: %d", s)
	}
	if s := c.do("POST", "/api/v1/auth/reset/password", map[string]any{"new_password": "nog-een-ander-wachtwoord"}, nil); s != 403 {
		t.Fatalf("tweede keer zonder reset: %d", s)
	}
	if s := c.do("GET", "/api/v1/clusters", nil, nil); s != 403 {
		t.Fatalf("clusters zonder TOTP: %d", s)
	}
	secret := enableTOTP(t, c)
	if s := c.do("GET", "/api/v1/clusters", nil, nil); s != 200 {
		t.Fatalf("clusters na instellen: %d", s)
	}
	c.do("GET", "/api/v1/auth/me", nil, &m)
	if m.User.MustChangePassword || m.User.MustSetupTotp || !m.User.TotpEnabled {
		t.Fatalf("me na instellen: %+v", m.User)
	}
	// Inloggen met het nieuwe wachtwoord en de nieuwe TOTP.
	code, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if s, _, er := e.client().login("jonas", "nog-een-lang-wachtwoord", code); s != 200 {
		t.Fatalf("login na herstel: %d %s", s, er.Code)
	}

	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = 'user.reset' AND payload->>'username' = 'jonas' AND payload->>'via' = 'cli'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("logboek user.reset: %d %v", n, err)
	}
}

func TestUserResetExpires(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	res, err := e.auth.ResetUser(ctx, "jonas", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET temp_password_expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := e.client().login("jonas", res.TempPassword, ""); s != 401 {
		t.Fatalf("verlopen tijdelijk wachtwoord: %d", s)
	}
	if _, err := e.auth.ResetUser(ctx, "niemand", 0); err == nil {
		t.Fatal("reset van onbekende gebruiker moet falen")
	}
	if _, err := e.auth.ResetUser(ctx, "jonas", 48*time.Hour); err == nil {
		t.Fatal("geldigheid boven 24 uur moet falen")
	}
}

type recoveryHelp struct {
	Code     string `json:"code"`
	Commands []struct {
		Label   string `json:"label"`
		Command string `json:"command"`
	} `json:"commands"`
	TTL int `json:"ttl_minutes"`
}

func TestAccountRecoveryHelp(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	c := e.client()
	var a, b recoveryHelp
	if s := c.do("POST", "/api/v1/auth/recovery", map[string]any{"username": "Jonas"}, &a); s != 200 {
		t.Fatalf("recovery: %d", s)
	}
	if s := c.do("POST", "/api/v1/auth/recovery", map[string]any{"username": "niemand@example.com"}, &b); s != 200 {
		t.Fatalf("recovery onbekend: %d", s)
	}
	// Zelfde vorm, de naam leesbaar in de code, en een geldige code.
	if !strings.HasPrefix(a.Code, "jonas.") || !strings.HasPrefix(b.Code, "niemandexample.com.") ||
		!auth.LooksLikeCode(a.Code) || !auth.LooksLikeCode(b.Code) || len(a.Commands) != len(b.Commands) || a.TTL != 60 ||
		a.Commands[0].Command != "docker compose exec server clusterforge-server user reset "+a.Code {
		t.Fatalf("antwoorden: %+v / %+v", a, b)
	}
	// Een zelf verzonnen code met dezelfde naam werkt niet.
	if _, _, err := e.auth.ResetByCode(ctx, "jonas.ABCD-EFGH-JKLM-NPQR", 0); !errors.Is(err, auth.ErrUnknownCode) {
		t.Fatalf("verzonnen code: %v", err)
	}
	// De echte code werkt één keer, ook in kleine letters geplakt.
	res, name, err := e.auth.ResetByCode(ctx, strings.ToLower(a.Code), 0)
	if err != nil || name != "Jonas" || res.User.Username != "jonas" || res.TempPassword == "" {
		t.Fatalf("reset met code: %v %q %+v", err, name, res)
	}
	if s, _, er := e.client().login("jonas", res.TempPassword, ""); s != 200 {
		t.Fatalf("login met tijdelijk wachtwoord: %d %s", s, er.Code)
	}
	if _, _, err := e.auth.ResetByCode(ctx, a.Code, 0); !errors.Is(err, auth.ErrUnknownCode) {
		t.Fatalf("code tweede keer: %v", err)
	}
	// Onbekend account: pas de CLI zegt het, en de code is op.
	if _, _, err := e.auth.ResetByCode(ctx, b.Code, 0); !errors.Is(err, auth.ErrUnknownUser) {
		t.Fatalf("code voor onbekend account: %v", err)
	}
	// Verlopen code.
	var d recoveryHelp
	c.do("POST", "/api/v1/auth/recovery", map[string]any{"username": "jonas"}, &d)
	if _, err := e.pool.Exec(ctx, `UPDATE password_reset_requests SET expires_at = now() - interval '1 second' WHERE used_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.auth.ResetByCode(ctx, d.Code, 0); !errors.Is(err, auth.ErrUnknownCode) {
		t.Fatalf("verlopen code: %v", err)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = 'user.reset' AND payload->>'via' = 'code'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("logboek user.reset via code: %d %v", n, err)
	}

	var er apiErr
	if s := c.do("POST", "/api/v1/auth/recovery", map[string]any{"username": "  "}, &er); s != 400 {
		t.Fatalf("lege login: %d", s)
	}
	e.api.recoveryLimiter = newIPLimiter(time.Hour, 2)
	c.do("POST", "/api/v1/auth/recovery", map[string]any{"username": "jonas"}, nil)
	c.do("POST", "/api/v1/auth/recovery", map[string]any{"username": "jonas"}, nil)
	if s := c.do("POST", "/api/v1/auth/recovery", map[string]any{"username": "jonas"}, nil); s != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %d", s)
	}
	e.api.cfg.ResetCommand = "ssh cf1 sudo cf-reset {code}"
	e.api.recoveryLimiter = newIPLimiter(time.Hour, 2)
	var f recoveryHelp
	c.do("POST", "/api/v1/auth/recovery", map[string]any{"username": "jonas"}, &f)
	if len(f.Commands) != 1 || f.Commands[0].Command != "ssh cf1 sudo cf-reset "+f.Code {
		t.Fatalf("eigen commando: %+v", f)
	}
}
