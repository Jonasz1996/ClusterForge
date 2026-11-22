-- +goose Up
-- Fase 43: een nmap-rapport van buiten (zoals de Kali-VM) inlezen als
-- uitvoering zonder eigen scan.
ALTER TABLE security_scan_runs ALTER COLUMN scan_id DROP NOT NULL;
ALTER TABLE security_scan_runs DROP CONSTRAINT security_scan_runs_trigger_check;
ALTER TABLE security_scan_runs ADD CONSTRAINT security_scan_runs_trigger_check CHECK (trigger IN ('schema', 'hand', 'import'));

-- +goose Down
DELETE FROM security_scan_runs WHERE scan_id IS NULL;
ALTER TABLE security_scan_runs DROP CONSTRAINT security_scan_runs_trigger_check;
ALTER TABLE security_scan_runs ADD CONSTRAINT security_scan_runs_trigger_check CHECK (trigger IN ('schema', 'hand'));
ALTER TABLE security_scan_runs ALTER COLUMN scan_id SET NOT NULL;
