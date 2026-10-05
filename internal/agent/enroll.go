package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nkeys"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type EnrollOptions struct {
	ServerURL string
	Token     string
	// CAFile is een extra CA voor de HTTPS-verbinding met de server, voor een
	// reverse proxy met een eigen CA.
	CAFile  string
	Version string
	// Root is de bestandssysteemwortel voor machine-id; leeg is "/".
	Root string
}

// Enroll maakt een nieuw sleutelpaar, meldt de agent aan en geeft de
// configuratie terug. De private sleutel verlaat de machine niet.
func Enroll(ctx context.Context, o EnrollOptions) (*Config, error) {
	server := strings.TrimRight(o.ServerURL, "/")
	if !strings.HasPrefix(server, "https://") && !strings.HasPrefix(server, "http://") {
		return nil, errors.New("server moet beginnen met https:// of http://")
	}
	kp, err := nkeys.CreateUser()
	if err != nil {
		return nil, err
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, err
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	c := &Collector{Root: o.Root}
	body, err := json.Marshal(protocol.EnrollRequest{
		Token: strings.TrimSpace(o.Token), NkeyPublic: pub, Hostname: hostname, MachineID: c.machineID(),
		AgentVersion: o.Version, ProtocolVersion: protocol.Version,
	})
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s bevat geen certificaat", o.CAFile)
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/api/v1/agents/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return nil, fmt.Errorf("aanmelden geweigerd (%d): %s", resp.StatusCode, e.Message)
	}
	var er protocol.EnrollResponse
	if err := json.Unmarshal(data, &er); err != nil {
		return nil, fmt.Errorf("onverwacht antwoord van de server: %w", err)
	}
	return &Config{
		ServerURL: server, AgentID: er.AgentID, NodeID: er.NodeID, NatsURL: er.NatsURL,
		NatsCertSHA256: er.NatsCertSHA256, NkeySeed: string(seed),
	}, nil
}
