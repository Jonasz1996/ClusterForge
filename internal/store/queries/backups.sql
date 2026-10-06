-- name: ListBackupStorages :many
-- Storages die back-ups mogen bevatten, met de host waarop ze staan en of
-- die host online is. Gedeelde storage staat er per host in.
SELECT s.pve_node, s.name AS storage, coalesce((s.data->>'shared')::int, 0) = 1 AS shared,
       coalesce(h.status, '') = 'online' AS host_online
FROM proxmox_resources s
LEFT JOIN proxmox_resources h ON h.connection_id = s.connection_id AND h.type = 'node' AND h.pve_node = s.pve_node
WHERE s.connection_id = $1 AND s.type = 'storage'
  AND 'backup' = ANY (string_to_array(coalesce(s.data->>'content', ''), ','))
  AND coalesce(s.status, '') IN ('', 'available')
ORDER BY s.name, s.pve_node;

-- name: UpsertProxmoxBackup :batchexec
INSERT INTO proxmox_backups (connection_id, volid, storage, pve_node, vmid, guest_type, ctime, size_bytes, format,
                             notes, protected, verify_state, synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (connection_id, volid) DO UPDATE
SET storage = EXCLUDED.storage, pve_node = EXCLUDED.pve_node, vmid = EXCLUDED.vmid, guest_type = EXCLUDED.guest_type,
    ctime = EXCLUDED.ctime, size_bytes = EXCLUDED.size_bytes, format = EXCLUDED.format, notes = EXCLUDED.notes,
    protected = EXCLUDED.protected, verify_state = EXCLUDED.verify_state, synced_at = EXCLUDED.synced_at;

-- name: DeleteStaleProxmoxBackups :exec
-- Verwijdert back-ups die niet meer in de lijst stonden, behalve op storages
-- die deze keer niet te lezen waren: keep bevat "storage" (gedeeld) of
-- "storage@host".
DELETE FROM proxmox_backups
WHERE connection_id = @connection_id AND synced_at < @synced_at
  AND NOT (storage = ANY (@keep::text[]) OR storage || '@' || pve_node = ANY (@keep::text[]));

-- name: SetBackupInventoryResult :exec
UPDATE proxmox_connections
SET backup_checked_at = now(), backup_error = @error::text,
    not_backed_up = CASE WHEN @error::text = '' THEN sqlc.narg('not_backed_up')::jsonb ELSE not_backed_up END
WHERE id = @id;

-- name: CountBackupTargets :one
-- Hoeveel VM's van een koppeling we bewaken: gekoppelde nodes en de lijst
-- "ook bewaken".
SELECT (SELECT count(*) FROM nodes WHERE proxmox_id = @id::uuid)
     + (SELECT count(*) FROM backup_watch WHERE connection_id = @id::uuid) AS count;

-- name: ListBackupTargets :many
-- Elke bewaakte VM met zijn node, cluster, maximale leeftijd en het aantal
-- back-ups.
WITH targets AS (
    SELECT n.proxmox_id AS connection_id, n.pve_vmid::integer AS vmid, false AS watched, '' AS label
    FROM nodes n WHERE n.proxmox_id IS NOT NULL
    UNION ALL
    SELECT w.connection_id, w.vmid, true, w.label
    FROM backup_watch w
    WHERE NOT EXISTS (SELECT 1 FROM nodes n WHERE n.proxmox_id = w.connection_id AND n.pve_vmid = w.vmid)
)
SELECT c.id AS connection_id, t.vmid::integer AS vmid, t.watched::boolean AS watched, t.label::text AS label,
       c.name AS connection_name, coalesce(bp.max_age_hours, 30)::integer AS max_age_hours,
       c.backup_checked_at, c.backup_error, c.last_error,
       n.id AS node_id, n.hostname AS node_hostname, n.cluster_id, cl.name AS cluster_name,
       r.name AS guest_name, r.type AS guest_type,
       (SELECT count(*) FROM proxmox_backups x WHERE x.connection_id = t.connection_id AND x.vmid = t.vmid)::integer
           AS backup_count,
       s.freshness AS last_freshness, s.changed_at AS freshness_since
FROM targets t
JOIN proxmox_connections c ON c.id = t.connection_id
LEFT JOIN nodes n ON n.proxmox_id = t.connection_id AND n.pve_vmid = t.vmid
LEFT JOIN clusters cl ON cl.id = n.cluster_id
LEFT JOIN backup_policies bp ON bp.cluster_id = n.cluster_id
LEFT JOIN proxmox_resources r ON r.connection_id = t.connection_id AND r.vmid = t.vmid AND r.type IN ('qemu', 'lxc')
LEFT JOIN backup_status s ON s.connection_id = t.connection_id AND s.vmid = t.vmid
ORDER BY t.watched, cl.name NULLS LAST, n.hostname NULLS LAST, c.name, t.vmid;

-- name: ListLatestBackups :many
-- De nieuwste back-up per VM.
SELECT DISTINCT ON (connection_id, vmid) *
FROM proxmox_backups
ORDER BY connection_id, vmid, ctime DESC;

-- name: ListBackupStatus :many
SELECT * FROM backup_status;

-- name: InsertBackupStatus :exec
INSERT INTO backup_status (connection_id, vmid, freshness, latest_backup_at)
VALUES ($1, $2, $3, $4);

-- name: UpdateBackupStatus :exec
UPDATE backup_status
SET freshness = $3, latest_backup_at = $4,
    changed_at = CASE WHEN freshness = $3 THEN changed_at ELSE now() END
WHERE connection_id = $1 AND vmid = $2;

-- name: DeleteBackupStatus :exec
DELETE FROM backup_status WHERE connection_id = $1 AND vmid = $2;

-- name: ListGuestBackups :many
SELECT * FROM proxmox_backups
WHERE connection_id = $1 AND vmid = $2
ORDER BY ctime DESC;

-- name: ListBackupConnections :many
SELECT id, name, last_error, backup_checked_at, backup_error, not_backed_up,
       (SELECT count(*) FROM proxmox_backups b WHERE b.connection_id = c.id)::integer AS backup_count
FROM proxmox_connections c
ORDER BY lower(name);

-- name: ListBackupWatch :many
SELECT * FROM backup_watch ORDER BY connection_id, vmid;

-- name: DeleteBackupWatch :exec
DELETE FROM backup_watch WHERE connection_id = $1;

-- name: InsertBackupWatch :exec
INSERT INTO backup_watch (connection_id, vmid, label) VALUES ($1, $2, $3);

-- name: GetBackupPolicy :one
SELECT * FROM backup_policies WHERE cluster_id = $1;

-- name: SetBackupVerifySchedule :exec
-- De back-upcontrole van een cluster gepland aan of uit.
INSERT INTO backup_policies (cluster_id, verify_enabled, next_run_at, updated_by)
VALUES (@cluster_id, @verify_enabled, @next_run_at, @updated_by)
ON CONFLICT (cluster_id) DO UPDATE
SET verify_enabled = EXCLUDED.verify_enabled, next_run_at = EXCLUDED.next_run_at,
    updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: SetBackupVerifyNextRun :exec
UPDATE backup_policies SET next_run_at = @next_run_at WHERE cluster_id = @cluster_id AND verify_enabled;

-- name: ListDueBackupVerifies :many
-- Clusters waarvan de geplande back-upcontrole aan de beurt is.
SELECT bp.cluster_id, bp.next_run_at, c.name AS cluster_name
FROM backup_policies bp
JOIN clusters c ON c.id = bp.cluster_id
WHERE bp.verify_enabled AND bp.next_run_at <= @now
ORDER BY bp.next_run_at, c.name;

-- name: ListVerifyCandidates :many
-- De nodes van een cluster die aan een VM gekoppeld zijn, de langst niet
-- gecontroleerde eerst.
SELECT n.id, n.hostname,
       coalesce((SELECT max(r.created_at) FROM test_runs r
                 WHERE r.node_id = n.id AND r.kind = 'backup.verify' AND r.result IN ('pass', 'warning', 'fail')),
                'epoch'::timestamptz)::timestamptz AS last_verified_at
FROM nodes n
WHERE n.cluster_id = @cluster_id AND n.proxmox_id IS NOT NULL AND n.pve_vmid IS NOT NULL
ORDER BY last_verified_at, n.hostname;

-- name: UpsertBackupPolicy :exec
INSERT INTO backup_policies (cluster_id, max_age_hours, updated_by)
VALUES ($1, $2, $3)
ON CONFLICT (cluster_id) DO UPDATE
SET max_age_hours = EXCLUDED.max_age_hours, updated_by = EXCLUDED.updated_by, updated_at = now();
