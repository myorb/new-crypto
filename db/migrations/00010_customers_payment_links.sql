-- +goose Up
-- =============================================================================
--  00010 · customers and payment links
--
--  A customer is the merchant's payer record: one row per (organization,
--  email) or per merchant-supplied external_id. Invoices point at it so the
--  dashboard can group activity per payer and the merchant can block one.
--
--  A payment link is a reusable checkout template: opening its public URL
--  (/l/<slug>) creates an invoice priced from the link. Single-use links carry
--  max_uses = 1 and complete once that invoice is paid.
-- =============================================================================

CREATE TYPE customer_status     AS ENUM ('active', 'blocked');
CREATE TYPE payment_link_status AS ENUM ('active', 'paused', 'completed', 'expired', 'archived');

-- -----------------------------------------------------------------------------
-- Customers
-- -----------------------------------------------------------------------------
CREATE TABLE customers (
    id              UUID            PRIMARY KEY DEFAULT uuidv7(),
    organization_id UUID            NOT NULL REFERENCES organizations(id),
    external_id     TEXT,                                   -- merchant's own customer id
    email           CITEXT,
    name            TEXT,
    country_code    CHAR(2),                                -- ISO 3166-1 alpha-2
    status          customer_status NOT NULL DEFAULT 'active',
    blocked_at      TIMESTAMPTZ,
    blocked_reason  TEXT,
    metadata        JSONB           NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    UNIQUE (organization_id, external_id),
    UNIQUE (organization_id, email),
    CONSTRAINT customers_identity       CHECK (email IS NOT NULL OR external_id IS NOT NULL),
    CONSTRAINT customers_email_format   CHECK (email IS NULL OR email ~ '^[^@[:space:]]+@[^@[:space:]]+$'),
    CONSTRAINT customers_external_id    CHECK (external_id IS NULL OR external_id <> ''),
    CONSTRAINT customers_name           CHECK (name IS NULL OR name <> ''),
    CONSTRAINT customers_country_format CHECK (country_code IS NULL OR country_code ~ '^[A-Z]{2}$'),
    CONSTRAINT customers_blocked        CHECK ((status = 'blocked') = (blocked_at IS NOT NULL))
);
CREATE TRIGGER trg_customers_updated BEFORE UPDATE ON customers FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX customers_org_created_idx ON customers(organization_id, created_at DESC);
GRANT SELECT, INSERT, UPDATE ON customers TO templ_app;   -- no DELETE: invoices refer to customers; block / anonymise instead

-- -----------------------------------------------------------------------------
-- Payment links
-- -----------------------------------------------------------------------------
CREATE TABLE payment_links (
    id                  UUID                PRIMARY KEY DEFAULT uuidv7(),
    organization_id     UUID                NOT NULL REFERENCES organizations(id),
    slug                TEXT                NOT NULL UNIQUE,              -- public token in the URL: /l/<slug>
    name                TEXT                NOT NULL,
    description         TEXT,
    price_currency      TEXT                NOT NULL,                     -- 'USD', 'EUR' or an asset code
    price_amount        NUMERIC(38, 18),                                  -- NULL = payer chooses the amount
    min_amount          NUMERIC(38, 18),                                  -- bounds for payer-chosen amounts
    max_amount          NUMERIC(38, 18),
    status              payment_link_status NOT NULL DEFAULT 'active',
    max_uses            INT,                                              -- NULL = unlimited; 1 = single-use
    uses_count          INT                 NOT NULL DEFAULT 0,           -- invoices paid through this link
    view_count          BIGINT              NOT NULL DEFAULT 0,
    invoice_ttl_seconds INT                 NOT NULL DEFAULT 3600,        -- lifetime of invoices opened from the link
    collect_email       BOOLEAN             NOT NULL DEFAULT TRUE,        -- hosted page asks the payer for an email
    customer_id         UUID                REFERENCES customers(id),     -- link addressed to one payer
    callback_url        TEXT,                                             -- copied onto each invoice; app enforces https + SSRF rules
    return_url          TEXT,
    metadata            JSONB               NOT NULL DEFAULT '{}'::jsonb,
    expires_at          TIMESTAMPTZ,                                      -- NULL = never
    last_viewed_at      TIMESTAMPTZ,
    created_by          UUID                REFERENCES users(id),
    created_by_api_key  UUID                REFERENCES api_keys(id),
    created_at          TIMESTAMPTZ         NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ         NOT NULL DEFAULT now(),
    CONSTRAINT payment_links_slug_format     CHECK (slug ~ '^[A-Za-z0-9_-]{6,64}$'),
    CONSTRAINT payment_links_name            CHECK (name <> ''),
    CONSTRAINT payment_links_currency_format CHECK (price_currency ~ '^[A-Z][A-Z0-9_]{1,31}$'),
    CONSTRAINT payment_links_price           CHECK (price_amount IS NULL OR price_amount > 0),
    CONSTRAINT payment_links_bounds          CHECK (
        (min_amount IS NULL OR min_amount > 0)
        AND (max_amount IS NULL OR max_amount > 0)
        AND (min_amount IS NULL OR max_amount IS NULL OR min_amount <= max_amount)
        AND (price_amount IS NULL OR (min_amount IS NULL AND max_amount IS NULL))
    ),
    CONSTRAINT payment_links_uses            CHECK (uses_count >= 0 AND (max_uses IS NULL OR max_uses > 0)),
    CONSTRAINT payment_links_views           CHECK (view_count >= 0),
    CONSTRAINT payment_links_ttl             CHECK (invoice_ttl_seconds BETWEEN 60 AND 604800),
    CONSTRAINT payment_links_expiry          CHECK (expires_at IS NULL OR expires_at > created_at)
);
CREATE TRIGGER trg_payment_links_updated BEFORE UPDATE ON payment_links FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX payment_links_org_created_idx ON payment_links(organization_id, created_at DESC);
CREATE INDEX payment_links_expiry_idx      ON payment_links(expires_at) WHERE status IN ('active', 'paused') AND expires_at IS NOT NULL;
CREATE INDEX payment_links_customer_idx    ON payment_links(customer_id) WHERE customer_id IS NOT NULL;
GRANT SELECT, INSERT, UPDATE ON payment_links TO templ_app;   -- no DELETE: invoices refer to links; status = 'archived'

-- Assets a link may be paid in. No rows = every asset the merchant has enabled.
CREATE TABLE payment_link_assets (
    payment_link_id UUID     NOT NULL REFERENCES payment_links(id) ON DELETE CASCADE,
    asset_id        SMALLINT NOT NULL REFERENCES assets(id),
    PRIMARY KEY (payment_link_id, asset_id)
);
GRANT SELECT, INSERT, DELETE ON payment_link_assets TO templ_app;   -- configuration, not money

-- -----------------------------------------------------------------------------
-- Invoices point at their payer and at the link that produced them
-- -----------------------------------------------------------------------------
ALTER TABLE invoices
    ADD COLUMN customer_id     UUID REFERENCES customers(id),
    ADD COLUMN payment_link_id UUID REFERENCES payment_links(id);
CREATE INDEX invoices_customer_idx     ON invoices(customer_id, created_at DESC)     WHERE customer_id IS NOT NULL;
CREATE INDEX invoices_payment_link_idx ON invoices(payment_link_id, created_at DESC) WHERE payment_link_id IS NOT NULL;

-- Backfill: every payer email already seen on an invoice becomes a customer.
INSERT INTO customers (organization_id, email, created_at)
SELECT organization_id, customer_email, min(created_at)
FROM invoices
WHERE customer_email IS NOT NULL
GROUP BY organization_id, customer_email;

UPDATE invoices i
SET customer_id = c.id
FROM customers c
WHERE c.organization_id = i.organization_id
  AND c.email = i.customer_email
  AND i.customer_id IS NULL;

-- +goose Down
DROP INDEX invoices_payment_link_idx;
DROP INDEX invoices_customer_idx;
ALTER TABLE invoices DROP COLUMN payment_link_id, DROP COLUMN customer_id;
DROP TABLE payment_link_assets;
DROP TABLE payment_links;
DROP TABLE customers;
DROP TYPE payment_link_status;
DROP TYPE customer_status;
