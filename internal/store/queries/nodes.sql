-- name: ListNodes :many
SELECT sqlc.embed(n), c.slug AS cluster_slug, c.name AS cluster_name,
       a.id AS agent_id, a.version AS agent_version, a.enrolled_at AS agent_enrolled_at,
       a.last_seen_at AS agent_last_seen_at, a.protocol_version AS agent_protocol,
       pc.name AS proxmox_name, r.type AS pve_type, r.pve_node, r.name AS pve_name, r.status AS pve_status,
       r.data AS pve_data
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN proxmox_connections pc ON pc.id = n.proxmox_id
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
ORDER BY lower(n.hostname);

-- name: ListNodesByCluster :many
SELECT sqlc.embed(n), c.slug AS cluster_slug, c.name AS cluster_name,
       a.id AS agent_id, a.version AS agent_version, a.enrolled_at AS agent_enrolled_at,
       a.last_seen_at AS agent_last_seen_at, a.protocol_version AS agent_protocol,
       pc.name AS proxmox_name, r.type AS pve_type, r.pve_node, r.name AS pve_name, r.status AS pve_status,
       r.data AS pve_data
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN proxmox_connections pc ON pc.id = n.proxmox_id
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
WHERE n.cluster_id = $1
ORDER BY lower(n.hostname);

-- name: GetNode :one
SELECT sqlc.embed(n), c.slug AS cluster_slug, c.name AS cluster_name,
       a.id AS agent_id, a.version AS agent_version, a.enrolled_at AS agent_enrolled_at,
       a.last_seen_at AS agent_last_seen_at, a.protocol_version AS agent_protocol,
       pc.name AS proxmox_name, r.type AS pve_type, r.pve_node, r.name AS pve_name, r.status AS pve_status,
       r.data AS pve_data
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN proxmox_connections pc ON pc.id = n.proxmox_id
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
WHERE n.id = $1;

-- name: CreateNode :one
INSERT INTO nodes (cluster_id, hostname, role, description, lifecycle, primary_ip, tags, proxmox_id, pve_vmid)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: UpdateNode :one
UPDATE nodes
SET cluster_id = $2, hostname = $3, role = $4, description = $5, lifecycle = $6,
    primary_ip = $7, tags = $8, proxmox_id = $9, pve_vmid = $10, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteNode :execrows
DELETE FROM nodes WHERE id = $1;

-- name: LockNode :one
SELECT * FROM nodes WHERE id = $1 FOR UPDATE;
