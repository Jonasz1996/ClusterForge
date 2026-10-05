-- name: ListFailoverTests :many
SELECT sqlc.embed(t), v.address AS vip_address, v.owner_node_id AS vip_owner_id, o.hostname AS vip_owner_hostname
FROM failover_tests t
JOIN vips v ON v.id = t.vip_id
LEFT JOIN nodes o ON o.id = v.owner_node_id
WHERE t.cluster_id = @cluster_id
ORDER BY t.created_at;

-- name: GetFailoverTest :one
SELECT sqlc.embed(t), v.address AS vip_address
FROM failover_tests t
JOIN vips v ON v.id = t.vip_id
WHERE t.id = @id;

-- name: InsertFailoverTest :one
INSERT INTO failover_tests (cluster_id, vip_id, name, scenario, service, max_takeover_seconds, expect_failback, probe, created_by)
VALUES (@cluster_id, @vip_id, @name, @scenario, @service, @max_takeover_seconds, @expect_failback, @probe, @created_by)
RETURNING *;

-- name: UpdateFailoverTest :one
UPDATE failover_tests
SET vip_id = @vip_id, name = @name, scenario = @scenario, service = @service,
    max_takeover_seconds = @max_takeover_seconds, expect_failback = @expect_failback, probe = @probe, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteFailoverTest :execrows
DELETE FROM failover_tests WHERE id = @id;

-- name: InsertTestRun :one
INSERT INTO test_runs (kind, cluster_id, test_id, node_id, hostname, job_id, definition, checks, requested_by)
VALUES (@kind, @cluster_id, @test_id, @node_id, @hostname, @job_id, @definition, @checks, @requested_by)
RETURNING *;

-- name: GetTestRun :one
SELECT sqlc.embed(r), j.status AS job_status, u.username AS requested_by_name, c.name AS cluster_name
FROM test_runs r
LEFT JOIN jobs j ON j.id = r.job_id
LEFT JOIN users u ON u.id = r.requested_by
LEFT JOIN clusters c ON c.id = r.cluster_id
WHERE r.id = @id;

-- name: GetTestRunByJob :one
SELECT * FROM test_runs WHERE job_id = @job_id;

-- name: ListTestRuns :many
-- De runs van één test, nieuwste eerst.
SELECT sqlc.embed(r), j.status AS job_status, u.username AS requested_by_name
FROM test_runs r
LEFT JOIN jobs j ON j.id = r.job_id
LEFT JOIN users u ON u.id = r.requested_by
WHERE r.test_id = @test_id
ORDER BY r.created_at DESC
LIMIT @lim;

-- name: LatestTestRuns :many
-- De laatste run per test van een cluster.
SELECT DISTINCT ON (r.test_id) sqlc.embed(r), j.status AS job_status
FROM test_runs r
LEFT JOIN jobs j ON j.id = r.job_id
WHERE r.cluster_id = @cluster_id AND r.test_id IS NOT NULL
ORDER BY r.test_id, r.created_at DESC;

-- name: ListUnrestoredRuns :many
-- Runs waarvan het herstel niet lukte; ze geven een rode balk bij het
-- cluster tot iemand ze opnieuw herstelt.
SELECT * FROM test_runs
WHERE cluster_id = @cluster_id AND restored = false
ORDER BY created_at DESC;

-- name: SetTestRunJob :exec
UPDATE test_runs SET job_id = @job_id WHERE id = @id;

-- name: FinishTestRun :one
-- Rondt een run af; een run met een resultaat blijft zoals hij is.
UPDATE test_runs
SET result = @result, restored = @restored, summary = @summary, checks = @checks,
    timeline = @timeline, measurements = @measurements, finished_at = now()
WHERE id = @id AND result IS NULL
RETURNING *;

-- name: SetTestRunRestored :exec
UPDATE test_runs SET restored = true, summary = @summary WHERE id = @id;

-- name: ListFailoverNodes :many
-- Wat de voorcontrole van een failovertest per node moet weten, uit de
-- laatste heartbeat en facts.
SELECT n.id, n.hostname, n.lifecycle, n.status,
       (a.id IS NOT NULL)::boolean AS has_agent,
       coalesce(a.protocol_version, 0)::int AS agent_protocol,
       s.heartbeat_at,
       coalesce(s.addresses, '{}')::text[] AS addresses,
       coalesce(s.services, '{}'::jsonb)::jsonb AS services,
       coalesce(f.facts -> 'keepalived' -> 'vips', '[]'::jsonb)::jsonb AS keepalived_vips
FROM nodes n
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id AND a.id IS NOT NULL
LEFT JOIN node_facts f ON f.node_id = n.id AND a.id IS NOT NULL
WHERE n.cluster_id = @cluster_id
ORDER BY n.hostname;

-- name: GetTestSlotJob :one
-- De invasieve test die nu wacht of loopt, over heel ClusterForge.
SELECT id, title FROM jobs
WHERE kind IN ('backup.verify', 'failover.test') AND status IN ('queued', 'running')
ORDER BY created_at LIMIT 1;

-- name: SetTestRunChecks :exec
UPDATE test_runs SET checks = @checks WHERE id = @id;
