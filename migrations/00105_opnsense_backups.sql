-- +goose Up
-- Fase 34 M5: back-ups van de OPNsense-configuratie in ClusterForge. De
-- volledige config.xml (met geheimen) staat versleuteld met de masterkey.
CREATE TABLE opnsense_backups (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id   uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    taken_at  timestamptz NOT NULL DEFAULT now(),
    reason    text NOT NULL,
    sha256    text NOT NULL,
    size      integer NOT NULL,
    version   text NOT NULL DEFAULT '',
    key_id    text NOT NULL,
    nonce     text NOT NULL,
    data      bytea NOT NULL
);
CREATE INDEX opnsense_backups_node ON opnsense_backups (node_id, taken_at DESC);

-- +goose Down
DROP TABLE opnsense_backups;
