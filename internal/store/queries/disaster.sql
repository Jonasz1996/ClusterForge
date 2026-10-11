-- name: ListDisasterScenarios :many
SELECT * FROM disaster_scenarios WHERE cluster_id = @cluster_id ORDER BY created_at, id;

-- name: GetDisasterScenario :one
SELECT * FROM disaster_scenarios WHERE id = @id;

-- name: InsertDisasterScenario :one
INSERT INTO disaster_scenarios (cluster_id, name, kind, params, expect, created_by)
VALUES (@cluster_id, @name, @kind, @params, @expect, @created_by)
RETURNING *;

-- name: UpdateDisasterScenario :one
UPDATE disaster_scenarios
SET name = @name, kind = @kind, params = @params, expect = @expect, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteDisasterScenario :execrows
DELETE FROM disaster_scenarios WHERE id = @id;

-- name: LatestDisasterRuns :many
-- De laatste run per scenario van een cluster.
SELECT DISTINCT ON (r.scenario_id) sqlc.embed(r), j.status AS job_status
FROM test_runs r
JOIN disaster_scenarios d ON d.id = r.scenario_id
LEFT JOIN jobs j ON j.id = r.job_id
WHERE d.cluster_id = @cluster_id
ORDER BY r.scenario_id, r.created_at DESC;

-- name: InsertDisasterRun :one
INSERT INTO test_runs (kind, trigger, cluster_id, scenario_id, cluster_ids, definition, checks, requested_by)
VALUES ('disaster.run', 'manual', @cluster_id, @scenario_id, @cluster_ids, @definition, @checks, @requested_by)
RETURNING *;

-- name: ListDisasterClusters :many
-- De status van de clusters die een rampoefening meet, uit de evaluator,
-- met de ergste bevestigde dienst erin: zijn eigen status, of de impact van
-- een dienst waarvan hij afhangt. Zo ziet de meting ook een cluster zonder
-- VIP dat door een ander cluster geraakt wordt.
SELECT c.id, c.name, c.status, c.status_reason,
       coalesce(w.status, 'healthy')::text AS service_status, coalesce(w.reason, '')::text AS service_reason
FROM clusters c
LEFT JOIN LATERAL (
    SELECT x.status, x.reason FROM (
        SELECT CASE WHEN s.status = 'down' OR s.impact = 'down' THEN 'down' ELSE 'degraded' END AS status,
               s.name || ': ' || CASE WHEN s.status IN ('down', 'degraded') THEN s.status_reason ELSE s.impact_reason END AS reason
        FROM services s
        LEFT JOIN nodes n ON n.id = s.node_id
        WHERE s.state = 'confirmed' AND (s.cluster_id = c.id OR n.cluster_id = c.id)
          AND (s.status IN ('down', 'degraded') OR s.impact <> 'none')
    ) x
    ORDER BY (x.status = 'down') DESC, x.reason
    LIMIT 1
) w ON true
WHERE c.id = ANY(@ids::uuid[]);

-- name: ListFaultFacts :many
-- Wat een reeks van storingen per node uit de laatste facts nodig heeft: de
-- interfaces, het adres waarmee de agent ClusterForge bereikt, nft en tc,
-- en de storingen die nu actief zijn.
SELECT f.node_id,
       coalesce(f.facts -> 'interfaces', '[]'::jsonb)::jsonb AS interfaces,
       coalesce(f.facts ->> 'primary_address', '')::text AS primary_address,
       coalesce(f.facts -> 'tools', 'null'::jsonb)::jsonb AS tools,
       coalesce(f.facts -> 'faults', '[]'::jsonb)::jsonb AS faults
FROM node_facts f
JOIN nodes n ON n.id = f.node_id
WHERE n.cluster_id = @cluster_id;
