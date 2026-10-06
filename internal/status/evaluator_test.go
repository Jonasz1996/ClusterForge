package status

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

// TestAfterFails: een hook na de evaluator die altijd faalt, zoals een kapotte
// berekening van de diensten, houdt de VIP-eigenaar en vip.owner_changed niet
// tegen.
func TestAfterFails(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ev := events.NewWriter(store.New(pool), log)

	var cluster, node, vip uuid.UUID
	for _, st := range []struct {
		sql  string
		args []any
		dst  *uuid.UUID
	}{
		{`INSERT INTO clusters (slug, name, type, environment) VALUES ('web-lab', 'web-lab', 'nginx', 'lab') RETURNING id`, nil, &cluster},
		{`INSERT INTO nodes (hostname, cluster_id) VALUES ('web01', $1) RETURNING id`, []any{&cluster}, &node},
		{`INSERT INTO vips (cluster_id, address) VALUES ($1, '10.0.30.100') RETURNING id`, []any{&cluster}, &vip},
	} {
		if err := pool.QueryRow(ctx, st.sql, st.args...).Scan(st.dst); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public) VALUES (gen_random_uuid(), $1, 'UTEST')`, node); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses, services)
		VALUES ($1, now(), '{10.0.30.11,10.0.30.100}', '{"keepalived": "active"}')`, node); err != nil {
		t.Fatal(err)
	}

	e := NewEvaluator(pool, ev, log)
	e.Warmup = 0
	calls := 0
	e.After = func(context.Context) error {
		calls++
		return errors.New("kapot")
	}
	if err := e.Evaluate(ctx); err != nil {
		t.Fatalf("Evaluate gaf de fout van de hook door: %v", err)
	}
	if calls != 1 {
		t.Fatalf("hook %d keer aangeroepen", calls)
	}
	var owner *uuid.UUID
	var events int
	if err := pool.QueryRow(ctx, `SELECT owner_node_id FROM vips WHERE id = $1`, vip).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = 'vip.owner_changed' AND subject_id = $1`, vip.String()).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if owner == nil || *owner != node || events != 1 {
		t.Fatalf("eigenaar %v, %d events vip.owner_changed", owner, events)
	}
}
