package zabbixagent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Settings zijn de instellingen van de pagina Zabbix-agents.
type Settings struct {
	AutoApply      bool     `json:"auto_apply"`
	AutoProd       bool     `json:"auto_prod"`
	Server         string   `json:"server"`
	ServerActive   string   `json:"server_active"`
	Version        string   `json:"version"`
	HostMetadata   string   `json:"host_metadata"`
	APIURL         string   `json:"api_url"`
	HostGroup      string   `json:"host_group"`
	Templates      []string `json:"templates"`
	DockerTemplate string   `json:"docker_template"`
	Proxy          string   `json:"proxy"`
	// TokenSet: er is een API-token; het token zelf gaat nooit naar de browser.
	TokenSet bool `json:"token_set"`
	// Available: de server heeft een masterkey om sleutels te bewaren.
	Available bool       `json:"available"`
	UpdatedAt time.Time  `json:"updated_at"`
	UpdatedBy *uuid.UUID `json:"-"`

	tokenEnc []byte
	tokenID  string
}

// Ready: genoeg ingesteld om toe te passen.
func (s Settings) Ready() bool {
	return s.Server != "" && s.ServerActive != "" && s.APIURL != "" && s.TokenSet
}

// Node is een node met een ClusterForge-agent, met zijn Zabbix-agent-stand.
type Node struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	IP          string     `json:"ip"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	ClusterEnv  string     `json:"cluster_environment"`
	Online      bool       `json:"online"`
	Supported   bool       `json:"supported"`
	Docker      bool       `json:"docker"`
	// AgentService is de stand van zabbix-agent2 uit de heartbeat.
	AgentService string `json:"agent_service"`
	// Enabled: er is een rij (ClusterForge kent een sleutel voor de node).
	Enabled       bool       `json:"enabled"`
	Managed       bool       `json:"managed"`
	PSKIdentity   string     `json:"psk_identity"`
	PSKCreatedAt  *time.Time `json:"psk_created_at"`
	AppliedHash   string     `json:"-"`
	AppliedAt     *time.Time `json:"applied_at"`
	ApplyJobID    *uuid.UUID `json:"apply_job_id"`
	ApplyRunning  bool       `json:"apply_running"`
	ApplyError    string     `json:"apply_error"`
	ApplyFailedAt *time.Time `json:"apply_failed_at"`
	ZabbixHostID  string     `json:"zabbix_host_id"`
	// Pending: wat er moet staan, is nog niet toegepast.
	Pending bool `json:"pending"`

	pskEnc  []byte
	pskKey  string
	pskHash string
}

// Prod: de node hoort bij een prod-cluster.
func (n Node) Prod() bool { return n.ClusterEnv == "prod" }

// Config is wat er op de node moet staan met deze instellingen.
func (n Node) Config(s Settings) Config {
	return Config{
		Hostname: n.Hostname, Server: s.Server, ServerActive: s.ServerActive, Version: s.Version,
		HostMetadata: s.HostMetadata, PSKIdentity: n.PSKIdentity, PSKHash: n.pskHash,
	}
}

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	var st Settings
	err := s.pool.QueryRow(ctx, `SELECT auto_apply, auto_prod, server, server_active, version, host_metadata, api_url,
		api_token_enc, api_token_id, host_group, templates, docker_template, proxy, updated_at, updated_by FROM zabbix_agent_settings`).
		Scan(&st.AutoApply, &st.AutoProd, &st.Server, &st.ServerActive, &st.Version, &st.HostMetadata, &st.APIURL,
			&st.tokenEnc, &st.tokenID, &st.HostGroup, &st.Templates, &st.DockerTemplate, &st.Proxy, &st.UpdatedAt, &st.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		st, err = Settings{Version: "8.0", HostMetadata: "Linux", HostGroup: "ClusterForge",
			Templates: []string{"Linux by Zabbix agent"}, DockerTemplate: "Docker by Zabbix agent 2"}, nil
	}
	if st.Templates == nil {
		st.Templates = []string{}
	}
	st.TokenSet = len(st.tokenEnc) > 0
	st.Available = s.Box != nil
	return st, err
}

const nodesSQL = `
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), ''), n.cluster_id,
       coalesce(c.name, ''), coalesce(c.environment::text, ''),
       a.protocol_version, s.heartbeat_at, f.facts, s.services,
       zn.node_id IS NOT NULL, coalesce(zn.managed, false), coalesce(zn.psk_identity, ''), zn.psk_created_at,
       zn.psk_enc, coalesce(zn.psk_key_id, ''), coalesce(zn.psk_hash, ''),
       coalesce(zn.applied_hash, ''), zn.applied_at, zn.apply_job_id, coalesce(j.status::text IN ('queued', 'running'), false),
       coalesce(zn.apply_error, ''), zn.apply_failed_at, coalesce(zn.zabbix_host_id, '')
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
LEFT JOIN zabbix_agent_nodes zn ON zn.node_id = n.id
LEFT JOIN jobs j ON j.id = zn.apply_job_id
`

// ErrNotFound: geen node met een agent.
var ErrNotFound = errors.New("node niet gevonden of zonder agent")

// Nodes geeft alle nodes met een agent.
func (s *Service) Nodes(ctx context.Context) ([]Node, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return s.nodes(ctx, set, nodesSQL+` ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname)`)
}

// Node geeft één node.
func (s *Service) Node(ctx context.Context, id uuid.UUID) (Node, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return Node{}, err
	}
	ns, err := s.nodes(ctx, set, nodesSQL+` WHERE n.id = $1`, id)
	if err != nil {
		return Node{}, err
	}
	if len(ns) == 0 {
		return Node{}, ErrNotFound
	}
	return ns[0], nil
}

func (s *Service) nodes(ctx context.Context, set Settings, sql string, args ...any) ([]Node, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	var out []Node
	for rows.Next() {
		var n Node
		var proto int32
		var hb *time.Time
		var facts, services []byte
		if err := rows.Scan(&n.NodeID, &n.Hostname, &n.IP, &n.ClusterID, &n.ClusterName, &n.ClusterEnv,
			&proto, &hb, &facts, &services, &n.Enabled, &n.Managed, &n.PSKIdentity, &n.PSKCreatedAt,
			&n.pskEnc, &n.pskKey, &n.pskHash, &n.AppliedHash, &n.AppliedAt, &n.ApplyJobID, &n.ApplyRunning,
			&n.ApplyError, &n.ApplyFailedAt, &n.ZabbixHostID); err != nil {
			return nil, err
		}
		n.Supported = int(proto) >= protocol.RunSince
		n.Online = hb != nil && now.Sub(*hb) <= status.HeartbeatLate
		if len(facts) > 0 {
			var f protocol.Facts
			if json.Unmarshal(facts, &f) == nil {
				n.Docker = f.Docker != nil
			}
		}
		if len(services) > 0 {
			var m map[string]string
			if json.Unmarshal(services, &m) == nil {
				n.AgentService = m["zabbix-agent2"]
			}
		}
		if n.Enabled {
			n.Pending = n.Config(set).Hash() != n.AppliedHash
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
