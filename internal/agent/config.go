// Package agent is cf-agent: aanmelden bij de server, en daarna heartbeats en
// facts sturen over NATS.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultConfigPath = "/etc/clusterforge/agent.json"
	// DefaultEnrollPath is een eenmalig aanmeldbestand, bijvoorbeeld door
	// Proxmox in een nieuwe VM gezet: {"server": "...", "token": "..."}.
	DefaultEnrollPath = "/etc/clusterforge/enroll.json"
)

// Config is wat de agent na het aanmelden bewaart. Het bestand bevat de
// private sleutel en staat dus op 0600.
type Config struct {
	ServerURL      string `json:"server_url"`
	AgentID        string `json:"agent_id"`
	NodeID         string `json:"node_id"`
	NatsURL        string `json:"nats_url"`
	NatsCertSHA256 string `json:"nats_cert_sha256"`
	NkeySeed       string `json:"nkey_seed"`
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.NodeID == "" || c.NatsURL == "" || c.NkeySeed == "" || c.NatsCertSHA256 == "" {
		return nil, fmt.Errorf("%s is onvolledig; meld de agent opnieuw aan", path)
	}
	return &c, nil
}

// Save schrijft de configuratie atomair met rechten 0600.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// EnrollFile is de inhoud van DefaultEnrollPath.
type EnrollFile struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func LoadEnrollFile(path string) (*EnrollFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e EnrollFile
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if e.Server == "" || e.Token == "" {
		return nil, errors.New(path + " mist server of token")
	}
	return &e, nil
}
