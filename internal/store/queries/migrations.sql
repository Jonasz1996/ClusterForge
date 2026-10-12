-- name: InsertDBMigration :one
INSERT INTO db_migrations (kind, component_id, cluster_id, source_key, source_name, database, target_database, started_at, job_id, requested_by,
    stage, force)
VALUES (@kind, @component_id, @cluster_id, @source_key, @source_name, @database, @target_database, @started_at, @job_id, @requested_by,
    @stage, @force)
RETURNING *;

-- name: SetDBMigrationRoute :exec
-- Wat de controle koos: de oude server, het adres, de importerende node,
-- en waar de app de nieuwe database vindt.
UPDATE db_migrations
SET source_node = @source_node, source_address = @source_address, target_node = @target_node, size_bytes = @size_bytes,
    target_address = @target_address
WHERE id = @id;

-- name: SetDBMigrationAccounts :exec
UPDATE db_migrations SET accounts = @accounts WHERE id = @id;

-- name: CopiedDBMigration :one
-- De kopie van een verhuizing is klaar en klopt; ze wacht tot de app op de
-- nieuwe database draait.
UPDATE db_migrations
SET stage = 'copied', copied_at = @copied_at, dump_bytes = @dump_bytes, import_ms = @import_ms, count_ms = @count_ms,
    tables = @tables, objects = @objects, differences = @differences, message = @message
WHERE id = @id AND stage = 'copy' AND status = 'running'
RETURNING *;

-- name: SwitchDBMigration :one
-- De app draait op de nieuwe database.
UPDATE db_migrations
SET stage = 'done', status = 'ok', switched_at = @switched_at, finished_at = @switched_at, locked = @locked, message = @message
WHERE id = @id AND stage = 'copied' AND status = 'running'
RETURNING *;

-- name: CancelDBMigration :one
-- De kopie is weg; de app blijft op de oude database.
UPDATE db_migrations
SET stage = 'cancelled', status = 'failed', finished_at = @finished_at, message = @message
WHERE id = @id AND stage = 'copied' AND status = 'running'
RETURNING *;

-- name: SetDBMigrationLocked :one
UPDATE db_migrations SET locked = @locked
WHERE id = @id AND stage = 'done'
RETURNING *;

-- name: GetDBMigration :one
SELECT sqlc.embed(m), c.name AS cluster_name
FROM db_migrations m
JOIN clusters c ON c.id = m.cluster_id
WHERE m.id = $1;

-- name: FinishDBMigration :one
UPDATE db_migrations
SET status = @status, finished_at = @finished_at, dump_bytes = @dump_bytes, import_ms = @import_ms, count_ms = @count_ms,
    tables = @tables, objects = @objects, differences = @differences, message = @message
WHERE id = @id
RETURNING *;

-- name: GetDBMigrationByJob :one
SELECT * FROM db_migrations WHERE job_id = $1;

-- name: ListDBMigrations :many
-- De verhuizingen en proeven van een database, de nieuwste eerst.
SELECT sqlc.embed(m), c.name AS cluster_name
FROM db_migrations m
JOIN clusters c ON c.id = m.cluster_id
WHERE m.source_key = @source_key AND m.database = @database
ORDER BY m.started_at DESC
LIMIT 10;

-- name: LatestDBMigrationTarget :one
-- De laatste proef van een onderdeel met deze nieuwe database: zo weet
-- ClusterForge dat een achtergebleven kopie van hem is.
SELECT * FROM db_migrations
WHERE component_id = @component_id AND target_database = @target_database AND id <> @exclude
ORDER BY started_at DESC
LIMIT 1;

-- name: FailRunningDBMigrations :exec
-- Een taak die stopte, laat geen lopende proef of kopie achter. Een kopie
-- die klaar is, wacht op overschakelen en blijft lopen.
UPDATE db_migrations SET status = 'failed', finished_at = now(), message = @message
WHERE job_id = @job_id AND status = 'running' AND stage IN ('', 'copy');

-- name: InsertDeployDatabase :exec
INSERT INTO deploy_databases (cluster_id, engine, job_id, requested_by) VALUES (@cluster_id, @engine, @job_id, @requested_by);

-- name: TakeDeployDatabase :one
-- De database die na de uitrol van een cluster erbij moet; één keer.
DELETE FROM deploy_databases WHERE cluster_id = $1 RETURNING *;
