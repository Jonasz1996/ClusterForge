-- +goose Up
-- API-tokens (fase 20): alleen-lezen toegang tot /api/dashboard voor een
-- eigen dashboard zoals homepage. De database bewaart alleen de sha256 van
-- het token; het token zelf ziet de beheerder één keer.
CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash   bytea NOT NULL UNIQUE,
    -- prefix: de eerste tekens van het token, om het in de lijst te herkennen.
    prefix       text NOT NULL,
    -- cluster_ids: de clusters die het token mag zien; leeg = alle clusters.
    cluster_ids  uuid[] NOT NULL DEFAULT '{}',
    expires_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    last_used_at timestamptz,
    last_used_ip text NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE api_tokens;
