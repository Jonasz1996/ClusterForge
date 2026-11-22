-- +goose Up
-- De laatste health score per cluster, en één rij voor heel ClusterForge
-- (subject_type fleet, subject_id de nul-uuid) met de kaart Platform. De
-- score rekent elke minuut opnieuw; de rij verandert alleen als de uitkomst
-- verandert. announced_grade is het cijfer van het laatste event
-- risk.grade_changed; grade_since is sinds wanneer het huidige cijfer geldt.
CREATE TABLE risk_scores (
    subject_type     text NOT NULL CHECK (subject_type IN ('cluster', 'fleet')),
    subject_id       uuid NOT NULL,
    score            integer CHECK (score BETWEEN 0 AND 100),
    grade            text NOT NULL DEFAULT '',
    result           jsonb NOT NULL,
    computed_at      timestamptz NOT NULL,
    announced_grade  text NOT NULL DEFAULT '',
    grade_since      timestamptz NOT NULL,
    PRIMARY KEY (subject_type, subject_id)
);

-- Elke dag één momentopname per onderwerp, voor de trend. Klein genoeg om
-- altijd te bewaren.
CREATE TABLE risk_snapshots (
    day           date NOT NULL,
    subject_type  text NOT NULL CHECK (subject_type IN ('cluster', 'fleet')),
    subject_id    uuid NOT NULL,
    score         integer CHECK (score BETWEEN 0 AND 100),
    grade         text NOT NULL DEFAULT '',
    cards         jsonb NOT NULL,
    PRIMARY KEY (subject_type, subject_id, day)
);

-- Een bevinding die een beheerder bewust laat staan, met een reden en
-- eventueel een einddatum. Ze kost geen punten maar blijft zichtbaar.
CREATE TABLE risk_acceptances (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    "check"       text NOT NULL CHECK ("check" ~ '^[a-z_]+\.[a-z_]+$'),
    subject_type  text NOT NULL,
    subject_id    uuid NOT NULL,
    subject_name  text NOT NULL DEFAULT '',
    reason        text NOT NULL CHECK (length(reason) BETWEEN 3 AND 500),
    expires_at    timestamptz,
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE ("check", subject_type, subject_id)
);

-- De vervaldatum van het certificaat van Proxmox, bij elke sync gelezen.
ALTER TABLE proxmox_connections ADD COLUMN cert_not_after timestamptz;

-- +goose Down
ALTER TABLE proxmox_connections DROP COLUMN cert_not_after;
DROP TABLE risk_acceptances;
DROP TABLE risk_snapshots;
DROP TABLE risk_scores;
