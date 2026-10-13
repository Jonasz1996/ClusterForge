-- +goose Up
-- De failovertest 'alles herstarten' (fase 5, mijlpaal 13) verwacht dat de
-- database binnen minuten weer gezond is, niet binnen seconden: tot een
-- half uur. De andere scenario's blijven tot 120 s; dat controleert de
-- applicatie.
ALTER TABLE failover_tests
    DROP CONSTRAINT failover_tests_max_takeover_seconds_check,
    ADD CONSTRAINT failover_tests_max_takeover_seconds_check CHECK (max_takeover_seconds BETWEEN 1 AND 1800);

-- +goose Down
DELETE FROM failover_tests WHERE scenario = 'restart_all';
ALTER TABLE failover_tests
    DROP CONSTRAINT failover_tests_max_takeover_seconds_check,
    ADD CONSTRAINT failover_tests_max_takeover_seconds_check CHECK (max_takeover_seconds BETWEEN 1 AND 120);
