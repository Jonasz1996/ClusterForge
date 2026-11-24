package publish

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

func TestAutoCandidatesNooitEenGok(t *testing.T) {
	cl, other := uuid.New(), uuid.New()
	n1, n2 := uuid.New(), uuid.New()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	yesterday := now.Add(-2 * time.Hour)
	st := Settings{Domain: "jbogaert.be"}
	svc := func(key, label string, mod func(*ServiceRow)) ServiceRow {
		r := ServiceRow{Key: key, ClusterID: &cl, ClusterName: "rustdesk", Port: 80, Web: true, Label: label,
			Target: "192.168.10.50", TargetVIP: true, Nodes: []NodeShort{{ID: n1}, {ID: n2}}}
		if mod != nil {
			mod(&r)
		}
		return r
	}
	ov := &Overview{
		Services: []ServiceRow{
			svc("ok", "rustdesk", nil),
			svc("published", "web", func(r *ServiceRow) { r.Published = []PublishedRef{{Domain: "web.jbogaert.be"}} }),
			svc("guess", "app", func(r *ServiceRow) { r.TargetVIP, r.Target = false, "192.168.10.51" }),
			svc("single", "single", func(r *ServiceRow) { r.TargetVIP, r.Nodes = false, []NodeShort{{ID: n1}} }),
			svc("internal", "patroni", func(r *ServiceRow) { r.Internal, r.InternalWhy = true, "Patroni" }),
			svc("taken", "adguard", nil),
			svc("dup1", "dubbel", nil),
			svc("dup2", "dubbel", func(r *ServiceRow) { r.Port = 81 }),
			svc("nonweb", "ssh", func(r *ServiceRow) { r.Web = false }),
			svc("failed", "kapot", nil),
			svc("given-up", "opgegeven", nil),
			svc("other-cluster", "ander", func(r *ServiceRow) { r.ClusterID = &other }),
		},
		rawHosts: []NPMHost{{ID: 3, DomainNames: []string{"adguard.jbogaert.be"}}},
	}
	rules := []AutoRule{{ClusterID: &cl, Enabled: true, Cleanup: true}}
	items := []AutoItem{
		{ServiceKey: "failed", State: AutoFailed, Tries: 1, TriedAt: &yesterday, Error: "502"},
		{ServiceKey: "given-up", State: AutoFailed, Tries: 3, TriedAt: &yesterday, Error: "502"},
	}
	got := map[string]AutoCandidate{}
	for _, c := range autoCandidates(st, ov, rules, items, now) {
		got[c.ServiceKey] = c
	}
	want := map[string]string{
		"ok":        "",
		"single":    "",
		"published": "al gepubliceerd",
		"guess":     "gok",
		"internal":  "intern",
		"taken":     "bestaat al",
		"dup1":      "meerdere diensten",
		"dup2":      "meerdere diensten",
		"failed":    "nieuwe poging",
		"given-up":  "Opnieuw",
	}
	for key, w := range want {
		c, ok := got[key]
		if !ok {
			t.Errorf("%s ontbreekt", key)
			continue
		}
		if w == "" && c.Skip != "" || w != "" && !strings.Contains(c.Skip, w) {
			t.Errorf("%s: skip %q, wil %q", key, c.Skip, w)
		}
	}
	for _, key := range []string{"nonweb", "other-cluster"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s hoort niet in de lijst", key)
		}
	}
	if got["ok"].Domain != "rustdesk.jbogaert.be" {
		t.Errorf("naam %q", got["ok"].Domain)
	}
	// Een regel per dienst gaat voor die van het cluster.
	rules = append(rules, AutoRule{ServiceKey: "ok", Enabled: false})
	for _, c := range autoCandidates(st, ov, rules, items, now) {
		if c.ServiceKey == "ok" {
			t.Errorf("dienst met regel uit hoort er niet bij")
		}
	}
}

// TestAutoSettleEnOpruimen: de afloop van de taken en wanneer een cluster
// als uit dienst geldt.
func TestAutoSettleEnOpruimen(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	s := NewService(pool, events.NewWriter(store.New(pool), log), log, nil, nil, nil)
	job := func(status string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO jobs (kind, title, status, error) VALUES ($1, 't', $2::job_status, 'NPM: 502')
			RETURNING id`, KindApply, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	var cl uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment) VALUES ('rd', 'RustDesk', 'keepalived', 'lab') RETURNING id`).Scan(&cl); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO publish_hosts (npm_host_id, domain, desired, cluster_id, created_by_cf) VALUES (7, 'ok.jbogaert.be', '{}', $1, true)`, cl); err != nil {
		t.Fatal(err)
	}
	add := func(key, domain, state string, jobID uuid.UUID) {
		if _, err := pool.Exec(ctx, `INSERT INTO publish_auto_items (service_key, domain, cluster_id, cluster_name, state, job_id, tries)
			VALUES ($1, $2, $3, 'RustDesk', $4, $5, 1)`, key, domain, cl, state, jobID); err != nil {
			t.Fatal(err)
		}
	}
	add("ok", "ok.jbogaert.be", AutoPublishing, job("succeeded"))
	add("kapot", "kapot.jbogaert.be", AutoPublishing, job("failed"))
	add("loopt", "loopt.jbogaert.be", AutoPublishing, job("running"))
	if err := s.autoSettle(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := s.autoItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AutoItem{}
	for _, it := range items {
		got[it.ServiceKey] = it
	}
	if it := got["ok"]; it.State != AutoPublished || it.NPMHostID == nil || *it.NPMHostID != 7 || it.Tries != 0 {
		t.Errorf("ok: %+v", it)
	}
	if it := got["kapot"]; it.State != AutoFailed || it.Error != "NPM: 502" {
		t.Errorf("kapot: %+v", it)
	}
	if it := got["loopt"]; it.State != AutoPublishing {
		t.Errorf("loopt: %+v", it)
	}
	// Het cluster heeft een actieve node: niets op te ruimen.
	if _, err := pool.Exec(ctx, `INSERT INTO nodes (hostname, cluster_id) VALUES ('rd1', $1)`, cl); err != nil {
		t.Fatal(err)
	}
	if started, err := s.autoCleanup(ctx); err != nil || started {
		t.Fatalf("opruimen met actieve node: %v %v", started, err)
	}
	// Alle nodes uit dienst: dan wel. Zonder NPM-koppeling mislukt de taak,
	// en dat staat bij de dienst.
	if _, err := pool.Exec(ctx, `UPDATE nodes SET lifecycle = 'decommissioned' WHERE cluster_id = $1`, cl); err != nil {
		t.Fatal(err)
	}
	if started, err := s.autoCleanup(ctx); err != nil || started {
		t.Fatalf("opruimen zonder NPM: %v %v", started, err)
	}
	items, _ = s.autoItems(ctx)
	for _, it := range items {
		if it.ServiceKey == "ok" && !strings.HasPrefix(it.Error, "opruimen mislukt") {
			t.Errorf("ok na opruimen: %+v", it)
		}
	}
	// De mislukte poging wacht een dag.
	if started, err := s.autoCleanup(ctx); err != nil || started {
		t.Fatalf("tweede poging: %v %v", started, err)
	}
	if err := s.RetryAuto(ctx, events.System(), "kapot"); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryAuto(ctx, events.System(), "ok"); err != ErrNotFound {
		t.Errorf("opnieuw op een gepubliceerde dienst: %v", err)
	}
}
