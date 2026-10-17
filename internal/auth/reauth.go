package auth

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrTooManyAttempts: te veel foute wachtwoorden of codes na elkaar bij een
// gevoelige wijziging; de gebruiker moet even wachten.
var ErrTooManyAttempts = errors.New("te veel foute pogingen; probeer het over een paar minuten opnieuw")

// Na reauthMaxFails foute pogingen zonder een goede ertussen wacht een
// gebruiker reauthLockout. Een TOTP-code heeft maar een miljoen
// mogelijkheden: zonder rem raadt een gestolen sessie hem in minuten.
const (
	reauthMaxFails = 5
	reauthLockout  = 5 * time.Minute
)

// reauthLimiter telt per gebruiker de foute pogingen bij het bevestigen
// van een gevoelige wijziging. Hij leeft in het geheugen: een herstart van
// de server zet de tellers terug, wat een aanvaller niet kan afdwingen.
type reauthLimiter struct {
	mu    sync.Mutex
	fails map[uuid.UUID]reauthState
}

type reauthState struct {
	n     int
	until time.Time
}

func (l *reauthLimiter) allowed(id uuid.UUID, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, ok := l.fails[id]; ok && now.Before(st.until) {
		return ErrTooManyAttempts
	}
	return nil
}

func (l *reauthLimiter) failed(id uuid.UUID, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[uuid.UUID]reauthState{}
	}
	st := l.fails[id]
	if !st.until.IsZero() && !now.Before(st.until) {
		st = reauthState{}
	}
	st.n++
	if st.n >= reauthMaxFails {
		st.until = now.Add(reauthLockout)
	}
	l.fails[id] = st
}

func (l *reauthLimiter) succeeded(id uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, id)
}
