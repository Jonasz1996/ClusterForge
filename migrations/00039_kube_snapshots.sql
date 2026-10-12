-- +goose Up
-- De etcd-snapshots van een Kubernetes-cluster, zoals k3s ze als
-- ETCDSnapshotFile meldt: per server de geplande, die op verzoek, en die
-- van ClusterForge. De sync leest ze om de vijf minuten, en meteen na
-- Snapshot nu of terugzetten.
CREATE TABLE kube_snapshots (
    cluster_id uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    -- name is de naam van het object, file die van het bestand en node de
    -- server in Kubernetes waarop het staat.
    name       text NOT NULL,
    file       text NOT NULL,
    node       text NOT NULL,
    location   text NOT NULL DEFAULT '',
    s3         boolean NOT NULL DEFAULT false,
    size_bytes bigint,
    taken_at   timestamptz,
    ready      boolean NOT NULL DEFAULT false,
    error      text NOT NULL DEFAULT '',
    PRIMARY KEY (cluster_id, name)
);

-- snapshots_at is wanneer de lijst laatst gelezen werd, en
-- snapshots_error waarom dat niet lukte.
ALTER TABLE kube_clusters
    ADD COLUMN snapshots_at timestamptz,
    ADD COLUMN snapshots_error text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE kube_clusters DROP COLUMN snapshots_at, DROP COLUMN snapshots_error;
DROP TABLE kube_snapshots;
