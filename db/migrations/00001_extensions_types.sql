-- +goose Up
-- =============================================================================
--  Crypto Payment Provider — PostgreSQL schema
--  Baseline 1/9 · prerequisites, extension, domain, enums, shared helpers
-- =============================================================================
--  Conventions (see db/README.md)
--   * PKs are UUIDs from uuidv7() (time-ordered, index-friendly). Reference
--     tables use SMALLINT identity columns; append-only logs use BIGINT identity.
--   * All timestamps are TIMESTAMPTZ.
--   * Money is crypto_amount = NUMERIC(38,18) in human units (1.5 = 1.5 USDT);
--     the asset row carries `decimals` for conversion to on-chain base units.
--   * Chain-specific formats (addresses, tx hashes, memos) are validated by
--     rules stored on the `networks` row and enforced with triggers, so adding
--     a chain is a data change, not a schema change.
--   * Rows are never hard-deleted where money is involved; use status columns.
--     The runtime role (templ_app) has no DELETE on those tables.
--   * Default deny: every table declares its GRANTs right after its definition.
--     A table without a GRANT is invisible to the app.
--   * Roles are created by db/roles.sql before the first migration, never here.
-- =============================================================================

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'templ_app') THEN
        RAISE EXCEPTION 'role "templ_app" does not exist - run db/roles.sql first (see db/README.md)';
    END IF;
END $$;
-- +goose StatementEnd

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
CREATE TYPE fee_payer                AS ENUM ('customer', 'merchant', 'platform');   -- who bears a flat network fee
CREATE TYPE webhook_delivery_status  AS ENUM ('pending', 'succeeded', 'failed', 'exhausted');

-- -----------------------------------------------------------------------------
-- Helpers
-- -----------------------------------------------------------------------------
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END $$;
-- +goose StatementEnd

-- Attached as BEFORE UPDATE/DELETE (row) and BEFORE TRUNCATE (statement) to
-- append-only tables: audit_logs, ledger_journals, ledger_entries.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION forbid_change() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% rows are immutable (% rejected)', TG_TABLE_NAME, TG_OP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION forbid_change();
DROP FUNCTION set_updated_at();
DROP TYPE webhook_delivery_status;
DROP TYPE fee_payer;
DROP TYPE ledger_account_type;
DROP TYPE internal_transfer_status;
DROP TYPE internal_transfer_kind;
DROP TYPE withdrawal_status;
DROP TYPE transfer_direction;
DROP TYPE tx_status;
DROP TYPE payment_status;
DROP TYPE invoice_status;
DROP TYPE wallet_kind;
DROP TYPE asset_kind;
DROP TYPE network_family;
DROP TYPE provider_kind;
DROP TYPE auth_challenge_purpose;
DROP TYPE mfa_method_type;
DROP TYPE auth_provider;
DROP TYPE organization_role;
DROP TYPE organization_status;
DROP TYPE user_status;
DROP DOMAIN crypto_amount;
DROP EXTENSION IF EXISTS citext;
