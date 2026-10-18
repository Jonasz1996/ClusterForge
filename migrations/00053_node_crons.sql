-- +goose Up
-- De cronjobs en systemd-timers die de agents melden (fase 6). ClusterForge
-- leest ze elke vijf minuten via agentprotocol 16. Commando's zijn op de
-- node al gemaskeerd.
CREATE TABLE node_cron_reads (
    node_id     uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    -- daemon is cron, cronie of leeg; logging zegt of cron einde en
    -- exitcode logt; journal of de agent de journal kon lezen.
    daemon      text NOT NULL DEFAULT '',
    logging     boolean NOT NULL DEFAULT false,
    journal     boolean NOT NULL DEFAULT false,
    truncated   boolean NOT NULL DEFAULT false,
    -- error en error_kind zoals bij db_servers: old_agent, offline,
    -- no_answer of agent; leeg als de agent antwoordde.
    error       text NOT NULL DEFAULT '',
    error_kind  text NOT NULL DEFAULT '',
    synced_at   timestamptz NOT NULL,
    ok_at       timestamptz
);

CREATE TABLE node_crons (
    node_id      uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    -- key is dezelfde voor dezelfde job op elke node: bron, bestand,
    -- gebruiker, schema en commando.
    key          text NOT NULL,
    source       text NOT NULL,
    file         text NOT NULL,
    line         integer NOT NULL DEFAULT 0,
    user_name    text NOT NULL DEFAULT '',
    schedule     text NOT NULL,
    command      text NOT NULL,
    unit         text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',
    package      text NOT NULL DEFAULT '',
    enabled      boolean NOT NULL DEFAULT true,
    clusterforge boolean NOT NULL DEFAULT false,
    last_run_at  timestamptz,
    last_end_at  timestamptz,
    last_exit    integer,
    last_result  text NOT NULL DEFAULT '',
    runs         integer NOT NULL DEFAULT 0,
    failures     integer NOT NULL DEFAULT 0,
    next_run_at  timestamptz,
    first_seen   timestamptz NOT NULL,
    PRIMARY KEY (node_id, key)
);

-- +goose Down
DROP TABLE node_crons;
DROP TABLE node_cron_reads;
