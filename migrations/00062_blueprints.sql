-- +goose Up
-- Zelf samengestelde clusters (fase 9): rollen met elk een recept, een VIP
-- en een database worden een gewone template. De gegenereerde template staat
-- hier als tekst, zodat een cluster altijd rendert met de versie waarmee het
-- is uitgerold, ook als het recept later verandert of weg is. Een nieuwe
-- samenstelling is een nieuwe versie. Er staan geen geheimen in.
CREATE TABLE blueprints (
    name          text NOT NULL,
    version       text NOT NULL,
    title         text NOT NULL,
    composition   jsonb NOT NULL,
    template_yaml text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (name, version)
);

-- +goose Down
DROP TABLE blueprints;
