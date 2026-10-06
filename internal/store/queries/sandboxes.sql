-- name: InsertBackupSandbox :one
-- Reserveert een VMID in het register, vóór het terugzetten.
INSERT INTO backup_sandboxes (connection_id, vmid, source_vmid, source_node_id, run_id, volid, host, storage)
VALUES (@connection_id, @vmid, @source_vmid, @source_node_id, @run_id, @volid, @host, @storage)
RETURNING *;

-- name: GetBackupSandbox :one
SELECT * FROM backup_sandboxes WHERE id = @id;

-- name: GetRunSandbox :one
-- De sandbox van een run; een run heeft er hoogstens één.
SELECT * FROM backup_sandboxes WHERE run_id = @run_id ORDER BY created_at DESC LIMIT 1;

-- name: SetBackupSandboxState :one
UPDATE backup_sandboxes
SET state = @state, error = @error,
    destroyed_at = CASE WHEN @state IN ('destroyed', 'none') THEN now() ELSE destroyed_at END
WHERE id = @id
RETURNING *;

-- name: ListLiveSandboxes :many
-- Sandboxes die nog kunnen bestaan, met de stand van hun run en taak, voor
-- de opruimer en de pagina Back-ups.
SELECT sqlc.embed(b), r.result AS run_result, j.status AS job_status,
       c.name AS connection_name, n.hostname AS source_hostname
FROM backup_sandboxes b
JOIN proxmox_connections c ON c.id = b.connection_id
LEFT JOIN test_runs r ON r.id = b.run_id
LEFT JOIN jobs j ON j.id = r.job_id
LEFT JOIN nodes n ON n.id = b.source_node_id
WHERE b.state IN ('reserved', 'present', 'destroy_failed')
ORDER BY b.created_at;

-- name: SandboxActiveForNode :one
-- true zolang er een sandbox van de VM van deze node kan bestaan.
SELECT EXISTS (
    SELECT 1 FROM backup_sandboxes
    WHERE source_node_id = @node_id AND state IN ('reserved', 'present', 'destroy_failed')
)::boolean;

-- name: IsSandboxGuest :one
-- true als dit VMID nu als sandbox in het register staat, of bij de laatste
-- sync in de pool cf-sandbox (proxmox.SandboxPool) zat.
SELECT (
    EXISTS (
        SELECT 1 FROM backup_sandboxes b
        WHERE b.connection_id = @connection_id AND b.vmid = @vmid AND b.state IN ('reserved', 'present', 'destroy_failed')
    ) OR EXISTS (
        SELECT 1 FROM proxmox_resources r
        WHERE r.connection_id = @connection_id AND r.vmid = @vmid AND r.type IN ('qemu', 'lxc') AND r.data->>'pool' = 'cf-sandbox'
    )
)::boolean;

-- name: GetBackup :one
SELECT * FROM proxmox_backups WHERE connection_id = @connection_id AND volid = @volid;

-- name: GetLatestGuestBackup :one
SELECT * FROM proxmox_backups
WHERE connection_id = @connection_id AND vmid = @vmid
ORDER BY ctime DESC LIMIT 1;

-- name: ListTestRunsFiltered :many
-- Runs met filters, nieuwste eerst.
SELECT sqlc.embed(r), j.status AS job_status, u.username AS requested_by_name, c.name AS cluster_name
FROM test_runs r
LEFT JOIN jobs j ON j.id = r.job_id
LEFT JOIN users u ON u.id = r.requested_by
LEFT JOIN clusters c ON c.id = r.cluster_id
WHERE (sqlc.narg('kind')::text IS NULL OR r.kind = sqlc.narg('kind'))
  AND (sqlc.narg('cluster_id')::uuid IS NULL OR r.cluster_id = sqlc.narg('cluster_id'))
  AND (sqlc.narg('node_id')::uuid IS NULL OR r.node_id = sqlc.narg('node_id'))
  AND (sqlc.narg('result')::text IS NULL OR r.result = sqlc.narg('result'))
ORDER BY r.created_at DESC
LIMIT @lim;

-- name: LatestVerifications :many
-- De laatste afgeronde back-upcontrole per node.
SELECT DISTINCT ON (r.node_id) r.node_id, r.id, r.result, r.summary, r.finished_at, r.measurements
FROM test_runs r
WHERE r.kind = 'backup.verify' AND r.node_id IS NOT NULL AND r.result IS NOT NULL
ORDER BY r.node_id, r.created_at DESC;

-- name: GetVerifyNode :one
-- Wat de back-upcontrole van een node nodig heeft.
SELECT n.id, n.hostname, n.cluster_id, n.proxmox_id, n.pve_vmid, c.name AS connection_name,
       c.last_error, r.type AS guest_type, r.name AS guest_name
FROM nodes n
LEFT JOIN proxmox_connections c ON c.id = n.proxmox_id
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
WHERE n.id = @id;

-- name: GetSandboxView :one
-- Eén sandbox met de namen erbij, voor de API.
SELECT sqlc.embed(b), r.result AS run_result, j.status AS job_status,
       c.name AS connection_name, n.hostname AS source_hostname
FROM backup_sandboxes b
JOIN proxmox_connections c ON c.id = b.connection_id
LEFT JOIN test_runs r ON r.id = b.run_id
LEFT JOIN jobs j ON j.id = r.job_id
LEFT JOIN nodes n ON n.id = b.source_node_id
WHERE b.id = @id;
