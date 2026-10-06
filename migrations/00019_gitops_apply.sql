-- +goose Up
-- De spec-revisie die op alle nodes is toegepast. Loopt ze achter op
-- spec_revision, dan mislukte een toepassing; Opnieuw toepassen brengt de
-- nodes bij. Een bestaand cluster telt als toegepast, behalve als zijn
-- laatste uitrol niet lukte.
ALTER TABLE clusters ADD COLUMN applied_revision integer NOT NULL DEFAULT 0;
UPDATE clusters c SET applied_revision = spec_revision
WHERE NOT EXISTS (
    SELECT 1 FROM jobs j
    WHERE j.cluster_id = c.id AND j.kind = 'cluster.deploy' AND j.status <> 'succeeded'
);

-- De spec-revisie die een goedgekeurde wijziging maakte.
ALTER TABLE git_changes ADD COLUMN revision integer;

-- +goose Down
ALTER TABLE git_changes DROP COLUMN revision;
ALTER TABLE clusters DROP COLUMN applied_revision;
