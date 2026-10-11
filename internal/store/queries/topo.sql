-- name: ListGuestConfigReadAt :many
SELECT vmid, read_at FROM proxmox_guest_config WHERE connection_id = $1;

-- name: UpsertGuestConfig :exec
INSERT INTO proxmox_guest_config (connection_id, vmid, disks, nets, onboot, addresses, error, read_at)
VALUES ($1, $2, $3, $4, $5, $6, '', $7)
ON CONFLICT (connection_id, vmid) DO UPDATE
SET disks = EXCLUDED.disks, nets = EXCLUDED.nets, onboot = EXCLUDED.onboot, addresses = EXCLUDED.addresses,
    error = '', read_at = EXCLUDED.read_at;

-- name: SetGuestConfigError :exec
-- Een mislukte lezing laat de vorige gegevens staan.
INSERT INTO proxmox_guest_config (connection_id, vmid, error, read_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (connection_id, vmid) DO UPDATE SET error = EXCLUDED.error, read_at = EXCLUDED.read_at;

-- name: DeleteStaleGuestConfig :exec
-- Configuraties van VM's die Proxmox niet meer kent.
DELETE FROM proxmox_guest_config c
WHERE c.connection_id = $1
  AND NOT EXISTS (SELECT 1 FROM proxmox_resources r
                  WHERE r.connection_id = c.connection_id AND r.vmid = c.vmid AND r.type IN ('qemu', 'lxc'));

-- name: ListTopoConnections :many
SELECT id, name, last_sync_at, last_error FROM proxmox_connections ORDER BY lower(name), id;

-- name: ListTopoResources :many
SELECT r.connection_id, r.type, r.pve_node, r.vmid, r.name, r.status, r.template, r.data,
       n.id AS node_id, n.hostname AS node_hostname
FROM proxmox_resources r
LEFT JOIN nodes n ON n.proxmox_id = r.connection_id AND n.pve_vmid = r.vmid AND r.type IN ('qemu', 'lxc')
ORDER BY r.connection_id, r.type, r.pve_node, r.vmid NULLS FIRST, r.name;

-- name: ListGuestConfigs :many
SELECT * FROM proxmox_guest_config ORDER BY connection_id, vmid;

-- name: GetSelfLocation :one
SELECT * FROM self_location;

-- name: SetSelfLocation :exec
INSERT INTO self_location (singleton, connection_id, vmid, updated_by)
VALUES (true, $1, $2, $3)
ON CONFLICT (singleton) DO UPDATE
SET connection_id = EXCLUDED.connection_id, vmid = EXCLUDED.vmid, updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: DeleteSelfLocation :execrows
DELETE FROM self_location;

-- name: ListGitServerURLs :many
SELECT server_url FROM git_repos WHERE server_url <> '';

-- name: GetGuestBackupState :one
-- Of de back-ups van een koppeling te lezen zijn, en de nieuwste van één VM.
SELECT c.name, c.backup_checked_at, c.backup_error,
       coalesce((SELECT max(b.ctime) FROM proxmox_backups b WHERE b.connection_id = c.id AND b.vmid = @vmid::integer),
                'epoch')::timestamptz AS latest
FROM proxmox_connections c
WHERE c.id = @connection_id;
