-- name: CreateUser :one
INSERT INTO users (username, password_hash, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE lower(username) = lower($1);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: SetPendingTOTPSecret :exec
UPDATE users SET totp_secret = $2, totp_enabled_at = NULL, totp_last_step = NULL, updated_at = now() WHERE id = $1;

-- name: EnableTOTP :exec
UPDATE users SET totp_enabled_at = now(), updated_at = now() WHERE id = $1 AND totp_secret IS NOT NULL;

-- name: DisableTOTP :exec
UPDATE users SET totp_secret = NULL, totp_enabled_at = NULL, totp_last_step = NULL, updated_at = now() WHERE id = $1;

-- name: UseTOTPStep :execrows
-- Legt de gebruikte TOTP-periode vast; 0 rijen betekent dat de code (of een
-- latere) al gebruikt is.
UPDATE users SET totp_last_step = $2
WHERE id = $1 AND (totp_last_step IS NULL OR totp_last_step < $2);

-- name: ListUsers :many
SELECT id, username, role FROM users WHERE disabled_at IS NULL ORDER BY lower(username);

-- name: ResetUserByCLI :exec
-- Zet een tijdelijk wachtwoord, schakelt TOTP uit en dwingt bij de
-- volgende login een nieuw wachtwoord en een nieuwe TOTP af.
UPDATE users SET
    password_hash = $2,
    temp_password_expires_at = $3,
    temp_password_used_at = NULL,
    must_change_password = true,
    must_setup_totp = true,
    totp_secret = NULL, totp_enabled_at = NULL, totp_last_step = NULL,
    updated_at = now()
WHERE id = $1;

-- name: UseTempPassword :execrows
-- Markeert het tijdelijke wachtwoord als gebruikt; 0 rijen betekent dat het
-- al gebruikt of verlopen is.
UPDATE users SET temp_password_used_at = now()
WHERE id = $1 AND must_change_password
  AND temp_password_used_at IS NULL
  AND temp_password_expires_at > now();

-- name: CompleteForcedPassword :exec
UPDATE users SET password_hash = $2, must_change_password = false,
    temp_password_expires_at = NULL, temp_password_used_at = NULL, updated_at = now()
WHERE id = $1;

-- name: ClearMustSetupTOTP :exec
UPDATE users SET must_setup_totp = false, updated_at = now() WHERE id = $1;

-- name: DeleteAllUserSessions :execrows
DELETE FROM sessions WHERE user_id = $1;
