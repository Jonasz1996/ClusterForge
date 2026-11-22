-- +goose Up
-- Onderdelen van een cluster (fase 5, mijlpaal 3): een gewenste staat die
-- op de nodes van een bestaand cluster komt, naast een template of een
-- baseline. In fase 5 is dat de database; een cluster heeft hoogstens één
-- MariaDB en één PostgreSQL. De spec heeft de vorm van die van een uitrol:
-- het onderdeel met zijn versie, de parameters zonder geheimen, de namen
-- van de geheimen en de nodes met rol en volgnummer. De geheimen staan in
-- secrets, met de engine als voorvoegsel (mariadb.sst_password).
CREATE TABLE cluster_components (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id       uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    engine           text NOT NULL CHECK (engine IN ('mariadb', 'postgresql')),
    spec             jsonb NOT NULL,
    revision         integer NOT NULL DEFAULT 1,
    -- De laatste revisie die helemaal gelukt is; NULL tot de eerste uitrol
    -- klaar is.
    applied_revision integer,
    -- De laatste taak die het onderdeel toepaste.
    job_id           uuid REFERENCES jobs (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, engine)
);

CREATE TABLE cluster_component_revisions (
    component_id uuid NOT NULL REFERENCES cluster_components (id) ON DELETE CASCADE,
    revision     integer NOT NULL,
    spec         jsonb NOT NULL,
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (component_id, revision)
);

-- De driftcontrole kent nu ook een cluster dat alleen een onderdeel heeft.
ALTER TABLE drift_checks DROP CONSTRAINT drift_checks_source_check;
ALTER TABLE drift_checks ADD CONSTRAINT drift_checks_source_check CHECK (source IN ('template', 'baseline', 'component'));

-- +goose Down
ALTER TABLE drift_checks DROP CONSTRAINT drift_checks_source_check;
DELETE FROM drift_checks WHERE source = 'component';
ALTER TABLE drift_checks ADD CONSTRAINT drift_checks_source_check CHECK (source IN ('template', 'baseline'));
DROP TABLE cluster_component_revisions;
DROP TABLE cluster_components;
