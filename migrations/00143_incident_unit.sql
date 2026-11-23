-- +goose Up
-- Testlijst 2 (23/11), punt 2.8: een systemd-unit in een herstartlus (meer
-- dan 100 automatische herstarts) is een eigen onderwerp van een incident.
-- subject_id is een vaste uuid uit node en unit.
ALTER TABLE incident_subjects DROP CONSTRAINT incident_subjects_subject_type_check;
ALTER TABLE incident_subjects ADD CONSTRAINT incident_subjects_subject_type_check
    CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run', 'app', 'cron', 'cron_job', 'unit'));

-- +goose Down
DELETE FROM incident_subjects WHERE subject_type = 'unit';
ALTER TABLE incident_subjects DROP CONSTRAINT incident_subjects_subject_type_check;
ALTER TABLE incident_subjects ADD CONSTRAINT incident_subjects_subject_type_check
    CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run', 'app', 'cron', 'cron_job'));
