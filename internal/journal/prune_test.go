package journal

import (
	"context"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

func TestRecordAndPrune(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	q := store.New(pool)
	node, err := q.CreateNode(ctx, store.CreateNodeParams{Hostname: "web-01", Lifecycle: store.NodeLifecycleActive, Tags: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	recent := Entry{NodeID: node.ID, TS: now.Add(-time.Hour), Source: SourceAgent, Kind: KindPackage, Subject: "nginx",
		Action: "upgrade", From: "1", To: "2", By: "root"}
	old := recent
	old.TS, old.Subject = now.Add(-Retention-time.Hour), "curl"
	// Twee keer dezelfde regel, zoals na een herverbinding: één keer bewaard.
	for range 2 {
		if err := Record(ctx, q, recent, old); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (n int) {
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM node_changes").Scan(&n)
		return n
	}
	if n := count(); n != 2 {
		t.Fatalf("na dubbel schrijven: %d regels", n)
	}
	var summary string
	_ = pool.QueryRow(ctx, "SELECT summary FROM node_changes WHERE subject = 'nginx'").Scan(&summary)
	if summary != "nginx bijgewerkt van 1 naar 2 (root)" {
		t.Fatalf("zin: %q", summary)
	}

	p := NewPruner(q)
	if err := p.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("na opruimen: %d regels", n)
	}
	// Binnen het uur doet de pruner niets.
	if err := Record(ctx, q, old); err != nil {
		t.Fatal(err)
	}
	if err := p.Tick(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 2 {
		t.Fatalf("binnen het uur: %d regels", n)
	}
	if err := p.Tick(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("na een uur: %d regels", n)
	}
}
