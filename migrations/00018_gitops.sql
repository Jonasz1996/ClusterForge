-- +goose Up
-- De koppeling met één Git-repository voor GitOps. Er is hoogstens één rij.
-- Het token is versleuteld met de masterkey (AAD git:<id>) en komt nooit
-- terug uit de API. scan is de laatste stand per clusterbestand: pad, slug,
-- toestand en fouten met veld en regel; een bestand met een geheim erin
-- wordt alleen met zijn fout bewaard.
CREATE TABLE git_repos (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    api_url       text NOT NULL DEFAULT 'https://api.github.com',
    owner         text NOT NULL,
    name          text NOT NULL,
    branch        text NOT NULL DEFAULT 'main',
    path          text NOT NULL DEFAULT 'clusters',
    token_enc     bytea NOT NULL,
    key_id        text NOT NULL,
    head_sha      text NOT NULL DEFAULT '',
    head_etag     text NOT NULL DEFAULT '',
    synced_sha    text NOT NULL DEFAULT '',
    head_commit   jsonb,
    scan          jsonb NOT NULL DEFAULT '[]',
    last_sync_at  timestamptz,
    last_error    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX git_repos_single ON git_repos ((true));

-- Eén wijziging van één clusterbestand in één commit: het plan, de
-- beslissing en de herkomst. spec en metadata zijn de nieuwe gewenste staat
-- zonder geheimen. Per slug hoogstens één wachtende en één lopende.
CREATE TABLE git_changes (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id          uuid NOT NULL REFERENCES git_repos (id) ON DELETE CASCADE,
    cluster_id       uuid REFERENCES clusters (id) ON DELETE SET NULL,
    slug             text NOT NULL,
    path             text NOT NULL,
    kind             text NOT NULL CHECK (kind IN ('create', 'update')),
    commit_sha       text NOT NULL,
    commit_message   text NOT NULL DEFAULT '',
    commit_author    text NOT NULL DEFAULT '',
    commit_verified  boolean NOT NULL DEFAULT false,
    commit_url       text NOT NULL DEFAULT '',
    committed_at     timestamptz,
    blob_sha         text NOT NULL,
    base_revision    integer NOT NULL DEFAULT 0,
    spec             jsonb NOT NULL,
    metadata         jsonb NOT NULL,
    plan             jsonb NOT NULL,
    status           text NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'applying', 'applied', 'failed', 'rejected', 'superseded')),
    job_id           uuid REFERENCES jobs (id) ON DELETE SET NULL,
    decided_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    decided_at       timestamptz,
    reason           text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX git_changes_pending ON git_changes (slug) WHERE status = 'pending';
CREATE UNIQUE INDEX git_changes_applying ON git_changes (slug) WHERE status = 'applying';
CREATE INDEX git_changes_cluster ON git_changes (cluster_id, created_at DESC);

-- Is git_repo_id gezet, dan is Git de bron van waarheid voor het cluster.
ALTER TABLE clusters ADD COLUMN git_repo_id uuid REFERENCES git_repos (id) ON DELETE SET NULL;

-- Elke revisie uit Git wijst naar haar commit.
ALTER TABLE cluster_spec_revisions ADD COLUMN commit_sha text;
ALTER TABLE cluster_spec_revisions ADD CONSTRAINT cluster_spec_revisions_git_commit
    CHECK (source <> 'git' OR commit_sha IS NOT NULL);

-- +goose Down
ALTER TABLE cluster_spec_revisions DROP CONSTRAINT cluster_spec_revisions_git_commit;
ALTER TABLE cluster_spec_revisions DROP COLUMN commit_sha;
ALTER TABLE clusters DROP COLUMN git_repo_id;
DROP TABLE git_changes;
DROP TABLE git_repos;
