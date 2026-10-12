-- name: ListDBCandidateNodes :many
-- De nodes met een agent waarvan de facts een databaseserver tonen, of die
-- al een rij in db_servers hebben. db_units zijn die units uit de facts.
SELECT n.id, n.hostname, a.protocol_version AS agent_protocol, s.heartbeat_at,
       coalesce((SELECT array_agg(DISTINCT x ->> 'name' ORDER BY x ->> 'name')
                 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(f.facts -> 'services') = 'array' THEN f.facts -> 'services' ELSE '[]'::jsonb END) x
                 WHERE x ->> 'name' IN ('mariadb', 'mysql', 'postgresql', 'patroni') OR x ->> 'name' LIKE 'postgresql@%'),
                '{}')::text[] AS db_units,
       coalesce((SELECT array_agg(d.engine ORDER BY d.engine) FROM db_servers d WHERE d.node_id = n.id), '{}')::text[] AS known_engines
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
WHERE EXISTS (SELECT 1
              FROM jsonb_array_elements(CASE WHEN jsonb_typeof(f.facts -> 'services') = 'array' THEN f.facts -> 'services' ELSE '[]'::jsonb END) x
              WHERE x ->> 'name' IN ('mariadb', 'mysql', 'postgresql', 'patroni') OR x ->> 'name' LIKE 'postgresql@%')
   OR EXISTS (SELECT 1 FROM db_servers d WHERE d.node_id = n.id)
ORDER BY lower(n.hostname);

-- name: UpsertDBServer :exec
-- Een antwoord van de agent: de hele status.
INSERT INTO db_servers (node_id, engine, unit, active, running, version, role, status, databases_total, error, error_kind, synced_at, ok_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, '', $11, $11)
ON CONFLICT (node_id, engine) DO UPDATE
SET unit = excluded.unit, active = excluded.active, running = excluded.running, version = excluded.version, role = excluded.role,
    status = excluded.status, databases_total = excluded.databases_total, error = excluded.error, error_kind = '',
    synced_at = excluded.synced_at, ok_at = excluded.ok_at;

-- name: MarkDBServer :exec
-- ClusterForge kon het de agent niet vragen: alleen de fout en het
-- tijdstip veranderen, de laatst bekende status blijft.
INSERT INTO db_servers (node_id, engine, error, error_kind, synced_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (node_id, engine) DO UPDATE
SET error = excluded.error, error_kind = excluded.error_kind, synced_at = excluded.synced_at;

-- name: DeleteDBServersExcept :exec
-- De engines die de agent niet meer meldt, gaan weg.
DELETE FROM db_servers WHERE node_id = $1 AND NOT (engine = ANY (sqlc.arg(engines)::text[]));

-- name: DeleteDBDatabases :exec
DELETE FROM db_databases WHERE node_id = $1 AND engine = $2;

-- name: InsertDBDatabase :exec
INSERT INTO db_databases (node_id, engine, name, size_bytes, tables) VALUES ($1, $2, $3, $4, $5);

-- name: ListDBServers :many
-- Alle databaseservers met hun node en cluster, voor het menu Databases.
SELECT d.node_id, n.hostname, n.cluster_id, c.name AS cluster_name, c.slug AS cluster_slug, c.environment AS cluster_environment,
       d.engine, d.unit, d.active, d.running, d.version, d.role, d.status, d.databases_total, d.error, d.error_kind, d.synced_at, d.ok_at
FROM db_servers d
JOIN nodes n ON n.id = d.node_id
LEFT JOIN clusters c ON c.id = n.cluster_id
ORDER BY lower(coalesce(c.name, n.hostname)), d.engine, lower(n.hostname);

-- name: ListDBDatabases :many
SELECT node_id, engine, name, size_bytes, tables FROM db_databases ORDER BY name;
