-- +goose Up
-- Planning in het testvenster. De planning staat per failovertest en per
-- cluster voor de back-upcontrole aan of uit, standaard uit; next_run_at is
-- het begin van het volgende testvenster. Een failovertest op prod kan niet
-- gepland worden; dat controleert de applicatie.
ALTER TABLE failover_tests
    ADD COLUMN scheduled   boolean NOT NULL DEFAULT false,
    ADD COLUMN next_run_at timestamptz,
    ADD CONSTRAINT failover_tests_next_run CHECK (NOT scheduled OR next_run_at IS NOT NULL);
CREATE INDEX failover_tests_due ON failover_tests (next_run_at) WHERE scheduled;

ALTER TABLE backup_policies
    ADD COLUMN verify_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN next_run_at    timestamptz,
    ADD CONSTRAINT backup_policies_next_run CHECK (NOT verify_enabled OR next_run_at IS NOT NULL);
CREATE INDEX backup_policies_due ON backup_policies (next_run_at) WHERE verify_enabled;

-- +goose Down
DROP INDEX backup_policies_due;
ALTER TABLE backup_policies
    DROP CONSTRAINT backup_policies_next_run,
    DROP COLUMN next_run_at,
    DROP COLUMN verify_enabled;
DROP INDEX failover_tests_due;
ALTER TABLE failover_tests
    DROP CONSTRAINT failover_tests_next_run,
    DROP COLUMN next_run_at,
    DROP COLUMN scheduled;
