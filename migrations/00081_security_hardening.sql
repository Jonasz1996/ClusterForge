-- +goose Up
-- Verharding (fase 27 M2): wat de agent van de node uitleest (protocol 41)
-- en de uitkomst van de regels op de server.
CREATE TABLE security_hardening (
    node_id      uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    collected_at timestamptz,
    audit        jsonb,
    results      jsonb NOT NULL DEFAULT '[]',
    score        integer CHECK (score BETWEEN 0 AND 100),
    error        text NOT NULL DEFAULT '',
    error_at     timestamptz
);

-- +goose Down
DROP TABLE security_hardening;
