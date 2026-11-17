-- +goose Up
-- Fase 35: publicatie. ClusterForge brengt de diensten van elke node in kaart
-- en beheert Nginx Proxy Manager en Authentik: proxy hosts onder het
-- hoofddomein, certificaten en forward auth.

-- Eén rij met de instellingen (Instellingen → Domeinen en publicatie).
-- Geheimen staan versleuteld met de masterkey (key_id).
CREATE TABLE publish_settings (
    id                   boolean PRIMARY KEY DEFAULT true CHECK (id),
    domain               text NOT NULL DEFAULT 'jbogaert.be',
    -- Standaarden voor nieuwe hosts (ssl, http2, hsts, websockets,
    -- block_exploits, authentik, uitzonderingen); zie internal/publish.
    defaults             jsonb NOT NULL DEFAULT '{}'::jsonb,
    npm_cluster_id       uuid REFERENCES clusters (id) ON DELETE SET NULL,
    npm_node_id          uuid REFERENCES nodes (id) ON DELETE SET NULL,
    npm_port             integer NOT NULL DEFAULT 81 CHECK (npm_port BETWEEN 1 AND 65535),
    npm_identity         text NOT NULL DEFAULT '',
    npm_secret_enc       bytea,
    npm_key_id           text NOT NULL DEFAULT '',
    authentik_cluster_id uuid REFERENCES clusters (id) ON DELETE SET NULL,
    authentik_node_id    uuid REFERENCES nodes (id) ON DELETE SET NULL,
    authentik_port       integer NOT NULL DEFAULT 9000 CHECK (authentik_port BETWEEN 1 AND 65535),
    authentik_token_enc  bytea,
    authentik_key_id     text NOT NULL DEFAULT '',
    -- Waar NPM de ingebouwde outpost bereikt, zoals http://192.168.103.244:9000.
    authentik_outpost    text NOT NULL DEFAULT '',
    updated_at           timestamptz NOT NULL DEFAULT now(),
    updated_by           uuid REFERENCES users (id) ON DELETE SET NULL
);
INSERT INTO publish_settings DEFAULT VALUES;

-- De laatste ontdekking per node: poorten, containers en wat er antwoordt.
CREATE TABLE publish_services (
    node_id    uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    services   jsonb NOT NULL DEFAULT '[]'::jsonb,
    error      text NOT NULL DEFAULT '',
    checked_at timestamptz NOT NULL DEFAULT now()
);

-- Wat ClusterForge laatst in NPM en Authentik las (kind npm of authentik).
CREATE TABLE publish_reads (
    kind    text PRIMARY KEY CHECK (kind IN ('npm', 'authentik')),
    data    jsonb NOT NULL DEFAULT '{}'::jsonb,
    error   text NOT NULL DEFAULT '',
    read_at timestamptz NOT NULL DEFAULT now()
);

-- De proxy hosts die ClusterForge beheert: gemaakt of overgenomen. desired
-- zijn de velden zoals NPM ze moet hebben; een verschil is drift.
CREATE TABLE publish_hosts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    npm_host_id      integer NOT NULL UNIQUE,
    domain           text NOT NULL,
    desired          jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Waar de dienst draait (voor de dienstenkaart en "naar VIP").
    cluster_id       uuid REFERENCES clusters (id) ON DELETE SET NULL,
    node_id          uuid REFERENCES nodes (id) ON DELETE SET NULL,
    service_port     integer,
    created_by_cf    boolean NOT NULL DEFAULT false,
    -- Authentik: wat ClusterForge maakte (verwijderen mag alleen dat).
    ak_provider_pk   integer,
    ak_app_slug      text,
    ak_created       boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    updated_by       uuid REFERENCES users (id) ON DELETE SET NULL
);

-- +goose Down
DROP TABLE publish_hosts;
DROP TABLE publish_reads;
DROP TABLE publish_services;
DROP TABLE publish_settings;
