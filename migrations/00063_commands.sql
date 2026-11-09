-- +goose Up
-- Eigen commando's (fase 10): scripts die de beheerder in de webinterface
-- schrijft en op gekozen nodes laat draaien, als taak command.run. Elke
-- andere tekst is een nieuwe revisie; een run onthoudt welke.
CREATE TABLE commands (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text NOT NULL,
    description     text NOT NULL DEFAULT '',
    script          text NOT NULL,
    run_as          text NOT NULL DEFAULT 'root',
    timeout_seconds integer NOT NULL DEFAULT 1800,
    revision        integer NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      uuid REFERENCES users (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX commands_name_key ON commands (lower(name));

CREATE TABLE command_revisions (
    command_id  uuid NOT NULL REFERENCES commands (id) ON DELETE CASCADE,
    revision    integer NOT NULL,
    script      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (command_id, revision)
);

-- De runs van een commando zoeken op zijn id in de parameters.
CREATE INDEX jobs_command_idx ON jobs ((params->>'command_id')) WHERE kind = 'command.run';

-- +goose Down
DROP INDEX jobs_command_idx;
DROP TABLE command_revisions;
DROP TABLE commands;
