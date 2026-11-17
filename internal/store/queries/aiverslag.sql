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
