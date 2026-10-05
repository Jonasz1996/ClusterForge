// Package config leest de configuratie van clusterforge-server uit
// omgevingsvariabelen. Alles heeft een veilige standaardwaarde behalve de
// databaseverbinding.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/secrets"
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
	// NATSListen is het adres waarop agents met NATS verbinden (CF_NATS_LISTEN,
	// standaard ":4222").
	NATSListen string
	// NATSAdvertise is host:poort van NATS zoals agents die bereiken
	// (CF_NATS_ADVERTISE). Leeg betekent: de hostnaam waarmee de agent zich
	// aanmeldt, met de poort van NATSListen.
	NATSAdvertise string
	// AgentDir bevat de cf-agent-binaries die de server aanbiedt om te
	// downloaden (CF_AGENT_DIR, standaard /usr/share/clusterforge/agents).
	AgentDir string
	// VictoriaMetricsURL is het adres van VictoriaMetrics, bijvoorbeeld
	// http://victoriametrics:8428 (CF_VICTORIAMETRICS_URL). Leeg zet de
	// grafieken uit.
	VictoriaMetricsURL string
	// GrafanaNodeURL en GrafanaClusterURL zijn links naar Grafana-dashboards
	// met plaatshouders zoals {hostname} en {cluster} (CF_GRAFANA_NODE_URL,
	// CF_GRAFANA_CLUSTER_URL). Leeg verbergt de link.
	GrafanaNodeURL    string
	GrafanaClusterURL string
	// MasterKey versleutelt geheimen in de database, zoals Proxmox-tokens
	// (CF_MASTER_KEY of CF_MASTER_KEY_FILE, 32 bytes in base64 of hex). Zonder
	// sleutel kun je geen Proxmox koppelen.
	MasterKey []byte
	// DriftInterval is hoe vaak de server elke node op drift controleert
	// (CF_DRIFT_INTERVAL, standaard 15m). 0 zet de controles op de
	// achtergrond uit; Nu controleren blijft werken.
	DriftInterval time.Duration
}

func FromEnv() (Config, error) {
	c := Config{
		DatabaseURL:   os.Getenv("CF_DATABASE_URL"),
		Listen:        envOr("CF_LISTEN", ":8080"),
		SecureCookies: true,
		SessionTTL:    12 * time.Hour,
		DriftInterval: 15 * time.Minute,
		LogLevel:      envOr("CF_LOG_LEVEL", "info"),
		NATSListen:    envOr("CF_NATS_LISTEN", ":4222"),
		NATSAdvertise: os.Getenv("CF_NATS_ADVERTISE"),
		AgentDir:      envOr("CF_AGENT_DIR", "/usr/share/clusterforge/agents"),

		VictoriaMetricsURL: os.Getenv("CF_VICTORIAMETRICS_URL"),
		GrafanaNodeURL:     os.Getenv("CF_GRAFANA_NODE_URL"),
		GrafanaClusterURL:  os.Getenv("CF_GRAFANA_CLUSTER_URL"),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("CF_DATABASE_URL is niet gezet")
	}
	for name, v := range map[string]string{
		"CF_VICTORIAMETRICS_URL": c.VictoriaMetricsURL, "CF_GRAFANA_NODE_URL": c.GrafanaNodeURL,
		"CF_GRAFANA_CLUSTER_URL": c.GrafanaClusterURL,
	} {
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return c, fmt.Errorf("%s moet met http:// of https:// beginnen", name)
		}
	}
	if err := c.readMasterKey(); err != nil {
		return c, err
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
	if v := os.Getenv("CF_DRIFT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("CF_DRIFT_INTERVAL: %w", err)
		}
		if d != 0 && d < time.Minute {
			return c, fmt.Errorf("CF_DRIFT_INTERVAL moet 0 of minstens 1m zijn")
		}
		c.DriftInterval = d
	}
	return c, nil
}

func (c *Config) readMasterKey() error {
	v, name := os.Getenv("CF_MASTER_KEY"), "CF_MASTER_KEY"
	if f := os.Getenv("CF_MASTER_KEY_FILE"); f != "" {
		if v != "" {
			return fmt.Errorf("zet CF_MASTER_KEY of CF_MASTER_KEY_FILE, niet allebei")
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("CF_MASTER_KEY_FILE: %w", err)
		}
		v, name = string(b), "CF_MASTER_KEY_FILE"
	}
	if strings.TrimSpace(v) == "" {
		return nil
	}
	key, err := secrets.ParseKey(v)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	c.MasterKey = key
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
