-- +goose Up
-- "Wachtwoord vergeten of kan niet inloggen?": de loginpagina geeft een
-- herstelcode die de server zelf uitgeeft. De beheerder plakt die in
-- `clusterforge-server user reset <code>`. Alleen de hash van de code
-- staat hier; de gebruikersnaam is wat de bezoeker invulde, ongecontroleerd,
-- zodat het antwoord niet verraadt of het account bestaat.
CREATE TABLE password_reset_requests (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash   bytea NOT NULL UNIQUE,
    username    text NOT NULL,
    ip          text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz
);
CREATE INDEX password_reset_requests_expires_idx ON password_reset_requests (expires_at);

-- +goose Down
DROP TABLE password_reset_requests;
