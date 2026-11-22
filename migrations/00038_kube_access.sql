-- +goose Up
-- De kubeconfigs die ClusterForge uitgaf. Elke kubeconfig heeft een eigen
-- ServiceAccount in de namespace clusterforge van het cluster, met een
-- token dat verloopt. Het token zelf bewaart ClusterForge niet: het gaat
-- één keer naar de browser.
CREATE TABLE kube_access (
    id            uuid PRIMARY KEY,
    cluster_id    uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- name is waarvoor de kubeconfig is, zoals laptop.
    name          text NOT NULL,
    role          text NOT NULL CHECK (role IN ('admin', 'view')),
    -- account is het ServiceAccount, en clusterforge:<account> zijn
    -- ClusterRoleBinding.
    account       text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    revoked_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    revoke_reason text NOT NULL DEFAULT '',
    -- removed_at is wanneer het ServiceAccount uit het cluster weg is,
    -- na intrekken of verlopen; remove_error zegt waarom dat nog niet
    -- lukte.
    removed_at    timestamptz,
    remove_error  text NOT NULL DEFAULT ''
);
CREATE INDEX kube_access_cluster_idx ON kube_access (cluster_id, created_at DESC);
CREATE INDEX kube_access_open_idx ON kube_access (cluster_id) WHERE removed_at IS NULL;

-- +goose Down
DROP TABLE kube_access;
