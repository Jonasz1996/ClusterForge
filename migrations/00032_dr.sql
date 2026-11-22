-- +goose Up
-- De DR-repetitie: een heel cluster terugzetten uit de back-ups op een
-- afgesloten bridge. De doelen staan per cluster: leeg is de standaard van
-- de omgeving (prod 60 minuten en 26 uur, lab en test geen), 0 is geen doel.
ALTER TABLE clusters
    ADD COLUMN rto_target_minutes integer CHECK (rto_target_minutes BETWEEN 0 AND 10080),
    ADD COLUMN rpo_target_hours   integer CHECK (rpo_target_hours BETWEEN 0 AND 8760);

-- Alleen een DR-repetitie kan gepland: elke week of in het eerste
-- testvenster van de maand. next_run_at is leeg zonder planning.
ALTER TABLE disaster_scenarios
    ADD COLUMN schedule    text NOT NULL DEFAULT '' CHECK (schedule IN ('', 'weekly', 'monthly')),
    ADD COLUMN next_run_at timestamptz;
CREATE INDEX disaster_scenarios_due ON disaster_scenarios (next_run_at) WHERE next_run_at IS NOT NULL;

-- Het testslot telt de DR-repetitie mee.
DROP INDEX jobs_test_slot;
CREATE UNIQUE INDEX jobs_test_slot ON jobs ((true))
    WHERE kind IN ('backup.verify', 'failover.test', 'disaster.run', 'dr.rehearsal') AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX jobs_test_slot;
CREATE UNIQUE INDEX jobs_test_slot ON jobs ((true))
    WHERE kind IN ('backup.verify', 'failover.test', 'disaster.run') AND status IN ('queued', 'running');
DROP INDEX disaster_scenarios_due;
ALTER TABLE disaster_scenarios DROP COLUMN next_run_at, DROP COLUMN schedule;
ALTER TABLE clusters DROP COLUMN rpo_target_hours, DROP COLUMN rto_target_minutes;
