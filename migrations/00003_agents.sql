-- +goose Up
-- Geheimen die de server zelf aanmaakt, zoals het TLS-certificaat van NATS.
CREATE TABLE server_secrets (
    name        text PRIMARY KEY,
    value       bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE enrollment_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- sha256 van het token; het token zelf wordt alleen bij aanmaken getoond.
    token_hash   bytea NOT NULL UNIQUE,
    description  text NOT NULL DEFAULT '',
    -- Vast gekoppeld aan een bestaande node, of nieuwe nodes in dit cluster zetten.
    node_id      uuid REFERENCES nodes (id) ON DELETE CASCADE,
    cluster_id   uuid REFERENCES clusters (id) ON DELETE SET NULL,
    max_uses     integer NOT NULL CHECK (max_uses BETWEEN 1 AND 100),
    uses         integer NOT NULL DEFAULT 0,
    expires_at   timestamptz NOT NULL,
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agents (
    id                uuid PRIMARY KEY,
    node_id           uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    nkey_public       text NOT NULL UNIQUE,
    machine_id        text NOT NULL DEFAULT '',
    version           text NOT NULL DEFAULT '',
    protocol_version  integer NOT NULL DEFAULT 1,
    enrolled_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at      timestamptz,
    revoked_at        timestamptz
);
-- Per node hoogstens één actieve agent.
CREATE UNIQUE INDEX agents_active_node_key ON agents (node_id) WHERE revoked_at IS NULL;

-- Laatste heartbeat per node.
CREATE TABLE node_status (
    node_id         uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    heartbeat_at    timestamptz NOT NULL,
    uptime_seconds  bigint NOT NULL DEFAULT 0,
    load1           double precision NOT NULL DEFAULT 0,
    load5           double precision NOT NULL DEFAULT 0,
    load15          double precision NOT NULL DEFAULT 0,
    addresses       text[] NOT NULL DEFAULT '{}',
    services        jsonb NOT NULL DEFAULT '{}'::jsonb
);

-- Laatste facts per node; een nieuwe snapshot alleen als de hash verandert.
CREATE TABLE node_facts (
    node_id       uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    hash          text NOT NULL,
    facts         jsonb NOT NULL,
    collected_at  timestamptz NOT NULL,
    changed_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE node_facts;
DROP TABLE node_status;
DROP TABLE agents;
DROP TABLE enrollment_tokens;
DROP TABLE server_secrets;
