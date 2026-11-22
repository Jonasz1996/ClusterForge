package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfigureTimeouts(t *testing.T) {
	for url, want := range map[string]time.Duration{
		"postgres://cf@db/cf":                   10 * time.Second,
		"postgres://cf@db/cf?connect_timeout=3": 3 * time.Second,
	} {
		cfg, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal(err)
		}
		configureTimeouts(cfg, url)
		if cfg.PingTimeout == 0 {
			t.Errorf("%s: geen PingTimeout; een dode verbinding blokkeert dan elke aanvraag", url)
		}
		if cfg.ConnConfig.ConnectTimeout != want {
			t.Errorf("%s: ConnectTimeout %s, verwacht %s", url, cfg.ConnConfig.ConnectTimeout, want)
		}
		if cfg.ConnConfig.DialFunc == nil {
			t.Errorf("%s: geen DialFunc", url)
		}
	}
}
