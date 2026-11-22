package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// lockTable houdt een tabel vast in een andere transactie, zoals een
// vastgelopen sessie in PostgreSQL dat zou doen, tot de test afloopt.
func lockTable(t *testing.T, e *testEnv, table string) {
	t.Helper()
	tx, err := e.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
}

func TestLoginTimesOutInsteadOfHanging(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	logs := &lockedBuffer{}
	e.api.log = slog.New(slog.NewTextHandler(logs, nil))
	e.api.loginTimeout = 500 * time.Millisecond
	lockTable(t, e, "users")

	start := time.Now()
	status, _, er := e.client().login("jonas", "een-lang-wachtwoord", "")
	if status != 503 || er.Code != "timeout" {
		t.Fatalf("login bij een vastzittende database: %d %q, verwacht 503 timeout", status, er.Code)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("login duurde %s", d)
	}
	if !strings.Contains(logs.String(), "geen antwoord op tijd") || !strings.Contains(logs.String(), "/api/v1/auth/login") {
		t.Fatalf("geen logregel voor de time-out:\n%s", logs.String())
	}
}

func TestSessionCheckTimesOutInsteadOfHanging(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	c := e.client()
	if s, _, _ := c.login("jonas", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatalf("login: %d", s)
	}
	logs := &lockedBuffer{}
	e.api.log = slog.New(slog.NewTextHandler(logs, nil))
	e.api.sessionTimeout = 500 * time.Millisecond
	lockTable(t, e, "sessions")

	var er apiErr
	if s := c.do("GET", "/api/v1/auth/me", nil, &er); s != 503 || er.Code != "timeout" {
		t.Fatalf("sessiecontrole bij een vastzittende database: %d %q, verwacht 503 timeout", s, er.Code)
	}
	if !strings.Contains(logs.String(), "sessie controleren") {
		t.Fatalf("geen logregel voor de time-out:\n%s", logs.String())
	}
}
