-- name: ListHealNodes :many
-- De nodes met een agent, met hun adressen uit de laatste heartbeat, voor
-- Zelfherstel.
SELECT n.id, n.hostname, n.cluster_id, n.primary_ip, a.protocol_version AS agent_protocol, s.heartbeat_at,
       coalesce(s.addresses, '{}')::text[] AS addresses
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
ORDER BY lower(n.hostname);

-- name: ListVIPAddresses :many
-- Elk VIP; een node die het heeft, is daar niet zelf op te bereiken.
SELECT host(address)::text AS address FROM vips;

-- name: ListHealJobs :many
-- De laatste taak heal.run per database van het afgelopen etmaal, voor de
-- kaart Zelfherstel.
SELECT DISTINCT ON (params->>'key') coalesce(params->>'key', '')::text AS key, id, status, error, created_at, finished_at
FROM jobs
WHERE kind = 'heal.run' AND created_at > now() - interval '1 day'
ORDER BY params->>'key', created_at DESC;

-- name: LockHealKey :exec
-- Vergrendelt een database tot het einde van de transactie, zodat er nooit
-- twee herstellen tegelijk aan beginnen.
SELECT pg_advisory_xact_lock(hashtext('heal:' || @key::text));

-- name: HealJobActive :one
SELECT EXISTS (
    SELECT 1 FROM jobs WHERE kind = 'heal.run' AND status IN ('queued', 'running') AND params->>'key' = @key::text
) AS active;
