-- +goose Up
-- Rampoefeningen: per cluster een scenario dat met opzet meer tegelijk laat
-- uitvallen dan een failovertest, alleen in lab en test. kind is tekst die
-- de applicatie controleert (host_down, service_down, vip_loss), zodat de
-- reeksen en de DR-repetitie van later geen migratie vragen. params is
-- {"connection_id": ..., "host": "pve1"} of {"unit": "mariadb"}; expect is
-- {"fault_seconds": 60, "recovery_seconds": 120}.
CREATE TABLE disaster_scenarios (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id  uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    kind        text NOT NULL,
    params      jsonb NOT NULL DEFAULT '{}',
    expect      jsonb NOT NULL DEFAULT '{}',
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX disaster_scenarios_cluster ON disaster_scenarios (cluster_id);

-- Een rampoefening raakt meer dan één cluster. cluster_ids zijn alle
-- clusters die ze meet: die met een node die de storing krijgt en die
-- ervan afhangen. Zo staat de rode balk en de testrun bij elk ervan.
ALTER TABLE test_runs ADD COLUMN scenario_id uuid REFERENCES disaster_scenarios (id) ON DELETE SET NULL;
ALTER TABLE test_runs ADD COLUMN cluster_ids uuid[] NOT NULL DEFAULT '{}';
CREATE INDEX test_runs_scenario ON test_runs (scenario_id, created_at DESC);
CREATE INDEX test_runs_cluster_ids ON test_runs USING gin (cluster_ids);

-- Een taak kan het slot van meer clusters houden dan zijn eigen cluster_id.
ALTER TABLE jobs ADD COLUMN slot_clusters uuid[] NOT NULL DEFAULT '{}';

-- Het testslot telt rampoefeningen mee.
DROP INDEX jobs_test_slot;
CREATE UNIQUE INDEX jobs_test_slot ON jobs ((true))
    WHERE kind IN ('backup.verify', 'failover.test', 'disaster.run') AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX jobs_test_slot;
CREATE UNIQUE INDEX jobs_test_slot ON jobs ((true))
    WHERE kind IN ('backup.verify', 'failover.test') AND status IN ('queued', 'running');
ALTER TABLE jobs DROP COLUMN slot_clusters;
DROP INDEX test_runs_cluster_ids;
DROP INDEX test_runs_scenario;
ALTER TABLE test_runs DROP COLUMN cluster_ids;
ALTER TABLE test_runs DROP COLUMN scenario_id;
DROP TABLE disaster_scenarios;
