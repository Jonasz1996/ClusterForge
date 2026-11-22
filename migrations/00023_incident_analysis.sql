-- +goose Up
-- De analyse van What Broke per incident: het eerste symptoom, de
-- waarschijnlijke oorzaken en het bereik. Ze wordt opnieuw berekend zolang
-- het incident open is en tot een uur na het sluiten; daarna ligt ze vast.
ALTER TABLE incidents
    ADD COLUMN analysis         jsonb,
    ADD COLUMN analysis_version integer,
    ADD COLUMN analyzed_at      timestamptz;

-- +goose Down
ALTER TABLE incidents DROP COLUMN analysis, DROP COLUMN analysis_version, DROP COLUMN analyzed_at;
