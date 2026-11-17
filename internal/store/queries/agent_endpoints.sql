-- name: GetAgentEndpointSettings :one
SELECT coalesce((SELECT server_url FROM agent_endpoint_settings LIMIT 1), '')::text AS server_url,
       coalesce((SELECT nats_url FROM agent_endpoint_settings LIMIT 1), '')::text AS nats_url;

-- name: SetAgentEndpointSettings :exec
INSERT INTO agent_endpoint_settings (server_url, nats_url, updated_by) VALUES (@server_url, @nats_url, @updated_by)
ON CONFLICT ((true)) DO UPDATE SET server_url = EXCLUDED.server_url, nats_url = EXCLUDED.nats_url,
    updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: ListReconfigureAgents :many
-- Elke node met een agent, met de adressen uit zijn agent.json.
SELECT n.id, n.hostname, n.cluster_id,
       coalesce(c.name, '')::text AS cluster_name, coalesce(c.slug, '')::text AS cluster_slug,
       coalesce(c.environment::text, '')::text AS cluster_environment,
       a.version, a.protocol_version, a.server_url, a.nats_url, a.reconfigure_rolled_back,
       s.heartbeat_at
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname);

-- name: GetReconfigureAgent :one
-- De agent van één node, terwijl een herconfiguratie wacht tot hij terug is.
SELECT a.server_url, a.nats_url, a.reconfigure_rolled_back, s.heartbeat_at
FROM agents a
LEFT JOIN node_status s ON s.node_id = a.node_id
WHERE a.node_id = @node_id AND a.revoked_at IS NULL;

-- name: ListActiveAgentReconfigureJobs :many
SELECT id, cluster_id FROM jobs
WHERE kind = 'agents.reconfigure' AND status IN ('queued', 'running');
