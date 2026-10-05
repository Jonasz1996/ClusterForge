-- +goose Up
-- Logboek. De eventtabel wordt doorzoekbaar op node en taak, ook voor events
-- waarvan het onderwerp iets anders is (een taak op een node, een agent).

-- De live-notificatie draagt alleen nog het id. Elke ingelogde browser krijgt
-- ze, ook die van een viewer; wat er gebeurde, leest een admin in het logboek.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION notify_event() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('cf_events', json_build_object('id', NEW.id)::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- TRUNCATE omzeilt de rij-trigger uit 00001.
CREATE TRIGGER events_no_truncate BEFORE TRUNCATE ON events
    FOR EACH STATEMENT EXECUTE FUNCTION events_append_only();

-- Een berekende kolom herschrijft de tabel zonder de UPDATE-trigger af te
-- vuren, dus ook bestaande regels krijgen de waarde.
ALTER TABLE events
    ADD COLUMN node_ref text GENERATED ALWAYS AS (
        CASE WHEN subject_type = 'node' THEN subject_id ELSE payload->>'node_id' END
    ) STORED,
    ADD COLUMN job_ref text GENERATED ALWAYS AS (
        CASE WHEN subject_type = 'job' THEN subject_id
             ELSE coalesce(payload->>'job_id', payload->'origin'->>'job_id') END
    ) STORED;
CREATE INDEX events_node_ref_idx ON events (node_ref, id DESC) WHERE node_ref IS NOT NULL;
CREATE INDEX events_job_ref_idx ON events (job_ref, id DESC) WHERE job_ref IS NOT NULL;
CREATE INDEX events_actor_idx ON events (actor_type, actor_id, id DESC);

-- +goose Down
DROP INDEX events_actor_idx;
DROP INDEX events_job_ref_idx;
DROP INDEX events_node_ref_idx;
ALTER TABLE events DROP COLUMN job_ref, DROP COLUMN node_ref;
DROP TRIGGER events_no_truncate ON events;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION notify_event() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('cf_events', json_build_object(
        'id', NEW.id, 'action', NEW.action, 'subject_type', NEW.subject_type,
        'subject_id', NEW.subject_id, 'cluster_id', NEW.cluster_id
    )::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
