-- name: ListPVEHostAgents :many
-- Fase 17: de nodes waarvan de agent op een Proxmox-host draait (facts
-- proxmox), met hun agent en heartbeat.
SELECT n.id, n.hostname, (f.facts -> 'proxmox')::jsonb AS proxmox,
       a.protocol_version AS agent_protocol, a.version AS agent_version, s.heartbeat_at
FROM nodes n
JOIN node_facts f ON f.node_id = n.id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
WHERE jsonb_typeof(f.facts -> 'proxmox') = 'object'
ORDER BY n.hostname;

-- name: ListProxmoxHostNodes :many
-- De Proxmox-hosts per koppeling uit de laatste sync.
SELECT r.connection_id, c.name AS connection_name, r.pve_node, r.status
FROM proxmox_resources r
JOIN proxmox_connections c ON c.id = r.connection_id
WHERE r.type = 'node'
ORDER BY c.name, r.pve_node;

-- name: GuestNodeLink :one
-- De node (en zijn cluster) die bij een gast hoort, als die er is.
SELECT n.id, n.hostname, n.cluster_id, coalesce(c.environment::text, '')::text AS environment, coalesce(c.name, '')::text AS cluster_name
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
WHERE n.proxmox_id = @connection_id AND n.pve_vmid = @vmid
LIMIT 1;
