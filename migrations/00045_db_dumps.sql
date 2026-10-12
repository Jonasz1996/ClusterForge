-- +goose Up
-- Back-ups van de databases (fase 5, mijlpaal 7): elke nacht een dump per
-- database op een node van het cluster, 'Dump nu', en een dump terugzetten
-- in een nieuwe database. De dumps staan op de nodes; hier staat wat
-- ClusterForge erover weet.
ALTER TABLE cluster_components
    -- Elke dag om backup_at (HH:MM, in de tijdzone van de server) een dump
    -- van elke database, backup_keep_days dagen bewaard.
    ADD COLUMN backup_on boolean NOT NULL DEFAULT true,
    ADD COLUMN backup_at text NOT NULL DEFAULT '02:00',
    ADD COLUMN backup_keep_days integer NOT NULL DEFAULT 7,
    -- Wanneer de volgende nachtelijke dump begint; NULL tot de planner hem
    -- voor het eerst zet.
    ADD COLUMN backup_next_at timestamptz;

CREATE TYPE db_dump_status AS ENUM ('running', 'ok', 'suspect', 'failed');

CREATE TABLE db_dumps (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    component_id uuid NOT NULL REFERENCES cluster_components (id) ON DELETE CASCADE,
    cluster_id uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    -- De node waarop de dump staat; de hostname blijft als de node weg is.
    node_id uuid REFERENCES nodes (id) ON DELETE SET NULL,
    hostname text NOT NULL,
    database text NOT NULL,
    -- De naam in /var/backups/clusterforge-db/<database>/.
    file text NOT NULL,
    -- schedule of manual.
    trigger text NOT NULL,
    status db_dump_status NOT NULL DEFAULT 'running',
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    size_bytes bigint NOT NULL DEFAULT 0,
    raw_bytes bigint NOT NULL DEFAULT 0,
    tables integer NOT NULL DEFAULT 0,
    tables_expected integer NOT NULL DEFAULT 0,
    -- Waarom de dump mislukte of verdacht is.
    message text NOT NULL DEFAULT '',
    job_id uuid REFERENCES jobs (id) ON DELETE SET NULL,
    requested_by uuid REFERENCES users (id) ON DELETE SET NULL,
    UNIQUE (component_id, hostname, file)
);

CREATE INDEX db_dumps_component ON db_dumps (component_id, database, started_at DESC);

-- +goose Down
DROP TABLE db_dumps;
DROP TYPE db_dump_status;
ALTER TABLE cluster_components
    DROP COLUMN backup_on,
    DROP COLUMN backup_at,
    DROP COLUMN backup_keep_days,
    DROP COLUMN backup_next_at;
