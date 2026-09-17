-- Ledger reads. Postings (journals + entries) are written inside one
-- transaction by the app; the database checks that they balance at COMMIT.

-- name: GetMerchantBalances :many
SELECT * FROM merchant_balances
WHERE organization_id = $1
ORDER BY asset_code;

-- name: GetLedgerAccount :one
SELECT * FROM ledger_accounts
WHERE organization_id IS NOT DISTINCT FROM sqlc.narg('organization_id')
  AND asset_id = $1
  AND type = $2;

-- name: CreateLedgerJournal :one
INSERT INTO ledger_journals (event_type, reference_type, reference_id, description)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreateLedgerEntry :one
INSERT INTO ledger_entries (journal_id, account_id, amount)
VALUES ($1, $2, $3)
RETURNING *;
