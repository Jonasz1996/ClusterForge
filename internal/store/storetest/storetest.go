// Package storetest geeft tests een lege database met het volledige schema.
package storetest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// DB verbindt met CF_TEST_DATABASE_URL, maakt het schema leeg en voert de
// migraties uit. Zonder die variabele wordt de test overgeslagen. Tests die
// dit gebruiken, mogen niet parallel lopen (go test -p 1).
func DB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("CF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CF_TEST_DATABASE_URL niet gezet; integratietest overgeslagen")
	}
	ctx := context.Background()
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
