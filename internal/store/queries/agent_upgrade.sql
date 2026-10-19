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
SELECT coalesce((SELECT auto FROM agent_upgrade_settings LIMIT 1), true)::boolean AS auto;

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
