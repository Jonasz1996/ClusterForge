-- +goose Up
-- Fase 46: automatisch onderhoud door de agents. Een beleid per omgeving,
-- voor de nodes zonder cluster en per cluster (geen rij = zoals de
-- omgeving); de laatste start per beleid; en per node wat elke beurt deed
-- (het logboek en het laatste voorbeeld).
CREATE TABLE upkeep_policies (
    scope      text PRIMARY KEY,
    cluster_id uuid REFERENCES clusters (id) ON DELETE CASCADE,
    policy     jsonb NOT NULL,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE upkeep_runs (
    scope      text PRIMARY KEY,
    started_at timestamptz NOT NULL,
    job_id     uuid,
    note       text NOT NULL DEFAULT ''
);

CREATE TABLE upkeep_results (
    id         bigserial PRIMARY KEY,
    job_id     uuid NOT NULL,
    node_id    uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    cluster_id uuid REFERENCES clusters (id) ON DELETE SET NULL,
    hostname   text NOT NULL,
    mode       text NOT NULL,
    at         timestamptz NOT NULL DEFAULT now(),
    ok         boolean NOT NULL,
    freed      bigint NOT NULL DEFAULT 0,
    result     jsonb NOT NULL
);
CREATE INDEX upkeep_results_node ON upkeep_results (node_id, at DESC);
CREATE INDEX upkeep_results_at ON upkeep_results (at DESC);

-- +goose Down
DROP TABLE upkeep_results;
DROP TABLE upkeep_runs;
DROP TABLE upkeep_policies;
