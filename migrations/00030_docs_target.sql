-- +goose Up
-- Waar de documentatie in Git komt: hoogstens één GitHub-repository, los
-- van GitOps, met een eigen token met schrijfrecht. Het token is versleuteld
-- met de masterkey en komt nooit terug uit de API.
CREATE TABLE docs_targets (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    api_url           text NOT NULL DEFAULT 'https://api.github.com',
    owner             text NOT NULL,
    name              text NOT NULL,
    branch            text NOT NULL DEFAULT 'main',
    -- path is de map in de repository; leeg is de hoofdmap.
    path              text NOT NULL DEFAULT 'docs',
    token_enc         bytea NOT NULL,
    key_id            text NOT NULL,
    enabled           boolean NOT NULL DEFAULT true,
    -- De laatste commit van ClusterForge.
    last_commit_sha   text NOT NULL DEFAULT '',
    last_commit_url   text NOT NULL DEFAULT '',
    last_commit_files integer NOT NULL DEFAULT 0,
    last_published_at timestamptz,
    -- De laatste keer dat de repository gelijk was aan de documentatie,
    -- met of zonder nieuwe commit.
    last_checked_at   timestamptz,
    last_attempt_at   timestamptz,
    last_error        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX docs_targets_single ON docs_targets ((true));

-- +goose Down
DROP TABLE docs_targets;
