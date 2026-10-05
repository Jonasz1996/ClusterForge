-- name: InsertEvent :exec
INSERT INTO events (actor_type, actor_id, subject_type, subject_id, cluster_id, action, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListRecentEvents :many
SELECT * FROM events ORDER BY id DESC LIMIT $1;
