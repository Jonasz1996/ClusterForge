-- name: ListOPNsenseAgents :many
-- Fase 34: de nodes waarvan de agent op een OPNsense-firewall draait
-- (facts opnsense), met cluster, agent en heartbeat.
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), '')::text AS address, (f.facts -> 'opnsense')::jsonb AS opnsense,
       n.cluster_id, coalesce(c.name, '')::text AS cluster_name, coalesce(c.environment::text, '')::text AS cluster_environment,
       a.protocol_version AS agent_protocol, a.version AS agent_version, s.heartbeat_at
FROM nodes n
JOIN node_facts f ON f.node_id = n.id
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
WHERE jsonb_typeof(f.facts -> 'opnsense') = 'object'
ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname);

-- name: InsertOpnsenseBackup :one
INSERT INTO opnsense_backups (node_id, reason, sha256, size, version, key_id, nonce, data)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, taken_at;

-- name: ListOpnsenseBackups :many
SELECT id, node_id, taken_at, reason, sha256, size, version
FROM opnsense_backups
WHERE node_id = $1
ORDER BY taken_at DESC;

-- name: LatestOpnsenseBackup :one
SELECT id, taken_at, sha256
FROM opnsense_backups
WHERE node_id = $1
ORDER BY taken_at DESC
LIMIT 1;

-- name: GetOpnsenseBackup :one
SELECT * FROM opnsense_backups WHERE id = $1;

-- name: TouchOpnsenseBackup :exec
UPDATE opnsense_backups SET taken_at = now(), reason = $2 WHERE id = $1;

-- name: PruneOpnsenseBackups :exec
DELETE FROM opnsense_backups b
WHERE b.node_id = $1
  AND b.id NOT IN (
    SELECT k.id FROM opnsense_backups k WHERE k.node_id = $1 ORDER BY k.taken_at DESC LIMIT $2
  );
