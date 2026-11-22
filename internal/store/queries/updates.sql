-- name: ListUpdateNodes :many
-- Elke node met een agent, met zijn facts, voor de pagina Updates (fase 10).
SELECT n.id, n.hostname, n.cluster_id, n.lifecycle,
       coalesce(c.name, '')::text AS cluster_name, coalesce(c.slug, '')::text AS cluster_slug,
       coalesce(c.environment::text, '')::text AS cluster_environment,
       a.protocol_version, s.heartbeat_at,
       coalesce(s.addresses, '{}')::text[] AS addresses,
       f.facts, f.collected_at,
       EXISTS (SELECT 1 FROM cluster_components cc WHERE cc.cluster_id = n.cluster_id)::boolean AS has_database
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname);

-- name: ListUpdatePolicies :many
-- Het beleid voor automatisch bijwerken, per omgeving en per cluster.
SELECT p.*, u.username AS updated_by_name
FROM update_policies p
LEFT JOIN users u ON u.id = p.updated_by
ORDER BY p.environment NULLS LAST, p.cluster_id;

-- name: UpsertEnvUpdatePolicy :one
INSERT INTO update_policies (environment, enabled, apt, apt_full, apt_autoremove, docker, docker_prune, reboot, update_window, updated_by, updated_at)
VALUES (@environment, @enabled, @apt, @apt_full, @apt_autoremove, @docker, @docker_prune, @reboot, @update_window, @updated_by, now())
ON CONFLICT (environment) DO UPDATE SET
    enabled = EXCLUDED.enabled, apt = EXCLUDED.apt, apt_full = EXCLUDED.apt_full, apt_autoremove = EXCLUDED.apt_autoremove,
    docker = EXCLUDED.docker, docker_prune = EXCLUDED.docker_prune, reboot = EXCLUDED.reboot,
    update_window = EXCLUDED.update_window, updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: UpsertClusterUpdatePolicy :one
INSERT INTO update_policies (cluster_id, enabled, apt, apt_full, apt_autoremove, docker, docker_prune, reboot, update_window, updated_by, updated_at)
VALUES (@cluster_id, @enabled, @apt, @apt_full, @apt_autoremove, @docker, @docker_prune, @reboot, @update_window, @updated_by, now())
ON CONFLICT (cluster_id) DO UPDATE SET
    enabled = EXCLUDED.enabled, apt = EXCLUDED.apt, apt_full = EXCLUDED.apt_full, apt_autoremove = EXCLUDED.apt_autoremove,
    docker = EXCLUDED.docker, docker_prune = EXCLUDED.docker_prune, reboot = EXCLUDED.reboot,
    update_window = EXCLUDED.update_window, updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: DeleteClusterUpdatePolicy :exec
DELETE FROM update_policies WHERE cluster_id = @cluster_id;

-- name: ListUpdateAutoRuns :many
SELECT * FROM update_auto_runs;

-- name: SetUpdateAutoRun :exec
INSERT INTO update_auto_runs (cluster_id, started_at, job_id, note)
VALUES (@cluster_id, @started_at, @job_id, @note)
ON CONFLICT (cluster_id) DO UPDATE SET started_at = EXCLUDED.started_at, job_id = EXCLUDED.job_id, note = EXCLUDED.note;

-- name: SaveDockerReport :exec
INSERT INTO docker_update_reports (node_id, project, job_id, result, report, created_at)
VALUES (@node_id, @project, @job_id, @result, @report, now())
ON CONFLICT (node_id, project) DO UPDATE SET
    job_id = EXCLUDED.job_id, result = EXCLUDED.result, report = EXCLUDED.report, created_at = now(), conversation_id = NULL;

-- name: ListDockerReports :many
SELECT * FROM docker_update_reports ORDER BY node_id, project;

-- name: GetDockerReport :one
SELECT * FROM docker_update_reports WHERE node_id = @node_id AND project = @project;

-- name: SetDockerReportConversation :exec
UPDATE docker_update_reports SET conversation_id = @conversation_id WHERE node_id = @node_id AND project = @project;
