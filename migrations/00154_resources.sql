-- +goose Up
-- Testlijst 3, N27: drempels voor de voorstellen over resources (schijf,
-- swap, geheugen, load), per omgeving.
CREATE TABLE resources_settings (
    id         boolean PRIMARY KEY DEFAULT true CHECK (id),
    thresholds jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE resources_settings;
