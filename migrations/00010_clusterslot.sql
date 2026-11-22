-- +goose Up
-- Het clusterslot: per cluster loopt hoogstens één schrijvende taak tegelijk.
-- Een taak die het slot nam, houdt het tot hij klaar is; leestaken, zoals
-- facts verversen, nemen het niet.
ALTER TABLE jobs ADD COLUMN cluster_slot boolean NOT NULL DEFAULT false;
CREATE INDEX jobs_cluster_slot_idx ON jobs (cluster_id) WHERE cluster_slot AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX jobs_cluster_slot_idx;
ALTER TABLE jobs DROP COLUMN cluster_slot;
