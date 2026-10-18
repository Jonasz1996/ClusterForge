-- +goose Up
-- Zelfherstel vanzelf (fase 7, mijlpaal 3): per cluster aan te zetten,
-- standaard uit en nooit op prod. Na drie mislukte pogingen op rij zet
-- ClusterForge het op pauze tot een beheerder het opnieuw aanzet.
CREATE TABLE heal_auto (
    cluster_id    uuid PRIMARY KEY REFERENCES clusters (id) ON DELETE CASCADE,
    enabled       boolean NOT NULL DEFAULT false,
    -- enabled_at is wanneer het laatst aangezet werd; de pogingen daarvoor
    -- tellen niet mee voor de pauze.
    enabled_at    timestamptz,
    paused_at     timestamptz,
    paused_reason text NOT NULL DEFAULT '',
    updated_at    timestamptz NOT NULL DEFAULT now(),
    updated_by    uuid REFERENCES users (id) ON DELETE SET NULL
);

-- De pogingen van heal.run per cluster tellen.
CREATE INDEX jobs_heal_idx ON jobs (cluster_id, created_at DESC) WHERE kind = 'heal.run';

-- +goose Down
DROP INDEX jobs_heal_idx;
DROP TABLE heal_auto;
