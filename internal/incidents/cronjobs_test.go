package incidents

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

// TestCronJobIncidents: een beheerde job is per node in de fout zolang de
// laatste run daar mislukte. Een run die overslaat omdat de vorige nog liep
// (zonder exitcode) verandert daar niets aan; een sync waarvan de
// voorbereiding zei dat deze node niet stuurt (overgeslagen, exitcode 0) wel:
// die node doet dan niet mee, ook niet met een oude mislukte run van vóór
// de update van de agent of van vóór een failover.
func TestCronJobIncidents(t *testing.T) {
	ctx := context.Background()
	pool := storetest.DB(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var cluster, job, nodeA, nodeB uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment) VALUES ('mp', 'Media', 'generic', 'prod') RETURNING id`).Scan(&cluster); err != nil {
		t.Fatal(err)
	}
	for _, n := range []struct {
		id   *uuid.UUID
		name string
	}{{&nodeA, "mp1"}, {&nodeB, "mp2"}} {
		if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, cluster_id) VALUES ($1, $2) RETURNING id`, n.name, cluster).Scan(n.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO cron_jobs (cluster_id, name, kind, schedule, run_on, command, alert)
		VALUES ($1, 'platform-sleutels', 'sync', '* * * * *', 'vip', '', true) RETURNING id`, cluster).Scan(&job); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour)
	at := 0
	run := func(node uuid.UUID, result string, exit *int) {
		t.Helper()
		at++
		start := t0.Add(time.Duration(at) * time.Minute)
		exec(`INSERT INTO cron_runs (id, job_id, node_id, revision, reason, result, started_at, ended_at, exit_code)
			VALUES ($1, $2, $3, 1, 'schedule', $4, $5, $5, $6)`, uuid.New(), job, node, result, start, exit)
	}
	code := func(c int) *int { return &c }
	failing := func() []string {
		t.Helper()
		w, err := (&Detector{Rules: DefaultRules}).world(ctx, store.New(pool), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		k := Key{SubjectCronJob, job}
		if w.Status[k] != "fail" {
			return nil
		}
		return w.Hosts[k]
	}

	// Vóór de update van de agent: de node met alleen het VIP van de
	// database faalde met 3, de node met het VIP stuurde.
	run(nodeA, "ok", code(0))
	run(nodeB, "failed", code(3))
	if got := failing(); len(got) != 1 || got[0] != "mp2" {
		t.Fatalf("vóór de update: %v", got)
	}
	// Na de update slaat mp2 over: deze node stuurt niet. Dan is de job
	// daar niet meer in de fout.
	run(nodeB, "skipped", code(0))
	if got := failing(); got != nil {
		t.Fatalf("na de update, mp2 stuurt niet: %v", got)
	}

	// Een echte fout op mp1, daarna liep de vorige run nog (overgeslagen
	// zonder exitcode): de fout blijft.
	run(nodeA, "failed", code(1))
	run(nodeA, "skipped", nil)
	if got := failing(); len(got) != 1 || got[0] != "mp1" {
		t.Fatalf("vorige run liep nog: %v", got)
	}
	// Failover: mp1 stuurt niet meer. De oude fout telt daar niet meer.
	run(nodeA, "skipped", code(0))
	if got := failing(); got != nil {
		t.Fatalf("na de failover: %v", got)
	}
	// mp2 heeft nu het VIP en faalt echt: weer in de fout.
	run(nodeB, "failed", code(1))
	if got := failing(); len(got) != 1 || got[0] != "mp2" {
		t.Fatalf("echte fout op mp2: %v", got)
	}
}
