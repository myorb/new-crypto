-- +goose Up
-- =============================================================================
--  Baseline 3/9 · payment providers, networks, assets
--
--  payment_providers  = how we talk to a rail (own node / RPC, custodial API
--                       such as Fireblocks, exchange rail such as Binance Pay)
--  networks           = the chain itself (tron, ethereum, bsc, bitcoin, ...)
--  provider_networks  = which provider serves which network, with capabilities
--  assets             = native coin or token on one network
--
--  These are reference tables: rows are managed by migrations / operators.
--  The app may only read them and flip is_enabled (column-level grant), so a
--  compromised app cannot repoint RPC endpoints or loosen address regexes.
-- =============================================================================

CREATE TABLE payment_providers (
    id              SMALLINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code            TEXT          NOT NULL UNIQUE,            -- 'tron_rpc', 'evm_rpc', 'fireblocks', 'binance_pay'
    name            TEXT          NOT NULL,
    kind            provider_kind NOT NULL,
    adapter         TEXT          NOT NULL,                   -- code module implementing the integration
    config          JSONB         NOT NULL DEFAULT '{}'::jsonb,   -- non-secret: endpoints, rate limits
    credentials_ref TEXT,                                     -- vault path for API keys / signing keys, never the secret
    is_enabled      BOOLEAN       NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    CONSTRAINT payment_providers_code_format CHECK (code ~ '^[a-z0-9_]{2,32}$')
);
CREATE TRIGGER trg_payment_providers_updated BEFORE UPDATE ON payment_providers FOR EACH ROW EXECUTE FUNCTION set_updated_at();
GRANT SELECT ON payment_providers TO templ_app;
GRANT UPDATE (is_enabled) ON payment_providers TO templ_app;

CREATE TABLE networks (
    id                     SMALLINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code                   TEXT           NOT NULL UNIQUE,   -- 'tron', 'ethereum', 'bsc', 'polygon', 'bitcoin', 'solana'
    name                   TEXT           NOT NULL,
    family                 network_family NOT NULL,
    chain_id               BIGINT,                           -- EVM chain id; NULL elsewhere
    native_symbol          TEXT           NOT NULL,
    native_decimals        SMALLINT       NOT NULL,
    required_confirmations INT            NOT NULL,
    avg_block_time_ms      INT            NOT NULL,
    address_regex          TEXT,                             -- enforced by triggers on every address column
    tx_hash_regex          TEXT,
    supports_memo          BOOLEAN        NOT NULL DEFAULT FALSE,  -- destination tag / memo (TON, XRP, ...)
    explorer_tx_url        TEXT,                             -- '.../tx/{hash}'
    explorer_address_url   TEXT,
    is_testnet             BOOLEAN        NOT NULL DEFAULT FALSE,
    is_enabled             BOOLEAN        NOT NULL DEFAULT TRUE,
    CONSTRAINT networks_code_format   CHECK (code ~ '^[a-z0-9_]{2,32}$'),
    CONSTRAINT networks_decimals      CHECK (native_decimals BETWEEN 0 AND 18),   -- crypto_amount carries 18 fractional digits
    CONSTRAINT networks_confirmations CHECK (required_confirmations >= 0),
    CONSTRAINT networks_block_time    CHECK (avg_block_time_ms > 0)
);
GRANT SELECT ON networks TO templ_app;
GRANT UPDATE (is_enabled) ON networks TO templ_app;

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
GRANT SELECT ON provider_networks TO templ_app;
GRANT UPDATE (is_enabled, priority) ON provider_networks TO templ_app;

CREATE TABLE assets (
    id               SMALLINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
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
    CONSTRAINT assets_code_format  CHECK (code ~ '^[A-Z0-9_]{2,32}$'),
    CONSTRAINT assets_decimals     CHECK (decimals BETWEEN 0 AND 18),
    CONSTRAINT assets_minimums     CHECK (min_deposit >= 0 AND min_withdrawal >= 0),
    CONSTRAINT assets_native_shape CHECK (
        (kind = 'native' AND contract_address IS NULL AND token_standard IS NULL) OR
        (kind = 'token'  AND contract_address IS NOT NULL AND token_standard IS NOT NULL)
    ),
    UNIQUE (network_id, contract_address)
);
CREATE UNIQUE INDEX assets_one_native_per_network ON assets(network_id) WHERE kind = 'native';
GRANT SELECT ON assets TO templ_app;
GRANT UPDATE (is_enabled) ON assets TO templ_app;

-- Provider-specific view of an asset (custodial APIs use their own ids).
CREATE TABLE provider_assets (
    provider_id       SMALLINT NOT NULL REFERENCES payment_providers(id) ON DELETE CASCADE,
    asset_id          SMALLINT NOT NULL REFERENCES assets(id),
    external_asset_id TEXT,                                  -- e.g. Fireblocks 'USDT_TRON', Binance 'USDT'
    is_enabled        BOOLEAN  NOT NULL DEFAULT TRUE,
    PRIMARY KEY (provider_id, asset_id)
);
GRANT SELECT ON provider_assets TO templ_app;
GRANT UPDATE (is_enabled) ON provider_assets TO templ_app;

-- Scanner / poller progress per provider+network.
CREATE TABLE chain_cursors (
    provider_id        SMALLINT    NOT NULL,
    network_id         SMALLINT    NOT NULL,
    last_scanned_block BIGINT      NOT NULL DEFAULT 0,
    last_scanned_hash  TEXT,
    external_cursor    TEXT,                                 -- opaque cursor for API-based providers
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_id, network_id),
    FOREIGN KEY (provider_id, network_id) REFERENCES provider_networks(provider_id, network_id) ON DELETE CASCADE,
    CONSTRAINT chain_cursors_block CHECK (last_scanned_block >= 0)
);
GRANT SELECT, INSERT, UPDATE ON chain_cursors TO templ_app;

-- Rate snapshots used to price invoices (asset → fiat or asset → asset).
CREATE TABLE exchange_rates (
    id            BIGINT          GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    base_asset_id SMALLINT        NOT NULL REFERENCES assets(id),
    quote         TEXT            NOT NULL,                  -- 'USD', 'EUR' or an asset code
    rate          NUMERIC(38, 18) NOT NULL CHECK (rate > 0),
    source        TEXT            NOT NULL,                  -- 'coingecko', 'binance', 'manual'
    fetched_at    TIMESTAMPTZ     NOT NULL DEFAULT now(),
    CONSTRAINT exchange_rates_quote_format CHECK (quote ~ '^[A-Z][A-Z0-9_]{1,31}$')
);
CREATE INDEX exchange_rates_lookup_idx   ON exchange_rates(base_asset_id, quote, fetched_at DESC);
CREATE INDEX exchange_rates_fetched_brin ON exchange_rates USING BRIN (fetched_at);   -- retention
GRANT SELECT, INSERT ON exchange_rates TO templ_app;

-- Which assets a merchant accepts (and optional per-merchant provider pin).
CREATE TABLE organization_assets (
    organization_id       UUID     NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    asset_id              SMALLINT NOT NULL REFERENCES assets(id),
    is_enabled            BOOLEAN  NOT NULL DEFAULT TRUE,
    preferred_provider_id SMALLINT REFERENCES payment_providers(id),
    auto_withdraw_to      TEXT,                              -- optional auto-settlement address (validated by the app)
    PRIMARY KEY (organization_id, asset_id)
);
GRANT SELECT, INSERT, UPDATE, DELETE ON organization_assets TO templ_app;

-- ---- Address / hash validation -----------------------------------------------
-- Called from BEFORE triggers on every column that stores an address. Reads the
-- rules from `networks`, so it runs as the caller (SECURITY INVOKER); the app
-- role has SELECT on networks.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assert_valid_address(p_network_id SMALLINT, p_address TEXT, p_memo TEXT DEFAULT NULL) RETURNS VOID
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_regex         TEXT;
    v_code          TEXT;
    v_supports_memo BOOLEAN;
BEGIN
    SELECT address_regex, code, supports_memo
      INTO v_regex, v_code, v_supports_memo
      FROM networks WHERE id = p_network_id;
    IF v_code IS NULL THEN
        RAISE EXCEPTION 'unknown network id %', p_network_id USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF v_regex IS NOT NULL AND p_address !~ v_regex THEN
        RAISE EXCEPTION 'address "%" is not valid for network %', p_address, v_code USING ERRCODE = 'check_violation';
    END IF;
    IF p_memo IS NOT NULL AND NOT v_supports_memo THEN
        RAISE EXCEPTION 'network % does not support memos / destination tags', v_code USING ERRCODE = 'check_violation';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assert_valid_tx_hash(p_network_id SMALLINT, p_hash TEXT) RETURNS VOID
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_regex TEXT;
    v_code  TEXT;
BEGIN
    SELECT tx_hash_regex, code INTO v_regex, v_code FROM networks WHERE id = p_network_id;
    IF v_code IS NULL THEN
        RAISE EXCEPTION 'unknown network id %', p_network_id USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF v_regex IS NOT NULL AND p_hash !~ v_regex THEN
        RAISE EXCEPTION 'tx hash "%" is not valid for network %', p_hash, v_code USING ERRCODE = 'check_violation';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION assert_valid_tx_hash(SMALLINT, TEXT);
DROP FUNCTION assert_valid_address(SMALLINT, TEXT, TEXT);
DROP TABLE organization_assets;
DROP TABLE exchange_rates;
DROP TABLE chain_cursors;
DROP TABLE provider_assets;
DROP TABLE assets;
DROP TABLE provider_networks;
DROP TABLE networks;
DROP TABLE payment_providers;
