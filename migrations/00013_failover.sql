-- +goose Up
-- Failovertests: per cluster een storing op de VIP-eigenaar met een
-- verwachting. scenario en service zijn tekst die de applicatie controleert,
-- zodat een nieuw scenario geen migratie vraagt. probe is
-- {"http":{"path":"/","expect":200}} of {"tcp":{"port":80}} en bevat nooit
-- een host: de host is altijd het VIP.
CREATE TABLE failover_tests (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id            uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    vip_id                uuid NOT NULL REFERENCES vips (id) ON DELETE CASCADE,
    name                  text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    scenario              text NOT NULL,
    service               text NOT NULL DEFAULT '',
    max_takeover_seconds  integer NOT NULL CHECK (max_takeover_seconds BETWEEN 1 AND 120),
    expect_failback       boolean NOT NULL,
    probe                 jsonb NOT NULL,
    created_by            uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX failover_tests_cluster ON failover_tests (cluster_id);

-- Eén rij is het rapport van één run, van een failovertest of later een
-- back-upcontrole. result is null zolang de taak loopt; of hij wacht of
-- loopt, staat in jobs. definition is de invoer bij de start, zodat een
-- latere wijziging van de test oude rapporten niet verandert.
CREATE TABLE test_runs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind          text NOT NULL,
    trigger       text NOT NULL DEFAULT 'manual' CHECK (trigger IN ('manual', 'schedule')),
    cluster_id    uuid REFERENCES clusters (id) ON DELETE CASCADE,
    test_id       uuid REFERENCES failover_tests (id) ON DELETE SET NULL,
    node_id       uuid REFERENCES nodes (id) ON DELETE SET NULL,
    hostname      text NOT NULL DEFAULT '',
    job_id        uuid REFERENCES jobs (id) ON DELETE SET NULL,
    definition    jsonb NOT NULL,
    result        text CHECK (result IN ('pass', 'warning', 'fail', 'error', 'skipped', 'canceled')),
    restored      boolean,
    summary       text NOT NULL DEFAULT '',
    checks        jsonb NOT NULL DEFAULT '[]',
    timeline      jsonb NOT NULL DEFAULT '[]',
    measurements  jsonb NOT NULL DEFAULT '{}',
    requested_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);
CREATE INDEX test_runs_test ON test_runs (test_id, created_at DESC);
CREATE INDEX test_runs_cluster ON test_runs (cluster_id, created_at DESC);
CREATE INDEX test_runs_unrestored ON test_runs (cluster_id) WHERE restored = false;

-- Het testslot: over heel ClusterForge hoogstens één invasieve test
-- tegelijk, ook als twee mensen tegelijk klikken.
CREATE UNIQUE INDEX jobs_test_slot ON jobs ((true))
    WHERE kind IN ('backup.verify', 'failover.test') AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX jobs_test_slot;
DROP TABLE test_runs;
DROP TABLE failover_tests;
