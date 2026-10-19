-- name: ListClusterComponents :many
-- De onderdelen van een cluster met de laatste taak die ze toepaste.
SELECT sqlc.embed(cc), j.status AS job_status, j.error AS job_error, j.finished_at AS job_finished_at,
       rj.status AS recover_status, rj.error AS recover_error
FROM cluster_components cc
LEFT JOIN jobs j ON j.id = cc.job_id
LEFT JOIN jobs rj ON rj.id = cc.recover_job_id
WHERE cc.cluster_id = $1
ORDER BY cc.engine;

-- name: GetClusterComponent :one
SELECT * FROM cluster_components WHERE id = $1;

-- name: LockClusterComponent :one
SELECT * FROM cluster_components WHERE id = $1 FOR UPDATE;

-- name: InsertClusterComponent :one
INSERT INTO cluster_components (cluster_id, engine, spec)
VALUES (@cluster_id, @engine, @spec)
RETURNING *;

-- name: InsertClusterComponentRevision :exec
INSERT INTO cluster_component_revisions (component_id, revision, spec, created_by)
VALUES (@component_id, @revision, @spec, @created_by);

-- name: GetClusterComponentRevision :one
SELECT * FROM cluster_component_revisions WHERE component_id = $1 AND revision = $2;

-- name: SetClusterComponentJob :exec
UPDATE cluster_components SET job_id = @job_id, updated_at = now() WHERE id = @id;

-- name: SetClusterComponentSpec :exec
-- Een nieuwe revisie van de spec, zoals andere instellingen.
UPDATE cluster_components SET spec = @spec, revision = @revision, updated_at = now() WHERE id = @id;

-- name: SetClusterComponentApplied :exec
UPDATE cluster_components SET applied_revision = @revision, updated_at = now() WHERE id = @id;

-- name: DeleteClusterComponent :exec
DELETE FROM cluster_components WHERE id = $1;

-- name: DeleteSecretsWithPrefix :exec
-- De geheimen van een onderdeel, zoals mariadb.sst_password.
DELETE FROM secrets WHERE cluster_id = @cluster_id AND starts_with(name, @prefix::text);

-- name: ListNodeComponents :many
-- De toegepaste onderdelen waarin een node staat, voor de driftcontrole.
SELECT cc.*
FROM cluster_components cc
WHERE cc.applied_revision IS NOT NULL
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(cc.spec -> 'nodes') e WHERE e ->> 'node_id' = sqlc.arg('node_id')::uuid::text)
ORDER BY cc.engine;

-- name: ListComponentCandidates :many
-- De nodes van een cluster met wat het plan van een onderdeel moet weten:
-- adres en facts, de agent en de VM of CT in Proxmox.
SELECT n.id, n.hostname, n.lifecycle, coalesce(host(n.primary_ip), '')::text AS primary_ip, f.facts,
       coalesce(a.protocol_version, 0)::int AS agent_protocol, (a.id IS NOT NULL)::boolean AS has_agent,
       s.heartbeat_at, n.pve_vmid, r.type AS pve_type, r.pve_node, pc.name AS proxmox_name
FROM nodes n
LEFT JOIN node_facts f ON f.node_id = n.id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN proxmox_connections pc ON pc.id = n.proxmox_id
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
WHERE n.cluster_id = $1
ORDER BY lower(n.hostname);

-- name: ListWatchedComponents :many
-- De toegepaste onderdelen, voor de bewaking.
SELECT cc.*, c.name AS cluster_name, c.slug AS cluster_slug
FROM cluster_components cc
JOIN clusters c ON c.id = cc.cluster_id
WHERE cc.applied_revision IS NOT NULL
ORDER BY c.name, cc.engine;

-- name: ListUnfinishedComponents :many
-- De onderdelen die nog nooit helemaal uitgerold zijn, met de laatste taak
-- die ze toepaste, voor Zelfherstel.
SELECT cc.id, cc.cluster_id, cc.engine, j.status AS job_status, j.error AS job_error
FROM cluster_components cc
LEFT JOIN jobs j ON j.id = cc.job_id
WHERE cc.applied_revision IS NULL
ORDER BY cc.cluster_id, cc.engine;

-- name: ListDBComponents :many
-- De toegepaste onderdelen met een database van deze soort, voor een
-- verhuizing.
SELECT cc.id, cc.cluster_id, c.name AS cluster_name
FROM cluster_components cc
JOIN clusters c ON c.id = cc.cluster_id
WHERE cc.applied_revision IS NOT NULL AND cc.engine = @engine
ORDER BY c.name;

-- name: SetClusterComponentHealth :exec
UPDATE cluster_components
SET down_since = @down_since, down_reason = @down_reason, nodes_out = @nodes_out
WHERE id = @id;

-- name: SetClusterComponentWatch :exec
-- Wat de bewaking zag: de toestand van de database en wat ze onthoudt
-- voor de volgende keer.
UPDATE cluster_components
SET health = @health, health_reason = @health_reason, health_at = @health_at, observed = @observed
WHERE id = @id;

-- name: ListComponentHealth :many
-- De toestand van de databases, voor de status van de clusters.
SELECT cluster_id, engine, health, health_reason, health_at
FROM cluster_components
WHERE applied_revision IS NOT NULL AND health_at IS NOT NULL;

-- name: SetClusterComponentAutoRecover :exec
UPDATE cluster_components SET auto_recover = @auto_recover, updated_at = now() WHERE id = @id;

-- name: SetClusterComponentAutoRecovered :exec
UPDATE cluster_components SET auto_recovered_at = @at WHERE id = @id;

-- name: SetClusterComponentRecoverJob :exec
UPDATE cluster_components SET recover_job_id = @job_id WHERE id = @id;

-- name: ListDatabaseTestPasses :many
-- Per cluster en database de laatste geslaagde failovertest met een
-- schrijftest via het VIP van die database, voor de scorekaart Database.
SELECT cluster_id::uuid AS cluster_id, (measurements->'writes'->>'title')::text AS title, max(finished_at)::timestamptz AS finished_at
FROM test_runs
WHERE kind = 'failover.test' AND result = 'pass' AND cluster_id IS NOT NULL AND finished_at IS NOT NULL
  AND measurements->'writes'->>'title' IS NOT NULL
GROUP BY 1, 2;
