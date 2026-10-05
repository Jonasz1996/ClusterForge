-- name: ListProxmoxConnections :many
SELECT * FROM proxmox_connections ORDER BY lower(name);

-- name: GetProxmoxConnection :one
SELECT * FROM proxmox_connections WHERE id = $1;

-- name: LockProxmoxConnection :one
SELECT * FROM proxmox_connections WHERE id = $1 FOR UPDATE;

-- name: CreateProxmoxConnection :one
INSERT INTO proxmox_connections (id, name, api_url, token_id, token_secret_enc, key_id, tls_fingerprint, pve_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateProxmoxConnection :one
UPDATE proxmox_connections
SET name = $2, api_url = $3, token_id = $4, token_secret_enc = $5, key_id = $6, tls_fingerprint = $7,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteProxmoxConnection :execrows
DELETE FROM proxmox_connections WHERE id = $1;

-- name: UnlinkProxmoxNodes :exec
UPDATE nodes SET proxmox_id = NULL, pve_vmid = NULL, updated_at = now() WHERE proxmox_id = $1;

-- name: SetProxmoxSyncResult :exec
UPDATE proxmox_connections
SET last_sync_at = CASE WHEN @error::text = '' THEN now() ELSE last_sync_at END,
    last_error = @error::text,
    pve_version = CASE WHEN @pve_version::text = '' THEN pve_version ELSE @pve_version::text END
WHERE id = @id;

-- name: UpsertProxmoxResource :batchexec
INSERT INTO proxmox_resources (connection_id, pve_id, type, pve_node, vmid, name, status, template, data, synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (connection_id, pve_id) DO UPDATE
SET type = EXCLUDED.type, pve_node = EXCLUDED.pve_node, vmid = EXCLUDED.vmid, name = EXCLUDED.name,
    status = EXCLUDED.status, template = EXCLUDED.template, data = EXCLUDED.data, synced_at = EXCLUDED.synced_at;

-- name: DeleteStaleProxmoxResources :exec
DELETE FROM proxmox_resources WHERE connection_id = $1 AND synced_at < $2;

-- name: ListProxmoxResources :many
SELECT sqlc.embed(r), n.id AS node_id, n.hostname AS node_hostname
FROM proxmox_resources r
LEFT JOIN nodes n ON n.proxmox_id = r.connection_id AND n.pve_vmid = r.vmid AND r.type IN ('qemu', 'lxc')
WHERE r.connection_id = $1
ORDER BY r.type, r.vmid NULLS FIRST, r.pve_node, r.name;

-- name: GetProxmoxGuest :one
SELECT * FROM proxmox_resources
WHERE connection_id = $1 AND vmid = $2 AND type IN ('qemu', 'lxc');

-- name: ListLinkedGuests :many
-- Gekoppelde VM's van een omgeving met hun toestand van de vorige sync.
SELECT n.id AS node_id, n.hostname, n.cluster_id, n.pve_vmid::integer AS vmid,
       r.status, r.pve_node
FROM nodes n
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
WHERE n.proxmox_id = $1;

-- name: CountProxmoxResources :many
SELECT connection_id, type, count(*)::integer AS count
FROM proxmox_resources
WHERE NOT template
GROUP BY connection_id, type;

-- name: GetNodeByGuest :one
SELECT id, hostname, cluster_id FROM nodes WHERE proxmox_id = $1 AND pve_vmid = $2;
