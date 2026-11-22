-- +goose Up
-- Voor de scorekaart Kubernetes: per app hoe vaak de containers van zijn
-- huidige pods herstartten en wanneer het laatst, en welke containers geen
-- geheugenlimiet hebben.
ALTER TABLE kube_workloads
    ADD COLUMN restarts        integer NOT NULL DEFAULT 0,
    ADD COLUMN restarted_at    timestamptz,
    ADD COLUMN no_memory_limit text[] NOT NULL DEFAULT '{}';

-- Voor de documentatie: de namespaces, de webadressen en de opslag van het
-- cluster, als JSON, elke vijf minuten gelezen. inventory_error zegt
-- waarom dat laatst niet lukte.
ALTER TABLE kube_clusters
    ADD COLUMN inventory       jsonb,
    ADD COLUMN inventory_at    timestamptz,
    ADD COLUMN inventory_error text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE kube_clusters DROP COLUMN inventory, DROP COLUMN inventory_at, DROP COLUMN inventory_error;
ALTER TABLE kube_workloads DROP COLUMN restarts, DROP COLUMN restarted_at, DROP COLUMN no_memory_limit;
