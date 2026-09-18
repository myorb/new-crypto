-- Identity: users, sessions, MFA and login auditing. Owned by internal/identity.

-- name: CreateUser :one
INSERT INTO users (email, password_hash, full_name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: SetUserLastLogin :exec
UPDATE users SET last_login_at = now() WHERE id = $1;

-- name: MarkUserEmailVerified :exec
UPDATE users SET email_verified_at = COALESCE(email_verified_at, now()) WHERE id = $1;

-- name: UpdateUserProfile :one
UPDATE users SET full_name = $2, avatar_url = $3
WHERE id = $1
RETURNING *;

-- name: SetUserStatus :exec
UPDATE users SET status = $2 WHERE id = $1;

-- name: SetUserMFARequired :exec
UPDATE users SET mfa_required = $2 WHERE id = $1;

-- Sessions ------------------------------------------------------------------

-- name: CreateSession :one
INSERT INTO sessions (user_id, token_hash, identity_id, ip_address, user_agent, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetSessionByTokenHash :one
SELECT sqlc.embed(s), sqlc.embed(u)
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1
  AND s.revoked_at IS NULL
  AND s.expires_at > now();

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now() WHERE id = $1;

-- name: SetSessionMFAVerified :exec
UPDATE sessions SET mfa_verified_at = now() WHERE id = $1;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeUserSessions :execrows
UPDATE sessions SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL AND id <> sqlc.narg('keep_session_id');

-- name: ListUserSessions :many
SELECT * FROM sessions
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now() - INTERVAL '7 days';

-- Login attempts -------------------------------------------------------------

-- name: RecordLoginAttempt :exec
INSERT INTO login_attempts (email, user_id, provider, ip_address, user_agent, success, failure_reason)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: CountRecentFailedLogins :one
SELECT count(*) FROM login_attempts
WHERE NOT success
  AND created_at > now() - make_interval(secs => sqlc.arg('window_seconds')::float8)
  AND (email = sqlc.narg('email') OR ip_address = sqlc.narg('ip_address'));

-- MFA -------------------------------------------------------------------------

-- name: ListUserMFAMethods :many
SELECT * FROM user_mfa_methods
WHERE user_id = $1
ORDER BY is_primary DESC, created_at;

-- name: GetMFAMethod :one
SELECT * FROM user_mfa_methods WHERE id = $1 AND user_id = $2;

-- name: CreateTOTPMethod :one
INSERT INTO user_mfa_methods (user_id, type, label, totp_secret_enc)
VALUES ($1, 'totp', $2, $3)
RETURNING *;

-- name: MarkMFAMethodVerified :exec
UPDATE user_mfa_methods
SET verified_at = COALESCE(verified_at, now()),
    is_primary  = $3,
    last_used_at = now()
WHERE id = $1 AND user_id = $2;

-- name: TouchMFAMethod :exec
UPDATE user_mfa_methods SET last_used_at = now() WHERE id = $1;

-- name: DeleteMFAMethod :execrows
DELETE FROM user_mfa_methods WHERE id = $1 AND user_id = $2;

-- name: DeleteUserRecoveryCodes :execrows
DELETE FROM user_mfa_recovery_codes WHERE user_id = $1;

-- name: CreateRecoveryCode :exec
INSERT INTO user_mfa_recovery_codes (user_id, code_hash) VALUES ($1, $2);

-- name: UseRecoveryCode :execrows
UPDATE user_mfa_recovery_codes SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM user_mfa_recovery_codes WHERE user_id = $1 AND used_at IS NULL;

-- Social identities -----------------------------------------------------------

-- name: GetUserIdentity :one
SELECT * FROM user_identities WHERE provider = $1 AND provider_user_id = $2;

-- name: UpsertUserIdentity :one
INSERT INTO user_identities (user_id, provider, provider_user_id, provider_email, email_verified, display_name, avatar_url, raw_profile, last_login_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
ON CONFLICT (provider, provider_user_id) DO UPDATE
    SET provider_email = EXCLUDED.provider_email,
        email_verified = EXCLUDED.email_verified,
        display_name   = EXCLUDED.display_name,
        avatar_url     = EXCLUDED.avatar_url,
        raw_profile    = EXCLUDED.raw_profile,
        last_login_at  = now()
RETURNING *;

-- name: ListUserIdentities :many
SELECT * FROM user_identities WHERE user_id = $1 ORDER BY created_at;

-- name: DeleteUserIdentity :execrows
DELETE FROM user_identities WHERE id = $1 AND user_id = $2;
