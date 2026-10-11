-- name: ListRiskClusters :many
-- Zonder niet toegepaste revisie of lifecycle-event is de tijd 1970 (epoch).
-- De clusters met wat de score over hun configuratie nodig heeft. Een
-- revisie die nog niet toegepast is, telt vanaf de eerste niet toegepaste;
-- zolang een taak het cluster verandert, is ze bezig.
SELECT c.id, c.name, c.environment::text AS environment, c.status, c.status_reason, c.created_at,
       c.spec_revision, c.applied_revision,
       (c.template_name IS NOT NULL OR coalesce(c.spec ->> 'kind', '') = 'baseline')::boolean AS desired_state,
       coalesce((SELECT min(r.created_at) FROM cluster_spec_revisions r
                 WHERE r.cluster_id = c.id AND r.revision > c.applied_revision), 'epoch')::timestamptz AS revision_since,
       EXISTS (SELECT 1 FROM jobs j WHERE j.cluster_id = c.id AND j.cluster_slot
               AND j.status IN ('queued', 'running'))::boolean AS busy
FROM clusters c
ORDER BY c.name;

-- name: ListRiskNodes :many
-- De nodes in een cluster, met hun Proxmox-host, facts, agent en sinds
-- wanneer hun lifecycle zo staat.
SELECT n.id, n.cluster_id::uuid AS cluster_id, n.hostname, n.lifecycle::text AS lifecycle,
       coalesce(r.pve_node, '')::text AS host,
       f.facts,
       coalesce(a.protocol_version, 0)::int AS protocol_version,
       a.enrolled_at AS agent_enrolled_at,
       coalesce((SELECT max(e.ts) FROM events e
                 WHERE e.subject_type = 'node' AND e.subject_id = n.id::text
                   AND (e.action = 'node.lifecycle_changed' OR (e.action = 'node.updated' AND e.payload ? 'lifecycle'))),
                'epoch')::timestamptz AS lifecycle_since
FROM nodes n
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
LEFT JOIN node_facts f ON f.node_id = n.id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
WHERE n.cluster_id IS NOT NULL
ORDER BY n.hostname;

-- name: ListUpdateChanges :many
-- Hoeveel beveiligingsupdates er volgens het journaal open stonden, nieuwste
-- eerst, om te zien sinds wanneer.
SELECT node_id, ts, coalesce(detail ->> 'security', '')::text AS security
FROM node_changes
WHERE kind = 'facts' AND subject = 'updates' AND ts >= @since
ORDER BY node_id, ts DESC;

-- name: ListClusterStatusHistory :many
-- De statusovergangen van de clusters sinds @since, met per cluster de
-- laatste ervoor, oudste eerst.
SELECT * FROM (
    SELECT e.cluster_id::uuid AS cluster_id, e.ts, coalesce(e.payload ->> 'to', '')::text AS status
    FROM events e
    WHERE e.action = 'cluster.status_changed' AND e.cluster_id IS NOT NULL AND e.ts >= @since
    UNION ALL
    (SELECT DISTINCT ON (e.cluster_id) e.cluster_id::uuid, e.ts, coalesce(e.payload ->> 'to', '')::text
     FROM events e
     WHERE e.action = 'cluster.status_changed' AND e.cluster_id IS NOT NULL AND e.ts < @since
     ORDER BY e.cluster_id, e.ts DESC)
) h
ORDER BY h.cluster_id, h.ts;

-- name: CountClusterIncidents :many
-- Het aantal incidenten per cluster sinds @since, zonder die van tests.
SELECT s.cluster_id::uuid AS cluster_id, count(DISTINCT i.id)::int AS incidents
FROM incident_subjects s
JOIN incidents i ON i.id = s.incident_id
WHERE s.cluster_id IS NOT NULL AND i.started_at >= @since AND NOT i.caused_by_test
GROUP BY s.cluster_id;

-- name: CountVIPMoves :many
-- Hoe vaak elk VIP sinds @since van eigenaar wisselde, zonder de wissels
-- tijdens een test op dat cluster.
SELECT e.subject_id::uuid AS vip_id, count(*)::int AS moves
FROM events e
WHERE e.action = 'vip.owner_changed' AND e.ts >= @since
  AND NOT EXISTS (
      SELECT 1 FROM test_runs r
      WHERE r.cluster_id = e.cluster_id
        AND e.ts BETWEEN r.created_at AND coalesce(r.finished_at, now()) + interval '5 minutes')
GROUP BY e.subject_id;

-- name: ListRiskFailoverTests :many
-- De failovertests met hun laatste afgeronde run.
SELECT t.id, t.cluster_id, t.vip_id, t.name,
       coalesce(lr.id, '00000000-0000-0000-0000-000000000000')::uuid AS run_id, lr.result AS run_result, coalesce(lr.summary, '')::text AS run_summary, lr.finished_at AS run_finished
FROM failover_tests t
LEFT JOIN LATERAL (
    SELECT r.id, r.result, r.summary, r.finished_at FROM test_runs r
    WHERE r.test_id = t.id AND r.result IN ('pass', 'warning', 'fail', 'error') AND r.finished_at IS NOT NULL
    ORDER BY r.finished_at DESC
    LIMIT 1
) lr ON true
ORDER BY t.name;

-- name: ListRiskUnrestoredRuns :many
SELECT r.id, r.cluster_id::uuid AS cluster_id, coalesce(t.name, r.hostname)::text AS name,
       coalesce(r.finished_at, r.created_at)::timestamptz AS finished
FROM test_runs r
LEFT JOIN failover_tests t ON t.id = r.test_id
WHERE r.restored = false AND r.kind = 'failover.test' AND r.cluster_id IS NOT NULL
ORDER BY r.created_at;

-- name: ListNonProdDependencies :many
-- Harde afhankelijkheden van een prod-dienst op een dienst in een lab- of
-- testcluster.
SELECT d.id, fc.id AS from_cluster_id, fs.name AS from_name, ts.name AS to_name,
       tc.name AS to_cluster, tc.environment::text AS to_env
FROM service_dependencies d
JOIN services fs ON fs.id = d.from_service_id
JOIN services ts ON ts.id = d.to_service_id
LEFT JOIN nodes fn ON fn.id = fs.node_id
JOIN clusters fc ON fc.id = coalesce(fs.cluster_id, fn.cluster_id)
LEFT JOIN nodes tn ON tn.id = ts.node_id
JOIN clusters tc ON tc.id = coalesce(ts.cluster_id, tn.cluster_id)
WHERE d.state = 'confirmed' AND d.strength = 'hard' AND fs.state = 'confirmed' AND ts.state = 'confirmed'
  AND fc.environment = 'prod' AND tc.environment IN ('lab', 'test')
ORDER BY fs.name, ts.name;

-- name: ListRiskDrift :many
SELECT d.node_id, n.cluster_id::uuid AS cluster_id, n.hostname,
       jsonb_array_length(d.findings)::int AS findings, d.drift_since
FROM drift_checks d
JOIN nodes n ON n.id = d.node_id
WHERE d.status = 'drift' AND n.cluster_id IS NOT NULL;

-- name: ListWildcardIgnores :many
SELECT i.id, i.cluster_id, coalesce(n.hostname, '')::text AS hostname, i.expires_at::timestamptz AS expires_at
FROM drift_ignores i
LEFT JOIN nodes n ON n.id = i.node_id
WHERE i.key = '*' AND i.expires_at > @now;

-- name: ListRiskGitChanges :many
-- De laatste Git-wijziging per cluster.
SELECT DISTINCT ON (g.cluster_id) g.id, g.cluster_id::uuid AS cluster_id, g.commit_sha, g.status, g.reason, g.updated_at
FROM git_changes g
WHERE g.cluster_id IS NOT NULL
ORDER BY g.cluster_id, g.created_at DESC;

-- name: ListAdminsWithoutTOTP :many
SELECT id, username FROM users
WHERE role = 'admin' AND disabled_at IS NULL AND totp_enabled_at IS NULL
ORDER BY lower(username);

-- name: ListRiskProxmox :many
SELECT id, name, last_error, last_sync_at, created_at, cert_not_after
FROM proxmox_connections
ORDER BY lower(name);

-- name: SetProxmoxCertNotAfter :exec
UPDATE proxmox_connections SET cert_not_after = @cert_not_after WHERE id = @id;

-- name: ListRiskAcceptances :many
SELECT a.*, coalesce(u.username, '')::text AS created_by_name
FROM risk_acceptances a
LEFT JOIN users u ON u.id = a.created_by
ORDER BY a.created_at;

-- name: GetRiskAcceptance :one
SELECT * FROM risk_acceptances WHERE id = $1;

-- name: InsertRiskAcceptance :one
INSERT INTO risk_acceptances ("check", subject_type, subject_id, subject_name, reason, expires_at, created_by)
VALUES (@check_id, @subject_type, @subject_id, @subject_name, @reason, @expires_at, @created_by)
ON CONFLICT ("check", subject_type, subject_id) DO UPDATE
SET reason = EXCLUDED.reason, expires_at = EXCLUDED.expires_at, subject_name = EXCLUDED.subject_name,
    created_by = EXCLUDED.created_by, created_at = now()
RETURNING *;

-- name: DeleteRiskAcceptance :one
DELETE FROM risk_acceptances WHERE id = $1 RETURNING *;

-- name: ListRiskScores :many
SELECT * FROM risk_scores;

-- name: GetRiskScore :one
SELECT * FROM risk_scores WHERE subject_type = @subject_type AND subject_id = @subject_id;

-- name: UpsertRiskScore :exec
INSERT INTO risk_scores (subject_type, subject_id, score, grade, result, computed_at, announced_grade, grade_since)
VALUES (@subject_type, @subject_id, @score, @grade, @result, @computed_at, @announced_grade, @grade_since)
ON CONFLICT (subject_type, subject_id) DO UPDATE
SET score = EXCLUDED.score, grade = EXCLUDED.grade, result = EXCLUDED.result, computed_at = EXCLUDED.computed_at,
    announced_grade = EXCLUDED.announced_grade, grade_since = EXCLUDED.grade_since;

-- name: DeleteStaleRiskScores :exec
-- Scores van clusters die niet meer bestaan.
DELETE FROM risk_scores
WHERE subject_type = 'cluster' AND NOT (subject_id = ANY(@keep::uuid[]));

-- name: SnapshotRiskScores :execrows
-- De momentopname van vandaag, één keer per dag per onderwerp.
INSERT INTO risk_snapshots (day, subject_type, subject_id, score, grade, cards)
SELECT @day::date, s.subject_type, s.subject_id, s.score, s.grade, coalesce(s.result -> 'cards', '[]'::jsonb)
FROM risk_scores s
ON CONFLICT (subject_type, subject_id, day) DO NOTHING;

-- name: ListRiskSnapshots :many
SELECT day, score, grade FROM risk_snapshots
WHERE subject_type = @subject_type AND subject_id = @subject_id AND day >= @since::date
ORDER BY day;
