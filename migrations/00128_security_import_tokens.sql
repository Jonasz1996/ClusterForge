-- +goose Up
-- Fase 43b: sleutels waarmee een pentestsysteem zelf nmap-rapporten
-- naar ClusterForge stuurt (POST /api/import/v1/nmap).
CREATE TABLE security_import_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    token_hash   bytea NOT NULL UNIQUE,
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    last_used_ip text NOT NULL DEFAULT '',
    imports      integer NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE security_import_tokens;
