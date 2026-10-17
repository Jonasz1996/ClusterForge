package store

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Jonasz1996/clusterforge/migrations"
)

// Open maakt een connectiepool naar PostgreSQL en controleert de verbinding.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	// De standaard is max(4, aantal CPU's): te weinig voor de workers, hun
	// heartbeats, de achtergrondlussen en de API samen. Een taak die te lang
	// op een verbinding wacht, lijkt anders onderbroken. pool_max_conns in de
	// URL gaat voor.
	if !strings.Contains(url, "pool_max_conns") {
		cfg.MaxConns = 25
	}
	configureTimeouts(cfg, url)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database bereiken: %w", err)
	}
	return pool, nil
}

// configureTimeouts zorgt dat een verbinding die niet meer antwoordt (een
// herstarte database, een netwerk dat pakketten laat vallen) snel als dood
// geldt. Standaard wacht pgx daar zonder grens op: de pool pingt een oude
// verbinding met de context van de aanroeper, en een login blijft dan
// hangen tot TCP na een kwartier opgeeft.
func configureTimeouts(cfg *pgxpool.Config, url string) {
	cfg.PingTimeout = 5 * time.Second
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable: true, Idle: 30 * time.Second, Interval: 10 * time.Second, Count: 3,
		},
	}
	// connect_timeout in de URL gaat voor.
	if strings.Contains(url, "connect_timeout") {
		d.Timeout = cfg.ConnConfig.ConnectTimeout
	} else {
		cfg.ConnConfig.ConnectTimeout = d.Timeout
	}
	cfg.ConnConfig.DialFunc = d.DialContext
}

// Migrate brengt het databaseschema naar de laatste versie.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	return migrate(ctx, db)
}

func migrate(ctx context.Context, db *sql.DB) error {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("migraties laden: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migraties uitvoeren: %w", err)
	}
	return nil
}
