-- +goose Up
-- Account herstellen via de CLI (`clusterforge-server user reset`): een
-- tijdelijk wachtwoord dat één keer werkt en snel verloopt, waarna de
-- gebruiker een nieuw wachtwoord en een nieuwe TOTP moet instellen.
ALTER TABLE users
    ADD COLUMN temp_password_expires_at timestamptz,
    ADD COLUMN temp_password_used_at    timestamptz,
    ADD COLUMN must_change_password     boolean NOT NULL DEFAULT false,
    ADD COLUMN must_setup_totp          boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users
    DROP COLUMN temp_password_expires_at,
    DROP COLUMN temp_password_used_at,
    DROP COLUMN must_change_password,
    DROP COLUMN must_setup_totp;
