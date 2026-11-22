-- +goose Up
-- Recepten (fase 9): hoe software op de nodes van een cluster komt, in een
-- tekstformaat dat op een Dockerfile lijkt (internal/recipes). Elke versie
-- is een eigen rij en verandert niet meer; een nieuwe tekst is een nieuwe
-- versie. Er staan nooit geheimen in, alleen {{ secret.naam }}.
CREATE TABLE recipe_versions (
    name        text NOT NULL,
    version     text NOT NULL,
    source      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (name, version)
);

-- +goose Down
DROP TABLE recipe_versions;
