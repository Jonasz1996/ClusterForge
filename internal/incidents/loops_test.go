package incidents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

// TestRestartLoopIncident: cf-vaultx die 29.000 keer herstartte (22/11) is
// een incident met de laatste fout; 5 herstarts niet.
func TestRestartLoopIncident(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	var node uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname) VALUES ('vaultx') RETURNING id`).Scan(&node); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	add := func(subject, action string, restarts string, ago time.Duration, summary string) {
		t.Helper()
		detail := `{}`
		if restarts != "" {
			detail = `{"restarts": "` + restarts + `"}`
		}
		if _, err := pool.Exec(ctx, `INSERT INTO node_changes (node_id, ts, source, kind, subject, action, summary, detail)
			VALUES ($1, $2, 'agent', 'service', $3, $4, $5, $6)`, node, now.Add(-ago), subject, action, summary, detail); err != nil {
			t.Fatal(err)
		}
	}
	add("cf-vaultx.service", "failed", "", 2*time.Minute, "cf-vaultx.service mislukt: afgesloten met een fout")
	add("cf-vaultx.service", "auto_restarted", "29000", time.Minute, "")
	add("nginx.service", "auto_restarted", "5", time.Minute, "")
	add("old.service", "auto_restarted", "500", time.Hour, "")
	d := NewDetector(pool, nil, nil)
	var w World
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		w = World{Hosts: map[Key][]string{}, Related: map[[2]uuid.UUID]bool{}, Status: map[Key]string{}}
		return restartLoops(ctx, tx, &w, d.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Problems) != 1 {
		t.Fatalf("problemen: %+v", w.Problems)
	}
	p := w.Problems[0]
	if p.Key != (Key{SubjectUnit, LoopSubject(node, "cf-vaultx.service")}) || !p.Primary ||
		!strings.Contains(p.Reason, "29000 keer") || !strings.Contains(p.Reason, "afgesloten met een fout") {
		t.Fatalf("probleem: %+v", p)
	}
	if Title(p) != "cf-vaultx.service op vaultx zit in een herstartlus" {
		t.Fatalf("titel: %s", Title(p))
	}
}
