-- +goose Up
-- Firewall (fase 16): ClusterForge beheert ufw op de nodes die de beheerder
-- aanzet en telt het verkeer dat op een dichte poort botst.

-- Eén rij met de instellingen.
CREATE TABLE firewall_settings (
    id              boolean PRIMARY KEY DEFAULT true CHECK (id),
    -- auto_apply: een gewijzigd plan op een beheerde node vanzelf toepassen.
    auto_apply      boolean NOT NULL DEFAULT true,
    -- lan: de eigen netwerken; admin: de beheernetwerken (leeg = lan).
    lan             text[] NOT NULL DEFAULT '{10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7}',
    admin           text[] NOT NULL DEFAULT '{}',
    ssh_from        text NOT NULL DEFAULT 'lan' CHECK (ssh_from IN ('lan', 'admin', 'any')),
    -- unknown_ports: poorten van onbekende software open voor het LAN of dicht.
    unknown_ports   text NOT NULL DEFAULT 'lan' CHECK (unknown_ports IN ('lan', 'closed')),
    -- strict: regels die niet van ClusterForge zijn, weghalen.
    strict          boolean NOT NULL DEFAULT true,
    confirm_seconds integer NOT NULL DEFAULT 120 CHECK (confirm_seconds BETWEEN 30 AND 900),
    -- grace_hours: zo lang blijft een poort open nadat er niets meer op luistert.
    grace_hours     integer NOT NULL DEFAULT 24 CHECK (grace_hours BETWEEN 0 AND 720),
    -- monitor: verkeer naar dichte poorten tellen.
    monitor         boolean NOT NULL DEFAULT true,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      uuid REFERENCES users (id) ON DELETE SET NULL
);
INSERT INTO firewall_settings DEFAULT VALUES;

-- Een node met firewallbeheer. mode off: ClusterForge doet niets (de rij en
-- de geschiedenis blijven); preview: plan en verschil, niets toepassen;
-- managed: het plan wordt toegepast.
CREATE TABLE firewall_nodes (
    node_id         uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    mode            text NOT NULL DEFAULT 'preview' CHECK (mode IN ('off', 'preview', 'managed')),
    status          jsonb,
    status_at       timestamptz,
    status_error    text NOT NULL DEFAULT '',
    plan            jsonb,
    plan_hash       text NOT NULL DEFAULT '',
    applied_hash    text NOT NULL DEFAULT '',
    applied_at      timestamptz,
    apply_job_id    uuid,
    apply_error     text NOT NULL DEFAULT '',
    apply_failed_at timestamptz,
    blocked_at      timestamptz,
    blocked_source  text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Poorten waarop iets luisterde, zodat een dienst die even stilstaat zijn
-- poort niet verliest.
CREATE TABLE firewall_ports (
    node_id    uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    proto      text NOT NULL,
    port       integer NOT NULL,
    addr       text NOT NULL DEFAULT '',
    process    text NOT NULL DEFAULT '',
    unit       text NOT NULL DEFAULT '',
    container  text NOT NULL DEFAULT '',
    image      text NOT NULL DEFAULT '',
    first_seen timestamptz NOT NULL DEFAULT now(),
    last_seen  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, proto, port)
);

-- Eigen regels: open of dicht, voor één node, een cluster of overal.
CREATE TABLE firewall_rules (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id uuid REFERENCES clusters (id) ON DELETE CASCADE,
    node_id    uuid REFERENCES nodes (id) ON DELETE CASCADE,
    action     text NOT NULL CHECK (action IN ('open', 'close')),
    proto      text NOT NULL CHECK (proto IN ('tcp', 'udp')),
    port       text NOT NULL,
    -- source: een klasse (any, lan, cluster, admin, containers) of een adres/netwerk.
    source     text NOT NULL DEFAULT 'lan',
    comment    text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    CHECK (cluster_id IS NULL OR node_id IS NULL)
);

-- Verkeer dat ufw weggooide, per uur, bron, protocol en poort; 30 dagen.
CREATE TABLE firewall_blocked (
    node_id    uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    hour       timestamptz NOT NULL,
    src        text NOT NULL,
    proto      text NOT NULL,
    port       integer NOT NULL,
    count      bigint NOT NULL,
    first_seen timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    PRIMARY KEY (node_id, hour, src, proto, port)
);
CREATE INDEX firewall_blocked_hour_idx ON firewall_blocked (hour DESC);

-- +goose Down
DROP TABLE firewall_blocked;
DROP TABLE firewall_rules;
DROP TABLE firewall_ports;
DROP TABLE firewall_nodes;
DROP TABLE firewall_settings;
