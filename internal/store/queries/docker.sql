-- name: ListDockerHosts :many
-- Fase 28: de nodes met Docker (facts docker), met hun cluster, agent,
-- heartbeat, of ze een VIP hebben, en de laatste samenvatting.
SELECT n.id, n.hostname, n.cluster_id,
       coalesce(c.name, '')::text AS cluster_name,
       coalesce(c.environment::text, '')::text AS environment,
       coalesce(f.facts -> 'docker' ->> 'version', '')::text AS docker_version,
       a.protocol_version AS agent_protocol, a.version AS agent_version, s.heartbeat_at,
       EXISTS (SELECT 1 FROM vips v WHERE v.owner_node_id = n.id)::boolean AS vip_holder,
       d.summary, d.error AS check_error, d.checked_at
FROM nodes n
JOIN node_facts f ON f.node_id = n.id
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN docker_hosts d ON d.node_id = n.id
WHERE jsonb_typeof(f.facts -> 'docker') = 'object'
ORDER BY coalesce(c.name, ''), n.hostname;

-- name: UpsertDockerHost :exec
INSERT INTO docker_hosts (node_id, summary, error, checked_at)
VALUES (@node_id, @summary, @error, now())
ON CONFLICT (node_id) DO UPDATE
SET summary = CASE WHEN EXCLUDED.error = '' THEN EXCLUDED.summary ELSE docker_hosts.summary END,
    error = EXCLUDED.error, checked_at = now();

-- name: ListDockerManagedStacks :many
-- De stacks die ClusterForge beheert (fase 21), per cluster: hun containers
-- worden via de stack gestopt of verwijderd, niet los.
SELECT id, cluster_id, name FROM docker_stacks ORDER BY name;
