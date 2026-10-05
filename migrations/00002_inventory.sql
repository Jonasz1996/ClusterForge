-- +goose Up
CREATE TYPE environment AS ENUM ('lab', 'test', 'prod');
CREATE TYPE node_lifecycle AS ENUM ('provisioning', 'active', 'maintenance', 'draining', 'decommissioned');

CREATE TABLE clusters (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug              text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    description       text NOT NULL DEFAULT '',
    -- Type is vrije tekst met een vaste lijst in de applicatie, zodat nieuwe
    -- types geen migratie vragen.
    type              text NOT NULL,
    environment       environment NOT NULL,
    git_repo_url      text NOT NULL DEFAULT '',
    tags              text[] NOT NULL DEFAULT '{}',
    -- Status wordt vanaf mijlpaal 3/4 berekend uit agent-gegevens.
    status            text NOT NULL DEFAULT 'unknown',
    -- Gewenste staat; wordt gevuld door templates (mijlpaal 7) en GitOps (fase 2).
    spec              jsonb NOT NULL DEFAULT '{}'::jsonb,
    spec_revision     integer NOT NULL DEFAULT 0,
    template_name     text,
    template_version  text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX clusters_tags_idx ON clusters USING gin (tags);

CREATE TABLE cluster_owners (
    cluster_id  uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (cluster_id, user_id)
);

CREATE TABLE nodes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Een node zonder cluster mag: losse servers inventariseren.
    cluster_id   uuid REFERENCES clusters (id) ON DELETE SET NULL,
    hostname     text NOT NULL CHECK (hostname ~ '^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$'),
    role         text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',
    lifecycle    node_lifecycle NOT NULL DEFAULT 'active',
    primary_ip   inet,
    tags         text[] NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX nodes_hostname_key ON nodes (lower(hostname));
CREATE INDEX nodes_cluster_idx ON nodes (cluster_id);
CREATE INDEX nodes_tags_idx ON nodes USING gin (tags);

CREATE TABLE vips (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id     uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    address        inet NOT NULL UNIQUE,
    interface      text NOT NULL DEFAULT '',
    vrid           integer CHECK (vrid BETWEEN 1 AND 255),
    description    text NOT NULL DEFAULT '',
    -- Huidige eigenaar; komt vanaf mijlpaal 3 uit de agent-facts.
    owner_node_id  uuid REFERENCES nodes (id) ON DELETE SET NULL,
    owner_since    timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vips_cluster_idx ON vips (cluster_id);

-- +goose Down
DROP TABLE vips;
DROP TABLE nodes;
DROP TABLE cluster_owners;
DROP TABLE clusters;
DROP TYPE node_lifecycle;
DROP TYPE environment;
