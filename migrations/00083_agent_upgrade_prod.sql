-- +goose Up
-- Productie-agents makkelijker bijwerken. prod_auto: verouderde agents op
-- prod ook vanzelf bijwerken (standaard uit; aanzetten vraagt een code van
-- tweestapsverificatie). agent_upgrade_prod_queue: de prodclusters van één
-- klik "ook productie", die ClusterForge één voor één bijwerkt; bij een fout
-- stopt de hele rij.
ALTER TABLE agent_upgrade_settings ADD COLUMN prod_auto boolean NOT NULL DEFAULT false;

CREATE TABLE agent_upgrade_prod_queue (
    cluster_id    uuid PRIMARY KEY REFERENCES clusters (id) ON DELETE CASCADE,
    position      integer NOT NULL,
    job_id        uuid REFERENCES jobs (id) ON DELETE SET NULL,
    requested_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE agent_upgrade_prod_queue;
ALTER TABLE agent_upgrade_settings DROP COLUMN prod_auto;
