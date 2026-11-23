-- +goose Up
-- Fase 48: een losse installatie (of de oude ClusterForge-server) is
-- vervangen door een cluster. Daaruit volgen voorstellen (NPM, regels en
-- NAT van OPNsense van het oude adres naar het VIP) en na keep_days de
-- vraag om de oude installatie op te ruimen (eerst een dump).
CREATE TABLE replacements (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id    uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    old_node_id   uuid REFERENCES nodes (id) ON DELETE SET NULL,
    old_hostname  text NOT NULL DEFAULT '',
    old_address   inet NOT NULL,
    -- vip: het VIP van het cluster op het moment van vervangen.
    vip           inet,
    -- reason: overname, clusterforge, vervangen.
    reason        text NOT NULL,
    keep_days     integer NOT NULL DEFAULT 14 CHECK (keep_days BETWEEN 1 AND 365),
    -- state: actief, opruimen, opgeruimd, gehouden.
    state         text NOT NULL DEFAULT 'actief',
    job_id        uuid,
    cleanup_job   uuid,
    note          text NOT NULL DEFAULT '',
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX replacements_open ON replacements (cluster_id, old_address) WHERE state IN ('actief', 'opruimen');

-- +goose Down
DROP TABLE replacements;
