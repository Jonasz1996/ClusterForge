-- +goose Up
-- Per node hoogstens één lopende actie (herstart, onderhoud, ...), ook als
-- twee mensen tegelijk klikken.
CREATE UNIQUE INDEX jobs_node_action_key ON jobs (node_id)
    WHERE kind = 'node.action' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX jobs_node_action_key;
