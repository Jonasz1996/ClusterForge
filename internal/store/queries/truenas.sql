-- name: ListTrueNASAgents :many
-- Fase 37: de nodes waarvan de agent op TrueNAS draait (facts truenas),
-- met hun agent en heartbeat.
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), '')::text AS address, (f.facts -> 'truenas')::jsonb AS truenas,
       a.protocol_version AS agent_protocol, a.version AS agent_version, s.heartbeat_at
FROM nodes n
JOIN node_facts f ON f.node_id = n.id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
WHERE jsonb_typeof(f.facts -> 'truenas') = 'object'
ORDER BY n.hostname;
