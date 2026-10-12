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
INSERT INTO kube_workloads (cluster_id, namespace, kind, name, system, desired, ready, up_to_date, images, trouble, short_since, seen_at)
VALUES (@cluster_id, @namespace, @kind, @name, @system, @desired, @ready, @up_to_date, @images, @trouble,
        CASE WHEN @short::boolean THEN now() END, @seen_at::timestamptz)
ON CONFLICT (cluster_id, namespace, kind, name) DO UPDATE
SET system = EXCLUDED.system, desired = EXCLUDED.desired, ready = EXCLUDED.ready, up_to_date = EXCLUDED.up_to_date,
    images = EXCLUDED.images, trouble = EXCLUDED.trouble,
    short_since = CASE WHEN @short::boolean THEN coalesce(kube_workloads.short_since, now()) END,
    seen_at = EXCLUDED.seen_at;

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
