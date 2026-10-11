-- +goose Up
-- Incidenten: statusovergangen die bij elkaar horen, als één storing met
-- een begin en een einde. De detector in internal/incidents schrijft ze na
-- elke ronde van de evaluator; niemand anders verandert ze, behalve de
-- notitie.
CREATE TABLE incidents (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Het nummer voor mensen: '#12'.
    number         integer NOT NULL GENERATED ALWAYS AS IDENTITY UNIQUE,
    title          text NOT NULL,
    severity       text NOT NULL CHECK (severity IN ('low', 'high', 'critical')),
    status         text NOT NULL CHECK (status IN ('open', 'resolved')),
    -- Wanneer de eerste overgang gebeurde, en wanneer de detector hem zag.
    started_at     timestamptz NOT NULL,
    detected_at    timestamptz NOT NULL,
    resolved_at    timestamptz,
    -- Een overgang tijdens een failovertest van ClusterForge in hetzelfde
    -- cluster; standaard verborgen.
    caused_by_test boolean NOT NULL DEFAULT false,
    test_run_id    uuid REFERENCES test_runs (id) ON DELETE SET NULL,
    note           text NOT NULL DEFAULT '' CHECK (length(note) <= 10000),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'resolved') = (resolved_at IS NOT NULL))
);
CREATE INDEX incidents_started_idx ON incidents (started_at DESC);
CREATE INDEX incidents_open_idx ON incidents (started_at) WHERE status = 'open';
CREATE INDEX incidents_resolved_idx ON incidents (resolved_at) WHERE status = 'resolved';

-- Wat er geraakt was. De naam en het cluster blijven staan als het onderwerp
-- later verdwijnt.
CREATE TABLE incident_subjects (
    incident_id  uuid NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    subject_type text NOT NULL CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run')),
    subject_id   uuid NOT NULL,
    name         text NOT NULL,
    cluster_id   uuid REFERENCES clusters (id) ON DELETE SET NULL,
    first_status text NOT NULL,
    worst_status text NOT NULL,
    last_status  text NOT NULL,
    reason       text NOT NULL DEFAULT '',
    since        timestamptz NOT NULL,
    -- Wanneer het weer goed was; leeg zolang het nog misgaat.
    until        timestamptz,
    PRIMARY KEY (incident_id, subject_type, subject_id)
);
CREATE INDEX incident_subjects_subject_idx ON incident_subjects (subject_type, subject_id);
CREATE INDEX incident_subjects_cluster_idx ON incident_subjects (cluster_id) WHERE cluster_id IS NOT NULL;

-- Elke wijziging is een live update, ook een onderwerp dat weer gezond is.
-- +goose StatementBegin
CREATE FUNCTION notify_incidents() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('cf_events', json_build_object('incidents', true)::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER incidents_notify AFTER INSERT OR UPDATE ON incidents
    FOR EACH STATEMENT EXECUTE FUNCTION notify_incidents();
CREATE TRIGGER incident_subjects_notify AFTER INSERT OR UPDATE ON incident_subjects
    FOR EACH STATEMENT EXECUTE FUNCTION notify_incidents();

-- vip.owner_changed en service.status_changed noemen nu de nodes in
-- payload.node_ids; het filter op node in het logboek zoekt ook daar.
CREATE INDEX events_node_ids_idx ON events USING gin ((payload -> 'node_ids') jsonb_path_ops);

-- +goose Down
DROP INDEX events_node_ids_idx;
DROP TRIGGER incident_subjects_notify ON incident_subjects;
DROP TRIGGER incidents_notify ON incidents;
DROP FUNCTION notify_incidents();
DROP TABLE incident_subjects;
DROP TABLE incidents;
