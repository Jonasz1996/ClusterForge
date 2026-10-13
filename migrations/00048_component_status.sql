-- +goose Up
-- Status van de database (fase 5, mijlpaal 12): de bewaking zegt om de
-- twintig seconden hoe het met de database van een onderdeel gaat, en de
-- status van het cluster neemt dat mee.
ALTER TABLE cluster_components
    -- healthy, degraded of down; leeg zolang de bewaking nog niet keek.
    ADD COLUMN health text NOT NULL DEFAULT '',
    ADD COLUMN health_reason text NOT NULL DEFAULT '',
    -- Wanneer de bewaking het laatst keek. Een oude waarde telt niet mee,
    -- zoals tijdens een taak in het cluster.
    ADD COLUMN health_at timestamptz,
    -- Wat de bewaking de vorige keer zag, om een herstart, een failover of
    -- een state transfer te herkennen: {"at", "primary", "nodes": {id:
    -- {"hostname", "uptime", "state"}}}.
    ADD COLUMN observed jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE cluster_components
    DROP COLUMN health,
    DROP COLUMN health_reason,
    DROP COLUMN health_at,
    DROP COLUMN observed;
