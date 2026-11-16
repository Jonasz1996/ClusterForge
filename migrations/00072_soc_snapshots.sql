-- +goose Up
-- SOC (fase 25): elk uur de beveiligingsscore en het dreigingsniveau, voor
-- de trend op de pagina SOC. 90 dagen.
CREATE TABLE soc_snapshots (
    hour  timestamptz PRIMARY KEY,
    score integer NOT NULL CHECK (score BETWEEN 0 AND 100),
    level text NOT NULL CHECK (level IN ('normaal', 'waakzaam', 'verhoogd', 'kritiek'))
);

-- +goose Down
DROP TABLE soc_snapshots;
