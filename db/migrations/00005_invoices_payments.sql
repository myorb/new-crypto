-- +goose Up
-- =============================================================================
--  Baseline 5/9 · merchant payments (inbound)
--
--  An invoice is priced once (usually in fiat) and can be paid with any of the
--  merchant's accepted assets. Each acceptable way to pay is a payment option
--  with its own amount, rate and deposit address; the payer picks one.
-- =============================================================================

CREATE TABLE invoices (
    id                 UUID            PRIMARY KEY DEFAULT uuidv7(),
    organization_id    UUID            NOT NULL REFERENCES organizations(id),
    external_id        TEXT,                                 -- merchant's own order id
    price_currency     TEXT            NOT NULL,             -- 'USD', 'EUR' or an asset code
    price_amount       NUMERIC(38, 18) NOT NULL CHECK (price_amount > 0),
    status             invoice_status  NOT NULL DEFAULT 'new',
    description        TEXT,
    customer_email     CITEXT,
    callback_url       TEXT,                                 -- per-invoice override; the app enforces https + SSRF rules
    return_url         TEXT,
    metadata           JSONB           NOT NULL DEFAULT '{}'::jsonb,
    expires_at         TIMESTAMPTZ     NOT NULL,
    paid_at            TIMESTAMPTZ,
    confirmed_at       TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    created_by_api_key UUID            REFERENCES api_keys(id),
    created_at         TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ     NOT NULL DEFAULT now(),
    UNIQUE (organization_id, external_id),
    CONSTRAINT invoices_currency_format CHECK (price_currency ~ '^[A-Z][A-Z0-9_]{1,31}$'),
    CONSTRAINT invoices_expiry          CHECK (expires_at > created_at)
);
CREATE TRIGGER trg_invoices_updated BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX invoices_org_created_idx ON invoices(organization_id, created_at DESC);
CREATE INDEX invoices_expiry_idx      ON invoices(expires_at) WHERE status IN ('new', 'partial');
GRANT SELECT, INSERT, UPDATE ON invoices TO templ_app;

CREATE TABLE invoice_payment_options (
    id                UUID            PRIMARY KEY DEFAULT uuidv7(),
    invoice_id        UUID            NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    asset_id          SMALLINT        NOT NULL REFERENCES assets(id),
    provider_id       SMALLINT        NOT NULL REFERENCES payment_providers(id),
    address_id        UUID            REFERENCES addresses(id),       -- allocated when option is selected
    memo              TEXT,
    amount_due        crypto_amount   NOT NULL CHECK (amount_due > 0),
    amount_paid       crypto_amount   NOT NULL DEFAULT 0,
    exchange_rate     NUMERIC(38, 18),                                -- price_currency per 1 asset, as quoted to the payer
    source_rate       NUMERIC(38, 18),                                -- mid-market rate the quote was derived from
    spread_bps        INT,                                            -- markup taken between source_rate and exchange_rate
    rate_id           BIGINT          REFERENCES exchange_rates(id),
    rate_locked_until TIMESTAMPTZ,
    is_selected       BOOLEAN         NOT NULL DEFAULT FALSE,
    selected_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ     NOT NULL DEFAULT now(),
    UNIQUE (invoice_id, asset_id),
    CONSTRAINT invoice_payment_options_paid CHECK (amount_paid >= 0),
    CONSTRAINT invoice_payment_options_rate CHECK (exchange_rate IS NULL OR exchange_rate > 0),
    CONSTRAINT invoice_payment_options_source_rate CHECK (source_rate IS NULL OR source_rate > 0),
    CONSTRAINT invoice_payment_options_spread CHECK (spread_bps IS NULL OR spread_bps BETWEEN 0 AND 10000),
    CONSTRAINT invoice_payment_options_memo CHECK (memo IS NULL OR memo <> '')
);
CREATE UNIQUE INDEX invoice_payment_options_selected_uq ON invoice_payment_options(invoice_id) WHERE is_selected;
CREATE INDEX invoice_payment_options_address_idx ON invoice_payment_options(address_id) WHERE address_id IS NOT NULL;
GRANT SELECT, INSERT, UPDATE ON invoice_payment_options TO templ_app;

-- A payment is one inbound transfer to a merchant deposit address.
-- option_id / invoice_id are NULL for unexpected deposits.
-- transfer_id is UNIQUE: one on-chain transfer can only ever become one payment.
CREATE TABLE payments (
    id              UUID           PRIMARY KEY DEFAULT uuidv7(),
    organization_id UUID           NOT NULL REFERENCES organizations(id),
    invoice_id      UUID           REFERENCES invoices(id),
    option_id       UUID           REFERENCES invoice_payment_options(id),
    address_id      UUID           NOT NULL REFERENCES addresses(id),
    asset_id        SMALLINT       NOT NULL REFERENCES assets(id),
    provider_id     SMALLINT       NOT NULL REFERENCES payment_providers(id),
    transfer_id     UUID           NOT NULL UNIQUE REFERENCES transfers(id),
    amount          crypto_amount  NOT NULL CHECK (amount > 0),
    -- What the platform keeps, snapshotted at pricing time so a later schedule
    -- change never rewrites history. fee_schedule_id is added in 00007.
    -- Merchant credit = amount - fee_amount - spread_amount - network_fee_amount.
    fee_amount         crypto_amount  NOT NULL DEFAULT 0,             -- percentage + fixed part
    fee_bps_applied    INT,                                           -- the percentage that produced fee_amount
    spread_amount      crypto_amount  NOT NULL DEFAULT 0,             -- amount minus its value at source_rate
    network_fee_amount crypto_amount  NOT NULL DEFAULT 0,             -- flat charge covering activation + sweep
    status          payment_status NOT NULL DEFAULT 'detected',
    detected_at     TIMESTAMPTZ    NOT NULL DEFAULT now(),
    confirmed_at    TIMESTAMPTZ,
    credited_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ    NOT NULL DEFAULT now(),
    CONSTRAINT payments_fee CHECK (
        fee_amount >= 0 AND spread_amount >= 0 AND network_fee_amount >= 0
        AND fee_amount + spread_amount + network_fee_amount <= amount
    ),
    CONSTRAINT payments_fee_bps CHECK (fee_bps_applied IS NULL OR fee_bps_applied BETWEEN 0 AND 10000)
);
CREATE TRIGGER trg_payments_updated BEFORE UPDATE ON payments FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX payments_invoice_idx ON payments(invoice_id);
CREATE INDEX payments_org_idx     ON payments(organization_id, created_at DESC);
GRANT SELECT, INSERT, UPDATE ON payments TO templ_app;

-- +goose Down
DROP TABLE payments;
DROP TABLE invoice_payment_options;
DROP TABLE invoices;
