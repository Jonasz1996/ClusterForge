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

-- name: ListHealAuto :many
SELECT * FROM heal_auto;

-- name: GetHealAuto :one
SELECT * FROM heal_auto WHERE cluster_id = $1;

-- name: SetHealAuto :one
-- Aanzetten wist een pauze en begint de telling opnieuw.
INSERT INTO heal_auto (cluster_id, enabled, enabled_at, paused_at, paused_reason, updated_at, updated_by)
VALUES (@cluster_id, @enabled, CASE WHEN @enabled::boolean THEN now() END, NULL, '', now(), @updated_by)
ON CONFLICT (cluster_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    enabled_at = CASE WHEN EXCLUDED.enabled THEN now() ELSE heal_auto.enabled_at END,
    paused_at = NULL, paused_reason = '', updated_at = now(), updated_by = EXCLUDED.updated_by
RETURNING *;

-- name: PauseHealAuto :execrows
UPDATE heal_auto SET paused_at = now(), paused_reason = @reason, updated_at = now()
WHERE cluster_id = @cluster_id AND enabled AND paused_at IS NULL;

-- name: ListHealAutoRuns :many
-- De pogingen van Zelfherstel vanzelf op een cluster sinds een tijdstip,
-- nieuwste eerst.
SELECT id, status, created_at, finished_at, coalesce((params->>'bootstrap')::boolean, false)::boolean AS bootstrap
FROM jobs
WHERE kind = 'heal.run' AND cluster_id = @cluster_id AND params->>'auto' = 'true' AND created_at >= @since
ORDER BY created_at DESC;
