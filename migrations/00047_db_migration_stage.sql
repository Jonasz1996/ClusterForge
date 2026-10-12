-- +goose Up
-- De echte verhuizing (fase 5, mijlpaal 9). Een proef heeft geen stage.
-- Een verhuizing kopieert eerst (copy), wacht daarna tot de app op de
-- nieuwe database draait (copied), en is dan klaar (done) of geannuleerd
-- (cancelled). Zolang ze kopieert of wacht, is haar status running, zodat
-- er geen tweede proef of verhuizing van dezelfde database begint.
ALTER TABLE db_migrations
    ADD COLUMN stage text NOT NULL DEFAULT '',
    -- De gebruikers van de app: per gebruiker zijn accounts op de oude
    -- server (met hun rechten, voor het slot) en de hosts op de nieuwe.
    -- Nooit een hash.
    ADD COLUMN accounts jsonb NOT NULL DEFAULT '[]',
    -- Toch verhuizen terwijl er nog verbindingen met de oude database waren.
    ADD COLUMN force boolean NOT NULL DEFAULT false,
    -- Waar de app de nieuwe database vindt.
    ADD COLUMN target_address text NOT NULL DEFAULT '',
    ADD COLUMN copied_at timestamptz,
    ADD COLUMN switched_at timestamptz,
    -- De gebruikers van de app mogen de oude database alleen nog lezen.
    ADD COLUMN locked boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE db_migrations
    DROP COLUMN locked,
    DROP COLUMN switched_at,
    DROP COLUMN copied_at,
    DROP COLUMN target_address,
    DROP COLUMN force,
    DROP COLUMN accounts,
    DROP COLUMN stage;
