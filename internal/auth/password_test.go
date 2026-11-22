package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestHashAndVerifyPassword(t *testing.T) {
	h, err := HashPassword(t.Context(), "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("onverwacht hashformaat: %s", h)
	}
	ok, err := VerifyPassword(t.Context(), "correct horse battery", h)
	if err != nil || !ok {
		t.Fatalf("juist wachtwoord afgewezen: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(t.Context(), "wrong horse battery", h)
	if err != nil || ok {
		t.Fatalf("fout wachtwoord geaccepteerd: ok=%v err=%v", ok, err)
	}
	h2, _ := HashPassword(t.Context(), "correct horse battery")
	if h == h2 {
		t.Fatal("twee hashes van hetzelfde wachtwoord zijn gelijk; salt ontbreekt")
	}
}

func TestVerifyPasswordRejectsGarbage(t *testing.T) {
	for _, h := range []string{"", "plain", "$bcrypt$x$y$z$w", "$argon2id$v=19$m=1,t=1,p=1$!!$!!"} {
		if _, err := VerifyPassword(t.Context(), "x", h); err == nil {
			t.Errorf("geen fout voor hash %q", h)
		}
	}
}

func TestArgonSlotsRespectContext(t *testing.T) {
	// Alle plaatsen bezet, zoals bij vier hashes die blijven hangen of een
	// vloed aan inlogpogingen: de volgende mag niet eindeloos wachten.
	for range cap(argonSlots) {
		argonSlots <- struct{}{}
	}
	defer func() {
		for range cap(argonSlots) {
			<-argonSlots
		}
	}()
	h := "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := VerifyPassword(ctx, "x", h)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("wachtte %s ondanks afgelopen context", d)
	}
	if _, err := HashPassword(ctx, "correct horse battery"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HashPassword err = %v, want DeadlineExceeded", err)
	}
}

func TestValidTOTP(t *testing.T) {
	key, err := newTOTPKey("jonas")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := totp.GenerateCode(key.Secret(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !validTOTP(code, key.Secret(), now) {
		t.Fatal("geldige code afgewezen")
	}
	if !validTOTP(code, key.Secret(), now.Add(30*time.Second)) {
		t.Fatal("code van vorige periode afgewezen; skew van 1 verwacht")
	}
	if validTOTP(code, key.Secret(), now.Add(5*time.Minute)) {
		t.Fatal("oude code geaccepteerd")
	}
	if validTOTP("000000x", key.Secret(), now) {
		t.Fatal("ongeldige invoer geaccepteerd")
	}
}

func TestCheckPassword(t *testing.T) {
	if err := checkPassword("kort"); err == nil {
		t.Fatal("te kort wachtwoord geaccepteerd")
	}
	if err := checkPassword("lang-genoeg-wachtwoord"); err != nil {
		t.Fatal(err)
	}
}
