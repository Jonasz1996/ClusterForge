-- +goose Up
-- Een cronjob die mislukt, kan een incident openen (fase 6, mijlpaal 2).
-- subject_id is een vaste uuid uit node en sleutel, zodat het incident
-- dezelfde job terugvindt na elke keer lezen.
ALTER TABLE node_crons ADD COLUMN subject_id uuid;
CREATE INDEX node_crons_subject_idx ON node_crons (subject_id);

ALTER TABLE incident_subjects DROP CONSTRAINT incident_subjects_subject_type_check;
ALTER TABLE incident_subjects ADD CONSTRAINT incident_subjects_subject_type_check
    CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run', 'app', 'cron'));

-- +goose Down
DELETE FROM incident_subjects WHERE subject_type = 'cron';
ALTER TABLE incident_subjects DROP CONSTRAINT incident_subjects_subject_type_check;
ALTER TABLE incident_subjects ADD CONSTRAINT incident_subjects_subject_type_check
    CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run', 'app'));
DROP INDEX node_crons_subject_idx;
ALTER TABLE node_crons DROP COLUMN subject_id;
