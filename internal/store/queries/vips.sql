-- name: ListVIPsByCluster :many
SELECT sqlc.embed(v), n.hostname AS owner_hostname
FROM vips v LEFT JOIN nodes n ON n.id = v.owner_node_id
WHERE v.cluster_id = $1
ORDER BY v.address;

-- name: GetVIP :one
SELECT * FROM vips WHERE id = $1;

-- name: CreateVIP :one
INSERT INTO vips (cluster_id, address, interface, vrid, description)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateVIP :one
UPDATE vips
SET address = $2, interface = $3, vrid = $4, description = $5, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteVIP :execrows
DELETE FROM vips WHERE id = $1;

-- name: LockVIP :one
SELECT * FROM vips WHERE id = $1 FOR UPDATE;
