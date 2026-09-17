-- =============================================================================
--  Crypto Payment Provider — PostgreSQL schema
--  v1.0 · Multi-provider, multi-network, multi-asset
--        Organizations, users, 2FA, social login
-- =============================================================================
--  Conventions
--   * All PKs are UUIDs (gen_random_uuid()); reference tables use SMALLSERIAL.
--   * All timestamps are TIMESTAMPTZ.
--   * Money is NUMERIC(38,18) in human units (1.5 = 1.5 USDT); the asset row
--     carries `decimals` for conversion to on-chain base units.
--   * Chain-specific formats (addresses, tx hashes) are validated by regexes
--     stored on the `networks` row and enforced with triggers, so adding a new
--     chain is a data change, not a schema change.
--   * Rows are never hard-deleted where money is involved; use status columns.
--
--  Layering
--     payment_providers  = how we talk to a rail (own node / RPC, custodial API
--                          such as Fireblocks, exchange rail such as Binance Pay)
--     networks           = the chain itself (tron, ethereum, bsc, bitcoin, ...)
--     provider_networks  = which provider serves which network, with capabilities
--     assets             = native coin or token on one network
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

-- -----------------------------------------------------------------------------
-- Domains
-- -----------------------------------------------------------------------------
CREATE DOMAIN crypto_amount AS NUMERIC(38, 18);

-- -----------------------------------------------------------------------------
-- Enums
-- -----------------------------------------------------------------------------
-- identity
CREATE TYPE user_status              AS ENUM ('active', 'suspended', 'deleted');
CREATE TYPE organization_status      AS ENUM ('pending_review', 'active', 'suspended', 'closed');
CREATE TYPE organization_role        AS ENUM ('owner', 'admin', 'developer', 'finance', 'viewer');
CREATE TYPE auth_provider            AS ENUM ('google', 'facebook', 'apple', 'github', 'microsoft');
CREATE TYPE mfa_method_type          AS ENUM ('totp', 'webauthn', 'sms', 'email');
CREATE TYPE auth_challenge_purpose   AS ENUM ('login', 'sensitive_action');
-- rails
CREATE TYPE provider_kind            AS ENUM ('node', 'custodial_api', 'exchange_rail');
CREATE TYPE network_family           AS ENUM ('tron', 'evm', 'bitcoin', 'solana', 'ton', 'other');
CREATE TYPE asset_kind               AS ENUM ('native', 'token');
CREATE TYPE wallet_kind              AS ENUM ('deposit', 'hot', 'cold', 'fee');
-- money movement
CREATE TYPE invoice_status           AS ENUM ('new', 'partial', 'paid', 'confirmed', 'completed', 'expired', 'cancelled');
CREATE TYPE payment_status           AS ENUM ('detected', 'confirmed', 'credited', 'reverted');
CREATE TYPE tx_status                AS ENUM ('pending', 'confirmed', 'failed', 'dropped');
CREATE TYPE transfer_direction       AS ENUM ('in', 'out', 'internal');
CREATE TYPE withdrawal_status        AS ENUM ('pending_approval', 'approved', 'queued', 'broadcast', 'confirmed', 'failed', 'rejected', 'cancelled');
CREATE TYPE internal_transfer_kind   AS ENUM ('sweep', 'fee_topup', 'rebalance');
CREATE TYPE internal_transfer_status AS ENUM ('queued', 'broadcast', 'confirmed', 'failed');
CREATE TYPE ledger_account_type      AS ENUM (
    'merchant_available', 'merchant_pending', 'merchant_locked',
    'platform_fee_revenue', 'platform_network_fees', 'platform_hot_wallet', 'platform_cold_wallet'
);
CREATE TYPE webhook_delivery_status  AS ENUM ('pending', 'succeeded', 'failed', 'exhausted');

-- -----------------------------------------------------------------------------
-- Helpers
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION forbid_change() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME;
END $$;

-- =============================================================================
-- 1. IDENTITY & ACCESS
-- =============================================================================

CREATE TABLE users (
    id                UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
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

CREATE TABLE organizations (
    id                  UUID                PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                CITEXT              NOT NULL UNIQUE,
    name                TEXT                NOT NULL,
    legal_name          TEXT,
    country_code        CHAR(2),
    status              organization_status NOT NULL DEFAULT 'pending_review',
    kyb_verified_at     TIMESTAMPTZ,
    default_currency    CHAR(3)             NOT NULL DEFAULT 'USD',   -- pricing / reporting currency
    require_mfa         BOOLEAN             NOT NULL DEFAULT FALSE,
    settings            JSONB               NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ         NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ         NOT NULL DEFAULT now(),
    CONSTRAINT organizations_slug_format CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{1,61}[a-z0-9])?$')
);
CREATE TRIGGER trg_organizations_updated BEFORE UPDATE ON organizations FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE organization_members (
    organization_id UUID              NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id         UUID              NOT NULL REFERENCES users(id)         ON DELETE CASCADE,
    role            organization_role NOT NULL DEFAULT 'viewer',
    invited_by      UUID              REFERENCES users(id),
    joined_at       TIMESTAMPTZ       NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);
CREATE INDEX organization_members_user_idx ON organization_members(user_id);

CREATE TABLE organization_invitations (
    id              UUID              PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID              NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    email           CITEXT            NOT NULL,
    role            organization_role NOT NULL DEFAULT 'viewer',
    token_hash      BYTEA             NOT NULL UNIQUE,
    invited_by      UUID              NOT NULL REFERENCES users(id),
    expires_at      TIMESTAMPTZ       NOT NULL,
    accepted_at     TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ       NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX organization_invitations_open_uq
    ON organization_invitations(organization_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- ---- Social / OAuth login ----------------------------------------------------
CREATE TABLE user_identities (
    id                UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
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

-- ---- Sessions ----------------------------------------------------------------
CREATE TABLE sessions (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash      BYTEA       NOT NULL UNIQUE,
    identity_id     UUID        REFERENCES user_identities(id) ON DELETE SET NULL,  -- NULL = password login
    mfa_verified_at TIMESTAMPTZ,
    ip_address      INET,
    user_agent      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ
);
CREATE INDEX sessions_user_idx ON sessions(user_id) WHERE revoked_at IS NULL;

-- ---- Two-factor authentication ----------------------------------------------
CREATE TABLE user_mfa_methods (
    id               UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID            NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type             mfa_method_type NOT NULL,
    label            TEXT            NOT NULL DEFAULT '',
    totp_secret_enc  BYTEA,
    credential_id    BYTEA,
    public_key       BYTEA,
    sign_count       BIGINT,
    aaguid           UUID,
    transports       TEXT[],
    destination_enc  BYTEA,
    is_primary       BOOLEAN         NOT NULL DEFAULT FALSE,
    verified_at      TIMESTAMPTZ,
    last_used_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ     NOT NULL DEFAULT now(),
    CONSTRAINT user_mfa_methods_shape CHECK (
        (type = 'totp'     AND totp_secret_enc IS NOT NULL) OR
        (type = 'webauthn' AND credential_id IS NOT NULL AND public_key IS NOT NULL) OR
        (type IN ('sms', 'email') AND destination_enc IS NOT NULL)
    )
);
CREATE UNIQUE INDEX user_mfa_methods_credential_uq ON user_mfa_methods(credential_id) WHERE credential_id IS NOT NULL;
CREATE UNIQUE INDEX user_mfa_methods_primary_uq    ON user_mfa_methods(user_id) WHERE is_primary AND verified_at IS NOT NULL;
CREATE INDEX        user_mfa_methods_user_idx      ON user_mfa_methods(user_id) WHERE verified_at IS NOT NULL;

CREATE TABLE user_mfa_recovery_codes (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  BYTEA       NOT NULL UNIQUE,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX user_mfa_recovery_codes_user_idx ON user_mfa_recovery_codes(user_id) WHERE used_at IS NULL;

CREATE TABLE user_trusted_devices (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
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
CREATE INDEX user_trusted_devices_user_idx ON user_trusted_devices(user_id) WHERE revoked_at IS NULL;

CREATE TABLE auth_challenges (
    id                 UUID                   PRIMARY KEY DEFAULT gen_random_uuid(),
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
    CONSTRAINT auth_challenges_attempts CHECK (attempts <= max_attempts)
);
CREATE INDEX auth_challenges_user_idx    ON auth_challenges(user_id) WHERE verified_at IS NULL;
CREATE INDEX auth_challenges_expires_idx ON auth_challenges(expires_at);

CREATE TABLE login_attempts (
    id             BIGSERIAL   PRIMARY KEY,
    email          CITEXT,
    user_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    provider       auth_provider,
    ip_address     INET        NOT NULL,
    user_agent     TEXT,
    success        BOOLEAN     NOT NULL,
    failure_reason TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX login_attempts_ip_idx    ON login_attempts(ip_address, created_at DESC);
CREATE INDEX login_attempts_email_idx ON login_attempts(email, created_at DESC);

-- ---- API access & audit ------------------------------------------------------
CREATE TABLE api_keys (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    key_prefix      TEXT        NOT NULL,
    key_hash        BYTEA       NOT NULL UNIQUE,
    scopes          TEXT[]      NOT NULL DEFAULT '{}',
    ip_allowlist    CIDR[],
    created_by      UUID        REFERENCES users(id),
    last_used_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_org_idx ON api_keys(organization_id) WHERE revoked_at IS NULL;

CREATE TABLE audit_logs (
    id               BIGSERIAL    PRIMARY KEY,
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

-- =============================================================================
-- 2. PAYMENT PROVIDERS, NETWORKS, ASSETS
-- =============================================================================

-- A provider is an integration we operate: our own node / RPC gateway, a
-- custodial API (Fireblocks, BitGo, Coinbase Prime), or an exchange rail
-- (Binance Pay, Coinbase Commerce). Secrets live in the vault under credentials_ref.
CREATE TABLE payment_providers (
    id              SMALLSERIAL   PRIMARY KEY,
    code            TEXT          NOT NULL UNIQUE,            -- 'tron_rpc', 'evm_rpc', 'fireblocks', 'binance_pay'
    name            TEXT          NOT NULL,
    kind            provider_kind NOT NULL,
    adapter         TEXT          NOT NULL,                   -- code module implementing the integration
    config          JSONB         NOT NULL DEFAULT '{}'::jsonb,   -- non-secret: endpoints, rate limits
    credentials_ref TEXT,                                     -- vault path for API keys / signing keys
    is_enabled      BOOLEAN       NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_payment_providers_updated BEFORE UPDATE ON payment_providers FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE networks (
    id                     SMALLSERIAL    PRIMARY KEY,
    code                   TEXT           NOT NULL UNIQUE,   -- 'tron', 'ethereum', 'bsc', 'polygon', 'bitcoin', 'solana'
    name                   TEXT           NOT NULL,
    family                 network_family NOT NULL,
    chain_id               BIGINT,                           -- EVM chain id; NULL elsewhere
    native_symbol          TEXT           NOT NULL,
    native_decimals        SMALLINT       NOT NULL,
    required_confirmations INT            NOT NULL,
    avg_block_time_ms      INT            NOT NULL,
    address_regex          TEXT,                             -- validated by trigger on address columns
    tx_hash_regex          TEXT,
    supports_memo          BOOLEAN        NOT NULL DEFAULT FALSE,  -- destination tag / memo (TON, XRP, ...)
    explorer_tx_url        TEXT,                             -- '.../tx/{hash}'
    explorer_address_url   TEXT,
    is_testnet             BOOLEAN        NOT NULL DEFAULT FALSE,
    is_enabled             BOOLEAN        NOT NULL DEFAULT TRUE
);

-- Which provider serves which network, and what it can do there.
-- `priority` drives routing when several providers serve the same network.
CREATE TABLE provider_networks (
    provider_id                 SMALLINT    NOT NULL REFERENCES payment_providers(id) ON DELETE CASCADE,
    network_id                  SMALLINT    NOT NULL REFERENCES networks(id),
    supports_deposits           BOOLEAN     NOT NULL DEFAULT TRUE,
    supports_withdrawals        BOOLEAN     NOT NULL DEFAULT TRUE,
    supports_address_generation BOOLEAN     NOT NULL DEFAULT TRUE,
    supports_fee_sponsorship    BOOLEAN     NOT NULL DEFAULT FALSE,   -- e.g. Tron energy delegation, gasless
    priority                    SMALLINT    NOT NULL DEFAULT 100,
    is_enabled                  BOOLEAN     NOT NULL DEFAULT TRUE,
    PRIMARY KEY (provider_id, network_id)
);

CREATE TABLE assets (
    id               SMALLSERIAL   PRIMARY KEY,
    network_id       SMALLINT      NOT NULL REFERENCES networks(id),
    code             TEXT          NOT NULL UNIQUE,          -- 'USDT_TRON', 'USDT_ETH', 'ETH', 'BTC'
    symbol           TEXT          NOT NULL,                 -- 'USDT'
    name             TEXT          NOT NULL,
    kind             asset_kind    NOT NULL,
    token_standard   TEXT,                                   -- 'trc20', 'erc20', 'bep20', 'spl', 'jetton'
    contract_address TEXT,                                   -- NULL for native
    decimals         SMALLINT      NOT NULL,
    is_stablecoin    BOOLEAN       NOT NULL DEFAULT FALSE,
    logo_url         TEXT,
    min_deposit      crypto_amount NOT NULL DEFAULT 0,
    min_withdrawal   crypto_amount NOT NULL DEFAULT 0,
    is_enabled       BOOLEAN       NOT NULL DEFAULT TRUE,
    CONSTRAINT assets_native_shape CHECK (
        (kind = 'native' AND contract_address IS NULL AND token_standard IS NULL) OR
        (kind = 'token'  AND contract_address IS NOT NULL AND token_standard IS NOT NULL)
    ),
    UNIQUE (network_id, contract_address)
);
CREATE UNIQUE INDEX assets_one_native_per_network ON assets(network_id) WHERE kind = 'native';

-- Provider-specific view of an asset (custodial APIs use their own ids).
CREATE TABLE provider_assets (
    provider_id       SMALLINT NOT NULL REFERENCES payment_providers(id) ON DELETE CASCADE,
    asset_id          SMALLINT NOT NULL REFERENCES assets(id),
    external_asset_id TEXT,                                  -- e.g. Fireblocks 'USDT_TRON', Binance 'USDT'
    is_enabled        BOOLEAN  NOT NULL DEFAULT TRUE,
    PRIMARY KEY (provider_id, asset_id)
);

-- Scanner / poller progress per provider+network.
CREATE TABLE chain_cursors (
    provider_id        SMALLINT    NOT NULL,
    network_id         SMALLINT    NOT NULL,
    last_scanned_block BIGINT      NOT NULL DEFAULT 0,
    last_scanned_hash  TEXT,
    external_cursor    TEXT,                                 -- opaque cursor for API-based providers
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_id, network_id),
    FOREIGN KEY (provider_id, network_id) REFERENCES provider_networks(provider_id, network_id) ON DELETE CASCADE
);

-- Rate snapshots used to price invoices (asset → fiat or asset → asset).
CREATE TABLE exchange_rates (
    id            BIGSERIAL       PRIMARY KEY,
    base_asset_id SMALLINT        NOT NULL REFERENCES assets(id),
    quote         TEXT            NOT NULL,                  -- 'USD', 'EUR' or an asset code
    rate          NUMERIC(38, 18) NOT NULL CHECK (rate > 0),
    source        TEXT            NOT NULL,                  -- 'coingecko', 'binance', 'manual'
    fetched_at    TIMESTAMPTZ     NOT NULL DEFAULT now()
);
CREATE INDEX exchange_rates_lookup_idx ON exchange_rates(base_asset_id, quote, fetched_at DESC);

-- Which assets a merchant accepts (and optional per-merchant provider pin).
CREATE TABLE organization_assets (
    organization_id      UUID     NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    asset_id             SMALLINT NOT NULL REFERENCES assets(id),
    is_enabled           BOOLEAN  NOT NULL DEFAULT TRUE,
    preferred_provider_id SMALLINT REFERENCES payment_providers(id),
    auto_withdraw_to     TEXT,                               -- optional auto-settlement address
    PRIMARY KEY (organization_id, asset_id)
);

-- ---- Address / hash validation -----------------------------------------------
CREATE OR REPLACE FUNCTION assert_valid_address(p_network_id SMALLINT, p_address TEXT) RETURNS VOID
LANGUAGE plpgsql STABLE AS $$
DECLARE v_regex TEXT; v_code TEXT;
BEGIN
    SELECT address_regex, code INTO v_regex, v_code FROM networks WHERE id = p_network_id;
    IF v_code IS NULL THEN
        RAISE EXCEPTION 'unknown network id %', p_network_id USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF v_regex IS NOT NULL AND p_address !~ v_regex THEN
        RAISE EXCEPTION 'address "%" is not valid for network %', p_address, v_code USING ERRCODE = 'check_violation';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION assert_valid_tx_hash(p_network_id SMALLINT, p_hash TEXT) RETURNS VOID
LANGUAGE plpgsql STABLE AS $$
DECLARE v_regex TEXT; v_code TEXT;
BEGIN
    SELECT tx_hash_regex, code INTO v_regex, v_code FROM networks WHERE id = p_network_id;
    IF v_regex IS NOT NULL AND p_hash !~ v_regex THEN
        RAISE EXCEPTION 'tx hash "%" is not valid for network %', p_hash, v_code USING ERRCODE = 'check_violation';
    END IF;
END $$;

-- =============================================================================
-- 3. WALLETS & ADDRESSES
-- =============================================================================

-- A wallet is a key root (own HD wallet) or a vault handle at a custodial provider.
CREATE TABLE wallets (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id        SMALLINT    NOT NULL REFERENCES payment_providers(id),
    network_id         SMALLINT    NOT NULL REFERENCES networks(id),
    kind               wallet_kind NOT NULL,
    name               TEXT        NOT NULL,
    key_ref            TEXT,                                 -- KMS/HSM ref for self-custody
    xpub               TEXT,
    derivation_path    TEXT,                                 -- "m/44'/195'/0'/0" (tron), "m/44'/60'/0'/0" (evm)
    next_index         BIGINT      NOT NULL DEFAULT 0,
    external_wallet_id TEXT,                                 -- vault / account id at custodial provider
    is_active          BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (provider_id, network_id) REFERENCES provider_networks(provider_id, network_id),
    CONSTRAINT wallets_custody_shape CHECK (key_ref IS NOT NULL OR external_wallet_id IS NOT NULL)
);
CREATE TRIGGER trg_wallets_updated BEFORE UPDATE ON wallets FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE addresses (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    network_id          SMALLINT    NOT NULL REFERENCES networks(id),
    provider_id         SMALLINT    NOT NULL REFERENCES payment_providers(id),
    wallet_id           UUID        NOT NULL REFERENCES wallets(id),
    organization_id     UUID        REFERENCES organizations(id),
    address             TEXT        NOT NULL,                -- canonical form (EIP-55 / base58 / bech32)
    memo                TEXT,                                -- destination tag for memo networks
    kind                wallet_kind NOT NULL,
    derivation_index    BIGINT,
    external_address_id TEXT,                                -- id at custodial provider
    is_active           BOOLEAN     NOT NULL DEFAULT TRUE,
    last_used_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (network_id, address, memo),
    UNIQUE (wallet_id, derivation_index),
    CONSTRAINT addresses_deposit_has_org CHECK (kind <> 'deposit' OR organization_id IS NOT NULL)
);
CREATE INDEX addresses_org_idx    ON addresses(organization_id) WHERE organization_id IS NOT NULL;
CREATE INDEX addresses_lookup_idx ON addresses(network_id, address);

CREATE OR REPLACE FUNCTION addresses_validate() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM assert_valid_address(NEW.network_id, NEW.address);
    RETURN NEW;
END $$;
CREATE TRIGGER trg_addresses_validate BEFORE INSERT OR UPDATE OF address, network_id ON addresses
    FOR EACH ROW EXECUTE FUNCTION addresses_validate();

CREATE TABLE address_balances (
    address_id  UUID          NOT NULL REFERENCES addresses(id) ON DELETE CASCADE,
    asset_id    SMALLINT      NOT NULL REFERENCES assets(id),
    balance     crypto_amount NOT NULL DEFAULT 0,
    as_of_block BIGINT        NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    PRIMARY KEY (address_id, asset_id)
);

-- =============================================================================
-- 4. ON-CHAIN OBSERVATIONS
-- =============================================================================

CREATE TABLE transactions (
    id              UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    network_id      SMALLINT      NOT NULL REFERENCES networks(id),
    provider_id     SMALLINT      NOT NULL REFERENCES payment_providers(id),   -- observed / broadcast through
    hash            TEXT          NOT NULL,
    block_number    BIGINT,
    block_hash      TEXT,
    block_timestamp TIMESTAMPTZ,
    from_address    TEXT,
    to_address      TEXT,
    status          tx_status     NOT NULL DEFAULT 'pending',
    confirmations   INT           NOT NULL DEFAULT 0,
    fee_native      crypto_amount,                            -- gas / energy / sats paid in native coin
    fee_details     JSONB,                                    -- gasUsed, energy, bandwidth, vsize ...
    external_tx_id  TEXT,                                     -- id at custodial provider
    raw             JSONB,
    first_seen_at   TIMESTAMPTZ   NOT NULL DEFAULT now(),
    confirmed_at    TIMESTAMPTZ,
    UNIQUE (network_id, hash)
);
CREATE INDEX transactions_block_idx   ON transactions(network_id, block_number);
CREATE INDEX transactions_pending_idx ON transactions(status) WHERE status = 'pending';

CREATE OR REPLACE FUNCTION transactions_validate() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM assert_valid_tx_hash(NEW.network_id, NEW.hash);
    RETURN NEW;
END $$;
CREATE TRIGGER trg_transactions_validate BEFORE INSERT OR UPDATE OF hash ON transactions
    FOR EACH ROW EXECUTE FUNCTION transactions_validate();

-- One row per value movement (native transfer, token Transfer event, UTXO output).
CREATE TABLE transfers (
    id             UUID               PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id UUID               NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    network_id     SMALLINT           NOT NULL REFERENCES networks(id),
    asset_id       SMALLINT           NOT NULL REFERENCES assets(id),
    log_index      INT                NOT NULL DEFAULT 0,      -- event index / vout
    from_address   TEXT,                                       -- NULL for coinbase / mint
    to_address     TEXT               NOT NULL,
    to_memo        TEXT,
    amount         crypto_amount      NOT NULL CHECK (amount > 0),
    direction      transfer_direction NOT NULL,
    address_id     UUID               REFERENCES addresses(id),
    created_at     TIMESTAMPTZ        NOT NULL DEFAULT now(),
    UNIQUE (transaction_id, log_index)
);
CREATE INDEX transfers_address_idx ON transfers(address_id, created_at DESC);
CREATE INDEX transfers_to_idx      ON transfers(network_id, to_address);

-- =============================================================================
-- 5. MERCHANT PAYMENTS (inbound)
-- =============================================================================

-- An invoice is priced once (usually in fiat) and can be paid with any of the
-- merchant's accepted assets. Each acceptable way to pay is a payment option
-- with its own amount, rate and deposit address; the payer picks one.
CREATE TABLE invoices (
    id                 UUID           PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    UUID           NOT NULL REFERENCES organizations(id),
    external_id        TEXT,
    price_currency     TEXT           NOT NULL,             -- 'USD', 'EUR' or an asset code
    price_amount       NUMERIC(38, 18) NOT NULL CHECK (price_amount > 0),
    status             invoice_status NOT NULL DEFAULT 'new',
    description        TEXT,
    customer_email     CITEXT,
    callback_url       TEXT,
    return_url         TEXT,
    metadata           JSONB          NOT NULL DEFAULT '{}'::jsonb,
    expires_at         TIMESTAMPTZ    NOT NULL,
    paid_at            TIMESTAMPTZ,
    confirmed_at       TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    created_by_api_key UUID           REFERENCES api_keys(id),
    created_at         TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ    NOT NULL DEFAULT now(),
    UNIQUE (organization_id, external_id)
);
CREATE TRIGGER trg_invoices_updated BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX invoices_org_created_idx ON invoices(organization_id, created_at DESC);
CREATE INDEX invoices_expiry_idx      ON invoices(expires_at) WHERE status IN ('new', 'partial');

CREATE TABLE invoice_payment_options (
    id               UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id       UUID            NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    asset_id         SMALLINT        NOT NULL REFERENCES assets(id),
    provider_id      SMALLINT        NOT NULL REFERENCES payment_providers(id),
    address_id       UUID            REFERENCES addresses(id),       -- allocated when option is selected
    memo             TEXT,
    amount_due       crypto_amount   NOT NULL CHECK (amount_due > 0),
    amount_paid      crypto_amount   NOT NULL DEFAULT 0,
    exchange_rate    NUMERIC(38, 18),                                 -- price_currency per 1 asset
    rate_id          BIGINT          REFERENCES exchange_rates(id),
    rate_locked_until TIMESTAMPTZ,
    is_selected      BOOLEAN         NOT NULL DEFAULT FALSE,
    selected_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ     NOT NULL DEFAULT now(),
    UNIQUE (invoice_id, asset_id)
);
CREATE UNIQUE INDEX invoice_payment_options_selected_uq ON invoice_payment_options(invoice_id) WHERE is_selected;
CREATE INDEX invoice_payment_options_address_idx ON invoice_payment_options(address_id) WHERE address_id IS NOT NULL;

-- A payment is one inbound transfer to a merchant deposit address.
-- option_id / invoice_id are NULL for unexpected deposits.
CREATE TABLE payments (
    id              UUID           PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID           NOT NULL REFERENCES organizations(id),
    invoice_id      UUID           REFERENCES invoices(id),
    option_id       UUID           REFERENCES invoice_payment_options(id),
    address_id      UUID           NOT NULL REFERENCES addresses(id),
    asset_id        SMALLINT       NOT NULL REFERENCES assets(id),
    provider_id     SMALLINT       NOT NULL REFERENCES payment_providers(id),
    transfer_id     UUID           NOT NULL UNIQUE REFERENCES transfers(id),
    amount          crypto_amount  NOT NULL CHECK (amount > 0),
    fee_amount      crypto_amount  NOT NULL DEFAULT 0,
    status          payment_status NOT NULL DEFAULT 'detected',
    detected_at     TIMESTAMPTZ    NOT NULL DEFAULT now(),
    confirmed_at    TIMESTAMPTZ,
    credited_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ    NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_payments_updated BEFORE UPDATE ON payments FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX payments_invoice_idx ON payments(invoice_id);
CREATE INDEX payments_org_idx     ON payments(organization_id, created_at DESC);

-- =============================================================================
-- 6. WITHDRAWALS (outbound)
-- =============================================================================

CREATE TABLE withdrawals (
    id                   UUID              PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      UUID              NOT NULL REFERENCES organizations(id),
    external_id          TEXT,
    asset_id             SMALLINT          NOT NULL REFERENCES assets(id),
    provider_id          SMALLINT          REFERENCES payment_providers(id),  -- chosen at routing time
    to_address           TEXT              NOT NULL,
    to_memo              TEXT,
    amount               crypto_amount     NOT NULL CHECK (amount > 0),
    fee_amount           crypto_amount     NOT NULL DEFAULT 0,               -- our fee
    network_fee_native   crypto_amount,                                      -- actual native fee paid
    status               withdrawal_status NOT NULL DEFAULT 'pending_approval',
    from_address_id      UUID              REFERENCES addresses(id),
    transaction_id       UUID              REFERENCES transactions(id),
    external_ref         TEXT,                                               -- provider's withdrawal id
    requested_by         UUID              REFERENCES users(id),
    requested_by_api_key UUID              REFERENCES api_keys(id),
    approved_by          UUID              REFERENCES users(id),
    approved_at          TIMESTAMPTZ,
    failure_reason       TEXT,
    metadata             JSONB             NOT NULL DEFAULT '{}'::jsonb,
    created_at           TIMESTAMPTZ       NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ       NOT NULL DEFAULT now(),
    completed_at         TIMESTAMPTZ,
    UNIQUE (organization_id, external_id)
);
CREATE TRIGGER trg_withdrawals_updated BEFORE UPDATE ON withdrawals FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX withdrawals_org_idx   ON withdrawals(organization_id, created_at DESC);
CREATE INDEX withdrawals_queue_idx ON withdrawals(status, created_at) WHERE status IN ('approved', 'queued', 'broadcast');

CREATE OR REPLACE FUNCTION withdrawals_validate() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE v_network_id SMALLINT;
BEGIN
    SELECT network_id INTO v_network_id FROM assets WHERE id = NEW.asset_id;
    PERFORM assert_valid_address(v_network_id, NEW.to_address);
    RETURN NEW;
END $$;
CREATE TRIGGER trg_withdrawals_validate BEFORE INSERT OR UPDATE OF to_address, asset_id ON withdrawals
    FOR EACH ROW EXECUTE FUNCTION withdrawals_validate();

CREATE TABLE withdrawal_addresses (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    network_id      SMALLINT    NOT NULL REFERENCES networks(id),
    address         TEXT        NOT NULL,
    memo            TEXT,
    label           TEXT        NOT NULL,
    is_whitelisted  BOOLEAN     NOT NULL DEFAULT FALSE,
    created_by      UUID        REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, network_id, address, memo)
);
CREATE TRIGGER trg_withdrawal_addresses_validate BEFORE INSERT OR UPDATE OF address, network_id ON withdrawal_addresses
    FOR EACH ROW EXECUTE FUNCTION addresses_validate();

-- =============================================================================
-- 7. TREASURY OPERATIONS
-- =============================================================================

CREATE TABLE internal_transfers (
    id              UUID                     PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            internal_transfer_kind   NOT NULL,
    asset_id        SMALLINT                 NOT NULL REFERENCES assets(id),
    provider_id     SMALLINT                 NOT NULL REFERENCES payment_providers(id),
    from_address_id UUID                     NOT NULL REFERENCES addresses(id),
    to_address_id   UUID                     NOT NULL REFERENCES addresses(id),
    amount          crypto_amount            NOT NULL CHECK (amount > 0),
    status          internal_transfer_status NOT NULL DEFAULT 'queued',
    transaction_id  UUID                     REFERENCES transactions(id),
    external_ref    TEXT,
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ              NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ              NOT NULL DEFAULT now(),
    CONSTRAINT internal_transfers_distinct CHECK (from_address_id <> to_address_id)
);
CREATE TRIGGER trg_internal_transfers_updated BEFORE UPDATE ON internal_transfers FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX internal_transfers_queue_idx ON internal_transfers(status, created_at) WHERE status IN ('queued', 'broadcast');

-- =============================================================================
-- 8. FEES
-- =============================================================================

-- Resolution order: (org, asset) → (org, NULL asset) → (NULL org, asset) → (NULL, NULL).
CREATE TABLE fee_schedules (
    id                   UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      UUID          REFERENCES organizations(id) ON DELETE CASCADE,
    asset_id             SMALLINT      REFERENCES assets(id),
    deposit_fee_bps      INT           NOT NULL DEFAULT 0 CHECK (deposit_fee_bps BETWEEN 0 AND 10000),
    deposit_fee_fixed    crypto_amount NOT NULL DEFAULT 0,
    withdrawal_fee_bps   INT           NOT NULL DEFAULT 0 CHECK (withdrawal_fee_bps BETWEEN 0 AND 10000),
    withdrawal_fee_fixed crypto_amount NOT NULL DEFAULT 0,
    pass_network_fee     BOOLEAN       NOT NULL DEFAULT TRUE,   -- charge merchant the actual network fee on payouts
    effective_from       TIMESTAMPTZ   NOT NULL DEFAULT now(),
    effective_to         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX fee_schedules_active_uq
    ON fee_schedules(COALESCE(organization_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(asset_id, 0))
    WHERE effective_to IS NULL;

-- =============================================================================
-- 9. DOUBLE-ENTRY LEDGER
-- =============================================================================

CREATE TABLE ledger_accounts (
    id              UUID                PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID                REFERENCES organizations(id),
    asset_id        SMALLINT            NOT NULL REFERENCES assets(id),
    type            ledger_account_type NOT NULL,
    created_at      TIMESTAMPTZ         NOT NULL DEFAULT now(),
    CONSTRAINT ledger_accounts_owner CHECK (
        (type::text LIKE 'merchant_%' AND organization_id IS NOT NULL) OR
        (type::text LIKE 'platform_%' AND organization_id IS NULL)
    )
);
CREATE UNIQUE INDEX ledger_accounts_uq
    ON ledger_accounts(COALESCE(organization_id, '00000000-0000-0000-0000-000000000000'::uuid), asset_id, type);

CREATE TABLE ledger_journals (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type     TEXT        NOT NULL,
    reference_type TEXT        NOT NULL,
    reference_id   UUID        NOT NULL,
    description    TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (event_type, reference_type, reference_id)
);

CREATE TABLE ledger_entries (
    id         BIGSERIAL     PRIMARY KEY,
    journal_id UUID          NOT NULL REFERENCES ledger_journals(id) ON DELETE CASCADE,
    account_id UUID          NOT NULL REFERENCES ledger_accounts(id),
    amount     crypto_amount NOT NULL CHECK (amount <> 0),
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX ledger_entries_account_idx ON ledger_entries(account_id, id);
CREATE INDEX ledger_entries_journal_idx ON ledger_entries(journal_id);

CREATE OR REPLACE FUNCTION ledger_journal_must_balance() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE v_sum NUMERIC;
BEGIN
    SELECT COALESCE(SUM(amount), 0) INTO v_sum FROM ledger_entries WHERE journal_id = NEW.journal_id;
    IF v_sum <> 0 THEN
        RAISE EXCEPTION 'ledger journal % is unbalanced (sum = %)', NEW.journal_id, v_sum;
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER trg_ledger_entries_balance
    AFTER INSERT OR UPDATE OR DELETE ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_journal_must_balance();

CREATE TABLE ledger_balances (
    account_id UUID          PRIMARY KEY REFERENCES ledger_accounts(id),
    balance    crypto_amount NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION ledger_apply_entry() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO ledger_balances (account_id, balance, updated_at)
    VALUES (NEW.account_id, NEW.amount, now())
    ON CONFLICT (account_id) DO UPDATE
        SET balance = ledger_balances.balance + EXCLUDED.balance, updated_at = now();
    RETURN NEW;
END $$;
CREATE TRIGGER trg_ledger_entries_apply AFTER INSERT ON ledger_entries FOR EACH ROW EXECUTE FUNCTION ledger_apply_entry();
CREATE TRIGGER trg_ledger_entries_immutable BEFORE UPDATE OR DELETE ON ledger_entries FOR EACH ROW EXECUTE FUNCTION forbid_change();

CREATE VIEW merchant_balances AS
SELECT la.organization_id,
       la.asset_id,
       a.code AS asset_code,
       a.symbol,
       n.code AS network_code,
       SUM(lb.balance) FILTER (WHERE la.type = 'merchant_available') AS available,
       SUM(lb.balance) FILTER (WHERE la.type = 'merchant_pending')   AS pending,
       SUM(lb.balance) FILTER (WHERE la.type = 'merchant_locked')    AS locked
FROM ledger_accounts la
JOIN ledger_balances lb ON lb.account_id = la.id
JOIN assets a           ON a.id = la.asset_id
JOIN networks n         ON n.id = a.network_id
WHERE la.organization_id IS NOT NULL
GROUP BY la.organization_id, la.asset_id, a.code, a.symbol, n.code;

-- =============================================================================
-- 10. WEBHOOKS (outbound to merchants, inbound from providers)
-- =============================================================================

CREATE TABLE webhook_endpoints (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    url             TEXT        NOT NULL,
    secret_enc      BYTEA       NOT NULL,
    event_types     TEXT[]      NOT NULL DEFAULT '{*}',
    description     TEXT,
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_by      UUID        REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_webhook_endpoints_updated BEFORE UPDATE ON webhook_endpoints FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX webhook_endpoints_org_idx ON webhook_endpoints(organization_id) WHERE is_active;

CREATE TABLE events (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    type            TEXT        NOT NULL,
    resource_type   TEXT        NOT NULL,
    resource_id     UUID        NOT NULL,
    payload         JSONB       NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX events_org_created_idx ON events(organization_id, created_at DESC);
CREATE INDEX events_resource_idx    ON events(resource_type, resource_id);

CREATE TABLE webhook_deliveries (
    id               UUID                    PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id      UUID                    NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    event_id         UUID                    NOT NULL REFERENCES events(id)            ON DELETE CASCADE,
    status           webhook_delivery_status NOT NULL DEFAULT 'pending',
    attempts         INT                     NOT NULL DEFAULT 0,
    max_attempts     INT                     NOT NULL DEFAULT 10,
    next_attempt_at  TIMESTAMPTZ             NOT NULL DEFAULT now(),
    last_attempt_at  TIMESTAMPTZ,
    last_status_code SMALLINT,
    last_error       TEXT,
    delivered_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ             NOT NULL DEFAULT now(),
    UNIQUE (endpoint_id, event_id)
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries(next_attempt_at) WHERE status IN ('pending', 'failed');

-- Raw callbacks received from custodial / exchange providers, stored before processing.
CREATE TABLE provider_webhook_events (
    id                BIGSERIAL   PRIMARY KEY,
    provider_id       SMALLINT    NOT NULL REFERENCES payment_providers(id),
    external_event_id TEXT,
    event_type        TEXT,
    payload           JSONB       NOT NULL,
    signature_valid   BOOLEAN,
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at      TIMESTAMPTZ,
    error             TEXT
);
CREATE UNIQUE INDEX provider_webhook_events_dedupe_uq
    ON provider_webhook_events(provider_id, external_event_id) WHERE external_event_id IS NOT NULL;
CREATE INDEX provider_webhook_events_unprocessed_idx ON provider_webhook_events(received_at) WHERE processed_at IS NULL;

-- =============================================================================
-- 11. SEED — providers, mainnets, major assets
--      (contract addresses are the well-known mainnet ones; verify before prod)
-- =============================================================================

INSERT INTO payment_providers (code, name, kind, adapter) VALUES
    ('tron_rpc',    'Tron node / TronGrid',   'node', 'tron'),
    ('evm_rpc',     'EVM JSON-RPC',           'node', 'evm'),
    ('bitcoin_rpc', 'Bitcoin Core / Electrs', 'node', 'bitcoin'),
    ('solana_rpc',  'Solana JSON-RPC',        'node', 'solana');

INSERT INTO networks (code, name, family, chain_id, native_symbol, native_decimals, required_confirmations, avg_block_time_ms,
                      address_regex, tx_hash_regex, explorer_tx_url, explorer_address_url) VALUES
    ('tron',     'Tron',          'tron',    NULL, 'TRX', 6,  19, 3000,
     '^T[1-9A-HJ-NP-Za-km-z]{33}$',                                   '^[0-9a-f]{64}$',
     'https://tronscan.org/#/transaction/{hash}',                     'https://tronscan.org/#/address/{address}'),
    ('ethereum', 'Ethereum',      'evm',     1,    'ETH', 18, 12, 12000,
     '^0x[0-9a-fA-F]{40}$',                                           '^0x[0-9a-f]{64}$',
     'https://etherscan.io/tx/{hash}',                                'https://etherscan.io/address/{address}'),
    ('bsc',      'BNB Smart Chain','evm',    56,   'BNB', 18, 15, 3000,
     '^0x[0-9a-fA-F]{40}$',                                           '^0x[0-9a-f]{64}$',
     'https://bscscan.com/tx/{hash}',                                 'https://bscscan.com/address/{address}'),
    ('polygon',  'Polygon PoS',   'evm',     137,  'POL', 18, 64, 2000,
     '^0x[0-9a-fA-F]{40}$',                                           '^0x[0-9a-f]{64}$',
     'https://polygonscan.com/tx/{hash}',                             'https://polygonscan.com/address/{address}'),
    ('bitcoin',  'Bitcoin',       'bitcoin', NULL, 'BTC', 8,  3,  600000,
     '^(bc1[02-9ac-hj-np-z]{11,87}|[13][a-km-zA-HJ-NP-Z1-9]{25,34})$', '^[0-9a-f]{64}$',
     'https://mempool.space/tx/{hash}',                               'https://mempool.space/address/{address}'),
    ('solana',   'Solana',        'solana',  NULL, 'SOL', 9,  32, 400,
     '^[1-9A-HJ-NP-Za-km-z]{32,44}$',                                 '^[1-9A-HJ-NP-Za-km-z]{64,88}$',
     'https://solscan.io/tx/{hash}',                                  'https://solscan.io/account/{address}');

INSERT INTO provider_networks (provider_id, network_id, supports_fee_sponsorship)
SELECT p.id, n.id, (n.code = 'tron')
FROM payment_providers p
JOIN networks n ON (p.code, n.family) IN (('tron_rpc','tron'), ('evm_rpc','evm'), ('bitcoin_rpc','bitcoin'), ('solana_rpc','solana'));

INSERT INTO assets (network_id, code, symbol, name, kind, token_standard, contract_address, decimals, is_stablecoin, min_deposit, min_withdrawal)
SELECT n.id, v.code, v.symbol, v.name, v.kind::asset_kind, v.std, v.contract, v.decimals, v.stable, v.min_dep, v.min_wd
FROM (VALUES
    -- Tron
    ('tron',     'TRX',          'TRX',  'Tron',                 'native', NULL,    NULL,                                           6,  FALSE, 1,       1),
    ('tron',     'USDT_TRON',    'USDT', 'Tether USD (TRC20)',   'token',  'trc20', 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t',           6,  TRUE,  1,       10),
    ('tron',     'USDC_TRON',    'USDC', 'USD Coin (TRC20)',     'token',  'trc20', 'TEkxiTehnzSmSe2XqrBj4w32RUN966rdz8',           6,  TRUE,  1,       10),
    -- Ethereum
    ('ethereum', 'ETH',          'ETH',  'Ether',                'native', NULL,    NULL,                                           18, FALSE, 0.001,   0.005),
    ('ethereum', 'USDT_ETH',     'USDT', 'Tether USD (ERC20)',   'token',  'erc20', '0xdAC17F958D2ee523a2206206994597C13D831ec7',   6,  TRUE,  5,       20),
    ('ethereum', 'USDC_ETH',     'USDC', 'USD Coin (ERC20)',     'token',  'erc20', '0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48',   6,  TRUE,  5,       20),
    -- BNB Smart Chain
    ('bsc',      'BNB',          'BNB',  'BNB',                  'native', NULL,    NULL,                                           18, FALSE, 0.001,   0.005),
    ('bsc',      'USDT_BSC',     'USDT', 'Tether USD (BEP20)',   'token',  'bep20', '0x55d398326f99059fF775485246999027B3197955',   18, TRUE,  1,       10),
    -- Polygon
    ('polygon',  'POL',          'POL',  'Polygon Ecosystem Token','native', NULL,  NULL,                                           18, FALSE, 1,       1),
    ('polygon',  'USDT_POLYGON', 'USDT', 'Tether USD (Polygon)', 'token',  'erc20', '0xc2132D05D31c914a87C6611C10748AEb04B58e8F',   6,  TRUE,  1,       10),
    -- Bitcoin
    ('bitcoin',  'BTC',          'BTC',  'Bitcoin',              'native', NULL,    NULL,                                           8,  FALSE, 0.0001,  0.0005),
    -- Solana
    ('solana',   'SOL',          'SOL',  'Solana',               'native', NULL,    NULL,                                           9,  FALSE, 0.01,    0.01),
    ('solana',   'USDC_SOL',     'USDC', 'USD Coin (SPL)',       'token',  'spl',   'EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v', 6,  TRUE,  1,       10),
    ('solana',   'USDT_SOL',     'USDT', 'Tether USD (SPL)',     'token',  'spl',   'Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB', 6,  TRUE,  1,       10)
) AS v(network, code, symbol, name, kind, std, contract, decimals, stable, min_dep, min_wd)
JOIN networks n ON n.code = v.network;

INSERT INTO provider_assets (provider_id, asset_id)
SELECT pn.provider_id, a.id FROM assets a JOIN provider_networks pn ON pn.network_id = a.network_id;

INSERT INTO chain_cursors (provider_id, network_id) SELECT provider_id, network_id FROM provider_networks;

INSERT INTO ledger_accounts (organization_id, asset_id, type)
SELECT NULL, a.id, t
FROM assets a
CROSS JOIN unnest(ARRAY['platform_fee_revenue', 'platform_network_fees', 'platform_hot_wallet', 'platform_cold_wallet']::ledger_account_type[]) AS t;

-- Platform-wide default fee: 1% on deposits, 0.5% + network fee on withdrawals.
INSERT INTO fee_schedules (organization_id, asset_id, deposit_fee_bps, withdrawal_fee_bps, pass_network_fee)
VALUES (NULL, NULL, 100, 50, TRUE);
