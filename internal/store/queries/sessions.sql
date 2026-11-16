-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, csrf_token, ip, user_agent, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetSessionWithUser :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.id = $1
  AND sessions.expires_at > now()
  AND users.disabled_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now(), expires_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteUserSessionsExcept :exec
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();

-- name: ListUserSessions :many
SELECT id, ip, user_agent, created_at, last_seen_at, expires_at
FROM sessions
WHERE user_id = $1 AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: DeleteUserSession :execrows
DELETE FROM sessions WHERE user_id = $1 AND id = $2;

-- name: DeleteUserOtherSessions :execrows
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;
