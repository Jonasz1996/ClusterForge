-- +goose Up
-- Historie van de gewenste staat van een cluster. Revisie 1 komt van de
-- uitrol uit een template; GitOps voegt later source 'git' toe.
CREATE TABLE cluster_spec_revisions (
    cluster_id  uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    revision    integer NOT NULL,
    spec        jsonb NOT NULL,
    source      text NOT NULL CHECK (source IN ('ui', 'api', 'git')),
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cluster_id, revision)
);

-- Geheimen van een cluster, zoals het VRRP-wachtwoord. Versleuteld met de
-- masterkey; key_id herkent die sleutel.
CREATE TABLE secrets (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id  uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    name        text NOT NULL,
    value_enc   bytea NOT NULL,
    key_id      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, name)
);

-- +goose Down
DROP TABLE secrets;
DROP TABLE cluster_spec_revisions;
