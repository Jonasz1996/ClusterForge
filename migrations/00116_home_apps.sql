-- +goose Up
-- Fase 36: apps voor de homepage-API v2. ClusterForge herkent apps zelf
-- (internal/homeapps); deze tabel onthoudt wanneer een app voor het eerst
-- en voor het laatst gezien is (zodat een verdwenen app nog 24 uur als
-- "weg?" op de homepage staat) en wat Jonas in de GUI bijstuurde.
CREATE TABLE home_apps (
    key          text PRIMARY KEY,
    name         text NOT NULL DEFAULT '',
    grp          text NOT NULL DEFAULT '',
    icon         text NOT NULL DEFAULT '',
    cluster_name text NOT NULL DEFAULT '',
    first_seen   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now(),
    -- Bijsturing uit de GUI: hidden, name, group, icon, url, sort, fields.
    override     jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Webdiensten die Jonas bij "Twijfel" negeerde of tot app maakte.
CREATE TABLE home_app_doubts (
    service_key text PRIMARY KEY,
    -- ignore of app
    decision    text NOT NULL CHECK (decision IN ('ignore', 'app')),
    name        text NOT NULL DEFAULT '',
    decided_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE home_app_doubts;
DROP TABLE home_apps;
