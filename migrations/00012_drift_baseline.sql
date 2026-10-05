-- +goose Up
-- Bewuste uitzonderingen op drift. key is een stap (file:/pad, service:naam,
-- package:naam), een voorvoegsel dat op * eindigt, of * alleen; node_id leeg
-- geldt voor het hele cluster. Een verlopen regel blijft zichtbaar maar telt
-- niet meer.
CREATE TABLE drift_ignores (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id  uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    node_id     uuid REFERENCES nodes (id) ON DELETE CASCADE,
    key         text NOT NULL CHECK (length(key) BETWEEN 1 AND 500),
    reason      text NOT NULL CHECK (length(reason) BETWEEN 3 AND 500),
    expires_at  timestamptz,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- * alleen pauzeert het cluster, en dat altijd tijdelijk.
    CHECK (key <> '*' OR expires_at IS NOT NULL)
);
CREATE INDEX drift_ignores_cluster ON drift_ignores (cluster_id);

-- Verhuist een node, dan vervallen ook zijn eigen negeerregels.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION drift_checks_node_moved() RETURNS trigger AS $$
BEGIN
    DELETE FROM drift_checks WHERE node_id = NEW.id;
    DELETE FROM drift_ignores WHERE node_id = NEW.id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION drift_checks_node_moved() RETURNS trigger AS $$
BEGIN
    DELETE FROM drift_checks WHERE node_id = NEW.id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
DROP TABLE drift_ignores;
