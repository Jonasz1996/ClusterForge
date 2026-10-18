-- name: ListCronCandidateNodes :many
-- Elke node met een agent die niet ingetrokken is.
SELECT n.id, n.hostname, n.cluster_id, a.protocol_version AS agent_protocol, s.heartbeat_at,
       (r.node_id IS NOT NULL)::boolean AS read_before
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_cron_reads r ON r.node_id = n.id AND r.ok_at IS NOT NULL
ORDER BY lower(n.hostname);

-- name: UpsertCronRead :exec
INSERT INTO node_cron_reads (node_id, daemon, logging, journal, truncated, error, error_kind, synced_at, ok_at)
VALUES ($1, $2, $3, $4, $5, '', '', $6, $6)
ON CONFLICT (node_id) DO UPDATE
SET daemon = excluded.daemon, logging = excluded.logging, journal = excluded.journal, truncated = excluded.truncated,
    error = '', error_kind = '', synced_at = excluded.synced_at, ok_at = excluded.ok_at;

-- name: MarkCronRead :exec
-- ClusterForge kon het de agent niet vragen: de laatst bekende jobs blijven.
INSERT INTO node_cron_reads (node_id, error, error_kind, synced_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (node_id) DO UPDATE
SET error = excluded.error, error_kind = excluded.error_kind, synced_at = excluded.synced_at;

-- name: ListNodeCronsOf :many
SELECT * FROM node_crons WHERE node_id = $1;

-- name: DeleteNodeCrons :exec
DELETE FROM node_crons WHERE node_id = $1;

-- name: InsertNodeCron :exec
INSERT INTO node_crons (node_id, key, source, file, line, user_name, schedule, command, unit, description, package, enabled,
                        clusterforge, last_run_at, last_end_at, last_exit, last_result, runs, failures, next_run_at, first_seen, subject_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22);

-- name: ListNodeCrons :many
-- Alle gevonden cronjobs met hun node en cluster, voor het menu Cronjobs.
SELECT j.*, n.hostname, n.cluster_id, c.name AS cluster_name, c.slug AS cluster_slug, c.environment AS cluster_environment
FROM node_crons j
JOIN nodes n ON n.id = j.node_id
LEFT JOIN clusters c ON c.id = n.cluster_id
ORDER BY lower(coalesce(c.name, n.hostname)), j.source, j.file, j.line, lower(n.hostname);

-- name: ListCronReads :many
SELECT r.*, n.hostname, n.cluster_id, c.name AS cluster_name, c.slug AS cluster_slug, c.environment AS cluster_environment
FROM node_cron_reads r
JOIN nodes n ON n.id = r.node_id
LEFT JOIN clusters c ON c.id = n.cluster_id
ORDER BY lower(n.hostname);

-- name: ListCronComponents :many
-- De onderdelen Database per cluster, voor de waarschuwingen bij cronjobs.
SELECT cluster_id, engine FROM cluster_components;

-- name: ListIncidentCrons :many
-- De cronjobs voor de incidenten: een job waarvan de laatste run mislukte,
-- opent er een. Jobs van Debian-pakketten (zoals e2scrub in een CT) tellen
-- niet mee.
SELECT j.node_id, j.key, j.subject_id, j.file, j.command, j.schedule, j.last_run_at, j.last_exit, j.last_result, n.hostname, n.cluster_id,
       (j.last_result = 'failed' AND j.enabled AND n.lifecycle = 'active')::boolean AS failing
FROM node_crons j
JOIN nodes n ON n.id = j.node_id
WHERE j.package = '' AND j.subject_id IS NOT NULL;

-- name: GetNodeCron :one
SELECT * FROM node_crons WHERE node_id = $1 AND key = $2;
