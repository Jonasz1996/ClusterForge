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
SET api_ok = true, error = '', version = EXCLUDED.version, nodes_total = EXCLUDED.nodes_total, nodes_ready = EXCLUDED.nodes_ready,
    pods_total = EXCLUDED.pods_total, pods_running = EXCLUDED.pods_running, pods_pending = EXCLUDED.pods_pending,
    pods_problem = EXCLUDED.pods_problem, workloads_total = EXCLUDED.workloads_total, workloads_ready = EXCLUDED.workloads_ready,
    via_node_id = EXCLUDED.via_node_id, synced_at = now(), ok_at = now();

-- name: SaveKubeClusterError :exec
-- Bewaart een mislukte sync; de laatst bekende tellingen blijven staan.
INSERT INTO kube_clusters (cluster_id, api_ok, error, synced_at)
VALUES ($1, false, $2, now())
ON CONFLICT (cluster_id) DO UPDATE SET api_ok = false, error = EXCLUDED.error, synced_at = now();

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
