package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type auditRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

type auditEntry struct {
	ID       int64  `json:"id"`
	Action   string `json:"action"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Actor    struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"actor"`
	OnBehalfOf *auditRef `json:"on_behalf_of"`
	Subject    struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
	} `json:"subject"`
	Cluster *auditRef `json:"cluster"`
	Node    *auditRef `json:"node"`
	Job     *auditRef `json:"job"`
	IP      *string   `json:"ip"`
	Changes []struct {
		Field string `json:"field"`
		Label string `json:"label"`
		From  string `json:"from"`
		To    string `json:"to"`
	} `json:"changes"`
	Payload map[string]any `json:"payload"`
}

type auditPage struct {
	Items      []auditEntry `json:"items"`
	NextBefore *int64       `json:"next_before"`
}

func (c *client) audit(query string) auditPage {
	c.t.Helper()
	var p auditPage
	if s := c.do("GET", "/api/v1/audit?"+query, nil, &p); s != http.StatusOK {
		c.t.Fatalf("audit?%s: %d", query, s)
	}
	return p
}

func actionsOf(items []auditEntry) []string {
	out := make([]string, len(items))
	for i, e := range items {
		out[i] = e.Action
	}
	return out
}

func find(t *testing.T, items []auditEntry, action string) auditEntry {
	t.Helper()
	for _, e := range items {
		if e.Action == action {
			return e
		}
	}
	t.Fatalf("%s niet gevonden in %v", action, actionsOf(items))
	return auditEntry{}
}

// stream opent /stream en geeft de data van elk change-event door.
func (c *client) stream(t *testing.T) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api/v1/stream", nil)
	resp, err := c.http.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %v %v", err, resp)
	}
	out := make(chan string, 100)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				out <- d
			}
		}
	}()
	return out
}

func TestAudit(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	ctx := context.Background()
	live := c.stream(t)

	var admin me
	c.do("GET", "/api/v1/auth/me", nil, &admin)
	var users struct {
		Items []struct{ ID, Username string } `json:"items"`
	}
	c.do("GET", "/api/v1/users", nil, &users)
	adminID := users.Items[slices.IndexFunc(users.Items, func(u struct{ ID, Username string }) bool { return u.Username == "admin" })].ID

	// Een cluster met een node en een VIP, gewijzigd en daarna verwijderd.
	var cl struct{ ID string }
	if s := c.do("POST", "/api/v1/clusters", map[string]any{
		"slug": "webcluster-prod", "name": "webcluster-prod", "type": "nginx", "environment": "test",
	}, &cl); s != 201 {
		t.Fatalf("cluster: %d", s)
	}
	c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": "prod"}, nil)
	var node struct{ ID string }
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web03", "cluster_id": cl.ID}, &node); s != 201 {
		t.Fatalf("node: %d", s)
	}
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.10.100"}, nil)
	c.do("DELETE", "/api/v1/nodes/"+node.ID, nil, nil)
	if s := c.do("DELETE", "/api/v1/clusters/"+cl.ID, nil, nil); s != 204 {
		t.Fatalf("cluster verwijderen: %d", s)
	}

	// De live-notificatie draagt alleen het id.
	select {
	case d := <-live:
		var m map[string]any
		if err := json.Unmarshal([]byte(d), &m); err != nil || len(m) != 1 || m["id"] == nil {
			t.Fatalf("notificatie: %s", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("geen live-notificatie")
	}

	// Filter op een verwijderd cluster.
	p := c.audit("cluster=" + cl.ID)
	want := []string{"cluster.deleted", "node.deleted", "vip.created", "node.created", "cluster.updated", "cluster.created"}
	if got := actionsOf(p.Items); !slices.Equal(got, want) {
		t.Fatalf("cluster: %v", got)
	}
	upd := find(t, p.Items, "cluster.updated")
	if upd.Summary != "Cluster webcluster-prod gewijzigd: omgeving van Test naar Productie" || upd.Actor.Name != "admin" ||
		len(upd.Changes) != 1 || upd.Changes[0].Label != "Omgeving" || upd.Changes[0].From != "Test" || upd.Changes[0].To != "Productie" {
		t.Fatalf("cluster.updated: %+v", upd)
	}
	if upd.Cluster == nil || upd.Cluster.Name != "webcluster-prod" || !upd.Cluster.Deleted || upd.Category != "inventory" {
		t.Fatalf("cluster bij een verwijderd cluster: %+v", upd.Cluster)
	}
	if s := find(t, p.Items, "node.created").Summary; s != "Node web03 toegevoegd" {
		t.Fatalf("node.created: %q", s)
	}
	if del := find(t, p.Items, "cluster.deleted"); del.Summary != "Cluster webcluster-prod verwijderd" || !del.Subject.Deleted {
		t.Fatalf("cluster.deleted: %+v", del)
	}

	// Filter op node, soort, zoektekst en actor.
	if got := actionsOf(c.audit("node=" + node.ID).Items); !slices.Equal(got, []string{"node.deleted", "node.created"}) {
		t.Fatalf("node: %v", got)
	}
	for _, it := range c.audit("category=security").Items {
		if it.Category != "security" {
			t.Fatalf("soort security geeft %s", it.Action)
		}
	}
	if got := actionsOf(c.audit("q=10.0.10.100").Items); !slices.Equal(got, []string{"vip.created"}) {
		t.Fatalf("zoeken: %v", got)
	}
	if got := c.audit("q=" + url.QueryEscape("100%_")).Items; len(got) != 0 {
		t.Fatalf("%%-teken en liggend streepje moeten letterlijk gezocht worden: %v", actionsOf(got))
	}
	login := find(t, c.audit("user="+adminID).Items, "auth.login")
	if login.IP == nil || login.Summary != "Ingelogd vanaf "+*login.IP {
		t.Fatalf("auth.login: %+v", login)
	}

	// Een taak die admin aanvroeg: de afronding is van het systeem, namens admin.
	_, srv, fp := newPVE(t)
	var conn struct{ ID string }
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{"name": "Thuislab", "api_url": srv.URL + "/api2/json",
		"token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp}, &conn); s != 201 {
		t.Fatalf("proxmox: %d", s)
	}
	var web01 struct{ ID string }
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "proxmox": map[string]any{"connection_id": conn.ID, "vmid": 101}}, &web01)
	var j job
	c.do("POST", "/api/v1/proxmox/"+conn.ID+"/vms/101/actions", map[string]any{"action": "start"}, &j)
	waitJob(t, c, j.ID)
	byNode := c.audit("node=" + web01.ID).Items
	done := find(t, byNode, "job.succeeded")
	if done.Actor.Type != "system" || done.OnBehalfOf == nil || done.OnBehalfOf.Name != "admin" || done.Job == nil || done.Job.ID != j.ID ||
		!strings.HasPrefix(done.Summary, "Taak gelukt: ") {
		t.Fatalf("job.succeeded: %+v", done)
	}
	find(t, byNode, "job.queued")
	if got := find(t, c.audit("user="+adminID).Items, "job.succeeded"); got.ID != done.ID {
		t.Fatalf("filter op gebruiker vindt de afronding niet: %+v", got)
	}
	if got := actionsOf(c.audit("job=" + j.ID).Items); !slices.Contains(got, "job.queued") || !slices.Contains(got, "job.succeeded") {
		t.Fatalf("filter op taak: %v", got)
	}

	// Paginering over meer dan 120 regels, zonder dubbele of ontbrekende.
	if _, err := e.pool.Exec(ctx, `INSERT INTO events (actor_type, subject_type, action, payload)
		SELECT 'system', 'test', 'test.ping', jsonb_build_object('n', g) FROM generate_series(1, 130) g`); err != nil {
		t.Fatal(err)
	}
	var total int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&total)
	var ids []int64
	before := ""
	for page := 0; ; page++ {
		pg := c.audit("limit=50" + before)
		for _, it := range pg.Items {
			ids = append(ids, it.ID)
		}
		if pg.NextBefore == nil {
			break
		}
		before = "&before=" + strconv.FormatInt(*pg.NextBefore, 10)
		if page > 10 {
			t.Fatal("paginering stopt niet")
		}
	}
	if len(ids) < total || !slices.IsSortedFunc(ids, func(a, b int64) int { return int(b - a) }) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Fatalf("%d regels via paginering, %d in de tabel", len(ids), total)
	}
	if ping := c.audit("q=test.ping&limit=1").Items[0]; ping.Summary != "test.ping" || ping.Category != "status" {
		t.Fatalf("onbekende action: %+v", ping)
	}

	// Export met dezelfde filters, en de export zelf in het logboek.
	resp, err := c.http.Get(c.base + "/api/v1/audit/export?cluster=" + cl.ID)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %v %+v", err, resp)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	var first auditEntry
	if len(lines) != 6 || json.Unmarshal([]byte(lines[0]), &first) != nil || first.Action != "cluster.deleted" {
		t.Fatalf("export: %d regels, eerste %s", len(lines), lines[0])
	}
	exp := c.audit("limit=1").Items[0]
	if exp.Action != "audit.exported" || exp.Summary != "Logboek geëxporteerd (6 regels)" || exp.Actor.Name != "admin" {
		t.Fatalf("audit.exported: %+v", exp)
	}

	// Info voor de filters.
	var info struct {
		Categories      []struct{ Key, Label string } `json:"categories"`
		Users           []struct{ Username string }   `json:"users"`
		DeletedClusters []auditRef                    `json:"deleted_clusters"`
		DeletedNodes    []auditRef                    `json:"deleted_nodes"`
		Total           int                           `json:"total"`
	}
	if s := c.do("GET", "/api/v1/audit/info", nil, &info); s != 200 || len(info.Categories) != len(events.Categories) || len(info.Users) != 2 ||
		len(info.DeletedClusters) != 1 || info.DeletedClusters[0].Name != "webcluster-prod" || len(info.DeletedNodes) != 1 || info.Total < total {
		t.Fatalf("info: %d %+v", s, info)
	}

	// Foute invoer en rechten.
	for _, q := range []string{"category=niets", "limit=500", "from=2026-10-05T10:00:00Z&to=2026-10-05T09:00:00Z", "q=" + strings.Repeat("x", 201)} {
		if s := c.do("GET", "/api/v1/audit?"+q, nil, nil); s != 400 {
			t.Errorf("audit?%s: %d", q, s)
		}
	}
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	for _, path := range []string{"/api/v1/audit", "/api/v1/audit/info", "/api/v1/audit/export"} {
		if s := v.do("GET", path, nil, nil); s != 403 {
			t.Errorf("%s als viewer: %d", path, s)
		}
	}

	// Onveranderbaar, ook voor TRUNCATE.
	if _, err := e.pool.Exec(ctx, "TRUNCATE events"); err == nil {
		t.Fatal("TRUNCATE op events moet geweigerd worden")
	}
}

func TestAuditLoginFailures(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("oud", "een-lang-wachtwoord", store.UserRoleViewer)
	ctx := context.Background()
	live := c.stream(t)

	// Wie zijn wachtwoord in het naamveld typt, vindt het nergens terug.
	if s, _, _ := e.client().login("MijnGeheim123!", "een-lang-wachtwoord", ""); s != 401 {
		t.Fatalf("login: %d", s)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE users SET disabled_at = now() WHERE username = 'oud'"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := e.client().login("oud", "een-lang-wachtwoord", ""); s != 401 {
		t.Fatalf("login uitgeschakeld: %d", s)
	}
	e.client().login("oud", "fout-fout-fout", "")

	var n int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE subject_id LIKE '%MijnGeheim%' OR payload::text LIKE '%MijnGeheim%'").Scan(&n)
	if n != 0 {
		t.Fatal("de getypte naam staat in events")
	}
	timeout := time.After(5 * time.Second)
	for got := 0; got < 3; {
		select {
		case d := <-live:
			if strings.Contains(d, "MijnGeheim") {
				t.Fatalf("de getypte naam staat in de stream: %s", d)
			}
			got++
		case <-timeout:
			t.Fatalf("maar %d notificaties", got)
		}
	}

	items := c.audit("category=security&limit=3").Items
	if got := actionsOf(items); !slices.Equal(got, []string{"auth.login_failed", "auth.login_failed", "auth.login_failed"}) {
		t.Fatalf("security: %v", got)
	}
	if s := items[0].Summary; !strings.HasPrefix(s, "Mislukte inlogpoging voor oud (fout wachtwoord) vanaf ") {
		t.Fatalf("fout wachtwoord: %q", s)
	}
	if s := items[1].Summary; !strings.HasPrefix(s, "Mislukte inlogpoging voor oud (account is uitgeschakeld) vanaf ") {
		t.Fatalf("uitgeschakeld: %q", s)
	}
	if s := items[2].Summary; !strings.HasPrefix(s, "Mislukte inlogpoging met een onbekende gebruikersnaam vanaf ") || items[2].Subject.ID != "" {
		t.Fatalf("onbekend: %+v", items[2])
	}

	// Een oude regel met een getypte naam toont die naam niet en vindt hem niet.
	if _, err := e.pool.Exec(ctx, `INSERT INTO events (actor_type, subject_type, subject_id, action, payload)
		VALUES ('system', 'user', 'Oud-Geheim-99', 'auth.login_failed', '{"reason": "unknown_user"}')`); err != nil {
		t.Fatal(err)
	}
	old := c.audit("limit=1").Items[0]
	if old.Subject.ID != "" || strings.Contains(old.Summary, "Oud-Geheim") {
		t.Fatalf("oude regel: %+v", old)
	}
	if got := c.audit("q=Oud-Geheim").Items; len(got) != 0 {
		t.Fatalf("zoeken vindt de getypte naam: %v", actionsOf(got))
	}
}
