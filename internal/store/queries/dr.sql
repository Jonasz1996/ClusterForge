-- name: ListRehearsalNodes :many
-- De nodes die een DR-repetitie terugzet: elke actieve node van het
-- cluster, met zijn VM en de koppeling.
SELECT n.id, n.hostname, n.primary_ip, n.proxmox_id, n.pve_vmid, c.name AS connection_name, c.last_error,
       r.type AS guest_type, r.name AS guest_name, r.pve_node AS guest_host
FROM nodes n
LEFT JOIN proxmox_connections c ON c.id = n.proxmox_id
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
WHERE n.cluster_id = @cluster_id AND n.lifecycle = 'active'
ORDER BY n.hostname;

-- name: ListRunSandboxes :many
-- Alle sandboxes van een run; een DR-repetitie heeft er een per node.
SELECT * FROM backup_sandboxes WHERE run_id = @run_id ORDER BY created_at, vmid;

-- name: InsertRehearsalRun :one
INSERT INTO test_runs (kind, trigger, cluster_id, scenario_id, cluster_ids, definition, checks, requested_by)
VALUES ('dr.rehearsal', @trigger, @cluster_id, @scenario_id, ARRAY[@cluster_id::uuid], @definition, @checks, @requested_by)
RETURNING *;

-- name: SetClusterRecoveryTargets :one
UPDATE clusters
SET rto_target_minutes = @rto_target_minutes, rpo_target_hours = @rpo_target_hours, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: ListDueRehearsals :many
-- Geplande DR-repetities die aan de beurt zijn.
SELECT sqlc.embed(s), c.name AS cluster_name
FROM disaster_scenarios s
JOIN clusters c ON c.id = s.cluster_id
WHERE s.kind = 'dr_rehearsal' AND s.next_run_at IS NOT NULL AND s.next_run_at <= @now::timestamptz
ORDER BY s.next_run_at, s.id;

-- name: SetScenarioNextRun :exec
UPDATE disaster_scenarios SET next_run_at = @next_run_at WHERE id = @id;

-- name: ListClusterVIPOwners :many
-- Wie nu elk VIP van een cluster heeft, voor de bewaking tijdens een
-- DR-repetitie.
SELECT address, owner_node_id FROM vips WHERE cluster_id = @cluster_id ORDER BY address;

-- name: SetTestRunDefinition :exec
-- De DR-repetitie legt bij de voorcontrole vast welke back-ups ze echt
-- terugzet.
UPDATE test_runs SET definition = @definition WHERE id = @id AND result IS NULL;
