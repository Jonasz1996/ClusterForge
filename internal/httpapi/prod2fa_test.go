package httpapi

import (
	"net/http"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// TestProd2FA: standaard (nieuwe installatie) vraagt prod tweestapsverificatie;
// uitgezet (met het wachtwoord) volstaat de clusternaam.
func TestProd2FA(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	c := e.client()
	c.login("jonas", "een-lang-wachtwoord", "")

	var st struct {
		Required bool `json:"required"`
	}
	if s := c.do("GET", "/api/v1/security/prod-2fa", nil, &st); s != http.StatusOK || !st.Required {
		t.Fatalf("standaard: %d %+v", s, st)
	}
	policy := func(confirm string) (int, apiErr) {
		var ae apiErr
		s := c.do("PUT", "/api/v1/updates/policies/environments/prod", map[string]any{
			"enabled": false, "apt": true, "apt_full": true, "apt_autoremove": false, "docker": true, "docker_prune": false,
			"reboot": false, "window": "zo 03:00-05:00", "confirm": confirm,
		}, &ae)
		return s, ae
	}
	if s, ae := policy("prod"); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("prod zonder tweestapsverificatie: %d %+v", s, ae)
	}

	// Uitzetten vraagt het wachtwoord.
	if s := c.do("PUT", "/api/v1/security/prod-2fa", map[string]any{"required": false}, nil); s != http.StatusConflict {
		t.Fatalf("uitzetten zonder wachtwoord: %d", s)
	}
	if s := c.do("PUT", "/api/v1/security/prod-2fa", map[string]any{"required": false, "password": "fout-wachtwoord"}, nil); s == http.StatusOK {
		t.Fatal("uitzetten met een fout wachtwoord lukte")
	}
	if s := c.do("PUT", "/api/v1/security/prod-2fa", map[string]any{"required": false, "password": "een-lang-wachtwoord"}, &st); s != http.StatusOK || st.Required {
		t.Fatalf("uitzetten: %d %+v", s, st)
	}

	// De clusternaam blijft nodig; daarmee lukt het nu.
	if s, ae := policy(""); s != http.StatusConflict || ae.Code != "needs_confirmation" {
		t.Fatalf("prod zonder bevestiging: %d %+v", s, ae)
	}
	if s, ae := policy("prod"); s != http.StatusOK {
		t.Fatalf("prod met bevestiging: %d %+v", s, ae)
	}

	// Weer aanzetten vraagt niets.
	if s := c.do("PUT", "/api/v1/security/prod-2fa", map[string]any{"required": true}, &st); s != http.StatusOK || !st.Required {
		t.Fatalf("aanzetten: %d %+v", s, st)
	}
	if s, _ := policy("prod"); s != http.StatusForbidden {
		t.Fatalf("weer aan: %d", s)
	}
}
