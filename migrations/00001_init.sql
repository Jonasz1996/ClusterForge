-- +goose Up
CREATE TYPE user_role AS ENUM ('admin', 'viewer');

CREATE TABLE users (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username         text NOT NULL,
    password_hash    text NOT NULL,
    role             user_role NOT NULL DEFAULT 'viewer',
    totp_secret      text,
    totp_enabled_at  timestamptz,
    totp_last_step   bigint,          -- laatst gebruikte TOTP-periode, tegen hergebruik van codes
    disabled_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_username_key ON users (lower(username));

CREATE TABLE sessions (
    id            bytea PRIMARY KEY,          -- sha256 van het sessietoken
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token    text NOT NULL,
    ip            text NOT NULL DEFAULT '',
    user_agent    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TYPE actor_type AS ENUM ('user', 'agent', 'system');

-- Append-only tijdlijn van alles wat verandert. Basis voor audit logging,
-- What Broke en de AI-assistent in latere fasen.
CREATE TABLE events (
    id            bigserial PRIMARY KEY,
    ts            timestamptz NOT NULL DEFAULT now(),
    actor_type    actor_type NOT NULL,
    actor_id      text NOT NULL DEFAULT '',
    subject_type  text NOT NULL,
    subject_id    text NOT NULL DEFAULT '',
    cluster_id    uuid,
    action        text NOT NULL,
    payload       jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX events_ts_idx ON events (ts DESC);
CREATE INDEX events_subject_idx ON events (subject_type, subject_id, ts DESC);
CREATE INDEX events_cluster_idx ON events (cluster_id, ts DESC) WHERE cluster_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'events is append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER events_no_update BEFORE UPDATE OR DELETE ON events
    FOR EACH ROW EXECUTE FUNCTION events_append_only();

-- +goose Down
DROP TABLE events;
DROP FUNCTION events_append_only();
DROP TYPE actor_type;
DROP TABLE sessions;
DROP TABLE users;
DROP TYPE user_role;
