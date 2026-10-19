-- +goose Up
-- Agents werken zichzelf bij (agentprotocol 23). De heartbeat meldt welke
-- binary draait; upgrade_rolled_back is de sha256 van een update die de
-- agent terugzette, zodat ClusterForge die niet vanzelf opnieuw probeert.
ALTER TABLE agents
    ADD COLUMN arch                text NOT NULL DEFAULT '',
    ADD COLUMN binary_sha256       text NOT NULL DEFAULT '',
    ADD COLUMN upgrade_rolled_back text NOT NULL DEFAULT '';

-- Agents vanzelf bijwerken: standaard aan, nooit op prod. Er is hoogstens
-- één rij; zonder rij staat het aan.
CREATE TABLE agent_upgrade_settings (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    auto        boolean NOT NULL DEFAULT true,
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX agent_upgrade_settings_single ON agent_upgrade_settings ((true));

-- Clusters waar agents niet vanzelf bijgewerkt worden.
CREATE TABLE agent_upgrade_off (
    cluster_id  uuid PRIMARY KEY REFERENCES clusters (id) ON DELETE CASCADE,
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX jobs_agent_upgrade_idx ON jobs (created_at DESC) WHERE kind = 'agents.upgrade';

-- +goose Down
DROP INDEX jobs_agent_upgrade_idx;
DROP TABLE agent_upgrade_off;
DROP TABLE agent_upgrade_settings;
ALTER TABLE agents DROP COLUMN upgrade_rolled_back, DROP COLUMN binary_sha256, DROP COLUMN arch;
