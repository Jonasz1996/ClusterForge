-- name: ListAIVAgents :many
-- Fase 38: de nodes waarop AI-verslag staat (facts aiverslag), met hun
-- agent en heartbeat.
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), '')::text AS address, (f.facts -> 'aiverslag')::jsonb AS aiverslag,
       a.protocol_version AS agent_protocol, a.version AS agent_version, s.heartbeat_at
FROM nodes n
JOIN node_facts f ON f.node_id = n.id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
WHERE jsonb_typeof(f.facts -> 'aiverslag') = 'object'
ORDER BY n.hostname;

-- name: AIVContextNodes :many
-- Fase 38 M4: de servers die AI-verslag als context krijgt.
SELECT n.hostname, n.role, n.lifecycle::text AS lifecycle, coalesce(host(n.primary_ip), '')::text AS address,
       coalesce(c.name, '')::text AS cluster_name, coalesce(c.environment::text, '')::text AS environment
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
WHERE n.lifecycle <> 'decommissioned'
ORDER BY lower(n.hostname);

-- name: AIVContextVIPs :many
SELECT host(v.address)::text AS address, c.name AS cluster_name, c.environment::text AS environment,
       coalesce(o.hostname, '')::text AS owner
FROM vips v
JOIN clusters c ON c.id = v.cluster_id
LEFT JOIN nodes o ON o.id = v.owner_node_id
ORDER BY v.address;

-- name: AIVContextChanges :many
-- De wijzigingen van de laatste dag per server (journaal), nieuwste eerst.
SELECT ch.ts, ch.source, ch.summary, n.hostname, coalesce(c.name, '')::text AS cluster_name
FROM node_changes ch
JOIN nodes n ON n.id = ch.node_id
LEFT JOIN clusters c ON c.id = ch.cluster_id
WHERE ch.ts > now() - interval '24 hours'
ORDER BY ch.ts DESC
LIMIT 300;
