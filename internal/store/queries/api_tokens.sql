-- name: ListAPITokens :many
SELECT t.*, coalesce(u.username, '')::text AS created_by_name
FROM api_tokens t
LEFT JOIN users u ON u.id = t.created_by
ORDER BY t.created_at;

-- name: CreateAPIToken :one
INSERT INTO api_tokens (name, token_hash, prefix, cluster_ids, scopes, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: DeleteAPIToken :execrows
DELETE FROM api_tokens WHERE id = $1;

-- name: GetAPITokenByHash :one
SELECT * FROM api_tokens WHERE token_hash = $1;

-- name: TouchAPIToken :exec
-- Hoogstens één schrijfactie per minuut per token, ook als homepage elke
-- paar seconden vraagt.
UPDATE api_tokens SET last_used_at = now(), last_used_ip = $2
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: DashboardNodes :many
-- Per node van een cluster: status, heartbeat en lifecycle voor de
-- samenvatting van /api/dashboard.
SELECT n.id, n.cluster_id, n.hostname, n.status, n.lifecycle::text AS lifecycle, s.heartbeat_at
FROM nodes n
LEFT JOIN node_status s ON s.node_id = n.id
WHERE n.cluster_id IS NOT NULL AND n.lifecycle <> 'decommissioned'
ORDER BY lower(n.hostname);

-- name: DashboardOpenIncidents :many
-- Open incidenten per cluster (zonder die van een failovertest).
SELECT s.cluster_id::uuid AS cluster_id, count(DISTINCT i.id)::int AS open,
       coalesce(max(CASE i.severity WHEN 'critical' THEN 3 WHEN 'high' THEN 2 ELSE 1 END), 0)::int AS worst
FROM incidents i
JOIN incident_subjects s ON s.incident_id = i.id
WHERE i.status = 'open' AND NOT i.caused_by_test AND s.cluster_id IS NOT NULL
GROUP BY s.cluster_id;

-- name: DashboardDBServers :many
-- De databaseservers op de nodes van een cluster.
SELECT n.cluster_id::uuid AS cluster_id, d.node_id, d.engine, d.running, d.role, d.error, d.error_kind
FROM db_servers d
JOIN nodes n ON n.id = d.node_id
WHERE n.cluster_id IS NOT NULL AND n.lifecycle <> 'decommissioned';

-- name: DashboardCrowdSec :many
-- CrowdSec per cluster: beheerde nodes, of CrowdSec en de bouncer draaien,
-- actieve bans en alerts van de laatste 24 uur.
SELECT n.cluster_id::uuid AS cluster_id,
       count(*) FILTER (WHERE c.managed)::int AS managed,
       count(*) FILTER (WHERE c.managed AND c.status->>'active' = 'active')::int AS running,
       count(*) FILTER (WHERE c.managed AND c.bouncer AND coalesce(c.status->>'bouncer', '') <> 'active')::int AS bouncers_down,
       (SELECT count(*) FROM crowdsec_decisions d JOIN nodes dn ON dn.id = d.node_id
         WHERE dn.cluster_id = n.cluster_id AND d.until > now())::int AS bans,
       (SELECT count(*) FROM crowdsec_alerts a JOIN nodes an ON an.id = a.node_id
         WHERE an.cluster_id = n.cluster_id AND a.started_at > now() - interval '24 hours' AND NOT a.simulated)::int AS alerts_24h
FROM crowdsec_nodes c
JOIN nodes n ON n.id = c.node_id
WHERE n.cluster_id IS NOT NULL
GROUP BY n.cluster_id;

-- name: DashboardFirewall :many
-- Firewall per cluster: nodes per stand, mislukte toepassingen en het
-- tegengehouden verkeer van de laatste 24 uur.
SELECT n.cluster_id::uuid AS cluster_id,
       count(*) FILTER (WHERE f.mode = 'managed')::int AS managed,
       count(*) FILTER (WHERE f.mode = 'preview')::int AS preview,
       count(*) FILTER (WHERE f.mode = 'managed' AND coalesce((f.status->>'active')::boolean, false))::int AS active,
       count(*) FILTER (WHERE f.mode = 'managed' AND f.apply_error <> '')::int AS failed,
       (SELECT coalesce(sum(b.count), 0) FROM firewall_blocked b JOIN nodes bn ON bn.id = b.node_id
         WHERE bn.cluster_id = n.cluster_id AND b.hour > now() - interval '24 hours')::bigint AS blocked_24h
FROM firewall_nodes f
JOIN nodes n ON n.id = f.node_id
WHERE n.cluster_id IS NOT NULL
GROUP BY n.cluster_id;

-- name: DashboardNodeList :many
-- Elke node behalve de afgebouwde, met cluster en Proxmox-gast, voor
-- /api/dashboard/v1/nodes (fase 31).
SELECT n.id, n.hostname, n.cluster_id, c.slug AS cluster_slug, c.name AS cluster_name,
       n.role, n.description, n.lifecycle::text AS lifecycle, n.primary_ip, n.tags,
       n.status, n.status_reason, s.heartbeat_at,
       n.proxmox_id, n.pve_vmid, r.pve_node, r.type AS pve_type, r.name AS pve_name
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
WHERE n.lifecycle <> 'decommissioned'
ORDER BY n.cluster_id NULLS LAST, lower(n.hostname);
