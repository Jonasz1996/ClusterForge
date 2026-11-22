-- +goose Up
-- Fase 40 M1: het filterlog van OPNsense per uur samengevat, 30 dagen.
CREATE TABLE opnsense_flows (
    node_id    uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    hour       timestamptz NOT NULL,
    action     text NOT NULL,
    iface      text NOT NULL,
    dir        text NOT NULL,
    proto      text NOT NULL,
    src        text NOT NULL,
    dst        text NOT NULL,
    port       integer NOT NULL DEFAULT 0,
    rule       text NOT NULL DEFAULT '',
    count      bigint NOT NULL,
    PRIMARY KEY (node_id, hour, action, iface, dir, proto, src, dst, port, rule)
);
CREATE INDEX opnsense_flows_hour ON opnsense_flows (hour);

-- Waar het lezen van het filterlog per firewall staat.
CREATE TABLE opnsense_flow_reads (
    node_id    uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    read_at    timestamptz NOT NULL,
    lines      bigint NOT NULL DEFAULT 0,
    skipped    bigint NOT NULL DEFAULT 0,
    dropped    bigint NOT NULL DEFAULT 0,
    error      text NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE opnsense_flow_reads;
DROP TABLE opnsense_flows;
