-- +goose Up
-- Docker-stacks (fase 21): de Compose-projecten van een cluster als gewenste
-- staat. De bestanden (compose, env, kleine configuratie) staan versleuteld
-- met de masterkey, want ze bevatten wachtwoorden; de agent schrijft ze op
-- de nodes en brengt de containers in de gewenste stand.
CREATE TABLE docker_stacks (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id     uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    -- name: de naam van het Compose-project (docker compose -p).
    name           text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    -- dir: de map van het project op elke node, zoals /docker/wordpress.
    dir            text NOT NULL,
    compose_files  text[] NOT NULL,
    -- files_enc: JSON [{path, content, mode}], versleuteld (secrets.Box,
    -- aad "docker-stack:<id>").
    files_enc      bytea NOT NULL,
    files_key_id   text NOT NULL,
    files_sha256   text NOT NULL,
    -- placement: alle (elke node), vip (alleen op de node met het VIP) of
    -- node (alleen op node_ids).
    placement      text NOT NULL CHECK (placement IN ('alle', 'vip', 'node')),
    node_ids       uuid[] NOT NULL DEFAULT '{}',
    -- desired: running of stopped.
    desired        text NOT NULL DEFAULT 'running' CHECK (desired IN ('running', 'stopped')),
    -- critical: keepalived geeft het VIP af als deze stack op de VIP-node
    -- niet gezond draait (template docker-ha).
    critical       boolean NOT NULL DEFAULT false,
    -- sync_paths: mappen die van de VIP-node naar de andere nodes gaan
    -- (alleen bij placement vip), met de containers kort gepauzeerd.
    sync_paths     text[] NOT NULL DEFAULT '{}',
    sync_schedule  text NOT NULL DEFAULT '*/15 * * * *',
    revision       integer NOT NULL DEFAULT 1,
    notes          text NOT NULL DEFAULT '',
    imported_from  uuid REFERENCES nodes (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    UNIQUE (cluster_id, name)
);

-- Elke versie van de bestanden, om terug te kijken en terug te zetten.
CREATE TABLE docker_stack_revisions (
    stack_id      uuid NOT NULL REFERENCES docker_stacks (id) ON DELETE CASCADE,
    revision      integer NOT NULL,
    files_enc     bytea NOT NULL,
    files_key_id  text NOT NULL,
    files_sha256  text NOT NULL,
    note          text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (stack_id, revision)
);

-- Wat elke node van elke stack heeft: de toegepaste versie en de laatste
-- fout.
CREATE TABLE docker_stack_nodes (
    stack_id      uuid NOT NULL REFERENCES docker_stacks (id) ON DELETE CASCADE,
    node_id       uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    revision      integer NOT NULL DEFAULT 0,
    applied_at    timestamptz,
    error         text NOT NULL DEFAULT '',
    failed_at     timestamptz,
    PRIMARY KEY (stack_id, node_id)
);

-- +goose Down
DROP TABLE docker_stack_nodes;
DROP TABLE docker_stack_revisions;
DROP TABLE docker_stacks;
