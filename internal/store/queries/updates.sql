-- name: ListUpdateNodes :many
-- Elke node met een agent, met zijn facts, voor de pagina Updates (fase 10).
SELECT n.id, n.hostname, n.cluster_id, n.lifecycle,
       coalesce(c.name, '')::text AS cluster_name, coalesce(c.slug, '')::text AS cluster_slug,
       coalesce(c.environment::text, '')::text AS cluster_environment,
       a.protocol_version, s.heartbeat_at,
       coalesce(s.addresses, '{}')::text[] AS addresses,
       f.facts, f.collected_at,
       EXISTS (SELECT 1 FROM cluster_components cc WHERE cc.cluster_id = n.cluster_id)::boolean AS has_database
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname);
