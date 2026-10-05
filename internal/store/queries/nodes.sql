-- name: ListNodes :many
SELECT sqlc.embed(n), c.slug AS cluster_slug, c.name AS cluster_name
FROM nodes n LEFT JOIN clusters c ON c.id = n.cluster_id
ORDER BY lower(n.hostname);

-- name: ListNodesByCluster :many
SELECT * FROM nodes WHERE cluster_id = $1 ORDER BY lower(hostname);

-- name: GetNode :one
SELECT sqlc.embed(n), c.slug AS cluster_slug, c.name AS cluster_name
FROM nodes n LEFT JOIN clusters c ON c.id = n.cluster_id
WHERE n.id = $1;

-- name: CreateNode :one
INSERT INTO nodes (cluster_id, hostname, role, description, lifecycle, primary_ip, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateNode :one
UPDATE nodes
SET cluster_id = $2, hostname = $3, role = $4, description = $5, lifecycle = $6,
    primary_ip = $7, tags = $8, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteNode :execrows
DELETE FROM nodes WHERE id = $1;

-- name: LockNode :one
SELECT * FROM nodes WHERE id = $1 FOR UPDATE;
