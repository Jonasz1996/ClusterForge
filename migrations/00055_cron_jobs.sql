-- +goose Up
-- Beheerde cronjobs (fase 6, mijlpaal 3). Een job hoort bij een cluster en
-- draait op de node met het VIP, op één node of op alle nodes, als
-- systemd-timer met cf-agent cron-run (agentprotocol 18).
CREATE TABLE cron_jobs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id      uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    name            text NOT NULL,
    kind            text NOT NULL DEFAULT 'command' CHECK (kind IN ('command')),
    schedule        text NOT NULL,
    run_on          text NOT NULL CHECK (run_on IN ('vip', 'node', 'all')),
    node_id         uuid REFERENCES nodes (id) ON DELETE SET NULL,
    user_name       text NOT NULL DEFAULT 'root',
    command         text NOT NULL,
    timeout_seconds integer NOT NULL DEFAULT 3600,
    enabled         boolean NOT NULL DEFAULT true,
    revision        integer NOT NULL DEFAULT 1,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, name)
);

-- Per node de set jobs die de agent het laatst toepaste, als hash; zo ziet
-- de server welke node achterloopt.
CREATE TABLE cron_node_sets (
    node_id    uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    hash       text NOT NULL DEFAULT '',
    applied_at timestamptz,
    error      text NOT NULL DEFAULT '',
    error_kind text NOT NULL DEFAULT '',
    synced_at  timestamptz NOT NULL
);

-- Elke run. id komt van de agent; de start en het einde komen elk als
-- bericht, het einde vervangt de start.
CREATE TABLE cron_runs (
    id               uuid PRIMARY KEY,
    job_id           uuid NOT NULL REFERENCES cron_jobs (id) ON DELETE CASCADE,
    node_id          uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    revision         integer NOT NULL,
    reason           text NOT NULL,
    result           text NOT NULL CHECK (result IN ('running', 'ok', 'failed', 'skipped', 'timeout')),
    started_at       timestamptz NOT NULL,
    ended_at         timestamptz,
    exit_code        integer,
    output           text NOT NULL DEFAULT '',
    output_truncated boolean NOT NULL DEFAULT false,
    received_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cron_runs_job_idx ON cron_runs (job_id, started_at DESC);

-- +goose Down
DROP TABLE cron_runs;
DROP TABLE cron_node_sets;
DROP TABLE cron_jobs;
