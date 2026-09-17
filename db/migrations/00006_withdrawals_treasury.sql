-- +goose Up
-- =============================================================================
--  Baseline 6/9 · withdrawals (outbound) and treasury operations
-- =============================================================================

CREATE TABLE withdrawals (
    id                   UUID              PRIMARY KEY DEFAULT uuidv7(),
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
    UNIQUE (organization_id, external_id),
    CONSTRAINT withdrawals_fee            CHECK (fee_amount >= 0),
    CONSTRAINT withdrawals_network_fee    CHECK (network_fee_native IS NULL OR network_fee_native >= 0),
    CONSTRAINT withdrawals_memo_not_empty CHECK (to_memo IS NULL OR to_memo <> ''),
    -- a named approver always comes with a timestamp; a timestamp alone is a policy auto-approval
    CONSTRAINT withdrawals_approval       CHECK (approved_by IS NULL OR approved_at IS NOT NULL)
);
CREATE TRIGGER trg_withdrawals_updated BEFORE UPDATE ON withdrawals FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX withdrawals_org_idx   ON withdrawals(organization_id, created_at DESC);
CREATE INDEX withdrawals_queue_idx ON withdrawals(status, created_at) WHERE status IN ('approved', 'queued', 'broadcast');
GRANT SELECT, INSERT, UPDATE ON withdrawals TO templ_app;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION withdrawals_validate() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE
    v_network_id SMALLINT;
BEGIN
    SELECT network_id INTO v_network_id FROM assets WHERE id = NEW.asset_id;
    IF v_network_id IS NULL THEN
        RAISE EXCEPTION 'unknown asset id %', NEW.asset_id USING ERRCODE = 'foreign_key_violation';
    END IF;
    PERFORM assert_valid_address(v_network_id, NEW.to_address, NEW.to_memo);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_withdrawals_validate BEFORE INSERT OR UPDATE OF to_address, to_memo, asset_id ON withdrawals
    FOR EACH ROW EXECUTE FUNCTION withdrawals_validate();

-- Merchant address book / allowlist for payouts.
CREATE TABLE withdrawal_addresses (
    id              UUID        PRIMARY KEY DEFAULT uuidv7(),
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    network_id      SMALLINT    NOT NULL REFERENCES networks(id),
    address         TEXT        NOT NULL,
    memo            TEXT,
    label           TEXT        NOT NULL,
    is_whitelisted  BOOLEAN     NOT NULL DEFAULT FALSE,
    created_by      UUID        REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (organization_id, network_id, address, memo),
    CONSTRAINT withdrawal_addresses_memo_not_empty CHECK (memo IS NULL OR memo <> '')
);
CREATE TRIGGER trg_withdrawal_addresses_validate BEFORE INSERT OR UPDATE OF address, network_id, memo ON withdrawal_addresses
    FOR EACH ROW EXECUTE FUNCTION addresses_validate();
GRANT SELECT, INSERT, UPDATE, DELETE ON withdrawal_addresses TO templ_app;

-- -----------------------------------------------------------------------------
-- Treasury operations: sweeps deposit → hot, fee top-ups, hot ↔ cold rebalance
-- -----------------------------------------------------------------------------
CREATE TABLE internal_transfers (
    id              UUID                     PRIMARY KEY DEFAULT uuidv7(),
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
GRANT SELECT, INSERT, UPDATE ON internal_transfers TO templ_app;

-- +goose Down
DROP TABLE internal_transfers;
DROP TABLE withdrawal_addresses;
DROP TABLE withdrawals;
DROP FUNCTION withdrawals_validate();
