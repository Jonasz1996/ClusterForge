-- name: ListCronJobs :many
SELECT j.*, c.name AS cluster_name, c.slug AS cluster_slug, c.environment AS cluster_environment, n.hostname AS node_hostname
FROM cron_jobs j
JOIN clusters c ON c.id = j.cluster_id
LEFT JOIN nodes n ON n.id = j.node_id
ORDER BY lower(c.name), lower(j.name);

-- name: GetCronJob :one
SELECT j.*, c.name AS cluster_name, c.slug AS cluster_slug, c.environment AS cluster_environment, n.hostname AS node_hostname
FROM cron_jobs j
JOIN clusters c ON c.id = j.cluster_id
LEFT JOIN nodes n ON n.id = j.node_id
WHERE j.id = $1;

-- name: InsertCronJob :one
INSERT INTO cron_jobs (cluster_id, name, schedule, run_on, node_id, user_name, command, timeout_seconds, enabled, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateCronJob :one
UPDATE cron_jobs
SET name = $2, schedule = $3, run_on = $4, node_id = $5, user_name = $6, command = $7, timeout_seconds = $8, enabled = $9,
    revision = revision + 1, updated_at = now()
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
INSERT INTO cron_runs (id, job_id, node_id, revision, reason, result, started_at, ended_at, exit_code, output, output_truncated)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO UPDATE
SET result = excluded.result, ended_at = excluded.ended_at, exit_code = excluded.exit_code, output = excluded.output,
    output_truncated = excluded.output_truncated, received_at = now()
WHERE cron_runs.ended_at IS NULL AND cron_runs.node_id = excluded.node_id;

-- name: CronJobExists :one
SELECT EXISTS (SELECT 1 FROM cron_jobs WHERE id = $1)::boolean;

-- name: ListCronRunsOf :many
-- De runs van een job, nieuwste eerst, zonder de uitvoer.
SELECT r.id, r.job_id, r.node_id, n.hostname, r.revision, r.reason, r.result, r.started_at, r.ended_at, r.exit_code, r.output_truncated
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
