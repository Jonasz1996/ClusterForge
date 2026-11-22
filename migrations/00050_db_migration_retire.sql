-- +goose Up
-- Het oude cluster afbouwen (fase 5, mijlpaal 14). Een verhuisde database
-- blijft op de oude server staan, op slot. Afbouwen maakt daar een laatste
-- dump, die blijft staan tot Jonas hem weghaalt, en haalt de database en
-- haar gebruikers weg. De verhuizing onthoudt de taak, waar de dump staat
-- en wanneer het klaar was.
ALTER TABLE db_migrations
    ADD COLUMN retire_job_id uuid REFERENCES jobs (id) ON DELETE SET NULL,
    ADD COLUMN retired_at timestamptz,
    -- De node met de laatste dump, het bestand in
    -- /var/backups/clusterforge-db/<database>/ en zijn grootte op schijf.
    ADD COLUMN retire_node text NOT NULL DEFAULT '',
    ADD COLUMN retire_file text NOT NULL DEFAULT '',
    ADD COLUMN retire_bytes bigint NOT NULL DEFAULT 0,
    -- Wat het afbouwen deed, of waarom het mislukte.
    ADD COLUMN retire_message text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE db_migrations
    DROP COLUMN retire_message,
    DROP COLUMN retire_bytes,
    DROP COLUMN retire_file,
    DROP COLUMN retire_node,
    DROP COLUMN retired_at,
    DROP COLUMN retire_job_id;
