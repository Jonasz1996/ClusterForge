-- name: ListHealNodes :many
-- De nodes met een agent, met hun adressen uit de laatste heartbeat, voor
-- Zelfherstel.
SELECT n.id, n.hostname, n.cluster_id, n.primary_ip, a.protocol_version AS agent_protocol, s.heartbeat_at,
       coalesce(s.addresses, '{}')::text[] AS addresses
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
ORDER BY lower(n.hostname);

-- name: ListVIPAddresses :many
-- Elk VIP; een node die het heeft, is daar niet zelf op te bereiken.
SELECT host(address)::text AS address FROM vips;
