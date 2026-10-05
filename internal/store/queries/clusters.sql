-- name: ListClusters :many
SELECT sqlc.embed(c),
       (SELECT count(*) FROM nodes n WHERE n.cluster_id = c.id)::int AS node_count,
       (SELECT count(*) FROM vips v WHERE v.cluster_id = c.id)::int AS vip_count
FROM clusters c
ORDER BY c.name;

-- name: GetCluster :one
SELECT * FROM clusters WHERE id = $1;

-- name: CreateCluster :one
INSERT INTO clusters (slug, name, description, type, environment, git_repo_url, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateCluster :one
UPDATE clusters
SET slug = $2, name = $3, description = $4, type = $5, environment = $6,
    git_repo_url = $7, tags = $8, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: TouchCluster :exec
UPDATE clusters SET updated_at = now() WHERE id = $1;

-- name: DeleteCluster :execrows
DELETE FROM clusters WHERE id = $1;

-- name: ListClusterOwners :many
SELECT u.id, u.username
FROM cluster_owners o JOIN users u ON u.id = o.user_id
WHERE o.cluster_id = $1
ORDER BY u.username;

-- name: ClearClusterOwners :exec
DELETE FROM cluster_owners WHERE cluster_id = $1;

-- name: AddClusterOwner :exec
INSERT INTO cluster_owners (cluster_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: LockCluster :one
SELECT * FROM clusters WHERE id = $1 FOR UPDATE;
