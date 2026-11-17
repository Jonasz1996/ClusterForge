-- +goose Up
-- Fase 28 M1: Docker zoals Portainer. Per node met Docker de laatste
-- samenvatting die de server elke minuut via de agent leest (aantallen,
-- versie, schijfgebruik), voor de pagina Docker, Overzicht en Aandacht nodig.
CREATE TABLE docker_hosts (
    node_id    uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    summary    jsonb NOT NULL DEFAULT '{}',
    error      text NOT NULL DEFAULT '',
    checked_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE docker_hosts;
