package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReauthLimiter(t *testing.T) {
	var l reauthLimiter
	id := uuid.New()
	now := time.Now()
	for i := range reauthMaxFails {
		if err := l.allowed(id, now); err != nil {
			t.Fatalf("poging %d geweigerd", i+1)
		}
		l.failed(id, now)
	}
	if err := l.allowed(id, now); err != ErrTooManyAttempts {
		t.Fatalf("na %d fouten: %v", reauthMaxFails, err)
	}
	if err := l.allowed(uuid.New(), now); err != nil {
		t.Fatal("een andere gebruiker wordt mee geremd")
	}
	// Na het wachten mag het opnieuw, en telt het van nul.
	later := now.Add(reauthLockout)
	if err := l.allowed(id, later); err != nil {
		t.Fatalf("na het wachten: %v", err)
	}
	l.failed(id, later)
	if err := l.allowed(id, later); err != nil {
		t.Fatalf("eerste fout na het wachten blokkeert al: %v", err)
	}
	// Een goede poging zet de teller terug.
	for range reauthMaxFails - 1 {
		l.failed(id, later)
	}
	l.succeeded(id)
	l.failed(id, later)
	if err := l.allowed(id, later); err != nil {
		t.Fatalf("na een goede poging: %v", err)
	}
}
