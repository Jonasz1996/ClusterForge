-- +goose Up
-- CrowdSec (fase 14): ClusterForge installeert en configureert CrowdSec op de
-- nodes die de beheerder aanzet, haalt hun alerts en bans op en deelt bans
-- tussen de nodes.

-- Eén rij met de instellingen.
CREATE TABLE crowdsec_settings (
    id              boolean PRIMARY KEY DEFAULT true CHECK (id),
    -- auto_apply: een gewijzigd plan (andere software, andere instellingen)
    -- vanzelf toepassen.
    auto_apply      boolean NOT NULL DEFAULT true,
    -- bouncer_default: de nftables-bouncer bij het aanzetten van een node.
    bouncer_default boolean NOT NULL DEFAULT true,
    -- share_bans: off, cluster of all.
    share_bans      text NOT NULL DEFAULT 'all' CHECK (share_bans IN ('off', 'cluster', 'all')),
    -- whitelist: adressen en netwerken die CrowdSec nooit bant.
    whitelist       text[] NOT NULL DEFAULT '{10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,::1/128,fc00::/7}',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      uuid REFERENCES users (id) ON DELETE SET NULL
);
INSERT INTO crowdsec_settings DEFAULT VALUES;

-- Een node met CrowdSec. managed false: ClusterForge past niets meer toe,
-- maar de rij (en de keuzes) blijven.
CREATE TABLE crowdsec_nodes (
    node_id           uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    managed           boolean NOT NULL DEFAULT true,
    bouncer           boolean NOT NULL DEFAULT true,
    extra_profiles    text[] NOT NULL DEFAULT '{}',
    disabled_profiles text[] NOT NULL DEFAULT '{}',
    -- status: de laatste protocol.CrowdSecStatus van de agent.
    status            jsonb,
    status_at         timestamptz,
    -- plan: het laatst uitgerekende plan (profielen, collections); hash
    -- daarvan en wat er toegepast is.
    plan              jsonb,
    plan_hash         text NOT NULL DEFAULT '',
    applied_hash      text NOT NULL DEFAULT '',
    applied_at        timestamptz,
    apply_job_id      uuid,
    apply_error       text NOT NULL DEFAULT '',
    apply_failed_at   timestamptz,
    blocklist         integer NOT NULL DEFAULT -1,
    poll_at           timestamptz,
    poll_error        text NOT NULL DEFAULT '',
    -- last_alert_at: de nieuwste stop_at die de poller zag.
    last_alert_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- Alerts van de nodes, 90 dagen. Zonder de community-blocklist.
CREATE TABLE crowdsec_alerts (
    id          bigserial PRIMARY KEY,
    node_id     uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    alert_id    bigint NOT NULL,
    scenario    text NOT NULL,
    message     text NOT NULL DEFAULT '',
    scope       text NOT NULL,
    value       text NOT NULL,
    country     text NOT NULL DEFAULT '',
    as_name     text NOT NULL DEFAULT '',
    events      integer NOT NULL DEFAULT 0,
    decisions   integer NOT NULL DEFAULT 0,
    simulated   boolean NOT NULL DEFAULT false,
    started_at  timestamptz NOT NULL,
    stopped_at  timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, alert_id, started_at)
);
CREATE INDEX crowdsec_alerts_started_idx ON crowdsec_alerts (started_at DESC);
CREATE INDEX crowdsec_alerts_value_idx ON crowdsec_alerts (value);

-- De actieve bans per node zoals de laatste poll ze zag.
CREATE TABLE crowdsec_decisions (
    node_id     uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    decision_id bigint NOT NULL,
    origin      text NOT NULL,
    type        text NOT NULL,
    scope       text NOT NULL,
    value       text NOT NULL,
    scenario    text NOT NULL DEFAULT '',
    until       timestamptz NOT NULL,
    country     text NOT NULL DEFAULT '',
    as_name     text NOT NULL DEFAULT '',
    first_seen  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, decision_id)
);
CREATE INDEX crowdsec_decisions_value_idx ON crowdsec_decisions (value);

-- Bans die ClusterForge naar andere nodes kopieerde; zo gebeurt dat één keer
-- per IP zolang de ban loopt.
CREATE TABLE crowdsec_shared (
    scope       text NOT NULL,
    value       text NOT NULL,
    source_node uuid REFERENCES nodes (id) ON DELETE SET NULL,
    scenario    text NOT NULL DEFAULT '',
    until       timestamptz NOT NULL,
    shared_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, value)
);

-- +goose Down
DROP TABLE crowdsec_shared;
DROP TABLE crowdsec_decisions;
DROP TABLE crowdsec_alerts;
DROP TABLE crowdsec_nodes;
DROP TABLE crowdsec_settings;
