-- +goose Up
-- CrowdSec Console (fase 14): de enroll-sleutel versleuteld met de
-- masterkey, en per node met welke sleutel hij aangemeld is.
ALTER TABLE crowdsec_settings
    ADD COLUMN console_key_enc bytea,
    ADD COLUMN console_key_id text NOT NULL DEFAULT '',
    ADD COLUMN console_key_hash text NOT NULL DEFAULT '';

ALTER TABLE crowdsec_nodes
    ADD COLUMN console_hash text NOT NULL DEFAULT '',
    ADD COLUMN console_enrolled_at timestamptz,
    ADD COLUMN console_error text NOT NULL DEFAULT '',
    ADD COLUMN console_failed_at timestamptz;

-- +goose Down
ALTER TABLE crowdsec_nodes
    DROP COLUMN console_failed_at,
    DROP COLUMN console_error,
    DROP COLUMN console_enrolled_at,
    DROP COLUMN console_hash;

ALTER TABLE crowdsec_settings
    DROP COLUMN console_key_hash,
    DROP COLUMN console_key_id,
    DROP COLUMN console_key_enc;
