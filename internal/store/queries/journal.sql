-- name: InsertNodeChanges :batchexec
-- Een regel die er al is (zelfde node, soort, onderwerp, tijd en actie),
-- wordt overgeslagen; een agent stuurt na een onderbreking soms opnieuw.
INSERT INTO node_changes (node_id, cluster_id, ts, source, kind, subject, action, summary, detail, job_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT ON CONSTRAINT node_changes_unique DO NOTHING;

-- name: ListNodeChanges :many
SELECT c.*, n.hostname
FROM node_changes c
JOIN nodes n ON n.id = c.node_id
WHERE c.node_id = @node_id
  AND c.ts >= @since AND c.ts < @until
  AND (cardinality(@kinds::text[]) = 0 OR c.kind = ANY(@kinds::text[]))
ORDER BY c.ts DESC, c.id DESC
LIMIT @lim;

-- name: ListClusterChanges :many
-- Het journaal van een cluster: de regels van toen de node erin zat, en van
-- de nodes die er nu in zitten.
SELECT c.*, n.hostname
FROM node_changes c
JOIN nodes n ON n.id = c.node_id
WHERE (c.cluster_id = @cluster_id OR n.cluster_id = @cluster_id)
  AND c.ts >= @since AND c.ts < @until
  AND (cardinality(@kinds::text[]) = 0 OR c.kind = ANY(@kinds::text[]))
ORDER BY c.ts DESC, c.id DESC
LIMIT @lim;

-- name: PruneNodeChanges :execrows
DELETE FROM node_changes WHERE id IN (
    SELECT o.id FROM node_changes o WHERE o.ts < @before::timestamptz ORDER BY o.ts LIMIT 10000
);

-- name: GetHeartbeatPrevious :one
-- Wat de vorige heartbeat zei, om overgangen te zien.
SELECT a.version AS agent_version, a.protocol_version,
       s.heartbeat_at, coalesce(s.services, '{}'::jsonb)::jsonb AS services,
       coalesce(s.clock_offset_ms, 0)::integer AS clock_offset_ms
FROM agents a
LEFT JOIN node_status s ON s.node_id = a.node_id
WHERE a.node_id = $1 AND a.revoked_at IS NULL;

-- name: GetClockOffset :one
SELECT clock_offset_ms FROM node_status WHERE node_id = $1;

-- name: ListProxmoxTasksSince :many
SELECT * FROM proxmox_tasks WHERE connection_id = $1 AND started_at >= $2;

-- name: UpsertProxmoxTask :batchexec
INSERT INTO proxmox_tasks (connection_id, upid, pve_node, type, vmid, username, exitstatus, started_at, ended_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (connection_id, upid) DO UPDATE
SET exitstatus = EXCLUDED.exitstatus, ended_at = EXCLUDED.ended_at;

-- name: PruneProxmoxTasks :execrows
DELETE FROM proxmox_tasks WHERE started_at < $1;

-- name: ListProxmoxHosts :many
SELECT pve_node, status FROM proxmox_resources WHERE connection_id = $1 AND type = 'node';

-- name: UpsertObservedConnections :batchexec
INSERT INTO observed_connections (node_id, unit, direction, peer_addr, peer_port, local_port, first_seen, last_seen, samples)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8)
ON CONFLICT (node_id, unit, direction, peer_addr, peer_port, local_port) DO UPDATE
SET last_seen = greatest(observed_connections.last_seen, EXCLUDED.last_seen),
    samples = observed_connections.samples + EXCLUDED.samples;

-- name: PruneObservedConnections :execrows
DELETE FROM observed_connections WHERE last_seen < $1;

-- name: ListKnownNodeAddresses :many
-- Alle adressen van nodes: het primaire en die uit de laatste heartbeat.
SELECT DISTINCT addr::text AS addr FROM (
    SELECT host(primary_ip) AS addr FROM nodes WHERE primary_ip IS NOT NULL
    UNION ALL
    SELECT unnest(addresses) FROM node_status
) a;

-- name: ListNodesWithoutJournal :many
-- Nodes met een agent die nog geen journaal stuurt.
SELECT n.id, n.hostname
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
WHERE (n.id = sqlc.narg(node_id) OR n.cluster_id = sqlc.narg(cluster_id))
  AND a.protocol_version < @journal_since::integer
ORDER BY lower(n.hostname);
