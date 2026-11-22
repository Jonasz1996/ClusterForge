-- +goose Up
-- Een database verhuizen (fase 5, mijlpaal 8): de proefverhuizingen, en
-- vanaf mijlpaal 9 de echte. Van een MariaDB die ClusterForge ziet (een
-- cluster of een losse node, source_key zoals bij Databases) naar de
-- database van een cluster (component_id).
CREATE TYPE db_migration_kind AS ENUM ('trial', 'migrate');

CREATE TYPE db_migration_status AS ENUM ('running', 'ok', 'failed');

CREATE TABLE db_migrations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind db_migration_kind NOT NULL,
    component_id uuid NOT NULL REFERENCES cluster_components (id) ON DELETE CASCADE,
    cluster_id uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    -- mariadb/<cluster> of mariadb/node/<node>, en de naam voor mensen.
    source_key text NOT NULL,
    source_name text NOT NULL,
    -- De server waarop de tijdelijke gebruiker kwam, en het adres waarmee
    -- de nieuwe node verbond; leeg tot de controle ze koos.
    source_node text NOT NULL DEFAULT '',
    source_address text NOT NULL DEFAULT '',
    database text NOT NULL,
    -- De nieuwe database, bij een proef <database>_proef, en de node die
    -- importeerde.
    target_database text NOT NULL,
    target_node text NOT NULL DEFAULT '',
    status db_migration_status NOT NULL DEFAULT 'running',
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    -- De data en indexen op de oude server, en de SQL die over het netwerk
    -- kwam.
    size_bytes bigint NOT NULL DEFAULT 0,
    dump_bytes bigint NOT NULL DEFAULT 0,
    -- Hoe lang ophalen en importeren duurde, en daarna het tellen.
    import_ms bigint,
    count_ms bigint,
    -- De rijen per tabel aan beide kanten: [{name, source, target}].
    tables jsonb NOT NULL DEFAULT '[]',
    -- Views, routines, triggers en events aan beide kanten:
    -- [{kind, source, target}].
    objects jsonb NOT NULL DEFAULT '[]',
    -- Het aantal tabellen met een ander aantal rijen.
    differences integer NOT NULL DEFAULT 0,
    message text NOT NULL DEFAULT '',
    job_id uuid REFERENCES jobs (id) ON DELETE SET NULL,
    requested_by uuid REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX db_migrations_source ON db_migrations (source_key, database, started_at DESC);

-- Eén verhuizing of proef tegelijk per database.
CREATE UNIQUE INDEX db_migrations_running ON db_migrations (source_key, database) WHERE status = 'running';

-- 'Database erbij' bij Cluster uitrollen: als de uitrol lukt, zet
-- ClusterForge de database op alle nodes van het nieuwe cluster, zodat er
-- een app naartoe kan verhuizen.
CREATE TABLE deploy_databases (
    cluster_id uuid PRIMARY KEY REFERENCES clusters (id) ON DELETE CASCADE,
    engine text NOT NULL,
    job_id uuid REFERENCES jobs (id) ON DELETE SET NULL,
    requested_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE deploy_databases;
DROP TABLE db_migrations;
DROP TYPE db_migration_status;
DROP TYPE db_migration_kind;
