-- +goose Up
-- Fase 39: verkeer van NPM en logins van Authentik. De agent op de NPM-node
-- leest de toegangslogs en geeft ze samengevat; de server haalt de events
-- van Authentik op. Zo ziet ClusterForge wie wat opvraagt, vergelijkt dat
-- met de bans van CrowdSec en telt (mislukte) logins.

-- Verkeer per node, proxy host en vijf minuten.
CREATE TABLE publish_traffic (
    node_id  uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    host_id  integer NOT NULL,
    bucket   timestamptz NOT NULL,
    requests integer NOT NULL DEFAULT 0,
    s2xx     integer NOT NULL DEFAULT 0,
    s3xx     integer NOT NULL DEFAULT 0,
    s4xx     integer NOT NULL DEFAULT 0,
    s5xx     integer NOT NULL DEFAULT 0,
    bytes    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, host_id, bucket)
);
CREATE INDEX publish_traffic_bucket_idx ON publish_traffic (bucket);

-- Verkeer per proxy host, client-IP en uur.
CREATE TABLE publish_traffic_ips (
    host_id   integer NOT NULL,
    hour      timestamptz NOT NULL,
    ip        text NOT NULL,
    requests  integer NOT NULL DEFAULT 0,
    s4xx      integer NOT NULL DEFAULT 0,
    s5xx      integer NOT NULL DEFAULT 0,
    denied    integer NOT NULL DEFAULT 0,
    bytes     bigint NOT NULL DEFAULT 0,
    first_at  timestamptz NOT NULL,
    last_at   timestamptz NOT NULL,
    PRIMARY KEY (host_id, hour, ip)
);
CREATE INDEX publish_traffic_ips_ip_idx ON publish_traffic_ips (ip);
CREATE INDEX publish_traffic_ips_hour_idx ON publish_traffic_ips (hour);

-- Open verbindingen van NPM, per node en meting.
CREATE TABLE publish_connections (
    node_id     uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    at          timestamptz NOT NULL,
    connections integer NOT NULL,
    PRIMARY KEY (node_id, at)
);

-- Events van Authentik: login, login_failed, logout en
-- authorize_application.
CREATE TABLE publish_auth_events (
    pk        text PRIMARY KEY,
    created   timestamptz NOT NULL,
    action    text NOT NULL,
    username  text NOT NULL DEFAULT '',
    client_ip text NOT NULL DEFAULT '',
    app       text NOT NULL DEFAULT '',
    country   text NOT NULL DEFAULT '',
    city      text NOT NULL DEFAULT '',
    reason    text NOT NULL DEFAULT ''
);
CREATE INDEX publish_auth_events_created_idx ON publish_auth_events (created);
CREATE INDEX publish_auth_events_ip_idx ON publish_auth_events (client_ip);

-- Hoe het lezen laatst ging, per bron (npm:<node>, authentik).
CREATE TABLE publish_feed_state (
    source   text PRIMARY KEY,
    at       timestamptz NOT NULL DEFAULT now(),
    ok_at    timestamptz,
    cursor   text NOT NULL DEFAULT '',
    error    text NOT NULL DEFAULT '',
    lines    bigint NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE publish_feed_state;
DROP TABLE publish_auth_events;
DROP TABLE publish_connections;
DROP TABLE publish_traffic_ips;
DROP TABLE publish_traffic;
