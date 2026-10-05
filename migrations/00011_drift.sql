-- +goose Up
-- De laatste driftcontrole per node, zoals node_facts: alleen de laatste
-- uitkomst, de events zijn de geschiedenis. findings bevat per afwijking de
-- sleutel, wat verwacht en wat gezien werd en een vingerafdruk, nooit inhoud
-- of een hash. Na een fout blijven de afwijkingen van de laatste geslaagde
-- controle staan.
CREATE TABLE drift_checks (
    node_id          uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    status           text NOT NULL CHECK (status IN ('in_sync', 'drift', 'error', 'none')),
    source           text NOT NULL CHECK (source IN ('template', 'baseline')),
    spec_revision    integer NOT NULL DEFAULT 0,
    template_version text NOT NULL DEFAULT '',
    findings         jsonb NOT NULL DEFAULT '[]',
    unchecked        jsonb NOT NULL DEFAULT '[]',
    -- fingerprint hasht de sleutels van de afwijkingen; verandert hij, dan
    -- is er een drift.changed.
    fingerprint      text NOT NULL DEFAULT '',
    error            text NOT NULL DEFAULT '',
    checked_at       timestamptz NOT NULL,
    drift_since      timestamptz
);

-- Een controle hoort bij het cluster van de node; verhuist de node, dan
-- vervalt ze.
-- +goose StatementBegin
CREATE FUNCTION drift_checks_node_moved() RETURNS trigger AS $$
BEGIN
    DELETE FROM drift_checks WHERE node_id = NEW.id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER nodes_drift_moved AFTER UPDATE OF cluster_id ON nodes
    FOR EACH ROW WHEN (OLD.cluster_id IS DISTINCT FROM NEW.cluster_id)
    EXECUTE FUNCTION drift_checks_node_moved();

-- +goose Down
DROP TRIGGER nodes_drift_moved ON nodes;
DROP FUNCTION drift_checks_node_moved();
DROP TABLE drift_checks;
