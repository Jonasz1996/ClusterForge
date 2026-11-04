-- name: ListBlueprints :many
-- Alle versies, oudste eerst; de server zet ze bij het starten in het
-- register van templates.
SELECT * FROM blueprints ORDER BY name, created_at, version;

-- name: LatestBlueprint :one
SELECT * FROM blueprints WHERE name = @name
ORDER BY created_at DESC, version DESC LIMIT 1;

-- name: InsertBlueprint :one
INSERT INTO blueprints (name, version, title, composition, template_yaml, created_by)
VALUES (@name, @version, @title, @composition, @template_yaml, @created_by)
RETURNING *;
