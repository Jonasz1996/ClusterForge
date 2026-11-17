-- +goose Up
-- Fase 33 M4: eigen dashboards met panelen op Zabbix-items. De panelen staan
-- als JSON in de rij; de server controleert ze bij het bewaren.
CREATE TABLE zabbix_dashboards (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    -- refresh: om de hoeveel seconden het scherm ververst (0 = niet).
    refresh      integer NOT NULL DEFAULT 60,
    panels       jsonb NOT NULL DEFAULT '[]',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid REFERENCES users (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX zabbix_dashboards_name_key ON zabbix_dashboards (lower(name));

-- +goose Down
DROP TABLE zabbix_dashboards;
