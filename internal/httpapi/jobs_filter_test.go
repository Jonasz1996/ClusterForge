package httpapi

import (
	"context"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// De takenlijst filtert (fase 47): wat jij startte, wat een gebruiker
// startte (geen automatische taken), en op status.
func TestListJobsFilter(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	e.createUser("piet", "een-lang-wachtwoord", store.UserRoleAdmin)
	c := e.client()
	c.login("jonas", "een-lang-wachtwoord", "")
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (kind, title, status, requested_by) VALUES
		('x', 'mijn gelukt', 'succeeded', (SELECT id FROM users WHERE username = 'jonas')),
		('x', 'mijn mislukt', 'failed', (SELECT id FROM users WHERE username = 'jonas')),
		('x', 'van piet', 'failed', (SELECT id FROM users WHERE username = 'piet')),
		('firewall.apply', 'Firewall toepassen', 'succeeded', NULL),
		('firewall.apply', 'Firewall toepassen', 'failed', NULL)`); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var out struct {
			Items []struct{ Title string } `json:"items"`
		}
		if s := c.do("GET", "/api/v1/jobs"+q, nil, &out); s != 200 {
			t.Fatalf("%s: %d", q, s)
		}
		return len(out.Items)
	}
	for q, want := range map[string]int{
		"": 5, "?who=me": 2, "?who=people": 3, "?status=failed": 3, "?who=me&status=failed": 1, "?who=people&status=failed": 2,
	} {
		if got := count(q); got != want {
			t.Errorf("%q: %d taken, wil %d", q, got, want)
		}
	}
}
