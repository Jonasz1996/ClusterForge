package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestMySessions(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	e.createUser("ander", "een-lang-wachtwoord", store.UserRoleViewer)
	a, b, c, x := e.client(), e.client(), e.client(), e.client()
	a.login("jonas", "een-lang-wachtwoord", "")
	b.login("jonas", "een-lang-wachtwoord", "")
	c.login("jonas", "een-lang-wachtwoord", "")
	x.login("ander", "een-lang-wachtwoord", "")

	type list struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"items"`
	}
	var l list
	if s := a.do("GET", "/api/v1/auth/sessions", nil, &l); s != 200 || len(l.Items) != 3 {
		t.Fatalf("sessies: %d %+v", s, l)
	}
	var mine, other string
	for _, it := range l.Items {
		if it.Current {
			mine = it.ID
		} else {
			other = it.ID
		}
	}
	if mine == "" || other == "" {
		t.Fatalf("huidige sessie niet gemarkeerd: %+v", l)
	}
	var er apiErr
	if s := a.do("DELETE", "/api/v1/auth/sessions/"+mine, nil, &er); s != 400 {
		t.Fatalf("eigen sessie beëindigen moet weigeren: %d", s)
	}
	// Een andere gebruiker kan mijn sessie niet beëindigen.
	if s := x.do("DELETE", "/api/v1/auth/sessions/"+other, nil, nil); s != 404 {
		t.Fatalf("sessie van een ander: %d", s)
	}
	if s := a.do("DELETE", "/api/v1/auth/sessions/"+other, nil, nil); s != 204 {
		t.Fatalf("andere sessie beëindigen: %d", s)
	}
	if s := a.do("DELETE", "/api/v1/auth/sessions/"+other, nil, nil); s != 404 {
		t.Fatalf("tweede keer: %d", s)
	}
	var ended struct {
		Ended int `json:"ended"`
	}
	if s := a.do("DELETE", "/api/v1/auth/sessions/others", nil, &ended); s != 200 || ended.Ended != 1 {
		t.Fatalf("andere sessies beëindigen: %d %+v", s, ended)
	}
	if s := a.do("GET", "/api/v1/auth/me", nil, nil); s != 200 {
		t.Fatalf("eigen sessie weg: %d", s)
	}
	if s1, s2 := b.do("GET", "/api/v1/auth/me", nil, nil), c.do("GET", "/api/v1/auth/me", nil, nil); s1 != 401 || s2 != 401 {
		t.Fatalf("andere sessies nog geldig: %d %d", s1, s2)
	}
	if s := x.do("GET", "/api/v1/auth/me", nil, nil); s != 200 {
		t.Fatalf("sessie van een andere gebruiker mag niet weg: %d", s)
	}
}

func TestSessionMaxAge(t *testing.T) {
	e := newTestEnv(t)
	e.auth.SetMaxAge(30 * 24 * time.Hour)
	t.Cleanup(func() { e.auth.SetMaxAge(0) })
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	a := e.client()
	a.login("jonas", "een-lang-wachtwoord", "")
	if s := a.do("GET", "/api/v1/auth/me", nil, nil); s != 200 {
		t.Fatalf("verse sessie: %d", s)
	}
	// Steeds gebruikt, maar 31 dagen geleden ingelogd: verlopen.
	if _, err := e.pool.Exec(context.Background(), `UPDATE sessions SET created_at = now() - interval '31 days', last_seen_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	if s := a.do("GET", "/api/v1/auth/me", nil, nil); s != 401 {
		t.Fatalf("sessie ouder dan de maximale duur: %d", s)
	}
	// Bijna 30 dagen oud: verlengen gaat niet voorbij de grens.
	b := e.client()
	b.login("jonas", "een-lang-wachtwoord", "")
	if _, err := e.pool.Exec(context.Background(), `UPDATE sessions SET created_at = now() - interval '30 days' + interval '10 minutes', last_seen_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	var m struct {
		SessionExpiresAt time.Time `json:"session_expires_at"`
	}
	if s := b.do("GET", "/api/v1/auth/me", nil, &m); s != 200 {
		t.Fatalf("sessie net binnen de grens: %d", s)
	}
	if left := time.Until(m.SessionExpiresAt); left > 11*time.Minute || left <= 0 {
		t.Fatalf("vervaldatum niet afgetopt: nog %s", left)
	}
}
