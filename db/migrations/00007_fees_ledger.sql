-- +goose Up
-- =============================================================================
--  Baseline 7/9 · fees and the double-entry ledger
--
--  Ledger rules enforced by the database, not the app:
--   * a journal's entries must sum to zero (deferred constraint trigger)
--   * journals and entries are append-only (UPDATE / DELETE / TRUNCATE rejected)
--   * ledger_balances is derived: only the SECURITY DEFINER trigger writes it,
--     the app role can merely read it
-- =============================================================================

-- Resolution order: (org, asset) → (org, NULL asset) → (NULL org, asset) → (NULL, NULL).
CREATE TABLE fee_schedules (
    id                   UUID          PRIMARY KEY DEFAULT uuidv7(),
    organization_id      UUID          REFERENCES organizations(id) ON DELETE CASCADE,
    asset_id             SMALLINT      REFERENCES assets(id),
    deposit_fee_bps      INT           NOT NULL DEFAULT 0 CHECK (deposit_fee_bps BETWEEN 0 AND 10000),
    deposit_fee_fixed    crypto_amount NOT NULL DEFAULT 0,
    withdrawal_fee_bps   INT           NOT NULL DEFAULT 0 CHECK (withdrawal_fee_bps BETWEEN 0 AND 10000),
    withdrawal_fee_fixed crypto_amount NOT NULL DEFAULT 0,
    pass_network_fee     BOOLEAN       NOT NULL DEFAULT TRUE,   -- charge merchant the actual network fee on payouts
    effective_from       TIMESTAMPTZ   NOT NULL DEFAULT now(),
    effective_to         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ   NOT NULL DEFAULT now(),
    CONSTRAINT fee_schedules_fixed  CHECK (deposit_fee_fixed >= 0 AND withdrawal_fee_fixed >= 0),
    CONSTRAINT fee_schedules_window CHECK (effective_to IS NULL OR effective_to > effective_from)
);
CREATE UNIQUE INDEX fee_schedules_active_uq
    ON fee_schedules(COALESCE(organization_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(asset_id, 0))
    WHERE effective_to IS NULL;
GRANT SELECT, INSERT, UPDATE ON fee_schedules TO templ_app;   -- close a schedule via effective_to

-- -----------------------------------------------------------------------------
-- Ledger
-- -----------------------------------------------------------------------------
CREATE TABLE ledger_accounts (
    id              UUID                PRIMARY KEY DEFAULT uuidv7(),
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
GRANT SELECT, INSERT ON ledger_accounts TO templ_app;        -- created lazily per (org, asset); never changed

CREATE TABLE ledger_journals (
    id             UUID        PRIMARY KEY DEFAULT uuidv7(),
    event_type     TEXT        NOT NULL,                     -- 'payment.credited', 'withdrawal.completed', ...
    reference_type TEXT        NOT NULL,                     -- 'payment', 'withdrawal', 'internal_transfer'
    reference_id   UUID        NOT NULL,
    description    TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (event_type, reference_type, reference_id)        -- posting the same business event twice is impossible
);
CREATE TRIGGER trg_ledger_journals_immutable   BEFORE UPDATE OR DELETE ON ledger_journals FOR EACH ROW       EXECUTE FUNCTION forbid_change();
CREATE TRIGGER trg_ledger_journals_no_truncate BEFORE TRUNCATE         ON ledger_journals FOR EACH STATEMENT EXECUTE FUNCTION forbid_change();
GRANT SELECT, INSERT ON ledger_journals TO templ_app;

CREATE TABLE ledger_entries (
    id         BIGINT        GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    journal_id UUID          NOT NULL REFERENCES ledger_journals(id),   -- RESTRICT: journals are never deleted
    account_id UUID          NOT NULL REFERENCES ledger_accounts(id),
    amount     crypto_amount NOT NULL CHECK (amount <> 0),   -- signed: debit < 0 < credit
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX ledger_entries_account_idx ON ledger_entries(account_id, id);
CREATE INDEX ledger_entries_journal_idx ON ledger_entries(journal_id);
GRANT SELECT, INSERT ON ledger_entries TO templ_app;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ledger_journal_must_balance() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE
    v_sum NUMERIC;
BEGIN
    SELECT COALESCE(SUM(amount), 0) INTO v_sum FROM ledger_entries WHERE journal_id = NEW.journal_id;
    IF v_sum <> 0 THEN
        RAISE EXCEPTION 'ledger journal % is unbalanced (sum = %)', NEW.journal_id, v_sum;
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER trg_ledger_entries_balance
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_journal_must_balance();

CREATE TABLE ledger_balances (
    account_id UUID          PRIMARY KEY REFERENCES ledger_accounts(id),
    balance    crypto_amount NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);
GRANT SELECT ON ledger_balances TO templ_app;                -- derived; written only by the trigger below

-- SECURITY DEFINER: runs with the migrator's rights so the app can post entries
-- without holding write access to ledger_balances. search_path is pinned, as
-- required for any SECURITY DEFINER function.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ledger_apply_entry() RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
    INSERT INTO ledger_balances (account_id, balance, updated_at)
    VALUES (NEW.account_id, NEW.amount, now())
    ON CONFLICT (account_id) DO UPDATE
        SET balance = ledger_balances.balance + EXCLUDED.balance, updated_at = now();
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_ledger_entries_apply       AFTER INSERT             ON ledger_entries FOR EACH ROW       EXECUTE FUNCTION ledger_apply_entry();
CREATE TRIGGER trg_ledger_entries_immutable   BEFORE UPDATE OR DELETE  ON ledger_entries FOR EACH ROW       EXECUTE FUNCTION forbid_change();
CREATE TRIGGER trg_ledger_entries_no_truncate BEFORE TRUNCATE          ON ledger_entries FOR EACH STATEMENT EXECUTE FUNCTION forbid_change();

-- security_invoker: the view is evaluated with the caller's privileges, so it
-- can never leak more than the underlying grants (and future RLS) allow.
CREATE VIEW merchant_balances WITH (security_invoker = true) AS
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
GRANT SELECT ON merchant_balances TO templ_app;

-- +goose Down
DROP VIEW merchant_balances;
DROP TABLE ledger_entries;
DROP FUNCTION ledger_apply_entry();
DROP FUNCTION ledger_journal_must_balance();
DROP TABLE ledger_balances;
DROP TABLE ledger_journals;
DROP TABLE ledger_accounts;
DROP TABLE fee_schedules;
