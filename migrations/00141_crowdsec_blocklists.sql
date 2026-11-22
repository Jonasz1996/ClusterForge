-- +goose Up
-- Fase 47: CrowdSec zonder cloud. ClusterForge is de centrale server: eigen
-- blocklists, automatische blocks over alle nodes heen en publieke lijsten,
-- lokaal verdeeld naar de nodes (cscli decisions import). Plus eigen
-- meldingen en de Console loskoppelen.

CREATE TABLE crowdsec_lists (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- slug staat in de tag op de nodes: cf-list:<slug>:<generation>.
    slug            text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    -- own: met de hand; auto: de automatische blocks; public: een
    -- publieke lijst (source = sleutel uit de catalogus of custom).
    kind            text NOT NULL CHECK (kind IN ('own', 'auto', 'public')),
    name            text NOT NULL,
    source          text NOT NULL DEFAULT '',
    url             text NOT NULL DEFAULT '',
    enabled         boolean NOT NULL DEFAULT true,
    -- scope all: alle beheerde nodes; cluster: alleen de nodes van cluster_id.
    scope           text NOT NULL DEFAULT 'all' CHECK (scope IN ('all', 'cluster')),
    cluster_id      uuid REFERENCES clusters (id) ON DELETE SET NULL,
    refresh_minutes integer NOT NULL DEFAULT 1440,
    -- pinned: een teruggedraaide publieke lijst haalt niet vanzelf op.
    pinned          boolean NOT NULL DEFAULT false,
    -- generation stijgt bij elke wijziging van de werkzame inhoud.
    generation      bigint NOT NULL DEFAULT 1,
    hash            text NOT NULL DEFAULT '',
    count           integer NOT NULL DEFAULT 0,
    dropped         integer NOT NULL DEFAULT 0,
    fetched_at      timestamptz,
    fetch_etag      text NOT NULL DEFAULT '',
    fetch_modified  text NOT NULL DEFAULT '',
    fetch_error     text NOT NULL DEFAULT '',
    fetch_failed_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE crowdsec_list_entries (
    list_id    uuid NOT NULL REFERENCES crowdsec_lists (id) ON DELETE CASCADE,
    value      text NOT NULL,
    reason     text NOT NULL DEFAULT '',
    expires_at timestamptz,
    added_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, value)
);
CREATE INDEX crowdsec_list_entries_value_idx ON crowdsec_list_entries (value);

-- Elke wijziging bewaart de inhoud, zodat ze terug te draaien is.
CREATE TABLE crowdsec_list_versions (
    id         bigserial PRIMARY KEY,
    list_id    uuid NOT NULL REFERENCES crowdsec_lists (id) ON DELETE CASCADE,
    count      integer NOT NULL,
    added      integer NOT NULL DEFAULT 0,
    removed    integer NOT NULL DEFAULT 0,
    entries    jsonb NOT NULL,
    note       text NOT NULL DEFAULT '',
    actor      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crowdsec_list_versions_list_idx ON crowdsec_list_versions (list_id, id DESC);

-- Wat er van elke lijst op elke node staat.
CREATE TABLE crowdsec_list_nodes (
    node_id    uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    list_id    uuid NOT NULL REFERENCES crowdsec_lists (id) ON DELETE CASCADE,
    tag        text NOT NULL DEFAULT '',
    count      integer NOT NULL DEFAULT 0,
    applied_at timestamptz,
    error      text NOT NULL DEFAULT '',
    failed_at  timestamptz,
    PRIMARY KEY (node_id, list_id)
);

-- Hoe vaak een IP automatisch geblokkeerd werd (voor het verdubbelen).
CREATE TABLE crowdsec_offenders (
    value      text PRIMARY KEY,
    offenses   integer NOT NULL DEFAULT 0,
    last_at    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE crowdsec_settings
    ADD COLUMN auto_rules jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN notify jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN notify_enc bytea,
    ADD COLUMN notify_key_id text NOT NULL DEFAULT '',
    -- console_off: het delen met de CrowdSec Console staat uit op alle nodes.
    ADD COLUMN console_off boolean NOT NULL DEFAULT false;

ALTER TABLE crowdsec_nodes
    -- lists: aantal bans per tag zoals de laatste poll ze zag (protocol 57).
    ADD COLUMN lists jsonb,
    ADD COLUMN console_off_done boolean NOT NULL DEFAULT false;

INSERT INTO crowdsec_lists (slug, kind, name) VALUES
    ('eigen', 'own', 'Eigen blocklist'),
    ('auto', 'auto', 'Automatisch geblokkeerd');

-- +goose Down
ALTER TABLE crowdsec_nodes DROP COLUMN console_off_done, DROP COLUMN lists;
ALTER TABLE crowdsec_settings DROP COLUMN console_off, DROP COLUMN notify_key_id, DROP COLUMN notify_enc,
    DROP COLUMN notify, DROP COLUMN auto_rules;
DROP TABLE crowdsec_offenders;
DROP TABLE crowdsec_list_nodes;
DROP TABLE crowdsec_list_versions;
DROP TABLE crowdsec_list_entries;
DROP TABLE crowdsec_lists;
