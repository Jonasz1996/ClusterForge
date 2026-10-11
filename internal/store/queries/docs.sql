-- name: ListDocClusters :many
SELECT c.id, c.slug, c.name, c.description, c.type, c.environment::text AS environment, c.tags, c.status,
       c.spec, c.spec_revision, c.applied_revision, (c.git_repo_id IS NOT NULL)::boolean AS git_linked,
       c.rto_target_minutes, c.rpo_target_hours
FROM clusters c
ORDER BY lower(c.name), c.id;

-- name: ListDocNodes :many
-- De nodes van alle clusters met hun facts en of ze een agent hebben.
SELECT n.id, n.cluster_id::uuid AS cluster_id, n.hostname, n.role, n.description, n.lifecycle::text AS lifecycle,
       coalesce(host(n.primary_ip), '')::text AS primary_ip, n.proxmox_id, n.pve_vmid, f.facts,
       EXISTS (SELECT 1 FROM agents a WHERE a.node_id = n.id AND a.revoked_at IS NULL)::boolean AS has_agent
FROM nodes n
LEFT JOIN node_facts f ON f.node_id = n.id
WHERE n.cluster_id IS NOT NULL
ORDER BY n.hostname, n.id;

-- name: ListDocVIPs :many
SELECT v.cluster_id, host(v.address)::text AS address, v.interface, v.vrid, v.description
FROM vips v
ORDER BY v.address, v.id;

-- name: ListDocFailoverTests :many
-- De failovertests met hun laatste afgeronde run.
SELECT t.cluster_id, t.name, t.scenario, t.service, t.max_takeover_seconds, t.expect_failback, t.scheduled,
       host(v.address)::text AS vip,
       coalesce(lr.result, '')::text AS run_result, coalesce(lr.summary, '')::text AS run_summary,
       lr.finished_at AS run_finished, lr.measurements AS run_measurements
FROM failover_tests t
JOIN vips v ON v.id = t.vip_id
LEFT JOIN LATERAL (
    SELECT r.result, r.summary, r.finished_at, r.measurements FROM test_runs r
    WHERE r.test_id = t.id AND r.result IN ('pass', 'warning', 'fail', 'error') AND r.finished_at IS NOT NULL
    ORDER BY r.finished_at DESC
    LIMIT 1
) lr ON true
ORDER BY t.name, t.id;

-- name: ListDocRehearsals :many
-- Per cluster de laatste afgeronde DR-repetitie.
SELECT DISTINCT ON (r.cluster_id) r.cluster_id::uuid AS cluster_id, r.id, r.result::text AS result, r.summary,
       r.finished_at::timestamptz AS finished_at, r.definition, r.measurements
FROM test_runs r
WHERE r.kind = 'dr.rehearsal' AND r.cluster_id IS NOT NULL AND r.finished_at IS NOT NULL
  AND r.result IN ('pass', 'warning', 'fail', 'error')
ORDER BY r.cluster_id, r.finished_at DESC, r.id;

-- name: ListDocMeasuredRehearsals :many
-- Per cluster de laatste DR-repetitie die de RTO mat: het cluster kwam
-- werkend terug.
SELECT DISTINCT ON (r.cluster_id) r.cluster_id::uuid AS cluster_id, r.id, r.result::text AS result, r.summary,
       r.finished_at::timestamptz AS finished_at, r.definition, r.measurements
FROM test_runs r
WHERE r.kind = 'dr.rehearsal' AND r.cluster_id IS NOT NULL AND r.finished_at IS NOT NULL
  AND r.result IN ('pass', 'warning') AND r.measurements ? 'rto_seconds'
ORDER BY r.cluster_id, r.finished_at DESC, r.id;

-- name: ListDocRehearsalScenarios :many
SELECT s.cluster_id, s.name, s.schedule, s.params
FROM disaster_scenarios s
WHERE s.kind = 'dr_rehearsal'
ORDER BY s.name, s.id;

-- name: ListDocEvents :many
-- De events na @after, met het cluster van hun node als het event zelf
-- geen cluster heeft. De documentatie zet daarmee de clusters in de rij.
SELECT e.id, e.action, coalesce(e.cluster_id, n.cluster_id) AS cluster_id
FROM events e
LEFT JOIN nodes n ON n.id::text = e.node_ref
WHERE e.id > @after
ORDER BY e.id
LIMIT @lim;

-- name: MaxEventID :one
SELECT coalesce(max(id), 0)::bigint FROM events;

-- name: LatestDocSnapshots :many
-- De nieuwste versie van elk document, zonder inhoud.
SELECT DISTINCT ON (s.scope, s.kind, s.cluster_id) s.id, s.scope, s.cluster_id, s.kind, s.sha256, s.generated_at, s.cause
FROM doc_snapshots s
ORDER BY s.scope, s.kind, s.cluster_id, s.id DESC;

-- name: GetLatestDocSnapshot :one
SELECT * FROM doc_snapshots
WHERE scope = @scope AND kind = @kind AND cluster_id IS NOT DISTINCT FROM sqlc.narg(cluster_id)::uuid
ORDER BY id DESC
LIMIT 1;

-- name: GetDocSnapshot :one
SELECT * FROM doc_snapshots WHERE id = @id;

-- name: ListDocHistory :many
-- De versies van één document, nieuwste eerst.
SELECT id, sha256, generated_at, cause
FROM doc_snapshots
WHERE scope = @scope AND kind = @kind AND cluster_id IS NOT DISTINCT FROM sqlc.narg(cluster_id)::uuid
ORDER BY id DESC;

-- name: InsertDocSnapshot :one
INSERT INTO doc_snapshots (scope, cluster_id, kind, content, sha256, cause, generated_at)
VALUES (@scope, sqlc.narg(cluster_id), @kind, @content, @sha256, @cause, @generated_at)
RETURNING id;

-- name: PruneDocSnapshots :exec
-- Houdt de nieuwste @keep versies van één document.
DELETE FROM doc_snapshots d
WHERE d.scope = @scope AND d.kind = @kind AND d.cluster_id IS NOT DISTINCT FROM sqlc.narg(cluster_id)::uuid
  AND d.id < (SELECT coalesce(min(k.id), 0) FROM (
      SELECT s.id FROM doc_snapshots s
      WHERE s.scope = @scope AND s.kind = @kind AND s.cluster_id IS NOT DISTINCT FROM sqlc.narg(cluster_id)::uuid
      ORDER BY s.id DESC
      LIMIT @keep) k);

-- name: GetDocsTarget :one
SELECT * FROM docs_targets LIMIT 1;

-- name: LockDocsTarget :one
SELECT * FROM docs_targets LIMIT 1 FOR UPDATE;

-- name: CreateDocsTarget :one
INSERT INTO docs_targets (id, api_url, owner, name, branch, path, token_enc, key_id, enabled)
VALUES (@id, @api_url, @owner, @name, @branch, @path, @token_enc, @key_id, @enabled)
RETURNING *;

-- name: UpdateDocsTarget :one
-- Een andere repository, branch of map begint opnieuw: de laatste commit
-- en fout horen bij de oude bestemming.
UPDATE docs_targets SET
    api_url = @api_url, owner = @owner, name = @name, branch = @branch, path = @path,
    token_enc = @token_enc, key_id = @key_id, enabled = @enabled,
    last_commit_sha = CASE WHEN @reset::boolean THEN '' ELSE last_commit_sha END,
    last_commit_url = CASE WHEN @reset::boolean THEN '' ELSE last_commit_url END,
    last_commit_files = CASE WHEN @reset::boolean THEN 0 ELSE last_commit_files END,
    last_published_at = CASE WHEN @reset::boolean THEN NULL ELSE last_published_at END,
    last_checked_at = CASE WHEN @reset::boolean THEN NULL ELSE last_checked_at END,
    last_error = CASE WHEN @reset::boolean THEN '' ELSE last_error END,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteDocsTarget :execrows
DELETE FROM docs_targets WHERE id = @id;

-- name: SetDocsPublished :exec
UPDATE docs_targets SET
    last_commit_sha = @sha, last_commit_url = @url, last_commit_files = @files, last_published_at = @at,
    last_checked_at = @at, last_attempt_at = @at, last_error = ''
WHERE id = @id;

-- name: SetDocsChecked :exec
UPDATE docs_targets SET last_checked_at = @at, last_attempt_at = @at, last_error = '' WHERE id = @id;

-- name: SetDocsPublishError :exec
UPDATE docs_targets SET last_attempt_at = @at, last_error = @last_error WHERE id = @id;

-- name: ListDocSummaries :many
SELECT d.cluster_id, d.text, d.model, d.created_at, coalesce(u.username, '')::text AS username
FROM doc_summaries d
LEFT JOIN users u ON u.id = d.created_by;

-- name: GetDocSummary :one
SELECT d.cluster_id, d.text, d.model, d.created_at, d.conversation_id, coalesce(u.username, '')::text AS username
FROM doc_summaries d
LEFT JOIN users u ON u.id = d.created_by
WHERE d.cluster_id = $1;

-- name: UpsertDocSummary :exec
INSERT INTO doc_summaries (cluster_id, text, model, created_by, created_at, conversation_id)
VALUES (@cluster_id, @text, @model, @created_by, @created_at, @conversation_id)
ON CONFLICT (cluster_id) DO UPDATE
SET text = excluded.text, model = excluded.model, created_by = excluded.created_by, created_at = excluded.created_at,
    conversation_id = excluded.conversation_id;

-- name: DeleteDocSummary :execrows
DELETE FROM doc_summaries WHERE cluster_id = $1;
