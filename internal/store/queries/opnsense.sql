-- name: ListOPNsenseAgents :many
-- Fase 34: de nodes waarvan de agent op een OPNsense-firewall draait
-- (facts opnsense), met cluster, agent en heartbeat.
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), '')::text AS address, (f.facts -> 'opnsense')::jsonb AS opnsense,
       n.cluster_id, coalesce(c.name, '')::text AS cluster_name, coalesce(c.environment::text, '')::text AS cluster_environment,
       a.protocol_version AS agent_protocol, a.version AS agent_version, s.heartbeat_at
FROM nodes n
JOIN node_facts f ON f.node_id = n.id
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
WHERE jsonb_typeof(f.facts -> 'opnsense') = 'object'
ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname);
