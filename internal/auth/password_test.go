package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestHashAndVerifyPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("onverwacht hashformaat: %s", h)
	}
	ok, err := VerifyPassword("correct horse battery", h)
	if err != nil || !ok {
		t.Fatalf("juist wachtwoord afgewezen: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong horse battery", h)
	if err != nil || ok {
		t.Fatalf("fout wachtwoord geaccepteerd: ok=%v err=%v", ok, err)
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("twee hashes van hetzelfde wachtwoord zijn gelijk; salt ontbreekt")
	}
}

func TestVerifyPasswordRejectsGarbage(t *testing.T) {
	for _, h := range []string{"", "plain", "$bcrypt$x$y$z$w", "$argon2id$v=19$m=1,t=1,p=1$!!$!!"} {
		if _, err := VerifyPassword("x", h); err == nil {
			t.Errorf("geen fout voor hash %q", h)
		}
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
