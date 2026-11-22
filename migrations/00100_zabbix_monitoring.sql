-- +goose Up
-- Fase 33: Zabbix-monitoring. ClusterForge leest de Zabbix-API (problemen,
-- hosts, proxy's, HA) en toont dat in eigen dashboards.

-- Elk kwartier een momentopname van de tellers, voor trends en sparklines.
CREATE TABLE zabbix_snapshots (
    at             timestamptz PRIMARY KEY,
    disaster       integer NOT NULL DEFAULT 0,
    high           integer NOT NULL DEFAULT 0,
    average        integer NOT NULL DEFAULT 0,
    warning        integer NOT NULL DEFAULT 0,
    info           integer NOT NULL DEFAULT 0,
    hosts          integer NOT NULL DEFAULT 0,
    hosts_down     integer NOT NULL DEFAULT 0,
    hosts_unknown  integer NOT NULL DEFAULT 0,
    -- nvps en queue: leeg als de gezondheidsitems van Zabbix er niet zijn.
    nvps           double precision,
    queue          integer
);

-- Handmatige koppeling van een Zabbix-host aan een ClusterForge-node.
-- node_id leeg: deze host hoort bewust bij geen enkele node.
CREATE TABLE zabbix_host_links (
    hostid      text PRIMARY KEY,
    node_id     uuid REFERENCES nodes (id) ON DELETE CASCADE,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX zabbix_host_links_node_key ON zabbix_host_links (node_id) WHERE node_id IS NOT NULL;

-- +goose Down
DROP TABLE zabbix_host_links;
DROP TABLE zabbix_snapshots;
