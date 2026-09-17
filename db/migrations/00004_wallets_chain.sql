-- +goose Up
-- =============================================================================
--  Baseline 4/9 · wallets, addresses, on-chain observations
-- =============================================================================

-- A wallet is a key root (own HD wallet) or a vault handle at a custodial provider.
-- key_ref / xpub are references into KMS / HSM - private keys never touch the DB.
CREATE TABLE wallets (
    id                 UUID        PRIMARY KEY DEFAULT uuidv7(),
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
    CONSTRAINT wallets_custody_shape CHECK (key_ref IS NOT NULL OR external_wallet_id IS NOT NULL),
    CONSTRAINT wallets_next_index    CHECK (next_index >= 0)
);
CREATE TRIGGER trg_wallets_updated BEFORE UPDATE ON wallets FOR EACH ROW EXECUTE FUNCTION set_updated_at();
GRANT SELECT, INSERT, UPDATE ON wallets TO templ_app;

CREATE TABLE addresses (
    id                  UUID        PRIMARY KEY DEFAULT uuidv7(),
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
    -- NULLS NOT DISTINCT: the same address with no memo must be one row, otherwise
    -- one deposit could be credited twice.
    UNIQUE NULLS NOT DISTINCT (network_id, address, memo),
    UNIQUE (wallet_id, derivation_index),
    CONSTRAINT addresses_memo_not_empty   CHECK (memo IS NULL OR memo <> ''),
    CONSTRAINT addresses_deposit_has_org  CHECK (kind <> 'deposit' OR organization_id IS NOT NULL),
    CONSTRAINT addresses_derivation_index CHECK (derivation_index IS NULL OR derivation_index >= 0)
);
CREATE INDEX addresses_org_idx    ON addresses(organization_id) WHERE organization_id IS NOT NULL;
CREATE INDEX addresses_lookup_idx ON addresses(network_id, address);
GRANT SELECT, INSERT, UPDATE ON addresses TO templ_app;

-- Shared by addresses and withdrawal_addresses (both have network_id, address, memo).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION addresses_validate() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM assert_valid_address(NEW.network_id, NEW.address, NEW.memo);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_addresses_validate BEFORE INSERT OR UPDATE OF address, network_id, memo ON addresses
    FOR EACH ROW EXECUTE FUNCTION addresses_validate();

-- Cache of on-chain balances per address/asset (source of truth is the chain).
CREATE TABLE address_balances (
    address_id  UUID          NOT NULL REFERENCES addresses(id) ON DELETE CASCADE,
    asset_id    SMALLINT      NOT NULL REFERENCES assets(id),
    balance     crypto_amount NOT NULL DEFAULT 0,
    as_of_block BIGINT        NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    PRIMARY KEY (address_id, asset_id)
);
GRANT SELECT, INSERT, UPDATE, DELETE ON address_balances TO templ_app;

-- -----------------------------------------------------------------------------
-- On-chain observations
-- -----------------------------------------------------------------------------
CREATE TABLE transactions (
    id              UUID          PRIMARY KEY DEFAULT uuidv7(),
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
    UNIQUE (network_id, hash),
    CONSTRAINT transactions_confirmations CHECK (confirmations >= 0),
    CONSTRAINT transactions_fee           CHECK (fee_native IS NULL OR fee_native >= 0),
    CONSTRAINT transactions_block         CHECK (block_number IS NULL OR block_number >= 0)
);
CREATE INDEX transactions_block_idx   ON transactions(network_id, block_number);
CREATE INDEX transactions_pending_idx ON transactions(status) WHERE status = 'pending';
GRANT SELECT, INSERT, UPDATE ON transactions TO templ_app;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION transactions_validate() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM assert_valid_tx_hash(NEW.network_id, NEW.hash);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_transactions_validate BEFORE INSERT OR UPDATE OF hash, network_id ON transactions
    FOR EACH ROW EXECUTE FUNCTION transactions_validate();

-- One row per value movement (native transfer, token Transfer event, UTXO output).
CREATE TABLE transfers (
    id             UUID               PRIMARY KEY DEFAULT uuidv7(),
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
    UNIQUE (transaction_id, log_index),
    CONSTRAINT transfers_log_index CHECK (log_index >= 0)
);
CREATE INDEX transfers_address_idx ON transfers(address_id, created_at DESC);
CREATE INDEX transfers_to_idx      ON transfers(network_id, to_address);
GRANT SELECT, INSERT, UPDATE ON transfers TO templ_app;

-- +goose Down
DROP TABLE transfers;
DROP TABLE transactions;
DROP FUNCTION transactions_validate();
DROP TABLE address_balances;
DROP TABLE addresses;
DROP FUNCTION addresses_validate();
DROP TABLE wallets;
