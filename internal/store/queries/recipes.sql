-- name: ListRecipeVersions :many
-- Alle versies van alle recepten, zonder de tekst: nieuwste eerst per recept.
SELECT name, version, created_at, created_by FROM recipe_versions
ORDER BY name, created_at DESC, version DESC;

-- name: ListVersionsOfRecipe :many
SELECT * FROM recipe_versions WHERE name = @name
ORDER BY created_at DESC, version DESC;

-- name: GetRecipeVersion :one
SELECT * FROM recipe_versions WHERE name = @name AND version = @version;

-- name: InsertRecipeVersion :one
INSERT INTO recipe_versions (name, version, source, created_by)
VALUES (@name, @version, @source, @created_by)
ON CONFLICT (name, version) DO NOTHING
RETURNING *;

-- name: DeleteRecipeVersion :execrows
DELETE FROM recipe_versions WHERE name = @name AND version = @version;
