-- name: SetClusterSpec :one
UPDATE clusters
SET spec = @spec, spec_revision = spec_revision + 1, template_name = @template_name,
    template_version = @template_version, updated_at = now()
WHERE id = @id
RETURNING spec_revision;

-- name: InsertSpecRevision :exec
INSERT INTO cluster_spec_revisions (cluster_id, revision, spec, source, created_by)
VALUES ($1, $2, $3, $4, $5);

-- name: InsertSecret :exec
INSERT INTO secrets (cluster_id, name, value_enc, key_id) VALUES ($1, $2, $3, $4);

-- name: GetSecret :one
SELECT * FROM secrets WHERE cluster_id = $1 AND name = $2;

-- name: SetNodeProxmox :exec
UPDATE nodes SET proxmox_id = @proxmox_id, pve_vmid = @pve_vmid, updated_at = now() WHERE id = @id;

-- name: UsedVRIDs :many
SELECT vrid::int FROM vips WHERE vrid IS NOT NULL;

-- name: UsedAddresses :many
-- Adressen die al bij een node of VIP horen, om botsingen te vermijden.
SELECT host(primary_ip)::text AS address FROM nodes WHERE primary_ip IS NOT NULL
UNION
SELECT host(address)::text FROM vips;

-- name: GetDeployNode :one
-- Wat een uitrol van een node moet weten: agent, heartbeat en facts.
SELECT n.id, n.hostname, n.role, n.lifecycle, coalesce(host(n.primary_ip), '')::text AS primary_ip,
       a.protocol_version AS agent_protocol, s.heartbeat_at, s.addresses,
       f.facts
FROM nodes n
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
WHERE n.id = $1;

-- name: RetryJob :one
UPDATE jobs
SET status = 'queued', error = '', cancel_requested = false, attempts = 0, finished_at = NULL, heartbeat_at = NULL
WHERE id = @id AND status IN ('failed', 'canceled') AND kind = ANY(@kinds::text[])
RETURNING *;

-- name: ClusterSlugTaken :one
SELECT EXISTS (SELECT 1 FROM clusters WHERE slug = $1);

-- name: SetVIPInterface :exec
-- Na een uitrol: de netwerkkaart van het VIP, als die nog leeg is.
UPDATE vips SET interface = @interface, updated_at = now()
WHERE cluster_id = @cluster_id AND host(address) = @address::text AND interface = '';

-- name: ListDesiredNodes :many
-- De nodes van een cluster met wat renderen nodig heeft: het vaste adres
-- en de facts.
SELECT n.id, n.hostname, n.lifecycle, coalesce(host(n.primary_ip), '')::text AS primary_ip, f.facts
FROM nodes n
LEFT JOIN node_facts f ON f.node_id = n.id
WHERE n.cluster_id = $1
ORDER BY n.hostname;

-- name: ListSpecRevisions :many
SELECT r.revision, r.spec, r.source, r.created_at, r.created_by, u.username AS created_by_name
FROM cluster_spec_revisions r
LEFT JOIN users u ON u.id = r.created_by
WHERE r.cluster_id = $1
ORDER BY r.revision DESC;

-- name: ListTemplateClusters :many
SELECT name, template_name::text AS template_name, template_version::text AS template_version
FROM clusters WHERE template_name IS NOT NULL AND template_version IS NOT NULL
ORDER BY name;
