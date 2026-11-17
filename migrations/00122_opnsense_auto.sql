-- +goose Up
-- Fase 40 M2: automatisch beheer van OPNsense. Per firewallcluster (de sleutel
-- van internal/opnsense.Cluster) en per soort de stand: uit, voorbeeld (alleen
-- voorstellen) of beheerd (ClusterForge past zelf toe via opnsense.change).
CREATE TABLE opnsense_auto (
    cluster_key text PRIMARY KEY,
    aliases     text NOT NULL DEFAULT 'voorbeeld' CHECK (aliases IN ('uit', 'voorbeeld', 'beheerd')),
    dns         text NOT NULL DEFAULT 'voorbeeld' CHECK (dns IN ('uit', 'voorbeeld', 'beheerd')),
    dhcp        text NOT NULL DEFAULT 'voorbeeld' CHECK (dhcp IN ('uit', 'voorbeeld', 'beheerd')),
    -- Het domein van de DNS-overrides; leeg is lan.<hoofddomein>.
    domain      text NOT NULL DEFAULT '',
    -- Voorstellen die je negeerde (hun sleutel, zoals dns:node1.lan.x.be).
    ignored     text[] NOT NULL DEFAULT '{}',
    last_run    timestamptz,
    last_error  text NOT NULL DEFAULT '',
    last_job    uuid,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL
);

-- +goose Down
DROP TABLE opnsense_auto;
