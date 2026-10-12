-- name: ListBackupComponents :many
-- De toegepaste onderdelen, voor de nachtelijke dumps.
SELECT cc.*, c.name AS cluster_name
FROM cluster_components cc
JOIN clusters c ON c.id = cc.cluster_id
WHERE cc.applied_revision IS NOT NULL
ORDER BY c.name;

-- name: SetComponentBackup :exec
UPDATE cluster_components
SET backup_on = @backup_on, backup_at = @backup_at, backup_keep_days = @backup_keep_days, backup_next_at = @next_at, updated_at = now()
WHERE id = @id;

-- name: SetComponentBackupNext :exec
UPDATE cluster_components SET backup_next_at = @next_at WHERE id = @id;

-- name: InsertDBDump :one
INSERT INTO db_dumps (component_id, cluster_id, node_id, hostname, database, file, trigger, started_at, job_id, requested_by)
VALUES (@component_id, @cluster_id, @node_id, @hostname, @database, @file, @trigger, @started_at, @job_id, @requested_by)
RETURNING *;

-- name: FinishDBDump :one
UPDATE db_dumps
SET status = @status, finished_at = @finished_at, size_bytes = @size_bytes, raw_bytes = @raw_bytes,
    tables = @tables, tables_expected = @tables_expected, message = @message
WHERE id = @id
RETURNING *;

-- name: GetDBDump :one
SELECT * FROM db_dumps WHERE id = $1;

-- name: ListDBDumps :many
-- De dumps van een onderdeel, de nieuwste eerst.
SELECT * FROM db_dumps WHERE component_id = $1 ORDER BY started_at DESC, database;

-- name: PreviousDBDump :one
-- De vorige complete dump van een database, goed of verdacht, om een
-- nieuwe mee te vergelijken. Kromp de database echt, dan is alleen de
-- eerste dump daarna verdacht.
SELECT * FROM db_dumps
WHERE component_id = @component_id AND database = @database AND status IN ('ok', 'suspect') AND id <> @exclude
ORDER BY started_at DESC
LIMIT 1;

-- name: LatestDumpHost :one
-- De node van de nieuwste goede dump van een onderdeel; de volgende komt
-- liefst op dezelfde node.
SELECT hostname FROM db_dumps WHERE component_id = $1 AND status = 'ok' ORDER BY started_at DESC LIMIT 1;

-- name: DeleteDBDump :exec
DELETE FROM db_dumps WHERE id = $1;

-- name: ActiveDumpJob :one
-- Een dump of terugzetten van een onderdeel dat wacht of loopt.
SELECT id, kind, title FROM jobs
WHERE kind IN ('component.dump', 'component.restore') AND status IN ('queued', 'running')
  AND params ->> 'component_id' = sqlc.arg('component_id')::uuid::text
ORDER BY created_at
LIMIT 1;

-- name: ListComponentDatabaseNames :many
-- De databases op de datanodes van de toegepaste onderdelen, zoals de
-- agents ze het laatst meldden.
SELECT DISTINCT cc.id AS component_id, d.name
FROM cluster_components cc
CROSS JOIN LATERAL jsonb_array_elements(cc.spec -> 'nodes') e
JOIN db_databases d ON d.node_id = (e ->> 'node_id')::uuid AND d.engine = cc.engine
WHERE cc.applied_revision IS NOT NULL AND e ->> 'role' = 'data'
ORDER BY cc.id, d.name;

-- name: ListLatestDBDumps :many
-- Per onderdeel en database de nieuwste goede dump.
SELECT DISTINCT ON (component_id, database) *
FROM db_dumps
WHERE status = 'ok'
ORDER BY component_id, database, started_at DESC;

-- name: ListLatestDBDumpAttempts :many
-- Per onderdeel en database de nieuwste dump, ook als die mislukte.
SELECT DISTINCT ON (component_id, database) *
FROM db_dumps
WHERE status <> 'running'
ORDER BY component_id, database, started_at DESC;

-- name: FailRunningDBDumps :exec
-- De dumps van een taak die stopte terwijl ze liepen.
UPDATE db_dumps SET status = 'failed', finished_at = now(), message = @message
WHERE job_id = @job_id AND status = 'running';
