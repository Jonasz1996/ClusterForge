package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestAuditOrigin(t *testing.T) {
	e, c := adminClient(t)

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "test"}, &cl)
	c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": "prod"}, nil)

	// Elke regel weet vanaf welk adres en uit welke login hij kwam.
	login := find(t, c.audit("category=security").Items, "auth.login")
	upd := find(t, c.audit("cluster="+cl.ID).Items, "cluster.updated")
	if upd.IP == nil || *upd.IP != "127.0.0.1" || upd.Session == nil || len(*upd.Session) != 8 {
		t.Fatalf("herkomst van de wijziging: ip %v sessie %v", upd.IP, upd.Session)
	}
	if login.Session == nil || *login.Session != *upd.Session {
		t.Fatalf("de login en de wijziging horen bij dezelfde sessie: %v en %v", login.Session, *upd.Session)
	}
	if o, _ := upd.Payload["origin"].(map[string]any); o["ip"] != "127.0.0.1" {
		t.Fatalf("payload.origin: %v", upd.Payload["origin"])
	}

	// Een tweede login is een andere sessie.
	e.createUser("tweede", "een-lang-wachtwoord", store.UserRoleAdmin)
	c2 := e.client()
	c2.login("tweede", "een-lang-wachtwoord", "")
	c2.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": "test"}, nil)
	if s := find(t, c.audit("cluster="+cl.ID).Items, "cluster.updated").Session; s == nil || *s == *upd.Session {
		t.Fatalf("sessie van de tweede login: %v", s)
	}

	// Een git-URL met een wachtwoord of token komt niet in het logboek.
	var er apiErr
	for _, u := range []string{"https://jonas:ghp_abc@github.com/x/y.git", "https://ghp_abc@github.com/x/y.git", "ssh://git:geheim@host/y.git"} {
		if s := c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"git_repo_url": u}, &er); s != 400 || !strings.Contains(er.Message, "git-URL") {
			t.Errorf("%s: %d %+v", u, s, er)
		}
	}
	for _, u := range []string{"https://github.com/x/y.git", "ssh://git@github.com/x/y.git", "git@github.com:x/y.git"} {
		if s := c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"git_repo_url": u}, nil); s != 200 {
			t.Errorf("%s: %d", u, s)
		}
	}
}

func TestReauthFailures(t *testing.T) {
	e, c := adminClient(t)
	if s := c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": "fout-fout-fout", "new_password": "nog-een-lang-wachtwoord"}, nil); s == 204 {
		t.Fatal("wachtwoord gewijzigd met een fout huidig wachtwoord")
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	c.do("POST", "/api/v1/auth/totp/setup", nil, &setup)
	c.do("POST", "/api/v1/auth/totp/enable", map[string]string{"code": "000000"}, nil)

	var got []string
	for _, it := range c.audit("category=security").Items {
		got = append(got, it.Summary)
	}
	for _, want := range []string{
		"Bevestiging mislukt bij wachtwoord wijzigen: fout wachtwoord vanaf 127.0.0.1",
		"Instellen van tweestapsverificatie gestart vanaf 127.0.0.1",
		"Bevestiging mislukt bij tweestapsverificatie aanzetten: foute code voor tweestapsverificatie vanaf 127.0.0.1",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("%q ontbreekt in %q", want, got)
		}
	}
	var leaks int
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM events WHERE strpos(payload::text, $1) > 0", setup.Secret).Scan(&leaks)
	if setup.Secret == "" || leaks != 0 {
		t.Fatalf("het TOTP-geheim staat %d keer in het logboek", leaks)
	}
}

func TestThrottledLoginEvents(t *testing.T) {
	e, c := adminClient(t)
	var mu sync.Mutex
	var pending []func()
	e.api.throttle.After = func(_ time.Duration, f func()) {
		mu.Lock()
		defer mu.Unlock()
		pending = append(pending, f)
	}
	// Na de login van admin nog één poging, daarna weigert de limiter.
	e.api.loginLimiter = newIPLimiter(time.Hour, 1)
	for range 6 {
		e.client().login("niemand", "een-lang-wachtwoord", "")
	}
	items := c.audit("category=security").Items
	var limited int
	for _, it := range items {
		if it.Action == "auth.rate_limited" {
			limited++
			if it.Summary != "Loginlimiet bereikt vanaf 127.0.0.1" {
				t.Fatalf("auth.rate_limited: %q", it.Summary)
			}
		}
	}
	if limited != 3 || slices.Contains(actionsOf(items), "audit.throttled") {
		t.Fatalf("%d keer auth.rate_limited, verwacht 3 per adres: %v", limited, actionsOf(items))
	}

	// Aan het eind van het venster één regel met wat wegviel.
	mu.Lock()
	if len(pending) != 1 {
		t.Fatalf("%d samenvattingen gepland", len(pending))
	}
	pending[0]()
	mu.Unlock()
	sum := find(t, c.audit("category=security").Items, "audit.throttled")
	if sum.Summary != "2 keer 'Loginlimiet bereikt' niet apart vastgelegd: te veel binnen 60 seconden" || sum.Actor.Type != "system" {
		t.Fatalf("audit.throttled: %+v", sum)
	}
}

func TestEnrollFailures(t *testing.T) {
	e, c := adminClient(t)
	resp, err := http.Post(e.srv.URL+"/api/v1/agents/enroll", "application/json",
		strings.NewReader(`{"token":"cf_bestaat-niet","machine_id":"`+strings.Repeat("9", 32)+`","hostname":"`+strings.Repeat("h", 100)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("enroll met onbekend token: %d", resp.StatusCode)
	}
	fail := find(t, c.audit("category=agents").Items, "agent.enroll_failed")
	if fail.Summary != "Aanmelding van agent op "+strings.Repeat("h", 64)+" geweigerd: onbekend token vanaf 127.0.0.1" {
		t.Fatalf("agent.enroll_failed: %q", fail.Summary)
	}
}

func TestClusterSlotConflicts(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	_, srv, fp := newPVE(t)
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{"name": "Thuislab", "api_url": srv.URL + "/api2/json",
		"token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp}, &conn); s != 201 {
		t.Fatalf("proxmox: %d", s)
	}
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	var web01, losse node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": cl.ID,
		"proxmox": map[string]any{"connection_id": conn.ID, "vmid": 101}}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "losse"}, &losse)

	// Een uitrol die nog loopt, houdt het slot van het cluster.
	var running string
	if err := e.pool.QueryRow(ctx, `INSERT INTO jobs (kind, title, status, cluster_id, cluster_slot, started_at, heartbeat_at)
		VALUES ('deploy', 'Cluster Web uitrollen', 'running', $1, true, now(), now() + interval '1 hour') RETURNING id`, cl.ID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	busy := "in cluster Web loopt al een taak (Cluster Web uitrollen); wacht tot die klaar is"
	var er apiErr
	if s := c.do("POST", "/api/v1/nodes/"+web01.ID+"/actions", map[string]any{"action": "maintenance"}, &er); s != 409 || er.Message != busy {
		t.Fatalf("node-actie tijdens een uitrol: %d %+v", s, er)
	}
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/vms/101/actions", map[string]any{"action": "start"}, &er); s != 409 || er.Message != busy {
		t.Fatalf("VM-actie tijdens een uitrol: %d %+v", s, er)
	}
	// Een node buiten het cluster merkt er niets van.
	var j job
	if s := c.do("POST", "/api/v1/nodes/"+losse.ID+"/actions", map[string]any{"action": "maintenance"}, &j); s != 202 {
		t.Fatalf("node buiten het cluster: %d", s)
	}
	waitJob(t, c, j.ID)

	// Klaar: het slot is vrij.
	if _, err := e.pool.Exec(ctx, "UPDATE jobs SET status = 'succeeded', finished_at = now() WHERE id = $1", running); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", "/api/v1/nodes/"+web01.ID+"/actions", map[string]any{"action": "maintenance"}, &j); s != 202 {
		t.Fatalf("node-actie na de uitrol: %d", s)
	}
	waitJob(t, c, j.ID)
}
