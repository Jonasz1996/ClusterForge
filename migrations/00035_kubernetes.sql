-- +goose Up
-- De samenvatting van elk Kubernetes-cluster, elke 30 seconden via de
-- kube-brug van een agent. Het dashboard leest live; deze tabel is voor de
-- lijst van clusters en voor wat er laatst bekend was als de API niet
-- antwoordt.
CREATE TABLE kube_clusters (
    cluster_id      uuid PRIMARY KEY REFERENCES clusters (id) ON DELETE CASCADE,
    -- api_ok is false als geen agent de API kon bereiken; error zegt waarom.
    api_ok          boolean NOT NULL DEFAULT false,
    error           text NOT NULL DEFAULT '',
    version         text NOT NULL DEFAULT '',
    nodes_total     integer NOT NULL DEFAULT 0,
    nodes_ready     integer NOT NULL DEFAULT 0,
    pods_total      integer NOT NULL DEFAULT 0,
    pods_running    integer NOT NULL DEFAULT 0,
    pods_pending    integer NOT NULL DEFAULT 0,
    -- pods_problem telt de pods die aandacht vragen, zoals CrashLoopBackOff.
    pods_problem    integer NOT NULL DEFAULT 0,
    workloads_total integer NOT NULL DEFAULT 0,
    workloads_ready integer NOT NULL DEFAULT 0,
    -- De node waarvan de agent het laatst antwoordde.
    via_node_id     uuid REFERENCES nodes (id) ON DELETE SET NULL,
    synced_at       timestamptz NOT NULL DEFAULT now(),
    -- Wanneer de API het laatst antwoordde.
    ok_at           timestamptz
);

-- De Kubernetes-nodes van een cluster, met de ClusterForge-node erbij als
-- die te vinden is op adres of naam.
CREATE TABLE kube_nodes (
    cluster_id     uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    name           text NOT NULL,
    node_id        uuid REFERENCES nodes (id) ON DELETE SET NULL,
    roles          text[] NOT NULL DEFAULT '{}',
    ready          boolean NOT NULL,
    unschedulable  boolean NOT NULL DEFAULT false,
    kubelet        text NOT NULL DEFAULT '',
    internal_ip    text NOT NULL DEFAULT '',
    cpu_millis     bigint NOT NULL DEFAULT 0,
    memory_bytes   bigint NOT NULL DEFAULT 0,
    pods           integer NOT NULL DEFAULT 0,
    created_at     timestamptz,
    PRIMARY KEY (cluster_id, name)
);
CREATE INDEX kube_nodes_node ON kube_nodes (node_id) WHERE node_id IS NOT NULL;

-- De leermodus in het dashboard Kubernetes: uitleg en kubectl-commando's.
-- Standaard aan, per gebruiker uit te zetten.
ALTER TABLE users ADD COLUMN kube_learning boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE users DROP COLUMN kube_learning;
DROP TABLE kube_nodes;
DROP TABLE kube_clusters;
