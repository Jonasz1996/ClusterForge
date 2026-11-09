-- name: ListCommands :many
SELECT c.*, u.username AS updated_by_name
FROM commands c LEFT JOIN users u ON u.id = c.updated_by
ORDER BY lower(c.name);

-- name: GetCommand :one
SELECT c.*, u.username AS updated_by_name
FROM commands c LEFT JOIN users u ON u.id = c.updated_by
WHERE c.id = $1;

-- name: LockCommand :one
SELECT * FROM commands WHERE id = $1 FOR UPDATE;

-- name: InsertCommand :one
INSERT INTO commands (name, description, script, run_as, timeout_seconds, updated_by)
VALUES (@name, @description, @script, @run_as, @timeout_seconds, @updated_by)
RETURNING *;

-- name: UpdateCommand :one
UPDATE commands
SET name = @name, description = @description, script = @script, run_as = @run_as,
    timeout_seconds = @timeout_seconds, revision = @revision, updated_by = @updated_by, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteCommand :execrows
DELETE FROM commands WHERE id = $1;

-- name: InsertCommandRevision :exec
INSERT INTO command_revisions (command_id, revision, script, created_by)
VALUES (@command_id, @revision, @script, @created_by);

-- name: ListCommandRevisions :many
SELECT r.*, u.username AS created_by_name
FROM command_revisions r LEFT JOIN users u ON u.id = r.created_by
WHERE r.command_id = $1
ORDER BY r.revision DESC;

-- name: ListCommandRuns :many
SELECT sqlc.embed(j), u.username AS requested_by_name
FROM jobs j LEFT JOIN users u ON u.id = j.requested_by
WHERE j.kind = 'command.run' AND j.params->>'command_id' = @command_id::text
ORDER BY j.created_at DESC
LIMIT @max_rows;

-- name: ListLastCommandRuns :many
SELECT DISTINCT ON (j.params->>'command_id') (j.params->>'command_id')::uuid AS command_id,
       sqlc.embed(j), u.username AS requested_by_name
FROM jobs j LEFT JOIN users u ON u.id = j.requested_by
WHERE j.kind = 'command.run'
ORDER BY j.params->>'command_id', j.created_at DESC;
