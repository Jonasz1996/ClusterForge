-- +goose Up
-- Bestandssync (fase 6, mijlpaal 6): een beheerde job van de soort sync
-- kopieert met rsync over SSH een map naar de andere nodes van het cluster
-- of naar een ander cluster (agentprotocol 21).
ALTER TABLE cron_jobs DROP CONSTRAINT cron_jobs_kind_check;
ALTER TABLE cron_jobs ADD CONSTRAINT cron_jobs_kind_check CHECK (kind IN ('command', 'sync'));
ALTER TABLE cron_jobs ADD COLUMN sync_source text NOT NULL DEFAULT '';
ALTER TABLE cron_jobs ADD COLUMN sync_target text NOT NULL DEFAULT '';
-- sync_to_cluster leeg: de andere nodes van het eigen cluster.
ALTER TABLE cron_jobs ADD COLUMN sync_to_cluster uuid REFERENCES clusters (id) ON DELETE SET NULL;
ALTER TABLE cron_jobs ADD COLUMN sync_delete boolean NOT NULL DEFAULT false;
ALTER TABLE cron_jobs ADD COLUMN sync_exclude text[] NOT NULL DEFAULT '{}';
ALTER TABLE cron_jobs ADD COLUMN sync_bwlimit_mb integer NOT NULL DEFAULT 0 CHECK (sync_bwlimit_mb >= 0);

-- De uitkomst per doel van een sync-run: [{node_id, hostname, result,
-- exit, files, bytes, deleted, error}].
ALTER TABLE cron_runs ADD COLUMN sync jsonb NOT NULL DEFAULT '[]';

-- Per node in een sync: de publieke sleutel (de geheime blijft op de
-- node), de hostsleutels en wat het plan nodig heeft.
CREATE TABLE cron_sync_nodes (
    node_id           uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    public_key        text NOT NULL DEFAULT '',
    host_keys         text[] NOT NULL DEFAULT '{}',
    rsync             boolean NOT NULL DEFAULT false,
    ssh               boolean NOT NULL DEFAULT false,
    sshd              boolean NOT NULL DEFAULT false,
    permit_root_login text NOT NULL DEFAULT '',
    error             text NOT NULL DEFAULT '',
    checked_at        timestamptz NOT NULL
);

-- +goose Down
DROP TABLE cron_sync_nodes;
ALTER TABLE cron_runs DROP COLUMN sync;
DELETE FROM cron_jobs WHERE kind = 'sync';
ALTER TABLE cron_jobs DROP COLUMN sync_bwlimit_mb;
ALTER TABLE cron_jobs DROP COLUMN sync_exclude;
ALTER TABLE cron_jobs DROP COLUMN sync_delete;
ALTER TABLE cron_jobs DROP COLUMN sync_to_cluster;
ALTER TABLE cron_jobs DROP COLUMN sync_target;
ALTER TABLE cron_jobs DROP COLUMN sync_source;
ALTER TABLE cron_jobs DROP CONSTRAINT cron_jobs_kind_check;
ALTER TABLE cron_jobs ADD CONSTRAINT cron_jobs_kind_check CHECK (kind IN ('command'));
