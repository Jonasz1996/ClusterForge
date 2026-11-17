package protocol

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ReconfigureSince is de eerste protocolversie die CmdAgentReconfigure kent
// en zijn adressen in de heartbeat meldt.
const ReconfigureSince = 47

// CmdAgentReconfigure zet het serveradres en/of het NATS-adres in agent.json
// om. De agent test eerst of hij de server op het nieuwe adres bereikt, en
// zet het oude terug als hij na de herstart niet met de server praat.
const CmdAgentReconfigure = "agent.reconfigure"

// ReconfigureRollbackAfter is hoe lang een agent na een herconfiguratie heeft
// om met de server te praten; daarna zet een timer van systemd het oude
// agent.json terug.
const ReconfigureRollbackAfter = 2 * time.Minute

// ReconfigureTimeout is hoe lang het commando zelf mag duren (de tests).
const ReconfigureTimeout = time.Minute

// AgentReconfigure zijn de nieuwe adressen; leeg laat dat adres zoals het is.
type AgentReconfigure struct {
	// ServerURL is het http(s)-adres van ClusterForge, voor nieuwe agents
	// en het installatiescript.
	ServerURL string `json:"server_url,omitempty"`
	// NatsURL is tls://host:poort van NATS.
	NatsURL string `json:"nats_url,omitempty"`
}

// NormalizeServerURL controleert een serveradres voor agents: http(s), een
// host, geen pad, query of gebruiker. Het geeft het adres zonder slash op het
// einde.
func NormalizeServerURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("geef een adres zoals http://192.168.113.153:8080 of https://clusterforge.lan")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("alleen schema, host en poort: geen pad, gebruiker of query")
	}
	return u.Scheme + "://" + u.Host, nil
}

// NormalizeNatsURL controleert een NATS-adres voor agents. Het mag zonder
// tls:// en zonder poort; de poort is dan defaultPort.
func NormalizeNatsURL(raw string, defaultPort int) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "tls://")
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, "/?@ ") || raw == "" {
		return "", errors.New("geef host:poort, zoals 192.168.113.153:4222")
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		host, port = strings.Trim(raw, "[]"), ""
	}
	if host == "" {
		return "", errors.New("geef host:poort, zoals 192.168.113.153:4222")
	}
	if port == "" {
		if defaultPort <= 0 {
			defaultPort = 4222
		}
		port = strconv.Itoa(defaultPort)
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return "", errors.New("de poort is geen getal")
		}
	}
	return "tls://" + net.JoinHostPort(host, port), nil
}
