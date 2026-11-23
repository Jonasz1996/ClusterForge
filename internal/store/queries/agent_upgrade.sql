-- name: ListUpgradeAgents :many
-- Elke node met een agent, met de binary die er draait, voor het bijwerken
-- van de agents.
SELECT n.id, n.hostname, n.cluster_id, n.lifecycle,
       coalesce(c.name, '')::text AS cluster_name, coalesce(c.slug, '')::text AS cluster_slug,
       coalesce(c.environment::text, '')::text AS cluster_environment,
       a.version, a.protocol_version, a.arch, a.binary_sha256, a.upgrade_rolled_back,
       s.heartbeat_at
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname);

-- name: GetAgentUpgradeSettings :one
SELECT coalesce((SELECT auto FROM agent_upgrade_settings LIMIT 1), true)::boolean AS auto,
       coalesce((SELECT prod_auto FROM agent_upgrade_settings LIMIT 1), false)::boolean AS prod_auto,
       coalesce((SELECT night_only FROM agent_upgrade_settings LIMIT 1), true)::boolean AS night_only;

-- name: SetAgentUpgradeNightOnly :exec
INSERT INTO agent_upgrade_settings (night_only, updated_by) VALUES (@night_only, @updated_by)
ON CONFLICT ((true)) DO UPDATE SET night_only = EXCLUDED.night_only, updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: SetAgentUpgradeProdAuto :exec
INSERT INTO agent_upgrade_settings (prod_auto, updated_by) VALUES (@prod_auto, @updated_by)
ON CONFLICT ((true)) DO UPDATE SET prod_auto = EXCLUDED.prod_auto, updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: ListAgentUpgradeProdQueue :many
-- De prodclusters die na een klik "ook productie" nog aan de beurt komen,
-- in volgorde.
SELECT q.cluster_id, q.job_id, q.requested_by, c.name AS cluster_name
FROM agent_upgrade_prod_queue q
JOIN clusters c ON c.id = q.cluster_id
ORDER BY q.position, c.name;

-- name: AddAgentUpgradeProdQueue :exec
INSERT INTO agent_upgrade_prod_queue (cluster_id, position, requested_by)
VALUES (@cluster_id, (SELECT coalesce(max(position), 0) + 1 FROM agent_upgrade_prod_queue), @requested_by)
ON CONFLICT (cluster_id) DO NOTHING;

-- name: SetAgentUpgradeProdQueueJob :exec
UPDATE agent_upgrade_prod_queue SET job_id = @job_id WHERE cluster_id = @cluster_id;

-- name: DeleteAgentUpgradeProdQueue :exec
DELETE FROM agent_upgrade_prod_queue WHERE cluster_id = @cluster_id;

-- name: ClearAgentUpgradeProdQueue :execrows
DELETE FROM agent_upgrade_prod_queue;

-- name: SetAgentUpgradeAuto :exec
INSERT INTO agent_upgrade_settings (auto, updated_by) VALUES (@auto, @updated_by)
ON CONFLICT ((true)) DO UPDATE SET auto = EXCLUDED.auto, updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: ListAgentUpgradeOff :many
SELECT cluster_id FROM agent_upgrade_off;

-- name: SetAgentUpgradeOff :exec
INSERT INTO agent_upgrade_off (cluster_id, updated_by) VALUES (@cluster_id, @updated_by)
ON CONFLICT (cluster_id) DO UPDATE SET updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: DeleteAgentUpgradeOff :exec
DELETE FROM agent_upgrade_off WHERE cluster_id = @cluster_id;

-- name: ListActiveAgentUpgradeJobs :many
-- De taken agents.upgrade die wachten of lopen, met hun cluster.
SELECT id, cluster_id FROM jobs
WHERE kind = 'agents.upgrade' AND status IN ('queued', 'running');

-- name: GetUpgradeAgent :one
-- De agent van één node, terwijl een update wacht tot hij terug is.
SELECT a.version, a.protocol_version, a.binary_sha256, a.upgrade_rolled_back, s.heartbeat_at
FROM agents a
LEFT JOIN node_status s ON s.node_id = a.node_id
WHERE a.node_id = @node_id AND a.revoked_at IS NULL;
