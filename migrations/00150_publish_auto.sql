-- +goose Up
-- Fase 48: automatisch publiceren. Een regel per cluster of per dienst zet
-- het aan; per dienst houdt publish_auto_items bij wat ClusterForge
-- vanzelf deed (publiceren, mislukt, opgeruimd bij decomm). Geen foreign key
-- op cluster_id in de items: na het verwijderen van een cluster moet de rij
-- blijven, want dan ruimt ClusterForge de host op.
CREATE TABLE publish_auto_rules (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id  uuid REFERENCES clusters (id) ON DELETE CASCADE,
    service_key text,
    enabled     boolean NOT NULL DEFAULT true,
    -- cleanup: bij decomm de host, provider en app weer weghalen.
    cleanup     boolean NOT NULL DEFAULT true,
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((cluster_id IS NULL) <> (service_key IS NULL))
);
CREATE UNIQUE INDEX publish_auto_rules_cluster ON publish_auto_rules (cluster_id) WHERE cluster_id IS NOT NULL;
CREATE UNIQUE INDEX publish_auto_rules_service ON publish_auto_rules (service_key) WHERE service_key IS NOT NULL;

CREATE TABLE publish_auto_items (
    service_key  text PRIMARY KEY,
    domain       text NOT NULL,
    cluster_id   uuid,
    cluster_name text NOT NULL DEFAULT '',
    -- state: publishing, published, failed, removing, removed.
    state        text NOT NULL,
    job_id       uuid,
    npm_host_id  integer,
    cleanup      boolean NOT NULL DEFAULT true,
    tries        integer NOT NULL DEFAULT 0,
    error        text NOT NULL DEFAULT '',
    -- backup: de host zoals NPM hem had, vlak voor het opruimen.
    backup       jsonb,
    tried_at     timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE publish_auto_items;
DROP TABLE publish_auto_rules;
