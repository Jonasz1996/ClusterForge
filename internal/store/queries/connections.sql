-- name: UpsertConnectionHours :batchexec
INSERT INTO observed_connection_hours (node_id, unit, direction, peer_addr, peer_port, local_port, hour, samples)
VALUES ($1, $2, $3, $4, $5, $6, date_trunc('hour', @seen::timestamptz), $7)
ON CONFLICT (node_id, unit, direction, peer_addr, peer_port, local_port, hour) DO UPDATE
SET samples = observed_connection_hours.samples + EXCLUDED.samples;

-- name: PruneConnectionHours :execrows
DELETE FROM observed_connection_hours WHERE hour < $1;

-- name: ListObservedConnections :many
-- De verbindingen die sinds @since nog gezien zijn, met hoe vaak sinds
-- @since. Met node_id alleen die van één node, met direction alleen die
-- richting.
SELECT c.node_id, c.unit, c.direction, c.peer_addr, c.peer_port, c.local_port, c.first_seen, c.last_seen,
       coalesce(h.samples, 0)::bigint AS recent
FROM observed_connections c
LEFT JOIN (
    SELECT node_id, unit, direction, peer_addr, peer_port, local_port, sum(samples)::bigint AS samples
    FROM observed_connection_hours
    WHERE hour >= date_trunc('hour', @since::timestamptz)
    GROUP BY node_id, unit, direction, peer_addr, peer_port, local_port
) h USING (node_id, unit, direction, peer_addr, peer_port, local_port)
WHERE c.last_seen >= @since
  AND (sqlc.narg(node_id)::uuid IS NULL OR c.node_id = sqlc.narg(node_id))
  AND (sqlc.narg(direction)::text IS NULL OR c.direction = sqlc.narg(direction))
ORDER BY c.node_id, recent DESC, c.peer_addr, c.peer_port, c.local_port, c.unit;

-- name: ListNodeNets :many
-- Per node de adressen en de luisterende poorten met hun unit: het
-- primaire adres, die uit de laatste heartbeat en die uit de facts.
SELECT n.id,
       coalesce(host(n.primary_ip), '')::text AS primary_ip,
       coalesce(s.addresses, '{}')::text[] AS addresses,
       coalesce(f.facts -> 'listeners', '[]'::jsonb)::jsonb AS listeners
FROM nodes n
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id;
