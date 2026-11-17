-- +goose Up
-- Tussenfase (agentprotocol 47): agents herconfigureren vanuit de GUI. De
-- agent meldt de adressen uit zijn agent.json in de heartbeat, en de server
-- bewaart het adres waarop agents hem moeten bereiken (leeg: het interne
-- adres uit CF_NATS_ADVERTISE).
ALTER TABLE agents
    ADD COLUMN server_url text NOT NULL DEFAULT '',
    ADD COLUMN nats_url text NOT NULL DEFAULT '',
    ADD COLUMN reconfigure_rolled_back text NOT NULL DEFAULT '';

CREATE TABLE agent_endpoint_settings (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_url  text NOT NULL DEFAULT '',
    nats_url    text NOT NULL DEFAULT '',
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX agent_endpoint_settings_single ON agent_endpoint_settings ((true));

-- +goose Down
DROP TABLE agent_endpoint_settings;
ALTER TABLE agents DROP COLUMN reconfigure_rolled_back, DROP COLUMN nats_url, DROP COLUMN server_url;
