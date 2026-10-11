-- name: ListIncidentClusters :many
SELECT id, name, environment::text AS environment, status, status_reason, status_since FROM clusters;

-- name: ListIncidentNodes :many
-- De nodes met hun Proxmox-host als '<koppeling>/<host>', of leeg.
SELECT n.id, n.hostname, n.cluster_id, n.lifecycle, n.status, n.status_reason, n.status_since,
       coalesce(r.connection_id::text || '/' || r.pve_node, '')::text AS host
FROM nodes n
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc');

-- name: ListIncidentServices :many
-- Bevestigde diensten met minstens één bevestigde afnemer.
SELECT s.id, s.name, s.cluster_id, s.node_id, coalesce(s.status, 'unknown')::text AS status, s.status_reason
FROM services s
WHERE s.state = 'confirmed' AND EXISTS (
    SELECT 1 FROM service_dependencies d
    JOIN services c ON c.id = d.from_service_id
    WHERE d.to_service_id = s.id AND d.state = 'confirmed' AND c.state = 'confirmed'
);

-- name: ListClusterDependencies :many
-- Welke clusters van welke afhangen, via bevestigde afhankelijkheden.
SELECT DISTINCT f.cluster_id AS from_cluster, t.cluster_id AS to_cluster
FROM service_dependencies d
JOIN services f ON f.id = d.from_service_id
JOIN services t ON t.id = d.to_service_id
WHERE d.state = 'confirmed' AND f.cluster_id IS NOT NULL AND t.cluster_id IS NOT NULL AND f.cluster_id <> t.cluster_id;

-- name: ListIncidentTestRuns :many
-- Geplande tests die mislukten sinds @failed_since, en failovertests die
-- lopen of na @running_since klaar waren.
SELECT r.id, r.kind, r.cluster_id, coalesce(c.name, '')::text AS cluster_name, r.hostname,
       coalesce(r.result, '')::text AS result, r.summary, r.created_at, r.finished_at,
       (r.trigger = 'schedule' AND r.result IN ('fail', 'error') AND r.finished_at >= @failed_since::timestamptz)::boolean AS failed
FROM test_runs r
LEFT JOIN clusters c ON c.id = r.cluster_id
WHERE (r.trigger = 'schedule' AND r.result IN ('fail', 'error') AND r.finished_at >= @failed_since::timestamptz)
   OR (r.kind = 'failover.test' AND (r.finished_at IS NULL OR r.finished_at >= @running_since::timestamptz));

-- name: ListDetectorIncidents :many
-- De open incidenten en die sinds @since afgesloten werden.
SELECT * FROM incidents WHERE status = 'open' OR resolved_at >= @since::timestamptz ORDER BY started_at, number;

-- name: ListIncidentSubjectsOf :many
SELECT * FROM incident_subjects WHERE incident_id = ANY(@ids::uuid[]) ORDER BY since, subject_type, name;

-- name: CreateIncident :one
INSERT INTO incidents (title, severity, status, started_at, detected_at, caused_by_test, test_run_id)
VALUES (@title, @severity, 'open', @started_at, @detected_at, @caused_by_test, @test_run_id)
RETURNING *;

-- name: UpdateIncidentState :exec
UPDATE incidents
SET severity = @severity, status = @status, resolved_at = @resolved_at, started_at = @started_at, updated_at = now()
WHERE id = @id;

-- name: UpsertIncidentSubject :exec
INSERT INTO incident_subjects (incident_id, subject_type, subject_id, name, cluster_id, first_status, worst_status,
                               last_status, reason, since, until)
VALUES (@incident_id, @subject_type, @subject_id, @name, @cluster_id, @first_status, @worst_status, @last_status,
        @reason, @since, @until)
ON CONFLICT (incident_id, subject_type, subject_id) DO UPDATE
SET name = EXCLUDED.name, worst_status = EXCLUDED.worst_status, last_status = EXCLUDED.last_status,
    reason = EXCLUDED.reason, until = EXCLUDED.until;

-- name: ListIncidents :many
-- Open incidenten altijd, afgesloten die in de periode begonnen. Met
-- @cluster_id alleen die het cluster raakten.
SELECT * FROM incidents i
WHERE (sqlc.narg(status)::text IS NULL OR i.status = sqlc.narg(status)::text)
  AND (@include_tests::boolean OR NOT i.caused_by_test)
  AND (i.status = 'open' OR (i.started_at >= @since AND i.started_at < @until))
  AND (sqlc.narg(cluster_id)::uuid IS NULL OR EXISTS (
      SELECT 1 FROM incident_subjects s WHERE s.incident_id = i.id AND s.cluster_id = sqlc.narg(cluster_id)::uuid
  ))
ORDER BY (i.status = 'open') DESC, i.started_at DESC, i.number DESC
LIMIT @lim;

-- name: GetIncident :one
SELECT * FROM incidents WHERE id = $1;

-- name: ListIncidentSubjects :many
-- De onderwerpen, en of ze nog bestaan.
SELECT s.*,
       (CASE s.subject_type
            WHEN 'cluster' THEN EXISTS (SELECT 1 FROM clusters WHERE id = s.subject_id)
            WHEN 'node' THEN EXISTS (SELECT 1 FROM nodes WHERE id = s.subject_id)
            WHEN 'service' THEN EXISTS (SELECT 1 FROM services WHERE id = s.subject_id)
            ELSE EXISTS (SELECT 1 FROM test_runs WHERE id = s.subject_id)
        END)::boolean AS present
FROM incident_subjects s
WHERE s.incident_id = ANY(@ids::uuid[])
ORDER BY s.since, s.subject_type, s.name;

-- name: SetIncidentNote :one
UPDATE incidents SET note = @note, updated_at = now() WHERE id = @id RETURNING *;
