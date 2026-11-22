-- +goose Up
-- Berekende status; zie internal/status voor de regels.
ALTER TABLE nodes
    ADD COLUMN status        text NOT NULL DEFAULT 'unknown',
    ADD COLUMN status_reason text NOT NULL DEFAULT '',
    ADD COLUMN status_since  timestamptz;
ALTER TABLE clusters
    ADD COLUMN status_reason text NOT NULL DEFAULT '',
    ADD COLUMN status_since  timestamptz;

-- Het volste bestandssysteem uit de laatste metrics, voor de statusregels.
ALTER TABLE node_status
    ADD COLUMN disk_used_ratio double precision NOT NULL DEFAULT 0,
    ADD COLUMN disk_used_mount text NOT NULL DEFAULT '';

-- Elk nieuw event gaat na de commit als notificatie naar de server, die het
-- als live update naar de webinterface stuurt.
-- +goose StatementBegin
CREATE FUNCTION notify_event() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('cf_events', json_build_object(
        'id', NEW.id, 'action', NEW.action, 'subject_type', NEW.subject_type,
        'subject_id', NEW.subject_id, 'cluster_id', NEW.cluster_id
    )::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER events_notify AFTER INSERT ON events
    FOR EACH ROW EXECUTE FUNCTION notify_event();

-- +goose Down
DROP TRIGGER events_notify ON events;
DROP FUNCTION notify_event();
ALTER TABLE node_status DROP COLUMN disk_used_mount, DROP COLUMN disk_used_ratio;
ALTER TABLE clusters DROP COLUMN status_since, DROP COLUMN status_reason;
ALTER TABLE nodes DROP COLUMN status_since, DROP COLUMN status_reason, DROP COLUMN status;
