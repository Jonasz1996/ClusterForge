-- name: ListChangesInScope :many
-- Het journaal van deze nodes, en van wat toen in deze clusters zat.
SELECT c.*, n.hostname, coalesce(cl.name, '')::text AS cluster_name
FROM node_changes c
JOIN nodes n ON n.id = c.node_id
LEFT JOIN clusters cl ON cl.id = c.cluster_id
WHERE (c.node_id = ANY(@node_ids::uuid[]) OR c.cluster_id = ANY(@cluster_ids::uuid[]))
  AND c.ts >= @since AND c.ts < @until
ORDER BY c.ts DESC, c.id DESC
LIMIT @lim;

-- name: ListIncidentsToAnalyze :many
-- Open incidenten, die sinds @since sloten, en die nog nooit geanalyseerd
-- werden. Daarna ligt de analyse vast.
SELECT * FROM incidents
WHERE status = 'open' OR resolved_at >= @since::timestamptz OR analyzed_at IS NULL
ORDER BY started_at DESC
LIMIT 50;

-- name: SetIncidentAnalysis :exec
-- Alleen als de analyse veranderde: elke update van incidents is een live
-- update in de webinterface.
UPDATE incidents SET analysis = @analysis, analysis_version = @analysis_version, analyzed_at = @analyzed_at
WHERE id = @id;
