-- name: ListServices :many
SELECT * FROM services ORDER BY lower(name), id;

-- name: GetService :one
SELECT * FROM services WHERE id = $1;

-- name: LockService :one
SELECT * FROM services WHERE id = $1 FOR UPDATE;

-- name: FindServiceByName :one
-- De dienst met deze naam in dezelfde scope: een cluster, een losse node of
-- extern (beide NULL).
SELECT * FROM services
WHERE cluster_id IS NOT DISTINCT FROM sqlc.narg('cluster_id')::uuid
  AND node_id IS NOT DISTINCT FROM sqlc.narg('node_id')::uuid
  AND lower(name) = lower(@name);

-- name: InsertService :one
INSERT INTO services (cluster_id, node_id, name, kind, unit, port, address, description, source, state, last_seen_at)
VALUES (@cluster_id, @node_id, @name, @kind, @unit, @port, @address, @description, @source, @state, @last_seen_at)
RETURNING *;

-- name: UpdateService :one
UPDATE services
SET name = @name, kind = @kind, unit = @unit, port = @port, address = @address, description = @description,
    source = @source, state = @state, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteService :exec
DELETE FROM services WHERE id = $1;

-- name: TouchServices :exec
-- De resolver zag deze units nog; de staat van de rijen blijft wat ze was.
UPDATE services SET last_seen_at = @seen_at
WHERE cluster_id = @cluster_id AND unit = ANY(@units::text[]);

-- name: ListServiceDependencies :many
SELECT * FROM service_dependencies ORDER BY created_at, id;

-- name: GetServiceDependency :one
SELECT * FROM service_dependencies WHERE id = $1;

-- name: LockServiceDependency :one
SELECT * FROM service_dependencies WHERE id = $1 FOR UPDATE;

-- name: InsertServiceDependency :one
INSERT INTO service_dependencies (from_service_id, to_service_id, strength, source, state, note)
VALUES (@from_service_id, @to_service_id, @strength, @source, @state, @note)
RETURNING *;

-- name: UpdateServiceDependency :one
UPDATE service_dependencies SET strength = @strength, note = @note, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteServiceDependency :exec
DELETE FROM service_dependencies WHERE id = $1;

-- name: DeleteDependenciesOfService :many
DELETE FROM service_dependencies WHERE from_service_id = @service_id OR to_service_id = @service_id
RETURNING *;

-- name: ListDepClusters :many
SELECT id, name, slug, environment, type, status, status_reason FROM clusters ORDER BY name;
