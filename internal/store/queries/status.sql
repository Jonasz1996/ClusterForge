-- name: ListNodeStatusInputs :many
-- Alles wat de statusregels per node nodig hebben.
SELECT n.id, n.hostname, n.cluster_id, n.lifecycle, n.status, n.status_reason,
       (a.id IS NOT NULL)::boolean AS has_agent,
       s.heartbeat_at,
       coalesce(s.addresses, '{}')::text[] AS addresses,
       coalesce(s.services, '{}'::jsonb)::jsonb AS services,
       coalesce(s.disk_used_ratio, 0)::float8 AS disk_used_ratio,
       coalesce(s.disk_used_mount, '')::text AS disk_used_mount,
       coalesce(jsonb_path_query_array(f.facts, '$.services[*] ? (@.enabled == "enabled").name'), '[]'::jsonb)::jsonb AS enabled_services
FROM nodes n
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id;

-- name: ListClusterStatuses :many
SELECT id, name, status, status_reason FROM clusters;

-- name: ListAllVIPs :many
SELECT id, cluster_id, address, owner_node_id FROM vips;

-- name: SetNodeStatus :exec
UPDATE nodes
SET status = @status, status_reason = @status_reason,
    status_since = CASE WHEN status = @status THEN status_since ELSE now() END
WHERE id = @id;

-- name: SetClusterStatus :exec
UPDATE clusters
SET status = @status, status_reason = @status_reason,
    status_since = CASE WHEN status = @status THEN status_since ELSE now() END
WHERE id = @id;

-- name: SetNodeDiskUsage :exec
UPDATE node_status SET disk_used_ratio = $2, disk_used_mount = $3 WHERE node_id = $1;

-- name: GetMetricLabels :one
-- Labels die de server aan de metrics van een node hangt.
SELECT n.hostname, n.cluster_id, coalesce(c.slug, '')::text AS cluster, coalesce(c.environment::text, '')::text AS environment
FROM nodes n LEFT JOIN clusters c ON c.id = n.cluster_id
WHERE n.id = $1;
