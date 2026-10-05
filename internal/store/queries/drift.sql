-- name: ListDriftNodes :many
-- De nodes van clusters met een gewenste staat, met wat de scanner moet
-- weten om te beslissen of hij ze controleert. Zonder cluster_id alle.
SELECT n.id, n.hostname, n.cluster_id, n.lifecycle,
       coalesce(a.protocol_version, 0)::int AS agent_protocol,
       (a.id IS NOT NULL)::boolean AS has_agent,
       s.heartbeat_at
FROM nodes n
JOIN clusters c ON c.id = n.cluster_id AND c.template_name IS NOT NULL
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
WHERE (sqlc.narg('cluster_id')::uuid IS NULL OR n.cluster_id = sqlc.narg('cluster_id'))
  AND (sqlc.narg('node_id')::uuid IS NULL OR n.id = sqlc.narg('node_id'))
ORDER BY n.hostname;

-- name: NodeJobBusy :one
-- Er wacht of loopt een taak op deze node, of een schrijvende taak in zijn
-- cluster. De agent doet één commando tegelijk; een controle wacht niet.
SELECT EXISTS (
    SELECT 1 FROM jobs
    WHERE status IN ('queued', 'running')
      AND (node_id = @node_id OR (cluster_slot AND cluster_id = sqlc.narg('cluster_id')::uuid))
)::boolean;

-- name: LockDriftNode :exec
-- Eén schrijver per node, zoals de evaluator, zodat er nooit een dubbel
-- event komt.
SELECT pg_advisory_xact_lock(hashtextextended('drift:' || sqlc.arg('node_id')::text, 0));

-- name: GetDriftCheck :one
SELECT * FROM drift_checks WHERE node_id = $1;

-- name: UpsertDriftCheck :exec
INSERT INTO drift_checks (node_id, status, source, spec_revision, template_version, findings, unchecked,
                          fingerprint, error, checked_at, drift_since)
VALUES (@node_id, @status, @source, @spec_revision, @template_version, @findings, @unchecked,
        @fingerprint, @error, @checked_at, @drift_since)
ON CONFLICT (node_id) DO UPDATE
SET status = EXCLUDED.status, source = EXCLUDED.source, spec_revision = EXCLUDED.spec_revision,
    template_version = EXCLUDED.template_version, findings = EXCLUDED.findings, unchecked = EXCLUDED.unchecked,
    fingerprint = EXCLUDED.fingerprint, error = EXCLUDED.error, checked_at = EXCLUDED.checked_at,
    drift_since = EXCLUDED.drift_since;

-- name: ListClusterDriftChecks :many
SELECT d.* FROM drift_checks d JOIN nodes n ON n.id = d.node_id WHERE n.cluster_id = $1;
