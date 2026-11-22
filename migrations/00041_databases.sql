-- +goose Up
-- De databaseservers die de agents melden (fase 5): per node en engine de
-- laatste status, en de databases met hun grootte. ClusterForge leest ze
-- elke minuut via agentprotocol 9.
CREATE TABLE db_servers (
    node_id          uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    engine           text NOT NULL CHECK (engine IN ('mariadb', 'postgresql')),
    unit             text NOT NULL DEFAULT '',
    active           text NOT NULL DEFAULT '',
    running          boolean NOT NULL DEFAULT false,
    version          text NOT NULL DEFAULT '',
    role             text NOT NULL DEFAULT '',
    -- De status zoals de agent hem stuurde, zonder de databases
    -- (protocol.DBServer).
    status           jsonb NOT NULL DEFAULT '{}'::jsonb,
    databases_total  integer NOT NULL DEFAULT 0,
    -- error zegt waarom de server of een deel ervan niet te lezen was, en
    -- error_kind waarom ClusterForge het niet kon vragen: old_agent,
    -- offline, no_answer of agent; leeg als de agent antwoordde.
    error            text NOT NULL DEFAULT '',
    error_kind       text NOT NULL DEFAULT '',
    synced_at        timestamptz NOT NULL,
    -- ok_at is wanneer de agent het laatst antwoordde.
    ok_at            timestamptz,
    PRIMARY KEY (node_id, engine)
);

CREATE TABLE db_databases (
    node_id     uuid NOT NULL,
    engine      text NOT NULL,
    name        text NOT NULL,
    size_bytes  bigint NOT NULL DEFAULT 0,
    -- -1 als het aantal tabellen niet te lezen was.
    tables      integer NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, engine, name),
    FOREIGN KEY (node_id, engine) REFERENCES db_servers (node_id, engine) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE db_databases;
DROP TABLE db_servers;
