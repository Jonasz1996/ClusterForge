-- +goose Up
-- Opvolgen van beheerde cronjobs (fase 6, mijlpaal 4): herhalen na een
-- fout, een incident per job (uit te zetten), en met de hand stoppen.
ALTER TABLE cron_jobs ADD COLUMN retries integer NOT NULL DEFAULT 0 CHECK (retries BETWEEN 0 AND 3);
ALTER TABLE cron_jobs ADD COLUMN alert boolean NOT NULL DEFAULT true;

ALTER TABLE cron_runs DROP CONSTRAINT cron_runs_result_check;
ALTER TABLE cron_runs ADD CONSTRAINT cron_runs_result_check
    CHECK (result IN ('running', 'ok', 'failed', 'skipped', 'timeout', 'stopped'));

ALTER TABLE incident_subjects DROP CONSTRAINT incident_subjects_subject_type_check;
ALTER TABLE incident_subjects ADD CONSTRAINT incident_subjects_subject_type_check
    CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run', 'app', 'cron', 'cron_job'));

-- +goose Down
DELETE FROM incident_subjects WHERE subject_type = 'cron_job';
ALTER TABLE incident_subjects DROP CONSTRAINT incident_subjects_subject_type_check;
ALTER TABLE incident_subjects ADD CONSTRAINT incident_subjects_subject_type_check
    CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run', 'app', 'cron'));
UPDATE cron_runs SET result = 'failed' WHERE result = 'stopped';
ALTER TABLE cron_runs DROP CONSTRAINT cron_runs_result_check;
ALTER TABLE cron_runs ADD CONSTRAINT cron_runs_result_check
    CHECK (result IN ('running', 'ok', 'failed', 'skipped', 'timeout'));
ALTER TABLE cron_jobs DROP COLUMN alert;
ALTER TABLE cron_jobs DROP COLUMN retries;
