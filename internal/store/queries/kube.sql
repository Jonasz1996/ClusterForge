-- name: ListKubeBridgeNodes :many
-- De nodes van een cluster met een agent, voor de keuze van de node die
-- een kube-verzoek doorstuurt.
SELECT n.id, n.hostname, n.primary_ip, a.protocol_version AS agent_protocol, s.heartbeat_at,
       (f.facts -> 'kube')::jsonb AS kube
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
WHERE n.cluster_id = $1
ORDER BY lower(n.hostname);

-- name: ListKubeClusterIDs :many
SELECT id FROM clusters WHERE type = 'kubernetes' ORDER BY lower(name);

-- name: ListKubeClusters :many
-- De Kubernetes-clusters met hun laatste samenvatting; een cluster dat nog
-- nooit gesynchroniseerd is, heeft geen rij in kube_clusters.
SELECT c.id, c.name, c.slug, c.environment, c.status, c.status_reason,
       (k.cluster_id IS NOT NULL)::boolean AS synced, coalesce(k.api_ok, false)::boolean AS api_ok, coalesce(k.error, '')::text AS error,
       coalesce(k.version, '')::text AS version,
       coalesce(k.nodes_total, 0)::int AS nodes_total, coalesce(k.nodes_ready, 0)::int AS nodes_ready,
       coalesce(k.pods_total, 0)::int AS pods_total, coalesce(k.pods_running, 0)::int AS pods_running,
       coalesce(k.pods_pending, 0)::int AS pods_pending, coalesce(k.pods_problem, 0)::int AS pods_problem,
       coalesce(k.workloads_total, 0)::int AS workloads_total, coalesce(k.workloads_ready, 0)::int AS workloads_ready,
       k.synced_at, k.ok_at
FROM clusters c
LEFT JOIN kube_clusters k ON k.cluster_id = c.id
WHERE c.type = 'kubernetes'
ORDER BY lower(c.name);

-- name: SaveKubeCluster :exec
-- Bewaart een geslaagde sync.
INSERT INTO kube_clusters (cluster_id, api_ok, error, version, nodes_total, nodes_ready, pods_total, pods_running, pods_pending,
                           pods_problem, workloads_total, workloads_ready, via_node_id, synced_at, ok_at)
VALUES ($1, true, '', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now(), now())
ON CONFLICT (cluster_id) DO UPDATE
SET api_ok = true, error = '', error_kind = '', version = EXCLUDED.version, nodes_total = EXCLUDED.nodes_total, nodes_ready = EXCLUDED.nodes_ready,
    pods_total = EXCLUDED.pods_total, pods_running = EXCLUDED.pods_running, pods_pending = EXCLUDED.pods_pending,
    pods_problem = EXCLUDED.pods_problem, workloads_total = EXCLUDED.workloads_total, workloads_ready = EXCLUDED.workloads_ready,
    via_node_id = EXCLUDED.via_node_id, synced_at = now(), ok_at = now();

-- name: SaveKubeClusterError :exec
-- Bewaart een mislukte sync; de laatst bekende tellingen blijven staan.
INSERT INTO kube_clusters (cluster_id, api_ok, error, error_kind, synced_at)
VALUES ($1, false, $2, $3, now())
ON CONFLICT (cluster_id) DO UPDATE SET api_ok = false, error = EXCLUDED.error, error_kind = EXCLUDED.error_kind, synced_at = now();

-- name: DeleteKubeNodes :exec
DELETE FROM kube_nodes WHERE cluster_id = $1;

-- name: InsertKubeNode :exec
INSERT INTO kube_nodes (cluster_id, name, node_id, roles, ready, unschedulable, kubelet, internal_ip, cpu_millis, memory_bytes, pods, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ListKubeNodes :many
SELECT k.*, n.hostname AS node_hostname
FROM kube_nodes k
LEFT JOIN nodes n ON n.id = k.node_id
WHERE k.cluster_id = $1
ORDER BY k.name;

-- name: SetUserKubeLearning :exec
UPDATE users SET kube_learning = $2, updated_at = now() WHERE id = $1;

-- name: ListKubeLessonSteps :many
-- De voortgang van een gebruiker in de lessen van een cluster.
SELECT * FROM kube_lesson_steps
WHERE cluster_id = $1 AND user_id = $2
ORDER BY lesson, started_at, step;

-- name: StartKubeLessonStep :one
-- Een opdracht is aan de beurt. Bestaat de rij al, dan blijft wat er toen
-- gezien werd staan.
INSERT INTO kube_lesson_steps (cluster_id, user_id, lesson, step, data)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (cluster_id, user_id, lesson, step) DO UPDATE SET lesson = EXCLUDED.lesson
RETURNING *;

-- name: FinishKubeLessonStep :exec
UPDATE kube_lesson_steps SET done_at = now(), data = $5, answer = $6
WHERE cluster_id = $1 AND user_id = $2 AND lesson = $3 AND step = $4 AND done_at IS NULL;

-- name: ResetKubeLesson :exec
DELETE FROM kube_lesson_steps WHERE cluster_id = $1 AND user_id = $2 AND lesson = $3;

-- name: ListKubeWorkloadImages :many
-- De apps van een cluster zoals de vorige sync ze zag, om een uitrol met
-- een nieuwe image te herkennen.
SELECT namespace, kind, name, images FROM kube_workloads WHERE cluster_id = $1;

-- name: UpsertKubeWorkload :exec
-- Bewaart een app. short_since blijft staan zolang de app niet in orde is.
INSERT INTO kube_workloads (cluster_id, namespace, kind, name, system, desired, ready, up_to_date, images, trouble, short_since, seen_at,
                            restarts, restarted_at, no_memory_limit)
VALUES (@cluster_id, @namespace, @kind, @name, @system, @desired, @ready, @up_to_date, @images, @trouble,
        CASE WHEN @short::boolean THEN now() END, @seen_at::timestamptz, @restarts, @restarted_at, @no_memory_limit)
ON CONFLICT (cluster_id, namespace, kind, name) DO UPDATE
SET system = EXCLUDED.system, desired = EXCLUDED.desired, ready = EXCLUDED.ready, up_to_date = EXCLUDED.up_to_date,
    images = EXCLUDED.images, trouble = EXCLUDED.trouble,
    short_since = CASE WHEN @short::boolean THEN coalesce(kube_workloads.short_since, now()) END,
    seen_at = EXCLUDED.seen_at, restarts = EXCLUDED.restarts, restarted_at = EXCLUDED.restarted_at,
    no_memory_limit = EXCLUDED.no_memory_limit;

-- name: DeleteGoneKubeWorkloads :exec
-- Haalt de apps weg die de sync van @seen_at niet meer zag.
DELETE FROM kube_workloads WHERE cluster_id = $1 AND seen_at <> @seen_at::timestamptz;

-- name: UpsertKubeEvent :exec
-- Bewaart een waarschuwing. Komt ze terug, dan telt Kubernetes op in
-- hetzelfde event; hier gaan de telling en het laatste tijdstip mee.
INSERT INTO kube_events (cluster_id, key, ts, last_ts, count, reason, namespace, object_kind, object_name, app_kind, app_name,
                         node_name, node_id, message)
VALUES (@cluster_id, @key, @ts, @last_ts, @count, @reason, @namespace, @object_kind, @object_name, @app_kind, @app_name,
        @node_name, @node_id, @message)
ON CONFLICT (cluster_id, key) DO UPDATE
SET last_ts = greatest(kube_events.last_ts, EXCLUDED.last_ts), count = greatest(kube_events.count, EXCLUDED.count),
    message = EXCLUDED.message, node_id = coalesce(kube_events.node_id, EXCLUDED.node_id)
WHERE kube_events.last_ts < EXCLUDED.last_ts OR kube_events.count < EXCLUDED.count;

-- name: PruneKubeEvents :execrows
DELETE FROM kube_events WHERE id IN (
    SELECT o.id FROM kube_events o WHERE o.last_ts < @before::timestamptz ORDER BY o.last_ts LIMIT 10000
);

-- name: ListKubeEventsInScope :many
-- De waarschuwingen van de clusters, of op de nodes, die tussen @since en
-- @until begonnen, de nieuwste eerst.
SELECT e.*, coalesce(n.hostname, '')::text AS hostname, c.name AS cluster_name
FROM kube_events e
JOIN clusters c ON c.id = e.cluster_id
LEFT JOIN nodes n ON n.id = e.node_id
WHERE (e.cluster_id = ANY(@cluster_ids::uuid[]) OR e.node_id = ANY(@node_ids::uuid[]))
  AND e.ts >= @since AND e.ts < @until
ORDER BY e.ts DESC, e.id DESC
LIMIT @lim;

-- name: ListKubeStatusInputs :many
-- Per Kubernetes-cluster wat de sync laatst zag, voor de clusterstatus.
SELECT c.id AS cluster_id, (k.cluster_id IS NOT NULL)::boolean AS synced, coalesce(k.api_ok, false)::boolean AS api_ok,
       coalesce(k.error_kind, '')::text AS error_kind, coalesce(k.error, '')::text AS error, k.ok_at
FROM clusters c
LEFT JOIN kube_clusters k ON k.cluster_id = c.id
WHERE c.type = 'kubernetes';

-- name: ListAllKubeNodes :many
SELECT cluster_id, name, roles, ready FROM kube_nodes ORDER BY cluster_id, name;

-- name: ListKubeApps :many
-- De apps buiten de systeemnamespaces, voor hun status.
SELECT id, cluster_id, namespace, kind, name, desired, ready, up_to_date, trouble, short_since, status, status_reason, status_since
FROM kube_workloads
WHERE NOT system
ORDER BY cluster_id, namespace, name, kind;

-- name: SetKubeAppStatus :exec
UPDATE kube_workloads SET status = @status, status_reason = @status_reason, status_since = @status_since WHERE id = @id;

-- name: ListKubeWorkloadsByID :many
SELECT id, cluster_id, namespace, kind, name FROM kube_workloads WHERE id = ANY(@ids::uuid[]);

-- name: InsertKubeAccess :one
INSERT INTO kube_access (id, cluster_id, user_id, name, role, account, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: SetKubeAccessExpires :exec
UPDATE kube_access SET expires_at = $2 WHERE id = $1;

-- name: DeleteKubeAccess :exec
DELETE FROM kube_access WHERE id = $1;

-- name: GetKubeAccess :one
SELECT sqlc.embed(k), u.username, coalesce(b.username, '')::text AS revoked_by_name
FROM kube_access k
JOIN users u ON u.id = k.user_id
LEFT JOIN users b ON b.id = k.revoked_by
WHERE k.id = $1;

-- name: ListKubeAccess :many
-- De kubeconfigs van een cluster, de nieuwste eerst; met user_id alleen
-- die van die gebruiker. Verlopen en ingetrokken kubeconfigs blijven 30
-- dagen in de lijst.
SELECT sqlc.embed(k), u.username, coalesce(b.username, '')::text AS revoked_by_name
FROM kube_access k
JOIN users u ON u.id = k.user_id
LEFT JOIN users b ON b.id = k.revoked_by
WHERE k.cluster_id = @cluster_id
  AND (sqlc.narg(user_id)::uuid IS NULL OR k.user_id = sqlc.narg(user_id))
  AND (k.removed_at IS NULL OR greatest(k.expires_at, k.revoked_at) > now() - interval '30 days')
ORDER BY k.created_at DESC
LIMIT 200;

-- name: RevokeKubeAccess :one
-- Zet een kubeconfig op ingetrokken; een tweede keer verandert niets.
UPDATE kube_access SET revoked_at = now(), revoked_by = $2, revoke_reason = $3
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: ListKubeAccessToRemove :many
-- De ServiceAccounts die weg moeten: ingetrokken of verlopen, of van een
-- gebruiker die uitgeschakeld is of geen admin meer is bij een
-- admin-kubeconfig. reason is leeg als ze al ingetrokken of verlopen zijn.
SELECT k.id, k.account, k.name, k.role, k.user_id, u.username, k.revoked_at,
       (CASE
            WHEN k.revoked_at IS NOT NULL OR k.expires_at <= now() THEN ''
            WHEN u.disabled_at IS NOT NULL THEN 'de gebruiker is uitgeschakeld'
            ELSE 'de gebruiker is geen admin meer'
        END)::text AS reason
FROM kube_access k
JOIN users u ON u.id = k.user_id
WHERE k.cluster_id = $1 AND k.removed_at IS NULL
  AND (k.revoked_at IS NOT NULL OR k.expires_at <= now() OR u.disabled_at IS NOT NULL
       OR (k.role = 'admin' AND u.role <> 'admin'));

-- name: SetKubeAccessRemoved :exec
UPDATE kube_access SET removed_at = now(), remove_error = '' WHERE id = $1;

-- name: SetKubeAccessRemoveError :exec
UPDATE kube_access SET remove_error = $2 WHERE id = $1;

-- name: DeleteKubeSnapshots :exec
DELETE FROM kube_snapshots WHERE cluster_id = $1;

-- name: InsertKubeSnapshot :exec
INSERT INTO kube_snapshots (cluster_id, name, file, node, location, s3, size_bytes, taken_at, ready, error)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: SetKubeSnapshotsRead :exec
-- Zegt dat de lijst met snapshots gelezen is, of waarom dat niet lukte. Een
-- cluster dat nog nooit gesynchroniseerd is, heeft nog geen rij.
UPDATE kube_clusters
SET snapshots_at = CASE WHEN @error::text = '' THEN now() ELSE snapshots_at END, snapshots_error = @error::text
WHERE cluster_id = $1;

-- name: ListKubeSnapshots :many
-- De snapshots van een cluster, of van alle clusters als cluster_id leeg
-- is; de nieuwste eerst.
SELECT * FROM kube_snapshots
WHERE sqlc.narg(cluster_id)::uuid IS NULL OR cluster_id = sqlc.narg(cluster_id)::uuid
ORDER BY cluster_id, taken_at DESC NULLS LAST, name;

-- name: ListKubeSnapshotClusters :many
-- De Kubernetes-clusters met wanneer hun snapshots laatst gelezen werden.
SELECT c.id, c.name, c.slug, c.environment, k.snapshots_at, coalesce(k.snapshots_error, '')::text AS snapshots_error
FROM clusters c
LEFT JOIN kube_clusters k ON k.cluster_id = c.id
WHERE c.type = 'kubernetes'
ORDER BY lower(c.name);

-- name: SetKubeInventory :exec
-- Bewaart de namespaces, webadressen en opslag van een cluster.
UPDATE kube_clusters SET inventory = @inventory, inventory_at = now(), inventory_error = '' WHERE cluster_id = @cluster_id;

-- name: SetKubeInventoryError :exec
UPDATE kube_clusters SET inventory_error = @inventory_error WHERE cluster_id = @cluster_id;

-- name: ListKubeFacts :many
-- Per Kubernetes-cluster wat de sync laatst zag, voor de score en de
-- documentatie, met de nieuwste etcd-snapshot die klaar is.
SELECT c.id AS cluster_id, (k.cluster_id IS NOT NULL)::boolean AS synced, coalesce(k.api_ok, false)::boolean AS api_ok,
       coalesce(k.version, '')::text AS version, k.synced_at, k.ok_at, k.snapshots_at,
       coalesce(k.snapshots_error, '')::text AS snapshots_error,
       coalesce((SELECT max(s.taken_at) FROM kube_snapshots s WHERE s.cluster_id = c.id AND s.ready), 'epoch')::timestamptz AS newest_snapshot,
       (SELECT count(*) FROM kube_snapshots s WHERE s.cluster_id = c.id AND s.ready)::int AS snapshots,
       k.inventory, k.inventory_at, coalesce(k.inventory_error, '')::text AS inventory_error
FROM clusters c
LEFT JOIN kube_clusters k ON k.cluster_id = c.id
WHERE c.type = 'kubernetes'
ORDER BY c.id;

-- name: ListKubeNodeFacts :many
-- De Kubernetes-nodes van alle clusters, met hun ClusterForge-node.
SELECT k.cluster_id, k.name, k.node_id, coalesce(n.hostname, '')::text AS hostname, k.roles, k.ready, k.unschedulable, k.kubelet,
       k.internal_ip, k.cpu_millis, k.memory_bytes, k.pods
FROM kube_nodes k
LEFT JOIN nodes n ON n.id = k.node_id
ORDER BY k.cluster_id, k.name;

-- name: ListKubeAppFacts :many
-- De apps van alle clusters, voor de score en de documentatie.
SELECT id, cluster_id, namespace, kind, name, system, desired, ready, images, trouble, status, restarts, restarted_at, no_memory_limit
FROM kube_workloads
ORDER BY cluster_id, namespace, name, kind;
