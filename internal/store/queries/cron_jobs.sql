-- name: ListCronJobs :many
SELECT j.*, c.name AS cluster_name, c.slug AS cluster_slug, c.environment AS cluster_environment, n.hostname AS node_hostname,
       coalesce(tc.name, '')::text AS sync_to_cluster_name
FROM cron_jobs j
JOIN clusters c ON c.id = j.cluster_id
LEFT JOIN nodes n ON n.id = j.node_id
LEFT JOIN clusters tc ON tc.id = j.sync_to_cluster
ORDER BY lower(c.name), lower(j.name);

-- name: GetCronJob :one
SELECT j.*, c.name AS cluster_name, c.slug AS cluster_slug, c.environment AS cluster_environment, n.hostname AS node_hostname,
       coalesce(tc.name, '')::text AS sync_to_cluster_name
FROM cron_jobs j
JOIN clusters c ON c.id = j.cluster_id
LEFT JOIN nodes n ON n.id = j.node_id
LEFT JOIN clusters tc ON tc.id = j.sync_to_cluster
WHERE j.id = $1;

-- name: InsertCronJob :one
INSERT INTO cron_jobs (cluster_id, name, schedule, run_on, node_id, user_name, command, timeout_seconds, enabled, created_by, retries, alert,
                       kind, sync_source, sync_target, sync_to_cluster, sync_delete, sync_exclude, sync_bwlimit_mb)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
RETURNING *;

-- name: UpdateCronJob :one
UPDATE cron_jobs
SET name = $2, schedule = $3, run_on = $4, node_id = $5, user_name = $6, command = $7, timeout_seconds = $8, enabled = $9,
    retries = $10, alert = $11, sync_source = $12, sync_target = $13, sync_to_cluster = $14, sync_delete = $15, sync_exclude = $16,
    sync_bwlimit_mb = $17, revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteCronJob :exec
DELETE FROM cron_jobs WHERE id = $1;

-- name: ListCronVIPs :many
SELECT cluster_id, address, owner_node_id FROM vips ORDER BY address;

-- name: ListCronNodeSets :many
SELECT * FROM cron_node_sets;

-- name: UpsertCronNodeSet :exec
INSERT INTO cron_node_sets (node_id, hash, applied_at, error, error_kind, synced_at)
VALUES ($1, $2, $3, '', '', $3)
ON CONFLICT (node_id) DO UPDATE
SET hash = excluded.hash, applied_at = excluded.applied_at, error = '', error_kind = '', synced_at = excluded.synced_at;

-- name: MarkCronNodeSet :exec
INSERT INTO cron_node_sets (node_id, error, error_kind, synced_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (node_id) DO UPDATE
SET error = excluded.error, error_kind = excluded.error_kind, synced_at = excluded.synced_at;

-- name: GetCronRunState :one
SELECT result FROM cron_runs WHERE id = $1;

-- name: UpsertCronRun :exec
INSERT INTO cron_runs (id, job_id, node_id, revision, reason, result, started_at, ended_at, exit_code, output, output_truncated, sync)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO UPDATE
SET result = excluded.result, ended_at = excluded.ended_at, exit_code = excluded.exit_code, output = excluded.output,
    output_truncated = excluded.output_truncated, sync = excluded.sync, received_at = now()
WHERE cron_runs.ended_at IS NULL AND cron_runs.node_id = excluded.node_id;

-- name: CronJobExists :one
SELECT EXISTS (SELECT 1 FROM cron_jobs WHERE id = $1)::boolean;

-- name: ListCronRunsOf :many
-- De runs van een job, nieuwste eerst, zonder de uitvoer.
SELECT r.id, r.job_id, r.node_id, n.hostname, r.revision, r.reason, r.result, r.started_at, r.ended_at, r.exit_code, r.output_truncated, r.sync
FROM cron_runs r
JOIN nodes n ON n.id = r.node_id
WHERE r.job_id = $1
ORDER BY r.started_at DESC
LIMIT $2;

-- name: ListLatestCronRuns :many
-- De laatste run per job en node, voor het overzicht.
SELECT DISTINCT ON (r.job_id, r.node_id) r.id, r.job_id, r.node_id, n.hostname, r.reason, r.result, r.started_at, r.ended_at, r.exit_code
FROM cron_runs r
JOIN nodes n ON n.id = r.node_id
ORDER BY r.job_id, r.node_id, r.started_at DESC;

-- name: CountCronFailures :many
-- Mislukte runs per job in de laatste 24 uur.
SELECT job_id, count(*)::int AS failures
FROM cron_runs
WHERE started_at > now() - interval '24 hours' AND result IN ('failed', 'timeout')
GROUP BY job_id;

-- name: GetCronRun :one
SELECT r.*, n.hostname, j.name AS job_name, j.user_name, j.cluster_id
FROM cron_runs r
JOIN nodes n ON n.id = r.node_id
JOIN cron_jobs j ON j.id = r.job_id
WHERE r.id = $1;

-- name: DeleteOldCronRuns :execrows
-- Runs ouder dan negentig dagen, behalve de laatste per job en node.
DELETE FROM cron_runs r
WHERE r.started_at < now() - interval '90 days'
  AND r.id NOT IN (SELECT DISTINCT ON (job_id, node_id) id FROM cron_runs ORDER BY job_id, node_id, started_at DESC);

-- name: ListIncidentCronJobs :many
-- De laatste afgeronde run per beheerde job en node, voor de incidenten;
-- een overgeslagen of gestopte run telt niet.
SELECT DISTINCT ON (r.job_id, r.node_id) r.job_id, r.node_id, n.hostname, r.result, r.exit_code, r.started_at,
       j.name, j.cluster_id, j.alert, j.enabled
FROM cron_runs r
JOIN cron_jobs j ON j.id = r.job_id
JOIN nodes n ON n.id = r.node_id
WHERE r.result IN ('ok', 'failed', 'timeout')
ORDER BY r.job_id, r.node_id, r.started_at DESC;

-- name: ListCronJobRunStarts :many
-- De starttijden van de runs sinds @since, voor gemiste runs.
SELECT job_id, max(started_at)::timestamptz AS last_start
FROM cron_runs
WHERE started_at >= @since
GROUP BY job_id;

-- name: ListCronSyncNodes :many
SELECT * FROM cron_sync_nodes;

-- name: UpsertCronSyncNode :exec
INSERT INTO cron_sync_nodes (node_id, public_key, host_keys, rsync, ssh, sshd, permit_root_login, error, checked_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8)
ON CONFLICT (node_id) DO UPDATE
SET public_key = excluded.public_key, host_keys = excluded.host_keys, rsync = excluded.rsync, ssh = excluded.ssh, sshd = excluded.sshd,
    permit_root_login = excluded.permit_root_login, error = '', checked_at = excluded.checked_at;

-- name: MarkCronSyncNode :exec
INSERT INTO cron_sync_nodes (node_id, error, checked_at)
VALUES ($1, $2, $3)
ON CONFLICT (node_id) DO UPDATE SET error = excluded.error, checked_at = excluded.checked_at;
