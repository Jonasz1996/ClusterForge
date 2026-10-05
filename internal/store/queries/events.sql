-- name: InsertEvent :exec
INSERT INTO events (actor_type, actor_id, subject_type, subject_id, cluster_id, action, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListRecentEvents :many
SELECT * FROM events ORDER BY id DESC LIMIT $1;

-- name: ListAudit :many
-- Het logboek, nieuwste eerst, met de namen die de zinnen nodig hebben. Een
-- verwijderd cluster of een verwijderde node krijgt zijn naam uit het event
-- van de verwijdering.
SELECT e.id, e.ts, e.actor_type, e.actor_id, e.subject_type, e.subject_id, e.cluster_id,
       e.action, e.payload, e.node_ref, e.job_ref,
       au.username AS actor_username,
       coalesce(agn.hostname, an.hostname, '')::text AS actor_hostname,
       coalesce(e.cluster_id::text, CASE WHEN e.subject_type = 'cluster' THEN e.subject_id END, '')::text AS cluster_ref,
       (c.id IS NOT NULL)::boolean AS cluster_exists,
       coalesce(c.name, (
           SELECT d.payload->>'name' FROM events d
           WHERE d.subject_type = 'cluster' AND d.action = 'cluster.deleted'
             AND d.subject_id = coalesce(e.cluster_id::text, CASE WHEN e.subject_type = 'cluster' THEN e.subject_id END)
           LIMIT 1), '')::text AS cluster_name,
       (n.id IS NOT NULL)::boolean AS node_exists,
       coalesce(n.hostname, (
           SELECT d.payload->>'hostname' FROM events d
           WHERE d.subject_type = 'node' AND d.action = 'node.deleted' AND d.subject_id = e.node_ref
           LIMIT 1), '')::text AS node_hostname,
       j.title AS job_title,
       j.requested_by AS job_requested_by,
       ju.username AS job_requested_by_username
FROM events e
LEFT JOIN users au ON e.actor_type = 'user' AND au.id::text = e.actor_id
LEFT JOIN agents ag ON e.actor_type = 'agent' AND ag.id::text = e.actor_id
LEFT JOIN nodes agn ON agn.id = ag.node_id
LEFT JOIN nodes an ON e.actor_type = 'agent' AND an.id::text = e.actor_id
LEFT JOIN clusters c ON c.id::text = coalesce(e.cluster_id::text, CASE WHEN e.subject_type = 'cluster' THEN e.subject_id END)
LEFT JOIN nodes n ON n.id::text = e.node_ref
LEFT JOIN jobs j ON j.id::text = e.job_ref
LEFT JOIN users ju ON ju.id = j.requested_by
WHERE (sqlc.narg('before_id')::bigint IS NULL OR e.id < sqlc.narg('before_id')::bigint)
  AND (sqlc.narg('from_ts')::timestamptz IS NULL OR e.ts >= sqlc.narg('from_ts')::timestamptz)
  AND (sqlc.narg('to_ts')::timestamptz IS NULL OR e.ts < sqlc.narg('to_ts')::timestamptz)
  AND (sqlc.narg('actor_type')::actor_type IS NULL OR e.actor_type = sqlc.narg('actor_type')::actor_type)
  AND (sqlc.narg('user_id')::text IS NULL
       OR (e.actor_type = 'user' AND e.actor_id = sqlc.narg('user_id')::text)
       OR (e.subject_type = 'user' AND e.subject_id = sqlc.narg('user_id')::text)
       OR (e.actor_type = 'system' AND j.requested_by::text = sqlc.narg('user_id')::text))
  AND (sqlc.narg('cluster_id')::text IS NULL
       OR e.cluster_id::text = sqlc.narg('cluster_id')::text
       OR (e.subject_type = 'cluster' AND e.subject_id = sqlc.narg('cluster_id')::text))
  AND (sqlc.narg('node_id')::text IS NULL OR e.node_ref = sqlc.narg('node_id')::text)
  AND (sqlc.narg('job_id')::text IS NULL OR e.job_ref = sqlc.narg('job_id')::text)
  AND (sqlc.narg('actions')::text[] IS NULL OR e.action = ANY(sqlc.narg('actions')::text[]))
  AND (sqlc.narg('pattern')::text IS NULL
       OR e.action ILIKE sqlc.narg('pattern')::text
       OR e.payload::text ILIKE sqlc.narg('pattern')::text
       OR au.username ILIKE sqlc.narg('pattern')::text
       OR c.name ILIKE sqlc.narg('pattern')::text
       OR n.hostname ILIKE sqlc.narg('pattern')::text
       OR agn.hostname ILIKE sqlc.narg('pattern')::text
       OR an.hostname ILIKE sqlc.narg('pattern')::text
       OR j.title ILIKE sqlc.narg('pattern')::text)
ORDER BY e.id DESC
LIMIT sqlc.arg('lim');

-- name: ListAuditUsers :many
-- Alle gebruikers, ook uitgeschakelde, voor het filter en de namen in het logboek.
SELECT id, username, role, disabled_at FROM users ORDER BY username;

-- name: ListAuditNames :many
-- Namen van clusters, nodes en Proxmox-koppelingen op id, voor het logboek.
SELECT 'cluster'::text AS kind, id, name FROM clusters
UNION ALL
SELECT 'node'::text AS kind, id, hostname AS name FROM nodes
UNION ALL
SELECT 'proxmox'::text AS kind, id, name FROM proxmox_connections;

-- name: ListDeletedSubjects :many
-- Verwijderde clusters en nodes, zodat het logboek er nog op kan filteren.
SELECT subject_type, subject_id, coalesce(payload->>'name', payload->>'hostname', '')::text AS name,
       cluster_id, ts
FROM events
WHERE action IN ('cluster.deleted', 'node.deleted')
ORDER BY ts DESC;

-- name: AuditStats :one
SELECT count(*) AS total, coalesce(min(ts), now())::timestamptz AS oldest FROM events;
