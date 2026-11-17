-- +goose Up
-- Fase 33 M6: onderhoud van ClusterForge doorzetten naar Zabbix.

-- Eén rij met de instellingen van de Zabbix-monitoring.
CREATE TABLE zabbix_monitor_settings (
    id                boolean PRIMARY KEY DEFAULT true CHECK (id),
    maintenance_sync  boolean NOT NULL DEFAULT false,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    updated_by        uuid REFERENCES users (id) ON DELETE SET NULL
);
INSERT INTO zabbix_monitor_settings DEFAULT VALUES;

-- De onderhouden die ClusterForge in Zabbix maakte, per node; alleen die
-- haalt hij ook weer weg.
CREATE TABLE zabbix_maintenances (
    node_id        uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    hostid         text NOT NULL,
    maintenanceid  text NOT NULL,
    active_till    timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE zabbix_maintenances;
DROP TABLE zabbix_monitor_settings;
