-- +goose Up
-- Fase 49: Scannen op de pagina Publicatie. Eén plan in drie soorten
-- (toevoegen, bijwerken, verwijderen), alleen als Jonas op Scannen klikt.

-- Uitzonderingen per dienst (op het eerste deel van de naam, zoals
-- matomo voor matomo.jbogaert.be): mode authentik (standaard), never (nooit
-- Authentik), own_login (eigen login, dus geen Authentik) of except_paths
-- (achter Authentik behalve de paden in paths).
CREATE TABLE publish_policies (
    name       text PRIMARY KEY CHECK (name = lower(name) AND name <> ''),
    mode       text NOT NULL CHECK (mode IN ('authentik', 'never', 'own_login', 'except_paths')),
    paths      text[] NOT NULL DEFAULT '{}',
    note       text NOT NULL DEFAULT '',
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Eigenaar per naam: elke NPM-host en Authentik-app hoort bij één cluster
-- of dienst. Na een verhuizing blijft het vorige doel als terugval
-- (fallback_*) tot Jonas het weghaalt. Geen foreign key op cluster_id:
-- na het verwijderen van een cluster blijft de eigenaar zichtbaar.
CREATE TABLE publish_owners (
    domain           text PRIMARY KEY,
    cluster_id       uuid,
    cluster_name     text NOT NULL DEFAULT '',
    service_key      text NOT NULL DEFAULT '',
    service_name     text NOT NULL DEFAULT '',
    -- source: scan (vastgelegd bij scannen of toepassen) of hand.
    source           text NOT NULL DEFAULT 'scan' CHECK (source IN ('scan', 'hand')),
    fallback_scheme  text,
    fallback_host    text,
    fallback_port    integer,
    fallback_cluster text NOT NULL DEFAULT '',
    moved_at         timestamptz,
    updated_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- De laatste scans met hun regels.
CREATE TABLE publish_scans (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    items      jsonb NOT NULL DEFAULT '[]',
    notes      jsonb NOT NULL DEFAULT '[]'
);

-- Uitgeschakeld door een scan (verwijderen = eerst uitschakelen): de kopie
-- van de NPM-host of van de Authentik-app en -provider, om terug te zetten
-- of later definitief te verwijderen.
CREATE TABLE publish_disabled (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         text NOT NULL CHECK (kind IN ('npm', 'authentik')),
    domain       text NOT NULL,
    npm_host_id  integer,
    ak_slug      text NOT NULL DEFAULT '',
    ak_provider  integer,
    copy         jsonb NOT NULL,
    job_id       uuid,
    disabled_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    disabled_at  timestamptz NOT NULL DEFAULT now(),
    -- done: restored (teruggezet) of removed (definitief weg).
    done         text CHECK (done IN ('restored', 'removed')),
    done_at      timestamptz
);
CREATE INDEX publish_disabled_open ON publish_disabled (disabled_at DESC) WHERE done IS NULL;

-- +goose Down
DROP TABLE publish_disabled;
DROP TABLE publish_scans;
DROP TABLE publish_owners;
DROP TABLE publish_policies;
