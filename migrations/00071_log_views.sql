-- +goose Up
-- Logweergaven (fase 23): een bewaarde selectie van logbronnen op nodes
-- (bestand onder /var/log, journaal of container) met een filter, om de
-- logs van een cluster snel opnieuw te bekijken of tussen nodes te
-- vergelijken. De logs zelf bewaart ClusterForge niet; de agents lezen ze
-- telkens opnieuw.
CREATE TABLE log_views (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- cluster_id: het cluster waar de weergave bij hoort; NULL voor een
    -- weergave over nodes van verschillende clusters.
    cluster_id  uuid REFERENCES clusters (id) ON DELETE CASCADE,
    -- sources: [{node_id, kind, name}], hoogstens 12.
    sources     jsonb NOT NULL DEFAULT '[]',
    -- filter: zoekterm, uitsluiten, niveaus, tijdvak (relatief, zoals
    -- "1h") en limiet, zoals de GUI het bewaart.
    filter      jsonb NOT NULL DEFAULT '{}',
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (name)
);

-- +goose Down
DROP TABLE log_views;
