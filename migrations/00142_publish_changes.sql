-- +goose Up
-- Testlijst 2 (23/11), punt 1.1: elke wijziging die ClusterForge aan een
-- proxy host deed, met de waarden van vooraf, zodat ze per host terug kan.
-- Plus de titel van de pagina vooraf en achteraf.
CREATE TABLE publish_changes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id       uuid REFERENCES jobs (id) ON DELETE SET NULL,
    npm_host_id  integer NOT NULL,
    domain       text NOT NULL,
    action       text NOT NULL,
    created      boolean NOT NULL DEFAULT false,
    deleted      boolean NOT NULL DEFAULT false,
    old          jsonb NOT NULL DEFAULT '{}'::jsonb,
    new          jsonb NOT NULL DEFAULT '{}'::jsonb,
    title_before text NOT NULL DEFAULT '',
    title_after  text NOT NULL DEFAULT '',
    changed_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    changed_at   timestamptz NOT NULL DEFAULT now(),
    reverted_at  timestamptz,
    reverted_by  uuid REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX publish_changes_at_idx ON publish_changes (changed_at DESC);

-- +goose Down
DROP TABLE publish_changes;
