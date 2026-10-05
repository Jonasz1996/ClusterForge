-- name: CreateJob :one
INSERT INTO jobs (kind, title, params, cluster_id, node_id, proxmox_id, requested_by, cluster_slot)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: LockClusterForJob :one
-- Vergrendelt de clusterrij, zodat twee aanvragen het slot niet tegelijk
-- vrij zien.
SELECT name FROM clusters WHERE id = $1 FOR UPDATE;

-- name: GetClusterSlotJob :one
-- De wachtende of lopende schrijvende taak in een cluster, ook een taak op
-- een node van dat cluster.
SELECT j.id, j.title FROM jobs j
WHERE j.cluster_slot AND j.status IN ('queued', 'running')
  AND (j.cluster_id = @cluster_id::uuid OR j.node_id IN (SELECT n.id FROM nodes n WHERE n.cluster_id = @cluster_id::uuid))
ORDER BY j.created_at
LIMIT 1;

-- name: ClaimJob :one
-- Neemt de oudste wachtende taak; SKIP LOCKED laat meerdere workers naast
-- elkaar claimen zonder op elkaar te wachten.
UPDATE jobs
SET status = 'running', attempts = attempts + 1, started_at = coalesce(started_at, now()), heartbeat_at = now()
WHERE id = (
    SELECT j.id FROM jobs j WHERE j.status = 'queued' ORDER BY j.created_at FOR UPDATE SKIP LOCKED LIMIT 1
)
RETURNING *;

-- name: JobHeartbeat :one
UPDATE jobs SET heartbeat_at = now() WHERE id = $1 AND status = 'running' RETURNING cancel_requested;

-- name: RequeueStaleJobs :many
-- Taken waarvan de heartbeat stilstaat, horen bij een gestopte server.
UPDATE jobs SET status = 'queued'
WHERE status = 'running' AND heartbeat_at < now() - make_interval(secs => @stale_seconds::float8)
RETURNING id;

-- name: RequeueJob :exec
UPDATE jobs SET status = 'queued' WHERE id = $1 AND status = 'running';

-- name: FinishJob :one
UPDATE jobs SET status = $2, error = $3, finished_at = now()
WHERE id = $1 AND status IN ('queued', 'running')
RETURNING *;

-- name: RequestJobCancel :one
UPDATE jobs SET cancel_requested = true WHERE id = $1 AND status = 'running' RETURNING *;

-- name: GetJob :one
SELECT sqlc.embed(j), u.username AS requested_by_name
FROM jobs j LEFT JOIN users u ON u.id = j.requested_by
WHERE j.id = $1;

-- name: ListJobs :many
SELECT sqlc.embed(j), u.username AS requested_by_name
FROM jobs j LEFT JOIN users u ON u.id = j.requested_by
WHERE (sqlc.narg(node_id)::uuid IS NULL OR j.node_id = sqlc.narg(node_id))
  AND (sqlc.narg(proxmox_id)::uuid IS NULL OR j.proxmox_id = sqlc.narg(proxmox_id))
  AND (sqlc.narg(cluster_id)::uuid IS NULL OR j.cluster_id = sqlc.narg(cluster_id))
ORDER BY j.created_at DESC
LIMIT @max_rows;

-- name: ListJobSteps :many
SELECT * FROM job_steps WHERE job_id = $1 ORDER BY seq;

-- name: SaveJobStep :exec
INSERT INTO job_steps (job_id, seq, name, status, finished_at, state, log, error)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (job_id, seq) DO UPDATE
SET name = EXCLUDED.name, status = EXCLUDED.status, finished_at = EXCLUDED.finished_at,
    state = EXCLUDED.state, log = EXCLUDED.log, error = EXCLUDED.error;

-- name: CancelQueuedJob :one
UPDATE jobs SET status = 'canceled', finished_at = now() WHERE id = $1 AND status = 'queued' RETURNING *;
