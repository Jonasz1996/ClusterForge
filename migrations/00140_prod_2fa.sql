-- +goose Up
-- B1b testronde 2026-11-22: tweestapsverificatie voor acties op prod is
-- instelbaar. Nieuwe installaties: aan. Een bestaande installatie (er zijn
-- al gebruikers) start uit, op vraag van de eigenaar; het intikken van de
-- clusternaam blijft altijd.
CREATE TABLE security_settings (
    id               boolean PRIMARY KEY DEFAULT true CHECK (id),
    prod_require_2fa boolean NOT NULL DEFAULT true,
    updated_at       timestamptz,
    updated_by       uuid REFERENCES users (id) ON DELETE SET NULL
);
INSERT INTO security_settings (prod_require_2fa) SELECT NOT EXISTS (SELECT 1 FROM users);

-- +goose Down
DROP TABLE security_settings;
