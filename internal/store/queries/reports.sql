-- name: ListReportClusters :many
-- De clusters die er voor @before al waren, prod eerst.
SELECT c.id, c.name, c.slug, c.environment, c.rto_target_minutes, c.rpo_target_hours
FROM clusters c
WHERE c.created_at < @before
ORDER BY CASE c.environment WHEN 'prod' THEN 0 WHEN 'test' THEN 1 ELSE 2 END, lower(c.name), c.id;

-- name: ListReportRuns :many
-- De afgeronde tests in [@since, @before), per cluster dat ze raakten.
SELECT r.id, cl.cluster_id::uuid AS cluster_id, r.kind, r.trigger, r.result::text AS result, r.summary,
       r.finished_at::timestamptz AS finished_at, r.hostname, r.measurements,
       coalesce(t.name, d.name, r.definition->>'name', r.hostname)::text AS name
FROM test_runs r
CROSS JOIN LATERAL (SELECT DISTINCT x AS cluster_id FROM unnest(array_append(r.cluster_ids, r.cluster_id)) AS x WHERE x IS NOT NULL) cl
LEFT JOIN failover_tests t ON t.id = r.test_id
LEFT JOIN disaster_scenarios d ON d.id = r.scenario_id
WHERE r.finished_at >= @since::timestamptz AND r.finished_at < @before::timestamptz AND r.result IS NOT NULL
  AND r.kind IN ('backup.verify', 'failover.test', 'disaster.run', 'dr.rehearsal')
ORDER BY r.finished_at, r.id, cl.cluster_id;

-- name: ListReportMeasuredRehearsals :many
-- Per cluster de laatste DR-repetitie voor @before die de RTO mat.
SELECT DISTINCT ON (r.cluster_id) r.cluster_id::uuid AS cluster_id, r.id, r.result::text AS result,
       r.finished_at::timestamptz AS finished_at, r.measurements, coalesce(r.definition->>'name', '')::text AS name
FROM test_runs r
WHERE r.kind = 'dr.rehearsal' AND r.cluster_id IS NOT NULL AND r.finished_at < @before::timestamptz
  AND r.result IN ('pass', 'warning') AND r.measurements ? 'rto_seconds'
ORDER BY r.cluster_id, r.finished_at DESC, r.id;

-- name: GetRecoveryReport :one
SELECT * FROM recovery_reports WHERE month = @month;

-- name: InsertRecoveryReport :execrows
-- Een bewaard rapport blijft zoals het is.
INSERT INTO recovery_reports (month, report, generated_at) VALUES (@month, @report, @generated_at)
ON CONFLICT (month) DO NOTHING;

-- name: ListRecoveryReportMonths :many
SELECT month FROM recovery_reports ORDER BY month DESC;
