package crowdsec

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestParseListAndGuard(t *testing.T) {
	firehol := "# FireHOL level1\n0.0.0.0/8\n10.0.0.0/8\n1.10.16.0/20\n224.0.0.0/3\n203.0.113.7\n203.0.113.7/32 ; dubbel\nkapot\n2001:db8::/32\n"
	vals, bad, err := ParseList(strings.NewReader(firehol), "lines")
	if err != nil || bad != 1 {
		t.Fatalf("parse %v %d %v", vals, bad, err)
	}
	if want := []string{"0.0.0.0/8", "10.0.0.0/8", "1.10.16.0/20", "224.0.0.0/3", "203.0.113.7", "2001:db8::/32"}; !slices.Equal(vals, want) {
		t.Errorf("waarden %v", vals)
	}
	spam := `{"cidr":"1.10.16.0/20","sblid":"SBL256894","rir":"apnic"}` + "\n" + `{"type":"metadata","timestamp":1700000000,"size":1}` + "\n"
	if vals, _, err := ParseList(strings.NewReader(spam), "ndjson"); err != nil || !slices.Equal(vals, []string{"1.10.16.0/20"}) {
		t.Errorf("spamhaus %v %v", vals, err)
	}

	g := NewGuard(DefaultWhitelist, []string{"81.82.83.84/24"})
	for v, ok := range map[string]bool{
		"1.10.16.0/20": true, "203.0.113.7": true, "2001:db8::/32": true,
		"10.0.0.0/8": false, "192.168.113.153": false, "224.0.0.0/3": false, "224.0.0.18": false, "0.0.0.0/8": false,
		"81.82.83.84": false, "81.82.0.0/16": false, "1.0.0.0/4": false, "fe80::1": false, "::ffff:192.168.1.1": false,
	} {
		_, why := g.Check(v)
		if (why == "") != ok {
			t.Errorf("%s: toegelaten %v, reden %q", v, ok, why)
		}
	}
}

func seedLists(t *testing.T, pool *pgxpool.Pool, host string, proto int) uuid.UUID {
	t.Helper()
	id := seed(t, pool, host, nil, proto, "ssh")
	if _, err := pool.Exec(context.Background(), `INSERT INTO crowdsec_nodes (node_id, managed, bouncer, status, status_at)
		VALUES ($1, true, true, '{"installed":true,"active":"active","bouncer":"active"}', now())`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// pollLists doet wat de poller doet met de lijsten van de agent.
func pollLists(t *testing.T, pool *pgxpool.Pool, bus *fakeBus, ids ...uuid.UUID) {
	t.Helper()
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for _, id := range ids {
		b, _ := json.Marshal(bus.listCounts(id))
		if _, err := pool.Exec(context.Background(), `UPDATE crowdsec_nodes SET lists = $2 WHERE node_id = $1`, id, b); err != nil {
			t.Fatal(err)
		}
	}
}

// onNode geeft de waarden van een lijst op een node, en het aantal tags.
func onNode(bus *fakeBus, id uuid.UUID, slug string) ([]string, int) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	var out []string
	n := 0
	for tag, v := range bus.lists[id] {
		if tagSlug(tag) == slug {
			n++
			out = append(out, v...)
		}
	}
	sort.Strings(out)
	return out, n
}

func TestListsFlow(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)
	bus := newFakeBus()
	s := NewService(pool, ev, nil, bus, log)
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.Box = box
	a := seedLists(t, pool, "web1", protocol.CrowdSecListsSince)
	b := seedLists(t, pool, "web2", protocol.CrowdSecListsSince)
	old := seedLists(t, pool, "oud", protocol.CrowdSecListsSince-1)
	actor := events.System()

	own, err := s.listBySlug(ctx, "eigen")
	if err != nil {
		t.Fatal(err)
	}
	add := []EntryInput{{Value: "203.0.113.7", Reason: "scanner"}, {Value: "198.51.100.0/24", ExpiresIn: time.Hour}, {Value: "192.168.1.5"}}

	// Voorbeeld: niets bewaard.
	d, err := s.ChangeEntries(ctx, actor, nil, own.ID, add, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Applied || d.AddedN != 2 || d.RefusedN != 1 || d.Nodes != 3 || !strings.Contains(d.Refused[0].Why, "whitelist") {
		t.Errorf("voorbeeld %+v", d)
	}
	if es, _, _ := s.Entries(ctx, own.ID, "", 0); len(es) != 0 {
		t.Fatalf("voorbeeld bewaarde %v", es)
	}

	d, err = s.ChangeEntries(ctx, actor, nil, own.ID, add, nil, false)
	if err != nil || !d.Applied || d.VersionID == 0 {
		t.Fatalf("toevoegen %+v %v", d, err)
	}
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{a, b} {
		if v, n := onNode(bus, id, "eigen"); !slices.Equal(v, []string{"198.51.100.0/24", "203.0.113.7"}) || n != 1 {
			t.Errorf("node %s: %v (%d tags)", id, v, n)
		}
	}
	if len(bus.lists[old]) != 0 {
		t.Error("een agent met protocol 56 kreeg een lijst")
	}
	ls, err := s.Lists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ls[0].Slug != "eigen" || ls[0].Count != 2 || ls[0].NodesWanted != 3 || ls[0].NodesSynced != 2 || len(ls[0].Failed) != 1 ||
		!strings.Contains(ls[0].Failed[0].Error, "agent te oud") {
		t.Errorf("lijst %+v", ls[0])
	}
	// Nog een ronde zonder wijziging: niets opnieuw.
	pollLists(t, pool, bus, a, b)
	bus.mu.Lock()
	before := len(bus.lists[a])
	bus.mu.Unlock()
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, n := onNode(bus, a, "eigen"); n != before {
		t.Errorf("tags %d, eerst %d", n, before)
	}

	// Weghalen: nieuwe generatie, de oude tag gaat weg.
	v1 := d.VersionID
	if _, err := s.ChangeEntries(ctx, actor, nil, own.ID, nil, []string{"203.0.113.7"}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	if v, n := onNode(bus, a, "eigen"); !slices.Equal(v, []string{"198.51.100.0/24"}) || n != 1 {
		t.Errorf("na weghalen: %v (%d tags)", v, n)
	}

	// Terugdraaien naar de eerste versie.
	d, err = s.Rollback(ctx, actor, nil, own.ID, v1, true)
	if err != nil || d.Applied || d.AddedN != 1 {
		t.Fatalf("voorbeeld terugdraaien %+v %v", d, err)
	}
	if _, err := s.Rollback(ctx, actor, nil, own.ID, v1, false); err != nil {
		t.Fatal(err)
	}
	pollLists(t, pool, bus, a, b)
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	if v, n := onNode(bus, b, "eigen"); !slices.Equal(v, []string{"198.51.100.0/24", "203.0.113.7"}) || n != 1 {
		t.Errorf("na terugdraaien: %v (%d tags)", v, n)
	}
	vs, err := s.Versions(ctx, own.ID)
	if err != nil || len(vs) != 3 || !strings.Contains(vs[0].Note, "teruggedraaid") {
		t.Errorf("versies %+v %v", vs, err)
	}

	// Verlopen: het tijdelijke /24 gaat er na een uur af.
	s.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := pool.Exec(ctx, `UPDATE node_status SET heartbeat_at = now() + interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE crowdsec_list_entries SET expires_at = now() - interval '1 minute' WHERE value = '198.51.100.0/24'`); err != nil {
		t.Fatal(err)
	}
	pollLists(t, pool, bus, a, b)
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	s.Now = time.Now
	if _, err := pool.Exec(ctx, `UPDATE node_status SET heartbeat_at = now()`); err != nil {
		t.Fatal(err)
	}
	if v, n := onNode(bus, a, "eigen"); !slices.Equal(v, []string{"203.0.113.7"}) {
		var cnt int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM crowdsec_list_entries WHERE list_id = $1`, own.ID).Scan(&cnt)
		t.Errorf("na verlopen: %v (%d tags, %d waarden) %v", v, n, cnt, bus.lists[a])
	}

	// Automatisch: hetzelfde IP op twee nodes.
	now := time.Now()
	for i, id := range []uuid.UUID{a, b} {
		if _, err := pool.Exec(ctx, `INSERT INTO crowdsec_alerts (node_id, alert_id, scenario, scope, value, started_at, stopped_at)
			VALUES ($1, $2, 'crowdsecurity/ssh-bf', 'ip', '198.51.100.77', $3, $3)`, id, 100+i, now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// En een alert op een eigen adres: nooit automatisch.
	if _, err := pool.Exec(ctx, `INSERT INTO crowdsec_alerts (node_id, alert_id, scenario, scope, value, started_at, stopped_at)
		VALUES ($1, 200, 'x', 'ip', '192.168.90.10', $2, $2), ($3, 201, 'x', 'ip', '192.168.90.10', $2, $2)`, a, now.Add(-time.Minute), b); err != nil {
		t.Fatal(err)
	}
	pollLists(t, pool, bus, a, b)
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := onNode(bus, b, "auto"); !slices.Equal(v, []string{"198.51.100.77"}) {
		t.Errorf("automatisch: %v", v)
	}
	auto, _ := s.listBySlug(ctx, "auto")
	es, _, _ := s.Entries(ctx, auto.ID, "", 0)
	if len(es) != 1 || es[0].ExpiresAt == nil || time.Until(*es[0].ExpiresAt) < 160*time.Hour || !strings.Contains(es[0].Reason, "2 nodes") {
		t.Errorf("automatische waarde %+v", es)
	}

	// Deblokkeren haalt het weg en het komt niet meteen terug.
	if _, err := s.Ban(ctx, actor, protocol.CrowdSecDecision{Delete: true, Scope: "ip", Value: "198.51.100.77"}, BanTarget{}); err != nil {
		t.Fatal(err)
	}
	pollLists(t, pool, bus, a, b)
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	if v, n := onNode(bus, b, "auto"); len(v) != 0 || n != 0 {
		t.Errorf("na deblokkeren: %v (%d tags)", v, n)
	}
	// Een nieuwe aanval daarna wel, en dan twee keer zo lang.
	for i, id := range []uuid.UUID{a, b} {
		if _, err := pool.Exec(ctx, `INSERT INTO crowdsec_alerts (node_id, alert_id, scenario, scope, value, started_at, stopped_at)
			VALUES ($1, $2, 'crowdsecurity/ssh-bf', 'ip', '198.51.100.77', $3, $3)`, id, 300+i, time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	es, _, _ = s.Entries(ctx, auto.ID, "", 0)
	if len(es) != 1 || time.Until(*es[0].ExpiresAt) < 300*time.Hour {
		t.Errorf("tweede keer %+v", es)
	}

	// Centraal blokkeren via IP blokkeren.
	if _, err := s.Ban(ctx, actor, protocol.CrowdSecDecision{Scope: "ip", Value: "203.0.113.50", Duration: "4h", Reason: "hand"},
		BanTarget{Central: true, Permanent: true}); err != nil {
		t.Fatal(err)
	}
	if es, _, _ := s.Entries(ctx, own.ID, "203.0.113.50", 0); len(es) != 1 || es[0].ExpiresAt != nil {
		t.Errorf("centraal %+v", es)
	}

	// Lijst uitzetten: de tags gaan van de nodes.
	off := false
	if _, err := s.UpdateList(ctx, actor, own.ID, ListInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	pollLists(t, pool, bus, a, b)
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	if v, n := onNode(bus, a, "eigen"); len(v) != 0 || n != 0 {
		t.Errorf("uitgezet: %v (%d tags)", v, n)
	}

	// Console loskoppelen: alleen agents met protocol 57, één keer.
	if _, err := s.DisconnectConsole(ctx, actor, nil, true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.ListTick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if bus.consoleOff[a] != 1 || bus.consoleOff[b] != 1 || bus.consoleOff[old] != 0 {
		t.Errorf("console off %v", bus.consoleOff)
	}
}

func TestPublicListAndNotify(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)
	bus := newFakeBus()
	s := NewService(pool, ev, nil, bus, log)
	box, _ := secrets.New(make([]byte, 32))
	s.Box = box
	a := seedLists(t, pool, "web1", protocol.CrowdSecListsSince)

	var mu sync.Mutex
	hits, body := 0, "# lijst\n10.0.0.0/8\n1.10.16.0/20\n203.0.113.0/24\n"
	src := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		if r.Header.Get("If-None-Match") == `"v1"` && strings.Contains(body, "203.0.113.0/24") {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = io.WriteString(w, body)
	}))
	defer src.Close()
	var got []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}))
	defer hook.Close()
	s.HTTP = src.Client()

	d, err := s.PreviewSource(ctx, "custom", src.URL+"/lijst.txt")
	if err != nil || d.Count != 2 || d.RefusedN != 1 {
		t.Fatalf("voorbeeld %+v %v", d, err)
	}
	if _, err := s.AddPublic(ctx, events.System(), ListInput{Source: "custom", URL: "http://x/y"}); err == nil {
		t.Error("http-URL toegelaten")
	}
	if _, err := s.AddPublic(ctx, events.System(), ListInput{Source: "feodo", RefreshMinutes: 5}); err == nil {
		t.Error("te vaak ophalen toegelaten")
	}
	l, err := s.AddPublic(ctx, events.System(), ListInput{Source: "custom", URL: src.URL + "/lijst.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := onNode(bus, a, l.Slug); !slices.Equal(v, []string{"1.10.16.0/20", "203.0.113.0/24"}) {
		t.Errorf("publieke lijst op de node: %v", v)
	}
	l, _ = s.list(ctx, nil, l.ID)
	if l.Count != 2 || l.Dropped != 1 {
		t.Errorf("lijst %+v", l)
	}
	// Binnen de verversing: niet opnieuw ophalen.
	if err := s.ListTick(ctx); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Errorf("%d keer opgehaald", hits)
	}
	// Nieuwe inhoud en terugdraaien zet de lijst vast.
	mu.Lock()
	body = "1.10.16.0/20\n"
	mu.Unlock()
	if _, err := s.RefreshList(ctx, events.System(), l.ID); err == nil {
		t.Error("meteen opnieuw ophalen toegelaten")
	}
	if _, err := pool.Exec(ctx, `UPDATE crowdsec_lists SET fetched_at = now() - interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshList(ctx, events.System(), l.ID); err != nil {
		t.Fatal(err)
	}
	vs, _ := s.Versions(ctx, l.ID)
	if len(vs) != 2 {
		t.Fatalf("versies %+v", vs)
	}
	if _, err := s.Rollback(ctx, events.System(), nil, l.ID, vs[1].ID, false); err != nil {
		t.Fatal(err)
	}
	l, _ = s.list(ctx, nil, l.ID)
	if !l.Pinned || l.Count != 2 {
		t.Errorf("teruggedraaid %+v", l)
	}

	// Meldingen via een webhook.
	s.HTTP = nil
	set, err := s.UpdateNotify(ctx, events.System(), nil, NotifySettings{AutoBlock: true, NodeProblems: true},
		[]ChannelInput{{Kind: "webhook", Name: "test", URL: hook.URL + "/geheim"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Notify.Channels) != 1 || strings.Contains(set.Notify.Channels[0].Target, "geheim") == false && set.Notify.Channels[0].Target == "" {
		t.Errorf("kanalen %+v", set.Notify.Channels)
	}
	raw, _ := json.Marshal(set)
	if strings.Contains(string(raw), hook.URL) {
		t.Error("het adres van het kanaal gaat naar de browser")
	}
	// Opnieuw bewaren zonder URL houdt het adres.
	if _, err := s.UpdateNotify(ctx, events.System(), nil, NotifySettings{AutoBlock: true, NodeProblems: true},
		[]ChannelInput{{ID: set.Notify.Channels[0].ID, Kind: "webhook", Name: "test"}}); err != nil {
		t.Fatal(err)
	}
	res, err := s.TestNotify(ctx)
	if err != nil || res["test"] != "" {
		t.Fatalf("test %v %v", res, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE crowdsec_nodes SET status = '{"installed":true,"active":"inactive"}' WHERE node_id = $1`, a); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.ListTick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[1]["title"] != "CrowdSec staat stil" {
		t.Errorf("meldingen %v", got)
	}
}
