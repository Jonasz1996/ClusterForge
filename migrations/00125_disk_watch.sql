-- +goose Up
-- Fase 41: schijfbewaking. De drempels (één rij) en per node de laatste
-- meting van wat er ruimte inneemt, met wat de laatste opruimbeurt deed.
CREATE TABLE disk_watch_settings (
    id            boolean PRIMARY KEY DEFAULT true CHECK (id),
    warn_pct      integer NOT NULL DEFAULT 80,
    crit_pct      integer NOT NULL DEFAULT 90,
    ignore_mounts text[]  NOT NULL DEFAULT '{}',
    updated_at    timestamptz NOT NULL DEFAULT now()
);
INSERT INTO disk_watch_settings (id) VALUES (true);

CREATE TABLE disk_scans (
    node_id     uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    job_id      uuid,
    running_job uuid,
    scanned_at  timestamptz,
    result      jsonb,
    cleaned_at  timestamptz,
    freed       bigint NOT NULL DEFAULT 0,
    cleaned     text[] NOT NULL DEFAULT '{}'
);

-- +goose Down
DROP TABLE disk_scans;
DROP TABLE disk_watch_settings;
