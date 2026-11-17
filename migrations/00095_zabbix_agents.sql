-- +goose Up
-- Zabbix-agents: ClusterForge installeert zabbix-agent2 op de nodes, geeft
-- elke node een eigen PSK en zet die ook bij de host in Zabbix (via de API).

-- Eén rij met de instellingen.
CREATE TABLE zabbix_agent_settings (
    id             boolean PRIMARY KEY DEFAULT true CHECK (id),
    -- auto_apply: elke node met een ClusterForge-agent krijgt vanzelf
    -- zabbix-agent2; auto_prod: ook nodes van prod-clusters (met TOTP aangezet).
    auto_apply     boolean NOT NULL DEFAULT false,
    auto_prod      boolean NOT NULL DEFAULT false,
    -- server en server_active: Server= en ServerActive= in de agent-config.
    server         text NOT NULL DEFAULT '',
    server_active  text NOT NULL DEFAULT '',
    version        text NOT NULL DEFAULT '8.0',
    host_metadata  text NOT NULL DEFAULT 'Linux',
    -- De Zabbix API: adres van api_jsonrpc.php en een API-token (versleuteld).
    api_url        text NOT NULL DEFAULT '',
    api_token_enc  bytea,
    api_token_id   text NOT NULL DEFAULT '',
    -- Voor hosts die ClusterForge in Zabbix aanmaakt.
    host_group     text NOT NULL DEFAULT 'ClusterForge',
    templates      text[] NOT NULL DEFAULT '{Linux by Zabbix agent}',
    docker_template text NOT NULL DEFAULT 'Docker by Zabbix agent 2',
    proxy          text NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid REFERENCES users (id) ON DELETE SET NULL
);
INSERT INTO zabbix_agent_settings DEFAULT VALUES;

-- Een node met zabbix-agent2 van ClusterForge. managed false: niets meer
-- toepassen; de agent blijft staan.
CREATE TABLE zabbix_agent_nodes (
    node_id         uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    managed         boolean NOT NULL DEFAULT true,
    psk_identity    text NOT NULL,
    psk_enc         bytea NOT NULL,
    psk_key_id      text NOT NULL,
    -- psk_hash: vingerafdruk van de sleutel, voor de hash van wat er moet staan.
    psk_hash        text NOT NULL,
    psk_created_at  timestamptz NOT NULL DEFAULT now(),
    applied_hash    text NOT NULL DEFAULT '',
    applied_at      timestamptz,
    apply_job_id    uuid,
    apply_error     text NOT NULL DEFAULT '',
    apply_failed_at timestamptz,
    zabbix_host_id  text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE zabbix_agent_nodes;
DROP TABLE zabbix_agent_settings;
