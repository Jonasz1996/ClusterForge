-- name: GetGitRepo :one
SELECT * FROM git_repos LIMIT 1;

-- name: LockGitRepo :one
SELECT * FROM git_repos LIMIT 1 FOR UPDATE;

-- name: CreateGitRepo :one
INSERT INTO git_repos (id, api_url, owner, name, branch, path, token_enc, key_id)
VALUES (@id, @api_url, @owner, @name, @branch, @path, @token_enc, @key_id)
RETURNING *;

-- name: UpdateGitRepo :one
-- Een andere repository, branch of map begint opnieuw: de kop, de ETag en
-- de scan vervallen.
UPDATE git_repos
SET api_url = @api_url, owner = @owner, name = @name, branch = @branch, path = @path,
    token_enc = @token_enc, key_id = @key_id,
    head_sha = CASE WHEN @reset::boolean THEN '' ELSE head_sha END,
    head_etag = '',
    synced_sha = CASE WHEN @reset::boolean THEN '' ELSE synced_sha END,
    head_commit = CASE WHEN @reset::boolean THEN NULL ELSE head_commit END,
    scan = CASE WHEN @reset::boolean THEN '[]'::jsonb ELSE scan END,
    last_error = '', updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteGitRepo :execrows
DELETE FROM git_repos WHERE id = $1;

-- name: SetGitHead :exec
UPDATE git_repos SET head_sha = @head_sha, head_etag = @head_etag, head_commit = @head_commit WHERE id = @id;

-- name: SetGitScan :exec
UPDATE git_repos
SET synced_sha = @synced_sha, scan = @scan, last_sync_at = @last_sync_at, last_error = ''
WHERE id = @id;

-- name: SetGitError :exec
UPDATE git_repos SET last_error = @last_error, last_sync_at = @last_sync_at WHERE id = @id;

-- name: ListGitClusters :many
-- De clusters die bij een scan horen: alle clusters met hun slug, of ze
-- uit een template komen en of ze gekoppeld zijn.
SELECT id, slug, name, description, environment, tags, type, spec, spec_revision, applied_revision,
       template_name, template_version, git_repo_id
FROM clusters
ORDER BY slug;

-- name: SetClusterGit :exec
UPDATE clusters SET git_repo_id = @git_repo_id, git_repo_url = @git_repo_url, updated_at = now() WHERE id = @id;

-- name: UnlinkGitClusters :many
-- Ontkoppelt alle clusters van een repository, voor het verwijderen ervan.
UPDATE clusters SET git_repo_id = NULL, updated_at = now()
WHERE git_repo_id = @repo_id
RETURNING id, name, slug;

-- name: GetPendingGitChange :one
SELECT * FROM git_changes WHERE slug = @slug AND status = 'pending';

-- name: InsertGitChange :one
INSERT INTO git_changes (repo_id, cluster_id, slug, path, kind, commit_sha, commit_message, commit_author,
                         commit_verified, commit_url, committed_at, blob_sha, base_revision, spec, metadata, plan)
VALUES (@repo_id, @cluster_id, @slug, @path, @kind, @commit_sha, @commit_message, @commit_author,
        @commit_verified, @commit_url, @committed_at, @blob_sha, @base_revision, @spec, @metadata, @plan)
RETURNING *;

-- name: SupersedeGitChange :one
UPDATE git_changes SET status = 'superseded', reason = @reason, updated_at = now()
WHERE id = @id AND status = 'pending'
RETURNING *;

-- name: ListPendingGitChanges :many
SELECT * FROM git_changes WHERE status = 'pending' ORDER BY slug;

-- name: ListGitChanges :many
-- Eerst wat wacht of loopt, dan de rest van nieuw naar oud.
SELECT g.*, c.name AS cluster_name, c.environment AS cluster_environment, u.username AS decided_by_name
FROM git_changes g
LEFT JOIN clusters c ON c.id = g.cluster_id
LEFT JOIN users u ON u.id = g.decided_by
WHERE (sqlc.narg('status')::text IS NULL OR g.status = sqlc.narg('status'))
  AND (sqlc.narg('cluster_id')::uuid IS NULL OR g.cluster_id = sqlc.narg('cluster_id'))
ORDER BY (g.status IN ('pending', 'applying')) DESC, g.created_at DESC
LIMIT @max;

-- name: GetGitChange :one
SELECT g.*, c.name AS cluster_name, c.environment AS cluster_environment, u.username AS decided_by_name
FROM git_changes g
LEFT JOIN clusters c ON c.id = g.cluster_id
LEFT JOIN users u ON u.id = g.decided_by
WHERE g.id = $1;

-- name: CountPendingGitChanges :one
SELECT count(*)::int FROM git_changes WHERE status = 'pending';

-- name: LockGitChange :one
SELECT * FROM git_changes WHERE id = $1 FOR UPDATE;

-- name: DecideGitChange :one
-- Goedkeuren (applying met een taak, of meteen applied) of afwijzen.
UPDATE git_changes
SET status = @status, revision = sqlc.narg('revision'), job_id = sqlc.narg('job_id'), reason = @reason,
    decided_by = @decided_by, decided_at = now(), updated_at = now()
WHERE id = @id AND status = 'pending'
RETURNING *;

-- name: FinishGitChange :one
-- Na de taak: applied of failed.
UPDATE git_changes SET status = @status, reason = @reason, updated_at = now()
WHERE job_id = @job_id AND status = 'applying'
RETURNING *;

-- name: ListLatestGitChanges :many
-- De nieuwste wijziging per slug, ongeacht haar status.
SELECT DISTINCT ON (slug) * FROM git_changes ORDER BY slug, created_at DESC, id;
