-- +goose Up
-- De status en impact per dienst, zoals deps.Evaluate ze na elke ronde van
-- de evaluator uitrekent. Zo schrijft hij alleen bij een verandering een
-- event, zoals de evaluator bij nodes. status is NULL zolang een dienst nog
-- niet berekend is: de eerste berekening geeft geen event, zodat een
-- upgrade of een nieuwe dienst het logboek niet vult.
ALTER TABLE services
    ADD COLUMN status          text CHECK (status IN ('unknown', 'healthy', 'degraded', 'down')),
    ADD COLUMN status_reason   text NOT NULL DEFAULT '',
    ADD COLUMN impact          text NOT NULL DEFAULT 'none' CHECK (impact IN ('none', 'degraded', 'down')),
    ADD COLUMN impact_reason   text NOT NULL DEFAULT '',
    -- De dienst waar de uitval begint; voor de badge "geraakt door".
    ADD COLUMN impact_cause_id uuid REFERENCES services (id) ON DELETE SET NULL,
    ADD COLUMN impact_since    timestamptz,
    ADD CONSTRAINT services_impact_since CHECK ((impact = 'none') = (impact_since IS NULL));

-- +goose Down
ALTER TABLE services
    DROP CONSTRAINT services_impact_since,
    DROP COLUMN impact_since,
    DROP COLUMN impact_cause_id,
    DROP COLUMN impact_reason,
    DROP COLUMN impact,
    DROP COLUMN status_reason,
    DROP COLUMN status;
