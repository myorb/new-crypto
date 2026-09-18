-- +goose Up
-- =============================================================================
--  Baseline 2/9 · identity & access
--  users, organizations, memberships, social login, sessions, 2FA, API keys,
--  audit log, idempotency keys
-- =============================================================================
-- // TODO only magick link for now 
-- only one owner per organization, 

CREATE TABLE users (
    id                UUID          PRIMARY KEY DEFAULT uuidv7(),
    email             CITEXT        NOT NULL UNIQUE,
    password_hash     TEXT,                                   -- NULL for social-login-only users
    full_name         TEXT          NOT NULL DEFAULT '',
    avatar_url        TEXT,
    status            user_status   NOT NULL DEFAULT 'active',
    email_verified_at TIMESTAMPTZ,
    mfa_required      BOOLEAN       NOT NULL DEFAULT FALSE,
    last_login_at     TIMESTAMPTZ,
    created_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_users_updated BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();
GRANT SELECT, INSERT, UPDATE ON users TO templ_app;          -- no DELETE: status = 'deleted' + anonymise
-- TODO no legal enitty no slag instead of use short id 
CREATE TABLE organizations (
    id                  UUID                PRIMARY KEY DEFAULT uuidv7(),
    slug                CITEXT              NOT NULL UNIQUE,
    name                TEXT                NOT NULL,
    legal_name          TEXT,
    country_code        CHAR(2),                                -- ISO 3166-1 alpha-2
    status              organization_status NOT NULL DEFAULT 'pending_review',
    kyb_verified_at     TIMESTAMPTZ,
    default_currency    CHAR(3)             NOT NULL DEFAULT 'USD',   -- ISO 4217, pricing / reporting currency
    require_mfa         BOOLEAN             NOT NULL DEFAULT FALSE,
    settings            JSONB               NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ         NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ         NOT NULL DEFAULT now(),
    -- slug is CITEXT, whose ~ operator is case-insensitive: cast so the pattern is enforced literally
    CONSTRAINT organizations_slug_format     CHECK (slug::text ~ '^[a-z0-9](?:[a-z0-9-]{1,61}[a-z0-9])?$'),
    CONSTRAINT organizations_country_format  CHECK (country_code IS NULL OR country_code ~ '^[A-Z]{2}$'),
    CONSTRAINT organizations_currency_format CHECK (default_currency ~ '^[A-Z]{3}$')
);
CREATE TRIGGER trg_organizations_updated BEFORE UPDATE ON organizations FOR EACH ROW EXECUTE FUNCTION set_updated_at();
GRANT SELECT, INSERT, UPDATE ON organizations TO templ_app;  -- no DELETE: status = 'closed'

CREATE TABLE organization_members (
    organization_id UUID              NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id         UUID              NOT NULL REFERENCES users(id)         ON DELETE CASCADE,
    role            organization_role NOT NULL DEFAULT 'viewer',
    invited_by      UUID              REFERENCES users(id),
    joined_at       TIMESTAMPTZ       NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);
CREATE INDEX organization_members_user_idx ON organization_members(user_id);
GRANT SELECT, INSERT, UPDATE, DELETE ON organization_members TO templ_app;

CREATE TABLE organization_invitations (
    id              UUID              PRIMARY KEY DEFAULT uuidv7(),
    organization_id UUID              NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    email           CITEXT            NOT NULL,
    role            organization_role NOT NULL DEFAULT 'viewer',
    token_hash      BYTEA             NOT NULL UNIQUE,
    invited_by      UUID              NOT NULL REFERENCES users(id),
    expires_at      TIMESTAMPTZ       NOT NULL,
    accepted_at     TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ       NOT NULL DEFAULT now(),
    CONSTRAINT organization_invitations_expiry CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX organization_invitations_open_uq
    ON organization_invitations(organization_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL;
GRANT SELECT, INSERT, UPDATE ON organization_invitations TO templ_app;   -- revoke via revoked_at

-- ---- Social / OAuth login ----------------------------------------------------
-- *_enc columns hold ciphertext produced by the app (envelope encryption, key
-- id embedded in the ciphertext header). The database never sees plaintext.
CREATE TABLE user_identities (
    id                UUID          PRIMARY KEY DEFAULT uuidv7(),
    user_id           UUID          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider          auth_provider NOT NULL,
    provider_user_id  TEXT          NOT NULL,
    provider_email    CITEXT,
    email_verified    BOOLEAN       NOT NULL DEFAULT FALSE,
    display_name      TEXT,
    avatar_url        TEXT,
    access_token_enc  BYTEA,
    refresh_token_enc BYTEA,
    token_expires_at  TIMESTAMPTZ,
    raw_profile       JSONB         NOT NULL DEFAULT '{}'::jsonb,
    last_login_at     TIMESTAMPTZ,
    created_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_user_id),
    UNIQUE (user_id, provider)
);
CREATE TRIGGER trg_user_identities_updated BEFORE UPDATE ON user_identities FOR EACH ROW EXECUTE FUNCTION set_updated_at();
GRANT SELECT, INSERT, UPDATE, DELETE ON user_identities TO templ_app;    -- DELETE = unlink a social login

CREATE TABLE oauth_states (
    state_hash        BYTEA         PRIMARY KEY,
    provider          auth_provider NOT NULL,
    pkce_verifier_enc BYTEA,
    nonce             TEXT,
    redirect_uri      TEXT          NOT NULL,
    link_to_user_id   UUID          REFERENCES users(id) ON DELETE CASCADE,
    invitation_id     UUID          REFERENCES organization_invitations(id) ON DELETE SET NULL,
    ip_address        INET,
    created_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ   NOT NULL DEFAULT now() + INTERVAL '10 minutes',
    consumed_at       TIMESTAMPTZ
);
CREATE INDEX oauth_states_expires_idx ON oauth_states(expires_at);
GRANT SELECT, INSERT, UPDATE, DELETE ON oauth_states TO templ_app;       -- ephemeral: app purges expired rows

-- ---- Sessions ----------------------------------------------------------------
CREATE TABLE sessions (
    id              UUID        PRIMARY KEY DEFAULT uuidv7(),
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash      BYTEA       NOT NULL UNIQUE,
    identity_id     UUID        REFERENCES user_identities(id) ON DELETE SET NULL,  -- NULL = password login
    mfa_verified_at TIMESTAMPTZ,
    ip_address      INET,
    user_agent      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ,
    CONSTRAINT sessions_expiry CHECK (expires_at > created_at)
);
CREATE INDEX sessions_user_idx    ON sessions(user_id) WHERE revoked_at IS NULL;
CREATE INDEX sessions_expires_idx ON sessions(expires_at);                -- purge job
GRANT SELECT, INSERT, UPDATE, DELETE ON sessions TO templ_app;

-- ---- Two-factor authentication ----------------------------------------------
-- TODO consider to use auth app form google
CREATE TABLE user_mfa_methods (
    id               UUID            PRIMARY KEY DEFAULT uuidv7(),
    user_id          UUID            NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type             mfa_method_type NOT NULL,
    label            TEXT            NOT NULL DEFAULT '',
    totp_secret_enc  BYTEA,
    credential_id    BYTEA,
    public_key       BYTEA,
    sign_count       BIGINT,
    aaguid           UUID,
    transports       TEXT[],
    destination_enc  BYTEA,                                   -- encrypted phone / e-mail for sms / email
    is_primary       BOOLEAN         NOT NULL DEFAULT FALSE,
    verified_at      TIMESTAMPTZ,
    last_used_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ     NOT NULL DEFAULT now(),
    CONSTRAINT user_mfa_methods_shape CHECK (
        (type = 'totp'     AND totp_secret_enc IS NOT NULL) OR
        (type = 'webauthn' AND credential_id IS NOT NULL AND public_key IS NOT NULL) OR
        (type IN ('sms', 'email') AND destination_enc IS NOT NULL)
    ),
    CONSTRAINT user_mfa_methods_sign_count CHECK (sign_count IS NULL OR sign_count >= 0)
);
CREATE UNIQUE INDEX user_mfa_methods_credential_uq ON user_mfa_methods(credential_id) WHERE credential_id IS NOT NULL;
CREATE UNIQUE INDEX user_mfa_methods_primary_uq    ON user_mfa_methods(user_id) WHERE is_primary AND verified_at IS NOT NULL;
CREATE INDEX        user_mfa_methods_user_idx      ON user_mfa_methods(user_id) WHERE verified_at IS NOT NULL;
GRANT SELECT, INSERT, UPDATE, DELETE ON user_mfa_methods TO templ_app;

CREATE TABLE user_mfa_recovery_codes (
    id         UUID        PRIMARY KEY DEFAULT uuidv7(),
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  BYTEA       NOT NULL UNIQUE,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX user_mfa_recovery_codes_user_idx ON user_mfa_recovery_codes(user_id) WHERE used_at IS NULL;
GRANT SELECT, INSERT, UPDATE, DELETE ON user_mfa_recovery_codes TO templ_app;   -- DELETE = regenerate the set

CREATE TABLE user_trusted_devices (
    id                UUID        PRIMARY KEY DEFAULT uuidv7(),
    user_id           UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_token_hash BYTEA       NOT NULL UNIQUE,
    name              TEXT,
    ip_address        INET,
    user_agent        TEXT,
    last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '30 days',
    revoked_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX user_trusted_devices_user_idx    ON user_trusted_devices(user_id) WHERE revoked_at IS NULL;
CREATE INDEX user_trusted_devices_expires_idx ON user_trusted_devices(expires_at);
GRANT SELECT, INSERT, UPDATE, DELETE ON user_trusted_devices TO templ_app;

CREATE TABLE auth_challenges (
    id                 UUID                   PRIMARY KEY DEFAULT uuidv7(),
    user_id            UUID                   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id         UUID                   REFERENCES sessions(id) ON DELETE CASCADE,
    identity_id        UUID                   REFERENCES user_identities(id) ON DELETE SET NULL,
    purpose            auth_challenge_purpose NOT NULL DEFAULT 'login',
    token_hash         BYTEA                  NOT NULL UNIQUE,
    method_id          UUID                   REFERENCES user_mfa_methods(id) ON DELETE SET NULL,
    webauthn_challenge BYTEA,
    otp_hash           BYTEA,
    attempts           SMALLINT               NOT NULL DEFAULT 0,
    max_attempts       SMALLINT               NOT NULL DEFAULT 5,
    ip_address         INET,
    user_agent         TEXT,
    created_at         TIMESTAMPTZ            NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ            NOT NULL DEFAULT now() + INTERVAL '5 minutes',
    verified_at        TIMESTAMPTZ,
    CONSTRAINT auth_challenges_attempts CHECK (max_attempts > 0 AND attempts BETWEEN 0 AND max_attempts)
);
CREATE INDEX auth_challenges_user_idx    ON auth_challenges(user_id) WHERE verified_at IS NULL;
CREATE INDEX auth_challenges_expires_idx ON auth_challenges(expires_at);
GRANT SELECT, INSERT, UPDATE, DELETE ON auth_challenges TO templ_app;

-- Append-only from the app's point of view (PII: e-mail + IP). Retention is an
-- ops job run as the migrator role; see db/README.md.
CREATE TABLE login_attempts (
    id             BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email          CITEXT,
    user_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    provider       auth_provider,
    ip_address     INET        NOT NULL,
    user_agent     TEXT,
    success        BOOLEAN     NOT NULL,
    failure_reason TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX login_attempts_ip_idx      ON login_attempts(ip_address, created_at DESC);
CREATE INDEX login_attempts_email_idx   ON login_attempts(email, created_at DESC);
CREATE INDEX login_attempts_created_brin ON login_attempts USING BRIN (created_at);   -- cheap range scans for retention
GRANT SELECT, INSERT ON login_attempts TO templ_app;

-- ---- API access & audit ------------------------------------------------------
CREATE TABLE api_keys (
    id              UUID        PRIMARY KEY DEFAULT uuidv7(),
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    key_prefix      TEXT        NOT NULL,                     -- first chars shown in the UI, never the key
    key_hash        BYTEA       NOT NULL UNIQUE,
    scopes          TEXT[]      NOT NULL DEFAULT '{}',
    ip_allowlist    CIDR[],
    created_by      UUID        REFERENCES users(id),
    last_used_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT api_keys_prefix_not_empty CHECK (key_prefix <> ''),
    CONSTRAINT api_keys_expiry           CHECK (expires_at IS NULL OR expires_at > created_at)
);
CREATE INDEX api_keys_org_idx ON api_keys(organization_id) WHERE revoked_at IS NULL;
GRANT SELECT, INSERT, UPDATE ON api_keys TO templ_app;       -- revoke via revoked_at, never DELETE

-- Immutable. Note: because rows can never change, an ON DELETE SET NULL from
-- users / organizations / api_keys fails once a row references them - which is
-- intended: those entities are closed or anonymised, never hard-deleted.
CREATE TABLE audit_logs (
    id               BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id  UUID         REFERENCES organizations(id) ON DELETE SET NULL,
    actor_user_id    UUID         REFERENCES users(id)         ON DELETE SET NULL,
    actor_api_key_id UUID         REFERENCES api_keys(id)      ON DELETE SET NULL,
    action           TEXT         NOT NULL,
    entity_type      TEXT,
    entity_id        TEXT,
    ip_address       INET,
    metadata         JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_org_created_idx ON audit_logs(organization_id, created_at DESC);
CREATE INDEX audit_logs_entity_idx      ON audit_logs(entity_type, entity_id);
CREATE INDEX audit_logs_created_brin    ON audit_logs USING BRIN (created_at);
CREATE TRIGGER trg_audit_logs_immutable   BEFORE UPDATE OR DELETE ON audit_logs FOR EACH ROW       EXECUTE FUNCTION forbid_change();
CREATE TRIGGER trg_audit_logs_no_truncate BEFORE TRUNCATE         ON audit_logs FOR EACH STATEMENT EXECUTE FUNCTION forbid_change();
GRANT SELECT, INSERT ON audit_logs TO templ_app;

CREATE TABLE idempotency_keys (
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    idempotency_key TEXT        NOT NULL,
    request_hash    BYTEA       NOT NULL,
    response_code   SMALLINT,
    response_body   JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '24 hours',
    PRIMARY KEY (organization_id, idempotency_key)
);
CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys(expires_at);
GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys TO templ_app;

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE audit_logs;
DROP TABLE api_keys;
DROP TABLE login_attempts;
DROP TABLE auth_challenges;
DROP TABLE user_trusted_devices;
DROP TABLE user_mfa_recovery_codes;
DROP TABLE user_mfa_methods;
DROP TABLE sessions;
DROP TABLE oauth_states;
DROP TABLE user_identities;
DROP TABLE organization_invitations;
DROP TABLE organization_members;
DROP TABLE organizations;
DROP TABLE users;
