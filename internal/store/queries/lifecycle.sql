-- name: GetNodeRuntime :one
-- Wat een actie op een node over die node moet weten.
SELECT n.id, n.hostname, n.cluster_id, n.lifecycle,
       (a.id IS NOT NULL)::boolean AS has_agent,
       coalesce(a.protocol_version, 0)::int AS agent_protocol,
       s.heartbeat_at,
       coalesce(s.uptime_seconds, 0)::bigint AS uptime_seconds,
       coalesce(s.addresses, '{}')::text[] AS addresses,
       coalesce(s.services, '{}'::jsonb)::jsonb AS services
FROM nodes n
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
WHERE n.id = @id;

-- name: ListClusterPeers :many
-- De andere nodes van een cluster, om te zien wie een VIP kan overnemen.
SELECT n.id, n.hostname, n.lifecycle, s.heartbeat_at,
       coalesce(s.services, '{}'::jsonb)::jsonb AS services
FROM nodes n
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id AND a.id IS NOT NULL
WHERE n.cluster_id = @cluster_id AND n.id <> @node_id
ORDER BY n.hostname;

-- name: SetNodeLifecycle :one
UPDATE nodes n SET lifecycle = @lifecycle, updated_at = now()
FROM (SELECT o.id, o.lifecycle FROM nodes o WHERE o.id = @node_id FOR UPDATE) old
WHERE n.id = old.id
RETURNING old.lifecycle AS previous, n.hostname, n.cluster_id;

-- name: GetActiveNodeJob :one
SELECT id, title FROM jobs
WHERE node_id = @node_id AND kind = @kind AND status IN ('queued', 'running')
ORDER BY created_at LIMIT 1;
