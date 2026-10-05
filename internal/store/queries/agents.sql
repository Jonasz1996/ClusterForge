-- name: GetServerSecret :one
SELECT value FROM server_secrets WHERE name = $1;

-- name: InsertServerSecret :exec
INSERT INTO server_secrets (name, value) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING;

-- name: CreateEnrollmentToken :one
INSERT INTO enrollment_tokens (token_hash, description, node_id, cluster_id, max_uses, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListActiveEnrollmentTokens :many
SELECT t.*, n.hostname AS node_hostname, c.name AS cluster_name, u.username AS created_by_username
FROM enrollment_tokens t
LEFT JOIN nodes n ON n.id = t.node_id
LEFT JOIN clusters c ON c.id = t.cluster_id
LEFT JOIN users u ON u.id = t.created_by
WHERE t.expires_at > now() AND t.uses < t.max_uses
ORDER BY t.created_at DESC;

-- name: LockEnrollmentTokenByHash :one
SELECT * FROM enrollment_tokens WHERE token_hash = $1 FOR UPDATE;

-- name: UseEnrollmentToken :exec
UPDATE enrollment_tokens SET uses = uses + 1 WHERE id = $1;

-- name: DeleteEnrollmentToken :execrows
DELETE FROM enrollment_tokens WHERE id = $1;

-- name: FindNodeByHostname :one
SELECT * FROM nodes WHERE lower(hostname) = lower(@hostname::text) FOR UPDATE;

-- name: GetActiveAgentByNode :one
SELECT * FROM agents WHERE node_id = $1 AND revoked_at IS NULL;

-- name: GetActiveAgentByNkey :one
SELECT * FROM agents WHERE nkey_public = $1 AND revoked_at IS NULL;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = $1;

-- name: RevokeAgentsOfNode :many
UPDATE agents SET revoked_at = now()
WHERE node_id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeAgent :one
UPDATE agents SET revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: CreateAgent :one
INSERT INTO agents (id, node_id, nkey_public, machine_id, version, protocol_version)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: TouchAgent :exec
UPDATE agents SET last_seen_at = now(), version = $2, protocol_version = $3
WHERE node_id = $1 AND revoked_at IS NULL;

-- name: UpsertNodeStatus :exec
INSERT INTO node_status (node_id, heartbeat_at, uptime_seconds, load1, load5, load15, addresses, services)
VALUES ($1, now(), $2, $3, $4, $5, $6, $7)
ON CONFLICT (node_id) DO UPDATE
SET heartbeat_at = now(), uptime_seconds = EXCLUDED.uptime_seconds, load1 = EXCLUDED.load1,
    load5 = EXCLUDED.load5, load15 = EXCLUDED.load15, addresses = EXCLUDED.addresses,
    services = EXCLUDED.services;

-- name: GetNodeStatus :one
SELECT * FROM node_status WHERE node_id = $1;

-- name: GetNodeFacts :one
SELECT * FROM node_facts WHERE node_id = $1;

-- name: LockNodeFacts :one
SELECT * FROM node_facts WHERE node_id = $1 FOR UPDATE;

-- name: UpsertNodeFacts :exec
-- De facts zelf altijd vervangen (schijfgebruik verandert steeds), changed_at
-- alleen als de hash van de stabiele velden verandert.
INSERT INTO node_facts (node_id, hash, facts, collected_at, changed_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (node_id) DO UPDATE
SET facts = EXCLUDED.facts, collected_at = EXCLUDED.collected_at,
    changed_at = CASE WHEN node_facts.hash = EXCLUDED.hash THEN node_facts.changed_at ELSE now() END,
    hash = EXCLUDED.hash;

-- name: SetNodePrimaryIPIfEmpty :exec
UPDATE nodes SET primary_ip = $2, updated_at = now() WHERE id = $1 AND primary_ip IS NULL;

-- name: SetVIPOwner :exec
UPDATE vips SET owner_node_id = $2, owner_since = now() WHERE id = $1;
