-- +goose Up
-- Opkomen na een herstart (fase 5, mijlpaal 5): ClusterForge kijkt om de
-- twintig seconden of de database van een onderdeel draait, en begint een
-- Galera-cluster na een volledige uitval opnieuw op de node met de nieuwste
-- data.
ALTER TABLE cluster_components
    -- Na een volledige uitval vanzelf opnieuw beginnen, zodra alle
    -- datanodes er zijn.
    ADD COLUMN auto_recover boolean NOT NULL DEFAULT true,
    -- Sinds wanneer geen enkele node in een Primary component zit, en
    -- waarom; NULL zolang de database draait.
    ADD COLUMN down_since timestamptz,
    ADD COLUMN down_reason text NOT NULL DEFAULT '',
    -- De nodes die niet meedoen terwijl het cluster draait, per node-id:
    -- {"hostname", "since", "reason", "reported"}.
    ADD COLUMN nodes_out jsonb NOT NULL DEFAULT '{}',
    -- Wanneer ClusterForge voor het laatst zelf een herstel begon.
    ADD COLUMN auto_recovered_at timestamptz,
    -- De laatste taak die de database opnieuw startte.
    ADD COLUMN recover_job_id uuid REFERENCES jobs (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE cluster_components
    DROP COLUMN auto_recover,
    DROP COLUMN down_since,
    DROP COLUMN down_reason,
    DROP COLUMN nodes_out,
    DROP COLUMN auto_recovered_at,
    DROP COLUMN recover_job_id;
