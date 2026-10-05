// Package config leest de configuratie van clusterforge-server uit
// omgevingsvariabelen. Alles heeft een veilige standaardwaarde behalve de
// databaseverbinding.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// DatabaseURL is een PostgreSQL-connectiestring (CF_DATABASE_URL).
	DatabaseURL string
	// Listen is het adres van de HTTP-server (CF_LISTEN, standaard ":8080").
	Listen string
	// SecureCookies zet de Secure-vlag op cookies (CF_SECURE_COOKIES,
	// standaard true). Alleen uitzetten voor lokale ontwikkeling zonder TLS.
	SecureCookies bool
	// SessionTTL is hoe lang een sessie zonder activiteit geldig blijft
	// (CF_SESSION_TTL, standaard 12h).
	SessionTTL time.Duration
	// TrustProxyHeaders laat de server het client-IP uit het laatste adres in
	// X-Forwarded-For halen (CF_TRUST_PROXY_HEADERS, standaard false). Alleen
	// aanzetten als de server uitsluitend via je eigen reverse proxy bereikbaar is.
	TrustProxyHeaders bool
	// LogLevel is debug, info, warn of error (CF_LOG_LEVEL, standaard info).
	LogLevel string
}

func FromEnv() (Config, error) {
	c := Config{
		DatabaseURL:   os.Getenv("CF_DATABASE_URL"),
		Listen:        envOr("CF_LISTEN", ":8080"),
		SecureCookies: true,
		SessionTTL:    12 * time.Hour,
		LogLevel:      envOr("CF_LOG_LEVEL", "info"),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("CF_DATABASE_URL is niet gezet")
	}
	if v := os.Getenv("CF_SECURE_COOKIES"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("CF_SECURE_COOKIES: %w", err)
		}
		c.SecureCookies = b
	}
	if v := os.Getenv("CF_TRUST_PROXY_HEADERS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("CF_TRUST_PROXY_HEADERS: %w", err)
		}
		c.TrustProxyHeaders = b
	}
	if v := os.Getenv("CF_SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("CF_SESSION_TTL: %w", err)
		}
		if d < time.Minute {
			return c, fmt.Errorf("CF_SESSION_TTL moet minstens 1m zijn")
		}
		c.SessionTTL = d
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
